// Package ui embeds the built web UI (src/ui/web/dist, produced by
// `npm run build` in src/ui/web) into the executable.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:web/dist
var dist embed.FS

// Assets returns the UI file system, or nil when the UI was not built
// (API-only development builds).
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "web/dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
