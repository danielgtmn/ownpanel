package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// GitHub's deliveries to GitHubWebhookPath. They come without a session
// or token: the only proof is the signature with the webhook secret of the
// App named in the delivery. The order of checks (the HTTP side does the
// first ones, see internal/api/github_webhook.go):
//
//  1. method, content type, a hard limit on the body, an overall rate
//     limit (AllowGitHubDelivery) — before the App is looked up;
//  2. the App by GitHub's id in X-GitHub-Hook-Installation-Target-ID;
//  3. the HMAC-SHA256 signature of the raw body with that App's sealed
//     webhook secret, compared in constant time;
//  4. a rate limit per App (only deliveries with a valid signature count,
//     so it says nothing about which Apps exist);
//  5. the delivery id: one seen in the last GitHubDeliveryMemory is
//     ignored;
//  6. only then the JSON, strictly (one value, the expected types, valid
//     UTF-8), and the event.
//
// An unknown App, a wrong or missing signature and a malformed delivery
// all end in the same ErrDeliveryRejected, so the answer tells nothing
// about which Apps or repositories exist.
//
// Redeliveries: GitHub sends a redelivered delivery with the same
// X-GitHub-Delivery id. A delivery the panel accepted once is ignored when
// it comes again (that is the replay protection); a delivery the panel did
// not accept (it failed on the panel's side) is not remembered, so
// redelivering it on GitHub works. A push older than GitHubDeliveryMemory
// is ignored too, so an old delivery replayed after its id was forgotten
// cannot bring back an old commit.
//
// All work here is local (no request to GitHub), so GitHub gets its answer
// well within its 10 seconds; the build itself happens elsewhere (#113).

const (
	// GitHubDeliveryMemory is how long delivery ids are remembered. GitHub
	// lets people redeliver deliveries of the last 3 days.
	GitHubDeliveryMemory = 7 * 24 * time.Hour
	// githubDeliveriesMax bounds the remembered ids (the rate limits
	// allow fewer in GitHubDeliveryMemory in practice).
	githubDeliveriesMax = 200_000
	// githubDeliveryPrune is how often old ids are removed.
	githubDeliveryPrune = time.Hour
	// githubDeliveryTimeout bounds handling one delivery.
	githubDeliveryTimeout = 5 * time.Second

	// Rate limits: tokens refilled per second, and the burst.
	githubHookAllRate   = 10.0
	githubHookAllBurst  = 300.0
	githubHookAppRate   = 2.0
	githubHookAppBurst  = 120.0
	githubHookMaxBucket = 1024
)

// ErrDeliveryRejected is the one answer to a delivery that is not
// accepted, whatever the reason.
var ErrDeliveryRejected = errorf(Invalid, "the delivery was not accepted")

// GitHubDelivery is a delivery as it arrived. Body is the raw body (within
// the size limit).
type GitHubDelivery struct {
	ID         string
	Event      string
	TargetType string
	TargetID   string
	Signature  string
	Body       []byte
}

// Results of an accepted delivery.
const (
	DeliveryHandled   = "handled"
	DeliveryIgnored   = "ignored"
	DeliveryDuplicate = "duplicate"
)

// GitHubDeliveryResult says what happened. It contains nothing of the
// delivery's content.
type GitHubDeliveryResult struct {
	Result string `json:"result"`
	// Runs is how many runs the delivery created.
	Runs int `json:"runs"`
}

// rateBucket is a token bucket.
type rateBucket struct {
	tokens float64
	last   time.Time
}

