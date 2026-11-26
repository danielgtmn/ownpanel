package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Deliveries: GitHub signs the raw body of every delivery with the App's
// webhook secret (HMAC-SHA256) and sends the hex digest as
// "X-Hub-Signature-256: sha256=<hex>". The headers below identify the
// delivery and the App it belongs to.
const (
	HeaderSignature  = "X-Hub-Signature-256"
	HeaderDelivery   = "X-GitHub-Delivery"
	HeaderEvent      = "X-GitHub-Event"
	HeaderTargetID   = "X-GitHub-Hook-Installation-Target-ID"
	HeaderTargetType = "X-GitHub-Hook-Installation-Target-Type"
	// TargetTypeApp is the target type of an App's own webhook.
	TargetTypeApp = "integration"
)

const signaturePrefix = "sha256="

// Sign returns the X-Hub-Signature-256 value for body, as GitHub computes
// it (for tests and the development stand-in).
func Sign(secret, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return signaturePrefix + hex.EncodeToString(m.Sum(nil))
}

// ValidSignature reports whether header is the signature of body with
// secret. The digests are compared in constant time.
func ValidSignature(secret, body []byte, header string) bool {
	hexSum, ok := strings.CutPrefix(header, signaturePrefix)
	if !ok || len(hexSum) != 2*sha256.Size {
		return false
	}
	got, err := hex.DecodeString(hexSum)
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return hmac.Equal(got, m.Sum(nil))
}

// ValidCommitSHA reports whether s is a full commit id (40 lowercase hex
// digits, SHA-1 repositories, as GitHub sends them).
func ValidCommitSHA(s string) bool { return len(s) == 40 && validSHA(s) }

// ValidFullName reports whether s can be the owner/name of a repository.
func ValidFullName(s string) bool {
	owner, name, ok := strings.Cut(s, "/")
	return ok && ValidLogin(owner) && repoNamePattern.MatchString(name) && name != "." && name != ".."
}
