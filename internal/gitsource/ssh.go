package gitsource

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Host keys: a source pins the key its SSH server showed when it was
// linked (the person confirmed its fingerprint). Every later connection
// must show exactly that key; anything else is a HostKeyChangedError and
// nothing is sent to the server, not even the deploy key's signature.

// hostKeyAlgorithms are the algorithms asked for when scanning, strongest
// first. ssh-rsa with SHA-1 and DSA are not among them.
var hostKeyAlgorithms = []string{
	ssh.KeyAlgoED25519,
	ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
	ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256,
}

// algorithmsFor lists the algorithms that verify with a pinned key, so the
// server shows that key if it has it.
func algorithmsFor(k ssh.PublicKey) []string {
	if k.Type() == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	}
	return []string{k.Type()}
}

// HostKeyChangedError: the server showed another key than the pinned one.
// Presented is the key it showed (nil if it has none of the pinned type).
type HostKeyChangedError struct {
	Server    string
	Presented ssh.PublicKey
}

func (e *HostKeyChangedError) Error() string {
	if e.Presented == nil {
		return fmt.Sprintf("%s no longer shows the server key that was confirmed for this site", e.Server)
	}
	return fmt.Sprintf("%s shows a different server key (%s) than the one confirmed for this site", e.Server, ssh.FingerprintSHA256(e.Presented))
}

// AuthError: the server did not accept the deploy key.
type AuthError struct{ Server string }

func (e *AuthError) Error() string {
	return fmt.Sprintf("%s did not accept the deploy key: add the site's deploy key to the repository (read access is enough)", e.Server)
}

// ConnectError: the server could not be reached.
type ConnectError struct {
	Server string
	Err    error
}

func (e *ConnectError) Error() string {
	switch {
	case errors.Is(e.Err, errNotPublic):
		return fmt.Sprintf("%s is not reachable from the panel: its address is not public", e.Server)
	case errors.Is(e.Err, context.DeadlineExceeded):
		return fmt.Sprintf("%s did not answer in time", e.Server)
	}
	var dnsErr *net.DNSError
	if errors.As(e.Err, &dnsErr) && dnsErr.IsNotFound {
		return fmt.Sprintf("%s does not exist (no address for this name)", e.Server)
	}
	return fmt.Sprintf("could not connect to %s", e.Server)
}

func (e *ConnectError) Unwrap() error { return e.Err }

// ScanHostKey connects to an SSH server and returns the key it shows. It
// authenticates nothing: the connection ends right after the key arrived.
func ScanHostKey(ctx context.Context, a Address) (ssh.PublicKey, error) {
	if a.Scheme != SchemeSSH {
		return nil, errors.New("only SSH servers have host keys")
	}
	ctx, cancel := context.WithTimeout(ctx, ScanTimeout)
	defer cancel()
	var got ssh.PublicKey
	errStop := errors.New("key received")
	cfg := &ssh.ClientConfig{
		User:              a.User,
		HostKeyAlgorithms: hostKeyAlgorithms,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got = key
			return errStop
		},
		Timeout: ScanTimeout,
	}
	conn, err := dial(ctx, a.HostPort())
	if err != nil {
		return nil, &ConnectError{Server: a.ServerName(), Err: err}
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(ScanTimeout))
	_, _, _, err = ssh.NewClientConn(conn, a.HostPort(), cfg)
	if got != nil {
		return got, nil
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return nil, &ConnectError{Server: a.ServerName(), Err: fmt.Errorf("no SSH server: %w", err)}
}

// sshSession runs git-upload-pack for the repository over SSH with the
// pinned host key and the deploy key.
type sshSession struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	stderr  *limitedBuffer
	stop    func() bool
}

func (s *sshSession) Close() error {
	s.stop()
	s.session.Close()
	return s.client.Close()
}

// limitedBuffer keeps the first bytes written to it.
type limitedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := maxServerTexts - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func openSSH(ctx context.Context, a Address, key ssh.Signer, hostKey ssh.PublicKey) (*sshSession, error) {
	if hostKey == nil {
		return nil, errors.New("no server key was confirmed for this source")
	}
	var presented ssh.PublicKey
	mismatch := false
	cfg := &ssh.ClientConfig{
		User:              a.User,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyAlgorithms: algorithmsFor(hostKey),
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			presented = k
			if !bytes.Equal(k.Marshal(), hostKey.Marshal()) {
				mismatch = true
				return errors.New("host key mismatch")
			}
			return nil
		},
		Timeout:       dialTimeout,
		ClientVersion: "SSH-2.0-ownpanel",
	}
	conn, err := dial(ctx, a.HostPort())
	if err != nil {
		return nil, &ConnectError{Server: a.ServerName(), Err: err}
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	// The handshake must finish in time; the deadline is lifted after.
	conn.SetDeadline(time.Now().Add(dialTimeout))
	c, chans, reqs, err := ssh.NewClientConn(conn, a.HostPort(), cfg)
	if err != nil {
		stop()
		conn.Close()
		switch {
		case mismatch:
			return nil, &HostKeyChangedError{Server: a.ServerName(), Presented: presented}
		case presented == nil && strings.Contains(err.Error(), "no common algorithm for host key"):
			return nil, &HostKeyChangedError{Server: a.ServerName()}
		case strings.Contains(err.Error(), "unable to authenticate"):
			return nil, &AuthError{Server: a.ServerName()}
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, &ConnectError{Server: a.ServerName(), Err: err}
	}
	conn.SetDeadline(time.Time{})
	// The connection is closed when ctx ends, or with the session.
	client := ssh.NewClient(c, chans, reqs)
	sess, err := client.NewSession()
	if err != nil {
		stop()
		client.Close()
		return nil, &ConnectError{Server: a.ServerName(), Err: err}
	}
	out := &sshSession{client: client, session: sess, stderr: &limitedBuffer{}, stop: stop}
	sess.Stderr = out.stderr
	if out.stdin, err = sess.StdinPipe(); err != nil {
		out.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		out.Close()
		return nil, err
	}
	out.stdout = stdout
	// The path only contains letters, digits, ".", "_", "-", "~" and "/"
	// (checkPath), so quoting it cannot be broken out of.
	if err := sess.Start("git-upload-pack '" + a.Path + "'"); err != nil {
		out.Close()
		return nil, &ConnectError{Server: a.ServerName(), Err: err}
	}
	return out, nil
}

// failure turns an error during the conversation into what the server
// said on its error output, if anything.
func (s *sshSession) failure(err error) error {
	var se *ServerError
	var le *LimitError
	if errors.As(err, &se) || errors.As(err, &le) {
		return err
	}
	if msg := serverText([]byte(s.stderr.String())); msg != "" {
		return &ServerError{Msg: msg}
	}
	return err
}
