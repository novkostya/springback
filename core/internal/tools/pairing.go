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
func (r *Real) Unpair(ctx context.Context, udid string) error {
	if err := r.pairingWritable(); err != nil {
		return err
	}
	out, err := r.run(ctx, r.DeviceTimeout, r.deviceEnv(), "", "idevicepair", append(r.netFlag(udid), "-u", udid, "unpair")...)
	if err != nil {
		return classifyPair(out, err)
	}
	return nil
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
