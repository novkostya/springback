package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTheTagFollowsTheAssets is the bug this replaced: the tag used to be the version string, so
// two builds of DIFFERENT assets at the same version — every `make dev`, and any rebuild at a
// tag — answered 304 and the browser kept the file it had. Reported as a UI that would not
// update, against a server that was serving the new one to anything without a cache.
func TestTheTagFollowsTheAssets(t *testing.T) {
	if etag == `"unfingerprinted"` {
		t.Fatal("the assets could not be hashed")
	}
	// A hash, not a version: it must not be something a release would produce.
	if strings.Contains(etag, "0.0.0") || strings.Contains(etag, "dev") {
		t.Errorf("etag %s looks like a version rather than a fingerprint", etag)
	}
	if again := fingerprint(); again != etag {
		t.Errorf("fingerprint is not stable: %s then %s", etag, again)
	}
}

// TestUnchangedAssetsStillRevalidateCheaply: the point of the tag is that the common case costs
// no body. Losing that would mean re-sending the whole UI on every navigation.
func TestUnchangedAssetsStillRevalidateCheaply(t *testing.T) {
	h := Handler()

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if first.Code != http.StatusOK || first.Body.Len() == 0 {
		t.Fatalf("first request = %d, %d bytes", first.Code, first.Body.Len())
	}
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag offered, so nothing can be revalidated")
	}

	again := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	r.Header.Set("If-None-Match", tag)
	h.ServeHTTP(again, r)
	if again.Code != http.StatusNotModified {
		t.Errorf("revalidation = %d, want 304", again.Code)
	}
	if again.Body.Len() != 0 {
		t.Errorf("304 carried %d bytes", again.Body.Len())
	}

	// A tag from a different build must NOT be honoured.
	stale := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	r2.Header.Set("If-None-Match", `"0.0.0-dev"`)
	h.ServeHTTP(stale, r2)
	if stale.Code != http.StatusOK {
		t.Errorf("a stale tag was accepted: %d", stale.Code)
	}
}
