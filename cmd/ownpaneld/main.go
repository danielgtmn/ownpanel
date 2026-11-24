// ownpaneld is the panel daemon: it keeps the desired state of all sites and
// reconciles a separately running Caddy to it through Caddy's admin API.
//
//	ownpaneld [serve] [flags]                    run the daemon
//	ownpaneld backup -data <dir> -out <file>     write a backup archive
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/danielgtmn/ownpanel/internal/api"
	"github.com/danielgtmn/ownpanel/internal/backup"
	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/deploy"
	"github.com/danielgtmn/ownpanel/internal/mcp"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/ownpanel/internal/secrets"
	"github.com/danielgtmn/ownpanel/internal/store"
	"github.com/danielgtmn/ownpanel/internal/webui"
)

// version is reported to MCP clients; release builds can set it with
// -ldflags "-X main.version=...".
var version = "dev"

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(args)
	case "backup":
		err = runBackup(args)
	default:
		err = fmt.Errorf("unknown command %q (use serve or backup)", cmd)
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dataDir := fs.String("data", "./data", "data directory of the daemon")
	out := fs.String("out", "", "archive to write (.tar.gz); must not exist")
	fs.Parse(args)
	if *out == "" {
		return errors.New("backup needs -out <file.tar.gz>")
	}
	if err := backup.Create(*dataDir, *out); err != nil {
		return err
	}
	slog.Info("backup written", "file", *out)
	return nil
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var (
		listen      = fs.String("listen", "127.0.0.1:8600", "address of the panel (web interface and API)")
		dataDir     = fs.String("data", "./data", "directory for the database, site files and certificates")
		caddyAdmin  = fs.String("caddy-admin", "http://127.0.0.1:2019", "Caddy's admin API: http://host:port or unix:///path/to.sock")
		tlsMode     = fs.String("tls", caddy.TLSACME, "certificates: acme (public), internal (self-signed, for testing) or off")
		httpPort    = fs.Int("http-port", 80, "port for HTTP")
		httpsPort   = fs.Int("https-port", 443, "port for HTTPS")
		acmeEmail   = fs.String("acme-email", "", "contact address given to the certificate authority (optional)")
		panelDomain = fs.String("panel-domain", "", "serve the panel itself through Caddy on this domain (optional)")
		interval    = fs.Duration("reconcile-interval", 30*time.Second, "how often the web server is re-synced in the background")
	)
	var serverIPFlags listFlag
	fs.Var(&serverIPFlags, "server-ip", "public IPv4/IPv6 address of this server that domains should point at (repeatable or comma-separated)")
	fs.Parse(args)

	data, err := filepath.Abs(*dataDir)
	if err != nil {
		return err
	}
	admin, err := caddy.ParseAdmin(*caddyAdmin)
	if err != nil {
		return err
	}
	opts := caddy.Options{
		AdminListen: admin.Listen(),
		StorageDir:  filepath.Join(data, "caddy"),
		TLSMode:     *tlsMode,
		HTTPPort:    *httpPort,
		HTTPSPort:   *httpsPort,
		ACMEEmail:   *acmeEmail,
	}
	if *panelDomain != "" {
		d, err := store.NormalizeDomain(*panelDomain)
		if err != nil {
			return fmt.Errorf("-panel-domain: %w", err)
		}
		opts.PanelDomain, opts.PanelUpstream = d, dialAddress(*listen)
	}

	serverIPs, err := panel.ParseServerIPs(serverIPFlags, *tlsMode == caddy.TLSInternal || *tlsMode == caddy.TLSOff)
	if err != nil {
		return fmt.Errorf("-server-ip: %w", err)
	}

	if err := os.MkdirAll(data, 0o755); err != nil {
		return err
	}
	secretBox, err := secrets.Open(filepath.Join(data, secrets.KeyFile))
	if err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(data, backup.DBFile))
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if n, err := st.ImportStateFile(ctx, filepath.Join(data, "state.json")); err != nil {
		return err
	} else if n > 0 {
		slog.Info("imported sites from state.json", "sites", n)
	}

	setupFile := filepath.Join(data, "setup-code")
	svc, err := panel.New(panel.Config{
		Store:         st,
		Deploys:       &deploy.Manager{Base: filepath.Join(data, "sites")},
		Web:           caddy.NewClient(admin),
		Caddy:         opts,
		SetupCodeFile: setupFile,
		Secrets:       secretBox,
		ServerIPs:     serverIPs,
	})
	if err != nil {
		return err
	}
	if needed, err := svc.EnsureSetupCode(ctx); err != nil {
		return err
	} else if needed {
		slog.Info("no user yet: open the panel and create the first admin with the setup code from " + setupFile)
	}

	// Initial sync, then heal drift (e.g. Caddy restarted).
	go svc.Run(ctx, *interval)

	var mcpOpts []mcp.Option
	if opts.PanelDomain != "" {
		mcpOpts = append(mcpOpts, mcp.WithPanelDomain(opts.PanelDomain))
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           compose((&api.Server{Panel: svc}).Handler(), webui.Handler(webui.FS()), mcp.Handler(svc, version, mcpOpts...)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	slog.Info("ownpaneld started", "listen", *listen, "data", data, "tls", *tlsMode, "panel_domain", opts.PanelDomain)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// dialAddress turns a listen address into one Caddy can connect to: a
// wildcard host becomes loopback, which also makes ownpaneld trust the
// X-Forwarded-* headers Caddy sets.
func dialAddress(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
