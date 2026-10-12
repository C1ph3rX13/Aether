package main

import (
	"io/fs"

	"github.com/C1ph3rX13/Aether/web"
)

// webFS returns the production Web resources embedded in the binary.
func webFS() fs.FS {
	return web.StaticFS()
}
