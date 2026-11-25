package panel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Updates. The panel shows the installed and the available versions and can
// ask for an update, but it never runs anything as root: it only writes a
// small request file into UpdatesConfig.Dir. ownpanel-update.path notices it
// and starts ownpanel-update.service (root), which checks the request
// strictly, removes it and runs update.sh. update.sh reports its progress in
// status.json in the same directory, which the panel reads. Automatic updates
// are configured the same way, with auto.json, and carried out by
// ownpanel-update.timer. See deploy/ownpanel-update.

// Update states, as reported in UpdateStatus.State.
const (
	UpdateIdle       = "idle"
	UpdateRequested  = "requested"
	UpdateRunning    = "running"
	UpdateSucceeded  = "succeeded"
	UpdateFailed     = "failed"
	UpdateRolledBack = "rolled_back"
)

// Files in UpdatesConfig.Dir.
const (
	updateRequestFile = "request.json"
	updateStatusFile  = "status.json"
	updateAutoFile    = "auto.json"
)

const (
	// releasesCacheTTL is how long a successful release lookup is reused;
	// a failed one is retried after releasesRetryTTL.
	releasesCacheTTL = 30 * time.Minute
	releasesRetryTTL = 2 * time.Minute
	// releasesCheckGap is how soon after the last lookup someone may ask
	// for another one (POST /api/updates/check). GitHub allows 60
	// unauthenticated requests an hour per address.
	releasesCheckGap = time.Minute
	releasesTimeout  = 5 * time.Second
	// updateStaleAfter: a "running" status older than this is an update
	// that died without reporting. systemd stops ownpanel-update.service
	// after 30 minutes (TimeoutStartSec) and update.sh then reports
	// "failed" itself; this only covers a run killed without that chance.
	updateStaleAfter  = 35 * time.Minute
	defaultAutoWindow = "03:00-05:00"
	// keyUpdateRecorded remembers which finished update is in the activity log.
	keyUpdateRecorded = "update_recorded"
)

var (
	// stableVersionRe is what a stable release may be called; the root side
	// accepts exactly the same.
	stableVersionRe  = regexp.MustCompile(`^v(\d{1,4})\.(\d{1,4})\.(\d{1,4})$`)
	nightlyVersionRe = regexp.MustCompile(`^nightly-(\d{8})-[0-9a-f]+$`)
	nightlyInTitleRe = regexp.MustCompile(`nightly-\d{8}-[0-9a-f]+`)
	autoWindowRe     = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)-([01]\d|2[0-3]):([0-5]\d)$`)
)

// UpdatesConfig tells the panel where updates come from and how it talks to
// the root-side updater.
type UpdatesConfig struct {
	// Version is the running version of ownpaneld.
	Version string
	// Repo is the GitHub repository ("owner/name") releases come from.
	Repo string
	// APIURL is GitHub's API; "" means https://api.github.com.
	APIURL string
	// ReleaseFile is install.sh's record of the installed release and
	// channel. Updates from the panel are only offered when it exists.
	ReleaseFile string
	// Dir holds request.json, auto.json (written here) and status.json
	// (written by the updater).
	Dir string
	// Arch is the machine's architecture as in the archive names; "" means
	// the one ownpaneld was built for.
	Arch string
	// Client makes the release lookups; nil means a client with a 5 s
	// timeout.
	Client *http.Client
}

// InstalledRelease is the running version and the channel it follows.
type InstalledRelease struct {
	Version string `json:"version"`
	Channel string `json:"channel"`
}

// AvailableRelease is the newest release of a channel.
type AvailableRelease struct {
	Version     string    `json:"version"`
	PublishedAt time.Time `json:"published_at"`
	NotesURL    string    `json:"notes_url"`
	// Older: installing it would go back to an older version.
	Older bool `json:"older"`
}

// AvailableReleases are the newest releases per channel; nil if there is none
// (or the lookup failed).
type AvailableReleases struct {
	Stable  *AvailableRelease `json:"stable"`
	Nightly *AvailableRelease `json:"nightly"`
}

// AutoUpdate is the automatic update setting: off, or a channel and a daily
// time window (server time, "HH:MM-HH:MM", may wrap past midnight).
type AutoUpdate struct {
	Mode   string `json:"mode"`
	Window string `json:"window"`
}

// UpdateStatus is the progress or the result of the last update.
type UpdateStatus struct {
	State      string `json:"state"`
	Step       string `json:"step"`
	Message    string `json:"message"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	From       string `json:"from"`
	To         string `json:"to"`
	id         string
}

