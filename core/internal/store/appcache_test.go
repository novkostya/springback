package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAppCacheRemembersAndForgets(t *testing.T) {
	c := NewAppCache(t.TempDir())

	if err := c.Put("UDID-1", "iPhone", json.RawMessage(`[{"bundle_id":"a"}]`)); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("UDID-2", "iPad", json.RawMessage(`[{"bundle_id":"b"}]`)); err != nil {
		t.Fatal(err)
	}

	all, err := c.All()
	if err != nil || len(all) != 2 {
		t.Fatalf("All() = %d entries (%v), want 2", len(all), err)
	}
	for _, s := range all {
		if s.SeenAt.IsZero() {
			t.Errorf("%s has no sighting time — the screen reports how old its evidence is", s.UDID)
		}
	}

	// FORGETTING HAS TO MEAN IT. This list names every app on somebody's phone, and it is
	// dropped when a device is unpaired — a claim not to know a device, with its app list still
	// on disk, would be a false one.
	if err := c.Forget("UDID-1"); err != nil {
		t.Fatal(err)
	}
	all, _ = c.All()
	if len(all) != 1 || all[0].UDID != "UDID-2" {
		t.Errorf("after Forget: %+v, want only UDID-2", all)
	}
	// Forgetting what is already gone is not an error: unpairing twice is not a fault.
	if err := c.Forget("UDID-1"); err != nil {
		t.Errorf("second Forget: %v", err)
	}
}

// TestAppCaseSurvivesAJunkFile: one unreadable file must cost one device's list, not the screen.
func TestAppCacheSurvivesAJunkFile(t *testing.T) {
	dir := t.TempDir()
	c := NewAppCache(dir)
	if err := c.Put("GOOD", "iPhone", json.RawMessage(`[{"bundle_id":"a"}]`)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".device-apps", "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	all, err := c.All()
	if err != nil {
		t.Fatalf("All() failed because of one bad file: %v", err)
	}
	if len(all) != 1 || all[0].UDID != "GOOD" {
		t.Errorf("All() = %+v, want the readable entry only", all)
	}
}

// TestAppCacheIgnoresEmptyWrites: a device that answered with nothing must not erase what is
// remembered about it. An empty answer is usually a scan that failed, not a phone with no apps.
func TestAppCacheIgnoresEmptyWrites(t *testing.T) {
	c := NewAppCache(t.TempDir())
	if err := c.Put("UDID", "iPhone", json.RawMessage(`[{"bundle_id":"a"}]`)); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("UDID", "iPhone", nil); err != nil {
		t.Fatal(err)
	}
	all, _ := c.All()
	if len(all) != 1 {
		t.Fatalf("All() = %d, want the earlier list kept", len(all))
	}
	// Compared as JSON rather than as bytes: Put stores the payload inside a document it
	// indents, so the exact spacing is not the cache's promise — the content is.
	var got []map[string]string
	if err := json.Unmarshal(all[0].Apps, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["bundle_id"] != "a" {
		t.Errorf("apps = %v, want the earlier list rather than an empty one", got)
	}
}
