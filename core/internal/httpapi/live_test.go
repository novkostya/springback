package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/novkostya/springback/core/internal/auth"
	"github.com/novkostya/springback/core/internal/devices"
	"github.com/novkostya/springback/core/internal/jobs"
	"github.com/novkostya/springback/core/internal/live"
	"github.com/novkostya/springback/core/internal/store"
	"github.com/novkostya/springback/core/internal/tools"
)

// stubTools is a device list that the test can change under a running server.
//
// The embedded nil interface supplies the twenty methods this does not implement: any one of them
// panics if it is ever called, which is the behaviour to want here — a test that silently
// exercised a different code path than the one it names is worse than one that crashes.
type stubTools struct {
	tools.Tools
	mu    sync.Mutex
	udids []string
}

func (s *stubTools) set(udids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.udids = udids
}

func (s *stubTools) ListDeviceUDIDs(context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.udids...), nil
}

func (s *stubTools) PairedUDIDs(context.Context) ([]string, error) {
	return s.ListDeviceUDIDs(context.Background())
}

// PairingKnown true, and PairedUDIDs returns the same set as ListDeviceUDIDs, so every stub
// device counts as paired — otherwise List would refuse to read its name, which is what these
// tests watch for a change in.
func (s *stubTools) PairingKnown() bool { return true }

func (s *stubTools) DeviceValue(_ context.Context, udid, key string) (string, error) {
	if key == "DeviceName" {
		return "phone-" + udid, nil
	}
	return "", nil
}

// liveServer returns a running server, a signed-in cookie and the stub whose devices can be
// changed. A real http.Server is unavoidable: httptest.NewRecorder cannot be hijacked, and a
// WebSocket is nothing but a hijacked connection.
func liveServer(t *testing.T) (*Server, *httptest.Server, *stubTools, string) {
	t.Helper()
	dir := t.TempDir()
	a, err := auth.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	a.Params = auth.Params{Memory: 8, Iterations: 1, Parallelism: 1, SaltLen: 8, KeyLen: 16}

	st := &stubTools{udids: []string{"AAA"}}
	s := &Server{
		Tools:    st,
		Auth:     a,
		Devices:  &devices.Service{Tools: st},
		Library:  store.NewLibrary(dir),
		Accounts: store.NewAccounts(dir),
		Jobs:     jobs.NewRegistry(),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h := s.Handler()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	w := do(h, "POST", "/api/auth/setup", `{"password":"correct horse battery"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("setup = %d", w.Code)
	}
	return s, srv, st, sessionCookie(t, w).Value
}

func dial(t *testing.T, srv *httptest.Server, token, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	h := http.Header{}
	if token != "" {
		h.Set("Cookie", auth.CookieName+"="+token)
	}
	if origin == "" {
		origin = "http://" + strings.TrimPrefix(srv.URL, "http://")
	}
	if origin != "-" {
		h.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/ws",
		&websocket.DialOptions{HTTPHeader: h})
}

func readFrame(t *testing.T, conn *websocket.Conn, within time.Duration) (live.Envelope, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		return live.Envelope{}, false
	}
	var env live.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("frame is not an envelope: %v (%s)", err, data)
	}
	return env, true
}

// TestSocketPushesDeviceChanges is the whole feature in one test: connect, change what the muxer
// reports, and be told — with no request from the client at any point.
func TestSocketPushesDeviceChanges(t *testing.T) {
	s, srv, st, token := liveServer(t)

	conn, _, err := dial(t, srv, token, "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	env, ok := readFrame(t, conn, 5*time.Second)
	if !ok || env.Type != live.TypeHello {
		t.Fatalf("first frame = %+v, want hello", env)
	}

	// A second phone turns up. The watcher finds it on the next scan; the kick is what a
	// pairing or a Refresh does, and is here so the test does not wait five seconds for a tick.
	st.set("AAA", "BBB")
	s.Kick()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		env, ok := readFrame(t, conn, 5*time.Second)
		if !ok {
			break
		}
		if env.Type != live.TypeDevices {
			continue // the jobs snapshot, which arrives on connect
		}
		b, _ := json.Marshal(env.Data)
		if strings.Contains(string(b), "BBB") {
			return // told about a device nobody asked about. That is the feature.
		}
	}
	t.Fatal("no devices frame naming the new device")
}

// TestSocketSaysNothingWhenNothingChanges guards the property the device page depends on: a frame
// rebuilds the screen, and the screen holds a search box. A watcher that published every scan
// would take the keyboard away from anyone typing, every five seconds.
func TestSocketSaysNothingWhenNothingChanges(t *testing.T) {
	s, srv, _, token := liveServer(t)

	conn, _, err := dial(t, srv, token, "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	// Read past the connection snapshot to the first devices frame, which IS a change: from
	// nothing known to a device list.
	seen := false
	for range 5 {
		env, ok := readFrame(t, conn, 5*time.Second)
		if !ok {
			break
		}
		if env.Type == live.TypeDevices {
			seen = true
			break
		}
	}
	if !seen {
		t.Fatal("the first scan was never published")
	}

	// Now scan repeatedly with the same devices in place. Nothing may be said.
	for range 3 {
		s.Kick()
	}
	if env, ok := readFrame(t, conn, 2*time.Second); ok {
		t.Fatalf("unchanged devices still published a %q frame", env.Type)
	}
}

// TestSocketRefusesForeignOrigin: a WebSocket handshake is not subject to CORS, so any page
// anywhere can attempt one against this port and the browser will attach the cookies. SameSite
// stops it too; this is the lock that does not depend on the browser's.
func TestSocketRefusesForeignOrigin(t *testing.T) {
	_, srv, _, token := liveServer(t)

	for _, origin := range []string{"http://evil.example", "-"} {
		conn, res, err := dial(t, srv, token, origin)
		if err == nil {
			_ = conn.CloseNow()
			t.Fatalf("origin %q was accepted", origin)
		}
		if res != nil && res.StatusCode != http.StatusForbidden {
			t.Errorf("origin %q = %d, want 403", origin, res.StatusCode)
		}
	}
}

// TestSocketNeedsASession: the guard runs before the upgrade, which is the only moment a
// WebSocket can be refused in a way the browser can see.
func TestSocketNeedsASession(t *testing.T) {
	_, srv, _, _ := liveServer(t)

	conn, res, err := dial(t, srv, "", "")
	if err == nil {
		_ = conn.CloseNow()
		t.Fatal("connected with no session")
	}
	if res != nil && res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.StatusCode)
	}
}

// TestWatcherIdlesWithNobodyListening: a springback in a container with no page open must not be
// asking a phone for its name every five seconds. The watcher is started by the first subscriber
// and skips its own tick while nobody is there.
func TestWatcherIdlesWithNobodyListening(t *testing.T) {
	s, _, _, _ := liveServer(t)
	if s.events.Listeners() != 0 {
		t.Fatalf("listeners = %d before anyone connected", s.events.Listeners())
	}
	// Nothing has been scanned, because nothing has asked.
	s.mu.Lock()
	last := s.lastDevices
	s.mu.Unlock()
	if last != "" {
		t.Errorf("devices were scanned with no listener: %s", last)
	}
}
