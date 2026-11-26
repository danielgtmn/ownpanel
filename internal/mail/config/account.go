package config

// Mailbox accounts (G1 in wiki/Groupware-Architecture.md): display names,
// organisations, app passwords and the changes a mailbox makes to its own
// account in the mail service.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/passwd"
)

// Limits of mailbox accounts.
const (
	// MaxDisplayName bounds display names, organisation names and app
	// password names, in characters (runes).
	MaxDisplayName = 64
	// MaxAppPasswords bounds the app passwords of one mailbox.
	MaxAppPasswords = 50
	// AppPasswordKeyBytes is the size of the server key app passwords are
	// checked with.
	AppPasswordKeyBytes = 32
)

// AppPassword is a password for one mail program or device. The mail
// service never sees the password itself, only MAC (AppPasswordMAC).
type AppPassword struct {
	// ID is the public part of the token (AppPasswordIDLen characters of
	// lowercase base32), unique on the server.
	ID string `json:"id"`
	// Name is the device or program, as the person called it.
	Name string `json:"name"`
	// MAC is AppPasswordMAC of the token's secret, in hex.
	MAC     string    `json:"mac"`
	Created time.Time `json:"created"`
}

// App password tokens are "op_<id>_<secret>": AppPasswordIDLen characters of
// lowercase base32 (80 bits) and AppPasswordSecretLen characters (160 bits).
const (
	AppPasswordPrefix    = "op_"
	AppPasswordIDLen     = 16
	AppPasswordSecretLen = 32
)

