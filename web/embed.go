package web

import "embed"

// Files contains the UI assets bundled into the daemon binary.
//
//go:embed *.html *.css *.js
var Files embed.FS
