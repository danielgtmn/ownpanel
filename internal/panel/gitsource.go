package panel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/danielgtmn/ownpanel/internal/gitsource"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// Sources of kind git (#111): any Git repository, reached over SSH with a
// deploy key the panel makes per site, or over HTTPS when it is public.
//
// Where the repository is fetched: in the panel, by internal/gitsource,
// which speaks the fetch side of Git's protocol itself and never runs a
// program. The deploy key is unsealed only in memory, for the one
// connection; the build service never sees it. See gitsource's package
// comment for the limits and wiki/Security.md ("Git repositories").
//
// Linking takes three steps, so the person sees what they are trusting:
//
//  1. InspectGitRepository checks the address and, for SSH, fetches the
//     server's key and shows its fingerprint (for HTTPS it lists the
//     branches of the public repository).
//  2. SetGitSiteSource links the site with the fingerprint the person
//     confirmed. The panel asks the server again and refuses if it shows
//     another key. It makes the deploy key (SSH) and the notification
//     secret, and keeps both when the site was already linked to a Git
//     repository, so changing the branch needs nothing new at the host.
//  3. The person adds the public key (read access) and the notification
//     address at the host; CheckGitSiteSource tries the key and lists the
//     branches.
//
// A server that later shows another key is refused before anything is
// sent to it; the source shows the new fingerprint and someone who may
// change the site accepts it (AcceptGitHostKey), again only if the server
// shows exactly the key they confirmed.
//
// Who may do what: seeing a Git source (address, public key, fingerprints,
// notification address) is for whoever may see the site; everything else,
// including the checks that connect to the host, needs the right to change
// the site. Unlike GitHub connections this is open to members: the
// repository is their own, reached with a key they add themselves. The
// private key and the notification secret are never returned; the secret
// is shown once, in the answer that created it.

// Purposes of the sealed secrets. They are part of the stored format and
// must never change.
const (
	gitDeployKeyPurpose = "ownpanel git deploy key"
	gitNotifyPurpose    = "ownpanel git notification secret"
)

// Git source states (besides SourceLinked).
const (
	// SourceNotChecked: the panel has not reached the repository since it
	// was linked or its key replaced.
	SourceNotChecked = "not_checked"
	// SourceHostKeyChanged: the SSH server showed another key than the
	// confirmed one; nothing is fetched until someone accepts it.
	SourceHostKeyChanged = "host_key_changed"
	// SourceAccessFailed: the last attempt to reach the repository failed
	// (Problem says why), e.g. the deploy key was not added.
	SourceAccessFailed = "access_failed"
)

// Transports of a Git source.
const (
	GitSSH   = "ssh"
	GitHTTPS = "https"
)

// GitNotifyPath is where Git hosts send notifications:
// GitNotifyPath + "<notify id>".
const GitNotifyPath = "/api/git/notify/"

// Rate limit of the checks that connect to Git hosts, per person.
const (
	gitCheckRate  = 0.2 // one every 5 seconds
	gitCheckBurst = 20.0
)

// gitFetchSlots bounds fetches running at once (each may use up to
// gitsource.DefaultLimits of temporary disk space).
var gitFetchSlots = make(chan struct{}, 2)

// GitSourceInput links a site to a Git repository.
type GitSourceInput struct {
	Repository    string `json:"repository"`
	Branch        string `json:"branch"`
	PublishOnPush bool   `json:"publish_on_push"`
	// HostKey is the fingerprint (SHA256:…) of the SSH server's key the
	// person confirmed; empty for HTTPS.
	HostKey string `json:"host_key"`
}

// GitRepositoryCheck is what InspectGitRepository found.
type GitRepositoryCheck struct {
	// Repository is the address in its canonical form.
	Repository string `json:"repository"`
	Server     string `json:"server"`
	Transport  string `json:"transport"`
	// HostKey is the fingerprint of the SSH server's key, HostKeyType its
	// type (SSH only).
	HostKey     string `json:"host_key,omitempty"`
	HostKeyType string `json:"host_key_type,omitempty"`
	// Pinned: the site is already linked to this server with this key.
	Pinned bool `json:"pinned,omitempty"`
	// Branches of a public repository (HTTPS only).
	Branches      []string `json:"branches,omitempty"`
	DefaultBranch string   `json:"default_branch,omitempty"`
	Truncated     bool     `json:"truncated,omitempty"`
}