// UpdateInfo is everything the update page shows.
type UpdateInfo struct {
	Installed   InstalledRelease  `json:"installed"`
	Available   AvailableReleases `json:"available"`
	CheckFailed bool              `json:"check_failed"`
	// CheckError says why the last lookup failed; Available then still
	// holds the result of the last one that worked.
	CheckError string `json:"check_error"`
	// CheckedAt is when the releases were last looked up (RFC3339), "" if
	// never.
	CheckedAt       string       `json:"checked_at"`
	UpdateAvailable bool         `json:"update_available"`
	Auto            AutoUpdate   `json:"auto"`
	Status          UpdateStatus `json:"status"`
	// Supported is false when ownpanel was not installed by install.sh
	// (e.g. in development): then it cannot update itself.
	Supported bool `json:"supported"`
}

// UpdateRequest asks for an update: the newest release of Channel, or
// exactly Version.
type UpdateRequest struct {
	Channel        string
	Version        string
	AllowDowngrade bool
}

type updater struct {
	cfg UpdatesConfig

	mu      sync.Mutex // guards the cache
	fetched time.Time  // when the releases were last looked up
	ttl     time.Duration
	latest  AvailableReleases    // the last lookup that worked
	dates   map[string]time.Time // publication dates by version; nil before the first that worked
	err     error                // of the last lookup

	writeMu  sync.Mutex // serializes requests and settings
	recordMu sync.Mutex

	// pending is the version the last request asks for, shown while it waits.
	pendingMu sync.Mutex
	pending   string
}

func newUpdater(cfg UpdatesConfig) *updater {
	if cfg.APIURL == "" {
		cfg.APIURL = "https://api.github.com"
	}
	if cfg.Arch == "" {
		cfg.Arch = runtime.GOARCH
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: releasesTimeout}
	}
	return &updater{cfg: cfg}
}

func (u *updater) path(name string) string { return filepath.Join(u.cfg.Dir, name) }

// supported: installed by install.sh, with the updater's directory.
func (u *updater) supported() bool {
	if u.cfg.ReleaseFile == "" || u.cfg.Dir == "" {
		return false
	}
	if _, err := os.Stat(u.cfg.ReleaseFile); err != nil {
		return false
	}
	st, err := os.Stat(u.cfg.Dir)
	return err == nil && st.IsDir()
}

// installedChannel reads the channel from the release file; "" if unknown.
func (u *updater) installedChannel() string {
	if u.cfg.ReleaseFile == "" {
		return ""
	}
	b, err := os.ReadFile(u.cfg.ReleaseFile)
	if err != nil {
		return ""
	}
	channel := ""
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "channel="); ok {
			channel = v
		}
	}
	if channel != "stable" && channel != "nightly" {
		return ""
	}
	return channel
}

// githubRelease is what the panel reads of GitHub's release list.
type githubRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

// releaseLookup is the newest release per channel as last looked up.
type releaseLookup struct {
	latest  AvailableReleases
	dates   map[string]time.Time
	err     error     // of the last lookup; latest is then the one before
	checked time.Time // when the last lookup was made
}

// releases returns the newest release per channel, looked up at most every
// 30 minutes. After a failed lookup it returns the error together with the
// result of the last one that worked.
func (u *updater) releases(ctx context.Context, now time.Time) (AvailableReleases, map[string]time.Time, error) {
	r := u.lookup(ctx, now, false)
	return r.latest, r.dates, r.err
}

// lookup returns the cached releases, looked up again once the cache is
// stale or, with force, unless the last lookup was less than a minute ago.
// A failed lookup keeps the previous result.
func (u *updater) lookup(ctx context.Context, now time.Time, force bool) releaseLookup {
	u.mu.Lock()
	defer u.mu.Unlock()
	age := now.Sub(u.fetched)
	fresh := !u.fetched.IsZero() && age < u.ttl
	if force {
		fresh = !u.fetched.IsZero() && age < releasesCheckGap
	}
	if !fresh {
		latest, dates, err := u.fetch(ctx)
		u.fetched, u.err = now, err
		u.ttl = releasesCacheTTL
		if err != nil {
			u.ttl = releasesRetryTTL
			slog.Warn("could not look up ownpanel releases", "err", err)
		} else {
			u.latest, u.dates = latest, dates
		}
	}
	return releaseLookup{latest: u.latest, dates: u.dates, err: u.err, checked: u.fetched}
}

