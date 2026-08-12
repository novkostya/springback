package devices

import (
	"context"
	"sync/atomic"
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

// Paired, and the records are readable — otherwise List refuses to read the device's name and
// Apps refuses to run at all, since springback will not open a lockdown session to a device it
// holds no pairing record for.
func (s *stubTools) PairingKnown() bool { return true }
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

// unpairedDevice is on the cable with no pairing record, and counts every question asked of it.
type unpairedDevice struct {
	tools.Tools
	asked atomic.Int64
}

func (s *unpairedDevice) ListDeviceUDIDs(context.Context) ([]string, error) {
	// On USB and visible to the muxer: `idevice_id -l` lists a device whether or not this host
	// has ever paired with it, which is exactly why this case exists.
	return []string{udid}, nil
}
func (s *unpairedDevice) PairedUDIDs(context.Context) ([]string, error) { return nil, nil }
func (s *unpairedDevice) PairingKnown() bool                            { return true }
func (s *unpairedDevice) DeviceValue(context.Context, string, string) (string, error) {
	s.asked.Add(1)
	return "", nil
}

// TestUnpairedDeviceIsNeverAskedAnything is the regression test for a trust prompt appearing on
// its own, which was reported twice.
//
// The mechanism is not obvious and that is why this exists: `ideviceinfo -k DeviceName` reads like
// a question and behaves like a pairing, because libimobiledevice completes a lockdown HANDSHAKE
// to answer it and a handshake with no record pairs. List asked four such questions of every
// reachable device every five seconds, so plugging a phone into the box made it ask "Trust This
// Computer?" seconds later with nothing on screen to explain why.
//
// Counting the calls rather than checking the pair label, because the label is cosmetic and the
// silence is the fix.
func TestUnpairedDeviceIsNeverAskedAnything(t *testing.T) {
	st := &unpairedDevice{}
	svc := &Service{Tools: st, Resolver: storefront.NewResolver(st, time.Hour, nil), Library: store.NewLibrary(t.TempDir())}

	list, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n := st.asked.Load(); n != 0 {
		t.Errorf("asked an unpaired device %d questions; every one of them can raise a trust prompt", n)
	}
	if len(list) != 1 {
		t.Fatalf("device list = %d entries, want 1 — an unpaired device is still a device", len(list))
	}
	if list[0].Pair != tools.Unpaired {
		t.Errorf("pair state = %q, want %q", list[0].Pair, tools.Unpaired)
	}
	// Reachable AND unpaired: the row is drawn from the pair state, not this flag, but the
	// flag must stay honest — the device really is on the cable.
	if !list[0].Reachable {
		t.Error("an unpaired device on USB should still be reachable")
	}
}

// TestUnreadableRecordsDoNotLockEverythingOut: "no record" is only a fact when the records can be
// read. A missing mount must not turn every device on the box into an unpaired one that springback
// refuses to talk to — that turns one misconfiguration into a completely dead UI.
func TestUnreadableRecordsDoNotLockEverythingOut(t *testing.T) {
	st := &unknownRecords{}
	svc := &Service{Tools: st, Resolver: storefront.NewResolver(st, time.Hour, nil), Library: store.NewLibrary(t.TempDir())}

	list, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Pair != tools.PairUnknown {
		t.Fatalf("pair state = %+v, want one device reported unknown", list)
	}
	if list[0].Name != "test" {
		t.Errorf("name = %q — an unknown pairing state must not stop the device being read", list[0].Name)
	}
}

type unknownRecords struct{ stubTools }

func (s *unknownRecords) PairingKnown() bool { return false }

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