// GitSourceCheck is the result of CheckGitSiteSource.
type GitSourceCheck struct {
	SiteSourceState
	// OK: the repository was reached (with the deploy key over SSH).
	OK            bool     `json:"ok"`
	Branches      []string `json:"branches"`
	DefaultBranch string   `json:"default_branch,omitempty"`
	Truncated     bool     `json:"truncated,omitempty"`
	// BranchFound: the linked branch exists; Commit is where it points.
	BranchFound bool   `json:"branch_found"`
	Commit      string `json:"commit,omitempty"`
}

// gitChecks limits the connections people make to Git hosts.
type gitChecks struct {
	mu    sync.Mutex
	users map[string]*rateBucket
}

func (s *Service) allowGitCheck(a Actor) error {
	c := &s.gitChecks
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.users == nil || len(c.users) > githubHookMaxBucket {
		c.users = map[string]*rateBucket{}
	}
	key := a.UserID
	if a.Via == ViaSystem {
		return nil
	}
	b := c.users[key]
	if b == nil {
		b = &rateBucket{}
		c.users[key] = b
	}
	if !b.take(s.now(), gitCheckRate, gitCheckBurst) {
		return errorf(Throttled, "too many connections to Git servers; try again in a minute")
	}
	return nil
}

// ---- keys ----

func (s *Service) needSecrets() error {
	if s.secrets == nil {
		return errorf(Unavailable, "this panel has no secret key, so it cannot keep deploy keys")
	}
	return nil
}

// newDeployKey makes an Ed25519 key pair: the sealed private key and the
// public key as an authorized_keys line named after the site.
func (s *Service) newDeployKey(site string) ([]byte, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, "", err
	}
	sealed := s.secrets.Seal(priv, gitDeployKeyPurpose)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " " + deployKeyComment(site, s.PanelDomain())
	return sealed, line, nil
}

// deployKeyComment names the key at the Git host: ownpanel-<site>[@<panel>].
func deployKeyComment(site, panel string) string {
	if panel == "" {
		return "ownpanel-" + site
	}
	return "ownpanel-" + site + "@" + panel
}

func (s *Service) deploySigner(src store.SiteSource) (ssh.Signer, error) {
	if err := s.needSecrets(); err != nil {
		return nil, err
	}
	raw, err := s.secrets.Open(src.DeployKey, gitDeployKeyPurpose)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		slog.Warn("a deploy key cannot be opened (the server's secret key changed?)", "site", src.Site)
		return nil, errorf(Unavailable, "the deploy key cannot be read; replace it")
	}
	return ssh.NewSignerFromKey(ed25519.PrivateKey(raw))
}

// newNotifySecret makes the id of a notification address and its sealed
// secret; secret is shown once.
func (s *Service) newNotifySecret() (id, secret string, sealed []byte, err error) {
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		return "", "", nil, err
	}
	id, secret = hex.EncodeToString(b[:16]), hex.EncodeToString(b[16:])
	return id, secret, s.secrets.Seal([]byte(secret), gitNotifyPurpose), nil
}

func parseKeyLine(line string) (ssh.PublicKey, bool) {
	if line == "" {
		return nil, false
	}
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	return k, err == nil
}

