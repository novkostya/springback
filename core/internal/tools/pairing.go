package tools

// Pairing and Wi-Fi sync — what springback needs in order to stand on its own.
//
// Up to now springback READ pairing records another tool wrote, mounted read-only, which
// was right while that tool owned the devices: a small tool has no business corrupting pairing
// state another one depends on. Standing alone, nothing else is going to pair a device, so it has
// to do it itself.
//
// STILL NO usbmuxd IN THE IMAGE. Pairing needs USB, and a muxer inside the container would fight
// whatever is already on the host bus for it. The documented deployment runs usbmuxd (or netmuxd)
// on the host and points springback at it — the same arrangement as before, except that what is
// on the other end no longer has to be somebody else's tool.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PairState is what this host knows about its pairing with a device.
type PairState string

const (
	Paired   PairState = "paired"
	Unpaired PairState = "unpaired"
	// PairUnknown is a genuine failure to ask — the device was not answering — and is NOT the
	// same as "not paired". Conflating them would offer a Pair button for a device that is
	// merely asleep, which cannot work and reads as though the pairing had been lost.
	PairUnknown PairState = "unknown"
)

// WifiSyncState is the device's EnableWifiConnections flag.
type WifiSyncState string

const (
	WifiSyncOn  WifiSyncState = "on"
	WifiSyncOff WifiSyncState = "off"
	// WifiSyncUnknown means the key could not be read. Reporting "off" for an unread key
	// would be a confident lie about every device that happened not to answer.
	WifiSyncUnknown WifiSyncState = "unknown"
)

var (
	// ErrTrustPending — the device is showing its "Trust This Computer?" dialog and nobody has
	// tapped it yet. Not a failure: the remedy is to look at the phone, and pairing works on
	// the next attempt.
	ErrTrustPending = errors.New("accept the trust dialog on the device")
	// ErrPasscodeLocked — the device is locked. lockdown will not pair with a locked phone.
	ErrPasscodeLocked = errors.New("unlock the device and try again")
	// ErrNeedsUSB — pairing happens over USB. Wireless pairing exists in idevicepair but is
	// Apple TV only, so a phone that is only on wifi cannot be paired from here.
	ErrNeedsUSB = errors.New("connect the device over USB to pair it")
	// ErrPairingReadOnly — the pairing directory is mounted read-only, which is the correct
	// setup when something else owns the pairing records and a dead end when nothing does.
	ErrPairingReadOnly = errors.New("the pairing directory is read-only")
	// ErrWifiSyncNotApplied — the device accepted the write and still reports the old value.
	ErrWifiSyncNotApplied = errors.New("the device did not apply the change")
	// ErrNotPaired — this host holds no pairing record for the device, so springback refused
	// to open a lockdown session to it.
	//
	// THE REFUSAL IS THE POINT, and it is not about tidiness. libimobiledevice's
	// lockdownd_client_new_with_handshake() PAIRS when validation fails for want of a record —
	// so `ideviceinfo -k DeviceName`, an apparently read-only question, makes the phone ask
	// "Trust This Computer?". springback asks that question for four keys on every reachable
	// device on every scan, which meant plugging a phone in raised a trust prompt nobody asked
	// for, seconds later, with nothing on screen to explain it. Reported twice.
	//
	// Pairing is a deliberate act with a button of its own. Nothing else here may start one.
	ErrNotPaired = errors.New("this host is not paired with the device")
)

// PairStatus asks whether this host holds a valid pairing record for the device.
func (r *Real) PairStatus(ctx context.Context, udid string) (PairState, error) {
	out, err := r.run(ctx, r.DeviceTimeout, r.deviceEnv(), "", "idevicepair", append(r.netFlag(udid), "-u", udid, "validate")...)
	if err == nil {
		return Paired, nil
	}
	low := strings.ToLower(out)
	switch {
	case strings.Contains(low, "not paired"), strings.Contains(low, "invalid host id"),
		strings.Contains(low, "invalidhostid"):
		return Unpaired, nil
	case strings.Contains(low, "no device found"), strings.Contains(low, "could not connect"):
		// Asleep or unplugged. Says nothing about whether a record exists.
		return PairUnknown, nil
	}
	return PairUnknown, fmt.Errorf("idevicepair validate: %w", classifyPair(out, err))
}

