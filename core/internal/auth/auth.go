// Package auth is the single-user password gate.
//
// springback holds live Apple ID sessions: anyone who can reach the port can download as every
// account signed in to it. Up to now the README said "keep it LAN-only" and that was the whole
// control, which is fine for a tool one person runs on their own box and not fine for one that
// gets handed to a friend.
//
// SINGLE USER, ONE PASSWORD, NO ACCOUNTS TABLE. There is exactly one person who operates a
// springback install, and inventing users, roles and invites for them would be a great deal of
// surface for no one. The password is set on first run rather than configured, so there is
// nothing to edit before the thing will start and no plaintext secret sitting in a compose file.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// The states the UI switches on.
const (
	StateNeedsSetup    = "needs_setup"
	StateNeedsLogin    = "needs_login"
	StateAuthenticated = "authenticated"
)

// CookieName is the session cookie.
const CookieName = "springback_session"

// Username is what the login form shows, readonly.
//
// THERE IS A USERNAME FIELD AND IT IS NOT DECORATION. A password manager will not reliably offer
// to save or fill a lone password input — it has nothing to key the entry on, and Safari in
// particular quietly declines. A readonly, prefilled username gives it the anchor it wants, and
// costs a single-user app nothing.
const Username = "springback"

var (
	ErrAlreadySetUp   = errors.New("a password is already set")
	ErrNoPassword     = errors.New("no password has been set yet")
	ErrBadCredentials = errors.New("wrong password")
	ErrWeakPassword   = errors.New("password too short")
	ErrRateLimited    = errors.New("too many attempts")
	ErrNoSession      = errors.New("not signed in")
)

// MinPasswordLen is deliberately modest. This is a LAN tool behind one door, and a length rule
// strict enough to be worth arguing about mostly produces a password on a sticky note.
const MinPasswordLen = 8

// Service holds the password hash and the live sessions.
type Service struct {
	dir string

	mu       sync.Mutex
	hash     string
	sessions map[string]*session // key: sha256 of the token, hex

	attempts map[string][]time.Time

	// Tunables, overridden in tests.
	Now             func() time.Time
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	// Params are the argon2id cost parameters. Exported so tests can make them cheap: the
	// production values take ~50ms per derivation on purpose, and a test suite that logs in
	// thirty times should not pay for it thirty times.
	Params Params
}

// Params are argon2id's cost knobs.
type Params struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLen     uint32
	KeyLen      uint32
}

// DefaultParams follows the RFC 9106 second recommendation (64 MiB, t=3), which is the sensible
// setting for a box that is also expected to run a container full of device tooling.
func DefaultParams() Params {
	return Params{Memory: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLen: 16, KeyLen: 32}
}

type session struct {
	created  time.Time
	lastSeen time.Time
}

// New loads any existing password from dir.
func New(dir string) (*Service, error) {
	s := &Service{
		dir:             dir,
		sessions:        map[string]*session{},
		attempts:        map[string][]time.Time{},
		Now:             time.Now,
		IdleTimeout:     14 * 24 * time.Hour,
		AbsoluteTimeout: 90 * 24 * time.Hour,
		Params:          DefaultParams(),
	}
	b, err := os.ReadFile(s.passwordPath())
	if err == nil {
		s.hash = strings.TrimSpace(string(b))
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read password file: %w", err)
	}
	return s, nil
}

func (s *Service) passwordPath() string { return filepath.Join(s.dir, "password") }

// IsSetUp reports whether a password exists.
func (s *Service) IsSetUp() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hash != ""
}

// SetPassword sets the password, once.
//
// ONE SHOT. An install that is already set up refuses, so a springback exposed by accident
// cannot be taken over by whoever reaches the setup screen first. Changing it later means
// deleting the file on the box, which requires the shell access that would let you read
// everything anyway.
func (s *Service) SetPassword(password string) error {
	if len([]rune(password)) < MinPasswordLen {
		return ErrWeakPassword
	}
	s.mu.Lock()
	if s.hash != "" {
		s.mu.Unlock()
		return ErrAlreadySetUp
	}
	params := s.Params
	s.mu.Unlock()

	// Derived OUTSIDE the lock: argon2id is meant to be slow and memory-hungry, and holding a
	// mutex across it would let one setup request stall every other handler.
	encoded, err := hashPassword(password, params)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hash != "" {
		return ErrAlreadySetUp
	}
	if err := writeFileAtomic(s.passwordPath(), []byte(encoded+"\n"), 0o600); err != nil {
		return err
	}
	s.hash = encoded
	return nil
}

