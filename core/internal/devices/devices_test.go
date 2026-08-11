package devices

import (
	"context"
	"testing"
	"time"

	"github.com/novkostya/springback/core/internal/store"
	"github.com/novkostya/springback/core/internal/storefront"
	"github.com/novkostya/springback/core/internal/tools"
)

// stubTools serves one device's app list and a store that contains nothing, so every judged app
// comes back delisted unless something upstream declines to judge it.
type stubTools struct {
	tools.Tools
	apps []tools.InstalledApp
}

const udid = "UDID-1"

func (s *stubTools) ListDeviceUDIDs(context.Context) ([]string, error) { return []string{udid}, nil }
func (s *stubTools) PairedUDIDs(context.Context) ([]string, error)     { return []string{udid}, nil }
func (s *stubTools) DeviceValue(_ context.Context, _, key string) (string, error) {
	if key == "RegionInfo" {
		return "AE/A", nil
	}
	return "test", nil
}
func (s *stubTools) ListApps(context.Context, string) ([]tools.InstalledApp, error) {
	return s.apps, nil
}
func (s *stubTools) Lookup(_ context.Context, _, country string) tools.StoreLookup {
	return tools.StoreLookup{Country: country, Checked: true}
}

func svc(t *testing.T, apps []tools.InstalledApp) *Service {
	st := &stubTools{apps: apps}
	return &Service{
		Tools:    st,
		Resolver: storefront.NewResolver(st, time.Hour, nil),
		Library:  store.NewLibrary(t.TempDir()),
	}
}

// An app the device holds NO purchase receipt for was never bought from the App Store, so it
// cannot have been delisted. Caught on the fake's fixtures, where a sideloaded build was being
// reported as an app the store had pulled — the exact overclaim the receipt work exists to avoid.
func TestAppWithNoReceiptIsNotDelisted(t *testing.T) {
	res, err := svc(t, []tools.InstalledApp{
		{BundleID: "com.bought.thing", AppID: 123, Storefront: "ru"},
		{BundleID: "com.example.sideloaded"}, // no receipt
	}).Apps(context.Background(), udid)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]App{}
	for _, a := range res.Apps {
		byID[a.BundleID] = a
	}
	if got := byID["com.example.sideloaded"].Status; got != storefront.NotListed {
		t.Errorf("sideloaded app: got %q, want %q", got, storefront.NotListed)
	}
	if got := byID["com.bought.thing"].Status; got != storefront.Delisted {
		t.Errorf("a real purchase absent from every store should still be %q, got %q", storefront.Delisted, got)
	}
	if res.Delisted != 1 || res.NotListed != 1 {
		t.Errorf("counts: delisted=%d not_listed=%d, want 1 and 1", res.Delisted, res.NotListed)
	}
}

// If NO app on the device reports a receipt, the receipt path did not work here at all — the CSV
// fallback, or an older iOS. A missing receipt then says nothing about any individual app, and
// judging by storefront is the best answer available. The opposite reading would report every
// app on such a device as "not listed" and find nothing at all.
func TestNoReceiptsAnywhereFallsBackToJudgingByStorefront(t *testing.T) {
	res, err := svc(t, []tools.InstalledApp{
		{BundleID: "com.a.one"},
		{BundleID: "com.a.two"},
	}).Apps(context.Background(), udid)
	if err != nil {
		t.Fatal(err)
	}
	if res.Delisted != 2 {
		t.Fatalf("delisted=%d, want 2 — a device that reports no receipts at all must still be judged", res.Delisted)
	}
	if res.NotListed != 0 {
		t.Errorf("not_listed=%d, want 0", res.NotListed)
	}
}

// A B2B or factory app is never a public listing whatever else is true of it.
func TestNotPublicIsNeverDelisted(t *testing.T) {
	res, err := svc(t, []tools.InstalledApp{
		{BundleID: "com.acme.internal", AppID: 999, NotPublic: true},
	}).Apps(context.Background(), udid)
	if err != nil {
		t.Fatal(err)
	}
	if res.Apps[0].Status != storefront.NotListed || res.Delisted != 0 {
		t.Errorf("got %q delisted=%d", res.Apps[0].Status, res.Delisted)
	}
}
