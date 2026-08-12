package store

// The device icon cache.
//
// A device holds a couple of hundred apps and each icon is ~35 KB, so a phone opening the app
// list would otherwise pull about 7 MB off an iPhone over wifi, every time. Two things make that
// tolerable: the icons are cached on disk, and they are fetched in ONE batch per device rather
// than one connection per icon.
//
// KEYED BY VERSION, so an app that updates its artwork gets a new icon rather than the old one
// forever. The version travels in from the caller — the same list that named the bundle id knows
// which version is installed — which is the same trick the library icons use with the download
// timestamp, and it beats a timed expiry: there is no window during which the wrong picture is
// served, and no refresh of icons that have not changed.

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/novkostya/springback/core/internal/tools"
)

// ErrNoDeviceIcon means the device has been asked and has no icon for this app. It is a normal
// outcome — the UI draws a lettered tile and stops asking.
var ErrNoDeviceIcon = errors.New("device has no icon for this app")

// DeviceIcons caches home-screen icons under <root>/<udid>/<bundle>@<version>.png.
type DeviceIcons struct {
	Root  string
	Tools tools.Tools

	// mu guards warms. A warm is per DEVICE, not per icon: the first request for any icon
	// fetches every icon that device is missing, and the two hundred requests behind it wait
	// for that one run instead of starting two hundred more.
	mu    sync.Mutex
	warms map[string]*warm
}

type warm struct {
	done chan struct{}
	err  error
}

func NewDeviceIcons(root string, t tools.Tools) *DeviceIcons {
	return &DeviceIcons{Root: root, Tools: t, warms: map[string]*warm{}}
}

// safeName keeps caller-supplied text inside one path segment. Bundle ids and versions come off a
// device, and neither gets to choose where this process writes.
func safeName(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if strings.HasPrefix(out, ".") {
		return "_" + out[1:]
	}
	return out
}

func (d *DeviceIcons) dir(udid string) string { return filepath.Join(d.Root, safeName(udid)) }

func (d *DeviceIcons) path(udid, bundleID, version string) string {
	return filepath.Join(d.dir(udid), safeName(bundleID)+"@"+safeName(version)+".png")
}

// missPath marks an app the device has already been asked about and had no icon for. Without it
// every request for such an app would trigger a fresh warm of the whole device, forever.
func (d *DeviceIcons) missPath(udid, bundleID, version string) string {
	return filepath.Join(d.dir(udid), safeName(bundleID)+"@"+safeName(version)+".none")
}

// genericPath marks a cached icon as the device's generic tile rather than the app's own artwork.
func (d *DeviceIcons) genericPath(udid, bundleID, version string) string {
	return filepath.Join(d.dir(udid), safeName(bundleID)+"@"+safeName(version)+".generic")
}

// IsGeneric reports whether the cached icon is the device's placeholder. The caller uses it to
// decide whether to go looking for the store's artwork first.
func (d *DeviceIcons) IsGeneric(udid, bundleID, version string) bool {
	_, err := os.Stat(d.genericPath(udid, bundleID, version))
	return err == nil
}

// Get returns one icon, warming the whole device on a miss.
func (d *DeviceIcons) Get(ctx context.Context, udid, bundleID, version string) ([]byte, error) {
	if b, err := os.ReadFile(d.path(udid, bundleID, version)); err == nil && len(b) > 0 {
		return b, nil
	}
	if _, err := os.Stat(d.missPath(udid, bundleID, version)); err == nil {
		return nil, ErrNoDeviceIcon
	}

	if err := d.Warm(ctx, udid); err != nil {
		return nil, err
	}

	if b, err := os.ReadFile(d.path(udid, bundleID, version)); err == nil && len(b) > 0 {
		return b, nil
	}
	return nil, ErrNoDeviceIcon
}

