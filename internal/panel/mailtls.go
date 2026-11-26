package panel

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/dns"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// The mail host's certificate (wiki/E-Mail-Architecture.md, "Mail host and
// TLS"). Caddy's certificates are private to Caddy, so the panel gets its
// own: with -tls acme from the public CA through autocert, answering the
// HTTP-01 challenge that Caddy passes on from port 80 (MailChallengeHandler);
// with -tls internal or off a self-signed one. Both an ECDSA and an RSA
// certificate are obtained, for old clients. Certificates and the ACME
// account are kept in <data>/mail-tls.
//
// autocert tries the TLS-ALPN-01 challenge before HTTP-01; that one cannot
// succeed here (port 443 is Caddy's), so a first issuance costs one failed
// validation at the CA. The back-off keeps repeated failures well inside
// the CA's limits.

const (
	// mailCertCheck is how often a certificate in place is looked at again
	// (autocert renews it in the background; a new one is then pushed).
	mailCertCheck = 24 * time.Hour
	// mailCertBackoffMin and Max bound the wait after a failed issuance.
	mailCertBackoffMin = 10 * time.Minute
	mailCertBackoffMax = 24 * time.Hour
	// mailCertDNSRetry is how soon a mail host that does not point here
	// yet is looked up again (no CA is asked meanwhile).
	mailCertDNSRetry = 10 * time.Minute
	// mailCertTick is how often the certificate loop wakes up.
	mailCertTick = time.Minute
	// selfSignedLife is the validity of a self-signed certificate; it is
	// made again 30 days before it ends.
	selfSignedLife = 2 * 365 * 24 * time.Hour
)

// mailCerts holds the mail host's certificates.
type mailCerts struct {
	dir  string
	acme bool

	mu       sync.Mutex
	host     string // what certs and mgr are for
	email    string
	mgr      *autocert.Manager
	certs    []config.Certificate
	loaded   bool
	nextTry  time.Time
	backoff  time.Duration
	lastGood time.Time
}

func newMailCerts(dir, tlsMode string) *mailCerts {
	if dir == "" {
		return nil
	}
	return &mailCerts{dir: dir, acme: tlsMode == caddy.TLSACME}
}

// mailCertificates are the certificates to push (nil if none yet).
func (s *Service) mailCertificates() []config.Certificate {
	if s.certs == nil {
		return nil
	}
	s.certs.mu.Lock()
	defer s.certs.mu.Unlock()
	if s.certs.host != s.MailHost() {
		return nil
	}
	return append([]config.Certificate(nil), s.certs.certs...)
}

// mailCertsLoaded: the certificates for the current mail host were looked
// for at least once (or there is nothing to look for).
func (s *Service) mailCertsLoaded() bool {
	if s.certs == nil || s.MailHost() == "" {
		return true
	}
	s.certs.mu.Lock()
	defer s.certs.mu.Unlock()
	return s.certs.loaded && s.certs.host == s.MailHost()
}

