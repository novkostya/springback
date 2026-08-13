package devices

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/novkostya/springback/core/internal/store"
	"github.com/novkostya/springback/core/internal/storefront"
	"github.com/novkostya/springback/core/internal/tools"
)

// remember writes a device's app list the way a successful scan does.
func remember(t *testing.T, c *store.AppCache, udid, name string, apps []App) {
	t.Helper()
	b, err := json.Marshal(apps)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Put(udid, name, b); err != nil {
		t.Fatal(err)
	}
}

func ownedService(t *testing.T) (*Service, *store.AppCache, *store.Library) {
	t.Helper()
	dir := t.TempDir()
	cache := store.NewAppCache(dir)
	lib := store.NewLibrary(dir + "/library")
	return &Service{
		Tools:   tools.NewFake(),
		Seen:    cache,
		Library: lib,
		Cache:   store.NewDeviceCache(dir),
	}, cache, lib
}

// TestOwnedAnswersWithoutTheDevice is the whole point of remembering. Before this, "which of my
// apps are gone" could only be asked of hardware that was awake and in the room — so it could not
// be asked about the old iPad in a drawer, which is where the irreplaceable things are.
func TestOwnedAnswersWithoutTheDevice(t *testing.T) {
	s, cache, _ := ownedService(t)
	remember(t, cache, "IPAD-IN-A-DRAWER", "Old iPad", []App{{
		InstalledApp: tools.InstalledApp{BundleID: "com.burbn.boomerang", Name: "Boomerang", Version: "1.8"},
		Status:       storefront.Delisted, AppID: 6744684419,
	}})

	owned, err := s.Owned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(owned.Apps) != 1 {
		t.Fatalf("apps = %d, want the one remembered from a device that is not here", len(owned.Apps))
	}
	a := owned.Apps[0]
	if a.BundleID != "com.burbn.boomerang" || a.AppID != 6744684419 {
		t.Errorf("app = %+v, want the receipt's bundle id and numeric id", a)
	}
	// THE ID IS THE PAYLOAD. It comes from the receipt, and it is the only thing in the world
	// that still names a delisted app — without it the archive flow has to ask a human to type
	// a number they have no way to look up.
	if len(a.Devices) != 1 || a.Devices[0].Here {
		t.Errorf("sighting = %+v, want exactly one, marked as not here", a.Devices)
	}
	if a.Devices[0].DeviceName != "Old iPad" {
		t.Errorf("device name = %q, want the remembered one", a.Devices[0].DeviceName)
	}
}

// TestOwnedUnionsDevices: the same app on two devices is ONE row with two sightings, not two rows.
// A list that repeated an app per device would be a list of installations, which is not what
// somebody asking "do I still own this" wants to read.
func TestOwnedUnionsDevices(t *testing.T) {
	s, cache, _ := ownedService(t)
	shared := App{
		InstalledApp: tools.InstalledApp{BundleID: "com.google.ios.youtube", Name: "YouTube", Version: "21.1"},
		Status:       storefront.Available, AppID: 544007664,
	}
	remember(t, cache, "PHONE", "iPhone", []App{shared})
	remember(t, cache, "TABLET", "iPad", []App{shared, {
		InstalledApp: tools.InstalledApp{BundleID: "com.only.tablet", Name: "Tablet Only"},
		Status:       storefront.Available,
	}})

	owned, err := s.Owned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if owned.Total != 2 {
		t.Fatalf("total = %d, want 2 distinct apps", owned.Total)
	}
	if owned.DevicesSeen != 2 {
		t.Errorf("devices_seen = %d, want 2", owned.DevicesSeen)
	}
	for _, a := range owned.Apps {
		if a.BundleID == "com.google.ios.youtube" && len(a.Devices) != 2 {
			t.Errorf("youtube sightings = %d, want one per device", len(a.Devices))
		}
	}
}

