package store

// Last-known device facts.
//
// A device that is not answering can only be asked for one thing: nothing. Its name, model, iOS
// version and region all come from lockdown, and lockdown needs the device. So an offline device
// used to render as a bare udid — a 40-character hex string where a name should be, with no
// model and no iOS version, which looks like a fault rather than a phone in someone's pocket.
//
// The facts barely change: a device is renamed once in its life and updated a few times a year.
// Remembering the last answer and showing it for a device that cannot be asked is both accurate
// and far friendlier than the truth-by-omission it replaces.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DeviceFacts is what was last read off a device.
type DeviceFacts struct {
	Name        string    `json:"name,omitempty"`
	ProductType string    `json:"product_type,omitempty"`
	IOS         string    `json:"ios,omitempty"`
	Region      string    `json:"region,omitempty"`
	LastSeen    time.Time `json:"last_seen"`
}

// DeviceCache persists those facts across restarts.
type DeviceCache struct {
	path string
	mu   sync.Mutex
	byID map[string]DeviceFacts
}

func NewDeviceCache(root string) *DeviceCache {
	c := &DeviceCache{
		path: filepath.Join(root, "devices.json"),
		byID: map[string]DeviceFacts{},
	}
	if b, err := os.ReadFile(c.path); err == nil {
		// A corrupt file is not worth failing to start over: the worst case is that offline
		// devices go back to showing their udid until each is next seen awake.
		_ = json.Unmarshal(b, &c.byID)
	}
	return c
}

// Remember records what a reachable device said.
//
// ONLY EVER RECORDS A NON-EMPTY ANSWER. A device that is awake but slow can return an empty
// string for a key, and writing that over a good value would erase the name this exists to keep.
func (c *DeviceCache) Remember(udid string, f DeviceFacts) {
	if udid == "" {
		return
	}
	c.mu.Lock()
	prev := c.byID[udid]
	if f.Name == "" {
		f.Name = prev.Name
	}
	if f.ProductType == "" {
		f.ProductType = prev.ProductType
	}
	if f.IOS == "" {
		f.IOS = prev.IOS
	}
	if f.Region == "" {
		f.Region = prev.Region
	}
	// COMPARED WITH THE TIMESTAMPS ZEROED, so the comparison is over the facts a person can
	// actually see. This runs for every reachable device on a five-second poll; if the moving
	// timestamp counted as a change, the file would be rewritten forty times a minute forever.
	before, after := prev, f
	before.LastSeen, after.LastSeen = time.Time{}, time.Time{}
	worthWriting := before != after

	f.LastSeen = time.Now().UTC()
	c.byID[udid] = f
	var snapshot []byte
	var err error
	if worthWriting {
		snapshot, err = json.MarshalIndent(c.byID, "", "  ")
	}
	c.mu.Unlock()

	if worthWriting && err == nil {
		_ = writeFileAtomic(c.path, snapshot, 0o644)
	}
}

// Recall returns what is known about a device, and whether anything is.
func (c *DeviceCache) Recall(udid string) (DeviceFacts, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.byID[udid]
	return f, ok
}

// Forget drops a device, for when it is unpaired.
func (c *DeviceCache) Forget(udid string) {
	c.mu.Lock()
	delete(c.byID, udid)
	snapshot, err := json.MarshalIndent(c.byID, "", "  ")
	c.mu.Unlock()
	if err == nil {
		_ = writeFileAtomic(c.path, snapshot, 0o644)
	}
}