func keyLine(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// ---- views ----

// gitNotifyURL is the notification address, "" while the panel has no
// public address with HTTPS.
func (s *Service) gitNotifyURL(id string) string {
	base, err := s.githubBase()
	if err != nil || id == "" {
		return ""
	}
	return base + GitNotifyPath + id
}

// gitSourceView fills in what a Git source shows. Nothing here is secret.
func (s *Service) gitSourceView(src store.SiteSource) SiteSource {
	v := sourceView(src)
	a, err := gitsource.ParseAddress(src.Repository)
	if err != nil {
		// An address stored before stricter rules: shown, never used.
		v.Status, v.Problem = SourceAccessFailed, err.Error()
		return v
	}
	v.Server, v.Transport = a.ServerName(), a.Scheme
	if _, ok := parseKeyLine(src.DeployPublicKey); ok {
		v.DeployKey = src.DeployPublicKey
	}
	if k, ok := parseKeyLine(src.HostKey); ok {
		v.HostKey, v.HostKeyType = ssh.FingerprintSHA256(k), k.Type()
	}
	v.NotifyURL = s.gitNotifyURL(src.NotifyID)
	if v.NotifyURL == "" {
		v.NotifyProblem = "Pushes can only be reported once the panel has its own domain with HTTPS (Settings → Panel address)."
	}
	v.CheckedAt = src.CheckedAt
	switch {
	case src.HostKeyNew != "":
		v.Status = SourceHostKeyChanged
		if k, ok := parseKeyLine(src.HostKeyNew); ok {
			v.NewHostKey = ssh.FingerprintSHA256(k)
		}
		v.Problem = fmt.Sprintf("%s now shows a different server key. Nothing is fetched until you check the new key and accept it.", v.Server)
	case src.CheckError != "":
		v.Status, v.Problem = SourceAccessFailed, src.CheckError
	case src.CheckedAt == nil:
		v.Status = SourceNotChecked
	}
	return v
}

// gitError turns what gitsource returns into an error for people. The
// texts of gitsource's errors are written for people and contain nothing
// secret (never the key, never the server's raw answer beyond one short
// line).
func gitError(err error) error {
	var (
		ae  *gitsource.AddressError
		hk  *gitsource.HostKeyChangedError
		au  *gitsource.AuthError
		bm  *gitsource.BranchMissingError
		mv  *gitsource.BranchMovedError
		le  *gitsource.LimitError
		te  *gitsource.TreeError
		se  *gitsource.ServerError
		pe  *gitsource.ProtocolError
		ce  *gitsource.ConnectError
		he  *gitsource.HTTPError
		own *Error
	)
	switch {
	case errors.As(err, &own):
		return err
	case errors.As(err, &ae), errors.As(err, &bm), errors.As(err, &le), errors.As(err, &te):
		return &Error{Kind: Invalid, Msg: err.Error(), Err: err}
	case errors.As(err, &hk), errors.As(err, &mv):
		return &Error{Kind: Conflict, Msg: err.Error(), Err: err}
	case errors.As(err, &au), errors.As(err, &se), errors.As(err, &pe), errors.As(err, &ce), errors.As(err, &he):
		return &Error{Kind: Unavailable, Msg: err.Error(), Err: err}
	}
	slog.Error("fetching from a Git repository failed", "err", err)
	return errorf(Internal, "fetching from the repository failed")
}

// problemText is what a failed attempt stores for the site page.
func problemText(err error) string {
	return plainText(gitError(err).Error(), 300)
}

// ---- operations ----

// gitSource returns the Git source of a site the actor may change.
func (s *Service) gitSource(ctx context.Context, a Actor, site string) (store.Site, store.SiteSource, error) {
	st, err := s.visibleSite(ctx, a, site)
	if err != nil {
		return store.Site{}, store.SiteSource{}, err
	}
	if err := needWrite(a); err != nil {
		return store.Site{}, store.SiteSource{}, err
	}
	src, err := s.store.SiteSource(ctx, st.Name)
	if err != nil {
		return store.Site{}, store.SiteSource{}, storeError(err)
	}
	if src.Kind != store.SourceGit {
		return store.Site{}, store.SiteSource{}, errorf(Conflict, "the site is not linked to a Git repository by address")
	}
	return st, src, nil
}

// InspectGitRepository checks an address before linking: for SSH it shows
// the server's key to confirm, for HTTPS the branches of the repository.
func (s *Service) InspectGitRepository(ctx context.Context, a Actor, site, address string) (GitRepositoryCheck, error) {
	st, err := s.visibleSite(ctx, a, site)
	if err != nil {
		return GitRepositoryCheck{}, err
	}
	if err := needWrite(a); err != nil {
		return GitRepositoryCheck{}, err
	}
	addr, err := gitsource.ParseAddress(strings.TrimSpace(address))
	if err != nil {
		return GitRepositoryCheck{}, gitError(err)
	}
	if err := s.allowGitCheck(a); err != nil {
		return GitRepositoryCheck{}, err
	}
	out := GitRepositoryCheck{Repository: addr.String(), Server: addr.ServerName(), Transport: addr.Scheme}
	if addr.Scheme == gitsource.SchemeHTTPS {
		b, err := gitsource.ListBranches(ctx, gitsource.Remote{Address: addr})
		if err != nil {
			return GitRepositoryCheck{}, gitError(err)
		}
		out.Branches, out.DefaultBranch, out.Truncated = nonNil(b.Names), b.Default, b.Truncated
		return out, nil
	}
	k, err := gitsource.ScanHostKey(ctx, addr)
	if err != nil {
		return GitRepositoryCheck{}, gitError(err)
	}
	out.HostKey, out.HostKeyType = ssh.FingerprintSHA256(k), k.Type()
	if cur, err := s.store.SiteSource(ctx, st.Name); err == nil && cur.Kind == store.SourceGit {
		if ca, err := gitsource.ParseAddress(cur.Repository); err == nil && ca.ServerName() == addr.ServerName() {
			if pk, ok := parseKeyLine(cur.HostKey); ok && keyLine(pk) == keyLine(k) {
				out.Pinned = true
			}
		}
	}
	return out, nil
}

// SetGitSiteSource links a site to a Git repository by address, or changes
// the branch or "publish on push" of that link. The answer carries the
// notification secret when it was made now (the only time it is shown).
func (s *Service) SetGitSiteSource(ctx context.Context, a Actor, site string, in GitSourceInput) (SiteSourceState, error) {
	st, err := s.visibleSite(ctx, a, site)
	if err != nil {
		return SiteSourceState{}, err
	}
	if err := needWrite(a); err != nil {
		return SiteSourceState{}, err
	}
	if a.Via == ViaSystem {
		return SiteSourceState{}, errorf(Forbidden, "a site must be linked by a person")
	}
	if err := ValidateBranch(in.Branch); err != nil {
		return SiteSourceState{}, err
	}
	addr, err := gitsource.ParseAddress(strings.TrimSpace(in.Repository))
	if err != nil {
		return SiteSourceState{}, gitError(err)
	}
	if err := s.needSecrets(); err != nil {
		return SiteSourceState{}, err
	}
	var cur *store.SiteSource
	if c, err := s.store.SiteSource(ctx, st.Name); err == nil && c.Kind == store.SourceGit {
		cur = &c
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return SiteSourceState{}, storeError(err)
	}
	src := store.SiteSource{Site: st.Name, Kind: store.SourceGit, Repository: addr.String(), Branch: in.Branch,
		PublishOnPush: in.PublishOnPush}
	if cur != nil && cur.Repository == src.Repository {
		// The same repository: what the panel learnt about it still holds.
		src.CheckedAt, src.CheckError, src.HostKeyNew = cur.CheckedAt, cur.CheckError, cur.HostKeyNew
	}

	switch addr.Scheme {
	case gitsource.SchemeSSH:
		want := strings.TrimSpace(in.HostKey)
		if want == "" {
			return SiteSourceState{}, errorf(Invalid, "confirm the fingerprint of %s's key first", addr.ServerName())
		}
		// Keep the confirmed key of the same server without asking it again.
		if cur != nil {
			if ca, err := gitsource.ParseAddress(cur.Repository); err == nil && ca.ServerName() == addr.ServerName() {
				if pk, ok := parseKeyLine(cur.HostKey); ok && ssh.FingerprintSHA256(pk) == want {
					src.HostKey = cur.HostKey
				}
			}
		}
		if src.HostKey == "" {
			if err := s.allowGitCheck(a); err != nil {
				return SiteSourceState{}, err
			}
			k, err := gitsource.ScanHostKey(ctx, addr)
			if err != nil {
				return SiteSourceState{}, gitError(err)
			}
			if got := ssh.FingerprintSHA256(k); got != want {
				return SiteSourceState{}, errorf(Conflict, "%s now shows the key %s, not %s which you confirmed; check it again", addr.ServerName(), got, want)
			}
			src.HostKey, src.HostKeyNew = keyLine(k), ""
			if cur == nil || cur.HostKey != src.HostKey {
				src.CheckedAt, src.CheckError = nil, ""
			}
		}
		if cur != nil && len(cur.DeployKey) > 0 {
			src.DeployKey, src.DeployPublicKey = cur.DeployKey, cur.DeployPublicKey
		} else {
			if src.DeployKey, src.DeployPublicKey, err = s.newDeployKey(st.Name); err != nil {
				return SiteSourceState{}, newError(Internal, err)
			}
			src.CheckedAt, src.CheckError = nil, ""
		}
	case gitsource.SchemeHTTPS:
		if err := s.allowGitCheck(a); err != nil {
			return SiteSourceState{}, err
		}
		if _, err := gitsource.BranchTip(ctx, gitsource.Remote{Address: addr}, in.Branch); err != nil {
			return SiteSourceState{}, gitError(err)
		}
		now := s.now()
		src.CheckedAt, src.CheckError, src.HostKeyNew = &now, "", ""
	}

	secret := ""
	if cur != nil {
		src.NotifyID, src.NotifySecret = cur.NotifyID, cur.NotifySecret
	} else {
		if src.NotifyID, secret, src.NotifySecret, err = s.newNotifySecret(); err != nil {
			return SiteSourceState{}, newError(Internal, err)
		}
	}
	saved, err := s.store.PutSiteSource(ctx, src, a.UserID)
	if err != nil {
		return SiteSourceState{}, storeError(err)
	}
	push := "off"
	if in.PublishOnPush {
		push = "on"
	}
	detail := fmt.Sprintf("%s, branch %s, publish on push %s", saved.Repository, in.Branch, push)
	if saved.HostKey != "" {
		if k, ok := parseKeyLine(saved.HostKey); ok {
			detail += ", server key " + ssh.FingerprintSHA256(k)
		}
	}
	s.record(ctx, a, "site.source", st.Name, detail)
	out, err := s.gitState(ctx, saved)
	if err != nil {
		return SiteSourceState{}, err
	}
	out.NotifySecret = secret
	return out, nil
}

func (s *Service) gitState(ctx context.Context, src store.SiteSource) (SiteSourceState, error) {
	v := s.gitSourceView(src)
	out := SiteSourceState{Site: src.Site, Source: &v, PublishingAvailable: PublishingAvailable}
	var err error
	if out.LastRun, err = s.lastRun(ctx, src.Site); err != nil {
		return SiteSourceState{}, storeError(err)
	}
	return out, nil
}

// gitRemote is how to reach a Git source's repository.
func (s *Service) gitRemote(src store.SiteSource) (gitsource.Remote, error) {
	addr, err := gitsource.ParseAddress(src.Repository)
	if err != nil {
		return gitsource.Remote{}, gitError(err)
	}
	r := gitsource.Remote{Address: addr}
	if addr.Scheme == gitsource.SchemeSSH {
		if r.Key, err = s.deploySigner(src); err != nil {
			return gitsource.Remote{}, err
		}
		var ok bool
		if r.HostKey, ok = parseKeyLine(src.HostKey); !ok {
			return gitsource.Remote{}, errorf(Conflict, "no server key was confirmed for this site; link it again")
		}
	}
	return r, nil
}

// reachGit runs fn against the source's repository and records how it
// went on the source (a changed server key, a key that was not accepted,
// or success).
func (s *Service) reachGit(ctx context.Context, src store.SiteSource, fn func(gitsource.Remote) error) error {
	if src.HostKeyNew != "" {
		return errorf(Conflict, "the server shows a different key than the one confirmed for this site; check the new key and accept it first")
	}
	r, err := s.gitRemote(src)
	if err != nil {
		return err
	}
	err = fn(r)
	checkErr, newKey := "", ""
	var hk *gitsource.HostKeyChangedError
	var bm *gitsource.BranchMissingError
	var mv *gitsource.BranchMovedError
	switch {
	case err == nil, errors.As(err, &bm), errors.As(err, &mv):
		// The repository was reached.
	case errors.As(err, &hk):
		if hk.Presented != nil {
			newKey = keyLine(hk.Presented)
		} else {
			checkErr = problemText(err)
		}
		slog.Warn("a Git server showed another key than the confirmed one", "site", src.Site, "server", r.Address.ServerName())
	case errors.Is(err, context.Canceled):
		return gitError(err)
	default:
		checkErr = problemText(err)
	}
	if serr := s.store.SetGitSourceCheck(context.WithoutCancel(ctx), src.Site, src.ID, checkErr, newKey); serr != nil &&
		!errors.Is(serr, store.ErrNotFound) {
		slog.Error("could not store the result of reaching a repository", "site", src.Site, "err", serr)
	}
	if err != nil {
		return gitError(err)
	}
	return nil
}

// CheckGitSiteSource connects to the repository with the deploy key and
// lists its branches.
func (s *Service) CheckGitSiteSource(ctx context.Context, a Actor, site string) (GitSourceCheck, error) {
	_, src, err := s.gitSource(ctx, a, site)
	if err != nil {
		return GitSourceCheck{}, err
	}
	if err := s.allowGitCheck(a); err != nil {
		return GitSourceCheck{}, err
	}
	var b gitsource.Branches
	ferr := s.reachGit(ctx, src, func(r gitsource.Remote) error {
		var err error
		b, err = gitsource.ListBranches(ctx, r)
		return err
	})
	if ferr != nil && KindOf(ferr) == Internal {
		return GitSourceCheck{}, ferr
	}
	cur, err := s.store.SiteSource(ctx, src.Site)
	if err != nil {
		return GitSourceCheck{}, storeError(err)
	}
	state, err := s.gitState(ctx, cur)
	if err != nil {
		return GitSourceCheck{}, err
	}
	out := GitSourceCheck{SiteSourceState: state, OK: ferr == nil, Branches: nonNil(b.Names), DefaultBranch: b.Default,
		Truncated: b.Truncated}
	if ferr == nil {
		for _, name := range b.Names {
			if name == cur.Branch {
				out.BranchFound = true
			}
		}
	}
	if ferr != nil && out.Source != nil && out.Source.Problem == "" {
		out.Source.Problem = ferr.Error()
	}
	return out, nil
}

// AcceptGitHostKey pins the key the SSH server shows now, if its
// fingerprint is the one the person confirmed.
func (s *Service) AcceptGitHostKey(ctx context.Context, a Actor, site, fingerprint string) (SiteSourceState, error) {
	st, src, err := s.gitSource(ctx, a, site)
	if err != nil {
		return SiteSourceState{}, err
	}
	addr, err := gitsource.ParseAddress(src.Repository)
	if err != nil {
		return SiteSourceState{}, gitError(err)
	}
	if addr.Scheme != gitsource.SchemeSSH {
		return SiteSourceState{}, errorf(Conflict, "only SSH servers have a key to accept")
	}
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return SiteSourceState{}, errorf(Invalid, "confirm the fingerprint of the new key")
	}
	if err := s.allowGitCheck(a); err != nil {
		return SiteSourceState{}, err
	}
	k, err := gitsource.ScanHostKey(ctx, addr)
	if err != nil {
		return SiteSourceState{}, gitError(err)
	}
	if got := ssh.FingerprintSHA256(k); got != fingerprint {
		return SiteSourceState{}, errorf(Conflict, "%s now shows the key %s, not %s which you confirmed; check it again", addr.ServerName(), got, fingerprint)
	}
	if err := s.store.SetGitHostKey(ctx, st.Name, src.ID, keyLine(k)); err != nil {
		return SiteSourceState{}, storeError(err)
	}
	s.record(ctx, a, "site.source-host-key", st.Name, fmt.Sprintf("%s, server key %s", addr.ServerName(), fingerprint))
	cur, err := s.store.SiteSource(ctx, st.Name)
	if err != nil {
		return SiteSourceState{}, storeError(err)
	}
	return s.gitState(ctx, cur)
}