// Warm fetches every icon the device has that is not already cached.
//
// Concurrent callers share one run. The map entry is removed when the run finishes, so a later
// request — after an app was installed, say — starts a fresh one rather than being told the
// answer from before.
func (d *DeviceIcons) Warm(ctx context.Context, udid string) error {
	d.mu.Lock()
	if w, ok := d.warms[udid]; ok {
		d.mu.Unlock()
		select {
		case <-w.done:
			return w.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	w := &warm{done: make(chan struct{})}
	d.warms[udid] = w
	d.mu.Unlock()

	w.err = d.fetch(ctx, udid)

	d.mu.Lock()
	delete(d.warms, udid)
	d.mu.Unlock()
	close(w.done)
	return w.err
}

func (d *DeviceIcons) fetch(ctx context.Context, udid string) error {
	apps, err := d.Tools.ListApps(ctx, udid)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d.dir(udid), 0o755); err != nil {
		return err
	}
	// Before working out what is missing, throw out anything already cached that turns out to be
	// the device's generic placeholder rather than an icon.
	d.dropCachedPlaceholders(udid)

	// Only ask for what is missing. A device whose icons are all cached costs one app list and
	// no SpringBoard connection at all.
	var want []string
	versions := make(map[string]string, len(apps))
	for _, a := range apps {
		if a.BundleID == "" {
			continue
		}
		versions[a.BundleID] = a.Version
		if _, err := os.Stat(d.path(udid, a.BundleID, a.Version)); err == nil {
			continue
		}
		if _, err := os.Stat(d.missPath(udid, a.BundleID, a.Version)); err == nil {
			continue
		}
		want = append(want, a.BundleID)
	}
	if len(want) == 0 {
		return nil
	}

	icons, err := d.Tools.DeviceIcons(ctx, udid, want)
	if err != nil {
		return err
	}
	placeholder := placeholderHashes(icons)

	for _, b := range want {
		png, ok := icons[b]
		if !ok || len(png) == 0 {
			_ = writeFileAtomic(d.missPath(udid, b, versions[b]), nil, 0o644)
			continue
		}
		if err := writeFileAtomic(d.path(udid, b, versions[b]), png, 0o644); err != nil {
			return err
		}
		// KEPT, BUT MARKED. A placeholder is still the best picture there is for an app with
		// no store listing — a delisted app has no artwork anywhere else — so it is not thrown
		// away. The marker lets the caller prefer the store's icon when there is one, and fall
		// back to this when there is not.
		if placeholder[hashBytes(png)] {
			_ = writeFileAtomic(d.genericPath(udid, b, versions[b]), nil, 0o644)
		}
		// The previous version's icon is now dead weight. Dropping it here keeps the cache
		// proportional to what is installed rather than to how often it has been updated.
		d.pruneOldVersions(udid, b, versions[b])
	}
	return nil
}

// placeholderHashes finds the icons the device handed back that are not really icons.
//
// SPRINGBOARD ANSWERS FOR AN APP IT HAS NO ARTWORK FOR BY SENDING A GENERIC PICTURE rather than
// by refusing, so springback cached it and drew it — a grey patterned tile that names nothing,
// on a list where every other row is recognisable. Reported with a screenshot.
//
// TWO APPS CANNOT HAVE THE SAME ICON, so an image returned for more than one bundle id in a
// single batch is a placeholder by definition. That is the whole test, and it is deliberately not
// a hard-coded hash: the picture differs between iOS versions and icon sizes, and a list of known
// bad hashes would be wrong on the next device anyone tried. Measured on one iPhone: fourteen apps
// sharing a single 11,598-byte image.
//
// The cost of being wrong is small in one direction and not the other. Two apps that genuinely
// ship identical artwork would both fall back to their lettered tile — which still names the app.
// Letting the placeholder through gives every one of them the same meaningless picture.
//
// It cannot catch a placeholder that arrives alone in a batch, which is the price of having no
// fixed hash to compare against. In practice they arrive in groups.
func placeholderHashes(icons map[string][]byte) map[string]bool {
	owners := make(map[string]int, len(icons))
	for _, png := range icons {
		if len(png) > 0 {
			owners[hashBytes(png)]++
		}
	}
	out := map[string]bool{}
	for h, n := range owners {
		if n > 1 {
			out[h] = true
		}
	}
	return out
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return string(sum[:])
}

// dropCachedPlaceholders applies the same rule to icons cached BEFORE that rule existed.
//
// Without it the fix would only reach devices nobody had scanned yet: a cached icon is never
// re-examined, because the whole point of the cache is not to ask again. Runs once per warm and
// costs a hash of each cached file — a couple of megabytes of small reads.
//
// The file is KEPT and marked, not deleted: it is still the only picture a delisted app has. The
// mark is what lets the caller prefer the store's artwork for everything else.
func (d *DeviceIcons) dropCachedPlaceholders(udid string) {
	entries, err := os.ReadDir(d.dir(udid))
	if err != nil {
		return
	}
	byHash := map[string][]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".png") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(d.dir(udid), name))
		if err != nil || len(b) == 0 {
			continue
		}
		h := hashBytes(b)
		byHash[h] = append(byHash[h], name)
	}
	for _, names := range byHash {
		if len(names) < 2 {
			continue
		}
		for _, name := range names {
			// <bundle>@<version>.png -> the marker beside it.
			stem := strings.TrimSuffix(name, ".png")
			if i := strings.LastIndex(stem, "@"); i > 0 {
				_ = writeFileAtomic(d.genericPath(udid, stem[:i], stem[i+1:]), nil, 0o644)
			}
		}
	}
}

func (d *DeviceIcons) pruneOldVersions(udid, bundleID, keep string) {
	entries, err := os.ReadDir(d.dir(udid))
	if err != nil {
		return
	}
	prefix := safeName(bundleID) + "@"
	current := prefix + safeName(keep)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		// EVERY suffix this cache writes, or pruning deletes the current version's own
		// markers. Adding `.generic` without adding it here cost exactly that: the marker was
		// written and removed again a line later, so nothing was ever known to be a
		// placeholder.
		stem := name
		for _, ext := range []string{".png", ".none", ".generic"} {
			stem = strings.TrimSuffix(stem, ext)
		}
		if stem == current {
			continue
		}
		// A bundle id is a prefix of no other bundle id at this position, because the "@"
		// cannot appear inside one — safeName has already replaced anything that is not a
		// letter, digit, dot, hyphen or underscore.
		_ = os.Remove(filepath.Join(d.dir(udid), name))
	}
}

// Forget drops a device's whole icon cache. Used when a device is no longer paired.
func (d *DeviceIcons) Forget(udid string) error {
	dir := d.dir(udid)
	if !isUnder(d.Root, dir) {
		return errors.New("refusing to remove a path outside the icon cache")
	}
	return os.RemoveAll(dir)
}
