package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/novkostya/springback/core/internal/tools"
)

// countingTools wraps the fake to count the calls that reach a device, which is the whole point
// of the cache: the second view of a device's app list must not touch it at all.
type countingTools struct {
	tools.Tools
	mu     sync.Mutex
	icons  int
	bundle int
}

func (c *countingTools) DeviceIcons(ctx context.Context, udid string, bundleIDs []string) (map[string][]byte, error) {
	c.mu.Lock()
	c.icons++
	c.bundle += len(bundleIDs)
	c.mu.Unlock()
	return c.Tools.DeviceIcons(ctx, udid, bundleIDs)
}

func (c *countingTools) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.icons, c.bundle
}

func newTestIcons(t *testing.T) (*DeviceIcons, *countingTools, string) {
	t.Helper()
	ct := &countingTools{Tools: tools.NewFake()}
	root := t.TempDir()
	d := NewDeviceIcons(root, ct)

	udids, err := ct.ListDeviceUDIDs(context.Background())
	if err != nil || len(udids) == 0 {
		t.Fatalf("fake has no reachable device: %v", err)
	}
	// THE DEVICE WITH THE MOST APPS, not simply the first. The fake withholds an icon from
	// every fourth app, so a device with two of them has no missing-icon case in it at all —
	// and `udids[0]` is whichever one Go's map iteration happened to yield, which made these
	// tests pass or fail at random.
	best, most := "", -1
	for _, u := range udids {
		apps, err := ct.ListApps(context.Background(), u)
		if err != nil {
			continue
		}
		if len(apps) > most {
			best, most = u, len(apps)
		}
	}
	if most < 4 {
		t.Fatalf("no fake device has enough apps to cover both icon cases (most = %d)", most)
	}
	return d, ct, best
}

// appsOf returns the fake device's apps, split into the ones the fake gives an icon for and the
// ones it does not — the fixture withholds every fourth on purpose.
func appsOf(t *testing.T, d *DeviceIcons, udid string) (with, without tools.InstalledApp) {
	t.Helper()
	apps, err := d.Tools.ListApps(context.Background(), udid)
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	icons, err := d.Tools.DeviceIcons(context.Background(), udid, bundleIDsOf(apps))
	if err != nil {
		t.Fatalf("DeviceIcons: %v", err)
	}
	for _, a := range apps {
		if _, ok := icons[a.BundleID]; ok && with.BundleID == "" {
			with = a
		}
		if _, ok := icons[a.BundleID]; !ok && without.BundleID == "" {
			without = a
		}
	}
	if with.BundleID == "" || without.BundleID == "" {
		t.Fatal("fixture must contain both an app with an icon and one without")
	}
	return with, without
}

func bundleIDsOf(apps []tools.InstalledApp) []string {
	out := make([]string, 0, len(apps))
	for _, a := range apps {
		out = append(out, a.BundleID)
	}
	return out
}

func TestDeviceIconsCachesAfterOneWarm(t *testing.T) {
	d, ct, udid := newTestIcons(t)
	// The helper above already called DeviceIcons directly to work out the fixture; count from
	// here so only the cache's own calls are measured.
	ct.mu.Lock()
	ct.icons, ct.bundle = 0, 0
	ct.mu.Unlock()
	app, _ := appsOf(t, d, udid)
	ct.mu.Lock()
	ct.icons, ct.bundle = 0, 0
	ct.mu.Unlock()

	first, err := d.Get(context.Background(), udid, app.BundleID, app.Version)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("first Get returned no bytes")
	}
	calls, _ := ct.counts()
	if calls != 1 {
		t.Errorf("first Get made %d device calls, want 1", calls)
	}

	// Every subsequent icon on that device — not just the one asked for — is now on disk.
	for i := 0; i < 5; i++ {
		if _, err := d.Get(context.Background(), udid, app.BundleID, app.Version); err != nil {
			t.Fatalf("cached Get: %v", err)
		}
	}
	if calls, _ := ct.counts(); calls != 1 {
		t.Errorf("cached Gets made %d device calls in total, want 1", calls)
	}
}