// ReplaceGitDeployKey makes a new deploy key for the site. The old one
// stops working in the panel at once; remove it at the Git host.
func (s *Service) ReplaceGitDeployKey(ctx context.Context, a Actor, site string) (SiteSourceState, error) {
	st, src, err := s.gitSource(ctx, a, site)
	if err != nil {
		return SiteSourceState{}, err
	}
	if len(src.DeployKey) == 0 {
		return SiteSourceState{}, errorf(Conflict, "the site's repository is fetched over HTTPS and has no deploy key")
	}
	if err := s.needSecrets(); err != nil {
		return SiteSourceState{}, err
	}
	sealed, public, err := s.newDeployKey(st.Name)
	if err != nil {
		return SiteSourceState{}, newError(Internal, err)
	}
	if err := s.store.SetGitDeployKey(ctx, st.Name, src.ID, sealed, public); err != nil {
		return SiteSourceState{}, storeError(err)
	}
	s.record(ctx, a, "site.source-deploy-key", st.Name, "new deploy key")
	cur, err := s.store.SiteSource(ctx, st.Name)
	if err != nil {
		return SiteSourceState{}, storeError(err)
	}
	return s.gitState(ctx, cur)
}

// ReplaceGitNotifySecret makes a new notification address and secret; the
// old address stops working at once. The answer is the only place the new
// secret is shown.
func (s *Service) ReplaceGitNotifySecret(ctx context.Context, a Actor, site string) (SiteSourceState, error) {
	st, src, err := s.gitSource(ctx, a, site)
	if err != nil {
		return SiteSourceState{}, err
	}
	if err := s.needSecrets(); err != nil {
		return SiteSourceState{}, err
	}
	if a.Via == ViaSystem {
		return SiteSourceState{}, errorf(Forbidden, "only a person can make a new notification secret")
	}
	id, secret, sealed, err := s.newNotifySecret()
	if err != nil {
		return SiteSourceState{}, newError(Internal, err)
	}
	if err := s.store.SetGitNotifySecret(ctx, st.Name, src.ID, id, sealed); err != nil {
		return SiteSourceState{}, storeError(err)
	}
	s.record(ctx, a, "site.source-notify-secret", st.Name, "new notification address and secret")
	cur, err := s.store.SiteSource(ctx, st.Name)
	if err != nil {
		return SiteSourceState{}, storeError(err)
	}
	out, err := s.gitState(ctx, cur)
	if err != nil {
		return SiteSourceState{}, err
	}
	out.NotifySecret = secret
	return out, nil
}

