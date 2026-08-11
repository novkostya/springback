package httpapi

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/novkostya/springback/core/internal/auth"
)

// sessionMaxAge is how long the cookie is allowed to sit in the browser. It matches the
// service's idle timeout, so the cookie and the session it names die together rather than the
// browser holding a cookie the server forgot months ago.
const sessionMaxAge = 14 * 24 * 60 * 60

// authStatus is the one endpoint the UI may call before signing in: it says which of the three
// screens to draw, and whether to warn about the connection.
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	writeJSONRaw(w, http.StatusOK, auth.MarshalState(
		s.Auth.State(auth.TokenFrom(r)),
		auth.SecureOrigin(r),
		auth.IsLoopback(r.Host),
	))
}

type passwordReq struct {
	Password string `json:"password"`
}

// authSetup takes the first password. Refuses once one exists.
func (s *Server) authSetup(w http.ResponseWriter, r *http.Request) {
	var req passwordReq
	if !decodeBody(w, r, &req) {
		return
	}
	switch err := s.Auth.SetPassword(req.Password); {
	case errors.Is(err, auth.ErrWeakPassword):
		writeErr(w, http.StatusUnprocessableEntity, "weak_password",
			"Use at least 8 characters.")
		return
	case errors.Is(err, auth.ErrAlreadySetUp):
		// 409, and deliberately not "wrong password": this install already belongs to
		// someone, and the person seeing this is not necessarily them.
		writeErr(w, http.StatusConflict, "already_set_up",
			"This springback already has a password. Sign in with it, or delete the `password` file in the accounts directory to start over.")
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	s.issueSession(w, r, req.Password)
}

// authLogin exchanges the password for a session.
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	var req passwordReq
	if !decodeBody(w, r, &req) {
		return
	}
	s.issueSession(w, r, req.Password)
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, password string) {
	token, err := s.Auth.Login(password, clientIP(r))
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		writeErr(w, http.StatusTooManyRequests, "rate_limited",
			"Too many attempts. Wait a minute and try again.")
		return
	case errors.Is(err, auth.ErrBadCredentials):
		writeErr(w, http.StatusUnauthorized, "bad_password", "Wrong password.")
		return
	case errors.Is(err, auth.ErrNoPassword):
		writeErr(w, http.StatusConflict, "needs_setup", "No password has been set yet.")
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	http.SetCookie(w, auth.CookieForToken(r, token, sessionMaxAge))
	writeJSONRaw(w, http.StatusOK, auth.MarshalState(
		auth.StateAuthenticated, auth.SecureOrigin(r), auth.IsLoopback(r.Host)))
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	s.Auth.Logout(auth.TokenFrom(r))
	// MaxAge -1 deletes it. Same attributes as the one that was set, or the browser keeps the
	// original alongside this one.
	http.SetCookie(w, auth.CookieForToken(r, "", -1))
	w.WriteHeader(http.StatusNoContent)
}

// guard is the gate. Everything that is not an auth endpoint, the health check or a static asset
// needs a session.
//
// THE UI IS SERVED UNAUTHENTICATED, ON PURPOSE. It is three files of markup with no secrets in
// them, and it has to load in order to draw the login form at all. Every byte of actual content
// — devices, library, accounts, icons — comes from /api, which is behind this.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || authExempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// CSRF, cheaply: SameSite=Lax already stops a cross-site form navigation, and this
		// refuses anything that names a different origin outright.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !auth.SameOrigin(r) {
			writeErr(w, http.StatusForbidden, "cross_origin",
				"This request came from another site.")
			return
		}

		if err := s.Auth.Check(auth.TokenFrom(r)); err != nil {
			// 401 with a machine-readable reason, so the UI can swap to the login screen
			// rather than showing "HTTP 401" over an empty list.
			writeErr(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authExempt lists the paths reachable without a session, and it is a SHORT list on purpose:
// /api/health so a container probe does not need credentials, and the auth endpoints themselves,
// which would otherwise require the session they exist to create.
func authExempt(path string) bool {
	switch path {
	case "/api/health", "/api/auth/status", "/api/auth/setup", "/api/auth/login", "/api/auth/logout":
		return true
	}
	return false
}

func clientIP(r *http.Request) string {
	// X-Forwarded-For's FIRST entry is the client as the nearest proxy saw it. Believed for
	// the same reason as X-Forwarded-Proto: the documented deployment is behind a proxy, and
	// the only thing derived from it is a login throttle. A client that spoofs it throttles
	// itself less; it cannot use it to lock anyone else out, because the limit is per-key and
	// a successful login clears it.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "Could not read that request.")
		return false
	}
	return true
}

func writeJSONRaw(w http.ResponseWriter, code int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}
