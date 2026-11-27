package panel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/gitsource"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// Notifications of Git hosts for sources of kind git (#111), to
// GitNotifyPath + "<notify id>". Like GitHub's deliveries they come
// without a session or token: the only proof is the secret of the
// address. The order of checks (the HTTP side does the first ones, see
// internal/api/gitnotify.go):
//
//  1. method, content type, a hard limit on the body, an overall rate
//     limit (AllowGitNotification) — before the source is looked up;
//  2. the source by the id in the address (32 hex digits);
//  3. the proof, in whichever form the host sends it, compared in
//     constant time: an HMAC-SHA256 of the raw body with the secret in
//     X-Hub-Signature-256 (GitHub, Gitea, Forgejo), X-Hub-Signature with
//     "sha256=" (Bitbucket; a "sha1=" one, which Gitea and GitHub add, is
//     ignored), X-Gitea-Signature, X-Forgejo-Signature or
//     X-Gogs-Signature (hex), or the secret itself in X-Gitlab-Token
//     (GitLab sends no signature). At least one must be present and every
//     one present must be right;
//  4. a rate limit per source (only proven notifications count);
//  5. the delivery id the host sends (X-GitHub-Delivery,
//     X-Gitea-Delivery, X-Forgejo-Delivery, X-Gogs-Delivery,
//     X-Gitlab-Event-UUID, X-Request-UUID, X-Request-Id): one seen for the
//     source in the last GitHubDeliveryMemory is ignored;
//  6. only then the JSON, strictly, and the event.
//
// An unknown address, a wrong or missing proof and a malformed body all
// end in the same ErrDeliveryRejected; an unknown address costs the same
// work as a wrong proof.
//
// What a notification can do: record a run for its own source's site,
// for a push to the linked branch while "publish on push" is on. The
// commit it names is never trusted for more than that: a build fetches
// only the commit the branch points at when it runs and fails the run if
// the branch moved on (FetchGitSource). A replayed or forged-by-the-host
// notification can at most build the branch's current commit again.

const (
	gitHookAllRate   = 10.0
	gitHookAllBurst  = 300.0
	gitHookSrcRate   = 1.0
	gitHookSrcBurst  = 60.0
	gitDeliveriesMax = 200_000
)

var (
	notifyIDPattern    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	gitDeliveryPattern = regexp.MustCompile(`^[A-Za-z0-9._:{}-]{1,128}$`)
)

// GitNotification is a notification as it arrived: the id from its
// address, its headers and its raw body (within the size limit).
type GitNotification struct {
	ID     string
	Header http.Header
	Body   []byte
}

type gitHooks struct {
	mu        sync.Mutex
	all       rateBucket
	sources   map[string]*rateBucket
	lastPrune time.Time
}

// AllowGitNotification takes one request from the overall limit of
// notifications. Call it before reading the body.
func (s *Service) AllowGitNotification() bool {
	h := &s.gitHooks
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.all.take(s.now(), gitHookAllRate, gitHookAllBurst)
}

func (s *Service) allowGitSource(id string) bool {
	h := &s.gitHooks
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sources == nil || len(h.sources) > githubHookMaxBucket {
		h.sources = map[string]*rateBucket{}
	}
	b := h.sources[id]
	if b == nil {
		b = &rateBucket{}
		h.sources[id] = b
	}
	return b.take(s.now(), gitHookSrcRate, gitHookSrcBurst)
}

// unknownSourceKey stands in for a secret when the address is unknown.
var unknownSourceKey = []byte("ownpanel: no such notification address")

// notifySource finds the source of a notification and its secret; for an
// unknown address it returns nil and a stand-in secret.
func (s *Service) notifySource(ctx context.Context, id string) (*store.SiteSource, []byte) {
	if !notifyIDPattern.MatchString(id) || s.secrets == nil {
		return nil, unknownSourceKey
	}
	src, err := s.store.GitSourceByNotifyID(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Error("could not look up the source of a notification", "err", err)
		}
		return nil, unknownSourceKey
	}
	secret, err := s.secrets.Open(src.NotifySecret, gitNotifyPurpose)
	if err != nil {
		slog.Warn("a notification cannot be checked: its secret cannot be opened (the server's secret key changed?)", "site", src.Site)
		return nil, unknownSourceKey
	}
	return &src, secret
}

