package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"
)

const (
	maxPEMBytes = 16 << 10
	minKeyBits  = 2048
	// jwtLifetime stays below GitHub's limit of 10 minutes, counted from a
	// start one minute in the past (GitHub's advice against clock drift).
	jwtLifetime = 9 * time.Minute
	jwtBackdate = 60 * time.Second
)

// ---- wire types ----

type wireAccount struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

func (w wireAccount) account() (Account, error) {
	if !ValidLogin(w.Login) || !validAccountType(w.Type) {
		return Account{}, badAnswer("an account is not a user or organisation")
	}
	return Account(w), nil
}

type wireApp struct {
	ID       int64       `json:"id"`
	Slug     string      `json:"slug"`
	Name     string      `json:"name"`
	ClientID string      `json:"client_id"`
	Owner    wireAccount `json:"owner"`
	HTMLURL  string      `json:"html_url"`
}

func (c *Client) app(w wireApp) (App, error) {
	owner, err := w.Owner.account()
	if err != nil {
		return App{}, err
	}
	switch {
	case w.ID <= 0:
		return App{}, badAnswer("the App has no id")
	case !validSlug(w.Slug):
		return App{}, badAnswer("the App's short name is not valid")
	case !validText(w.Name, 100):
		return App{}, badAnswer("the App's name is not valid")
	case !clientIDPattern.MatchString(w.ClientID):
		return App{}, badAnswer("the App's client id is not valid")
	case !c.webURLOn(w.HTMLURL):
		return App{}, badAnswer("the App's address is not on %s", c.web.Host)
	}
	return App{ID: w.ID, Slug: w.Slug, Name: w.Name, ClientID: w.ClientID, Owner: owner, HTMLURL: w.HTMLURL}, nil
}

type wireInstallation struct {
	ID                  int64       `json:"id"`
	AppID               int64       `json:"app_id"`
	Account             wireAccount `json:"account"`
	RepositorySelection string      `json:"repository_selection"`
	SuspendedAt         *string     `json:"suspended_at"`
}

func (w wireInstallation) installation() (Installation, error) {
	acc, err := w.Account.account()
	if err != nil {
		return Installation{}, err
	}
	if w.ID <= 0 || w.AppID <= 0 {
		return Installation{}, badAnswer("an installation has no id")
	}
	if w.RepositorySelection != SelectionAll && w.RepositorySelection != SelectionSelected {
		return Installation{}, badAnswer("an installation has an unknown repository selection")
	}
	return Installation{
		ID: w.ID, AppID: w.AppID, Account: acc,
		RepositorySelection: w.RepositorySelection, Suspended: w.SuspendedAt != nil,
	}, nil
}

type wireRepository struct {
	ID            int64       `json:"id"`
	Name          string      `json:"name"`
	FullName      string      `json:"full_name"`
	Owner         wireAccount `json:"owner"`
	Private       bool        `json:"private"`
	Archived      bool        `json:"archived"`
	DefaultBranch string      `json:"default_branch"`
	HTMLURL       string      `json:"html_url"`
}

func (c *Client) repository(w wireRepository) (Repository, error) {
	if w.ID <= 0 || !repoNamePattern.MatchString(w.Name) || !ValidLogin(w.Owner.Login) ||
		w.FullName != w.Owner.Login+"/"+w.Name {
		return Repository{}, badAnswer("a repository has an invalid name")
	}
	if !validBranch(w.DefaultBranch) {
		return Repository{}, badAnswer("the repository %s has an invalid default branch", w.FullName)
	}
	if !c.webURLOn(w.HTMLURL) {
		return Repository{}, badAnswer("the repository %s is not on %s", w.FullName, c.web.Host)
	}
	return Repository{
		ID: w.ID, Owner: w.Owner.Login, Name: w.Name, FullName: w.FullName,
		Private: w.Private, Archived: w.Archived, DefaultBranch: w.DefaultBranch, HTMLURL: w.HTMLURL,
	}, nil
}

// ---- creating an App ----

