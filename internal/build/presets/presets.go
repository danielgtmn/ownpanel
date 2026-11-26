// Package presets recognises how a site is made from its source tree and
// proposes build settings for it (#114): Vite, Astro, Next.js with static
// export, Hugo, Jekyll, Eleventy and plain HTML.
//
// The source tree is somebody's repository, so every file is untrusted.
// Detection reads only a few small files at the root of the tree, each up
// to a size limit; package.json is parsed as strict JSON and nothing else
// is parsed at all: JavaScript and TypeScript configuration files are read
// as text and matched line by line (they are never run, so a computed
// setting is not seen), and of TOML and YAML files only a few top-level
// keys are matched. Symbolic links are treated as missing, wherever they
// point. A proposal is only a proposal: people confirm it, and the build
// settings are checked again (build.Settings.Clean) before they are saved.
package presets

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtmn/ownpanel/internal/build"
)

// Preset names.
const (
	Vite     = "vite"
	Astro    = "astro"
	NextJS   = "nextjs"
	Hugo     = "hugo"
	Jekyll   = "jekyll"
	Eleventy = "eleventy"
	HTML     = "html"
)

// Confidence says how sure detection is.
type Confidence string

const (
	// High: the framework is a dependency and its configuration (or its
	// own configuration file and a matching folder) is there.
	High Confidence = "high"
	// Medium: one clear sign, or several frameworks were found.
	Medium Confidence = "medium"
	// Low: a guess (an index.html and nothing else).
	Low Confidence = "low"
	// None: nothing was recognised.
	None Confidence = "none"
)

// Package managers.
const (
	NPM  = "npm"
	PNPM = "pnpm"
	Yarn = "yarn"
	Bun  = "bun"
)

// Result is what detection found and proposes.
type Result struct {
	// Preset is one of the preset names, "" when nothing was recognised.
	Preset string `json:"preset"`
	// Name is the preset for people ("Astro").
	Name       string     `json:"name"`
	Confidence Confidence `json:"confidence"`
	// Buildable: the proposed settings can run on this server. False when
	// the site needs a build environment the server does not have, or (for
	// Next.js) a setting in the site itself.
	Buildable bool `json:"buildable"`
	// Settings are the proposal: command, output folder and image. No
	// environment variables are ever proposed.
	Settings build.Settings `json:"settings"`
	// PackageManager is the one the command uses (Node.js sites only).
	PackageManager string `json:"package_manager,omitempty"`
	// Also are other presets with signs in the tree.
	Also []string `json:"also,omitempty"`
	// Notes explain the proposal and what people should check, as
	// sentences for people.
	Notes []string `json:"notes,omitempty"`
}

// names are the presets for people, in the order detection prefers them
// when several are found: the more specific framework first (Astro and
// Next.js sites usually bring Vite or other tools along), plain HTML last.
var order = []struct{ id, name string }{
	{NextJS, "Next.js"},
	{Astro, "Astro"},
	{Eleventy, "Eleventy"},
	{Vite, "Vite"},
	{Hugo, "Hugo"},
	{Jekyll, "Jekyll"},
	{HTML, "Plain HTML"},
}

func displayName(id string) string {
	for _, p := range order {
		if p.id == id {
			return p.name
		}
	}
	return id
}

// ErrNoLinks: the file system cannot tell links from files.
var ErrNoLinks = errors.New("presets: the file system must implement fs.ReadLinkFS")

// Detect looks at the root of the source tree fsys and proposes build
// settings, choosing the build image from images. fsys must tell symbolic
// links apart (fs.ReadLinkFS, as os.Root.FS and os.DirFS do); pass an
// os.Root's FS so nothing outside the tree can be opened even if a file is
// replaced while detection runs.
func Detect(fsys fs.FS, images []build.Image) (Result, error) {
	if _, ok := fsys.(fs.ReadLinkFS); !ok {
		return Result{}, ErrNoLinks
	}
	var notes []string
	t := tree{fsys: fsys, notes: &notes}
	d := detector{t: t, images: images}
	res := d.detect()
	res.Notes = append(notes, res.Notes...)
	if res.Preset != "" {
		res.Name = displayName(res.Preset)
	}
	return res, nil
}

type detector struct {
	t      tree
	images []build.Image
	pkg    *packageJSON
}

// candidate is one preset with signs in the tree.
type candidate struct {
	id     string
	strong bool // dependency and configuration, or the generator's own files
}

