// Package tools is the ONE seam between springback and the outside world.
//
// Every external call — ipatool, ideviceinstaller, idevice_id, ideviceinfo, and the iTunes
// lookup API — goes through the Tools interface. Nothing above this package builds an argv or
// opens a socket. Two implementations satisfy it: Real (shells out, talks to Apple) and Fake
// (canned fixtures, no hardware and no network).
//
// THE FAKE IS NOT A TEST DOUBLE THAT HAPPENS TO EXIST. springback is developed on a box with no
// iPhone, no Apple ID and no netmuxd, and the part of it that is a product rather than plumbing
// — deciding which installed apps are gone from the App Store — is exactly the part that needs
// the most exercising. The fake's fixtures carry all three store outcomes on purpose, including
// the not-sold-in-this-storefront case that produces a false DELISTED if the multi-storefront
// rule is ever broken.
package tools

import (
	"context"
	"errors"
)

// Device is one paired iPhone or iPad.
//
// Reachable is not a property of the device; it is a property of THIS MOMENT. A sleeping iPhone
// drops off mDNS entirely and vanishes from `idevice_id -n` (SPEC §3). Callers must render that
// as "not currently reachable" and never as "gone".
type Device struct {
	UDID        string `json:"udid"`
	Name        string `json:"name"`
	ProductType string `json:"product_type"`
	IOS         string `json:"ios"`
	// Region is the raw RegionInfo value, e.g. "AE/A" or "LL/A". It is an APPLE part-number
	// region code, NOT an ISO country code — see the storefront package for why that
	// distinction decides whether the at-risk screen is trustworthy.
	Region    string `json:"region"`
	Reachable bool   `json:"reachable"`
}

// InstalledApp is one row of `ideviceinstaller list --user`.
//
// There is no numeric App Store id here, and its absence is measured rather than assumed:
// installation_proxy returns Info.plist keys plus a handful of container attributes, and
// requesting `-a ITunesMetadata` returns nothing at all (verified against a live iPhone,
// 2026-08-11). So a delisted app's numeric id cannot be recovered from the device, which is why
// SPEC §4's one-time manual entry exists.
type InstalledApp struct {
	BundleID string `json:"bundle_id"`
	Version  string `json:"version"`
	Name     string `json:"name"`
}

// StoreLookup is one storefront's answer for one bundle id.
type StoreLookup struct {
	Country string
	// Checked distinguishes "this storefront answered, and the app is not in it" from "this
	// storefront never answered". Only the former is evidence of delisting. An unknown
	// storefront code returns HTTP 400 — NOT resultCount 0 — and conflating the two would
	// invent delisted apps out of a bad region code.
	Checked   bool
	Found     bool
	TrackID   int64
	TrackName string
	Err       error
}

// Account is what `ipatool auth info` reports for one Apple ID.
type Account struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// DownloadResult reports what a download actually did.
//
// Purchased false means the account already held the licence and nothing was bought, which is
// the only outcome springback ever produces: --purchase acquires a licence, a state change on
// someone's Apple account, and is never passed (SPEC §3).
type DownloadResult struct {
	Purchased bool   `json:"purchased"`
	Path      string `json:"path"`
	Output    string `json:"-"`
}

// InstallProgress is one parsed line of ideviceinstaller's install chatter.
type InstallProgress struct {
	Stage   string
	Percent int
}

// The failure modes of SPEC §7, as values. They are classified here, at the seam, because the
// raw strings are the vocabulary of four different tools and nothing above this package should
// have to know them. Each one maps to a different thing for the user to DO, which is the whole
// reason for telling them apart.
var (
	// ErrNeeds2FA — the first auth call always returns this for a 2FA account. Re-run the
	// same command with an --auth-code. Not a failure; a step.
	ErrNeeds2FA = errors.New("two-factor code required")
	// ErrAppNotFound — ipatool could not find the id. Means the numeric id is wrong. (It is
	// ALSO what -b returns for a delisted app, which is why springback never uses -b.)
	ErrAppNotFound = errors.New("app not found")
	// ErrLicenseNotFound — this Apple ID does not own it. Try another account.
	ErrLicenseNotFound = errors.New("license not found")
	// ErrNotAuthenticated — keyring "item not found": not logged in on that account, or the
	// stored passphrase is wrong.
	ErrNotAuthenticated = errors.New("not authenticated on this account")
	// ErrDeviceUnreachable — no pairing record, or netmuxd is not answering.
	ErrDeviceUnreachable = errors.New("device not reachable")
	// ErrInstallIncomplete — the install stopped before "Install: Complete".
	ErrInstallIncomplete = errors.New("install did not complete")
)

// Tools is the seam. Implementations: Real, Fake.
type Tools interface {
	// ListDeviceUDIDs returns the udids netmuxd can currently see. An EMPTY LIST IS NOT AN
	// ERROR — every device may simply be asleep.
	ListDeviceUDIDs(ctx context.Context) ([]string, error)
	// PairedUDIDs returns every device this host holds a pairing record for, awake or not.
	//
	// It is the ONLY reason a sleeping iPhone can be shown at all. idevice_id knows nothing
	// about a device that is not currently answering, so without the pairing records the
	// Devices screen would silently shrink to whatever happens to be awake — which is the
	// "gone" reading SPEC §3 forbids. The records are mounted READ-ONLY: springback is a
	// day-old tool and must not be able to corrupt the pairing state quince depends on.
	PairedUDIDs(ctx context.Context) ([]string, error)
	// DeviceValue reads one lockdown key, e.g. DeviceName or RegionInfo.
	DeviceValue(ctx context.Context, udid, key string) (string, error)
	// ListApps returns the user-installed apps on a device.
	ListApps(ctx context.Context, udid string) ([]InstalledApp, error)
	// InstallApp pushes an .ipa. onProgress may be nil.
	InstallApp(ctx context.Context, udid, ipaPath string, onProgress func(InstallProgress)) error

	// AuthLogin signs in. authCode is empty on the first call; ErrNeeds2FA means call again
	// with one. The password is passed to the process over STDIN — never argv, where `ps`
	// would show it (SPEC §3).
	AuthLogin(ctx context.Context, home, passphrase, email, password, authCode string) error
	// AuthInfo reports who a signed-in account directory belongs to.
	AuthInfo(ctx context.Context, home, passphrase string) (Account, error)
	// Download fetches an owned app BY NUMERIC ID. Never by bundle id: -b searches the store,
	// and a delisted app is not in search, so -b fails for exactly the apps springback exists
	// to fetch (SPEC §3, measured both ways).
	Download(ctx context.Context, home, passphrase string, appID int64, outPath string) (DownloadResult, error)

	// Lookup asks one storefront about one bundle id.
	Lookup(ctx context.Context, bundleID, country string) StoreLookup
}
