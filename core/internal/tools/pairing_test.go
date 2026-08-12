package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestNothingTouchesAnUnpairedDevice guards the rule at the seam, where it cannot be forgotten.
//
// devices.Service has its own check and its own test, and this is deliberately a second one in a
// different place. The reason is that the rule is not enforceable by remembering it: EVERY command
// here opens a lockdown session, and libimobiledevice pairs when it opens one with no record — so
// a sixth caller added later inherits the trust prompt unless the refusal lives below all of them.
func TestNothingTouchesAnUnpairedDevice(t *testing.T) {
	dir := t.TempDir()
	r := &Real{LockdownDir: dir}

	// The pairing directory is readable and holds no record for this device, which is exactly
	// the state a phone is in the moment it is first plugged in.
	calls := map[string]func() error{
		"DeviceValue": func() error {
			_, err := r.DeviceValue(context.Background(), "UDID-1", "DeviceName")
			return err
		},
		"ListApps": func() error {
			_, err := r.ListApps(context.Background(), "UDID-1")
			return err
		},
		"DeviceIcons": func() error {
			_, err := r.DeviceIcons(context.Background(), "UDID-1", []string{"com.example.app"})
			return err
		},
		"InstallApp": func() error {
			return r.InstallApp(context.Background(), "UDID-1", "/nonexistent.ipa", nil)
		},
		"SetWifiSync": func() error {
			return r.SetWifiSync(context.Background(), "UDID-1", true)
		},
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrNotPaired) {
			t.Errorf("%s on an unpaired device = %v, want ErrNotPaired — it would have raised a trust prompt", name, err)
		}
	}

	// WifiSync is the one that reports a state rather than an error, because its caller draws a
	// switch and "unknown" is what an unreadable flag means everywhere else in that screen.
	if got, _ := r.WifiSync(context.Background(), "UDID-1"); got != WifiSyncUnknown {
		t.Errorf("WifiSync on an unpaired device = %q, want %q", got, WifiSyncUnknown)
	}
}

// TestARecordIsWhatMakesADeviceTouchable: the guard must open again once pairing has happened, or
// the Pair button would leave the device permanently unusable.
func TestARecordIsWhatMakesADeviceTouchable(t *testing.T) {
	dir := t.TempDir()
	r := &Real{LockdownDir: dir}

	if err := r.requirePaired("UDID-1"); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("before pairing = %v, want ErrNotPaired", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "UDID-1.plist"), []byte("<plist/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.requirePaired("UDID-1"); err != nil {
		t.Errorf("after pairing = %v, want nil", err)
	}
}

// TestUnreadableRecordsMeanUnknownNotUnpaired: with no pairing directory at all, springback cannot
// tell an unpaired device from one whose record it simply cannot see. Refusing everything then
// would turn a missing volume into an application that does nothing and explains nothing — worse
// than the prompt this guard exists to prevent.
func TestUnreadableRecordsMeanUnknownNotUnpaired(t *testing.T) {
	for name, r := range map[string]*Real{
		"no directory configured": {LockdownDir: ""},
		"directory not mounted":   {LockdownDir: filepath.Join(t.TempDir(), "absent")},
	} {
		if r.PairingKnown() {
			t.Errorf("%s: PairingKnown = true", name)
		}
		if err := r.requirePaired("UDID-1"); err != nil {
			t.Errorf("%s: requirePaired = %v, want nil (unknown must not lock the device out)", name, err)
		}
	}
}
