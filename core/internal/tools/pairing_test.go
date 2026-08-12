package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
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
			_, err := r.DeviceValue(context.Background(), "00008140-000269063E88801C", "DeviceName")
			return err
		},
		"ListApps": func() error {
			_, err := r.ListApps(context.Background(), "00008140-000269063E88801C")
			return err
		},
		"DeviceIcons": func() error {
			_, err := r.DeviceIcons(context.Background(), "00008140-000269063E88801C", []string{"com.example.app"})
			return err
		},
		"InstallApp": func() error {
			return r.InstallApp(context.Background(), "00008140-000269063E88801C", "/nonexistent.ipa", nil)
		},
		"SetWifiSync": func() error {
			return r.SetWifiSync(context.Background(), "00008140-000269063E88801C", true)
		},
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrNotPaired) {
			t.Errorf("%s on an unpaired device = %v, want ErrNotPaired — it would have raised a trust prompt", name, err)
		}
	}

	// WifiSync is the one that reports a state rather than an error, because its caller draws a
	// switch and "unknown" is what an unreadable flag means everywhere else in that screen.
	if got, _ := r.WifiSync(context.Background(), "00008140-000269063E88801C"); got != WifiSyncUnknown {
		t.Errorf("WifiSync on an unpaired device = %q, want %q", got, WifiSyncUnknown)
	}
}

// TestARecordIsWhatMakesADeviceTouchable: the guard must open again once pairing has happened, or
// the Pair button would leave the device permanently unusable.
func TestARecordIsWhatMakesADeviceTouchable(t *testing.T) {
	dir := t.TempDir()
	r := &Real{LockdownDir: dir}

	if err := r.requirePaired("00008140-000269063E88801C"); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("before pairing = %v, want ErrNotPaired", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00008140-000269063E88801C.plist"), []byte("<plist/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.requirePaired("00008140-000269063E88801C"); err != nil {
		t.Errorf("after pairing = %v, want nil", err)
	}
}

// TestUnpairForgetsTheDeviceEvenWhenItIsNotThere is the reported bug: unpairing a device that is
// asleep or unplugged left it in the list as an offline row that nothing could remove.
//
// idevicepair does two things and fails at the first — it needs the phone in order to tell the
// phone. But the record that puts a device in the list is a file on THIS disk, and deleting it
// needs nothing. `idevicepair` is not even installed in the test image, so the device half fails
// here exactly as it does for an absent phone.
func TestUnpairForgetsTheDeviceEvenWhenItIsNotThere(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "00008140-000269063E88801C.plist")
	if err := os.WriteFile(record, []byte("<plist/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Real{LockdownDir: dir, DeviceTimeout: time.Second}

	if err := r.Unpair(context.Background(), "00008140-000269063E88801C"); err != nil {
		t.Fatalf("Unpair = %v, want nil: the host can always forget a device", err)
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Error("the pairing record survived, so the device stays in the list forever")
	}
	// And the device is now genuinely unpaired as far as everything else is concerned.
	if err := r.requirePaired("00008140-000269063E88801C"); !errors.Is(err, ErrNotPaired) {
		t.Errorf("after unpairing, requirePaired = %v, want ErrNotPaired", err)
	}
}

// TestUnpairWillNotDeleteWhateverItIsAsked: the udid arrives in a URL path, and it becomes a
// filename. Nothing that is not a udid may get that far.
func TestUnpairWillNotDeleteWhateverItIsAsked(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "SystemConfiguration.plist")
	if err := os.WriteFile(victim, []byte("<plist/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Real{LockdownDir: dir, DeviceTimeout: time.Second}

	for _, udid := range []string{"../SystemConfiguration", "SystemConfiguration", "a/b", ".."} {
		if err := r.removePairingRecord(udid); err == nil {
			t.Errorf("removePairingRecord(%q) was allowed", udid)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Error("the host's own identity file was deleted")
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
		if err := r.requirePaired("00008140-000269063E88801C"); err != nil {
			t.Errorf("%s: requirePaired = %v, want nil (unknown must not lock the device out)", name, err)
		}
	}
}
