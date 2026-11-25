package server

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"errors"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// certSet are the certificates of one configuration. The service presents
// them whatever the SNI: clients without SNI and clients asking for another
// name get them too (there is only one mail host).
type certSet struct {
	certs []tls.Certificate // in config order, Leaf set
}

func parseCerts(cs []config.Certificate) (*certSet, error) {
	set := &certSet{}
	for _, c := range cs {
		kp, err := tls.X509KeyPair([]byte(c.CertPEM), []byte(c.KeyPEM))
		if err != nil {
			return nil, err
		}
		if kp.Leaf == nil {
			if kp.Leaf, err = x509.ParseCertificate(kp.Certificate[0]); err != nil {
				return nil, err
			}
		}
		set.certs = append(set.certs, kp)
	}
	return set, nil
}

// pick chooses a certificate for a handshake: ECDSA when the client
// supports it, otherwise the first one it supports (RSA for old servers),
// otherwise the first one.
func (s *certSet) pick(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if s == nil || len(s.certs) == 0 {
		return nil, errors.New("no certificate")
	}
	// SupportsCertificate also checks the name; ours is the only one, so
	// only key type and signature algorithms matter.
	h := *hello
	h.ServerName = ""
	hello = &h
	for i := range s.certs {
		if _, ok := s.certs[i].PrivateKey.(*ecdsa.PrivateKey); ok && hello.SupportsCertificate(&s.certs[i]) == nil {
			return &s.certs[i], nil
		}
	}
	for i := range s.certs {
		if hello.SupportsCertificate(&s.certs[i]) == nil {
			return &s.certs[i], nil
		}
	}
	return &s.certs[0], nil
}