// Login checks the password and returns a new session token.
func (s *Service) Login(password, clientIP string) (string, error) {
	s.mu.Lock()
	hash := s.hash
	if hash == "" {
		s.mu.Unlock()
		return "", ErrNoPassword
	}
	if !s.allowAttemptLocked(clientIP) {
		s.mu.Unlock()
		return "", ErrRateLimited
	}
	s.mu.Unlock()

	ok, err := verifyPassword(password, hash)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrBadCredentials
	}

	token, err := randomToken()
	if err != nil {
		return "", err
	}
	now := s.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	// A successful login clears the throttle for that address, so someone who mistyped twice
	// and then got it right is not still being counted against.
	delete(s.attempts, clientIP)
	s.sessions[tokenKey(token)] = &session{created: now, lastSeen: now}
	return token, nil
}

// Check validates a token and refreshes its idle clock.
func (s *Service) Check(token string) error {
	if token == "" {
		return ErrNoSession
	}
	now := s.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	key := tokenKey(token)
	sess, ok := s.sessions[key]
	if !ok {
		return ErrNoSession
	}
	if now.Sub(sess.lastSeen) > s.IdleTimeout || now.Sub(sess.created) > s.AbsoluteTimeout {
		delete(s.sessions, key)
		return ErrNoSession
	}
	sess.lastSeen = now
	return nil
}

// Valid reports whether a session is still good WITHOUT counting as activity.
//
// THE DIFFERENCE FROM Check IS THE WHOLE POINT. Check refreshes lastSeen, which is right for a
// request somebody made and wrong for the event socket, which pings itself every thirty seconds
// with nobody in the room. Using Check there would mean a forgotten tab held a session open
// forever — an idle timeout that no longer measures idleness. This lets the socket notice a dead
// session without being the reason one never dies.
func (s *Service) Valid(token string) bool {
	if token == "" {
		return false
	}
	now := s.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[tokenKey(token)]
	if !ok {
		return false
	}
	return now.Sub(sess.lastSeen) <= s.IdleTimeout && now.Sub(sess.created) <= s.AbsoluteTimeout
}

// Logout drops one session.
func (s *Service) Logout(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tokenKey(token))
}

// State reports what the UI should show.
func (s *Service) State(token string) string {
	if !s.IsSetUp() {
		return StateNeedsSetup
	}
	if s.Check(token) == nil {
		return StateAuthenticated
	}
	return StateNeedsLogin
}

// allowAttemptLocked is a per-address throttle: ten tries a minute. Enough that a person
// fumbling a password never notices, little enough that guessing over the network is hopeless.
func (s *Service) allowAttemptLocked(ip string) bool {
	const limit, window = 10, time.Minute
	now := s.Now()
	kept := s.attempts[ip][:0]
	for _, t := range s.attempts[ip] {
		if now.Sub(t) < window {
			kept = append(kept, t)
		}
	}
	s.attempts[ip] = kept
	if len(kept) >= limit {
		return false
	}
	s.attempts[ip] = append(kept, now)
	return true
}

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