// ConvertManifest exchanges the temporary code from the manifest flow for
// the new App and its credentials. The code works once, for one hour.
func (c *Client) ConvertManifest(ctx context.Context, code string) (AppCredentials, error) {
	if !ValidCode(code) {
		return AppCredentials{}, &Error{Kind: ErrInvalid, Message: "the code from GitHub is not valid"}
	}
	var w struct {
		wireApp
		ClientSecret  string  `json:"client_secret"`
		WebhookSecret *string `json:"webhook_secret"`
		PEM           string  `json:"pem"`
	}
	if _, err := c.do(ctx, http.MethodPost, "/app-manifests/"+url.PathEscape(code)+"/conversions", nil, "", nil, &w, http.StatusCreated); err != nil {
		return AppCredentials{}, err
	}
	app, err := c.app(w.wireApp)
	if err != nil {
		return AppCredentials{}, err
	}
	if !validSecret(w.ClientSecret) {
		return AppCredentials{}, badAnswer("the App's client secret is missing")
	}
	if w.WebhookSecret == nil || !validSecret(*w.WebhookSecret) {
		return AppCredentials{}, badAnswer("the App's webhook secret is missing")
	}
	key, err := ParsePrivateKey([]byte(w.PEM))
	if err != nil {
		return AppCredentials{}, badAnswer("the App's private key is not usable")
	}
	return AppCredentials{
		App:           app,
		PrivateKey:    pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		WebhookSecret: *w.WebhookSecret,
		ClientSecret:  w.ClientSecret,
	}, nil
}

// ParsePrivateKey reads an App's private key: PEM, PKCS #1 (as GitHub
// hands it out) or PKCS #8, RSA with at least 2048 bits.
func ParsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
	if len(data) == 0 || len(data) > maxPEMBytes {
		return nil, errors.New("no private key")
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("not an RSA private key")
		}
		key = k
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("not a private key")
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("not an RSA private key")
		}
		key = rk
	default:
		return nil, errors.New("not a private key")
	}
	if key.N.BitLen() < minKeyBits {
		return nil, errors.New("the key is too short")
	}
	return key, nil
}

// ---- signed in as the App ----

// AppAuth makes requests as one App, with a JWT signed by its private key.
type AppAuth struct {
	c      *Client
	issuer string
	key    *rsa.PrivateKey
}

// AsApp signs in as the App with the given client id (GitHub's
// recommendation for the JWT's issuer; the App id works too) and private
// key.
func (c *Client) AsApp(issuer string, privateKey []byte) (*AppAuth, error) {
	if !clientIDPattern.MatchString(issuer) {
		return nil, errors.New("the App's client id is not valid")
	}
	key, err := ParsePrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	return &AppAuth{c: c, issuer: issuer, key: key}, nil
}

// JWT returns a token that authenticates as the App for a few minutes. It
// is a credential: never log or store it.
func (a *AppAuth) JWT() (string, error) {
	now := a.c.now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(struct {
		IAT int64  `json:"iat"`
		EXP int64  `json:"exp"`
		ISS string `json:"iss"`
	}{now.Add(-jwtBackdate).Unix(), now.Add(-jwtBackdate + jwtLifetime).Unix(), a.issuer})
	if err != nil {
		return "", err
	}
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", errors.New("the JWT could not be signed")
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (a *AppAuth) bearer() (string, error) {
	jwt, err := a.JWT()
	if err != nil {
		return "", err
	}
	return "Bearer " + jwt, nil
}

// Installation returns one installation of the App; ErrNotFound if it does
// not exist or belongs to another App.
func (a *AppAuth) Installation(ctx context.Context, id int64) (Installation, error) {
	if id <= 0 {
		return Installation{}, &Error{Kind: ErrNotFound}
	}
	auth, err := a.bearer()
	if err != nil {
		return Installation{}, err
	}
	var w wireInstallation
	if _, err := a.c.do(ctx, http.MethodGet, "/app/installations/"+strconv.FormatInt(id, 10), nil, auth, nil, &w, http.StatusOK); err != nil {
		return Installation{}, err
	}
	inst, err := w.installation()
	if err != nil {
		return Installation{}, err
	}
	if inst.ID != id {
		return Installation{}, badAnswer("GitHub answered with another installation")
	}
	return inst, nil
}

// Installations lists every installation of the App.
func (a *AppAuth) Installations(ctx context.Context) ([]Installation, error) {
	auth, err := a.bearer()
	if err != nil {
		return nil, err
	}
	out := []Installation{}
	for page := 1; page <= a.c.maxPages; page++ {
		var list []wireInstallation
		q := url.Values{"per_page": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)}}
		if _, err := a.c.do(ctx, http.MethodGet, "/app/installations", q, auth, nil, &list, http.StatusOK); err != nil {
			return nil, err
		}
		for _, w := range list {
			inst, err := w.installation()
			if err != nil {
				return nil, err
			}
			if !slices.ContainsFunc(out, func(i Installation) bool { return i.ID == inst.ID }) {
				out = append(out, inst)
			}
		}
		if len(list) < perPage {
			return out, nil
		}
	}
	return out, nil
}