// validProof checks the proofs a notification carries against secret: at
// least one, and every one that is there must be right. Every form is
// computed and compared, so the time says nothing about which failed.
func validProof(secret, body []byte, h http.Header) bool {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	sum := mac.Sum(nil)
	seen, ok := 0, 1
	check := func(v string, prefix string) {
		if v == "" {
			return
		}
		seen++
		hexed, found := strings.CutPrefix(v, prefix)
		got, err := hex.DecodeString(hexed)
		if !found || err != nil || len(got) != sha256.Size {
			got = make([]byte, sha256.Size)
			ok = 0
		}
		ok &= subtle.ConstantTimeCompare(got, sum)
	}
	check(h.Get("X-Hub-Signature-256"), "sha256=")
	// Bitbucket signs with SHA-256 here; Gitea, Forgejo and GitHub put a
	// SHA-1 signature here next to their SHA-256 ones. SHA-1 is neither
	// trusted nor held against the notification.
	if v := h.Get("X-Hub-Signature"); !strings.HasPrefix(v, "sha1=") {
		check(v, "sha256=")
	}
	check(h.Get("X-Gitea-Signature"), "")
	check(h.Get("X-Forgejo-Signature"), "")
	check(h.Get("X-Gogs-Signature"), "")
	if tok := h.Get("X-Gitlab-Token"); tok != "" {
		seen++
		// Hashes of equal length, so the length tells nothing.
		a, b := sha256.Sum256([]byte(tok)), sha256.Sum256(secret)
		ok &= subtle.ConstantTimeCompare(a[:], b[:])
	}
	return seen > 0 && ok == 1
}

func firstHeader(h http.Header, names ...string) string {
	for _, n := range names {
		if v := h.Get(n); v != "" {
			return v
		}
	}
	return ""
}

// ReceiveGitNotification checks and handles one notification.
func (s *Service) ReceiveGitNotification(ctx context.Context, n GitNotification) (GitHubDeliveryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, githubDeliveryTimeout)
	defer cancel()
	src, secret := s.notifySource(ctx, n.ID)
	if err := ctx.Err(); err != nil {
		return GitHubDeliveryResult{}, newError(Internal, err)
	}
	if !validProof(secret, n.Body, n.Header) || src == nil {
		return GitHubDeliveryResult{}, ErrDeliveryRejected
	}
	if !utf8.Valid(n.Body) {
		return GitHubDeliveryResult{}, ErrDeliveryRejected
	}
	if !s.allowGitSource(src.ID) {
		return GitHubDeliveryResult{}, errorf(Throttled, "too many notifications; try again later")
	}
	delivery := firstHeader(n.Header, github.HeaderDelivery, "X-Gitea-Delivery", "X-Forgejo-Delivery", "X-Gogs-Delivery",
		"X-Gitlab-Event-UUID", "X-Request-UUID", "X-Request-Id")
	// Every host the panel supports names its deliveries; one without an
	// id could be replayed without anyone noticing.
	if !gitDeliveryPattern.MatchString(delivery) {
		return GitHubDeliveryResult{}, ErrDeliveryRejected
	}
	event := firstHeader(n.Header, github.HeaderEvent, "X-Gitea-Event", "X-Forgejo-Event", "X-Gogs-Event", "X-Gitlab-Event",
		"X-Event-Key")

	s.pruneGitDeliveries(ctx)
	fresh, err := s.store.ClaimGitDelivery(ctx, src.ID, delivery)
	if err != nil {
		return GitHubDeliveryResult{}, storeError(err)
	}
	if !fresh {
		slog.Info("Git notification ignored: seen before", "site", src.Site, "delivery", delivery)
		return GitHubDeliveryResult{Result: DeliveryDuplicate}, nil
	}
	res, err := s.handleGitNotification(ctx, *src, event, n.Body)
	if err != nil {
		if rerr := s.store.ReleaseGitDelivery(context.WithoutCancel(ctx), src.ID, delivery); rerr != nil {
			slog.Error("could not forget a notification that failed", "site", src.Site, "err", rerr)
		}
		if errors.Is(err, errMalformed) {
			slog.Warn("Git notification refused: unexpected content", "site", src.Site, "delivery", delivery)
			return GitHubDeliveryResult{}, ErrDeliveryRejected
		}
		return GitHubDeliveryResult{}, err
	}
	slog.Info("Git notification", "site", src.Site, "delivery", delivery, "event", plainText(event, 40), "result", res.Result, "runs", res.Runs)
	return res, nil
}

func (s *Service) pruneGitDeliveries(ctx context.Context) {
	h := &s.gitHooks
	h.mu.Lock()
	now := s.now()
	due := now.Sub(h.lastPrune) >= githubDeliveryPrune || now.Before(h.lastPrune)
	if due {
		h.lastPrune = now
	}
	h.mu.Unlock()
	if !due {
		return
	}
	if _, err := s.store.ForgetGitDeliveries(ctx, now.Add(-GitHubDeliveryMemory), gitDeliveriesMax); err != nil {
		slog.Error("could not remove old notification ids", "err", err)
	}
}

