package filter

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"mime"
	"net/mail"
	"net/textproto"
	"path"
	"regexp"
	"strings"

	"github.com/emersion/go-message"
)

// CheckHeader scores the header of an incoming message and returns the
// domains of its From addresses (lowercase, without duplicates) for DMARC.
// rcptDomains are the domains of the message's local recipients; own tells
// whether a domain is one of this server's.
func CheckHeader(h textproto.MIMEHeader, rcptDomains []string, own func(string) bool) (Score, []string) {
	var sc Score
	if strings.TrimSpace(h.Get("Message-Id")) == "" {
		sc.Add(PointsNoMessageID, "no-message-id")
	}
	if d := strings.TrimSpace(h.Get("Date")); d == "" {
		sc.Add(PointsNoDate, "no-date")
	} else if _, err := mail.ParseDate(d); err != nil {
		sc.Add(PointsNoDate, "bad-date")
	}
	values := h.Values("From")
	switch {
	case len(values) == 0:
		sc.Add(PointsBadFrom, "no-from")
		return sc, nil
	case len(values) > 1:
		sc.Add(PointsBadFrom, "multiple-from")
	}
	var domains []string
	spoofed, bad := false, false
	for _, v := range values {
		list, err := (&mail.AddressParser{WordDecoder: wordDecoder}).ParseList(v)
		if err != nil || len(list) == 0 {
			bad = true
			continue
		}
		for _, a := range list {
			i := strings.LastIndexByte(a.Address, '@')
			if i < 0 {
				bad = true
				continue
			}
			d := strings.ToLower(a.Address[i+1:])
			if !contains(domains, d) {
				domains = append(domains, d)
			}
			if own(d) || a.Name == "" {
				continue
			}
			name := strings.ToLower(a.Name)
			for _, rd := range rcptDomains {
				if strings.Contains(name, "@"+rd) {
					spoofed = true
				}
			}
		}
	}
	if bad {
		sc.Add(PointsBadFrom, "bad-from")
	}
	if spoofed {
		sc.Add(PointsSpoofedName, "spoofed-name")
	}
	return sc, domains
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var wordDecoder = &mime.WordDecoder{CharsetReader: func(_ string, r io.Reader) (io.Reader, error) {
	// Unknown charsets: keep the bytes; only addresses and ASCII
	// fragments matter here.
	return r, nil
}}

// dangerous are file name extensions of attachments that are refused:
// programs, scripts, shortcuts, disk images and Office files with macros.
var dangerous = map[string]bool{
	"exe": true, "scr": true, "com": true, "bat": true, "cmd": true, "pif": true,
	"js": true, "jse": true, "vbs": true, "vbe": true, "wsf": true, "wsh": true, "ps1": true,
	"msi": true, "msp": true, "jar": true, "lnk": true, "hta": true, "cpl": true, "reg": true,
	"scf": true, "iso": true, "img": true, "vhd": true, "vhdx": true,
	"docm": true, "dotm": true, "xlsm": true, "xltm": true, "xlam": true,
	"pptm": true, "potm": true, "ppam": true, "ppsm": true, "sldm": true,
}

// DangerousName tells whether a file name has a refused extension.
func DangerousName(name string) bool {
	name = strings.ToLower(strings.TrimRight(name, " .\t"))
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	i := strings.LastIndexByte(name, '.')
	return i >= 0 && dangerous[name[i+1:]]
}

// Limits of the attachment scan.
const (
	maxDepth    = 5
	maxParts    = 500
	maxZipBytes = 10 << 20
	maxZipFiles = 1000
)

// DangerousAttachment looks through the MIME structure of the message in
// r and returns the name of the first attachment with a refused extension
// (also inside .zip attachments, one level deep). parsed is false when the
// structure could not be read completely; that alone is no reason to
// refuse the message.
func DangerousAttachment(r io.Reader) (name string, parsed bool) {
	e, err := message.Read(r)
	if e == nil || (err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err)) {
		return "", false
	}
	s := &scan{parsed: true}
	s.walk(e, 0)
	return s.found, s.parsed
}

type scan struct {
	found  string
	parsed bool
	parts  int
}

func (s *scan) walk(e *message.Entity, depth int) {
	if s.found != "" {
		return
	}
	s.parts++
	if s.parts > maxParts || depth > maxDepth {
		s.parsed = false
		return
	}
	names := fileNames(e.Header)
	for _, n := range names {
		if DangerousName(n) {
			s.found = n
			return
		}
	}
	mt, _, _ := e.Header.ContentType()
	switch {
	case strings.HasPrefix(mt, "multipart/"):
		mr := e.MultipartReader()
		if mr == nil {
			s.parsed = false
			return
		}
		for {
			p, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				return
			}
			if p == nil || (err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err)) {
				s.parsed = false
				return
			}
			s.walk(p, depth+1)
			if s.found != "" {
				return
			}
		}
	case mt == "message/rfc822":
		inner, err := message.Read(e.Body)
		if inner == nil || (err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err)) {
			s.parsed = false
			return
		}
		s.walk(inner, depth+1)
	case isZip(mt, names):
		if n := zipDangerous(e.Body); n != "" {
			s.found = n
		}
	}
}

func isZip(mt string, names []string) bool {
	if mt == "application/zip" || mt == "application/x-zip-compressed" {
		return true
	}
	for _, n := range names {
		if strings.HasSuffix(strings.ToLower(strings.TrimSpace(n)), ".zip") {
			return true
		}
	}
	return false
}

// zipDangerous returns the first refused file name inside a zip archive
// (names only; nothing is unpacked). Archives over maxZipBytes or that
// cannot be read are not looked into.
func zipDangerous(body io.Reader) string {
	b, err := io.ReadAll(io.LimitReader(body, maxZipBytes+1))
	if err != nil || len(b) > maxZipBytes {
		return ""
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return ""
	}
	for i, f := range zr.File {
		if i >= maxZipFiles {
			break
		}
		if DangerousName(f.Name) {
			return f.Name
		}
	}
	return ""
}

// nameParam finds a (file)name parameter in a header value that
// mime.ParseMediaType refused; mail clients are lenient, so this is too.
var nameParam = regexp.MustCompile(`(?i)\bname\*?(?:\*\d+)?\*?\s*=\s*"?([^";\r\n]+)`)

// fileNames returns the file names an entity declares (Content-Disposition
// filename, Content-Type name), decoded.
func fileNames(h message.Header) []string {
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if d, err := wordDecoder.DecodeHeader(v); err == nil {
			v = d
		}
		out = append(out, v)
	}
	if _, p, err := h.ContentDisposition(); err == nil {
		add(p["filename"])
	} else {
		for _, m := range nameParam.FindAllStringSubmatch(h.Get("Content-Disposition"), -1) {
			add(m[1])
		}
	}
	if _, p, err := h.ContentType(); err == nil {
		add(p["name"])
	} else {
		for _, m := range nameParam.FindAllStringSubmatch(h.Get("Content-Type"), -1) {
			add(m[1])
		}
	}
	return out
}