func (b *rateBucket) take(now time.Time, rate, burst float64) bool {
	if b.last.IsZero() {
		b.tokens = burst
	} else if d := now.Sub(b.last).Seconds(); d > 0 {
		b.tokens = min(burst, b.tokens+d*rate)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type githubHooks struct {
	mu        sync.Mutex
	all       rateBucket
	apps      map[int64]*rateBucket
	lastPrune time.Time
}

var (
	deliveryIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	eventPattern      = regexp.MustCompile(`^[a-z_]{1,64}$`)
	appIDPattern      = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
)

// AllowGitHubDelivery takes one request from the overall limit of
// deliveries. Call it before reading the body.
func (s *Service) AllowGitHubDelivery() bool {
	h := &s.githubHooks
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.all.take(s.now(), githubHookAllRate, githubHookAllBurst)
}

func (s *Service) allowGitHubApp(id int64) bool {
	h := &s.githubHooks
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.apps == nil || len(h.apps) > githubHookMaxBucket {
		h.apps = map[int64]*rateBucket{}
	}
	b := h.apps[id]
	if b == nil {
		b = &rateBucket{}
		h.apps[id] = b
	}
	return b.take(s.now(), githubHookAppRate, githubHookAppBurst)
}

// unknownAppKey stands in for a webhook secret when the App is unknown, so
// that case costs the same work as a wrong signature.
var unknownAppKey = []byte("ownpanel: no such GitHub App")

// ReceiveGitHubDelivery checks and handles one delivery.
func (s *Service) ReceiveGitHubDelivery(ctx context.Context, d GitHubDelivery) (GitHubDeliveryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, githubDeliveryTimeout)
	defer cancel()

	app, secret := s.deliveryApp(ctx, d)
	if err := ctx.Err(); err != nil {
		return GitHubDeliveryResult{}, newError(Internal, err)
	}
	if !github.ValidSignature(secret, d.Body, d.Signature) || app == nil {
		return GitHubDeliveryResult{}, ErrDeliveryRejected
	}
	if !deliveryIDPattern.MatchString(d.ID) || !eventPattern.MatchString(d.Event) || !utf8.Valid(d.Body) {
		return GitHubDeliveryResult{}, ErrDeliveryRejected
	}
	if !s.allowGitHubApp(app.ID) {
		return GitHubDeliveryResult{}, errorf(Throttled, "too many deliveries; try again later")
	}

	s.pruneDeliveries(ctx)
	fresh, err := s.store.ClaimDelivery(ctx, d.ID, app.ID)
	if err != nil {
		return GitHubDeliveryResult{}, storeError(err)
	}
	if !fresh {
		slog.Info("GitHub delivery ignored: seen before", "delivery", d.ID, "event", d.Event, "connection", app.ID)
		return GitHubDeliveryResult{Result: DeliveryDuplicate}, nil
	}
	res, err := s.handleDelivery(ctx, *app, d)
	if err != nil {
		// Forget the id, so the same delivery is handled when GitHub sends
		// it again.
		if rerr := s.store.ReleaseDelivery(context.WithoutCancel(ctx), d.ID); rerr != nil {
			slog.Error("could not forget a GitHub delivery that failed", "delivery", d.ID, "err", rerr)
		}
		if errors.Is(err, errMalformed) {
			slog.Warn("GitHub delivery refused: unexpected content", "delivery", d.ID, "event", d.Event, "connection", app.ID)
			return GitHubDeliveryResult{}, ErrDeliveryRejected
		}
		return GitHubDeliveryResult{}, err
	}
	slog.Info("GitHub delivery", "delivery", d.ID, "event", d.Event, "connection", app.ID, "result", res.Result, "runs", res.Runs)
	return res, nil
}

// deliveryApp finds the App a delivery names and its webhook secret. For
// an unknown App (or one whose secret cannot be opened) it returns nil and
// a stand-in key.
func (s *Service) deliveryApp(ctx context.Context, d GitHubDelivery) (*store.GitHubApp, []byte) {
	if d.TargetType != github.TargetTypeApp || !appIDPattern.MatchString(d.TargetID) {
		return nil, unknownAppKey
	}
	id, err := strconv.ParseInt(d.TargetID, 10, 64)
	if err != nil {
		return nil, unknownAppKey
	}
	app, err := s.store.GitHubAppByAppID(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Error("could not look up the GitHub app of a delivery", "err", err)
		}
		return nil, unknownAppKey
	}
	if s.secrets == nil {
		return nil, unknownAppKey
	}
	secret, err := s.secrets.Open(app.WebhookSecret, githubWebhookPurpose)
	if err != nil {
		slog.Warn("a GitHub delivery cannot be checked: the connection's secret cannot be opened (the server's secret key changed?)", "connection", app.ID)
		return nil, unknownAppKey
	}
	return &app, secret
}

func (s *Service) pruneDeliveries(ctx context.Context) {
	h := &s.githubHooks
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
	if _, err := s.store.ForgetDeliveries(ctx, now.Add(-GitHubDeliveryMemory), githubDeliveriesMax); err != nil {
		slog.Error("could not remove old GitHub delivery ids", "err", err)
	}
}

// errMalformed: the delivery is signed but its content is not what GitHub
// sends.
var errMalformed = errors.New("malformed delivery")

// decodeStrict reads exactly one JSON object into v.
func decodeStrict(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return errMalformed
	}
	if _, err := dec.Token(); err != io.EOF {
		return errMalformed
	}
	return nil
}

// Parts of the payloads the panel reads. Everything else is ignored.
type hookRepository struct {
	ID       int64           `json:"id"`
	FullName string          `json:"full_name"`
	PushedAt json.RawMessage `json:"pushed_at"`
}

type hookInstallation struct {
	ID    int64 `json:"id"`
	AppID int64 `json:"app_id"`
}

type hookCommon struct {
	Action       string            `json:"action"`
	Installation *hookInstallation `json:"installation"`
	Repository   *hookRepository   `json:"repository"`
}

