// Package auth is message authentication of the mail service: DKIM
// signing of outgoing mail; SPF, DKIM and DMARC verdicts for incoming mail
// (verify.go); and the Sender Rewriting Scheme for forwarded mail
// (srs.go).
package auth

import (
	"bufio"
	"bytes"
	"crypto"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/emersion/go-msgauth/dkim"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// SignedHeaders are the header fields a DKIM signature covers, when the
// message has them.
var SignedHeaders = []string{
	"From", "To", "Cc", "Subject", "Date", "Message-ID", "Reply-To",
	"MIME-Version", "Content-Type", "In-Reply-To", "References",
}

// ErrNoKey: the domain has no active DKIM key; the message goes out
// unsigned.
var ErrNoKey = errors.New("no active DKIM key")

// Signer signs messages with the active DKIM key of a domain. It caches
// parsed keys; it is safe for concurrent use.
type Signer struct {
	mu   sync.Mutex
	keys map[[32]byte]crypto.Signer
}

// NewSigner returns a Signer.
func NewSigner() *Signer { return &Signer{keys: map[[32]byte]crypto.Signer{}} }

func (s *Signer) key(pemKey string) (crypto.Signer, error) {
	h := sha256.Sum256([]byte(pemKey))
	s.mu.Lock()
	k, ok := s.keys[h]
	s.mu.Unlock()
	if ok {
		return k, nil
	}
	k, err := config.ParseDKIMKey(pemKey)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if len(s.keys) > 1000 {
		clear(s.keys) // keys of old configurations
	}
	s.keys[h] = k
	s.mu.Unlock()
	return k, nil
}

// Signature computes the DKIM-Signature header field (with its final CRLF)
// for the message in r, signed for domain d (relaxed/relaxed, RSA-SHA256).
// It returns ErrNoKey when d has no active key. r must use CRLF line
// endings.
func (s *Signer) Signature(d *config.Domain, r io.Reader) (string, error) {
	var active *config.DKIMKey
	for i := range d.DKIM {
		if d.DKIM[i].Active {
			active = &d.DKIM[i]
		}
	}
	if active == nil {
		return "", ErrNoKey
	}
	key, err := s.key(active.PrivateKeyPEM)
	if err != nil {
		return "", err
	}
	br := bufio.NewReaderSize(r, maxHeaderPeek)
	hdr, err := peekHeaderNames(br)
	if err != nil {
		return "", err
	}
	var keys []string
	for _, k := range SignedHeaders {
		if hdr[strings.ToLower(k)] {
			keys = append(keys, k)
		}
	}
	signer, err := dkim.NewSigner(&dkim.SignOptions{
		Domain:                 d.Name,
		Selector:               active.Selector,
		Signer:                 key,
		Hash:                   crypto.SHA256,
		HeaderCanonicalization: dkim.CanonicalizationRelaxed,
		BodyCanonicalization:   dkim.CanonicalizationRelaxed,
		HeaderKeys:             keys,
	})
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(signer, br); err != nil {
		signer.Close()
		return "", err
	}
	if err := signer.Close(); err != nil {
		return "", err
	}
	return signer.Signature(), nil
}

// maxHeaderPeek bounds how much of a message is read to find out which
// header fields it has.
const maxHeaderPeek = 256 << 10

// peekHeaderNames returns the lowercase names of the header fields at the
// start of br without consuming them. A header larger than maxHeaderPeek
// is looked at only that far.
func peekHeaderNames(br *bufio.Reader) (map[string]bool, error) {
	b, err := br.Peek(maxHeaderPeek)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, err
	}
	if end := bytes.Index(b, []byte("\r\n\r\n")); end >= 0 {
		b = b[:end+2]
	}
	names := map[string]bool{}
	for _, line := range strings.Split(string(b), "\r\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		if i := strings.IndexByte(line, ':'); i > 0 {
			names[strings.ToLower(strings.TrimSpace(line[:i]))] = true
		}
	}
	return names, nil
}
