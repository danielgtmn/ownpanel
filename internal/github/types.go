package github

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Account types of owners and installations.
const (
	AccountUser         = "User"
	AccountOrganization = "Organization"
)

// Repository selections of an installation.
const (
	SelectionAll      = "all"
	SelectionSelected = "selected"
)

// Account is a GitHub user or organisation.
type Account struct {
	Login string
	Type  string // AccountUser or AccountOrganization
}

// App is a GitHub App, without its secrets.
type App struct {
	ID       int64
	Slug     string
	Name     string
	ClientID string
	Owner    Account
	HTMLURL  string
}

// AppCredentials is what GitHub hands out once, when an App is created
// from a manifest.
type AppCredentials struct {
	App
	PrivateKey    []byte // PEM, checked to be an RSA key of at least 2048 bits
	WebhookSecret string
	ClientSecret  string
}

// Installation is an App installed on an account.
type Installation struct {
	ID                  int64
	AppID               int64
	Account             Account
	RepositorySelection string // SelectionAll or SelectionSelected
	Suspended           bool
}

// Repository is a repository an installation may see.
type Repository struct {
	ID            int64
	Owner         string
	Name          string
	FullName      string // owner/name
	Private       bool
	Archived      bool
	DefaultBranch string
	HTMLURL       string
}

var (
	loginPattern    = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	slugPattern     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,99})$`)
	clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	codePattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)
)

// ValidLogin reports whether s can be a GitHub user or organisation name.
func ValidLogin(s string) bool { return loginPattern.MatchString(s) }

// ValidCode reports whether s can be a manifest code.
func ValidCode(s string) bool { return codePattern.MatchString(s) }

func validSlug(s string) bool { return slugPattern.MatchString(s) }

func validAccountType(t string) bool { return t == AccountUser || t == AccountOrganization }

// validText: printable, one line, not empty, at most max runes.
func validText(s string, max int) bool {
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || !unicode.IsPrint(r) })
}

// validSecret: printable ASCII without spaces, 1 to 1024 bytes.
func validSecret(s string) bool {
	if s == "" || len(s) > 1024 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

// validBranch: a name Git accepts as a branch (roughly; check-ref-format).
func validBranch(s string) bool {
	if s == "" || len(s) > 255 || !utf8.ValidString(s) || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "/") ||
		strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") || strings.HasSuffix(s, ".lock") ||
		strings.Contains(s, "..") || strings.Contains(s, "//") || strings.Contains(s, "@{") {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r) || strings.ContainsRune(`~^:?*[\`, r)
	})
}

// webURLOn checks that s is a page on this GitHub (same scheme and host).
func (c *Client) webURLOn(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == c.web.Scheme && strings.EqualFold(u.Host, c.web.Host) && u.User == nil
}
