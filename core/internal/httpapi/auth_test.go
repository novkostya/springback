package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/novkostya/springback/core/internal/auth"
	"github.com/novkostya/springback/core/internal/store"
	"github.com/novkostya/springback/core/internal/tools"
)

func testServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	a, err := auth.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Cheap derivation: this file logs in repeatedly and the production parameters are meant
	// to cost 50ms each.
	a.Params = auth.Params{Memory: 8, Iterations: 1, Parallelism: 1, SaltLen: 8, KeyLen: 16}

	s := &Server{
		Tools:    tools.NewFake(),
		Auth:     a,
		Library:  store.NewLibrary(dir),
		Accounts: store.NewAccounts(dir),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return s, s.Handler()
}

func do(h http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no session cookie in response (status %d)", w.Code)
	return nil
}

// TestAPIIsClosedUntilSignedIn is the point of the whole package: every route that can reach a
// device, an Apple ID or the library must 401 without a session.
func TestAPIIsClosedUntilSignedIn(t *testing.T) {
	_, h := testServer(t)

	protected := []struct{ method, path string }{
		{"GET", "/api/devices"},
		{"GET", "/api/devices/UDID/apps"},
		{"GET", "/api/devices/UDID/installed"},
		{"POST", "/api/devices/UDID/install"},
		{"GET", "/api/devices/UDID/icon.png?bundle=a&v=1"},
		{"GET", "/api/library"},
		{"POST", "/api/library"},
		{"DELETE", "/api/library/1"},
		{"GET", "/api/library/1/icon.png"},
		{"GET", "/api/accounts"},
		{"POST", "/api/accounts"},
		{"DELETE", "/api/accounts/x"},
		{"GET", "/api/jobs"},
		{"GET", "/api/lookup?bundle_id=x"},
	}
	for _, p := range protected {
		w := do(h, p.method, p.path, "{}")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", p.method, p.path, w.Code)
		}
	}
}

// TestExemptRoutesStayOpen: a health probe must not need credentials, and the auth endpoints
// cannot require the session they exist to hand out.
func TestExemptRoutesStayOpen(t *testing.T) {
	_, h := testServer(t)
	for _, p := range []string{"/api/health", "/api/auth/status"} {
		if w := do(h, "GET", p, ""); w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", p, w.Code)
		}
	}
	// And the UI itself, or there is nothing to draw a login form with.
	if w := do(h, "GET", "/", ""); w.Code != http.StatusOK {
		t.Errorf("GET / = %d, want 200", w.Code)
	}
}

func TestSetupThenUseTheAPI(t *testing.T) {
	_, h := testServer(t)

	var st struct{ State, Username string }
	if err := json.Unmarshal(do(h, "GET", "/api/auth/status", "").Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.State != auth.StateNeedsSetup {
		t.Fatalf("fresh install state = %q, want %q", st.State, auth.StateNeedsSetup)
	}
	if st.Username == "" {
		t.Error("status carries no username for the login form to prefill")
	}

	w := do(h, "POST", "/api/auth/setup", `{"password":"a good password"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("setup = %d: %s", w.Code, w.Body)
	}
	c := sessionCookie(t, w)

	// Setup signs you in, so the API opens immediately.
	if got := do(h, "GET", "/api/library", "", c); got.Code != http.StatusOK {
		t.Errorf("GET /api/library with the setup session = %d, want 200", got.Code)
	}
	// And a second setup is refused.
	if got := do(h, "POST", "/api/auth/setup", `{"password":"another one"}`); got.Code != http.StatusConflict {
		t.Errorf("second setup = %d, want 409", got.Code)
	}
}

func TestLoginLogout(t *testing.T) {
	_, h := testServer(t)
	do(h, "POST", "/api/auth/setup", `{"password":"a good password"}`)

	if w := do(h, "POST", "/api/auth/login", `{"password":"wrong"}`); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong password = %d, want 401", w.Code)
	}

	w := do(h, "POST", "/api/auth/login", `{"password":"a good password"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", w.Code, w.Body)
	}
	c := sessionCookie(t, w)
	if got := do(h, "GET", "/api/accounts", "", c); got.Code != http.StatusOK {
		t.Fatalf("GET /api/accounts signed in = %d, want 200", got.Code)
	}

	do(h, "POST", "/api/auth/logout", "", c)
	if got := do(h, "GET", "/api/accounts", "", c); got.Code != http.StatusUnauthorized {
		t.Errorf("the session still worked after logout: %d", got.Code)
	}
}

// TestWeakPasswordRefused keeps the setup screen from accepting something that makes the whole
// exercise pointless.
func TestWeakPasswordRefused(t *testing.T) {
	_, h := testServer(t)
	if w := do(h, "POST", "/api/auth/setup", `{"password":"short"}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("short password = %d, want 422", w.Code)
	}
}

// TestCrossOriginMutationRefused: SameSite=Lax covers the form-navigation case, and this covers
// a scripted request that announces a different origin.
func TestCrossOriginMutationRefused(t *testing.T) {
	_, h := testServer(t)
	w := do(h, "POST", "/api/auth/setup", `{"password":"a good password"}`)
	c := sessionCookie(t, w)

	r := httptest.NewRequest("DELETE", "/api/library/1", nil)
	r.Host = "springback.local"
	r.Header.Set("Origin", "http://evil.example")
	r.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin DELETE = %d, want 403", rec.Code)
	}

	// Same origin still works (404 here because the library is empty — the point is that it
	// got past the guard).
	r2 := httptest.NewRequest("DELETE", "/api/library/1", nil)
	r2.Host = "springback.local"
	r2.Header.Set("Origin", "http://springback.local")
	r2.AddCookie(c)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, r2)
	if rec2.Code == http.StatusForbidden {
		t.Error("a same-origin DELETE was refused as cross-origin")
	}
}

// TestStatusReportsTransport drives the banner: the UI has to know whether the password it is
// about to send is crossing the network in the clear.
func TestStatusReportsTransport(t *testing.T) {
	_, h := testServer(t)

	check := func(host, xfp string, wantSecure, wantLoopback bool) {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/auth/status", nil)
		r.Host = host
		if xfp != "" {
			r.Header.Set("X-Forwarded-Proto", xfp)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var st struct{ Secure, Loopback bool }
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		if st.Secure != wantSecure || st.Loopback != wantLoopback {
			t.Errorf("host %q xfp %q: secure=%v loopback=%v, want %v/%v",
				host, xfp, st.Secure, st.Loopback, wantSecure, wantLoopback)
		}
	}
	check("192.168.1.5:8971", "", false, false) // the case that gets a banner
	check("localhost:8971", "", false, true)    // a secure context already
	check("springback.example", "https", true, false)
}
