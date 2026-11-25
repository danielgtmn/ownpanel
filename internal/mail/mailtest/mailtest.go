// Package mailtest has helpers for the mail service's tests.
package mailtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// Hash is a well-formed argon2id hash (of nothing in particular).
const Hash = "$argon2id$v=19$m=65536,t=2,p=2$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g"

// Cert returns a self-signed certificate for host, ECDSA or RSA.
func Cert(t testing.TB, host string, useRSA bool) config.Certificate {
	t.Helper()
	var key any
	var pub any
	if useRSA {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		key, pub = k, &k.PublicKey
	} else {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key, pub = k, &k.PublicKey
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return config.Certificate{
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk})),
	}
}

// ID returns a storage id made of the hex digit c.
func ID(c byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

// CheapHash is an argon2id hash of pw with the smallest parameters the
// service accepts, so tests that sign in stay fast.
func CheapHash(pw string) string {
	salt := []byte("saltsaltsaltsalt")
	key := argon2.IDKey([]byte(pw), salt, 1, 8, 1, 32)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=8,t=1,p=1$%s$%s", argon2.Version, b64.EncodeToString(salt), b64.EncodeToString(key))
}

// DKIMKey returns a new 2048-bit RSA key as PKCS#8 PEM and its DNS TXT
// record (v=DKIM1; k=rsa; p=...).
func DKIMKey(t testing.TB) (keyPEM, record string) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(pub)
}
