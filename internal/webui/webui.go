// Package webui embeds the built web app into the binary. `make ui` copies web/dist into dist/ here before the build.
package webui

import (
	"embed"
	"io/fs"
)

// dist is the web app's build. The "all:" prefix keeps every file, dot files included, and dist/README keeps the
// folder from being empty when no UI was built.
//
//go:embed all:dist
var dist embed.FS

// FS is the built web app: index.html and its assets, at the root of the tree.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// fs.Sub only fails on an invalid name, and "dist" is the name embedded above.
		panic(err)
	}
	return sub
}