func (d *detector) detect() Result {
	pkg, hasPkg := d.t.readPackageJSON()
	d.pkg = pkg
	var found []candidate
	add := func(id string, dep bool, config string) {
		if dep || config != "" {
			found = append(found, candidate{id: id, strong: dep && config != ""})
		}
	}
	add(NextJS, pkg.has("next"), d.t.firstFile("next.config.js", "next.config.mjs", "next.config.ts", "next.config.cjs", "next.config.mts"))
	add(Astro, pkg.has("astro"), d.t.firstFile("astro.config.mjs", "astro.config.js", "astro.config.ts", "astro.config.mts", "astro.config.cjs"))
	add(Eleventy, pkg.has("@11ty/eleventy"), d.t.firstFile(eleventyConfigs...))
	add(Vite, pkg.has("vite"), d.t.firstFile("vite.config.js", "vite.config.mjs", "vite.config.ts", "vite.config.mts", "vite.config.cjs", "vite.config.cts"))
	if c := d.t.firstFile("hugo.toml", "hugo.yaml", "hugo.json"); c != "" {
		found = append(found, candidate{id: Hugo, strong: true})
	} else if d.t.isFile("config.toml") && d.hugoConfigToml() {
		found = append(found, candidate{id: Hugo, strong: d.t.isDir("content") || d.t.isDir("layouts") || d.t.isDir("themes")})
	}
	if d.t.isFile("_config.yml") {
		gem := d.t.text("Gemfile")
		if strings.Contains(gem, "jekyll") || strings.Contains(gem, "github-pages") {
			found = append(found, candidate{id: Jekyll, strong: true})
		}
	}

	// Vite brought along by a framework built on it is not a sign of its
	// own.
	if len(found) > 1 {
		found = slices.DeleteFunc(found, func(c candidate) bool {
			return c.id == Vite && !c.strong && d.t.firstFile("vite.config.js", "vite.config.mjs", "vite.config.ts", "vite.config.mts", "vite.config.cjs", "vite.config.cts") == ""
		})
	}

	if len(found) == 0 {
		if d.t.isFile("index.html") && !pkg.script("build") {
			return d.html()
		}
		res := Result{Confidence: None}
		if hasPkg && pkg.script("build") {
			// Some other Node.js tool: propose its build script, but the
			// output folder is unknown.
			res = d.node(Result{Confidence: Low}, "dist")
			res.Notes = append(res.Notes, "The kind of site was not recognised. The site's own build script is proposed; check the command and enter the folder the build writes the finished site to.")
			return res
		}
		res.Notes = append(res.Notes, "The kind of site was not recognised. Enter the build command and the folder with the finished site yourself.")
		return res
	}

	// The first one in order wins.
	slices.SortStableFunc(found, func(a, b candidate) int { return rank(a.id) - rank(b.id) })
	best := found[0]
	var res Result
	switch best.id {
	case NextJS:
		res = d.next()
	case Astro:
		res = d.astro()
	case Eleventy:
		res = d.eleventy()
	case Vite:
		res = d.vite()
	case Hugo:
		res = d.hugo()
	case Jekyll:
		res = d.jekyll()
	}
	res.Preset = best.id
	res.Confidence = Medium
	if best.strong {
		res.Confidence = High
	}
	for _, c := range found[1:] {
		if !slices.Contains(res.Also, c.id) {
			res.Also = append(res.Also, c.id)
		}
	}
	if len(res.Also) > 0 {
		names := make([]string, len(res.Also))
		for i, id := range res.Also {
			names[i] = displayName(id)
		}
		res.Confidence = Medium
		res.Notes = append(res.Notes, fmt.Sprintf("The site also shows signs of %s; the proposal follows %s. Check that this is how the site is built.", strings.Join(names, ", "), displayName(best.id)))
	}
	return res
}

func rank(id string) int {
	for i, p := range order {
		if p.id == id {
			return i
		}
	}
	return len(order)
}

var eleventyConfigs = []string{".eleventy.js", "eleventy.config.js", "eleventy.config.mjs", "eleventy.config.cjs", "eleventy.config.ts"}

func (d *detector) hugoConfigToml() bool {
	src := d.t.text("config.toml")
	return hasTOMLKey(src, "baseURL") || hasTOMLKey(src, "theme") || hasTOMLKey(src, "languageCode")
}

