package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/novkostya/springback/core/internal/auth"
	"github.com/novkostya/springback/core/internal/store"
	"github.com/novkostya/springback/core/internal/tools"
)

// stubIcons answers for a device with exactly the icons the test names, so a test can say "this
// app got the generic tile" or "this app got nothing" without depending on which apps the fake
// fixture happens to leave blank.
type stubIcons struct {
	tools.Tools
	apps  []tools.InstalledApp
	icons map[string][]byte
}

func (s stubIcons) ListApps(context.Context, string) ([]tools.InstalledApp, error) {
	return s.apps, nil
}

func (s stubIcons) DeviceIcons(context.Context, string, []string) (map[string][]byte, error) {
	return s.icons, nil
}

func iconServer(t *testing.T, tl tools.Tools) (http.Handler, *store.Library, *http.Cookie) {
	t.Helper()
	dir := t.TempDir()
	a, err := auth.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	a.Params = auth.Params{Memory: 8, Iterations: 1, Parallelism: 1, SaltLen: 8, KeyLen: 16}

	lib := store.NewLibrary(filepath.Join(dir, "library"))
	s := &Server{
		Tools:       tl,
		Auth:        a,
		Library:     lib,
		Accounts:    store.NewAccounts(dir),
		DeviceIcons: store.NewDeviceIcons(filepath.Join(dir, "icons"), tl),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h := s.Handler()
	w := do(h, "POST", "/api/auth/setup", `{"password":"a good password"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("setup = %d: %s", w.Code, w.Body)
	}
	return h, lib, sessionCookie(t, w)
}

// archive puts an app in the library with an icon already extracted, which is what a real archive
// looks like after its icon has been asked for once.
func archive(t *testing.T, lib *store.Library, id int64, bundleID string, icon []byte) {
	t.Helper()
	dir := filepath.Join(lib.Root, strconv.FormatInt(id, 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"id":%d,"bundle_id":%q,"name":"Clips","version":"3.1.7"}`, id, bundleID)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lib.IconPath(id), icon, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDeviceIconPrefersTheArchiveToAPlaceholder is the bug it is named after, reported with three
// screenshots of the same app: Clips drew a grey grid on the device's app list, its own artwork on
// its detail page, and its own artwork again in the library. One app, three places, two pictures.
//
// The cause was that the device list asks the DEVICE, and SpringBoard answers for an app it has
// not rendered with a generic tile. The store fallback that was already here cannot help, because
// Clips is delisted and a delisted app has no listing to take artwork from — but it had been
// archived, and the .ipa on this disk has the real icon. That is the source this reaches for.
func TestDeviceIconPrefersTheArchiveToAPlaceholder(t *testing.T) {
	const udid = "UDID"
	tile := []byte("the-generic-tile")
	real := []byte("clips-own-artwork")

	// The same bytes for two apps is what marks them generic: two apps cannot share an icon.
	tl := stubIcons{
		Tools: tools.NewFake(),
		apps: []tools.InstalledApp{
			{BundleID: "com.apple.clips", Version: "3.1.7"},
			{BundleID: "com.other.app", Version: "1.0"},
		},
		icons: map[string][]byte{"com.apple.clips": tile, "com.other.app": tile},
	}
	h, lib, c := iconServer(t, tl)
	archive(t, lib, 1234, "com.apple.clips", real)

	w := do(h, "GET", "/api/devices/"+udid+"/icon.png?bundle=com.apple.clips&v=3.1.7", "", c)
	if w.Code != http.StatusOK {
		t.Fatalf("icon = %d, want 200", w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), real) {
		t.Errorf("served %q, want the archive's icon %q", w.Body.Bytes(), real)
	}

	// NOT IMMUTABLE. Archiving an app is what makes a better picture exist, so a week-long
	// promise about this URL would hide the icon the user just created.
	if cc := w.Header().Get("Cache-Control"); cc != "private, max-age=60, must-revalidate" {
		t.Errorf("Cache-Control = %q, want a revalidating one for a fallback", cc)
	}

	// The app that is NOT archived keeps the tile: it is a poor picture, and it is the only
	// one that exists. Drawing nothing there would be worse.
	w = do(h, "GET", "/api/devices/"+udid+"/icon.png?bundle=com.other.app&v=1.0", "", c)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), tile) {
		t.Errorf("unarchived app: %d %q, want 200 and the device's tile", w.Code, w.Body.Bytes())
	}
}

// TestDeviceIconFallsBackWhenTheDeviceHasNone covers the other half: a device that answers with no
// icon at all used to be a 404 and a lettered tile, even with the app's artwork sitting on disk.
func TestDeviceIconFallsBackWhenTheDeviceHasNone(t *testing.T) {
	real := []byte("clips-own-artwork")
	tl := stubIcons{
		Tools: tools.NewFake(),
		apps:  []tools.InstalledApp{{BundleID: "com.apple.clips", Version: "3.1.7"}},
		icons: map[string][]byte{},
	}
	h, lib, c := iconServer(t, tl)
	archive(t, lib, 1234, "com.apple.clips", real)

	w := do(h, "GET", "/api/devices/UDID/icon.png?bundle=com.apple.clips&v=3.1.7", "", c)
	if w.Code != http.StatusOK {
		t.Fatalf("icon = %d, want 200 from the archive", w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), real) {
		t.Errorf("served %q, want the archive's icon %q", w.Body.Bytes(), real)
	}
}

// TestDeviceIconStaysImmutableWhenItCameFromTheDevice guards the caching that makes a two-hundred
// icon list cheap on a second visit: the fallback's revalidation must not leak onto the normal answer.
func TestDeviceIconStaysImmutableWhenItCameFromTheDevice(t *testing.T) {
	tl := stubIcons{
		Tools: tools.NewFake(),
		apps:  []tools.InstalledApp{{BundleID: "com.apple.clips", Version: "3.1.7"}},
		icons: map[string][]byte{"com.apple.clips": []byte("real-device-icon")},
	}
	h, _, c := iconServer(t, tl)

	w := do(h, "GET", "/api/devices/UDID/icon.png?bundle=com.apple.clips&v=3.1.7", "", c)
	if w.Code != http.StatusOK {
		t.Fatalf("icon = %d, want 200", w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "private, max-age=604800, immutable" {
		t.Errorf("Cache-Control = %q, want immutable for a device's own icon", cc)
	}
}
