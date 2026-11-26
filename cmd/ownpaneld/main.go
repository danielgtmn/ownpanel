// ownpaneld is the panel daemon: it keeps the desired state of all sites and
// reconciles a separately running Caddy to it through Caddy's admin API.
//
//	ownpaneld [serve] [flags]                    run the daemon
//	ownpaneld backup -data <dir> -out <file>     write a backup archive (with -mail-data, -no-mail)
//	ownpaneld mail [flags]                       run the mail service
//	ownpaneld build-worker [flags]               run the build service
//	ownpaneld version                            print the version
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
	"github.com/danielgtmn/ownpanel/internal/build"
	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/deploy"
	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/gitsource"
	mailconfig "github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mcp"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/ownpanel/internal/store"
	"github.com/danielgtmn/ownpanel/internal/timeouts"
	"github.com/danielgtmn/ownpanel/internal/webui"
	"github.com/danielgtmn/panzerschrank"
)

// version is reported to MCP clients and by the version command; release
// builds set it with -ldflags "-X main.version=..." (scripts/package.sh).
var version = "dev"

// repo is the GitHub repository the panel looks for new releases in; release
// builds set it with -ldflags "-X main.repo=..." (scripts/package.sh).
var repo = "danielgtmn/ownpanel"

// releaseFile is where install.sh records the installed release and channel.
// The panel only offers updates when it exists.
const releaseFile = "/etc/ownpanel/release"