// TokenRequest narrows an installation token. GitHub never grants more
// than the installation has; empty fields mean everything it has.
type TokenRequest struct {
	RepositoryIDs []int64
	Permissions   map[string]string
}

// Token is an installation access token. It expires after an hour at the
// latest. It prints as "[redacted]" so it cannot end up in a log by
// accident; use Value where it is sent.
type Token struct {
	value     string
	ExpiresAt time.Time
}

// NewToken wraps a token value (for tests and callers that received one).
func NewToken(value string, expiresAt time.Time) Token {
	return Token{value: value, ExpiresAt: expiresAt}
}

// Value is the secret itself.
func (t Token) Value() string { return t.value }

func (t Token) String() string               { return "[redacted]" }
func (t Token) GoString() string             { return "github.Token{[redacted]}" }
func (t Token) LogValue() slog.Value         { return slog.StringValue("[redacted]") }
func (t Token) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

// InstallationToken creates a short-lived token for an installation,
// narrowed down by req.
func (a *AppAuth) InstallationToken(ctx context.Context, installationID int64, req TokenRequest) (Token, error) {
	if installationID <= 0 {
		return Token{}, &Error{Kind: ErrNotFound}
	}
	auth, err := a.bearer()
	if err != nil {
		return Token{}, err
	}
	body := struct {
		RepositoryIDs []int64           `json:"repository_ids,omitempty"`
		Permissions   map[string]string `json:"permissions,omitempty"`
	}{req.RepositoryIDs, req.Permissions}
	var w struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if _, err := a.c.do(ctx, http.MethodPost, "/app/installations/"+strconv.FormatInt(installationID, 10)+"/access_tokens", nil, auth, body, &w, http.StatusCreated); err != nil {
		return Token{}, err
	}
	// Tokens may be longer than 40 characters (GitHub's newer format).
	if len(w.Token) > 4096 || !validSecret(w.Token) || w.ExpiresAt.IsZero() {
		return Token{}, badAnswer("the installation token is missing")
	}
	return Token{value: w.Token, ExpiresAt: w.ExpiresAt}, nil
}

// ---- as an installation ----

// InstallationRepositories lists the repositories the installation token
// may see, in GitHub's order. truncated reports that there were more than
// the client reads (see WithMaxPages).
func (c *Client) InstallationRepositories(ctx context.Context, token Token) (repos []Repository, truncated bool, err error) {
	if token.value == "" {
		return nil, false, &Error{Kind: ErrUnauthorized}
	}
	auth := "Bearer " + token.value
	out := []Repository{}
	seen := map[int64]bool{}
	for page := 1; page <= c.maxPages; page++ {
		var w struct {
			TotalCount   int              `json:"total_count"`
			Repositories []wireRepository `json:"repositories"`
		}
		q := url.Values{"per_page": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)}}
		if _, err := c.do(ctx, http.MethodGet, "/installation/repositories", q, auth, nil, &w, http.StatusOK); err != nil {
			return nil, false, err
		}
		for _, wr := range w.Repositories {
			r, err := c.repository(wr)
			if err != nil {
				return nil, false, err
			}
			if !seen[r.ID] {
				seen[r.ID] = true
				out = append(out, r)
			}
		}
		if len(w.Repositories) < perPage || len(out) >= w.TotalCount {
			return out, false, nil
		}
	}
	return out, true, nil
}

// String describes an installation for messages.
func (i Installation) String() string {
	return fmt.Sprintf("%s (%d)", i.Account.Login, i.ID)
}
