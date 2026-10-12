//go:build !embedstatic

// Package web owns the production Aether frontend assets.
package web

import "io/fs"

// StaticFS returns nil in builds that do not include the production export.
func StaticFS() fs.FS {
	return nil
}