// defaultCaddyStorage is where install.sh lets Caddy keep its certificates.
const defaultCaddyStorage = "/var/lib/ownpanel-caddy/storage"

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
	case backupMailCommand:
		err = runBackupMail(args)
	case backupDBCommand:
		err = runBackupDB(args)
	case "mail":
		err = runMail(args)
	case "build-worker":
		err = runBuildWorker(args)
	case "version":
		fmt.Println(version)
	default:
		err = fmt.Errorf("unknown command %q (use serve, backup, mail, build-worker or version)", cmd)
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dataDir := fs.String("data", "./data", "data directory of the daemon")
	mailData := fs.String("mail-data", "/var/lib/ownpanel-mail", "data directory of the mail service (skipped if it does not exist)")
	caddyStorage := fs.String("caddy-storage", defaultCaddyStorage, "Caddy's certificate storage; if it does not exist, caddy in the data directory")
	noMail := fs.Bool("no-mail", false, "leave the stored mail out of the archive")
	out := fs.String("out", "", "archive to write (.tar.gz); must not exist")
	fs.Parse(args)
	if *out == "" {
		return errors.New("backup needs -out <file.tar.gz>")
	}
	src := backup.Sources{}
	if fi, err := os.Lstat(*caddyStorage); err == nil && fi.IsDir() {
		src.CaddyStorage = *caddyStorage
	}
	var err error
	if src.DB, err = dbSource(*dataDir); err != nil {
		return err
	}
	if !*noMail {
		if src.Mail, err = mailSource(*mailData); err != nil {
			return err
		}
	}
	if err := backup.CreateFrom(*dataDir, *out, src); err != nil {
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
		storage     = fs.String("caddy-storage", "", "where Caddy keeps certificates (default: caddy in the data directory)")
		updateDir   = fs.String("update-dir", "", "where update requests go to the root-side updater and its status comes back (default: update in the data directory)")
		caddyAdmin  = fs.String("caddy-admin", "http://127.0.0.1:2019", "Caddy's admin API: http://host:port or unix:///path/to.sock")
		tlsMode     = fs.String("tls", caddy.TLSACME, "certificates: acme (public), internal (self-signed, for testing) or off")
		httpPort    = fs.Int("http-port", 80, "port for HTTP")
		httpsPort   = fs.Int("https-port", 443, "port for HTTPS")
		acmeEmail   = fs.String("acme-email", "", "contact address given to the certificate authority (optional; overrides the panel settings)")
		panelDomain = fs.String("panel-domain", "", "serve the panel itself through Caddy on this domain (optional; overrides the panel settings)")
		interval    = fs.Duration("reconcile-interval", 30*time.Second, "how often the web server is re-synced in the background")
		releasesAPI = fs.String("releases-api", "https://api.github.com", "GitHub API address used to look for new releases (change it only for testing)")
		mailAdmin   = fs.String("mail-admin", "", "admin socket of the mail service as unix:///absolute/path.sock (optional; without it e-mail is only stored, not served)")
		buildWorker = fs.String("build-worker", "", "socket of the build service as unix:///absolute/path.sock (optional; without it sites cannot be built on this server)")
		mailHost    = fs.String("mail-host", "", "name of the mail server, e.g. mail.example.com (optional; overrides the panel settings)")
		mailWeb     = fs.String("mail-web", "", "web socket of the mail service as unix:///absolute/path.sock (optional; without it mailbox users have no sign-in page on the mail server's name)")
		githubURL   = fs.String("github-url", github.DefaultURL, "address of GitHub for connecting repositories (change it only for testing; http only on a loopback address)")
		gitPrivate  = fs.Bool("git-private-hosts", false, "let repository addresses point at loopback and private networks (only for testing, with -tls internal or off: it lets anyone who may link a site reach the server's own network)")
	)
	var serverIPFlags listFlag
	fs.Var(&serverIPFlags, "server-ip", "public IPv4/IPv6 address of this server that domains should point at (repeatable or comma-separated; overrides the panel settings)")
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
		StorageDir:  orDefault(*storage, filepath.Join(data, "caddy")),
		TLSMode:     *tlsMode,
		HTTPPort:    *httpPort,
		HTTPSPort:   *httpsPort,
		ACMEEmail:   *acmeEmail,
		// The panel is always served through Caddy: on the server's IP
		// addresses until set up, and on its domain.
		PanelUpstream: dialAddress(*listen),
	}
	if *panelDomain != "" {
		d, err := store.NormalizeDomain(*panelDomain)
		if err != nil {
			return fmt.Errorf("-panel-domain: %w", err)
		}
		opts.PanelDomain = d
	}

	var mailService panel.MailService
	if *mailAdmin != "" {
		path, ok := strings.CutPrefix(*mailAdmin, "unix://")
		if !ok || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("-mail-admin: %q must be unix:///absolute/path/to.sock", *mailAdmin)
		}
		mailService = mailconfig.NewClient(path)
	}
	if *mailWeb != "" {
		path, ok := strings.CutPrefix(*mailWeb, "unix://")
		if !ok || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("-mail-web: %q must be unix:///absolute/path/to.sock", *mailWeb)
		}
		opts.MailWebSocket = path
	}
	var buildService panel.BuildService
	if *buildWorker != "" {
		path, ok := strings.CutPrefix(*buildWorker, "unix://")
		if !ok || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("-build-worker: %q must be unix:///absolute/path/to.sock", *buildWorker)
		}
		buildService = build.NewClient(path)
	}
	if *mailHost != "" {
		if _, err := store.NormalizeDomain(*mailHost); err != nil {
			return fmt.Errorf("-mail-host: %w", err)
		}
	}

	if *gitPrivate {
		if *tlsMode != caddy.TLSInternal && *tlsMode != caddy.TLSOff {
			return errors.New("-git-private-hosts is only for testing, with -tls internal or off")
		}
		slog.Warn("repository addresses may point at loopback and private networks (-git-private-hosts): never on a real server")
		gitsource.AllowPrivateHosts()
	}

	githubClient, err := github.New(*githubURL)
	if err != nil {
		return fmt.Errorf("-github-url: %w", err)
	}

	serverIPs, err := panel.ParseServerIPs(serverIPFlags, *tlsMode == caddy.TLSInternal || *tlsMode == caddy.TLSOff)
	if err != nil {
		return fmt.Errorf("-server-ip: %w", err)
	}

	if err := os.MkdirAll(data, 0o755); err != nil {
		return err
	}
	secretBox, err := panzerschrank.OpenKeyFile(filepath.Join(data, panzerschrank.KeyFile))
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
		Updates: panel.UpdatesConfig{
			Version:     version,
			Repo:        repo,
			APIURL:      *releasesAPI,
			ReleaseFile: releaseFile,
			Dir:         orDefault(*updateDir, filepath.Join(data, "update")),
		},
		Mail:            mailService,
		Builds:          buildService,
		BuildsStateFile: build.StateFile,
		MailHost:        *mailHost,
		MailTLSDir:      filepath.Join(data, "mail-tls"),
		GitHub:          githubClient,
	})
	if err != nil {
		return err
	}
	if needed, err := svc.EnsureSetupCode(ctx); err != nil {
		return err
	} else if needed {
		slog.Info("no user yet: open the panel and create the first admin with the setup code from " + setupFile)
	}

	// An update restarts the daemon: put its result in the activity log.
	go svc.WatchUpdateResult(ctx, 3*time.Second, 10*time.Minute)

	// Initial sync, then heal drift (e.g. Caddy restarted).
	go svc.Run(ctx, *interval)

	app := webui.FS()
	apiServer := &api.Server{Panel: svc, Build: webui.Build(app)}
	panelHandler := apiServer.MailSetupHandler(compose(apiServer.Handler(), webui.Handler(app), mcp.Handler(svc, version, mcp.WithPanelDomainFunc(svc.PanelDomain))))
	srv := timeouts.Server(*listen, svc.MailChallengeHandler(svc.MailHostHandler(webui.AccountHandler(app, svc.MailHost), panelHandler)))
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	slog.Info("ownpaneld started", "listen", *listen, "data", data, "tls", *tlsMode, "panel_domain", svc.PanelDomain(),
		"mail_service", *mailAdmin != "", "mail_host", svc.MailHost())
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// dialAddress turns a listen address into one Caddy can connect to: a
// wildcard host becomes loopback, which also makes ownpaneld trust the
// X-Forwarded-* headers Caddy sets.
// orDefault is path made absolute, or def if path is "".
func orDefault(path, def string) string {
	if path == "" {
		return def
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

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
