//go:build darwin

package main

import (
	"os"
	"path/filepath"
)

// NOTHING HERE IS A MOUNT POINT, and that is why these cannot be the Linux defaults. A .app is
// double-clicked with no arguments, so whatever these say is what the user silently gets —
// and what they would have got is /library and /accounts created at the root of the disk.
var (
	defaultLibrary  = appSupport("library")
	defaultAccounts = appSupport("accounts")

	// EMPTY, DELIBERATELY. Apple's usbmuxd owns the pairing records and hands them out over the
	// mux protocol; libimobiledevice keeps none on disk of its own, so there is no directory
	// here to read. /var/db/lockdown does exist, but it is unreadable without root and reading
	// it would mean answering from a copy of the truth instead of the truth.
	//
	// Empty makes PairingKnown() false, which is already a case the pairing code handles: it
	// means "springback cannot see the records", so it stops claiming a device is unpaired and
	// stops refusing to touch it. Degraded and honest, rather than wrong.
	defaultLockdown = ""
)

// appSupport is where macOS puts data an application owns rather than a user edits.
func appSupport(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		// No home directory is not a reason to fall back to a path at the root of the disk.
		// Empty fails at the MkdirAll in main with the directory named, which is readable.
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "springback", name)
}
