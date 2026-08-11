package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestService uses deliberately cheap argon parameters. The production ones take ~50ms and
// this file derives dozens of times; the algorithm under test is the same either way.
func newTestService(t *testing.T) *Service {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.Params = Params{Memory: 8, Iterations: 1, Parallelism: 1, SaltLen: 8, KeyLen: 16}
	return s
}

func TestSetPasswordThenLogin(t *testing.T) {
	s := newTestService(t)
	if s.IsSetUp() {
		t.Fatal("a fresh install reports as already set up")
	}
	if got := s.State(""); got != StateNeedsSetup {
		t.Errorf("state = %q, want %q", got, StateNeedsSetup)
	}

	if err := s.SetPassword("correct horse"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if got := s.State(""); got != StateNeedsLogin {
		t.Errorf("state after setup = %q, want %q", got, StateNeedsLogin)
	}

	token, err := s.Login("correct horse", "10.0.0.1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := s.Check(token); err != nil {
		t.Errorf("Check on a fresh token: %v", err)
	}
	if got := s.State(token); got != StateAuthenticated {
		t.Errorf("state with a token = %q, want %q", got, StateAuthenticated)
	}
}

// TestSetPasswordIsOneShot: an install left open must not be claimable by whoever finds it.
func TestSetPasswordIsOneShot(t *testing.T) {
	s := newTestService(t)
	if err := s.SetPassword("first password"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword("second password"); !errors.Is(err, ErrAlreadySetUp) {
		t.Fatalf("second SetPassword = %v, want ErrAlreadySetUp", err)
	}
	// And the original still works — the failed attempt must not have replaced anything.
	if _, err := s.Login("first password", "ip"); err != nil {
		t.Errorf("the original password stopped working: %v", err)
	}
}

func TestWrongPasswordAndShortPassword(t *testing.T) {
	s := newTestService(t)
	if err := s.SetPassword("short"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("SetPassword(short) = %v, want ErrWeakPassword", err)
	}
	if err := s.SetPassword("long enough"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login("wrong one", "ip"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("Login(wrong) = %v, want ErrBadCredentials", err)
	}
}

func TestPasswordSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Params = Params{Memory: 8, Iterations: 1, Parallelism: 1, SaltLen: 8, KeyLen: 16}
	if err := s.SetPassword("persisted password"); err != nil {
		t.Fatal(err)
	}

	// A second Service over the same directory is what a container restart looks like.
	again, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !again.IsSetUp() {
		t.Fatal("the password did not survive a restart")
	}
	if _, err := again.Login("persisted password", "ip"); err != nil {
		t.Errorf("login after restart: %v", err)
	}

	// And it is not sitting there in the clear.
	b, err := os.ReadFile(filepath.Join(dir, "password"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "persisted password\n" {
		t.Error("the password file holds the plaintext password")
	}
	info, err := os.Stat(filepath.Join(dir, "password"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("password file mode = %o, want 600", perm)
	}
}

func TestSessionsExpire(t *testing.T) {
	s := newTestService(t)
	now := time.Now()
	s.Now = func() time.Time { return now }
	if err := s.SetPassword("a good password"); err != nil {
		t.Fatal(err)
	}
	token, err := s.Login("a good password", "ip")
	if err != nil {
		t.Fatal(err)
	}

	// Idle: activity keeps it alive...
	now = now.Add(s.IdleTimeout - time.Minute)
	if err := s.Check(token); err != nil {
		t.Fatalf("session died before the idle timeout: %v", err)
	}
	now = now.Add(s.IdleTimeout - time.Minute) // refreshed by the Check above
	if err := s.Check(token); err != nil {
		t.Fatalf("Check did not refresh the idle clock: %v", err)
	}
	// ...but silence kills it.
	now = now.Add(s.IdleTimeout + time.Minute)
	if err := s.Check(token); !errors.Is(err, ErrNoSession) {
		t.Errorf("idle session = %v, want ErrNoSession", err)
	}
}

func TestSessionAbsoluteTimeout(t *testing.T) {
	s := newTestService(t)
	now := time.Now()
	s.Now = func() time.Time { return now }
	if err := s.SetPassword("a good password"); err != nil {
		t.Fatal(err)
	}
	token, _ := s.Login("a good password", "ip")
	start := now

	// Used every twelve hours forever, so the idle clock never runs out — the absolute cap is
	// the only thing that can end it.
	var diedAfter time.Duration
	for i := 0; i < 250; i++ {
		now = now.Add(12 * time.Hour)
		if err := s.Check(token); err != nil {
			if !errors.Is(err, ErrNoSession) {
				t.Fatalf("unexpected error: %v", err)
			}
			diedAfter = now.Sub(start)
			break
		}
	}
	if diedAfter == 0 {
		t.Fatal("a session in constant use never hit the absolute cap")
	}
	if diedAfter <= s.AbsoluteTimeout {
		t.Errorf("session died after %v, before the %v cap", diedAfter, s.AbsoluteTimeout)
	}
	if diedAfter > s.AbsoluteTimeout+24*time.Hour {
		t.Errorf("session outlived the %v cap by %v", s.AbsoluteTimeout, diedAfter-s.AbsoluteTimeout)
	}
}

func TestLogoutInvalidates(t *testing.T) {
	s := newTestService(t)
	if err := s.SetPassword("a good password"); err != nil {
		t.Fatal(err)
	}
	token, _ := s.Login("a good password", "ip")
	s.Logout(token)
	if err := s.Check(token); !errors.Is(err, ErrNoSession) {
		t.Errorf("Check after Logout = %v, want ErrNoSession", err)
	}
}

func TestLoginRateLimited(t *testing.T) {
	s := newTestService(t)
	if err := s.SetPassword("a good password"); err != nil {
		t.Fatal(err)
	}
	var limited bool
	for i := 0; i < 20; i++ {
		_, err := s.Login("wrong", "10.0.0.9")
		if errors.Is(err, ErrRateLimited) {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("twenty wrong passwords from one address were never throttled")
	}
	// A different address is unaffected: the throttle is per-client, not global, or one
	// attacker locks the owner out of their own box.
	if _, err := s.Login("wrong", "10.0.0.10"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("a second address was throttled by the first: %v", err)
	}
}

// TestCookieIsNotSecureOnPlainHTTP pins the decision that makes login work at all on a LAN
// address. A Secure cookie sent over http is dropped by the browser, so the user logs in
// successfully and stays logged out, with nothing in any log to say why.
func TestCookieIsNotSecureOnPlainHTTP(t *testing.T) {
	plain := httptest.NewRequest("POST", "http://192.168.1.10:8971/api/auth/login", nil)
	if c := CookieForToken(plain, "tok", 100); c.Secure {
		t.Error("cookie marked Secure on a plain-http origin; the browser would discard it")
	}

	proxied := httptest.NewRequest("POST", "http://192.168.1.10:8971/api/auth/login", nil)
	proxied.Header.Set("X-Forwarded-Proto", "https")
	if c := CookieForToken(proxied, "tok", 100); !c.Secure {
		t.Error("cookie not marked Secure behind a TLS-terminating proxy")
	}

	for _, c := range []*http.Cookie{CookieForToken(plain, "tok", 100)} {
		if !c.HttpOnly {
			t.Error("session cookie is readable from JavaScript")
		}
	}
}

func TestSecureOriginAndLoopback(t *testing.T) {
	cases := []struct {
		name   string
		xfp    string
		host   string
		secure bool
		loop   bool
	}{
		{"plain lan", "", "192.168.1.10:8971", false, false},
		{"proxied", "https", "springback.example:443", true, false},
		{"proxied chain", "https, http", "springback.example", true, false},
		{"localhost", "", "localhost:8971", false, true},
		{"127.0.0.1", "", "127.0.0.1:8971", false, true},
		{"ipv6 loopback", "", "[::1]:8971", false, true},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "http://x/", nil)
		r.Host = c.host
		if c.xfp != "" {
			r.Header.Set("X-Forwarded-Proto", c.xfp)
		}
		if got := SecureOrigin(r); got != c.secure {
			t.Errorf("%s: SecureOrigin = %v, want %v", c.name, got, c.secure)
		}
		if got := IsLoopback(c.host); got != c.loop {
			t.Errorf("%s: IsLoopback(%q) = %v, want %v", c.name, c.host, got, c.loop)
		}
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		origin string
		host   string
		want   bool
	}{
		{"", "springback.example", true}, // curl and friends
		{"http://springback.example", "springback.example", true},
		{"https://springback.example", "springback.example", true}, // TLS-terminating proxy
		{"http://evil.example", "springback.example", false},
		{"https://springback.example.evil.com", "springback.example", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "http://x/api/library", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := SameOrigin(r); got != c.want {
			t.Errorf("Origin %q Host %q: SameOrigin = %v, want %v", c.origin, c.host, got, c.want)
		}
	}
}