type hookPush struct {
	hookCommon
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	HeadCommit *struct {
		ID      string `json:"id"`
		Message string `json:"message"`
		Author  struct {
			Name     string `json:"name"`
			Username string `json:"username"`
		} `json:"author"`
	} `json:"head_commit"`
	Pusher struct {
		Name string `json:"name"`
	} `json:"pusher"`
}

type hookInstallationRepositories struct {
	hookCommon
	Added   []hookRepository `json:"repositories_added"`
	Removed []hookRepository `json:"repositories_removed"`
}

func (s *Service) handleDelivery(ctx context.Context, app store.GitHubApp, d GitHubDelivery) (GitHubDeliveryResult, error) {
	ignored := GitHubDeliveryResult{Result: DeliveryIgnored}
	switch d.Event {
	case "push":
		var p hookPush
		if err := decodeStrict(d.Body, &p); err != nil {
			return GitHubDeliveryResult{}, err
		}
		return s.handlePush(ctx, app, p)
	case "installation":
		var p hookCommon
		if err := decodeStrict(d.Body, &p); err != nil {
			return GitHubDeliveryResult{}, err
		}
		return s.handleInstallation(ctx, app, p)
	case "installation_repositories":
		var p hookInstallationRepositories
		if err := decodeStrict(d.Body, &p); err != nil {
			return GitHubDeliveryResult{}, err
		}
		return s.handleInstallationRepositories(ctx, app, p)
	case "repository":
		var p hookCommon
		if err := decodeStrict(d.Body, &p); err != nil {
			return GitHubDeliveryResult{}, err
		}
		return s.handleRepository(ctx, app, p)
	}
	// ping, pull_request (#116) and anything else: accepted, nothing to do.
	var obj map[string]json.RawMessage
	if err := decodeStrict(d.Body, &obj); err != nil {
		return GitHubDeliveryResult{}, err
	}
	return ignored, nil
}

// deliveryInstallation is the stored installation a delivery is about, if
// it belongs to the App that signed it.
func (s *Service) deliveryInstallation(ctx context.Context, app store.GitHubApp, p hookCommon) (store.GitHubInstallation, bool, error) {
	if p.Installation == nil || p.Installation.ID <= 0 {
		return store.GitHubInstallation{}, false, errMalformed
	}
	if p.Installation.AppID != 0 && p.Installation.AppID != app.AppID {
		return store.GitHubInstallation{}, false, nil
	}
	inst, err := s.store.GitHubInstallation(ctx, p.Installation.ID)
	if errors.Is(err, store.ErrNotFound) {
		return store.GitHubInstallation{}, false, nil
	}
	if err != nil {
		return store.GitHubInstallation{}, false, storeError(err)
	}
	return inst, inst.AppID == app.ID, nil
}

// pushTime reads repository.pushed_at: seconds since 1970 in push
// deliveries, a time in others.
func pushTime(raw json.RawMessage) (time.Time, bool) {
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		if sec, err := n.Int64(); err == nil {
			return time.Unix(sec, 0), true
		}
	}
	var t time.Time
	if err := json.Unmarshal(raw, &t); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func (s *Service) handlePush(ctx context.Context, app store.GitHubApp, p hookPush) (GitHubDeliveryResult, error) {
	ignored := GitHubDeliveryResult{Result: DeliveryIgnored}
	if p.Repository == nil || p.Repository.ID <= 0 || p.Ref == "" {
		return GitHubDeliveryResult{}, errMalformed
	}
	inst, ours, err := s.deliveryInstallation(ctx, app, p.hookCommon)
	if err != nil || !ours {
		return ignored, err
	}
	// Tags and other refs, and deleted branches, publish nothing.
	branch, ok := strings.CutPrefix(p.Ref, "refs/heads/")
	if !ok || p.Deleted || !github.ValidBranch(branch) {
		return ignored, nil
	}
	if !github.ValidCommitSHA(p.After) || strings.Trim(p.After, "0") == "" {
		return GitHubDeliveryResult{}, errMalformed
	}
	pushed, ok := pushTime(p.Repository.PushedAt)
	if now := s.now(); !ok || pushed.Before(now.Add(-GitHubDeliveryMemory)) || pushed.After(now.Add(24*time.Hour)) {
		slog.Warn("GitHub push ignored: too old or without a time", "connection", app.ID, "repository", p.Repository.ID)
		return ignored, nil
	}
	if github.ValidFullName(p.Repository.FullName) {
		if err := s.store.RenameSourceRepository(ctx, p.Repository.ID, p.Repository.FullName); err != nil {
			return GitHubDeliveryResult{}, storeError(err)
		}
	}
	if inst.Suspended {
		return ignored, nil
	}
	if gone, err := s.store.RepositoryGone(ctx, inst.ID, p.Repository.ID); err != nil || gone {
		return ignored, storeErrorOrNil(err)
	}
	sources, err := s.store.SiteSourcesOfRepository(ctx, p.Repository.ID)
	if err != nil {
		return GitHubDeliveryResult{}, storeError(err)
	}
	run := store.Run{
		Trigger: RunTriggerPush, SourceKind: store.SourceGitHub, RepositoryID: p.Repository.ID, Ref: p.Ref,
		CommitSHA: p.After, Pusher: plainText(p.Pusher.Name, runNameMax),
	}
	if c := p.HeadCommit; c != nil && c.ID == p.After {
		run.Message = plainText(c.Message, runMessageMax)
		run.AuthorName = plainText(c.Author.Name, runNameMax)
		if github.ValidLogin(c.Author.Username) {
			run.AuthorLogin = c.Author.Username
		}
	}
	out := GitHubDeliveryResult{Result: DeliveryIgnored}
	for _, src := range sources {
		// Only by GitHub's ids: the repository and the installation the
		// site was linked through.
		if src.InstallationID != inst.ID || src.Branch != branch || !src.PublishOnPush {
			continue
		}
		r := run
		r.Site, r.Repository = src.Site, src.Repository
		if _, err := s.store.AddRun(ctx, r, runSupersededDetail, RunsKept); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue // the site was just removed
			}
			return GitHubDeliveryResult{}, storeError(err)
		}
		out.Result = DeliveryHandled
		out.Runs++
	}
	return out, nil
}