var (
	b32           = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)
	appTokenRe    = regexp.MustCompile(`^op_([a-z2-7]{16})_([a-z2-7]{32})$`)
	appIDRe       = regexp.MustCompile(`^[a-z2-7]{16}$`)
	macRe         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	organisationR = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,127}$`)
)

// NewAppPassword makes a new token and returns its id, its secret and the
// token as the person enters it ("op_<id>_<secret>").
func NewAppPassword() (id, secret, token string) {
	buf := make([]byte, 10+20)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	id, secret = b32.EncodeToString(buf[:10]), b32.EncodeToString(buf[10:])
	return id, secret, AppPasswordPrefix + id + "_" + secret
}

// ParseAppPassword splits a token into id and secret. ok is false for
// anything that does not have exactly the form of a token.
func ParseAppPassword(token string) (id, secret string, ok bool) {
	m := appTokenRe.FindStringSubmatch(token)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// LooksLikeAppPassword tells whether pw has the form of an app password.
// Such strings are always checked as app passwords, never as the main
// password, so the panel refuses them as main passwords.
func LooksLikeAppPassword(pw string) bool {
	_, _, ok := ParseAppPassword(pw)
	return ok
}

// ValidAppPasswordID tells whether id has the form of an app password id.
func ValidAppPasswordID(id string) bool { return appIDRe.MatchString(id) }

// NewAppPasswordKey makes a server key for app passwords (hex).
func NewAppPasswordKey() string {
	k := make([]byte, AppPasswordKeyBytes)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return hex.EncodeToString(k)
}

// AppPasswordMAC is HMAC-SHA-256 under key (hex, Config.AppPasswordKey) of
// the token's secret, bound to the mailbox and the id: a MAC copied to
// another mailbox or id never matches. It returns "" for a malformed key.
func AppPasswordMAC(keyHex, storageID, id, secret string) string {
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != AppPasswordKeyBytes {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("ownpanel app password\x00" + storageID + "\x00" + id + "\x00" + secret))
	return hex.EncodeToString(mac.Sum(nil))
}

// CheckAppPassword compares a token's secret with a stored MAC in constant
// time.
func CheckAppPassword(keyHex, storageID string, ap AppPassword, secret string) bool {
	want := AppPasswordMAC(keyHex, storageID, ap.ID, secret)
	return want != "" && hmac.Equal([]byte(want), []byte(ap.MAC))
}

// ValidDisplayName tells whether s may be a display name (also used for
// organisation and app password names): valid UTF-8, at most MaxDisplayName
// characters, no control characters (so no CR/LF) and none of < > " which
// would break a From header. Empty is valid (no display name).
func ValidDisplayName(s string) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > MaxDisplayName || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		// Cf: format characters such as bidi overrides; Zl/Zp: line and
		// paragraph separators.
		if unicode.IsControl(r) || r == '<' || r == '>' || r == '"' || r == utf8.RuneError ||
			unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}
	return true
}

// ValidOrganisation tells whether id has the form of an organisation id:
// lowercase letters, digits, dots and dashes, at most 128 characters.
func ValidOrganisation(id string) bool { return organisationR.MatchString(id) }

// StandaloneOrganisation is the organisation id of a domain that is not
// grouped with others.
func StandaloneOrganisation(domain string) string { return "dom-" + domain }

// Network is the part of an address shown for the last use of an app
// password: the /24 for IPv4, the /48 for IPv6, never the full address.
func Network(ip netip.Addr) string {
	ip = ip.Unmap()
	bits := 24
	if ip.Is6() {
		bits = 48
	}
	p, err := ip.Prefix(bits)
	if err != nil {
		return ""
	}
	return p.String()
}

// Account events: a mailbox changed its own account in the mail service
// (self-service), or an app password was used. Event.Account carries the
// change; the panel applies changes with a Revision higher than the
// mailbox's (Mailbox.Revision) and records them.
const (
	EventPasswordChanged    = "mailbox.password-changed"
	EventDisplayNameChanged = "mailbox.display-name-changed"
	EventAppPasswordsOnly   = "mailbox.app-passwords-only"
	EventAppPasswordCreated = "mailbox.app-password-created"
	EventAppPasswordRevoked = "mailbox.app-password-revoked"
	// EventAppPasswordUsed: an app password signed in (at most once per
	// AppPasswordUseEvery per app password). Revision is 0: not a change.
	EventAppPasswordUsed = "mailbox.app-password-used"
)

// AppPasswordUseEvery is how often the use of one app password is reported.
const AppPasswordUseEvery = time.Hour

// AccountChange is what an account event carries. Exactly the fields of
// its kind are set. It holds a password hash or a MAC, never a password.
type AccountChange struct {
	// Revision orders the changes of one mailbox; 0 for EventAppPasswordUsed.
	Revision int64 `json:"revision,omitempty"`
	// PasswordHash: EventPasswordChanged (argon2id).
	PasswordHash string `json:"password_hash,omitempty"`
	// DisplayName: EventDisplayNameChanged ("" removes it).
	DisplayName *string `json:"display_name,omitempty"`
	// AppPasswordsOnly: EventAppPasswordsOnly.
	AppPasswordsOnly *bool `json:"app_passwords_only,omitempty"`
	// AppPassword: EventAppPasswordCreated.
	AppPassword *AppPassword `json:"app_password,omitempty"`
	// AppPasswordID: EventAppPasswordRevoked and EventAppPasswordUsed.
	AppPasswordID string `json:"app_password_id,omitempty"`
	// Network: EventAppPasswordUsed (see Network).
	Network string `json:"network,omitempty"`
}

// ErrLooksLikeAppPassword: a new main password has the form of an app
// password, so mail programs would only ever check it as one.
var ErrLooksLikeAppPassword = errors.New("this looks like an app password; choose another password")

// CheckNewPassword tells whether pw may be a mailbox's new main password:
// the panel's rules (passwd.CheckNew), and not of the form of an app
// password. Errors are meant for people.
func CheckNewPassword(pw string) error {
	if err := passwd.CheckNew(pw); err != nil {
		return err
	}
	if LooksLikeAppPassword(pw) {
		return ErrLooksLikeAppPassword
	}
	return nil
}