// Pair runs the pairing handshake. The device must be on USB and unlocked, and somebody has to
// tap Trust on it.
func (r *Real) Pair(ctx context.Context, udid string) error {
	if err := r.pairingWritable(); err != nil {
		return err
	}
	// REFUSED BEFORE IT IS ATTEMPTED when the device is only on Wi-Fi. idevicepair does have a
	// wireless mode, and its own help says it is Apple TV only — so on a phone the attempt
	// fails with a lockdown error that explains nothing about the actual remedy, which is a
	// cable. Better to say so than to relay a confusing failure.
	if r.Transport(udid) != transportUSB {
		return ErrNeedsUSB
	}
	out, err := r.run(ctx, r.PairTimeout(), r.deviceEnv(), "", "idevicepair", append(r.netFlag(udid), "-u", udid, "pair")...)
	if err != nil {
		return classifyPair(out, err)
	}
	return nil
}

// Unpair removes this host's pairing record.
//
// TWO THINGS HAPPEN HERE AND ONLY ONE OF THEM NEEDS THE DEVICE. Telling the phone to forget this
// computer needs the phone; deleting our own copy of the pairing record is a file on our own disk.
// idevicepair does both and fails at the first, so unpairing a device that is asleep or unplugged
// left the record sitting there — and a record is what puts a device in the list, so the "gone"
// device came back as an offline row that nothing could remove. Reported.
//
// So the local record is removed whichever way the device call goes, and the device call becomes
// best-effort. That is also the honest reading of the button: the user asked THIS host to forget
// the device, and this host can always do that. The phone keeps a stale trust entry until it is
// next plugged in, which costs nothing — pairing again overwrites it.
func (r *Real) Unpair(ctx context.Context, udid string) error {
	if err := r.pairingWritable(); err != nil {
		return err
	}

	// Best effort, and its failure is not the answer: "No device found" here is the ordinary
	// case for a phone in somebody's pocket.
	out, devErr := r.run(ctx, r.DeviceTimeout, r.deviceEnv(), "", "idevicepair", append(r.netFlag(udid), "-u", udid, "unpair")...)

	switch err := r.removePairingRecord(udid); {
	case err == nil:
		return nil
	case devErr != nil:
		// Neither half worked. The device's complaint is the more useful one — it names the
		// state the device is in — so it is what the user sees.
		return classifyPair(out, devErr)
	default:
		return err
	}
}

// removePairingRecord deletes <lockdown>/<udid>.plist. A record that is already gone is success:
// idevicepair may well have removed it a moment ago, and the caller wants the end state.
func (r *Real) removePairingRecord(udid string) error {
	if r.LockdownDir == "" {
		return nil
	}
	// A udid is hex and hyphens. Anything else is not a device and must not become a path —
	// this value arrives from a URL, and `..` in it would delete a file elsewhere.
	if !safeUDID(udid) {
		return fmt.Errorf("not a udid: %q", udid)
	}
	if err := os.Remove(filepath.Join(r.LockdownDir, udid+".plist")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing the pairing record: %w", err)
	}
	return nil
}

// safeUDID accepts only what a udid can contain, so it can be used as a filename.
func safeUDID(udid string) bool {
	if udid == "" || len(udid) > 64 {
		return false
	}
	for _, c := range udid {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F', c == '-':
		default:
			return false
		}
	}
	return true
}

// PairTimeout is longer than an ordinary device call: the handshake waits on a human noticing a
// dialog on a phone that may be in another room.
func (r *Real) PairTimeout() time.Duration {
	if r.DeviceTimeout < 90*time.Second {
		return 90 * time.Second
	}
	return r.DeviceTimeout
}

// pairingWritable reports whether pairing records can be written at all.
//
// CHECKED BEFORE THE ATTEMPT, because the failure otherwise arrives as idevicepair's own
// complaint about a path the user never chose and cannot see from the UI. A read-only mount is a
// deployment decision — correct when something else owns the records — and deserves to be named
// as one.
func (r *Real) pairingWritable() error {
	if r.LockdownDir == "" {
		return nil
	}
	probe := filepath.Join(r.LockdownDir, ".springback-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsPermission(err) || errors.Is(err, os.ErrPermission) || isReadOnlyFS(err) {
			return fmt.Errorf("%w: %s", ErrPairingReadOnly, r.LockdownDir)
		}
		return fmt.Errorf("pairing directory %s: %w", r.LockdownDir, err)
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return nil
}

// PairingWritable is the exported form, so the UI can grey out a control it knows cannot work
// rather than offering a button that always fails.
func (r *Real) PairingWritable() bool { return r.pairingWritable() == nil }