func (d *detector) html() Result {
	return Result{
		Preset: HTML, Confidence: Low, Buildable: true,
		Settings: build.Settings{Output: "."},
		Notes:    []string{"The site looks like finished pages: they are published as they are, without a build."},
	}
}

// configOutput returns a literal output folder from a configuration file,
// or "" with a note when it is set but cannot be read safely.
func (d *detector) configOutput(file, key, def string) string {
	if file == "" {
		return def
	}
	src := d.t.text(file)
	v, seen := findJSString(src, key)
	switch {
	case v != "":
		if out, err := (build.Settings{Output: v}).Clean(nil); err == nil {
			return out.Output
		}
		d.t.note("The output folder set in %s is not a folder inside the site; %s is proposed instead.", file, def)
	case seen:
		d.t.note("The output folder in %s could not be read with certainty; %s is proposed. Check it.", file, def)
	}
	return def
}

func (d *detector) vite() Result {
	cfg := d.t.firstFile("vite.config.js", "vite.config.mjs", "vite.config.ts", "vite.config.mts", "vite.config.cjs", "vite.config.cts")
	out := d.configOutput(cfg, "outDir", "dist")
	return d.node(Result{}, out, "vite", "build")
}

func (d *detector) astro() Result {
	cfg := d.t.firstFile("astro.config.mjs", "astro.config.js", "astro.config.ts", "astro.config.mts", "astro.config.cjs")
	out := d.configOutput(cfg, "outDir", "dist")
	res := d.node(Result{}, out, "astro", "build")
	if cfg != "" {
		if v, _ := findJSString(d.t.text(cfg), "output"); v == "server" {
			res.Notes = append(res.Notes, "The site is set up to render pages on a server; only the pages that are built ahead of time will be published.")
		}
	}
	return res
}

func (d *detector) next() Result {
	cfg := d.t.firstFile("next.config.js", "next.config.mjs", "next.config.ts", "next.config.cjs", "next.config.mts")
	src := ""
	if cfg != "" {
		src = d.t.text(cfg)
	}
	output, _ := findJSString(src, "output")
	if output != "export" {
		res := d.node(Result{}, "out", "next", "build")
		res.Buildable = false
		res.Notes = append(res.Notes, "Next.js sites can only be published here as finished pages. Set output: 'export' in the Next.js configuration, then the build writes them to the folder out.")
		return res
	}
	out := "out"
	if v, _ := findJSString(src, "distDir"); v != "" {
		if c, err := (build.Settings{Output: v}).Clean(nil); err == nil {
			out = c.Output
		}
	}
	return d.node(Result{}, out, "next", "build")
}

func (d *detector) eleventy() Result {
	cfg := d.t.firstFile(eleventyConfigs...)
	out := "_site"
	if cfg != "" {
		// Eleventy's output folder is dir.output; "output" alone is the
		// most specific key that can be matched as text.
		out = d.configOutput(cfg, "output", "_site")
	}
	return d.node(Result{}, out, "@11ty/eleventy")
}

// noImage is a site whose generator has no build environment on this
// server.
func (d *detector) noImage(name, cmd, out string) Result {
	return Result{
		Buildable: false,
		Settings:  build.Settings{Command: cmd, Output: out},
		Notes:     []string{fmt.Sprintf("This server cannot build %s sites yet. Build the site elsewhere and publish the finished pages, or publish them from a repository that holds them.", name)},
	}
}

func (d *detector) hugo() Result {
	out := "public"
	if f := d.t.firstFile("hugo.toml", "config.toml"); f != "" {
		if v := findTOMLString(d.t.text(f), "publishDir"); v != "" {
			out = d.cleanOr(v, out)
		}
	} else if d.t.isFile("hugo.yaml") {
		if v := findYAMLString(d.t.text("hugo.yaml"), "publishDir"); v != "" {
			out = d.cleanOr(v, out)
		}
	}
	return d.noImage("Hugo", "hugo --minify", out)
}

func (d *detector) jekyll() Result {
	out := "_site"
	if v := findYAMLString(d.t.text("_config.yml"), "destination"); v != "" {
		out = d.cleanOr(v, out)
	}
	return d.noImage("Jekyll", "bundle install\nbundle exec jekyll build", out)
}

func (d *detector) cleanOr(v, def string) string {
	if c, err := (build.Settings{Output: v}).Clean(nil); err == nil {
		return c.Output
	}
	d.t.note("The output folder set by the site is not a folder inside the site; %s is proposed instead.", def)
	return def
}

