//go:build !linux

package worker

import (
	"context"
	"errors"
	"log/slog"

	"github.com/danielgtmn/ownpanel/internal/build"
)

// On other systems builds are unavailable; the types exist so the daemon
// builds everywhere.

// Podman is only implemented on Linux.
type Podman struct {
	Bin, Home, RunDir, Work string
	Log                     *slog.Logger
}

var errNotLinux = unavailable(build.UnavailableSystem, "builds only run on Linux")

// Check implements Runtime.
func (p *Podman) Check(context.Context) (RuntimeInfo, error) { return RuntimeInfo{}, errNotLinux }

// Cleanup implements Runtime.
func (p *Podman) Cleanup(context.Context) error { return errNotLinux }

// Images implements Runtime.
func (p *Podman) Images(context.Context, []build.Image) ([]build.ImageStatus, error) {
	return nil, errNotLinux
}

// Run implements Runtime.
func (p *Podman) Run(context.Context, Run) (Result, error) { return Result{}, errNotLinux }

// RemoveTree implements Runtime.
func (p *Podman) RemoveTree(_ context.Context, dir string) error { return removeTree(dir) }

// QuotaDisk is only implemented on Linux.
type QuotaDisk struct{}

// Check implements Disk.
func (QuotaDisk) Check(string) error { return errNotLinux }

// SetProject implements Disk.
func (QuotaDisk) SetProject(string, uint32) error { return errNotLinux }

// Project implements Disk.
func (QuotaDisk) Project(string) (uint32, error) { return 0, errNotLinux }

// Usage implements Disk.
func (QuotaDisk) Usage(string) (Usage, error) { return Usage{}, errNotLinux }

// FilterGuard is only implemented on Linux.
type FilterGuard struct{}

// Protected implements Guard.
func (FilterGuard) Protected() error { return errors.New("builds only run on Linux") }
