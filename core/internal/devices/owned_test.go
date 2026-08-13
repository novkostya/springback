package devices

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

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
	fake := tools.NewFake()
	return &Service{
		Tools: fake,
		Seen:  cache,
		// A REAL RESOLVER, because Apps requires one — it asks the store about every app it
		// finds. The Owned tests never reached that path, so the first test to call Rescan
		// panicked on a nil pointer that production never has: main.go always builds one.
		Resolver: storefront.NewResolver(fake, time.Hour, nil),
		Library:  lib,
		Cache:    store.NewDeviceCache(dir),
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

// TestOwnedCarriesWhatTheDetailPageShows: the app page is reached from a device list and from the
// Apps list, and it must be the same page either way.
//
// Reported with two screenshots of one app: from a device it showed the developer, the owning
// Apple ID, the storefront and the installed size; from Apps it showed three rows. The owner is
// the one that cost something rather than merely looking thin — the Archive button downloads with
// the account on the RECEIPT, so without it the page offers an Apple ID that does not own the app
// and the download fails with "license not found".
func TestOwnedCarriesWhatTheDetailPageShows(t *testing.T) {
	s, cache, _ := ownedService(t)
	remember(t, cache, "PHONE", "alina-iphone", []App{{
		InstalledApp: tools.InstalledApp{
			BundleID: "ru.mobile.timob", Name: "TIMOB", Version: "4.9.1",
			AppID: 6469694058, StoreName: "TIMOB: sim, call recorder",
			Artist: "T-MOB, LLC", OwnerAppleID: "novikova.a.o@example.com",
			Storefront: "ru", DiskUsage: 202 << 20,
		},
		Status: storefront.Available, StoreVersion: "4.9.2", StoreSize: 180 << 20,
		StoreUpdated: "2025-04-02T07:00:00Z", Checked: []string{"ru", "us"},
	}})

	owned, err := s.Owned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := owned.Apps[0]

	// Facts about the LISTING live on the app.
	for _, tc := range []struct{ name, got, want string }{
		{"artist", a.Artist, "T-MOB, LLC"},
		{"store version", a.StoreVersion, "4.9.2"},
		{"store updated", a.StoreUpdated, "2025-04-02T07:00:00Z"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if a.StoreSize != 180<<20 || len(a.Checked) != 2 {
		t.Errorf("store size/checked = %d/%v, want the values from the scan", a.StoreSize, a.Checked)
	}

	// Facts about the COPY live on the sighting, because two devices can disagree about all
	// three: bought by different Apple IDs, from different storefronts, occupying different space.
	seen := a.Devices[0]
	if seen.OwnerAppleID != "novikova.a.o@example.com" {
		t.Errorf("owner = %q, want the receipt's — the Archive button defaults to it", seen.OwnerAppleID)
	}
	if seen.Storefront != "ru" || seen.DiskUsage != 202<<20 {
		t.Errorf("sighting = %+v, want the storefront and installed size from the receipt", seen)
	}
}

// TestRescanCountsWhatItCouldNotAsk: the button says what it reached, and the two reasons for not
// reaching a device are different facts with different remedies. A device that is elsewhere needs
// nothing from anyone; an unpaired device is sitting right here and is skipped ON PURPOSE, because
// asking it anything is what raises the Trust prompt. Reporting both as "not here" would be untrue
// of the second and unhelpful about it.
func TestRescanCountsWhatItCouldNotAsk(t *testing.T) {
	s, _, _ := ownedService(t)

	_, res, err := s.Rescan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The fake's fixtures are one reachable paired iPhone, one reachable UNPAIRED iPad, and one
	// that is asleep — which is the point of them.
	if res.Total != 3 {
		t.Fatalf("total = %d, want the three fixture devices", res.Total)
	}
	if res.Scanned != 1 {
		t.Errorf("scanned = %d, want the one device that is here and paired", res.Scanned)
	}
	if res.Away != 1 {
		t.Errorf("away = %d, want the sleeping one", res.Away)
	}
	if res.Unpaired != 1 {
		t.Errorf("unpaired = %d, want the iPad — which is here, and deliberately not asked", res.Unpaired)
	}
}

// TestRescanRemembersWhatItScanned is the point of the button: one tap fills a screen that was
// empty, without visiting each device's page.
func TestRescanRemembersWhatItScanned(t *testing.T) {
	s, cache, _ := ownedService(t)

	if all, _ := cache.All(); len(all) != 0 {
		t.Fatalf("started with %d remembered devices, want none", len(all))
	}
	owned, _, err := s.Rescan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if owned.Total == 0 {
		t.Fatal("rescan returned no apps")
	}
	all, _ := cache.All()
	if len(all) != 1 {
		t.Errorf("remembered %d devices, want the one it scanned", len(all))
	}
}