// node proposes the settings of a Node.js site: install the packages with
// the site's package manager, then run its build script, or, without one,
// the framework's own command (bin and its arguments).
func (d *detector) node(res Result, out string, bin ...string) Result {
	pm, install, run, exec := d.packageManager()
	var lines []string
	if strings.HasPrefix(install, "corepack ") {
		// Corepack would otherwise ask before it downloads the package
		// manager; the build has nobody to answer.
		lines = append(lines, "export COREPACK_ENABLE_DOWNLOAD_PROMPT=0")
	}
	lines = append(lines, install)
	switch {
	case d.pkg.script("build"):
		lines = append(lines, run+" build")
	case len(bin) > 0:
		lines = append(lines, exec+" "+strings.Join(bin, " "))
	}
	res.PackageManager = pm
	res.Buildable = true
	res.Settings.Command = strings.Join(lines, "\n")
	res.Settings.Output = out
	res.Settings.Image = d.nodeImage(&res)
	if res.Settings.Image == "" {
		res.Buildable = false
	}
	return res
}

// packageManager picks the package manager from the lock files and the
// packageManager field and returns the commands to install, to run a
// script and to run a package's program. The images have npm and Yarn 1;
// pnpm and newer Yarn come through Corepack (part of Node.js up to 24).
// Bun is not in the images.
func (d *detector) packageManager() (pm, install, run, exec string) {
	declared := ""
	if d.pkg != nil {
		declared, _, _ = strings.Cut(d.pkg.PackageManager, "@")
	}
	locks := map[string]bool{
		NPM:  d.t.isFile("package-lock.json") || d.t.isFile("npm-shrinkwrap.json"),
		PNPM: d.t.isFile("pnpm-lock.yaml"),
		Yarn: d.t.isFile("yarn.lock"),
		Bun:  d.t.isFile("bun.lock") || d.t.isFile("bun.lockb"),
	}
	var present []string
	for _, p := range []string{PNPM, Yarn, NPM, Bun} {
		if locks[p] {
			present = append(present, p)
		}
	}
	switch {
	case declared != "" && (locks[declared] || len(present) == 0):
		pm = declared
	case len(present) > 0:
		pm = present[0]
		if len(present) > 1 {
			d.t.note("The site has lock files of several package managers (%s); %s is used. Remove the others, or name the package manager in package.json.", strings.Join(present, ", "), pm)
		}
	default:
		pm = NPM
	}
	lock := locks[pm]
	switch pm {
	case PNPM:
		install = "corepack pnpm install"
		if lock {
			install += " --frozen-lockfile"
		}
		return PNPM, install, "corepack pnpm run", "corepack pnpm exec"
	case Yarn:
		if d.yarnBerry() {
			install = "corepack yarn install"
			if lock {
				install += " --immutable"
			}
			return Yarn, install, "corepack yarn run", "corepack yarn exec"
		}
		install = "yarn install"
		if lock {
			install += " --frozen-lockfile"
		}
		return Yarn, install, "yarn run", "yarn exec"
	case Bun:
		d.t.note("The site uses Bun, which this server cannot build with; npm is used instead, without the lock file. Check that the build works with npm.")
	case NPM:
	default:
		d.t.note("The package manager named in package.json is not known; npm is used instead.")
	}
	if locks[NPM] {
		return NPM, "npm ci", "npm run", "npx --no-install"
	}
	if pm == NPM {
		d.t.note("The site has no lock file, so the newest matching versions of its packages are installed on every build. Commit package-lock.json to build the same way every time.")
	}
	return NPM, "npm install", "npm run", "npx --no-install"
}

// yarnBerry tells whether the site uses Yarn 2 or newer.
func (d *detector) yarnBerry() bool {
	if d.pkg != nil {
		if name, ver, ok := strings.Cut(d.pkg.PackageManager, "@"); ok && name == Yarn {
			return !strings.HasPrefix(ver, "1.")
		}
	}
	return d.t.isFile(".yarnrc.yml")
}

