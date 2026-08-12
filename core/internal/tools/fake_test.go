package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/novkostya/springback/core/internal/ipa"
)

// downloadFixture archives one app through the fake, exactly as the Archive button does.
func downloadFixture(t *testing.T, appID int64) string {
	t.Helper()
	f := NewFake()
	home := t.TempDir()
	if err := f.AuthLogin(context.Background(), home, "pp", "demo@example.com", "hunter2", ""); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "app.ipa")
	if _, err := f.Download(context.Background(), home, "pp", appID, out, nil); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestFakeArchivesADelistedAppUnderItsRealIdentity is the fixture bug the public demo exposed.
//
// The fake resolved a numeric id through its STOREFRONT map, which the delisted fixtures are
// deliberately absent from — being in no storefront is the whole point of them. So archiving
// Boomerang, the app springback exists for, produced `com.example.app6744684419`: a bundle id
// matching nothing installed on any fixture device, which left the demo unable to show the one
// row its argument rests on, delisted and in the library at once.
func TestFakeArchivesADelistedAppUnderItsRealIdentity(t *testing.T) {
	meta, err := ipa.Read(downloadFixture(t, 6744684419))
	if err != nil {
		t.Fatal(err)
	}
	if meta.BundleID != "com.burbn.boomerang" {
		t.Errorf("bundle id = %q, want the installed fixture's com.burbn.boomerang", meta.BundleID)
	}
	if meta.Name != "Boomerang from Instagram" {
		t.Errorf("name = %q, want the store's name for it", meta.Name)
	}
}

// TestFakeArchiveCarriesAnIcon: every archive this fake produced was one with no icon in it, so
// ipa.Icon — the extractor the whole library screen depends on — was never run outside its own
// unit tests, and the library was a column of lettered tiles wherever the fake was used.
func TestFakeArchiveCarriesAnIcon(t *testing.T) {
	path := downloadFixture(t, 6744684419)

	icon, err := ipa.Icon(path)
	if err != nil {
		t.Fatalf("no icon in the fake's archive: %v", err)
	}
	if len(icon) == 0 {
		t.Fatal("icon is empty")
	}
	// The same picture the device draws for that bundle: the library and the device must not
	// disagree about what one app looks like.
	want, err := fakeIcon("com.burbn.boomerang")
	if err != nil {
		t.Fatal(err)
	}
	if string(icon) != string(want) {
		t.Error("the archive's icon is not the one the device draws for the same app")
	}
	if st, err := os.Stat(path); err == nil && st.Size() == 0 {
		t.Error("archive is empty")
	}
}

// TestFakeStillServesAnUnknownID: a hand-typed id for an app in no fixture at all is the ORDINARY
// case for a delisted archive, so it must still produce a usable .ipa rather than an error.
func TestFakeStillServesAnUnknownID(t *testing.T) {
	meta, err := ipa.Read(downloadFixture(t, 1234567890))
	if err != nil {
		t.Fatal(err)
	}
	if meta.BundleID != "com.example.app1234567890" {
		t.Errorf("bundle id = %q, want the synthetic fallback", meta.BundleID)
	}
}
