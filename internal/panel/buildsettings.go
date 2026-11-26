package panel

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build"
	"github.com/danielgtmn/ownpanel/internal/build/presets"
	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// Build settings per site (#114): what runs to build a site from its
// repository, in which environment, and which folder is published. They
// are kept apart from the site's source, so unlinking keeps them.
//
// Who may do what:
//   - seeing them: whoever may see the site; the values of secret
//     variables are never shown again, to anyone (only their names);
//   - changing them, and asking for a proposal from the repository: the
//     same as changing the site (owner or administrator, a write token, a
//     token limited to sites only for its sites). The build runs isolated
//     from the panel and the other sites (internal/build), which is what
//     lets people who are not administrators choose its command.
//
// Variables are stored sealed as a whole (buildVariablesPurpose); secret
// values are blanked out of the build's log by the build service and again
// by the panel (build.MaskSecrets) and never appear in the panel's log,
// activity entries, errors or answers.

// buildVariablesPurpose is the purpose of the sealed variables. It is part
// of the stored format: never change it.
const buildVariablesPurpose = "ownpanel site build variables"

const (
	// detectTime bounds fetching a repository to propose settings.
	detectTime = 2 * time.Minute
	// detectMaxBytes bounds the archive read for a proposal; only files at
	// the top of the tree are kept, each up to detectMaxFile.
	detectMaxBytes = 256 << 20
	detectMaxFile  = 1 << 20
	detectMaxFiles = 2000
)

// BuildVariable is an environment variable of a site's build as shown.
// Value is empty for secret ones: it is never shown again.
type BuildVariable struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret"`
}

// SiteBuildSettings are a site's build settings as shown.
type SiteBuildSettings struct {
	Site string `json:"site"`
	// Configured: settings were saved; without them a push or "Build now"
	// fails and asks for them.
	Configured bool            `json:"configured"`
	Command    string          `json:"command"`
	Output     string          `json:"output"`
	Image      string          `json:"image"`
	Variables  []BuildVariable `json:"variables"`
	UpdatedBy  string          `json:"updated_by,omitempty"`
	UpdatedAt  *time.Time      `json:"updated_at,omitempty"`
	// Images are the build environments this panel offers.
	Images []string `json:"images"`
	// BuildsAvailable: this server builds sites (the build service is set
	// up).
	BuildsAvailable bool `json:"builds_available"`
}

// BuildVariableInput is a variable to save. Value nil keeps the value the
// variable of the same name has now (so secret values need not be entered
// again); a new variable needs a value.
type BuildVariableInput struct {
	Name   string  `json:"name"`
	Value  *string `json:"value"`
	Secret bool    `json:"secret"`
}

// String keeps values out of logs.
func (v BuildVariableInput) String() string { return v.Name }

// GoString is String.
func (v BuildVariableInput) GoString() string { return "BuildVariableInput{" + v.Name + "}" }

// SiteBuildSettingsInput replaces a site's build settings. An empty
// command means the output folder is published as it is.
type SiteBuildSettingsInput struct {
	Command   string               `json:"command"`
	Output    string               `json:"output"`
	Image     string               `json:"image"`
	Variables []BuildVariableInput `json:"variables"`
}

