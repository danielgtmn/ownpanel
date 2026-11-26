package mcp

import (
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/build"
	"github.com/danielgtmn/ownpanel/internal/build/buildtest"
	"github.com/danielgtmn/ownpanel/internal/panel"
)

func TestBuildSiteTool(t *testing.T) {
	fake := buildtest.Start(t)
	e := newEnvWith(t, "", func(c *panel.Config) { c.Builds = fake.Client() })
	cs := e.connect(t, e.write)
	var site siteOutput
	ok(t, call_(t, cs, "create_site", map[string]any{"name": "blog", "domains": []string{"blog.example.com"}}), &site)

	args := func() map[string]any {
		return map[string]any{
			"site": "blog", "command": "npm run build", "output": "dist", "image": "node-24",
			"files":   []any{map[string]any{"path": "package.json", "content": "{}"}},
			"secrets": map[string]any{"TOKEN": "s3cret-value"},
			"live":    false,
		}
	}
	var out buildSiteOutput
	ok(t, call_(t, cs, "build_site", args()), &out)
	if out.Status != "succeeded" || out.Version == "" || out.Live {
		t.Fatalf("built: %+v", out)
	}
	if specs := fake.Specs(); len(specs) != 1 || specs[0].Secrets["TOKEN"] != "s3cret-value" || specs[0].Command != "npm run build" {
		t.Errorf("specs: %v", specs)
	}

	// A failed build is an answer, not a tool error: it says why.
	fake.Result = func(build.Spec) buildtest.Result {
		return buildtest.Result{State: build.StateFailed, Reason: build.ReasonExit, ExitCode: 2,
			Message: "the build command failed", Log: "using s3cret-value\nboom\n"}
	}
	out = buildSiteOutput{}
	ok(t, call_(t, cs, "build_site", args()), &out)
	if out.Status != "failed" || out.Reason != "exit" || out.ExitCode != 2 || out.Version != "" ||
		strings.Contains(out.LogTail, "s3cret-value") || !strings.Contains(out.LogTail, "boom") {
		t.Fatalf("failed: %+v", out)
	}

	// Only administrators; read-only tokens not at all.
	refused(t, call_(t, e.connect(t, e.read), "build_site", args()), "admins")
	bad := args()
	bad["image"] = "ubuntu"
	refused(t, call_(t, cs, "build_site", bad), "no build image")
}