func (u *updater) fetch(ctx context.Context) (AvailableReleases, map[string]time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, releasesTimeout)
	defer cancel()
	url := strings.TrimRight(u.cfg.APIURL, "/") + "/repos/" + u.cfg.Repo + "/releases?per_page=50"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return AvailableReleases{}, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ownpanel/"+u.cfg.Version)
	resp, err := u.cfg.Client.Do(req)
	if err != nil {
		return AvailableReleases{}, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return AvailableReleases{}, nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var list []githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&list); err != nil {
		return AvailableReleases{}, nil, fmt.Errorf("reading the release list: %w", err)
	}
	return pickReleases(list, u.cfg.Arch)
}

// pickReleases finds the newest stable release (highest version) and the
// nightly build in GitHub's list. A release only counts when it has the
// archive for this machine and checksums.txt, so one that is still being
// published is skipped, like the updater would refuse it.
func pickReleases(list []githubRelease, arch string) (AvailableReleases, map[string]time.Time, error) {
	var out AvailableReleases
	dates := map[string]time.Time{}
	complete := func(r githubRelease, version string) bool {
		var archive, sums bool
		for _, a := range r.Assets {
			archive = archive || a.Name == "ownpanel_"+version+"_linux_"+arch+".tar.gz"
			sums = sums || a.Name == "checksums.txt"
		}
		return archive && sums
	}
	for _, r := range list {
		if r.Draft {
			continue
		}
		if r.TagName == "nightly" {
			v := nightlyInTitleRe.FindString(r.Name)
			if v == "" || !complete(r, v) {
				continue
			}
			dates[v] = r.PublishedAt
			out.Nightly = &AvailableRelease{Version: v, PublishedAt: r.PublishedAt, NotesURL: r.HTMLURL}
			continue
		}
		if r.Prerelease || !stableVersionRe.MatchString(r.TagName) {
			continue
		}
		dates[r.TagName] = r.PublishedAt
		if !complete(r, r.TagName) {
			continue
		}
		if out.Stable == nil || compareStable(r.TagName, out.Stable.Version) > 0 {
			out.Stable = &AvailableRelease{Version: r.TagName, PublishedAt: r.PublishedAt, NotesURL: r.HTMLURL}
		}
	}
	return out, dates, nil
}

// compareStable compares two stable versions like v1.2.3.
func compareStable(a, b string) int {
	ma, mb := stableVersionRe.FindStringSubmatch(a), stableVersionRe.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(ma[i])
		y, _ := strconv.Atoi(mb[i])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// isOlder tells whether installing target means going back from installed.
// Two stable versions compare by number; otherwise by the day they were
// built or published (nightly builds carry it in their name). Unknown means
// not older.
func isOlder(target, installed string, dates map[string]time.Time) bool {
	if target == installed {
		return false
	}
	if stableVersionRe.MatchString(target) && stableVersionRe.MatchString(installed) {
		return compareStable(target, installed) < 0
	}
	day := func(v string) (time.Time, bool) {
		if m := nightlyVersionRe.FindStringSubmatch(v); m != nil {
			t, err := time.Parse("20060102", m[1])
			return t, err == nil
		}
		t, ok := dates[v]
		if !ok {
			return time.Time{}, false
		}
		t = t.UTC()
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), true
	}
	td, ok1 := day(target)
	id, ok2 := day(installed)
	return ok1 && ok2 && td.Before(id)
}

// readStatus returns the updater's status. A request that is still waiting
// is "requested".
func (u *updater) readStatus(now time.Time) UpdateStatus {
	idle := UpdateStatus{State: UpdateIdle}
	if u.cfg.Dir == "" {
		return idle
	}
	if _, err := os.Lstat(u.path(updateRequestFile)); err == nil {
		u.pendingMu.Lock()
		to := u.pending
		u.pendingMu.Unlock()
		return UpdateStatus{State: UpdateRequested, Step: "waiting", From: u.cfg.Version, To: to}
	}
	st, err := parseUpdateStatus(u.path(updateStatusFile))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("could not read the update status", "err", err)
		}
		return idle
	}
	if st.State == UpdateRunning {
		if t, err := time.Parse(time.RFC3339, st.StartedAt); err == nil && now.Sub(t) > updateStaleAfter {
			st.State = UpdateFailed
			st.Step = ""
			st.Message = "The update stopped without reporting a result. The server's log for ownpanel-update tells more."
		}
	}
	return st
}