// TestDeviceIconsRemembersAppsWithNoIcon is the one that stops a permanent miss from re-warming
// the whole device on every request for it, forever.
func TestDeviceIconsRemembersAppsWithNoIcon(t *testing.T) {
	d, ct, udid := newTestIcons(t)
	_, missing := appsOf(t, d, udid)
	ct.mu.Lock()
	ct.icons, ct.bundle = 0, 0
	ct.mu.Unlock()

	for i := 0; i < 3; i++ {
		_, err := d.Get(context.Background(), udid, missing.BundleID, missing.Version)
		if !errors.Is(err, ErrNoDeviceIcon) {
			t.Fatalf("Get for an app with no icon = %v, want ErrNoDeviceIcon", err)
		}
	}
	if calls, _ := ct.counts(); calls != 1 {
		t.Errorf("three Gets for an iconless app made %d device calls, want 1", calls)
	}
}

// TestDeviceIconsAreKeyedByVersion is what makes an app that changes its artwork show the new
// one. A cache keyed on the bundle id alone would serve the old picture indefinitely.
func TestDeviceIconsAreKeyedByVersion(t *testing.T) {
	d, _, udid := newTestIcons(t)
	app, _ := appsOf(t, d, udid)

	if _, err := d.Get(context.Background(), udid, app.BundleID, app.Version); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := os.Stat(d.path(udid, app.BundleID, app.Version)); err != nil {
		t.Fatalf("icon not cached under its version: %v", err)
	}

	// A version the device does not report is a miss, not a silent fallback to whatever is
	// on disk.
	if _, err := d.Get(context.Background(), udid, app.BundleID, "99.99"); !errors.Is(err, ErrNoDeviceIcon) {
		t.Errorf("Get with an unknown version = %v, want ErrNoDeviceIcon", err)
	}
}

func TestDeviceIconsPruneOldVersions(t *testing.T) {
	d, _, udid := newTestIcons(t)
	app, _ := appsOf(t, d, udid)

	if _, err := d.Get(context.Background(), udid, app.BundleID, app.Version); err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Plant a previous version's icon, as an update would have left behind.
	stale := d.path(udid, app.BundleID, "0.0.1")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// And a different app's icon, which must survive: the prune keys on the bundle id.
	other := d.path(udid, "com.other.app", "1.0")
	if err := os.WriteFile(other, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	d.pruneOldVersions(udid, app.BundleID, app.Version)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the previous version's icon was not pruned")
	}
	if _, err := os.Stat(d.path(udid, app.BundleID, app.Version)); err != nil {
		t.Errorf("the current icon was pruned: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another app's icon was pruned: %v", err)
	}
}

// TestDeviceIconsPathsStayInsideTheCache. Bundle ids and versions are read off a device, and a
// device does not get to choose where this process writes.
func TestDeviceIconsPathsStayInsideTheCache(t *testing.T) {
	d, _, udid := newTestIcons(t)
	// The property is that the name becomes ONE path component that is not "..", not that the
	// dots vanish: `_._.._etc_passwd@1.0.png` is a perfectly ordinary filename.
	check := func(what, bundleID, version string) {
		t.Helper()
		p := d.path(udid, bundleID, version)
		if !strings.HasPrefix(filepath.Clean(p), filepath.Clean(d.Root)+string(filepath.Separator)) {
			t.Errorf("%s %q escaped the cache root: %s", what, bundleID+version, p)
			return
		}
		if got, want := filepath.Dir(p), d.dir(udid); got != want {
			t.Errorf("%s %q landed outside the device directory: %s (want %s)", what, bundleID+version, got, want)
		}
		if base := filepath.Base(p); base == ".." || base == "." {
			t.Errorf("%s %q produced a traversal component: %s", what, bundleID+version, base)
		}
	}

	for _, evil := range []string{"../../etc/passwd", "..", "/absolute/path", ".hidden", "a/b"} {
		check("bundle id", evil, "1.0")
	}
	// The same for a version, which is equally device-supplied.
	for _, evil := range []string{"../../../etc", "..", "/tmp"} {
		check("version", "com.example.app", evil)
	}
}

