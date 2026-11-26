package presets

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/danielgtmn/ownpanel/internal/build"
)

var testImages = []build.Image{{Name: "node-22"}, {Name: "node-24"}}

func file(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }

func notesContain(r Result, sub string) bool {
	for _, n := range r.Notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

func TestDetect(t *testing.T) {
	viteDeps := `{"scripts":{"build":"vite build"},"devDependencies":{"vite":"^6"}}`
	tests := []struct {
		name       string
		files      fstest.MapFS
		preset     string
		confidence Confidence
		buildable  bool
		command    string
		output     string
		image      string
		pm         string
		also       []string
		note       string
	}{
		{
			name:   "vite with npm",
			files:  fstest.MapFS{"package.json": file(viteDeps), "package-lock.json": file("{}"), "vite.config.ts": file("export default { build: { outDir: 'build' } }")},
			preset: Vite, confidence: High, buildable: true,
			command: "npm ci\nnpm run build", output: "build", image: "node-24", pm: NPM,
		},
		{
			name:   "vite without config and without lock file",
			files:  fstest.MapFS{"package.json": file(viteDeps)},
			preset: Vite, confidence: Medium, buildable: true,
			command: "npm install\nnpm run build", output: "dist", image: "node-24", pm: NPM, note: "no lock file",
		},
		{
			name: "vite outDir in a comment does not count",
			files: fstest.MapFS{"package.json": file(viteDeps), "package-lock.json": file("{}"),
				"vite.config.js": file("export default {\n  // outDir: 'nope',\n  /* outDir: 'nope2'\n */\n}")},
			preset: Vite, confidence: High, buildable: true, command: "npm ci\nnpm run build", output: "dist", image: "node-24", pm: NPM,
		},
		{
			name: "vite outDir outside the tree",
			files: fstest.MapFS{"package.json": file(viteDeps), "package-lock.json": file("{}"),
				"vite.config.js": file("export default { build: { outDir: '../../etc' } }")},
			preset: Vite, confidence: High, buildable: true, command: "npm ci\nnpm run build", output: "dist", image: "node-24", pm: NPM, note: "not a folder inside the site",
		},
		{
			name: "astro with pnpm",
			files: fstest.MapFS{"package.json": file(`{"scripts":{"build":"astro build"},"dependencies":{"astro":"^5"},"devDependencies":{"vite":"^6"}}`),
				"pnpm-lock.yaml": file("lockfileVersion: 9"), "astro.config.mjs": file("export default defineConfig({ outDir: \"./public-site/\" })")},
			preset: Astro, confidence: High, buildable: true,
			command: "export COREPACK_ENABLE_DOWNLOAD_PROMPT=0\ncorepack pnpm install --frozen-lockfile\ncorepack pnpm run build", output: "public-site", image: "node-24", pm: PNPM,
		},
		{
			name: "astro without build script uses its own command",
			files: fstest.MapFS{"package.json": file(`{"dependencies":{"astro":"^5"}}`), "package-lock.json": file("{}"),
				"astro.config.mjs": file("export default {}")},
			preset: Astro, confidence: High, buildable: true, command: "npm ci\nnpx --no-install astro build", output: "dist", image: "node-24", pm: NPM,
		},
		{
			name: "next with static export and yarn 1",
			files: fstest.MapFS{"package.json": file(`{"scripts":{"build":"next build"},"dependencies":{"next":"15"}}`),
				"yarn.lock": file(""), "next.config.mjs": file("const nextConfig = {\n  output: 'export',\n}\nexport default nextConfig")},
			preset: NextJS, confidence: High, buildable: true, command: "yarn install --frozen-lockfile\nyarn run build", output: "out", image: "node-24", pm: Yarn,
		},
		{
			name: "next without static export",
			files: fstest.MapFS{"package.json": file(`{"scripts":{"build":"next build"},"dependencies":{"next":"15"}}`),
				"package-lock.json": file("{}"), "next.config.js": file("module.exports = { reactStrictMode: true }")},
			preset: NextJS, confidence: High, buildable: false, command: "npm ci\nnpm run build", output: "out", image: "node-24", pm: NPM, note: "output: 'export'",
		},
		{
			name: "next with computed output is not trusted",
			files: fstest.MapFS{"package.json": file(`{"dependencies":{"next":"15"}}`),
				"next.config.js": file("module.exports = { output: process.env.EXPORT ? 'export' : undefined }")},
			preset: NextJS, confidence: High, buildable: false, command: "npm install\nnpx --no-install next build", output: "out", image: "node-24", pm: NPM, note: "output: 'export'",
		},
		{
			name: "eleventy with yarn berry",
			files: fstest.MapFS{"package.json": file(`{"scripts":{"build":"eleventy"},"devDependencies":{"@11ty/eleventy":"^3"},"packageManager":"yarn@4.5.0"}`),
				"yarn.lock": file(""), "eleventy.config.js": file("export default function(c) { return { dir: { input: 'src', output: 'dist' } } }")},
			preset: Eleventy, confidence: High, buildable: true,
			command: "export COREPACK_ENABLE_DOWNLOAD_PROMPT=0\ncorepack yarn install --immutable\ncorepack yarn run build", output: "dist", image: "node-24", pm: Yarn,
		},
		{
			name:   "eleventy config only",
			files:  fstest.MapFS{".eleventy.js": file("module.exports = {}")},
			preset: Eleventy, confidence: Medium, buildable: true, command: "npm install\nnpx --no-install @11ty/eleventy", output: "_site", image: "node-24", pm: NPM,
		},
		{
			name:   "hugo",
			files:  fstest.MapFS{"hugo.toml": file("baseURL = 'https://example.org/'\npublishDir = \"site\"\n[params]\npublishDir = 'nope'\n"), "content/_index.md": file("")},
			preset: Hugo, confidence: High, buildable: false, command: "hugo --minify", output: "site", note: "cannot build Hugo",
		},
		{
			name:   "hugo with config.toml",
			files:  fstest.MapFS{"config.toml": file("baseURL = \"https://example.org/\"\n"), "content/a.md": file("")},
			preset: Hugo, confidence: High, buildable: false, command: "hugo --minify", output: "public",
		},
		{
			name:   "config.toml of something else is not hugo",
			files:  fstest.MapFS{"config.toml": file("[server]\nport = 1\n"), "index.html": file("<h1>")},
			preset: HTML, confidence: Low, buildable: true, output: ".",
		},
		{
			name:   "jekyll",
			files:  fstest.MapFS{"_config.yml": file("title: Blog\ndestination: ./build\n"), "Gemfile": file("gem \"jekyll\", \"~> 4.3\"\n")},
			preset: Jekyll, confidence: High, buildable: false, command: "bundle install\nbundle exec jekyll build", output: "build", note: "cannot build Jekyll",
		},
		{
			name:       "_config.yml without jekyll in the Gemfile",
			files:      fstest.MapFS{"_config.yml": file("title: x\n")},
			confidence: None,
		},
		{
			name:   "plain html",
			files:  fstest.MapFS{"index.html": file("<!doctype html>"), "style.css": file("")},
			preset: HTML, confidence: Low, buildable: true, output: ".",
		},
		{
			name:       "nothing",
			files:      fstest.MapFS{"README.md": file("# hi")},
			confidence: None, note: "not recognised",
		},
		{
			name:       "unknown tool with build script",
			files:      fstest.MapFS{"package.json": file(`{"scripts":{"build":"webpack"}}`), "package-lock.json": file("{}"), "index.html": file("")},
			confidence: Low, buildable: true, command: "npm ci\nnpm run build", output: "dist", image: "node-24", pm: NPM, note: "not recognised",
		},
		{
			name: "two frameworks",
			files: fstest.MapFS{"package.json": file(`{"scripts":{"build":"x"},"dependencies":{"astro":"5","@11ty/eleventy":"3"}}`), "package-lock.json": file("{}"),
				"astro.config.mjs": file("export default {}"), ".eleventy.js": file("")},
			preset: Astro, confidence: Medium, buildable: true, command: "npm ci\nnpm run build", output: "dist", image: "node-24", pm: NPM,
			also: []string{Eleventy}, note: "also shows signs of Eleventy",
		},
		{
			name:   "malformed package.json",
			files:  fstest.MapFS{"package.json": file(`{"dependencies":{"vite":"6"},}`), "vite.config.js": file("export default {}")},
			preset: Vite, confidence: Medium, buildable: true, command: "npm install\nnpx --no-install vite build", output: "dist", image: "node-24", pm: NPM, note: "not valid",
		},
		{
			name:   "package.json with two values",
			files:  fstest.MapFS{"package.json": file(`{"dependencies":{"astro":"5"}} {}`), "index.html": file("")},
			preset: HTML, confidence: Low, buildable: true, output: ".", note: "not valid",
		},
		{
			name:   "huge package.json",
			files:  fstest.MapFS{"package.json": file(`{"x":"` + strings.Repeat("a", maxPackageJSON) + `"}`), "index.html": file("")},
			preset: HTML, confidence: Low, buildable: true, output: ".", note: "larger than",
		},
		{
			name: "huge config file",
			files: fstest.MapFS{"package.json": file(viteDeps), "package-lock.json": file("{}"),
				"vite.config.js": file("export default { build: { outDir: 'x' } }\n" + strings.Repeat("//\n", maxConfigFile))},
			preset: Vite, confidence: High, buildable: true, command: "npm ci\nnpm run build", output: "dist", image: "node-24", pm: NPM, note: "too large",
		},
		{
			name: "several lock files",
			files: fstest.MapFS{"package.json": file(viteDeps), "package-lock.json": file("{}"), "yarn.lock": file(""),
				"vite.config.js": file("")},
			preset: Vite, confidence: High, buildable: true, command: "yarn install --frozen-lockfile\nyarn run build", output: "dist", image: "node-24", pm: Yarn, note: "several package managers",
		},
		{
			name: "packageManager decides between lock files",
			files: fstest.MapFS{"package.json": file(`{"scripts":{"build":"vite build"},"devDependencies":{"vite":"6"},"packageManager":"npm@10.9.0"}`),
				"package-lock.json": file("{}"), "yarn.lock": file(""), "vite.config.js": file("")},
			preset: Vite, confidence: High, buildable: true, command: "npm ci\nnpm run build", output: "dist", image: "node-24", pm: NPM,
		},
		{
			name:   "bun falls back to npm",
			files:  fstest.MapFS{"package.json": file(viteDeps), "bun.lockb": file("x"), "vite.config.js": file("")},
			preset: Vite, confidence: High, buildable: true, command: "npm install\nnpm run build", output: "dist", image: "node-24", pm: NPM, note: "Bun",
		},
		{
			name:   "nvmrc picks node 22",
			files:  fstest.MapFS{"package.json": file(viteDeps), "package-lock.json": file("{}"), ".nvmrc": file("v22.11.0\n")},
			preset: Vite, confidence: Medium, buildable: true, command: "npm ci\nnpm run build", output: "dist", image: "node-22", pm: NPM,
		},
		{
			name:   "node-version lts codename",
			files:  fstest.MapFS{"package.json": file(viteDeps), "package-lock.json": file("{}"), ".node-version": file("lts/jod")},
			preset: Vite, confidence: Medium, buildable: true, command: "npm ci\nnpm run build", output: "dist", image: "node-22", pm: NPM,
		},
		{
			name:   "unsupported node version takes the closest newer",
			files:  fstest.MapFS{"package.json": file(viteDeps), "package-lock.json": file("{}"), ".nvmrc": file("20")},
			preset: Vite, confidence: Medium, buildable: true, command: "npm ci\nnpm run build", output: "dist", image: "node-22", pm: NPM, note: "Node.js 20",
		},
		{
			name:   "too new node version takes the newest",
			files:  fstest.MapFS{"package.json": file(viteDeps), "package-lock.json": file("{}"), ".nvmrc": file("26")},
			preset: Vite, confidence: Medium, buildable: true, command: "npm ci\nnpm run build", output: "dist", image: "node-24", pm: NPM, note: "Node.js 26",
		},
		{
			name: "engines exact",
			files: fstest.MapFS{"package.json": file(`{"scripts":{"build":"vite build"},"devDependencies":{"vite":"6"},"engines":{"node":"^22.12.0"}}`),
				"package-lock.json": file("{}")},
			preset: Vite, confidence: Medium, buildable: true, command: "npm ci\nnpm run build", output: "dist", image: "node-22", pm: NPM,
		},
		{
			name: "engines at least",
			files: fstest.MapFS{"package.json": file(`{"scripts":{"build":"vite build"},"devDependencies":{"vite":"6"},"engines":{"node":">=18"}}`),
				"package-lock.json": file("{}")},
			preset: Vite, confidence: Medium, buildable: true, command: "npm ci\nnpm run build", output: "dist", image: "node-24", pm: NPM,
		},
		{
			name:   "nvmrc nonsense",
			files:  fstest.MapFS{"package.json": file(viteDeps), "package-lock.json": file("{}"), ".nvmrc": file("$(rm -rf /)")},
			preset: Vite, confidence: Medium, buildable: true, command: "npm ci\nnpm run build", output: "dist", image: "node-24", pm: NPM, note: "could not be read",
		},
		{
			name:   "symlinked package.json is not followed",
			files:  fstest.MapFS{"package.json": &fstest.MapFile{Data: []byte("/etc/passwd"), Mode: fs.ModeSymlink}, "index.html": file("")},
			preset: HTML, confidence: Low, buildable: true, output: ".", note: "not a plain file",
		},
		{
			name: "symlinked config file counts as missing",
			files: fstest.MapFS{"package.json": file(viteDeps), "package-lock.json": file("{}"),
				"vite.config.js": &fstest.MapFile{Data: []byte("../outside.js"), Mode: fs.ModeSymlink}},
			preset: Vite, confidence: Medium, buildable: true, command: "npm ci\nnpm run build", output: "dist", image: "node-24", pm: NPM,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Detect(tt.files, testImages)
			if err != nil {
				t.Fatal(err)
			}
			if r.Preset != tt.preset || r.Confidence != tt.confidence || r.Buildable != tt.buildable {
				t.Errorf("preset %q confidence %q buildable %v, want %q %q %v (notes %q)", r.Preset, r.Confidence, r.Buildable, tt.preset, tt.confidence, tt.buildable, r.Notes)
			}
			s := r.Settings
			if s.Command != tt.command || s.Output != tt.output || s.Image != tt.image || r.PackageManager != tt.pm {
				t.Errorf("command %q output %q image %q pm %q,\nwant    %q output %q image %q pm %q", s.Command, s.Output, s.Image, r.PackageManager, tt.command, tt.output, tt.image, tt.pm)
			}
			if !slices.Equal(r.Also, tt.also) {
				t.Errorf("also %v, want %v", r.Also, tt.also)
			}
			if tt.note != "" && !notesContain(r, tt.note) {
				t.Errorf("no note with %q in %q", tt.note, r.Notes)
			}
			if r.Buildable {
				if _, err := s.Clean(append(testImages, build.Image{Name: "x"})); err != nil {
					t.Errorf("the proposal does not pass the settings check: %v", err)
				}
			}
		})
	}
}

