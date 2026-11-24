package panel

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"strings"

	"golang.org/x/net/html"
)

const (
	// MaxSiteIconBytes caps an icon file.
	MaxSiteIconBytes = 256 << 10
	// maxDataIconBytes caps an icon written into index.html as a data: URL.
	maxDataIconBytes = 64 << 10
	// maxIndexScanBytes is how much of index.html is searched for icon links.
	maxIndexScanBytes = 512 << 10
)

// SiteIcon is the icon of a site's live release.
type SiteIcon struct {
	Release     string
	Path        string // inside the release; "index.html#data" for a data: URL
	ContentType string
	Data        []byte
}

var iconTypes = map[string]string{
	".png":  "image/png",
	".ico":  "image/x-icon",
	".svg":  "image/svg+xml",
	".webp": "image/webp",
	".gif":  "image/gif",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
}

var iconFiles = []string{"favicon.svg", "favicon.png", "favicon.ico", "apple-touch-icon.png"}

// SiteIcon finds the icon of a site in its live release only: a <link
// rel="icon"> (or "shortcut icon", "apple-touch-icon") in the root
// index.html pointing at a file in the same release or holding a small data:
// URL, else one of the usual icon files at the root. Nothing is fetched from
// elsewhere. NotFound when there is none.
func (s *Service) SiteIcon(ctx context.Context, a Actor, name string) (SiteIcon, error) {
	site, err := s.visibleSite(ctx, a, name)
	if err != nil {
		return SiteIcon{}, err
	}
	release := s.deploys.Current(site.Name)
	if release == "" {
		return SiteIcon{}, errorf(NotFound, "site has no published files")
	}
	// os.Root keeps every lookup inside the release, whatever a link says.
	root, err := os.OpenRoot(s.deploys.ReleaseDir(site.Name, release))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return SiteIcon{}, errorf(NotFound, "site has no published files")
		}
		return SiteIcon{}, newError(Internal, err)
	}
	defer root.Close()

	icon, ok := findSiteIcon(root)
	if !ok {
		return SiteIcon{}, errorf(NotFound, "site has no icon")
	}
	icon.Release = release
	return icon, nil
}

func findSiteIcon(root *os.Root) (SiteIcon, bool) {
	for _, href := range iconLinks(root) {
		if icon, ok := iconFromHref(root, href); ok {
			return icon, true
		}
	}
	for _, name := range iconFiles {
		if icon, ok := iconFile(root, name); ok {
			return icon, true
		}
	}
	return SiteIcon{}, false
}

// iconLinks returns the hrefs of the icon links in index.html, "icon" and
// "shortcut icon" before "apple-touch-icon", each in document order.
func iconLinks(root *os.Root) []string {
	f, err := root.Open("index.html")
	if err != nil {
		return nil
	}
	defer f.Close()
	var icons, touch []string
	z := html.NewTokenizer(io.LimitReader(f, maxIndexScanBytes))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		tag, hasAttr := z.TagName()
		if string(tag) == "body" {
			break
		}
		if string(tag) != "link" || !hasAttr {
			continue
		}
		var rel, href string
		for {
			key, val, more := z.TagAttr()
			switch string(key) {
			case "rel":
				rel = strings.ToLower(string(val))
			case "href":
				href = strings.TrimSpace(string(val))
			}
			if !more {
				break
			}
		}
		if href == "" {
			continue
		}
		tokens := strings.Fields(rel)
		switch {
		case containsToken(tokens, "icon"):
			icons = append(icons, href)
		case containsToken(tokens, "apple-touch-icon"):
			touch = append(touch, href)
		}
	}
	return append(icons, touch...)
}

func containsToken(tokens []string, want string) bool {
	for _, t := range tokens {
		if t == want {
			return true
		}
	}
	return false
}

func iconFromHref(root *os.Root, href string) (SiteIcon, bool) {
	if len(href) >= 5 && strings.EqualFold(href[:5], "data:") {
		return iconFromData(href)
	}
	u, err := url.Parse(href)
	if err != nil || u.Scheme != "" || u.Host != "" || strings.HasPrefix(href, "//") || u.Path == "" {
		return SiteIcon{}, false
	}
	// Links may not climb out of the release, not even ones that would
	// land back inside it.
	for _, part := range strings.Split(u.Path, "/") {
		if part == ".." {
			return SiteIcon{}, false
		}
	}
	name := strings.TrimPrefix(path.Clean("/"+u.Path), "/")
	if name == "" {
		return SiteIcon{}, false
	}
	return iconFile(root, name)
}

func iconFile(root *os.Root, name string) (SiteIcon, bool) {
	want, ok := iconTypes[strings.ToLower(path.Ext(name))]
	if !ok {
		return SiteIcon{}, false
	}
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > MaxSiteIconBytes {
		return SiteIcon{}, false
	}
	f, err := root.Open(name)
	if err != nil {
		return SiteIcon{}, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxSiteIconBytes+1))
	if err != nil || len(data) > MaxSiteIconBytes {
		return SiteIcon{}, false
	}
	got, ok := sniffIcon(data, want)
	if !ok {
		return SiteIcon{}, false
	}
	return SiteIcon{Path: name, ContentType: got, Data: data}, true
}

func iconFromData(href string) (SiteIcon, bool) {
	if len(href) > maxDataIconBytes*3 {
		return SiteIcon{}, false
	}
	meta, payload, ok := strings.Cut(href[len("data:"):], ",")
	if !ok {
		return SiteIcon{}, false
	}
	params := strings.Split(meta, ";")
	declared := strings.ToLower(strings.TrimSpace(params[0]))
	var data []byte
	if strings.EqualFold(strings.TrimSpace(params[len(params)-1]), "base64") {
		decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(payload), ""))
		if err != nil {
			return SiteIcon{}, false
		}
		data = decoded
	} else {
		decoded, err := url.PathUnescape(payload)
		if err != nil {
			return SiteIcon{}, false
		}
		data = []byte(decoded)
	}
	if len(data) == 0 || len(data) > maxDataIconBytes {
		return SiteIcon{}, false
	}
	if declared == "image/vnd.microsoft.icon" {
		declared = "image/x-icon"
	}
	allowed := false
	for _, t := range iconTypes {
		if t == declared {
			allowed = true
		}
	}
	if !allowed {
		return SiteIcon{}, false
	}
	got, ok := sniffIcon(data, declared)
	if !ok {
		return SiteIcon{}, false
	}
	return SiteIcon{Path: "index.html#data", ContentType: got, Data: data}, true
}

// sniffIcon checks that data really is the image type its name or data:
// URL claims and returns the type to serve. A .ico file may hold a PNG, as
// many do.
func sniffIcon(data []byte, want string) (string, bool) {
	got := sniffImage(data)
	switch {
	case got == "":
		return "", false
	case got == want:
		return got, true
	case want == "image/x-icon" && got == "image/png":
		return got, true
	}
	return "", false
}

func sniffImage(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(data, []byte{0, 0, 1, 0}):
		return "image/x-icon"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	case looksLikeSVG(data):
		return "image/svg+xml"
	}
	return ""
}

// looksLikeSVG accepts text that starts with markup (an XML declaration,
// comments or a doctype may come first) and has an <svg element early on.
func looksLikeSVG(data []byte) bool {
	head := data[:min(len(data), 4096)]
	head = bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))
	head = bytes.TrimSpace(head)
	if !bytes.HasPrefix(head, []byte("<")) || bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	return bytes.Contains(bytes.ToLower(head), []byte("<svg"))
}
