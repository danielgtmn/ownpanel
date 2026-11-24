// Package secrets encrypts small secrets (such as DNS provider tokens) for
// storage in the database, with AES-256-GCM. The key is kept in a file next
// to the database, never in it, so a copy of the database alone reveals
// nothing.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// KeyFile is the key's name inside the data directory.
const KeyFile = "secret.key"

// KeySize is the length of a key in bytes (AES-256).
const KeySize = 32

// ErrDecrypt means sealed data could not be opened: it was changed, or it
// was sealed with another key. The message never contains secret material.
var ErrDecrypt = errors.New("secrets: cannot decrypt (wrong key or damaged data)")

// Box seals and opens secrets with one key.
type Box struct {
	aead cipher.AEAD
}

// New returns a Box for a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secrets: the key must be %d bytes, not %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Open returns a Box for the key in file, creating the file with a new
// random key (mode 0600) if it does not exist.
func Open(file string) (*Box, error) {
	key, err := LoadOrCreateKey(file)
	if err != nil {
		return nil, err
	}
	return New(key)
}

// LoadOrCreateKey reads the key in file. If the file does not exist it is
// created with 32 random bytes and mode 0600.
func LoadOrCreateKey(file string) ([]byte, error) {
	key, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		if err := createKey(file); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		// Read back what is on disk, also when another process won the race.
		key, err = os.ReadFile(file)
	}
	if err != nil {
		return nil, err
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("secrets: %s must hold exactly %d bytes, it has %d", file, KeySize, len(key))
	}
	return key, nil
}

func createKey(file string) (err error) {
	key := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(file)
		}
	}()
	if _, err := f.Write(key); err != nil {
		return err
	}
	return f.Sync()
}

// Seal encrypts plaintext. purpose is authenticated but not stored: Open
// must be given the same value, so a secret sealed for one use cannot be
// passed off as another.
func (b *Box) Seal(plaintext []byte, purpose string) []byte {
	nonce := make([]byte, b.aead.NonceSize(), b.aead.NonceSize()+len(plaintext)+b.aead.Overhead())
	// crypto/rand.Read never fails on supported platforms.
	rand.Read(nonce)
	return b.aead.Seal(nonce, nonce, plaintext, []byte(purpose))
}

// Open decrypts what Seal returned. Any change to sealed, a different key
// or a different purpose yields ErrDecrypt.
func (b *Box) Open(sealed []byte, purpose string) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n+b.aead.Overhead() {
		return nil, ErrDecrypt
	}
	out, err := b.aead.Open(nil, sealed[:n], sealed[n:], []byte(purpose))
	if err != nil {
		return nil, ErrDecrypt
	}
	return out, nil
}
