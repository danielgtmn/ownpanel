package smtpd

import (
	"io"
	"net/textproto"

	"github.com/danielgtmn/ownpanel/internal/mail/header"
)

// maxHeaderBytes bounds the header of an incoming message; a larger one
// is refused (no real mail has one).
const maxHeaderBytes = 1 << 20

var errHeaderTooLarge = header.ErrTooLarge

// field is one header field as received (package header, shared with the
// filters so both see the same fields).
type field = header.Field

// readHeader reads the header fields at the start of r (header.Read).
func readHeader(r io.Reader) ([]field, int64, []byte, error) {
	return header.Read(r, maxHeaderBytes)
}

// mimeHeader returns the fields as a textproto.MIMEHeader.
func mimeHeader(fields []field) textproto.MIMEHeader {
	h := textproto.MIMEHeader{}
	for _, f := range fields {
		k := textproto.CanonicalMIMEHeaderKey(f.Name)
		h[k] = append(h[k], f.Value())
	}
	return h
}
