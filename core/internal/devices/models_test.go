package devices

import (
	"context"
	"testing"

	"github.com/novkostya/springback/core/internal/store"
	"github.com/novkostya/springback/core/internal/tools"
)

func TestModelName(t *testing.T) {
	cases := map[string]string{
		"iPhone17,1": "iPhone 16 Pro",
		"iPhone14,3": "iPhone 13 Pro Max",
		"iPad15,7":   "iPad (A16)",
		// THE IMPORTANT ONE. This table is out of date the moment Apple announces
		// something, so an identifier it has never heard of has to come back as itself —
		// not blank, and certainly not as some other phone.
		"iPhone99,9": "iPhone99,9",
		"":           "",
	}
	for id, want := range cases {
		if got := ModelName(id); got != want {
			t.Errorf("ModelName(%q) = %q, want %q", id, got, want)
		}
	}
}

// comingAndGoing is a device that answers, and then stops.
type comingAndGoing struct {
	tools.Tools
	awake bool
}

func (s *comingAndGoing) ListDeviceUDIDs(context.Context) ([]string, error) {
	if s.awake {
		return []string{"UDID-1"}, nil
	}
	return nil, nil
}
func (s *comingAndGoing) PairedUDIDs(context.Context) ([]string, error) {
	return []string{"UDID-1"}, nil
}

// The record survives the device going away, which is what makes an offline device still a
// device. Readable, so List trusts it.
func (s *comingAndGoing) PairingKnown() bool { return true }

// On the cable while it is awake, and asked of nothing once it is gone.
func (s *comingAndGoing) Transport(string) string { return "usb" }
func (s *comingAndGoing) DeviceValue(_ context.Context, _, key string) (string, error) {
	if !s.awake {
		return "", tools.ErrDeviceUnreachable
	}
	switch key {
	case "DeviceName":
		return "Anna's iPhone", nil
	case "ProductType":
		return "iPhone17,1", nil
	case "ProductVersion":
		return "26.6", nil
	case "RegionInfo":
		return "AE/A", nil
	}
	return "", nil
}

// TestOfflineDeviceKeepsItsName is the whole point of the cache: a device that has gone away
// still renders as a phone rather than as forty characters of hex.
func TestOfflineDeviceKeepsItsName(t *testing.T) {
	st := &comingAndGoing{awake: true}
	svc := &Service{Tools: st, Cache: store.NewDeviceCache(t.TempDir())}

	awake, err := svc.List(context.Background())
	if err != nil || len(awake) != 1 {
		t.Fatalf("List while awake: %v (%d devices)", err, len(awake))
	}
	if awake[0].Name != "Anna's iPhone" || awake[0].Model != "iPhone 16 Pro" || !awake[0].Reachable {
		t.Fatalf("awake device = %+v", awake[0])
	}

	// The phone leaves the building.
	st.awake = false
	gone, err := svc.List(context.Background())
	if err != nil || len(gone) != 1 {
		t.Fatalf("List while offline: %v (%d devices)", err, len(gone))
	}
	d := gone[0]
	if d.Reachable {
		t.Error("device still reported as reachable")
	}
	if d.Name != "Anna's iPhone" {
		t.Errorf("offline device lost its name: %q", d.Name)
	}
	if d.Model != "iPhone 16 Pro" {
		t.Errorf("offline device lost its model: %q", d.Model)
	}
	if d.IOS != "26.6" || d.Region != "AE/A" {
		t.Errorf("offline device lost its facts: %+v", d)
	}
	if d.LastSeen.IsZero() {
		t.Error("offline device carries no last-seen time to show")
	}
}

// TestOfflineDeviceNeverSeenIsHonest: a device known only from a pairing record, never once
// reachable, has nothing to show but its udid — and must not invent anything.
func TestOfflineDeviceNeverSeenIsHonest(t *testing.T) {
	svc := &Service{Tools: &comingAndGoing{awake: false}, Cache: store.NewDeviceCache(t.TempDir())}
	got, err := svc.List(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("List: %v (%d)", err, len(got))
	}
	if got[0].Name != "" || got[0].IOS != "" {
		t.Errorf("invented facts for a device never seen: %+v", got[0])
	}
	if !got[0].LastSeen.IsZero() {
		t.Error("a device never seen has a last-seen time")
	}
}
