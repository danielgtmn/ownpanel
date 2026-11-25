package dav

import (
	"bufio"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

// Namespaces.
const (
	nsDAV   = "DAV:"
	nsCal   = "urn:ietf:params:xml:ns:caldav"
	nsCard  = "urn:ietf:params:xml:ns:carddav"
	nsCS    = "http://calendarserver.org/ns/"
	nsApple = "http://apple.com/ns/ical/"
)

// prefixes are the prefixes declared on every answer.
var prefixes = []struct{ prefix, ns string }{
	{"d", nsDAV}, {"c", nsCal}, {"card", nsCard}, {"cs", nsCS}, {"a", nsApple},
}

func prefixOf(ns string) string {
	for _, p := range prefixes {
		if p.ns == ns {
			return p.prefix
		}
	}
	return ""
}

func dn(local string) xml.Name  { return xml.Name{Space: nsDAV, Local: local} }
func cn(local string) xml.Name  { return xml.Name{Space: nsCal, Local: local} }
func crn(local string) xml.Name { return xml.Name{Space: nsCard, Local: local} }

// Limits of request bodies of XML requests (untrusted input).
const (
	maxXMLBytes = 1 << 20
	maxXMLDepth = 32
	maxXMLNodes = 20_000
)

var errBadXML = errors.New("the request body is not valid XML")

// node is an element of a request body.
type node struct {
	name xml.Name
	attr []xml.Attr
	kids []*node
	text strings.Builder
}

func (n *node) child(name xml.Name) *node {
	if n == nil {
		return nil
	}
	for _, k := range n.kids {
		if k.name == name {
			return k
		}
	}
	return nil
}

func (n *node) children(name xml.Name) []*node {
	var out []*node
	if n == nil {
		return nil
	}
	for _, k := range n.kids {
		if k.name == name {
			out = append(out, k)
		}
	}
	return out
}

func (n *node) attrValue(local string) (string, bool) {
	for _, a := range n.attr {
		if a.Name.Local == local && (a.Name.Space == "" || a.Name.Space == n.name.Space) {
			return a.Value, true
		}
	}
	return "", false
}

// parseXML reads a request body into a tree with resolved namespaces,
// bounded in size, depth and number of elements. A body without elements gives nil. Document type
// declarations are refused; the decoder never loads entities.
func parseXML(r io.Reader) (*node, error) {
	lr := &io.LimitedReader{R: r, N: maxXMLBytes + 1}
	d := xml.NewDecoder(lr)
	d.Strict = true
	var root *node
	var stack []*node
	nodes := 0
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if lr.N <= 0 {
			return nil, errTooLarge
		}
		if err != nil {
			return nil, errBadXML
		}
		switch t := tok.(type) {
		case xml.StartElement:
			nodes++
			if nodes > maxXMLNodes || len(stack) >= maxXMLDepth {
				return nil, errTooLarge
			}
			n := &node{name: t.Name}
			for _, a := range t.Attr {
				if a.Name.Space != "xmlns" && !(a.Name.Space == "" && a.Name.Local == "xmlns") {
					n.attr = append(n.attr, a)
				}
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, errBadXML
				}
				root = n
			} else {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errBadXML
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(t)
			}
		case xml.Directive:
			return nil, errBadXML
		}
	}
	if len(stack) != 0 {
		return nil, errBadXML
	}
	return root, nil
}

var errTooLarge = errors.New("the request body is too large")

// readXML reads the body of an XML request; it answers itself on failure.
func readXML(w http.ResponseWriter, r *http.Request) (*node, bool) {
	root, err := parseXML(r.Body)
	switch {
	case errors.Is(err, errTooLarge):
		http.Error(w, "The request body is too large.", http.StatusRequestEntityTooLarge)
		return nil, false
	case err != nil:
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "The request body is too large.", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "The request body is not valid XML.", http.StatusBadRequest)
		}
		return nil, false
	}
	return root, true
}

// ---- writing ----

func escText(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func escAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;", "\r", "&#xD;", "\n", "&#xA;", "\t", "&#x9;")
	return r.Replace(s)
}