func TestDetectNoImages(t *testing.T) {
	r, err := Detect(fstest.MapFS{"package.json": file(`{"devDependencies":{"vite":"6"}}`), "vite.config.js": file("")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Buildable || r.Settings.Image != "" || !notesContain(r, "no build environment") {
		t.Errorf("%+v", r)
	}
}

// plainFS hides ReadLink, like a file system that cannot tell links apart.
type plainFS struct{ fs.FS }

func TestDetectNeedsLinks(t *testing.T) {
	if _, err := Detect(plainFS{fstest.MapFS{}}, testImages); err != ErrNoLinks {
		t.Errorf("%v", err)
	}
}

// TestDetectOnDisk checks a real tree through os.Root: a link that leads
// out of the tree and a folder named like a file are ignored.
func TestDetectOnDisk(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "package.json"), []byte(`{"dependencies":{"next":"15"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "package.json"), filepath.Join(dir, "package.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "astro.config.mjs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	r, err := Detect(root.FS(), build.Images)
	if err != nil {
		t.Fatal(err)
	}
	if r.Preset != HTML || r.Settings.Output != "." {
		t.Errorf("%+v", r)
	}
}

func TestParseEngine(t *testing.T) {
	tests := []struct {
		in      string
		major   int
		atLeast bool
		ok      bool
	}{
		{"20", 20, false, true},
		{"20.x", 20, false, true},
		{"^22.12.0", 22, false, true},
		{"~18.2", 18, false, true},
		{">=18", 18, true, true},
		{">= 18.0.0 <23", 18, true, true},
		{"^20 || ^22", 22, false, true},
		{"*", 0, false, false},
		{"lts", 0, false, false},
		{strings.Repeat("1", 200), 0, false, false},
	}
	for _, tt := range tests {
		m, a, ok := parseEngine(tt.in)
		if m != tt.major || a != tt.atLeast || ok != tt.ok {
			t.Errorf("%q: %d %v %v", tt.in, m, a, ok)
		}
	}
}

func TestFindKeys(t *testing.T) {
	if v := findTOMLString("title = 'x'\npublishDir = \"out\" # here\n", "publishDir"); v != "out" {
		t.Errorf("toml %q", v)
	}
	if v := findTOMLString("[x]\npublishDir = 'out'\n", "publishDir"); v != "" {
		t.Errorf("toml table %q", v)
	}
	if v := findYAMLString("destination: 'site'\n", "destination"); v != "site" {
		t.Errorf("yaml %q", v)
	}
	if v := findYAMLString("  destination: site\n", "destination"); v != "" {
		t.Errorf("yaml nested %q", v)
	}
	if v, seen := findJSString("a = { outDir: 'a' }\nb = { outDir: 'b' }", "outDir"); v != "" || !seen {
		t.Errorf("js twice %q %v", v, seen)
	}
	if v, _ := findJSString("{ myoutDir: 'a' }", "outDir"); v != "" {
		t.Errorf("js prefix %q", v)
	}
	if v, _ := findJSString("{ outDir: `${x}/a` }", "outDir"); v != "" {
		t.Errorf("js template %q", v)
	}
}