// TestDeviceIconsWarmIsShared: two hundred rows appear at once, and they must not become two
// hundred SpringBoard connections.
func TestDeviceIconsWarmIsShared(t *testing.T) {
	d, ct, udid := newTestIcons(t)
	apps, err := d.Tools.ListApps(context.Background(), udid)
	if err != nil {
		t.Fatal(err)
	}
	ct.mu.Lock()
	ct.icons, ct.bundle = 0, 0
	ct.mu.Unlock()

	var wg sync.WaitGroup
	for _, a := range apps {
		wg.Add(1)
		go func(a tools.InstalledApp) {
			defer wg.Done()
			_, _ = d.Get(context.Background(), udid, a.BundleID, a.Version)
		}(a)
	}
	wg.Wait()

	calls, _ := ct.counts()
	if calls == 0 || calls > 2 {
		// One warm normally; two is tolerated because a request that arrives in the instant
		// after a warm finishes legitimately starts the next one.
		t.Errorf("%d concurrent Gets made %d device calls, want 1 (2 at worst)", len(apps), calls)
	}
}

// TestDeviceReturnsTheSamePictureForSeveralApps is the reported bug: a grey patterned tile on a
// handful of rows, identical on each, naming nothing.
//
// SpringBoard answers for an app it has no artwork for by sending a generic picture rather than by
// refusing, so springback cached it and drew it. Two apps cannot have the same icon, so an image
// returned for more than one app in a batch is a placeholder — and the right thing to show is the
// lettered tile, which at least says which app it is.
func TestDeviceReturnsTheSamePictureForSeveralApps(t *testing.T) {
	placeholder := []byte("PNG-the-device-sends-when-it-has-nothing")
	fake := &iconTools{
		apps: []tools.InstalledApp{
			{BundleID: "com.real.one", Version: "1"},
			{BundleID: "com.blank.a", Version: "1"},
			{BundleID: "com.blank.b", Version: "1"},
			{BundleID: "com.blank.c", Version: "1"},
		},
		icons: map[string][]byte{
			"com.real.one": []byte("a genuine icon"),
			"com.blank.a":  placeholder,
			"com.blank.b":  placeholder,
			"com.blank.c":  placeholder,
		},
	}
	d := NewDeviceIcons(t.TempDir(), fake)
	if err := d.Warm(context.Background(), "UDID"); err != nil {
		t.Fatal(err)
	}

	if got, err := d.Get(context.Background(), "UDID", "com.real.one", "1"); err != nil {
		t.Errorf("the one real icon was thrown away too: %v", err)
	} else if string(got) != "a genuine icon" {
		t.Errorf("got %q", got)
	}
	for _, b := range []string{"com.blank.a", "com.blank.b", "com.blank.c"} {
		if _, err := d.Get(context.Background(), "UDID", b, "1"); !errors.Is(err, ErrNoDeviceIcon) {
			t.Errorf("%s served the placeholder: %v", b, err)
		}
	}
}

// TestPlaceholdersAlreadyOnDiskAreDropped: the cache is never re-examined once written, so without
// this the fix would only ever reach devices nobody had scanned before it shipped.
func TestPlaceholdersAlreadyOnDiskAreDropped(t *testing.T) {
	root := t.TempDir()
	fake := &iconTools{
		apps: []tools.InstalledApp{
			{BundleID: "com.blank.a", Version: "1"},
			{BundleID: "com.blank.b", Version: "1"},
		},
		icons: map[string][]byte{},
	}
	d := NewDeviceIcons(root, fake)

	// Two apps cached with the same picture, as an older springback would have left them.
	if err := os.MkdirAll(d.dir("UDID"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"com.blank.a", "com.blank.b"} {
		if err := os.WriteFile(d.path("UDID", b, "1"), []byte("the same picture"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := d.Warm(context.Background(), "UDID"); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"com.blank.a", "com.blank.b"} {
		if _, err := d.Get(context.Background(), "UDID", b, "1"); !errors.Is(err, ErrNoDeviceIcon) {
			t.Errorf("%s still serves the cached placeholder: %v", b, err)
		}
	}
}

// iconTools serves an exact app list and an exact set of icons, so a test can say precisely what
// the device hands back — including handing the same picture to several apps.
type iconTools struct {
	tools.Tools
	apps  []tools.InstalledApp
	icons map[string][]byte
}

func (f *iconTools) ListApps(context.Context, string) ([]tools.InstalledApp, error) {
	return f.apps, nil
}

func (f *iconTools) DeviceIcons(_ context.Context, _ string, bundleIDs []string) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, b := range bundleIDs {
		if png, ok := f.icons[b]; ok {
			out[b] = png
		}
	}
	return out, nil
}
