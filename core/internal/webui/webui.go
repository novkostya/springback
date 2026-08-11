// Package webui embeds the static UI into the binary.
//
// Hand-written HTML/CSS/JS, no bundler and no lockfile. "Anything pretty" is explicitly out of
// v0.1 (SPEC §1), and a Node toolchain image, a lockfile and a build stage would cost more to
// carry than the three screens they would produce.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var static embed.FS

// Handler serves the UI at /.
func Handler() http.Handler {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		// Unreachable: the embed is checked at compile time. Panicking beats serving a
		// 500 on every page for a fault that can only be a build error.
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// One page, so anything that is not a known asset is the app itself.
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			if _, err := fs.Stat(sub, r.URL.Path[1:]); err != nil {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}