// MailChallengeHandler answers the ACME HTTP-01 challenges for the mail
// host (which Caddy passes on from port 80) and hands every other request
// to next.
func (s *Service) MailChallengeHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.certs != nil && strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
			host := strings.ToLower(r.Host)
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			if mh := s.MailHost(); mh != "" && host == mh {
				if mgr := s.certs.manager(); mgr != nil {
					mgr.HTTPHandler(http.NotFoundHandler()).ServeHTTP(w, r)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (c *mailCerts) manager() *autocert.Manager {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mgr
}

// fingerprint identifies a set of certificates.
func fingerprint(certs []config.Certificate) string {
	h := sha256.New()
	for _, c := range certs {
		h.Write([]byte(c.CertPEM))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// runMailCerts keeps the certificates current and pushes the configuration
// when they change.
func (s *Service) runMailCerts(ctx context.Context) {
	for {
		if s.refreshMailCerts(ctx, true) {
			// The web server serves the mail host with the same
			// certificates (caddy.MailWeb).
			if err := s.sync(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("web server not in sync after a certificate change", "err", err)
			}
			if err := s.mailSync(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("mail server not in sync after a certificate change", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(mailCertTick):
		}
	}
}

// refreshMailCerts loads, obtains or renews the certificates when due and
// reports whether they changed. Without issue, the CA is never asked (only
// cached or self-signed certificates are used), so it returns quickly.
func (s *Service) refreshMailCerts(ctx context.Context, issue bool) bool {
	c := s.certs
	if c == nil {
		return false
	}
	host, email := s.MailHost(), s.acmeEmail()
	c.mu.Lock()
	if host != c.host || (c.acme && email != c.email) {
		c.host, c.email, c.certs, c.loaded = host, email, nil, false
		c.nextTry, c.backoff, c.lastGood, c.mgr = time.Time{}, 0, time.Time{}, nil
		if host != "" && c.acme {
			c.mgr = &autocert.Manager{
				Prompt:     autocert.AcceptTOS,
				Cache:      autocert.DirCache(c.dir),
				HostPolicy: autocert.HostWhitelist(host),
				Email:      email,
			}
			// autocert offers HTTP-01 only once HTTPHandler has been called;
			// MailChallengeHandler calls it only when a challenge comes in,
			// which never happens before the CA is offered HTTP-01. Without
			// this, only TLS-ALPN-01 is tried, which cannot succeed here.
			c.mgr.HTTPHandler(nil)
		}
	}
	before := fingerprint(c.certs)
	c.mu.Unlock()
	if host == "" {
		c.mu.Lock()
		c.loaded = true
		c.mu.Unlock()
		return before != fingerprint(nil)
	}
	if !c.acme {
		certs, err := selfSigned(c.dir, host, s.now())
		c.mu.Lock()
		defer c.mu.Unlock()
		c.loaded = true
		if err != nil {
			slog.Error("could not make a certificate for the mail host", "host", host, "err", err)
			return false
		}
		c.certs = certs
		return fingerprint(certs) != before
	}
	return s.refreshACME(ctx, c, host, before, issue)
}

func (s *Service) refreshACME(ctx context.Context, c *mailCerts, host, before string, issue bool) bool {
	c.mu.Lock()
	mgr, loaded := c.mgr, c.loaded
	c.mu.Unlock()
	now := s.now()
	if !loaded {
		// Certificates in the cache are used at once, without asking the CA.
		_, e1 := mgr.Cache.Get(ctx, host)
		_, e2 := mgr.Cache.Get(ctx, host+"+rsa")
		if e1 == nil && e2 == nil {
			if certs, err := obtainBoth(mgr, host); err == nil {
				c.mu.Lock()
				c.certs, c.lastGood, c.nextTry = certs, now, now.Add(mailCertCheck)
				c.mu.Unlock()
			} else {
				slog.Warn("the cached certificates of the mail host cannot be used", "host", host, "err", err)
			}
		}
		c.mu.Lock()
		c.loaded = true
		c.mu.Unlock()
	}
	c.mu.Lock()
	due := issue && !now.Before(c.nextTry)
	c.mu.Unlock()
	if !due {
		c.mu.Lock()
		defer c.mu.Unlock()
		return fingerprint(c.certs) != before
	}
	// Only ask the CA once the mail host points here: a failed validation
	// counts against the CA's limits.
	ips := s.effectiveServerIPs()
	pointsHere := false
	if len(ips) > 0 {
		lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		pointsHere = dns.CheckDomain(lctx, s.resolver, host, ips).State == dns.PointsHere
		cancel()
	}
	if !pointsHere {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.nextTry = now.Add(mailCertDNSRetry)
		return fingerprint(c.certs) != before
	}
	certs, err := obtainBoth(mgr, host)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.backoff = min(max(2*c.backoff, mailCertBackoffMin), mailCertBackoffMax)
		c.nextTry = now.Add(c.backoff)
		slog.Warn("could not obtain a certificate for the mail host", "host", host, "retry_in", c.backoff, "err", err)
		return fingerprint(c.certs) != before
	}
	c.certs, c.backoff, c.lastGood, c.nextTry = certs, 0, now, now.Add(mailCertCheck)
	return fingerprint(certs) != before
}

// Client hellos that make autocert pick an ECDSA or an RSA certificate.
func helloFor(host string, ecdsaOK bool) *tls.ClientHelloInfo {
	if ecdsaOK {
		return &tls.ClientHelloInfo{
			ServerName:        host,
			CipherSuites:      []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
			SignatureSchemes:  []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
			SupportedCurves:   []tls.CurveID{tls.CurveP256},
			SupportedVersions: []uint16{tls.VersionTLS12},
		}
	}
	return &tls.ClientHelloInfo{
		ServerName:        host,
		CipherSuites:      []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
		SignatureSchemes:  []tls.SignatureScheme{tls.PKCS1WithSHA256},
		SupportedCurves:   []tls.CurveID{tls.CurveP256},
		SupportedVersions: []uint16{tls.VersionTLS12},
	}
}

// obtainBoth gets the ECDSA and the RSA certificate (from the cache or the
// CA) as PEM.
func obtainBoth(mgr *autocert.Manager, host string) ([]config.Certificate, error) {
	var out []config.Certificate
	for _, ecdsaOK := range []bool{true, false} {
		cert, err := mgr.GetCertificate(helloFor(host, ecdsaOK))
		if err != nil {
			return nil, err
		}
		c, err := certPEM(cert)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// certPEM encodes a chain and its key.
func certPEM(cert *tls.Certificate) (config.Certificate, error) {
	var chain strings.Builder
	for _, der := range cert.Certificate {
		chain.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return config.Certificate{}, err
	}
	return config.Certificate{CertPEM: chain.String(), KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))}, nil
}

// selfSigned returns a self-signed ECDSA certificate for host from dir,
// making (and storing) a new one when there is none for host or it ends
// within 30 days.
func selfSigned(dir, host string, now time.Time) ([]config.Certificate, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	file := filepath.Join(dir, "self-signed-"+host+".pem")
	if b, err := os.ReadFile(file); err == nil {
		if c, ok := parseSelfSigned(b, host, now); ok {
			return []config.Certificate{c}, nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(selfSignedLife),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	c := config.Certificate{
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})),
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(c.CertPEM+c.KeyPEM), 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, file); err != nil {
		return nil, err
	}
	return []config.Certificate{c}, nil
}

func parseSelfSigned(b []byte, host string, now time.Time) (config.Certificate, bool) {
	var c config.Certificate
	for rest := b; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			c.CertPEM += string(pem.EncodeToMemory(block))
		case "PRIVATE KEY":
			c.KeyPEM = string(pem.EncodeToMemory(block))
		}
	}
	pair, err := tls.X509KeyPair([]byte(c.CertPEM), []byte(c.KeyPEM))
	if err != nil {
		return c, false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.VerifyHostname(host) != nil || now.Add(30*24*time.Hour).After(leaf.NotAfter) {
		return c, false
	}
	return c, true
}
