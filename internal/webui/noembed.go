//go:build !webui

package webui

import "io/fs"

// FS returns nil: this build does not include the web app (see the Makefile).
func FS() fs.FS { return nil }
