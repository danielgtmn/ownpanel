package panel

// Every form of proof a notification can carry (from the security review of #137).

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
)

func TestNotificationProofForms(t *testing.T) {
	secret := []byte("s3cret")
	body := []byte(`{"ref":"refs/heads/main"}`)
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	good := hex.EncodeToString(m.Sum(nil))
	m1 := hmac.New(sha1.New, secret)
	m1.Write(body)
	sha1sig := "sha1=" + hex.EncodeToString(m1.Sum(nil))
	for _, tc := range []struct {
		name string
		h    map[string]string
		want bool
	}{
		{"sha1 only", map[string]string{"X-Hub-Signature": sha1sig}, false},
		{"none", map[string]string{}, false},
		{"gitea hex", map[string]string{"X-Gitea-Signature": good}, true},
		{"gitea upper hex", map[string]string{"X-Gitea-Signature": "SHA256=" + good}, false},
		{"good 256 + wrong sha1", map[string]string{"X-Hub-Signature-256": "sha256=" + good, "X-Hub-Signature": "sha1=00"}, true},
		{"good 256 + wrong gitea", map[string]string{"X-Hub-Signature-256": "sha256=" + good, "X-Gitea-Signature": "00"}, false},
		{"gitlab token", map[string]string{"X-Gitlab-Token": "s3cret"}, true},
		{"gitlab wrong", map[string]string{"X-Gitlab-Token": "s3cre"}, false},
		{"hub-sig sha256", map[string]string{"X-Hub-Signature": "sha256=" + good}, true},
		{"hub-sig unprefixed", map[string]string{"X-Hub-Signature": good}, false},
		{"empty secret token", map[string]string{"X-Gitlab-Token": ""}, false},
	} {
		h := http.Header{}
		for k, v := range tc.h {
			h.Set(k, v)
		}
		if got := validProof(secret, body, h); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	// Empty secret: can an unknown/empty secret be matched?
	h := http.Header{}
	h.Set("X-Gitlab-Token", "x")
	if validProof(nil, body, h) {
		t.Error("nil secret accepted")
	}
}

func FuzzNotificationParsing(f *testing.F) {
	f.Add([]byte(`{"ref":"refs/heads/main","after":"1111111111111111111111111111111111111111","commits":[{"id":"1111111111111111111111111111111111111111"}]}`), "push", "sha256=00")
	f.Add([]byte(`{"push":{"changes":[{"new":{"type":"branch","name":"main","target":{"hash":"ab","author":{"raw":"a <b>"}}}}]}}`), "repo:push", "")
	f.Fuzz(func(t *testing.T, body []byte, event, sig string) {
		h := http.Header{}
		h.Set("X-Hub-Signature-256", sig)
		h.Set("X-Hub-Signature", sig)
		h.Set("X-Gitlab-Token", sig)
		_ = validProof([]byte("k"), body, h)
		_, _ = pushEvent(event)
		var p gitPush
		_ = decodeStrict(body, &p)
		_ = plainText(string(body), runMessageMax)
	})
}