func (s *Service) handleInstallation(ctx context.Context, app store.GitHubApp, p hookCommon) (GitHubDeliveryResult, error) {
	ignored := GitHubDeliveryResult{Result: DeliveryIgnored}
	inst, ours, err := s.deliveryInstallation(ctx, app, p)
	if err != nil || !ours {
		return ignored, err
	}
	switch p.Action {
	case "deleted":
		// Its sites keep their link and show "connection missing".
		err = s.store.DeleteGitHubInstallation(ctx, app.ID, inst.ID)
	case "suspend":
		err = s.store.SetGitHubInstallationSuspended(ctx, app.ID, inst.ID, true)
	case "unsuspend":
		err = s.store.SetGitHubInstallationSuspended(ctx, app.ID, inst.ID, false)
	default:
		return ignored, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return GitHubDeliveryResult{}, storeError(err)
	}
	s.githubRepos.clear()
	return GitHubDeliveryResult{Result: DeliveryHandled}, nil
}

func (s *Service) handleInstallationRepositories(ctx context.Context, app store.GitHubApp, p hookInstallationRepositories) (GitHubDeliveryResult, error) {
	ignored := GitHubDeliveryResult{Result: DeliveryIgnored}
	inst, ours, err := s.deliveryInstallation(ctx, app, p.hookCommon)
	if err != nil || !ours {
		return ignored, err
	}
	var list []hookRepository
	gone := false
	switch p.Action {
	case "removed":
		list, gone = p.Removed, true
	case "added":
		list = p.Added
	default:
		return ignored, nil
	}
	for _, r := range list {
		if r.ID <= 0 {
			return GitHubDeliveryResult{}, errMalformed
		}
		if err := s.store.SetRepositoryGone(ctx, inst.ID, r.ID, gone); err != nil {
			return GitHubDeliveryResult{}, storeError(err)
		}
	}
	s.githubRepos.clear()
	return GitHubDeliveryResult{Result: DeliveryHandled}, nil
}

func (s *Service) handleRepository(ctx context.Context, app store.GitHubApp, p hookCommon) (GitHubDeliveryResult, error) {
	ignored := GitHubDeliveryResult{Result: DeliveryIgnored}
	if p.Repository == nil || p.Repository.ID <= 0 {
		return GitHubDeliveryResult{}, errMalformed
	}
	inst, ours, err := s.deliveryInstallation(ctx, app, p)
	if err != nil || !ours {
		return ignored, err
	}
	switch p.Action {
	case "renamed", "transferred":
		if !github.ValidFullName(p.Repository.FullName) {
			return GitHubDeliveryResult{}, errMalformed
		}
		err = s.store.RenameSourceRepository(ctx, p.Repository.ID, p.Repository.FullName)
	case "deleted":
		err = s.store.SetRepositoryGone(ctx, inst.ID, p.Repository.ID, true)
	default:
		return ignored, nil
	}
	if err != nil {
		return GitHubDeliveryResult{}, storeError(err)
	}
	s.githubRepos.clear()
	return GitHubDeliveryResult{Result: DeliveryHandled}, nil
}

func storeErrorOrNil(err error) error {
	if err == nil {
		return nil
	}
	return storeError(err)
}
