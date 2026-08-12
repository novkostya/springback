package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDeviceCacheRemembersAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	c := NewDeviceCache(dir)
	c.Remember("UDID-1", DeviceFacts{Name: "Anna's iPhone", ProductType: "iPhone17,1", IOS: "26.6", Region: "AE/A"})

	// A second cache over the same directory is what a container restart looks like.
	again := NewDeviceCache(dir)
	got, ok := again.Recall("UDID-1")
	if !ok {
		t.Fatal("nothing recalled after restart")
	}
	if got.Name != "Anna's iPhone" || got.ProductType != "iPhone17,1" || got.IOS != "26.6" || got.Region != "AE/A" {
		t.Errorf("recalled %+v, want the facts that were remembered", got)
	}
	if got.LastSeen.IsZero() {
		t.Error("LastSeen was not stamped")
	}
}

// TestDeviceCacheNeverErasesWithBlanks is the one that protects the whole feature. A device that
// is awake but slow can answer a lockdown read with an empty string, and writing that over a good
// value would delete the name this cache exists to keep — turning a named device back into a
// hex string at random.
func TestDeviceCacheNeverErasesWithBlanks(t *testing.T) {
	c := NewDeviceCache(t.TempDir())
	c.Remember("UDID-1", DeviceFacts{Name: "Anna's iPhone", ProductType: "iPhone17,1", IOS: "26.6", Region: "AE/A"})
	c.Remember("UDID-1", DeviceFacts{}) // every read failed this time round

	got, _ := c.Recall("UDID-1")
	if got.Name != "Anna's iPhone" || got.ProductType != "iPhone17,1" {
		t.Errorf("a blank answer erased the cached facts: %+v", got)
	}

	// A real change still lands.
	c.Remember("UDID-1", DeviceFacts{Name: "Anna's iPhone", ProductType: "iPhone17,1", IOS: "26.7", Region: "AE/A"})
	if got, _ := c.Recall("UDID-1"); got.IOS != "26.7" {
		t.Errorf("an iOS update was not recorded: %+v", got)
	}
}

// TestDeviceCacheDoesNotRewriteUnchanged: this is called for every reachable device on a
// five-second poll. If the moving timestamp counted as a change, the file would be rewritten
// forty times a minute forever.
func TestDeviceCacheDoesNotRewriteUnchanged(t *testing.T) {
	dir := t.TempDir()
	c := NewDeviceCache(dir)
	facts := DeviceFacts{Name: "Anna's iPhone", ProductType: "iPhone17,1", IOS: "26.6", Region: "AE/A"}
	c.Remember("UDID-1", facts)

	path := filepath.Join(dir, "devices.json")
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Make a stale mtime detectable without sleeping.
	old := first.ModTime().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 10; i++ {
		c.Remember("UDID-1", facts)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(old) {
		t.Error("an unchanged Remember rewrote the file")
	}
}

func TestDeviceCacheForget(t *testing.T) {
	c := NewDeviceCache(t.TempDir())
	c.Remember("UDID-1", DeviceFacts{Name: "gone"})
	c.Forget("UDID-1")
	if _, ok := c.Recall("UDID-1"); ok {
		t.Error("Forget left the device behind")
	}
}

func TestDeviceCacheSurvivesACorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "devices.json"), []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Must not panic or fail to construct: the worst acceptable outcome is offline devices
	// showing their udid again until each is next seen awake.
	c := NewDeviceCache(dir)
	if _, ok := c.Recall("anything"); ok {
		t.Error("recalled something from a corrupt file")
	}
	c.Remember("UDID-1", DeviceFacts{Name: "fresh"})
	if got, ok := c.Recall("UDID-1"); !ok || got.Name != "fresh" {
		t.Error("the cache did not recover after a corrupt file")
	}
}
