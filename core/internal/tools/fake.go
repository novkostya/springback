package tools

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Fake is the whole app minus Apple and minus hardware.
//
// THE FIXTURES ARE REAL. The device names, region codes, bundle ids and versions below were read
// off the two devices paired to the staging host on 2026-08-11 (`idevice_id -n`, `ideviceinfo`,
// `ideviceinstaller list --user`), and the three store outcomes are the ones SPEC §3 measured
// against the live lookup API. A fake built from invented data would agree with any
// implementation of the at-risk rule, including a wrong one.
//
// The three cases it carries, and why each has to be there:
//
//	ru.yandex.mobile.music        us=0  ru=1  ae=1   NOT delisted — just not sold in the US
//	com.dreamgoods.officecapital  us=0  ru=0  ae=0   genuinely gone
//	com.assetsonline.ios          us=0  ru=0  ae=0   genuinely gone
//
// The first one is the fixture that matters. A single-storefront implementation calls it
// DELISTED and looks completely correct on every other row — so this is the app that fails the
// build when the multi-storefront rule is broken.
type Fake struct {
	mu sync.Mutex

	// Devices are keyed by udid. Asleep devices are in the map and out of ListDeviceUDIDs,
	// which is exactly the shape reality has: the pairing record persists, mDNS does not.
	devices map[string]*fakeDevice
	// apps by udid.
	apps map[string][]InstalledApp
	// store maps bundle id -> the storefronts it is present in.
	store map[string]map[string]int64
	// authed tracks which HOME directories have completed login, and pending2FA which are
	// mid-2FA. `2fa@example.com` is the address that exercises the two-step form.
	authed    map[string]Account
	pending   map[string]bool
	LookupErr map[string]bool
}

type fakeDevice struct {
	Device
	awake bool
}

// NewFake builds the fixture set described on the type.
func NewFake() *Fake {
	const iphone = "00008140-000269063E88801C"
	const ipad = "00008120-000C1DDE20614932"
	// The Apple ID every receipt on the staging device names. A placeholder here, since the
	// fake is what runs on a shared dev box.
	const owner = "owner@example.com"

	f := &Fake{
		devices: map[string]*fakeDevice{
			iphone: {Device{
				UDID: iphone, Name: "novkostya-iphone", ProductType: "iPhone17,1",
				IOS: "26.6", Region: "AE/A",
			}, true},
			// LL/A is the fixture that keeps the storefront mapping honest: read naively
			// it produces country=ll, which the live API answers with HTTP 400. It is
			// also ASLEEP, so the "paired but not currently reachable" path is on screen
			// by default rather than only when someone remembers to test it.
			ipad: {Device{
				UDID: ipad, Name: "iPad (2)", ProductType: "iPad15,7",
				IOS: "26.6", Region: "LL/A",
			}, false},
		},
		apps: map[string][]InstalledApp{
			// The ids, owners and storefronts below are real, read off the staging
			// iPhone's own purchase receipts. The delisted ones carry ids too — that is
			// the whole point: the receipt outlives the listing, so Archive never has to
			// ask for a number.
			//
			// storefront `ru` on an `AE/A` device is not a typo. It is the measurement
			// that says the receipt beats the region.
			iphone: {
				{BundleID: "ru.aviasales.app", Version: "9.28", Name: "Aviasales",
					AppID: 358848275, StoreName: "Aviasales", OwnerAppleID: owner, Storefront: "ru"},
				{BundleID: "ru.yandex.mobile.music", Version: "797", Name: "Yandex Music",
					AppID: 599981012, StoreName: "Яндекс Музыка", OwnerAppleID: owner, Storefront: "ru"},
				{BundleID: "com.dreamgoods.officecapital", Version: "1.8", Name: "OfficeCapital",
					AppID: 6744684419, StoreName: "Office-Capital", Artist: "Yauheni Pazniak", OwnerAppleID: owner, Storefront: "ru"},
				{BundleID: "com.assetsonline.ios", Version: "2.1", Name: "Assets Online",
					AppID: 6742457200, StoreName: "Аssets Оnline", OwnerAppleID: owner, Storefront: "ru"},
				{BundleID: "io.wio.retail", Version: "1.69.0", Name: "Wio Personal",
					AppID: 1592748917, StoreName: "Wio Personal", OwnerAppleID: owner, Storefront: "ae"},
				{BundleID: "com.google.ios.youtube", Version: "21.31.3", Name: "YouTube",
					AppID: 544007664, StoreName: "YouTube", OwnerAppleID: owner, Storefront: "ru"},
				{BundleID: "com.tinyspeck.chatlyio", Version: "26.08.10", Name: "Slack",
					AppID: 618783545, StoreName: "Slack", OwnerAppleID: owner, Storefront: "ru"},
				{BundleID: "ru.cardsmobile.wallet", Version: "6.63", Name: "Кошелёк",
					AppID: 921320737, StoreName: "Кошелёк", OwnerAppleID: owner, Storefront: "ru"},
				{BundleID: "com.flydubai.app.booking", Version: "6.8.29", Name: "flydubai",
					AppID: 1013889784, StoreName: "flydubai", OwnerAppleID: owner, Storefront: "ae"},
				{BundleID: "info.tapestry.journal", Version: "5.2.1", Name: "Tapestry",
					AppID: 1442916401, StoreName: "Tapestry Journal", OwnerAppleID: owner, Storefront: "ru"},
				// A B2B custom app: never a public listing, so it must be reported as
				// not_listed rather than counted among the apps at risk.
				{BundleID: "com.acme.internal.fieldtool", Version: "3.2", Name: "Field Tool",
					AppID: 1500000001, StoreName: "Acme Field Tool", OwnerAppleID: owner, Storefront: "ae", NotPublic: true},
				// No receipt at all — a developer-signed build. Nothing to archive, and
				// nothing to accuse it of.
				{BundleID: "com.example.sideloaded", Version: "0.9", Name: "Sideloaded Thing"},
			},
			ipad: {
				{BundleID: "com.google.ios.youtube", Version: "21.31.3", Name: "YouTube",
					AppID: 544007664, StoreName: "YouTube", OwnerAppleID: owner, Storefront: "ru"},
				{BundleID: "com.dreamgoods.officecapital", Version: "1.8", Name: "OfficeCapital",
					AppID: 6744684419, StoreName: "Office-Capital", OwnerAppleID: owner, Storefront: "ru"},
			},
		},
		store: map[string]map[string]int64{
			"ru.aviasales.app":         {"ru": 358848275, "ae": 358848275, "us": 358848275},
			"ru.yandex.mobile.music":   {"ru": 599981012, "ae": 599981012},
			"io.wio.retail":            {"ae": 1592748917},
			"com.google.ios.youtube":   {"us": 544007664, "ru": 544007664, "ae": 544007664},
			"com.tinyspeck.chatlyio":   {"us": 618783545, "ru": 618783545, "ae": 618783545},
			"ru.cardsmobile.wallet":    {"ru": 921320737},
			"com.flydubai.app.booking": {"ae": 1013889784, "us": 1013889784},
			"info.tapestry.journal":    {"us": 6448124272, "ae": 6448124272},
			// com.dreamgoods.officecapital and com.assetsonline.ios are in NO storefront.
			// Their absence from this map is the fixture.
		},
		authed:    map[string]Account{},
		pending:   map[string]bool{},
		LookupErr: map[string]bool{},
	}
	return f
}