// SecureOrigin reports whether the BROWSER considers this origin secure — TLS terminated here,
// or terminated by a reverse proxy that said so.
//
// X-Forwarded-Proto is believed because the documented deployment puts springback behind a proxy
// on a private network, and a header from a client that reached us directly can only ever turn a
// plain-http origin into "secure", which relaxes nothing that matters: the only decision made
// from it is whether to mark the cookie Secure, and a client lying to itself about that gains
// nothing it did not already have by holding the cookie.
func SecureOrigin(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if i := strings.IndexByte(proto, ','); i >= 0 {
		proto = proto[:i]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// IsLoopback reports whether the browser reached us at localhost.
//
// Browsers treat http://localhost as a secure context, so there is nothing to warn about there —
// and warning anyway would put a red banner on the most common way to try the thing out.
func IsLoopback(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	if strings.EqualFold(h, "localhost") || strings.HasSuffix(strings.ToLower(h), ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// CookieForToken builds the session cookie.
//
// SECURE IS NOT SET ON A PLAIN-HTTP ORIGIN, and that is the whole fix rather than an oversight.
// A cookie marked Secure is DISCARDED by the browser when it arrives over http, so the login
// succeeds, the response sets a cookie, the browser throws it away, and the next request is
// unauthenticated again — a login that fails with no error anywhere, and nothing in any log.
// The honest answer on plain http is a cookie that works plus a banner saying it is in the
// clear, which is what the UI does.
func CookieForToken(r *http.Request, token string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   SecureOrigin(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	}
}

// TokenFrom pulls the session token out of a request.
func TokenFrom(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// SameOrigin reports whether a state-changing request came from this site.
//
// The cheap half of CSRF: SameSite=Lax already blocks cross-site POSTs from a form navigation,
// and this closes the remaining case by refusing any mutating request that names a different
// origin. A request with NO Origin header is allowed — that is curl and every other non-browser
// client, which is not what CSRF is about.
func SameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	// Compare host:port, not the scheme: a proxy terminating TLS makes the browser say
	// https:// while the request reaches us as http, and that is the supported deployment.
	u := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	return strings.EqualFold(u, r.Host)
}

// ---------------------------------------------------------------------------
// argon2id
// ---------------------------------------------------------------------------

// derivations bounds how many argon2id derivations run AT ONCE, anywhere in the process.
//
// THE COST THAT MAKES THE HASH GOOD IS ALSO A WEAPON POINTED AT THE BOX. One derivation is 64 MiB
// by design, and the login route hands that cost to anyone who can reach the port: a wrong password
// costs the sender ~100 bytes and the server 64 MiB. Measured against this image, on an ordinary
// install with a password set, sixteen concurrent wrong guesses:
//
//	before   idle 138 MB  ->  1059 MB
//	after    idle 138 MB  ->   295 MB, and 296 MB at SIXTY-FOUR concurrent — flat, because the
//	                           bound is on the work rather than on the number of askers
//
// The per-IP throttle does not bound it, and this is the part worth being plain about: springback
// believes X-Forwarded-For unconditionally, so an attacker who varies that header gets a fresh
// bucket every time. Measured — sixty wrong passwords from sixty forwarded addresses were all
// answered 401, while twenty from one address reached 429. That trade is deliberate and documented
// where the header is read (it bills the right visitor behind a proxy, which is what a public demo
// needs), but it means the throttle counts honest mistakes rather than attacks, and cannot be the
// thing that bounds memory.
//
// So the bound is here, on the resource itself, where no caller can forget it and no header can
// move it. Requests over the limit WAIT rather than being refused: a queued request holds a
// connection and a goroutine, which is kilobytes, and the alternative is refusing logins to a
// household because somebody is knocking. Under attack the honest visitor's sign-in is slow.
//
// Two, not more: the peak is this many times 64 MiB, and springback shares its box with a container
// full of device tooling. Nothing legitimate needs a third — this is one household's password,
// typed by hand.
const maxConcurrentDerivations = 2

var derivations = make(chan struct{}, maxConcurrentDerivations)

func hashPassword(password string, p Params) (string, error) {
	derivations <- struct{}{}
	defer func() { <-derivations }()
	return deriveHash(password, p)
}

func deriveHash(password string, p Params) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLen)
	// The standard PHC string, so the parameters travel with the hash and can be raised later
	// without stranding the passwords already set with the old ones.
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(password, encoded string) (bool, error) {
	// Bounded for the same reason as hashing, and this is the route that matters: an unlimited
	// number of wrong passwords is something a stranger can arrange, whereas setup answers 409
	// without deriving anything at all.
	derivations <- struct{}{}
	defer func() { <-derivations }()
	return deriveAndCompare(password, encoded)
}

func deriveAndCompare(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("password file is not an argon2id hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("unsupported argon2 version")
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return false, errors.New("unreadable argon2 parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, uint32(len(want)))
	// Constant time: a byte-by-byte compare leaks how much of the hash matched, which over
	// enough attempts is a way in.
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// tokenKey is what the session map is keyed by. The raw token is never held in memory as a map
// key, so a heap dump does not hand over live sessions.
func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func writeFileAtomic(dest string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()

	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, dest)
}

// MarshalState is a tiny helper so the handler stays about HTTP.
//
// demoPassword is the PUBLISHED password of a public demo, and empty everywhere else. It is
// omitted rather than sent empty: a client can then treat its presence as the whole question, and
// no ordinary install ever puts a password-shaped key on an unauthenticated endpoint.
func MarshalState(state string, secure, loopback bool, demoPassword string) []byte {
	m := map[string]any{
		"state":    state,
		"username": Username,
		"secure":   secure,
		"loopback": loopback,
	}
	if demoPassword != "" {
		m["demo_password"] = demoPassword
	}
	b, _ := json.Marshal(m)
	return b
}
