package store

// The remembered app list, per device.
//
// springback already remembers a device's NAME when it is away — DeviceCache, and for the same
// reason: an offline iPhone rendered as a bare udid looks like a fault rather than a phone in
// somebody's pocket. It forgot that device's APPS entirely, which is a larger hole than it sounds.
//
// The question this tool exists to answer is "which of the apps I own are already gone". Answering
// it required the hardware to be awake, plugged in and enumerated, one device at a time — so the
// answer was unavailable for exactly the devices most likely to hold something irreplaceable: the
// old iPad in a drawer, the phone that was replaced. Those are not edge cases; they are the ones
// worth asking about.
//
// A remembered list is also the only honest basis for "everything you own". A receipt on a device
// is proof of purchase that survives the listing being pulled, and it stays true while the device
// is elsewhere.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// AppCache stores one file per device under <root>/.device-apps/<udid>.json.
//
// A DOT-DIRECTORY, like the icon cache beside it, because Library.List() keys on directory names
// that parse as a numeric App Store id — so this one is skipped by the same rule that already skips
// devices.json and the status cache.
type AppCache struct {
	root string
	mu   sync.Mutex
}

// SeenApps is one device's last known app list.
//
// Apps is RAW JSON on purpose. The shape belongs to the devices package, which imports this one, so
// naming the type here would be an import cycle — and copying the struct would give the project two
// definitions of an app that drift apart the first time a field is added. This way the cache stores
// what it was handed and the devices package stays the single owner of the shape.
type SeenApps struct {
	UDID       string          `json:"udid"`
	DeviceName string          `json:"device_name,omitempty"`
	SeenAt     time.Time       `json:"seen_at"`
	Apps       json.RawMessage `json:"apps"`
}

func NewAppCache(root string) *AppCache {
	return &AppCache{root: filepath.Join(root, ".device-apps")}
}

func (c *AppCache) path(udid string) string {
	return filepath.Join(c.root, safeName(udid)+".json")
}

// Put records what a device answered. Errors are returned rather than swallowed, but the caller is
// expected to treat a failure as cosmetic: a cache that could not be written must not fail the
// request that was actually asked.
func (c *AppCache) Put(udid, deviceName string, apps json.RawMessage) error {
	if udid == "" || len(apps) == 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := os.MkdirAll(c.root, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(SeenApps{
		UDID: udid, DeviceName: deviceName, SeenAt: time.Now().UTC(), Apps: apps,
	}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(c.path(udid), b, 0o644)
}

// All returns every device's remembered list, newest sighting first.
func (c *AppCache) All() ([]SeenApps, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entries, err := os.ReadDir(c.root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []SeenApps
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c.root, e.Name()))
		if err != nil {
			continue
		}
		var s SeenApps
		if err := json.Unmarshal(b, &s); err != nil || s.UDID == "" {
			// A half-written or hand-edited file is skipped rather than fatal. The
			// remembered list is a convenience; refusing to draw the screen because one
			// device's file is unreadable would trade a small loss for a total one.
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SeenAt.After(out[j].SeenAt) })
	return out, nil
}

// Forget drops one device's remembered apps. Used when a device is unpaired: springback is saying
// it no longer knows that device, and a list of its apps outliving that claim would contradict it.
func (c *AppCache) Forget(udid string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !isUnder(c.root, c.path(udid)) {
		return os.ErrInvalid
	}
	err := os.Remove(c.path(udid))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