// nodeImage chooses the Node.js image: the version the site asks for in
// .nvmrc, .node-version or engines.node when the list has it, else the
// closest one with a note. Without a wish, the newest.
func (d *detector) nodeImage(res *Result) string {
	avail := nodeMajors(d.images)
	if len(avail) == 0 {
		res.Notes = append(res.Notes, "This server has no build environment for Node.js sites.")
		return ""
	}
	newest := avail[len(avail)-1]
	want, atLeast, source := d.wantedNode()
	if source == "" {
		return nodeName(newest)
	}
	if want == 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("The Node.js version in %s could not be read; Node.js %d is used.", source, newest))
		return nodeName(newest)
	}
	if atLeast {
		if newest >= want {
			return nodeName(newest)
		}
		res.Notes = append(res.Notes, fmt.Sprintf("The site asks for Node.js %d or newer in %s; the newest this server has is %d, which is used.", want, source, newest))
		return nodeName(newest)
	}
	if slices.Contains(avail, want) {
		return nodeName(want)
	}
	pick := newest
	for _, v := range avail {
		if v > want {
			pick = v
			break
		}
	}
	res.Notes = append(res.Notes, fmt.Sprintf("The site asks for Node.js %d in %s, which this server does not have; Node.js %d is used instead. Check that the site builds with it.", want, source, pick))
	return nodeName(pick)
}

func nodeName(major int) string { return "node-" + strconv.Itoa(major) }

// nodeMajors returns the Node.js versions of the list, oldest first.
func nodeMajors(images []build.Image) []int {
	var out []int
	for _, img := range images {
		if v, ok := strings.CutPrefix(img.Name, "node-"); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				out = append(out, n)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// ltsCodenames are the release names nvm accepts after lts/.
var ltsCodenames = map[string]int{
	"hydrogen": 18, "iron": 20, "jod": 22, "krypton": 24,
}

// wantedNode returns the major version the site asks for and where; 0 when
// a file is there but its content is not understood. atLeast: any newer
// version is fine.
func (d *detector) wantedNode() (major int, atLeast bool, source string) {
	for _, f := range []string{".nvmrc", ".node-version"} {
		data, err := d.t.read(f, maxVersionFile)
		if errors.Is(err, errMissing) {
			continue
		}
		if err != nil {
			return 0, false, f
		}
		line, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
		line = strings.ToLower(strings.TrimSpace(line))
		switch {
		case line == "node" || line == "latest" || line == "current" || line == "lts/*" || line == "lts":
			return 1, true, f
		case strings.HasPrefix(line, "lts/"):
			if v, ok := ltsCodenames[strings.TrimPrefix(line, "lts/")]; ok {
				return v, false, f
			}
			return 0, false, f
		}
		v, ok := leadingMajor(strings.TrimPrefix(line, "v"))
		if !ok {
			return 0, false, f
		}
		return v, false, f
	}
	if e := strings.TrimSpace(d.pkg.nodeEngine()); e != "" {
		v, atLeast, ok := parseEngine(e)
		if !ok {
			return 0, false, "package.json (engines)"
		}
		return v, atLeast, "package.json (engines)"
	}
	return 0, false, ""
}

// parseEngine understands the common forms of engines.node: "20", "20.x",
// "^20.1", "~20", "20 || 22", ">=18", ">= 18.0.0". Of "a || b" the newest
// alternative counts.
func parseEngine(e string) (major int, atLeast bool, ok bool) {
	if len(e) > 128 {
		return 0, false, false
	}
	best, bestAtLeast, any := 0, false, false
	for alt := range strings.SplitSeq(e, "||") {
		alt = strings.TrimSpace(alt)
		first, _, _ := strings.Cut(alt, " ")
		if strings.HasPrefix(alt, ">=") || strings.HasPrefix(alt, ">") {
			first = strings.TrimSpace(strings.TrimLeft(alt, ">="))
			first, _, _ = strings.Cut(first, " ")
			v, ok := leadingMajor(strings.TrimPrefix(first, "v"))
			if !ok {
				return 0, false, false
			}
			if !any || v >= best {
				best, bestAtLeast, any = v, true, true
			}
			continue
		}
		first = strings.TrimLeft(first, "^~=v")
		v, ok := leadingMajor(first)
		if !ok {
			return 0, false, false
		}
		if !any || v > best {
			best, bestAtLeast, any = v, false, true
		}
	}
	return best, bestAtLeast, any
}

// leadingMajor reads the major version of "20", "20.11.1", "20.x".
func leadingMajor(s string) (int, bool) {
	head, _, _ := strings.Cut(s, ".")
	if head == "" || len(head) > 3 {
		return 0, false
	}
	n, err := strconv.Atoi(head)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }
