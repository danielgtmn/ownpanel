package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newBox(t *testing.T) *Box {
	t.Helper()
	b, err := Open(filepath.Join(t.TempDir(), KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	b := newBox(t)
	for _, plain := range []string{"", "hetzner-token-123", strings.Repeat("x", 5000)} {
		sealed := b.Seal([]byte(plain), "dns")
		if plain != "" && bytes.Contains(sealed, []byte(plain)) {
			t.Fatal("sealed data contains the plaintext")
		}
		got, err := b.Open(sealed, "dns")
		if err != nil || string(got) != plain {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	// A fresh nonce every time.
	if bytes.Equal(b.Seal([]byte("same"), "dns"), b.Seal([]byte("same"), "dns")) {
		t.Fatal("two seals of the same plaintext are equal")
	}
}

func TestTamperDetection(t *testing.T) {
	b := newBox(t)
	sealed := b.Seal([]byte("token"), "dns")
	for i := range sealed {
		bad := bytes.Clone(sealed)
		bad[i] ^= 0x01
		if _, err := b.Open(bad, "dns"); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("byte %d flipped: err = %v", i, err)
		}
	}
	if _, err := b.Open(sealed[:len(sealed)-1], "dns"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("truncated: %v", err)
	}
	if _, err := b.Open(nil, "dns"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := b.Open(sealed, "other purpose"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("other purpose: %v", err)
	}
}

func TestWrongKey(t *testing.T) {
	sealed := newBox(t).Seal([]byte("token"), "dns")
	_, err := newBox(t).Open(sealed, "dns")
	if !errors.Is(err, ErrDecrypt) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "token") {
		t.Fatal("the error reveals the plaintext")
	}
}

func TestKeyFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), KeyFile)
	b1, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || fi.Size() != KeySize {
		t.Fatalf("mode %v size %d", fi.Mode().Perm(), fi.Size())
	}
	// Opening again reuses the key.
	b2, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := b2.Open(b1.Seal([]byte("x"), "p"), "p"); err != nil || string(got) != "x" {
		t.Fatalf("second open uses another key: %q %v", got, err)
	}

	short := filepath.Join(t.TempDir(), KeyFile)
	os.WriteFile(short, []byte("too short"), 0o600)
	if _, err := Open(short); err == nil {
		t.Fatal("a key file of the wrong size must be refused")
	}
	if _, err := New(make([]byte, 16)); err == nil {
		t.Fatal("a 16-byte key must be refused")
	}
}