// BuildSettingsProposal is what the repository's files suggest
// (presets.Detect on the newest commit of the linked branch).
type BuildSettingsProposal struct {
	Site   string `json:"site"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
	presets.Result
}

func imageList() []string {
	out := make([]string, 0, len(build.Images))
	for _, img := range build.Images {
		out = append(out, img.Name)
	}
	return out
}

// GetSiteBuildSettings returns a site's build settings.
func (s *Service) GetSiteBuildSettings(ctx context.Context, a Actor, site string) (SiteBuildSettings, error) {
	st, err := s.visibleSite(ctx, a, site)
	if err != nil {
		return SiteBuildSettings{}, err
	}
	out := SiteBuildSettings{Site: st.Name, Variables: []BuildVariable{}, Images: imageList(), BuildsAvailable: s.PublishingAvailable()}
	b, ok, err := s.siteBuildSettings(ctx, st.Name)
	if err != nil || !ok {
		return out, err
	}
	stored, err := s.store.SiteBuildSettings(ctx, st.Name)
	if err != nil {
		return SiteBuildSettings{}, storeError(err)
	}
	out.Configured = true
	out.Command, out.Output, out.Image = b.Command, b.Output, b.Image
	out.UpdatedBy, out.UpdatedAt = stored.UpdatedBy, &stored.UpdatedAt
	for _, v := range b.Env {
		bv := BuildVariable{Name: v.Name, Secret: v.Secret}
		if !v.Secret {
			bv.Value = v.Value
		}
		out.Variables = append(out.Variables, bv)
	}
	return out, nil
}

// siteBuildSettings returns a site's stored settings with their variables
// unsealed; false when it has none.
func (s *Service) siteBuildSettings(ctx context.Context, site string) (build.Settings, bool, error) {
	stored, err := s.store.SiteBuildSettings(ctx, site)
	if errors.Is(err, store.ErrNotFound) {
		return build.Settings{}, false, nil
	}
	if err != nil {
		return build.Settings{}, false, storeError(err)
	}
	b := build.Settings{Command: stored.Command, Output: stored.Output, Image: stored.Image}
	if len(stored.Variables) > 0 {
		if s.secrets == nil {
			return build.Settings{}, false, errorf(Internal, "no secret key configured; the build's variables cannot be read")
		}
		raw, err := s.secrets.Open(stored.Variables, buildVariablesPurpose)
		if err != nil {
			return build.Settings{}, false, errorf(Conflict, "the build's variables cannot be read because the server's secret key has changed; enter them again")
		}
		if err := json.Unmarshal(raw, &b.Env); err != nil {
			return build.Settings{}, false, errorf(Internal, "the build's variables are damaged; enter them again")
		}
	}
	return b, true, nil
}

// SetSiteBuildSettings replaces a site's build settings.
func (s *Service) SetSiteBuildSettings(ctx context.Context, a Actor, site string, in SiteBuildSettingsInput) (SiteBuildSettings, error) {
	st, err := s.visibleSite(ctx, a, site)
	if err != nil {
		return SiteBuildSettings{}, err
	}
	if err := needWrite(a); err != nil {
		return SiteBuildSettings{}, err
	}
	have, _, err := s.siteBuildSettings(ctx, st.Name)
	if err != nil && KindOf(err) != Conflict {
		return SiteBuildSettings{}, err
	}
	old := map[string]build.EnvVar{}
	for _, v := range have.Env {
		old[v.Name] = v
	}
	b := build.Settings{Command: in.Command, Output: in.Output, Image: in.Image}
	for _, v := range in.Variables {
		ev := build.EnvVar{Name: v.Name, Secret: v.Secret}
		switch {
		case v.Value != nil:
			ev.Value = *v.Value
		case old[v.Name].Name != "":
			ev.Value = old[v.Name].Value
		default:
			return SiteBuildSettings{}, errorf(Invalid, "enter a value for %s", v.Name)
		}
		b.Env = append(b.Env, ev)
	}
	if strings.TrimSpace(b.Command) != "" && b.Image == "" {
		b.Image = build.Images[0].Name
	}
	clean, err := b.Clean(build.Images)
	if err != nil {
		return SiteBuildSettings{}, errorf(Invalid, "%v", err)
	}
	row := store.SiteBuildSettings{Site: st.Name, Command: clean.Command, Output: clean.Output, Image: clean.Image}
	if len(clean.Env) > 0 {
		if s.secrets == nil {
			return SiteBuildSettings{}, errorf(Internal, "no secret key configured; variables cannot be stored")
		}
		raw, err := json.Marshal(clean.Env)
		if err != nil {
			return SiteBuildSettings{}, newError(Internal, err)
		}
		row.Variables = s.secrets.Seal(raw, buildVariablesPurpose)
	}
	if _, err := s.store.PutSiteBuildSettings(ctx, row, a.UserID); err != nil {
		return SiteBuildSettings{}, storeError(err)
	}
	names := make([]string, 0, len(clean.Env))
	for _, v := range clean.Env {
		names = append(names, v.Name)
	}
	detail := fmt.Sprintf("output %s", clean.Output)
	if clean.Image != "" {
		detail = fmt.Sprintf("environment %s, %s", clean.Image, detail)
	}
	if len(names) > 0 {
		detail += ", variables " + strings.Join(names, " ")
	}
	s.record(ctx, a, "site.build-settings", st.Name, detail)
	return s.GetSiteBuildSettings(ctx, a, st.Name)
}

// buildSettingsFor turns a site's settings into what BuildSite takes. A
// site without a command still goes through the build service (with a
// command that does nothing), so its folder gets the same checks.
func buildSettingsFor(b build.Settings) BuildSettings {
	out := BuildSettings{Command: b.Command, Output: b.Output, Image: b.Image}
	if strings.TrimSpace(out.Command) == "" {
		out.Command = ":"
	}
	if out.Image == "" {
		out.Image = build.Images[0].Name
	}
	for _, v := range b.Env {
		m := &out.Env
		if v.Secret {
			m = &out.Secrets
		}
		if *m == nil {
			*m = map[string]string{}
		}
		(*m)[v.Name] = v.Value
	}
	return out
}

// linkedGitHubSource returns a site's GitHub source with its current name
// on GitHub, or a Conflict saying why it cannot be used now.
func (s *Service) linkedGitHubSource(ctx context.Context, site string) (store.SiteSource, SiteSource, error) {
	src, err := s.store.SiteSource(ctx, site)
	if errors.Is(err, store.ErrNotFound) {
		return store.SiteSource{}, SiteSource{}, errorf(Conflict, "the site is not linked to a repository")
	}
	if err != nil {
		return store.SiteSource{}, SiteSource{}, storeError(err)
	}
	if src.Kind != store.SourceGitHub {
		return store.SiteSource{}, SiteSource{}, errorf(Conflict, "the site's repository is not on a connected GitHub account")
	}
	v := s.checkSource(ctx, src, true)
	switch v.Status {
	case SourceLinked:
	case SourceConnectionMissing:
		return src, v, errorf(Conflict, "the GitHub connection of the site's repository was removed; link the site again")
	case SourceSuspended:
		return src, v, errorf(Conflict, "access to the site's repository is paused on GitHub")
	case SourceRepositoryMissing:
		return src, v, errorf(Conflict, "the panel can no longer see the site's repository on GitHub")
	default:
		return src, v, errorf(Unavailable, "GitHub could not be asked about the site's repository: %s", v.Problem)
	}
	return src, v, nil
}

// repoParts splits owner/name.
func repoParts(full string) (string, string) {
	owner, name, _ := strings.Cut(full, "/")
	return owner, name
}

// DetectBuildSettings proposes build settings from the files of the newest
// commit of the site's linked branch.
func (s *Service) DetectBuildSettings(ctx context.Context, a Actor, site string) (BuildSettingsProposal, error) {
	st, err := s.visibleSite(ctx, a, site)
	if err != nil {
		return BuildSettingsProposal{}, err
	}
	if err := needWrite(a); err != nil {
		return BuildSettingsProposal{}, err
	}
	src, v, err := s.linkedGitHubSource(ctx, st.Name)
	if err != nil {
		return BuildSettingsProposal{}, err
	}
	_, app, err := s.installationApp(ctx, src.InstallationID)
	if err != nil {
		return BuildSettingsProposal{}, err
	}
	tok, err := s.githubReadToken(ctx, src.InstallationID, src.RepositoryID)
	if err != nil {
		return BuildSettingsProposal{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, detectTime)
	defer cancel()
	owner, name := repoParts(v.Repository)
	br, err := s.github.GetBranch(ctx, tok, owner, name, src.Branch)
	if err != nil {
		if errors.Is(err, github.ErrNotFound) {
			return BuildSettingsProposal{}, errorf(Conflict, "%s has no branch %q any more", v.Repository, src.Branch)
		}
		return BuildSettingsProposal{}, githubError(app.Name, err)
	}
	rc, err := s.github.Tarball(ctx, tok, owner, name, br.SHA, detectMaxBytes)
	if err != nil {
		return BuildSettingsProposal{}, githubError(app.Name, err)
	}
	defer rc.Close()
	dir, err := os.MkdirTemp("", "ownpanel-detect-")
	if err != nil {
		return BuildSettingsProposal{}, newError(Internal, err)
	}
	defer os.RemoveAll(dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return BuildSettingsProposal{}, newError(Internal, err)
	}
	defer root.Close()
	if err := topFiles(rc, root, 1); err != nil {
		if ctx.Err() != nil {
			return BuildSettingsProposal{}, errorf(Unavailable, "GitHub did not send the repository's files in time")
		}
		if errors.Is(err, github.ErrTooLarge) {
			return BuildSettingsProposal{}, errorf(TooLarge, "the repository is too large to look at (over %d MB)", detectMaxBytes>>20)
		}
		slog.Info("reading a repository to propose build settings failed", "site", st.Name, "err", err)
		return BuildSettingsProposal{}, errorf(Unavailable, "the repository's files could not be read")
	}
	res, err := presets.Detect(root.FS(), build.Images)
	if err != nil {
		return BuildSettingsProposal{}, newError(Internal, err)
	}
	return BuildSettingsProposal{Site: st.Name, Branch: src.Branch, Commit: br.SHA, Result: res}, nil
}

// topFiles writes the plain files at the top of a tar.gz tree (after strip
// leading folders) into root: all a proposal looks at. Everything else
// (folders, links, deeper files, files over detectMaxFile) is skipped; the
// whole stream is read, so files at the top found late count too.
func topFiles(r io.Reader, root *os.Root, strip int) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	n := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || h.Size > detectMaxFile || n >= detectMaxFiles {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(h.Name, "./"), "/")
		if len(parts) != strip+1 {
			continue
		}
		name := parts[strip]
		if name == "" || name == "." || name == ".." || name != path.Base(name) || strings.ContainsAny(name, "\x00\\") {
			continue
		}
		f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			continue // a duplicate name: the first one counts
		}
		_, cerr := io.Copy(f, io.LimitReader(tr, detectMaxFile))
		if err := f.Close(); cerr == nil {
			cerr = err
		}
		if cerr != nil {
			return cerr
		}
		n++
	}
}
