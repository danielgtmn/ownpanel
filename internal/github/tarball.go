package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrTooLarge: an archive was longer than the caller allowed.
var ErrTooLarge = errors.New("the archive from GitHub is larger than allowed")

// Tarball returns the source tree of one commit as GitHub's tar.gz, whose
// names all start with one folder GitHub adds ("owner-name-sha/"). The
// token must be allowed to read the repository's contents; sha is the full
// commit id. The stream fails with ErrTooLarge once it is longer than max
// bytes; ctx bounds the whole download, so it must stay alive until the
// stream is read (the client's own 30 s limit does not apply here).
//
// GitHub answers with a redirect to a short-lived download address that
// carries its own credentials. The token is only sent to the API; the
// download address is never logged or put into an error.
func (c *Client) Tarball(ctx context.Context, token Token, owner, name, sha string, max int64) (io.ReadCloser, error) {
	if token.value == "" {
		return nil, &Error{Kind: ErrUnauthorized}
	}
	base, err := repoPath(owner, name)
	if err != nil {
		return nil, err
	}
	if !validSHA(sha) {
		return nil, &Error{Kind: ErrInvalid, Message: "not a commit id"}
	}
	hc := *c.hc
	hc.Timeout = 0 // ctx bounds it
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.api+base+"/tarball/"+sha, nil)
	if err != nil {
		return nil, &Error{Kind: ErrInvalid, Message: "the request could not be built"}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+token.value)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, transportError(ctx, err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return limitBody(resp.Body, max), nil
	case http.StatusFound, http.StatusTemporaryRedirect, http.StatusMovedPermanently, http.StatusSeeOther:
		resp.Body.Close()
	default:
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		return nil, statusError(resp, data)
	}
	loc, err := resp.Location()
	if err != nil || !c.downloadAllowed(loc) {
		return nil, badAnswer("the download address is not usable")
	}
	// The download address is a new request without the token. Redirects
	// beyond it are followed (a few at most) and never get a token either.
	hc.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !c.downloadAllowed(r.URL) {
			return errors.New("too many or unusable redirects")
		}
		return nil
	}
	dl, err := http.NewRequestWithContext(ctx, http.MethodGet, loc.String(), nil)
	if err != nil {
		return nil, badAnswer("the download address is not usable")
	}
	dl.Header.Set("User-Agent", userAgent)
	resp, err = hc.Do(dl)
	if err != nil {
		return nil, transportError(ctx, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		return nil, statusError(resp, data)
	}
	if resp.ContentLength > max {
		resp.Body.Close()
		return nil, ErrTooLarge
	}
	return limitBody(resp.Body, max), nil
}

// downloadAllowed: https anywhere (GitHub's download host differs from the
// API's), or http on a loopback address when the client itself talks to
// one (a stand-in for tests).
func (c *Client) downloadAllowed(u *url.URL) bool {
	if u == nil || u.Host == "" || u.User != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return true
	case "http":
		return c.web.Scheme == "http" && isLoopback(u.Hostname())
	}
	return false
}

// limitBody fails the stream with ErrTooLarge once more than max bytes
// arrive.
func limitBody(rc io.ReadCloser, max int64) io.ReadCloser {
	return &limitedBody{rc: rc, left: max}
}

type limitedBody struct {
	rc   io.ReadCloser
	left int64
}

func (l *limitedBody) Read(p []byte) (int, error) {
	if l.left <= 0 {
		var one [1]byte
		if n, _ := l.rc.Read(one[:]); n > 0 {
			return 0, ErrTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.rc.Read(p)
	l.left -= int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		// Network errors may carry the download address.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
	}
	return n, err
}

func (l *limitedBody) Close() error { return l.rc.Close() }

// Commit states of SetCommitStatus.
const (
	StatusPending = "pending"
	StatusSuccess = "success"
	StatusFailure = "failure"
	StatusError   = "error"
)

// CommitStatus is a status on a commit, as GitHub shows it next to the
// commit and in pull requests.
type CommitStatus struct {
	State string `json:"state"`
	// TargetURL is where "Details" leads; "" for none.
	TargetURL   string `json:"target_url,omitempty"`
	Description string `json:"description,omitempty"`
	// Context names the status; a later status with the same context
	// replaces it.
	Context string `json:"context"`
}

// SetCommitStatus sets a status on a commit. The token must be allowed to
// write statuses of the repository.
func (c *Client) SetCommitStatus(ctx context.Context, token Token, owner, name, sha string, st CommitStatus) error {
	if token.value == "" {
		return &Error{Kind: ErrUnauthorized}
	}
	base, err := repoPath(owner, name)
	if err != nil {
		return err
	}
	if !validSHA(sha) {
		return &Error{Kind: ErrInvalid, Message: "not a commit id"}
	}
	switch st.State {
	case StatusPending, StatusSuccess, StatusFailure, StatusError:
	default:
		return &Error{Kind: ErrInvalid, Message: "not a commit state"}
	}
	// GitHub's limit for descriptions is 140 characters.
	if r := []rune(st.Description); len(r) > 140 {
		st.Description = string(r[:139]) + "…"
	}
	_, err = c.do(ctx, http.MethodPost, base+"/statuses/"+sha, nil, "Bearer "+token.value, st, nil, http.StatusCreated)
	return err
}
