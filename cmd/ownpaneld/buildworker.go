package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/danielgtmn/ownpanel/internal/build"
	"github.com/danielgtmn/ownpanel/internal/build/worker"
)

// runBuildWorker runs the build service (ownpanel-build.service, as the
// user ownpanel-build) until SIGTERM or SIGINT. It never logs build
// commands, environment variables or what builds print.
func runBuildWorker(args []string) error {
	fs := flag.NewFlagSet("build-worker", flag.ExitOnError)
	socket := fs.String("socket", build.DefaultSocket, "Unix socket the panel talks to the build service through")
	work := fs.String("work", "/var/lib/ownpanel-build/work", "mount point of the build disk (set up by ownpanel-build-setup)")
	resolvers := fs.String("resolvers", "/run/ownpanel-build-setup/resolvers", "file with the name servers for builds (written by ownpanel-build-setup)")
	runDir := fs.String("run-dir", "/run/ownpanel-build/podman", "private runtime directory of the container runtime")
	slots := fs.Int("slots", 1, "how many builds run at once")
	workspace := fs.String("workspace-size", "4G", "disk space of one build (the quota ownpanel-build-setup sets)")
	cacheSize := fs.String("cache-size", "1G", "disk space of one site's build cache (the quota ownpanel-build-setup sets)")
	caches := fs.Int("caches", 100, "how many sites keep a build cache (the quotas ownpanel-build-setup sets)")
	fs.Parse(args)

	if os.Geteuid() == 0 {
		return errors.New("the build service must not run as root (see ownpanel-build.service)")
	}
	ws, err := parseSize(*workspace)
	if err != nil {
		return fmt.Errorf("-workspace-size: %w", err)
	}
	cs, err := parseSize(*cacheSize)
	if err != nil {
		return fmt.Errorf("-cache-size: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if !filepath.IsAbs(*runDir) {
		return errors.New("-run-dir must be an absolute path")
	}
	if err := os.MkdirAll(*runDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(*runDir, 0o700); err != nil {
		return err
	}
	rt := &worker.Podman{Home: home, RunDir: *runDir, Work: *work, Log: slog.Default()}
	w, err := worker.New(worker.Config{
		Work:           *work,
		Resolvers:      *resolvers,
		Slots:          *slots,
		WorkspaceBytes: ws,
		CacheBytes:     cs,
		Caches:         *caches,
		Runtime:        rt,
		Disk:           worker.QuotaDisk{},
		Guard:          worker.FilterGuard{},
		Logger:         slog.Default(),
	})
	if err != nil {
		return err
	}
	ln, err := worker.Listen(*socket)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.Info("build service starting", "version", version, "socket", *socket, "work", *work, "slots", *slots)
	err = worker.Serve(ctx, w, ln)
	os.Remove(*socket)
	slog.Info("build service stopped")
	return err
}

// parseSize reads a size like 4G, 512M or 1073741824 (binary units).
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	mult := int64(1)
	if n := len(s); n > 0 {
		switch strings.ToUpper(s[n-1:]) {
		case "K":
			mult = 1 << 10
		case "M":
			mult = 1 << 20
		case "G":
			mult = 1 << 30
		case "T":
			mult = 1 << 40
		}
		if mult > 1 {
			s = s[:n-1]
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 || v > (1<<62)/mult {
		return 0, fmt.Errorf("%q is not a size (e.g. 4G or 512M)", s)
	}
	return v * mult, nil
}