// PairingKnown reports whether the pairing records can be READ — whether "there is no record for
// this device" is a fact or merely something springback cannot see.
//
// The distinction carries real weight, because everything below refuses to touch a device with no
// record. If the directory is simply not mounted, treating that as "nothing is paired" would
// refuse every device on the box and produce a completely dead UI out of a missing volume. So a
// directory that cannot be read means UNKNOWN, and unknown means behave as before.
func (r *Real) PairingKnown() bool {
	if r.LockdownDir == "" {
		return false
	}
	_, err := os.ReadDir(r.LockdownDir)
	return err == nil
}

// hasPairingRecord is the cheap, local answer to "have we ever paired with this device".
//
// A FILE TEST, NOT A DEVICE CALL, and that is the whole point: it is asked before every lockdown
// session, so it must cost nothing and must not itself talk to the device. `idevicepair validate`
// would be more authoritative — it asks the device whether the record still works — but it needs
// the device to be answering, and the case being guarded is exactly the one where reaching the
// device is what must not happen yet.
func (r *Real) hasPairingRecord(udid string) bool {
	// Same rule as the delete path: this value comes from a URL and becomes a path.
	if r.LockdownDir == "" || !safeUDID(udid) {
		return false
	}
	st, err := os.Stat(filepath.Join(r.LockdownDir, udid+".plist"))
	return err == nil && !st.IsDir()
}

// requirePaired is the guard in front of every command that opens a lockdown session.
//
// It exists because the alternative — remembering, at each of the five call sites, that this
// particular tool happens to pair as a side effect of being asked a question — is exactly the kind
// of thing that gets forgotten when a sixth is added.
func (r *Real) requirePaired(udid string) error {
	if !r.PairingKnown() || r.hasPairingRecord(udid) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrNotPaired, udid)
}

func isReadOnlyFS(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "read-only file system")
}

// classifyPair turns idevicepair's prose into the four things a user can actually do about it.
func classifyPair(out string, err error) error {
	low := strings.ToLower(out)
	switch {
	case strings.Contains(low, "trust dialog"), strings.Contains(low, "pairing dialog"):
		return ErrTrustPending
	case strings.Contains(low, "passcode"), strings.Contains(low, "password protected"),
		strings.Contains(low, "please enter the passcode"):
		return ErrPasscodeLocked
	case strings.Contains(low, "no device found"), strings.Contains(low, "could not connect"):
		return ErrNeedsUSB
	}
	return fmt.Errorf("%w: %s", err, sanitize(out))
}

// WifiSync reads the device's Wi-Fi sync flag.
func (r *Real) WifiSync(ctx context.Context, udid string) (WifiSyncState, error) {
	if err := r.requirePaired(udid); err != nil {
		return WifiSyncUnknown, nil
	}
	out, err := r.run(ctx, r.DeviceTimeout, r.deviceEnv(), "", "sbwifi", "get", udid)
	if err != nil {
		return WifiSyncUnknown, nil // unreachable device, not a fault worth surfacing
	}
	switch strings.TrimSpace(out) {
	case "on":
		return WifiSyncOn, nil
	case "off":
		return WifiSyncOff, nil
	}
	return WifiSyncUnknown, nil
}

// SetWifiSync writes the flag and checks it landed.
//
// TURNING IT OFF SEVERS THE CONNECTION THE CHECK WOULD USE. The device leaves the network the
// moment the write is applied, so an unreadable value after disabling is the expected shape of
// success — while after ENABLING it means nothing was confirmed. The helper reports what it saw;
// the difference is decided here, because only this layer knows which way the switch was thrown.
func (r *Real) SetWifiSync(ctx context.Context, udid string, enable bool) error {
	if err := r.requirePaired(udid); err != nil {
		return err
	}
	want := "off"
	if enable {
		want = "on"
	}
	out, err := r.run(ctx, r.DeviceTimeout, r.deviceEnv(), "", "sbwifi", "set", udid, want)
	if err != nil {
		return fmt.Errorf("sbwifi set: %w: %s", err, sanitize(out))
	}
	switch got := strings.TrimSpace(out); {
	case got == want:
		return nil
	case got == "unreadable" && !enable:
		return nil
	case got == "unreadable":
		return fmt.Errorf("%w: the device stopped answering before the change could be confirmed", ErrWifiSyncNotApplied)
	default:
		return fmt.Errorf("%w: asked for %s, device reports %s", ErrWifiSyncNotApplied, want, got)
	}
}