// parseUpdateStatus reads status.json; unknown states count as idle.
func parseUpdateStatus(path string) (UpdateStatus, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return UpdateStatus{}, err
	}
	var raw struct {
		ID         string `json:"id"`
		State      string `json:"state"`
		Step       string `json:"step"`
		Message    string `json:"message"`
		StartedAt  string `json:"started_at"`
		FinishedAt string `json:"finished_at"`
		From       string `json:"from"`
		To         string `json:"to"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return UpdateStatus{}, fmt.Errorf("%s: %w", path, err)
	}
	st := UpdateStatus{State: raw.State, Step: raw.Step, Message: raw.Message, StartedAt: raw.StartedAt,
		FinishedAt: raw.FinishedAt, From: raw.From, To: raw.To, id: raw.ID}
	switch st.State {
	case UpdateRunning, UpdateSucceeded, UpdateFailed, UpdateRolledBack:
	default:
		return UpdateStatus{State: UpdateIdle}, nil
	}
	return st, nil
}

func (st UpdateStatus) finished() bool {
	return st.State == UpdateSucceeded || st.State == UpdateFailed || st.State == UpdateRolledBack
}

func (st UpdateStatus) busy() bool {
	return st.State == UpdateRequested || st.State == UpdateRunning
}

// readAuto returns the automatic update setting; off if not set.
func (u *updater) readAuto() AutoUpdate {
	off := AutoUpdate{Mode: "off", Window: defaultAutoWindow}
	if u.cfg.Dir == "" {
		return off
	}
	b, err := os.ReadFile(u.path(updateAutoFile))
	if err != nil {
		return off
	}
	var a AutoUpdate
	if json.Unmarshal(b, &a) != nil || validAuto(a) != nil {
		return off
	}
	return a
}

func validAuto(a AutoUpdate) error {
	switch a.Mode {
	case "off", "stable", "nightly":
	default:
		return errorf(Invalid, "mode must be off, stable or nightly")
	}
	m := autoWindowRe.FindStringSubmatch(a.Window)
	if m == nil {
		return errorf(Invalid, "the time window must look like 03:00-05:00")
	}
	if m[1]+m[2] == m[3]+m[4] {
		return errorf(Invalid, "the time window must not start and end at the same time")
	}
	return nil
}

// writeFile replaces dir/name atomically with data, readable by the group
// (root reads it anyway; the mode keeps other users out).
func (u *updater) writeFile(name string, data []byte) error {
	f, err := os.CreateTemp(u.cfg.Dir, "."+name+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o640); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, u.path(name))
}

// updateInfo puts together what GET /api/updates shows.
func (s *Service) updateInfo(ctx context.Context) UpdateInfo {
	return s.lookUpUpdates(ctx, false)
}

// lookUpUpdates is updateInfo; check asks GitHub again unless it was asked
// less than a minute ago.
func (s *Service) lookUpUpdates(ctx context.Context, check bool) UpdateInfo {
	u := s.updates
	now := s.now()
	channel := u.installedChannel()
	info := UpdateInfo{
		Installed: InstalledRelease{Version: u.cfg.Version, Channel: channel},
		Auto:      u.readAuto(),
		Status:    u.readStatus(now),
		Supported: u.supported(),
	}
	r := u.lookup(ctx, now, check)
	latest, dates := r.latest, r.dates
	if !r.checked.IsZero() {
		info.CheckedAt = r.checked.UTC().Format(time.RFC3339)
	}
	if r.err != nil {
		info.CheckFailed = true
		info.CheckError = r.err.Error()
		if dates == nil {
			return info
		}
	}
	mark := func(r *AvailableRelease) *AvailableRelease {
		if r == nil {
			return nil
		}
		c := *r
		c.Older = isOlder(c.Version, u.cfg.Version, dates)
		return &c
	}
	info.Available = AvailableReleases{Stable: mark(latest.Stable), Nightly: mark(latest.Nightly)}
	cur := info.Available.Stable
	if channel == "nightly" {
		cur = info.Available.Nightly
	}
	info.UpdateAvailable = info.Supported && cur != nil && cur.Version != u.cfg.Version && !cur.Older
	return info
}

func (s *Service) needUpdates(a Actor) error {
	if err := needAdmin(a); err != nil {
		return err
	}
	if s.updates == nil {
		return errorf(Internal, "updates are not configured")
	}
	return nil
}

func (s *Service) needUpdatable() error {
	if !s.updates.supported() {
		return errorf(Conflict, "This panel was not set up with the installer, so it can't update itself. Update it the way you installed it.")
	}
	return nil
}

// Updates returns the installed and available versions, the automatic update
// setting and the progress of a running update.
func (s *Service) Updates(ctx context.Context, a Actor) (UpdateInfo, error) {
	if err := s.needUpdates(a); err != nil {
		return UpdateInfo{}, err
	}
	s.RecordUpdateResult(ctx)
	return s.updateInfo(ctx), nil
}

// CheckUpdates is Updates, but looks for new releases now instead of
// relying on the last lookup. To spare GitHub's limit it looks at most once
// a minute; a check within that minute answers with the last result, and
// checked_at tells when that was.
func (s *Service) CheckUpdates(ctx context.Context, a Actor) (UpdateInfo, error) {
	if err := s.needUpdates(a); err != nil {
		return UpdateInfo{}, err
	}
	s.RecordUpdateResult(ctx)
	return s.lookUpUpdates(ctx, true), nil
}

// RequestUpdate asks the updater to install the newest release of a channel
// or one version. It refuses while an update is under way, and going back to
// an older version unless AllowDowngrade is set.
func (s *Service) RequestUpdate(ctx context.Context, a Actor, r UpdateRequest) (UpdateInfo, error) {
	if err := s.needUpdates(a); err != nil {
		return UpdateInfo{}, err
	}
	if err := s.needUpdatable(); err != nil {
		return UpdateInfo{}, err
	}
	switch {
	case (r.Channel == "") == (r.Version == ""):
		return UpdateInfo{}, errorf(Invalid, "give either a channel or a version")
	case r.Channel != "" && r.Channel != "stable" && r.Channel != "nightly":
		return UpdateInfo{}, errorf(Invalid, "channel must be stable or nightly")
	case r.Version != "" && !stableVersionRe.MatchString(r.Version):
		return UpdateInfo{}, errorf(Invalid, "version must look like v1.2.3")
	}
	u := s.updates
	u.writeMu.Lock()
	defer u.writeMu.Unlock()
	now := s.now()
	if u.readStatus(now).busy() {
		return UpdateInfo{}, errorf(Conflict, "an update is already under way")
	}
	installedChannel := u.installedChannel()
	latest, dates, err := u.releases(ctx, now)
	target, channel := r.Version, r.Channel
	if r.Channel != "" {
		if err != nil {
			return UpdateInfo{}, errorf(Unavailable, "could not check for new versions right now; try again in a moment")
		}
		rel := latest.Stable
		if r.Channel == "nightly" {
			rel = latest.Nightly
		}
		if rel == nil {
			return UpdateInfo{}, errorf(Conflict, "there is no %s release to install yet", r.Channel)
		}
		target = rel.Version
	} else {
		channel = "stable"
	}
	if target == u.cfg.Version && channel == installedChannel {
		return UpdateInfo{}, errorf(Conflict, "%s is already installed", target)
	}
	older := isOlder(target, u.cfg.Version, dates)
	if older && !r.AllowDowngrade {
		return UpdateInfo{}, errorf(Conflict, "%s is older than the installed %s; confirm that you want to go back to it", target, u.cfg.Version)
	}

	idb := make([]byte, 16)
	if _, err := rand.Read(idb); err != nil {
		return UpdateInfo{}, newError(Internal, err)
	}
	body, err := json.Marshal(struct {
		ID      string `json:"id"`
		Channel string `json:"channel,omitempty"`
		Version string `json:"version,omitempty"`
	}{hex.EncodeToString(idb), r.Channel, r.Version})
	if err != nil {
		return UpdateInfo{}, newError(Internal, err)
	}
	u.pendingMu.Lock()
	u.pending = target
	u.pendingMu.Unlock()
	if err := u.writeFile(updateRequestFile, body); err != nil {
		return UpdateInfo{}, newError(Internal, fmt.Errorf("write update request: %w", err))
	}
	detail := fmt.Sprintf("%s to %s (%s)", u.cfg.Version, target, channel)
	if older {
		detail += ", an older version"
	}
	s.record(ctx, a, "update.request", "", detail)

	// Automatic updates follow the installed channel; move them along.
	if auto := u.readAuto(); auto.Mode != "off" && auto.Mode != channel {
		auto.Mode = channel
		if err := u.writeAuto(auto); err != nil {
			slog.Warn("could not move automatic updates to the new channel", "err", err)
		}
	}
	return s.updateInfo(ctx), nil
}

func (u *updater) writeAuto(a AutoUpdate) error {
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return u.writeFile(updateAutoFile, body)
}

// SetAutoUpdate turns automatic updates off or on for the installed channel,
// within a daily time window.
func (s *Service) SetAutoUpdate(ctx context.Context, a Actor, auto AutoUpdate) (UpdateInfo, error) {
	if err := s.needUpdates(a); err != nil {
		return UpdateInfo{}, err
	}
	if err := s.needUpdatable(); err != nil {
		return UpdateInfo{}, err
	}
	auto.Mode = strings.TrimSpace(auto.Mode)
	auto.Window = strings.TrimSpace(auto.Window)
	if auto.Window == "" {
		auto.Window = defaultAutoWindow
	}
	if err := validAuto(auto); err != nil {
		return UpdateInfo{}, err
	}
	u := s.updates
	if ch := u.installedChannel(); auto.Mode != "off" && auto.Mode != ch {
		return UpdateInfo{}, errorf(Conflict, "automatic updates follow the installed channel (%s); switch channels first", orUnknown(ch))
	}
	u.writeMu.Lock()
	defer u.writeMu.Unlock()
	if u.readAuto() != auto {
		if err := u.writeAuto(auto); err != nil {
			return UpdateInfo{}, newError(Internal, fmt.Errorf("write automatic update setting: %w", err))
		}
		detail := "off"
		if auto.Mode != "off" {
			detail = fmt.Sprintf("%s releases between %s", auto.Mode, strings.Replace(auto.Window, "-", " and ", 1))
		}
		s.record(ctx, a, "update.auto", "", detail)
	}
	return s.updateInfo(ctx), nil
}

func orUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

// WatchUpdateResult records the result of an update that restarted the
// daemon. The updater only reports success after the restarted daemon
// answered, so at startup the status may still say "running": keep looking
// for a while.
func (s *Service) WatchUpdateResult(ctx context.Context, every, upTo time.Duration) {
	if s.updates == nil {
		return
	}
	deadline := time.Now().Add(upTo)
	for {
		s.RecordUpdateResult(ctx)
		if !s.updates.readStatus(s.now()).busy() || time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// RecordUpdateResult writes the result of the last finished update to the
// activity log, once. It runs after the daemon starts (an update restarts
// it) and whenever the update page is read.
func (s *Service) RecordUpdateResult(ctx context.Context) {
	if s.updates == nil || s.store == nil {
		return
	}
	u := s.updates
	u.recordMu.Lock()
	defer u.recordMu.Unlock()
	st := u.readStatus(s.now())
	if !st.finished() || st.FinishedAt == "" {
		return
	}
	key := st.id + "@" + st.FinishedAt
	m, err := s.store.Settings(ctx)
	if err != nil {
		slog.Warn("could not read settings", "err", err)
		return
	}
	if m[keyUpdateRecorded] == key {
		return
	}
	if err := s.store.PutSettings(ctx, map[string]string{keyUpdateRecorded: key}); err != nil {
		slog.Warn("could not remember the recorded update", "err", err)
		return
	}
	var detail string
	switch st.State {
	case UpdateSucceeded:
		if st.From == st.To {
			detail = st.To + " was already installed"
		} else {
			detail = fmt.Sprintf("updated from %s to %s", st.From, st.To)
		}
	case UpdateRolledBack:
		detail = fmt.Sprintf("update from %s to %s failed; %s was put back", st.From, st.To, st.From)
	default:
		detail = "update failed"
		if st.To != "" {
			detail = "update to " + st.To + " failed"
		}
		if st.Message != "" {
			detail += ": " + st.Message
		}
	}
	if st.id == "auto" {
		detail += " (automatic)"
	}
	s.record(ctx, System, "update.result", "", detail)
}