func (f *Fake) ListDeviceUDIDs(ctx context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for udid, d := range f.devices {
		if d.awake {
			out = append(out, udid)
		}
	}
	return out, nil
}

func (f *Fake) PairedUDIDs(ctx context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.devices))
	for udid := range f.devices {
		out = append(out, udid)
	}
	return out, nil
}

func (f *Fake) DeviceValue(ctx context.Context, udid, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[udid]
	if !ok || !d.awake {
		return "", ErrDeviceUnreachable
	}
	switch key {
	case "DeviceName":
		return d.Name, nil
	case "ProductType":
		return d.ProductType, nil
	case "ProductVersion":
		return d.IOS, nil
	case "RegionInfo":
		return d.Region, nil
	}
	return "", nil
}

func (f *Fake) ListApps(ctx context.Context, udid string) ([]InstalledApp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[udid]
	if !ok || !d.awake {
		return nil, ErrDeviceUnreachable
	}
	return append([]InstalledApp(nil), f.apps[udid]...), nil
}

func (f *Fake) InstallApp(ctx context.Context, udid, ipaPath string, onProgress func(InstallProgress)) error {
	f.mu.Lock()
	d, ok := f.devices[udid]
	f.mu.Unlock()
	if !ok || !d.awake {
		return ErrDeviceUnreachable
	}
	for _, p := range []InstallProgress{
		{Stage: "CreatingStagingDirectory", Percent: 5},
		{Stage: "ExtractingPackage", Percent: 15},
		{Stage: "InspectingPackage", Percent: 20},
		{Stage: "VerifyingApplication", Percent: 45},
		{Stage: "InstallingApplication", Percent: 60},
		{Stage: "GeneratingApplicationMap", Percent: 90},
	} {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
		if onProgress != nil {
			onProgress(p)
		}
	}
	return nil
}

func (f *Fake) AuthLogin(ctx context.Context, home, passphrase, email, password, authCode string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if password == "" {
		return fmt.Errorf("empty password")
	}
	// Any address containing "2fa" walks the two-step path, so the second form is reachable
	// on a box with no Apple ID at all.
	if strings.Contains(strings.ToLower(email), "2fa") && authCode == "" {
		f.pending[home] = true
		return ErrNeeds2FA
	}
	delete(f.pending, home)
	name := strings.TrimSuffix(email, filepath.Ext(email))
	if i := strings.Index(email, "@"); i > 0 {
		name = email[:i]
	}
	f.authed[home] = Account{Email: email, Name: name}
	return nil
}

func (f *Fake) AuthInfo(ctx context.Context, home, passphrase string) (Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	acc, ok := f.authed[home]
	if !ok {
		return Account{}, ErrNotAuthenticated
	}
	return acc, nil
}