// gitPush is what the panel reads of the push notifications of GitHub,
// Gitea, Forgejo, Gogs and GitLab (ref, after and the commits), and of
// Bitbucket Cloud (push.changes) and Bitbucket Server (changes).
type gitPush struct {
	Ref        string      `json:"ref"`
	After      string      `json:"after"`
	HeadCommit *gitCommit  `json:"head_commit"`
	Commits    []gitCommit `json:"commits"`
	Pusher     *struct {
		Name     string `json:"name"`
		Username string `json:"username"`
		Login    string `json:"login"`
	} `json:"pusher"`
	UserName string `json:"user_name"` // GitLab
	// Bitbucket Cloud.
	Push *struct {
		Changes []struct {
			New *struct {
				Type   string `json:"type"`
				Name   string `json:"name"`
				Target struct {
					Hash    string `json:"hash"`
					Message string `json:"message"`
					Author  struct {
						Raw string `json:"raw"`
					} `json:"author"`
				} `json:"target"`
			} `json:"new"`
		} `json:"changes"`
	} `json:"push"`
	Actor *struct {
		DisplayName string `json:"display_name"` // Bitbucket Cloud
		Name        string `json:"name"`         // Bitbucket Server
	} `json:"actor"`
	// Bitbucket Server.
	Changes []struct {
		Ref struct {
			ID string `json:"id"`
		} `json:"ref"`
		ToHash string `json:"toHash"`
		Type   string `json:"type"`
	} `json:"changes"`
}

type gitCommit struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Author  struct {
		Name     string `json:"name"`
		Username string `json:"username"`
	} `json:"author"`
}

// pushEvent reports whether the event header names a push (or there is
// none, and the body decides).
func pushEvent(event string) (push, known bool) {
	switch strings.ToLower(event) {
	case "push", "push hook", "repo:push", "repo:refs_changed":
		return true, true
	case "":
		return true, false
	}
	return false, true
}

func (s *Service) handleGitNotification(ctx context.Context, src store.SiteSource, event string, body []byte) (GitHubDeliveryResult, error) {
	ignored := GitHubDeliveryResult{Result: DeliveryIgnored}
	push, _ := pushEvent(event)
	if !push {
		// ping, tag pushes in GitLab ("Tag Push Hook"), pull requests…
		var obj map[string]json.RawMessage
		if err := decodeStrict(body, &obj); err != nil {
			return GitHubDeliveryResult{}, err
		}
		return ignored, nil
	}
	var p gitPush
	if err := decodeStrict(body, &p); err != nil {
		return GitHubDeliveryResult{}, err
	}
	ref, commit, message, author, pusher := "", "", "", "", ""
	switch {
	case p.Ref != "":
		ref, commit = p.Ref, p.After
		c := p.HeadCommit
		if c == nil || c.ID != commit {
			c = nil
			for i := range p.Commits {
				if p.Commits[i].ID == commit {
					c = &p.Commits[i]
				}
			}
		}
		if c != nil {
			message, author = c.Message, c.Author.Name
			if author == "" {
				author = c.Author.Username
			}
		}
		if p.Pusher != nil {
			pusher = firstNonEmpty(p.Pusher.Name, p.Pusher.Login, p.Pusher.Username)
		}
		pusher = firstNonEmpty(pusher, p.UserName)
	case p.Push != nil:
		for _, ch := range p.Push.Changes {
			if ch.New != nil && ch.New.Type == "branch" && ch.New.Name == src.Branch {
				ref, commit = "refs/heads/"+ch.New.Name, ch.New.Target.Hash
				message = ch.New.Target.Message
				author, _, _ = strings.Cut(ch.New.Target.Author.Raw, " <")
			}
		}
		if p.Actor != nil {
			pusher = p.Actor.DisplayName
		}
	case len(p.Changes) > 0:
		for _, ch := range p.Changes {
			if ch.Ref.ID == "refs/heads/"+src.Branch && ch.Type != "DELETE" {
				ref, commit = ch.Ref.ID, ch.ToHash
			}
		}
		if p.Actor != nil {
			pusher = p.Actor.Name
		}
	default:
		// Not a push the panel knows (or a test notification).
		return ignored, nil
	}
	if ref == "" {
		return ignored, nil
	}
	branch, ok := strings.CutPrefix(ref, "refs/heads/")
	if !ok || !github.ValidBranch(branch) {
		return ignored, nil // tags and other refs
	}
	if strings.Trim(commit, "0") == "" {
		return ignored, nil // a deleted branch
	}
	if !validCommit(commit) {
		// SHA-256 repositories are not supported yet.
		return GitHubDeliveryResult{}, errMalformed
	}
	if branch != src.Branch || !src.PublishOnPush {
		return ignored, nil
	}
	// The notification only says that the branch changed: the panel asks
	// the repository where it points now and records a run of that commit
	// (gitTips). Message, author and pusher are taken from the
	// notification only when it names that very commit.
	s.queueTipCheck(src, pushHint{commit: commit, message: message, author: author, pusher: pusher})
	return GitHubDeliveryResult{Result: DeliveryAccepted}, nil
}

