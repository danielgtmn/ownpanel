package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/textproto"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/mail/auth"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// maxSendBytes bounds the JSON body of POST /send.
const maxSendBytes = 256 << 10

// Limits of the header fields of a message the panel sends: well below
// the 998 bytes a header line may have.
const (
	maxSubjectBytes  = 500
	maxFromNameBytes = 200
)

// sendError is a message the panel must fix (400); its text is safe to show.
type sendError string

func (e sendError) Error() string { return string(e) }

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var m config.OutgoingMail
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSendBytes)).Decode(&m); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "the message is too large", http.StatusBadRequest)
			return
		}
		// The decoder's message names a position or type, never content.
		http.Error(w, "invalid message: "+err.Error(), http.StatusBadRequest)
		return
	}
	err := s.Send(r.Context(), m)
	var se sendError
	switch {
	case errors.As(err, &se):
		http.Error(w, se.Error(), http.StatusBadRequest)
	case err != nil:
		s.internalError(w, "send", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// Send writes, signs and delivers a message of the panel: into local
// mailboxes directly, to outside addresses through the queue (envelope
// sender From, no storage id, so bounces are dropped).
func (s *Server) Send(ctx context.Context, m config.OutgoingMail) error {
	dir := s.Directory()
	from, to, err := checkOutgoing(dir, &m)
	if err != nil {
		return err
	}
	_, fromDomain, _ := config.SplitAddress(from)
	_, toDomain, _ := config.SplitAddress(to)

	var local []*config.Mailbox
	var external []string
	if _, isLocal := dir.Domain(toDomain); isLocal || toDomain == dir.Hostname() {
		route, out := dir.Resolve(to)
		switch out {
		case directory.Unknown:
			return sendError("unknown recipient")
		case directory.NoPostmaster:
			return sendError("the recipient cannot receive mail yet")
		}
		local, external = route.Mailboxes, route.External
	} else {
		external = []string{to}
	}

	msg, err := buildMessage(dir.Hostname(), from, to, &m, time.Now())
	if err != nil {
		return err
	}
	sig := s.signOutgoing(dir, fromDomain, msg)
	signed := append([]byte(sig), msg...)

	returnPath := []byte("Return-Path: <" + from + ">\r\n")
	full := 0
	for _, box := range local {
		_, err := s.store.Append(ctx, box.StorageID, storage.Inbox,
			io.MultiReader(bytes.NewReader(returnPath), bytes.NewReader(signed)), storage.AppendOptions{})
		switch {
		case err == nil:
		case errors.Is(err, storage.ErrOverQuota):
			full++
		default:
			return fmt.Errorf("store message: %w", err)
		}
	}
	if full > 0 && full == len(local) && len(external) == 0 {
		return sendError("the recipient's mailbox is full")
	}
	if len(external) > 0 {
		if err := s.queue.Enqueue(ctx, bytes.NewReader(signed), from, "", external); err != nil {
			return fmt.Errorf("enqueue: %w", err)
		}
	}
	s.log.Info("mail: panel message accepted", "domain", fromDomain,
		"local", len(local)-full, "external", len(external), "signed", sig != "")
	return nil
}

// checkOutgoing validates m and returns the normalized sender and
// recipient addresses.
func checkOutgoing(dir *directory.Directory, m *config.OutgoingMail) (from, to string, err error) {
	for _, f := range []string{m.From, m.FromName, m.To, m.Subject} {
		if hasControl(f) {
			return "", "", sendError("header fields must not contain line breaks or control characters")
		}
	}
	for _, f := range []string{m.FromName, m.Subject, m.Text, m.HTML} {
		if !utf8.ValidString(f) {
			return "", "", sendError("the message is not valid UTF-8")
		}
	}
	from = directory.Normalize(m.From)
	if !config.ValidAddress(from) {
		return "", "", sendError("the sender is not a valid address")
	}
	_, fromDomain, _ := config.SplitAddress(from)
	if _, ok := dir.Domain(fromDomain); !ok {
		return "", "", sendError("the sender's domain is not a mail domain of this server")
	}
	to = directory.Normalize(m.To)
	if !config.ValidAddress(to) || len(to) > 254 {
		return "", "", sendError("the recipient must be a single valid address")
	}
	switch {
	case strings.TrimSpace(m.Subject) == "":
		return "", "", sendError("the subject is empty")
	case len(m.Subject) > maxSubjectBytes:
		return "", "", sendError("the subject is too long")
	case len(m.FromName) > maxFromNameBytes:
		return "", "", sendError("the sender name is too long")
	case strings.TrimSpace(m.Text) == "":
		return "", "", sendError("the text is empty")
	}
	return from, to, nil
}

// hasControl tells whether v has a control character (CR and LF above
// all, which would start a new header field); tab is allowed.
func hasControl(v string) bool {
	for _, c := range v {
		if c < 0x20 && c != '\t' || c == 0x7f {
			return true
		}
	}
	return false
}

// buildMessage writes the RFC 5322 message with CRLF line endings: text
// only, or multipart/alternative with text and HTML; quoted-printable
// bodies.
func buildMessage(hostname, from, to string, m *config.OutgoingMail, now time.Time) ([]byte, error) {
	if hostname == "" {
		_, hostname, _ = config.SplitAddress(from)
	}
	id := make([]byte, 16)
	rand.Read(id)

	var b bytes.Buffer
	sender := mail.Address{Name: m.FromName, Address: from}
	fmt.Fprintf(&b, "From: %s\r\n", sender.String())
	fmt.Fprintf(&b, "To: <%s>\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", encodeSubject(m.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", hex.EncodeToString(id), hostname)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Auto-Submitted: auto-generated\r\n")

	if m.HTML == "" {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
		b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		if err := writeQP(&b, m.Text); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fmt.Fprintf(&b, "Content-Type: %s\r\n\r\n", mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": mw.Boundary()}))
	for _, part := range []struct{ typ, content string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", part.typ+"; charset=utf-8")
		h.Set("Content-Transfer-Encoding", "quoted-printable")
		pw, err := mw.CreatePart(h)
		if err != nil {
			return nil, err
		}
		if err := writeQP(pw, part.content); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	b.Write(body.Bytes())
	return b.Bytes(), nil
}

// writeQP writes text quoted-printable; the encoder turns line breaks into
// CRLF. The body ends with a line break.
func writeQP(w io.Writer, text string) error {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	qw := quotedprintable.NewWriter(w)
	if _, err := io.WriteString(qw, text); err != nil {
		return err
	}
	return qw.Close()
}

// encodeSubject Q-encodes a subject that is not plain ASCII, folding
// between the encoded words so no line gets too long.
func encodeSubject(v string) string {
	e := mime.QEncoding.Encode("utf-8", v)
	if e == v {
		return v
	}
	return strings.ReplaceAll(e, "?= =?utf-8?q?", "?=\r\n =?utf-8?q?")
}

// signOutgoing returns the DKIM-Signature header for msg, or "" when the
// domain has no key or signing failed (the message goes out unsigned).
func (s *Server) signOutgoing(dir *directory.Directory, domain string, msg []byte) string {
	d, ok := dir.Domain(domain)
	if !ok {
		return ""
	}
	sig, err := s.signer.Signature(d, bytes.NewReader(msg))
	if errors.Is(err, auth.ErrNoKey) {
		return ""
	}
	if err != nil {
		s.log.Error("mail: DKIM signing failed", "domain", domain, "err", err)
		return ""
	}
	return sig
}
