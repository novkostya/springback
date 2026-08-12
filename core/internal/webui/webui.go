// Package webui embeds the static UI into the binary.
//
// Hand-written HTML/CSS/JS, no bundler and no lockfile. "Anything pretty" is explicitly out of
// v0.1 (SPEC §1), and a Node toolchain image, a lockfile and a build stage would cost more to
// carry than the three screens they would produce.
package webui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
)

//go:embed static
var static embed.FS

// etag identifies the ASSETS, by hashing them, rather than identifying the release.
//
// IT USED TO BE THE VERSION STRING, on the reasoning that the whole UI is embedded in the binary
// so every asset changes exactly when the binary does. That is true one way round and false the
// other: the binary changes without the version whenever it is rebuilt at the same version — which
// is every single `make dev` build, where the version is the constant `0.0.0-dev`.
//
// The result was a browser that revalidated politely, got 304 Not Modified, and kept serving the
// app.js it had. Reported as "still seeing huge buttons" against a demo that had been rebuilt
// minutes earlier and was serving the new file to anything without a cache. The screenshots were
// of a UI that no longer existed.
//
// Hashing costs one pass over ~100 KB at startup and removes the whole class: the tag changes if
// and only if what is served changes.
var etag = fingerprint()

func fingerprint() string {
	h := sha256.New()
	// WalkDir is deterministic — it walks in lexical order — so the same assets always produce
	// the same tag, on any machine and in any build.
	err := fs.WalkDir(static, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := static.ReadFile(p)
		if err != nil {
			return err
		}
		h.Write([]byte(p))
		h.Write(b)
		return nil
	})
	if err != nil {
		// Unreachable for an embed.FS, and a wrong tag is worse than none: fall back to a
		// value that cannot match anything and so always refetches.
		return `"unfingerprinted"`
	}
	return `"` + hex.EncodeToString(h.Sum(nil)[:8]) + `"`
}

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
		// The tag is a hash of the embedded assets — see fingerprint. `no-cache` means "you
		// may keep it, but ask first", so the common case is still a 304 with no body, and
		// the moment the assets differ the answer is the new file instead.
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
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