// DeliveryAccepted: the notification was accepted; the panel asks the
// repository where the branch points and records a run of that commit.
const DeliveryAccepted = "accepted"

// pushHint is what a notification says about a push: untrusted, used only
// for display when it matches the commit the panel finds itself.
type pushHint struct {
	commit, message, author, pusher string
}

// gitTips resolves where a site's branch points after notifications, one
// at a time per source: notifications that arrive while one is resolved
// are folded into one more round.
type gitTips struct {
	mu      sync.Mutex
	pending map[string]*tipJob // by source row id
	wg      sync.WaitGroup
}

type tipJob struct {
	again bool
	hint  pushHint
}

// tipCheckTimeout bounds one round of resolving a branch (waiting for a
// check slot included).
const tipCheckTimeout = gitsource.CheckTimeout + gitSlotWait

func (s *Service) queueTipCheck(src store.SiteSource, hint pushHint) {
	t := &s.gitTips
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending == nil {
		t.pending = map[string]*tipJob{}
	}
	if j := t.pending[src.ID]; j != nil {
		j.again, j.hint = true, hint
		return
	}
	j := &tipJob{hint: hint}
	t.pending[src.ID] = j
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		for {
			t.mu.Lock()
			hint := j.hint
			j.again = false
			t.mu.Unlock()
			s.recordTipRun(src.Site, src.ID, hint)
			t.mu.Lock()
			if !j.again {
				delete(t.pending, src.ID)
				t.mu.Unlock()
				return
			}
			t.mu.Unlock()
		}
	}()
}

// recordTipRun asks the repository where the linked branch points and
// records a run of that commit, unless the site's newest run that makes a
// version live already is one of it that is waiting, building or published (a notification sent
// again, or several for one push).
func (s *Service) recordTipRun(site, sourceID string, hint pushHint) {
	ctx, cancel := context.WithTimeout(context.Background(), tipCheckTimeout)
	defer cancel()
	src, err := s.store.SiteSource(ctx, site)
	if err != nil || src.ID != sourceID || src.Kind != store.SourceGit || !src.PublishOnPush {
		return // unlinked or changed meanwhile
	}
	release, err := s.gitCheckSlots.acquire(ctx, "site:"+site, gitSlotWait)
	if err != nil {
		slog.Warn("a push could not be checked: the panel is busy", "site", site)
		return
	}
	defer release()
	var tip string
	if err := s.reachGit(ctx, src, func(r gitsource.Remote) error {
		var err error
		tip, err = gitsource.BranchTip(ctx, r, src.Branch)
		return err
	}); err != nil {
		slog.Info("a push could not be checked", "site", site, "err", err)
		return
	}
	if !validCommit(tip) {
		return // SHA-256 repositories are not supported yet
	}
	// Only runs that make a version live count: a preview of a pull
	// request of the same commit (a GitHub link the site had before) is
	// not the push.
	if last, err := s.store.NewestRun(ctx, site, ""); err == nil && last.CommitSHA == tip &&
		(last.Status == store.RunQueued || last.Status == store.RunRunning || last.Status == store.RunSucceeded) {
		return
	}
	run := store.Run{Site: site, Trigger: RunTriggerPush, SourceKind: store.SourceGit, Repository: src.Repository,
		Ref: "refs/heads/" + src.Branch, CommitSHA: tip}
	if hint.commit == tip {
		run.Message = plainText(hint.message, runMessageMax)
		run.AuthorName = plainText(hint.author, runNameMax)
		run.Pusher = plainText(hint.pusher, runNameMax)
	}
	if _, err := s.store.AddRun(ctx, run, runSupersededDetail, RunsKept); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Error("could not record a run", "site", site, "err", err)
		}
		return
	}
	s.wakeRunner()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