// FetchGitSource fetches the commit of a site's Git source as a tar.gz of
// its files (strip 0) for a build. Only the commit the linked branch
// points at now is fetched: when commit is not "" and the branch moved on,
// it fails (a newer push has its own run). It is the kind-git half of the
// SourceFetcher of builds (#133); the caller closes tree, which removes
// its temporary file.
//
// Not reachable through the API or MCP: builds call it with the site of
// the run they build.
func (s *Service) FetchGitSource(ctx context.Context, site, commit string) (tree io.ReadCloser, strip int, err error) {
	src, err := s.store.SiteSource(ctx, site)
	if err != nil {
		return nil, 0, storeError(err)
	}
	if src.Kind != store.SourceGit {
		return nil, 0, errorf(Conflict, "the site is not linked to a Git repository by address")
	}
	if commit != "" && !validCommit(commit) {
		return nil, 0, errorf(Invalid, "%q is not a full commit id", commit)
	}
	select {
	case gitFetchSlots <- struct{}{}:
		defer func() { <-gitFetchSlots }()
	case <-ctx.Done():
		return nil, 0, gitError(ctx.Err())
	}
	var f gitsource.Fetched
	err = s.reachGit(ctx, src, func(r gitsource.Remote) error {
		var err error
		f, err = gitsource.Fetch(ctx, r, src.Branch, commit, "", gitsource.Limits{})
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	slog.Info("fetched a site's source", "site", site, "commit", f.Commit, "bytes", f.Size)
	return f.Tree, 0, nil
}

func validCommit(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}

// siteSources is the SourceFetcher of builds when Config.Sources is nil:
// sources of kind git are fetched by FetchGitSource; fetching a commit
// from a GitHub connection is not there yet.
type siteSources struct{ s *Service }

func (f siteSources) FetchSource(ctx context.Context, ref SourceRef) (io.ReadCloser, int, error) {
	src, err := f.s.store.SiteSource(ctx, ref.Site)
	if err != nil {
		return nil, 0, storeError(err)
	}
	if src.Kind != store.SourceGit {
		return nil, 0, errorf(Unavailable, "fetching a commit from a GitHub connection is not available yet")
	}
	return f.s.FetchGitSource(ctx, ref.Site, ref.Commit)
}