// Download writes a REAL .ipa-shaped zip: Payload/<name>.app/Info.plist plus an
// iTunesMetadata.plist at the archive root, both XML plists. The library layer reads its
// meta.json out of the archive rather than trusting what the user typed (SPEC §4), so a fake
// that returned an empty file would leave that whole path untested on this box.
func (f *Fake) Download(ctx context.Context, home, passphrase string, appID int64, outPath string, onProgress func(DownloadProgress)) (DownloadResult, error) {
	f.mu.Lock()
	_, ok := f.authed[home]
	bundle, name := f.bundleForIDLocked(appID)
	f.mu.Unlock()
	if !ok {
		return DownloadResult{}, ErrNotAuthenticated
	}
	if appID <= 0 {
		return DownloadResult{}, ErrAppNotFound
	}

	// Progress, paced like a real download, so the progress UI can be built and watched on a
	// box with no Apple ID. The byte figures track a plausible ~200 MB app at ~35 MB/s — the
	// rate measured off the real tool.
	if onProgress != nil {
		const totalMB = 197
		for pct := 0; pct <= 100; pct += 4 {
			select {
			case <-ctx.Done():
				return DownloadResult{}, ctx.Err()
			case <-time.After(120 * time.Millisecond):
			}
			onProgress(DownloadProgress{
				Percent: pct,
				Detail:  fmt.Sprintf("%d/%d MB, 35 MB/s", totalMB*pct/100, totalMB),
			})
		}
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return DownloadResult{}, err
	}
	fh, err := os.Create(outPath)
	if err != nil {
		return DownloadResult{}, err
	}
	defer func() { _ = fh.Close() }()

	zw := zip.NewWriter(fh)
	info := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>%s</string>
<key>CFBundleShortVersionString</key><string>1.0</string>
<key>CFBundleDisplayName</key><string>%s</string>
</dict></plist>`, bundle, name)
	meta := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>itemId</key><integer>%d</integer>
<key>itemName</key><string>%s</string>
<key>artistName</key><string>springback fake</string>
<key>bundleShortVersionString</key><string>1.0</string>
</dict></plist>`, appID, name)

	for path, body := range map[string]string{
		"Payload/" + name + ".app/Info.plist": info,
		"iTunesMetadata.plist":                meta,
	} {
		w, err := zw.Create(path)
		if err != nil {
			return DownloadResult{}, err
		}
		if _, err := w.Write([]byte(body)); err != nil {
			return DownloadResult{}, err
		}
	}
	// Some bulk, so the library's size column shows something and a progress-free 500 MB
	// download is at least suggested. Kept small: this runs on a dev box.
	w, err := zw.Create("Payload/" + name + ".app/" + name)
	if err != nil {
		return DownloadResult{}, err
	}
	if _, err := w.Write(make([]byte, 512*1024)); err != nil {
		return DownloadResult{}, err
	}
	if err := zw.Close(); err != nil {
		return DownloadResult{}, err
	}

	// purchased=false always: the fake never simulates acquiring a licence, because the real
	// path never does either.
	return DownloadResult{Purchased: false, Path: outPath}, nil
}

func (f *Fake) bundleForIDLocked(appID int64) (bundle, name string) {
	for b, fronts := range f.store {
		for _, id := range fronts {
			if id == appID {
				return b, lastSegment(b)
			}
		}
	}
	// An id with no fixture is the ordinary case for a DELISTED app typed in by hand — it is
	// in no storefront by definition. Serve it rather than refusing, or the one flow the tool
	// exists for would be the one flow the fake cannot walk.
	return fmt.Sprintf("com.example.app%d", appID), fmt.Sprintf("App%d", appID)
}

func lastSegment(bundle string) string {
	parts := strings.Split(bundle, ".")
	return parts[len(parts)-1]
}

func (f *Fake) Lookup(ctx context.Context, bundleID, country string) StoreLookup {
	f.mu.Lock()
	defer f.mu.Unlock()

	res := StoreLookup{Country: country}
	if f.LookupErr[country] {
		res.Err = fmt.Errorf("itunes lookup %s: injected failure", country)
		return res
	}
	// The live API answers an unknown storefront with HTTP 400, which is "not checked" and
	// never "not in the store". The fake reproduces that rather than returning zero results,
	// because the difference is the whole false-positive guard.
	if !validStorefront(country) {
		res.Err = fmt.Errorf("itunes lookup %s: http 400", country)
		return res
	}

	res.Checked = true
	if id, ok := f.store[bundleID][country]; ok {
		res.Found = true
		res.TrackID = id
		res.TrackName = lastSegment(bundleID)
	}
	return res
}

// validStorefront is the fake's stand-in for Apple's own storefront list — deliberately small,
// covering only what the fixtures and the region mapping produce.
func validStorefront(cc string) bool {
	switch cc {
	case "us", "ru", "ae", "gb", "de", "fr", "jp", "ca", "au", "in", "sg", "hk", "cn", "kr", "tw", "it", "es", "nl", "se", "ch", "tr", "br", "mx", "pl", "ua", "kz", "ge", "am", "az", "il", "sa", "qa", "kw", "bh", "om", "eg":
		return true
	}
	return false
}
