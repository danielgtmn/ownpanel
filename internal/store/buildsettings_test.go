package store

import (
	"errors"
	"testing"
)

func TestBuildSettingsAndRunLogs(t *testing.T) {
	eachStore(t, func(t *testing.T, s *Store) {
		if _, err := s.CreateSite(ctx, "a", []string{"a.example.com"}, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SiteBuildSettings(ctx, "a"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("none yet: %v", err)
		}
		if _, err := s.PutSiteBuildSettings(ctx, SiteBuildSettings{Site: "nope"}, ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown site: %v", err)
		}
		b, err := s.PutSiteBuildSettings(ctx, SiteBuildSettings{Site: "a", Command: "npm ci", Output: "dist", Image: "node-24",
			Variables: []byte("sealed")}, "")
		if err != nil || b.Command != "npm ci" || string(b.Variables) != "sealed" || b.UpdatedAt.IsZero() {
			t.Fatalf("%+v %v", b, err)
		}
		b, err = s.PutSiteBuildSettings(ctx, SiteBuildSettings{Site: "a", Output: "."}, "")
		if err != nil || b.Command != "" || b.Output != "." || len(b.Variables) != 0 {
			t.Fatalf("replaced: %+v %v", b, err)
		}

		// Runs: release, logs, failing running runs at start.
		r1, _ := s.AddRun(ctx, testRun("a", sha('1')), "replaced", 2)
		if _, err := s.ClaimNextRun(ctx, "a", "replaced"); err != nil {
			t.Fatal(err)
		}
		if err := s.PutRunLog(ctx, r1.ID, "line 1\n", false); err != nil {
			t.Fatal(err)
		}
		if err := s.PutRunLog(ctx, r1.ID, "line 1\nline 2\n", true); err != nil {
			t.Fatal(err)
		}
		if log, tr, err := s.RunLog(ctx, r1.ID); err != nil || log != "line 1\nline 2\n" || !tr {
			t.Fatalf("log: %q %v %v", log, tr, err)
		}
		if err := s.PutRunLog(ctx, "nope", "x", false); !errors.Is(err, ErrNotFound) {
			t.Fatalf("log of no run: %v", err)
		}
		done, err := s.FinishRunWithRelease(ctx, r1.ID, RunSucceeded, "", "20260101-000000")
		if err != nil || done.Release != "20260101-000000" {
			t.Fatalf("release: %+v %v", done, err)
		}
		r2, _ := s.AddRun(ctx, testRun("a", sha('2')), "replaced", 2)
		if _, err := s.ClaimNextRun(ctx, "a", "replaced"); err != nil {
			t.Fatal(err)
		}
		r3, _ := s.AddRun(ctx, testRun("a", sha('3')), "replaced", 2)
		if n, err := s.FailRunningRuns(ctx, "the panel restarted"); err != nil || n != 1 {
			t.Fatalf("fail running: %d %v", n, err)
		}
		if got, _ := s.Run(ctx, r2.ID); got.Status != RunFailed || got.Detail != "the panel restarted" || got.FinishedAt == nil {
			t.Fatalf("running run: %+v", got)
		}
		if got, _ := s.Run(ctx, r3.ID); got.Status != RunQueued {
			t.Fatalf("queued run: %+v", got)
		}
		// Pruning r1 (keep 2) removes its log too.
		if _, err := s.AddRun(ctx, testRun("a", sha('4')), "replaced", 2); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Run(ctx, r1.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("r1 kept: %v", err)
		}
		if _, _, err := s.RunLog(ctx, r1.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("log outlived its run: %v", err)
		}

		// Everything goes with the site.
		if err := s.DeleteSite(ctx, "a"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SiteBuildSettings(ctx, "a"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("settings outlived the site: %v", err)
		}
	})
}
