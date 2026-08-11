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

	"github.com/novkostya/springback/core/internal/version"
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
		// THE BROWSER MUST REVALIDATE, and this is a bug fix rather than a precaution.
		// Files from an embed.FS have a ZERO modification time, so net/http emits no
		// Last-Modified and no ETag — nothing a cache can check. Browsers fall back to
		// heuristic freshness and keep serving the old app.js, which is exactly what
		// happened: a deployed fix was invisible in the UI, and the screenshots reporting
		// it as broken were of a version that no longer existed on the server.
		//
		// The build stamp is the version: the whole UI is embedded in the binary, so every
		// asset changes exactly when the binary does. `no-cache` means "you may keep it,
		// but ask first" — so the common case is still a 304 with no body.
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", `"`+version.Version+`"`)
		if match := r.Header.Get("If-None-Match"); match == `"`+version.Version+`"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}

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
