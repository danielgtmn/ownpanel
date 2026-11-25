package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/danielgtmn/ownpanel/internal/mail/account"
	"github.com/danielgtmn/ownpanel/internal/mail/server"
)

// runMail runs the mail service (ownpanel-mail.service) until SIGTERM or
// SIGINT. It logs no message contents, password hashes, keys or
// configurations.
func runMail(args []string) error {
	fs := flag.NewFlagSet("mail", flag.ExitOnError)
	dataDir := fs.String("data", "/var/lib/ownpanel-mail", "data directory: configuration, mail index and messages")
	admin := fs.String("admin", "/run/ownpanel-mail/admin.sock", "Unix socket the panel configures the service through")
	webSock := fs.String("web", "", "Unix socket for the mail server name's web requests from the web server, as unix:///absolute/path.sock (\"\" to disable)")
	webGroup := fs.String("web-group", "", "group to hand the web socket and its directory to, so only the web server can connect (the service must be a member)")
	smtpAddr := fs.String("smtp", ":25", "address for incoming mail from other servers")
	submission := fs.String("submission", ":587", "address for mail clients sending mail, STARTTLS required before sign-in (\"\" to disable)")
	submissions := fs.String("submissions", ":465", "address for mail clients sending mail over TLS (\"\" to disable)")
	imaps := fs.String("imaps", ":993", "address for mail clients reading mail with IMAP over TLS (\"\" to disable)")
	manageSieve := fs.String("managesieve", ":4190", "address for mail programs managing their mail filters (ManageSieve, STARTTLS required before sign-in; \"\" to disable)")
	ipv6Out := fs.Bool("ipv6-outbound", false, "also deliver outgoing mail over IPv6 (only if the server's IPv6 address has a reverse DNS entry naming the mail host)")
	allowPrivateMX := fs.Bool("allow-private-mx", false, "for test setups only: deliver outgoing mail to mail servers with loopback or private network addresses")
	fs.Parse(args)

	var webPath string
	if *webSock != "" {
		path, ok := strings.CutPrefix(*webSock, "unix://")
		if !ok || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("-web: %q must be unix:///absolute/path/to.sock", *webSock)
		}
		webPath = path
	}

	// The self-service API needs the server, the server needs the handler
	// before it opens the web socket (in Run): it is set right after New.
	var acct *account.Handler
	srv, err := server.New(server.Options{
		DataDir:         *dataDir,
		AdminSocket:     *admin,
		WebSocket:       webPath,
		WebGroup:        *webGroup,
		SMTPAddr:        *smtpAddr,
		SubmissionAddr:  *submission,
		SubmissionsAddr: *submissions,
		IMAPSAddr:       *imaps,
		ManageSieveAddr: *manageSieve,
		IPv6Outbound:    *ipv6Out,
		AllowPrivateMX:  *allowPrivateMX,
		AccountHandler:  http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { acct.ServeHTTP(w, r) }),
		Logger:          slog.Default(),
	})
	if err != nil {
		return err
	}
	acct = account.New(account.Options{Backend: srv, Logger: slog.Default()})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if webPath != "" {
		go acct.Run(ctx)
	}
	slog.Info("mail service starting", "version", version, "data", *dataDir, "admin", *admin, "web", webPath)
	if err := srv.Run(ctx); err != nil {
		return err
	}
	slog.Info("mail service stopped")
	return nil
}
