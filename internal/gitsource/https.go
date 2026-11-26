package gitsource

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"time"
)

// HTTPS: Git's "smart" protocol over HTTP, for public repositories (no
// credentials are ever sent). Certificates are checked against the
// system's roots; redirects are followed only to HTTPS on the same host.

// testRoots, when set (tests), replace the system's roots.
var testRoots *x509.CertPool

const userAgent = "git/2.0 (ownpanel)"

func httpClient() *http.Client {
	tr := &http.Transport{
		DialContext:            dialer().DialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: testRoots},
		TLSHandshakeTimeout:    dialTimeout,
		ResponseHeaderTimeout:  time.Minute,
		MaxResponseHeaderBytes: 64 << 10,
		DisableCompression:     true,
		ForceAttemptHTTP2:      true,
		Proxy:                  nil,
	}
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" || req.URL.Hostname() != via[0].URL.Hostname() || req.URL.User != nil {
				return errors.New("redirect to another server")
			}
			return nil
		},
	}
}

// HTTPError: the server answered with an HTTP status that is not success.
type HTTPError struct {
	Server string
	Status int
}

func (e *HTTPError) Error() string {
	switch e.Status {
	case http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Sprintf("%s has no public repository at this address (over HTTPS the panel only fetches public repositories; use an SSH address and a deploy key for private ones)", e.Server)
	}
	return fmt.Sprintf("%s answered with HTTP status %d", e.Server, e.Status)
}

func repoURL(a Address, suffix string) string {
	u := url.URL{Scheme: "https", Host: a.Host, Path: a.Path + suffix}
	if a.Port != 443 {
		u.Host = netJoin(a.Host, a.Port)
	}
	return u.String()
}

func httpFailure(a Address, err error, ctx context.Context) error {
	if ctx.Err() != nil {
		return &ConnectError{Server: a.ServerName(), Err: ctx.Err()}
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil && ue.Err.Error() == "redirect to another server" {
		return &ServerError{Msg: "the address leads to another server; use the repository's address on that server"}
	}
	return &ConnectError{Server: a.ServerName(), Err: err}
}

// httpAdvert asks for the ref advertisement.
func httpAdvert(ctx context.Context, c *http.Client, a Address, ol openLimits) (advert, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, repoURL(a, "/info/refs")+"?service=git-upload-pack", nil)
	if err != nil {
		return advert{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/x-git-upload-pack-advertisement")
	resp, err := c.Do(req)
	if err != nil {
		return advert{}, httpFailure(a, err, ctx)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return advert{}, &HTTPError{Server: a.ServerName(), Status: resp.StatusCode}
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != "application/x-git-upload-pack-advertisement" {
		return advert{}, &HTTPError{Server: a.ServerName(), Status: http.StatusNotFound}
	}
	p := newPktReader(&rawLimit{r: resp.Body, max: int64(ol.advertBytes) + 64<<10})
	line, err := p.next()
	if err != nil || string(bytes.TrimSuffix(line, []byte("\n"))) != "# service=git-upload-pack" {
		return advert{}, protocolf("no service line")
	}
	if _, err := p.next(); !errors.Is(err, errFlush) {
		return advert{}, protocolf("no flush after the service line")
	}
	return readAdvert(p, ol.branch, ol.advertBytes)
}

// httpPack sends the fetch request and returns the answer's body, read up
// to the pack.
func httpPack(ctx context.Context, c *http.Client, a Address, body []byte, ad advert, transfer int64) (io.ReadCloser, io.Reader, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, repoURL(a, "/git-upload-pack"), bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	req.Header.Set("Accept", "application/x-git-upload-pack-result")
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, httpFailure(a, err, ctx)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, nil, &HTTPError{Server: a.ServerName(), Status: resp.StatusCode}
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != "application/x-git-upload-pack-result" {
		resp.Body.Close()
		return nil, nil, protocolf("unexpected type of answer")
	}
	// Every raw byte of the answer counts, side-band progress included.
	p := newPktReader(&rawLimit{r: resp.Body, max: transfer})
	if err := readUntilPack(p); err != nil {
		resp.Body.Close()
		return nil, nil, err
	}
	return resp.Body, packStream(p, ad), nil
}

// UseRoots makes HTTPS connections trust only roots (for tests with their
// own certificate authority).
func UseRoots(roots *x509.CertPool) { testRoots = roots }