// validXMLName tells whether s can be the local name of an element we
// write: an XML name without a colon (NCName). Names parsed by
// encoding/xml are; names taken from attributes must be checked.
func validXMLName(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || unicode.IsLetter(r):
		case i > 0 && (r == '-' || r == '.' || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r)):
		default:
			return false
		}
	}
	return true
}

// elem writes an element with raw inner XML ("" makes it empty).
func elem(name xml.Name, inner string) string {
	open, closeTag := tagOf(name)
	if inner == "" {
		return open[:len(open)-1] + "/>"
	}
	return open + inner + closeTag
}

func tagOf(name xml.Name) (string, string) {
	if p := prefixOf(name.Space); p != "" {
		return "<" + p + ":" + name.Local + ">", "</" + p + ":" + name.Local + ">"
	}
	if name.Space == "" {
		return "<" + name.Local + ` xmlns="">`, "</" + name.Local + ">"
	}
	return "<x:" + name.Local + ` xmlns:x="` + escAttr(name.Space) + `">`, "</x:" + name.Local + ">"
}

func href(p string) string { return elem(dn("href"), escText(p)) }

// propResult is one property of a response.
type propResult struct {
	name   xml.Name
	inner  string
	status int
}

// msWriter streams a multistatus answer.
type msWriter struct {
	w       *bufio.Writer
	started bool
}

func newMS(w http.ResponseWriter) *msWriter {
	return &msWriter{w: bufio.NewWriterSize(w, 32<<10)}
}

func (m *msWriter) start(w http.ResponseWriter) {
	if m.started {
		return
	}
	m.started = true
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	m.w.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n<d:multistatus")
	for _, p := range prefixes {
		m.w.WriteString(` xmlns:` + p.prefix + `="` + p.ns + `"`)
	}
	m.w.WriteString(">\n")
}

func statusLine(code int) string {
	return "HTTP/1.1 " + strconv.Itoa(code) + " " + http.StatusText(code)
}

// response writes the properties of one resource, grouped by status.
func (m *msWriter) response(w http.ResponseWriter, hrefPath string, props []propResult) {
	m.start(w)
	m.w.WriteString("<d:response>" + href(hrefPath))
	var order []int
	groups := map[int][]propResult{}
	for _, p := range props {
		if !validXMLName(p.name.Local) {
			continue // never written; only names we parsed get here
		}
		if _, ok := groups[p.status]; !ok {
			order = append(order, p.status)
		}
		groups[p.status] = append(groups[p.status], p)
	}
	for _, st := range order {
		m.w.WriteString("<d:propstat><d:prop>")
		for _, p := range groups[st] {
			if st == http.StatusOK {
				m.w.WriteString(elem(p.name, p.inner))
			} else {
				m.w.WriteString(elem(p.name, ""))
			}
		}
		m.w.WriteString("</d:prop><d:status>" + statusLine(st) + "</d:status></d:propstat>")
	}
	m.w.WriteString("</d:response>\n")
}

// status writes a response with a status only.
func (m *msWriter) status(w http.ResponseWriter, hrefPath string, code int) {
	m.start(w)
	m.w.WriteString("<d:response>" + href(hrefPath) + "<d:status>" + statusLine(code) + "</d:status></d:response>\n")
}

// end closes the answer, with a sync token if given.
func (m *msWriter) end(w http.ResponseWriter, syncToken string) error {
	m.start(w)
	if syncToken != "" {
		m.w.WriteString(elem(dn("sync-token"), escText(syncToken)))
	}
	m.w.WriteString("</d:multistatus>\n")
	return m.w.Flush()
}

// writeError answers with a DAV error body naming a precondition.
func writeError(w http.ResponseWriter, code int, condition xml.Name, inner string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(code)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n<d:error")
	for _, p := range prefixes {
		b.WriteString(` xmlns:` + p.prefix + `="` + p.ns + `"`)
	}
	b.WriteString(">" + elem(condition, inner) + "</d:error>\n")
	io.WriteString(w, b.String())
}
