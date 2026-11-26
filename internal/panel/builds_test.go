package panel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/build"
)

type fakeBuilds struct {
	st  build.Status
	err error
}

func (f *fakeBuilds) Status(context.Context) (build.Status, error) { return f.st, f.err }

func TestBuildAvailability(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	member := Actor{UserID: "2", Name: "bob", Via: ViaSession}
	if _, err := svc.BuildAvailability(ctx, member); KindOf(err) != Forbidden {
		t.Errorf("a member may see it: %v", err)
	}

	// No build service configured.
	got, err := svc.BuildAvailability(ctx, admin)
	if err != nil || got.Available || got.Reason != build.UnavailableNotConfigured {
		t.Errorf("not configured: %+v %v", got, err)
	}

	// Ready.
	fb := &fakeBuilds{st: build.Status{
		Available: true, Slots: 2, Running: 1,
		Images:    []build.ImageStatus{{Image: build.Images[0], Present: true}},
		MaxLimits: build.MaxLimits, WorkspaceBytes: 4 << 30,
	}}
	svc.builds = fb
	got, _ = svc.BuildAvailability(ctx, admin)
	if !got.Available || got.Slots != 2 || got.Running != 1 || len(got.Images) != 1 || got.WorkspaceBytes != 4<<30 {
		t.Errorf("ready: %+v", got)
	}

	// The service says why not; an unknown reason is not passed on as is,
	// the detail is one line.
	fb.st = build.Status{Reason: build.UnavailableNetwork, Detail: "the filter\nis gone"}
	got, _ = svc.BuildAvailability(ctx, admin)
	if got.Available || got.Reason != build.UnavailableNetwork || got.Detail != "the filter is gone" {
		t.Errorf("network: %+v", got)
	}
	fb.st = build.Status{Reason: "<script>"}
	if got, _ = svc.BuildAvailability(ctx, admin); got.Reason != build.UnavailableWorker {
		t.Errorf("unknown reason: %+v", got)
	}

	// The service does not answer: install.sh's record says why.
	fb.err = errors.New("connection refused")
	got, _ = svc.BuildAvailability(ctx, admin)
	if got.Reason != build.UnavailableWorker {
		t.Errorf("not answering: %+v", got)
	}
	state := filepath.Join(t.TempDir(), "builds")
	svc.buildsStateFile = state
	os.WriteFile(state, []byte("# x\nstate=unavailable\nreason=system\ndetail=Ubuntu 22.04 ships Podman 3.4\n"), 0o644)
	got, _ = svc.BuildAvailability(ctx, admin)
	if got.Reason != build.UnavailableSystem || got.Detail != "Ubuntu 22.04 ships Podman 3.4" {
		t.Errorf("from the record: %+v", got)
	}
	// A record of a set-up environment does not hide that it does not answer.
	os.WriteFile(state, []byte("state=ready\nreason=\ndetail=\n"), 0o644)
	if got, _ = svc.BuildAvailability(ctx, admin); got.Reason != build.UnavailableWorker {
		t.Errorf("ready record: %+v", got)
	}
}
