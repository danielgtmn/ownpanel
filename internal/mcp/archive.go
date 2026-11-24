package mcp

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Limits of publish_files; larger sites go through an upload link.
const (
	maxPublishBytes = 10 << 20
	maxPublishFiles = 2000
)

// File is one file of a site given directly to publish_files.
type File struct {
	Path     string `json:"path" jsonschema:"file path relative to the site root, with forward slashes, e.g. index.html or css/site.css; no leading slash and no .. segments"`
	Content  string `json:"content" jsonschema:"the file content, as text (encoding utf8) or base64 encoded bytes (encoding base64)"`
	Encoding string `json:"encoding,omitempty" jsonschema:"utf8 (default) for text files, base64 for binary files such as images"`
}

// buildArchive validates files completely and only then packs them into a
// tar.gz, which the panel unpacks like any uploaded archive. Every problem is
// reported in a message meant for the caller.
func buildArchive(files []File) ([]byte, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("files is empty: give at least one file such as index.html")
	}
	if len(files) > maxPublishFiles {
		return nil, fmt.Errorf("too many files (%d): publish_files takes at most %d; use create_upload_link for bigger sites", len(files), maxPublishFiles)
	}

	type entry struct {
		path string
		data []byte
	}
	entries := make([]entry, 0, len(files))
	seen := map[string]bool{}
	total := 0
	for _, f := range files {
		if err := checkPath(f.Path); err != nil {
			return nil, err
		}
		if seen[f.Path] {
			return nil, fmt.Errorf("the path %q is given twice", f.Path)
		}
		seen[f.Path] = true

		var data []byte
		switch f.Encoding {
		case "", "utf8":
			data = []byte(f.Content)
		case "base64":
			// Bound the size before decoding so a huge string is never expanded.
			if base64.StdEncoding.DecodedLen(len(f.Content)) > maxPublishBytes-total+2 {
				return nil, tooLarge()
			}
			var err error
			if data, err = base64.StdEncoding.DecodeString(f.Content); err != nil {
				return nil, fmt.Errorf("file %q: the content is not valid base64 (standard alphabet, with padding)", f.Path)
			}
		default:
			return nil, fmt.Errorf("file %q: encoding must be \"utf8\" or \"base64\", not %q", f.Path, f.Encoding)
		}
		if total += len(data); total > maxPublishBytes {
			return nil, tooLarge()
		}
		entries = append(entries, entry{f.Path, data})
	}
	// A path cannot be both a file and a folder.
	for p := range seen {
		for i := range p {
			if p[i] == '/' && seen[p[:i]] {
				return nil, fmt.Errorf("%q is both a file and a folder (%q lies inside it)", p[:i], p)
			}
		}
	}

	slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.path, b.path) })
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	now := time.Now()
	for _, e := range entries {
		hdr := &tar.Header{Name: e.path, Mode: 0o644, Size: int64(len(e.data)), ModTime: now, Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func tooLarge() error {
	return fmt.Errorf("the files are larger than %d MB in total; use create_upload_link for bigger sites", maxPublishBytes>>20)
}

func checkPath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("a file has an empty path")
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("path %q must be relative to the site root, without a leading slash", p)
	case strings.ContainsAny(p, "\\\x00"):
		return fmt.Errorf("path %q must use forward slashes and no control characters", p)
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "..":
			return fmt.Errorf("path %q must not contain \"..\"", p)
		case ".", "":
			return fmt.Errorf("path %q must be a clean relative path like css/site.css", p)
		}
	}
	return nil
}
