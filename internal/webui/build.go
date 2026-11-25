package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
)

// Build names the built web app: a short hash of its index.html, which
// lists every script and style sheet by their content-hashed names, so it
// changes whenever the app does. The API sends it with every answer; a page
// that sees a different one than it started with was loaded from an older
// panel and reloads itself. "" when no app is built in.
func Build(fsys fs.FS) string {
	if fsys == nil {
		return ""
	}
	b, err := fs.ReadFile(fsys, indexFile)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