// TestOwnedCountsTheLibraryLive: archiving is the one action that changes this answer, so it is
// recomputed rather than read from what was remembered. A screen that told somebody their archive
// had not worked would be worse than one that never mentioned the library at all.
func TestOwnedCountsTheLibraryLive(t *testing.T) {
	s, cache, lib := ownedService(t)
	remember(t, cache, "PHONE", "iPhone", []App{{
		InstalledApp: tools.InstalledApp{BundleID: "com.burbn.boomerang", Name: "Boomerang"},
		Status:       storefront.Delisted, AppID: 6744684419,
		InLibrary: false, // as it was when the device was scanned
	}})
	// Archived since.
	writeLibraryItem(t, lib, 6744684419, "com.burbn.boomerang", "Boomerang from Instagram")

	owned, err := s.Owned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(owned.Apps) != 1 {
		t.Fatalf("apps = %d, want 1", len(owned.Apps))
	}
	if !owned.Apps[0].InLibrary || owned.Apps[0].LibraryID != 6744684419 {
		t.Errorf("app = %+v, want it reported as archived", owned.Apps[0])
	}
	if owned.InLibrary != 1 {
		t.Errorf("in_library count = %d, want 1", owned.InLibrary)
	}
}

// TestOwnedIncludesArchiveOnlyApps covers the state this tool is trying to reach: Apple pulled the
// app AND it is on no device any more, so the copy on this box is the only one left in the world.
// It would be a strange list that left that one out.
func TestOwnedIncludesArchiveOnlyApps(t *testing.T) {
	s, _, lib := ownedService(t)
	writeLibraryItem(t, lib, 6744684419, "com.burbn.boomerang", "Boomerang from Instagram")

	owned, err := s.Owned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(owned.Apps) != 1 {
		t.Fatalf("apps = %d, want the archive-only app", len(owned.Apps))
	}
	a := owned.Apps[0]
	if !a.Archived || len(a.Devices) != 0 {
		t.Errorf("app = %+v, want archived_only with no sightings", a)
	}
	if a.Name != "Boomerang from Instagram" {
		t.Errorf("name = %q, want the archive's own name", a.Name)
	}
	// AND IT MAKES NO CLAIM IT HAS NOT CHECKED. Nothing in this path looked the app up, and the
	// first version assumed DELISTED — which on a real library of five archived apps, every one
	// still on sale, would have put a red chip on all five. The chip is only worth anything if
	// it is never guessed.
	if a.Status != "" {
		t.Errorf("store_status = %q, want no verdict when none was reached", a.Status)
	}
	if owned.Delisted != 0 {
		t.Errorf("delisted count = %d, want 0 — nothing was checked", owned.Delisted)
	}
}

// TestOwnedPutsDelistedFirst: the screen exists for the apps that are gone, and a reader scrolling
// a hundred rows should not have to hunt for them.
func TestOwnedPutsDelistedFirst(t *testing.T) {
	s, cache, _ := ownedService(t)
	remember(t, cache, "PHONE", "iPhone", []App{
		{InstalledApp: tools.InstalledApp{BundleID: "a.available", Name: "Available"}, Status: storefront.Available},
		{InstalledApp: tools.InstalledApp{BundleID: "z.delisted", Name: "Zebra"}, Status: storefront.Delisted},
	})

	owned, err := s.Owned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if owned.Apps[0].Status != storefront.Delisted {
		t.Errorf("first row is %q/%s, want the delisted one despite the name order",
			owned.Apps[0].Name, owned.Apps[0].Status)
	}
	if owned.Delisted != 1 {
		t.Errorf("delisted count = %d, want 1", owned.Delisted)
	}
}

// writeLibraryItem puts an item in the library the way a finished download does — meta.json is
// what List() and BundleIDs() read, and it is the only part these tests need.
func writeLibraryItem(t *testing.T, lib *store.Library, id int64, bundleID, name string) {
	t.Helper()
	dir := filepath.Join(lib.Root, strconv.FormatInt(id, 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"id":%d,"bundle_id":%q,"name":%q,"version":"1.0"}`, id, bundleID, name)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
}
