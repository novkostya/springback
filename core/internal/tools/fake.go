package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"

	"github.com/novkostya/springback/core/internal/ipa"
	"strings"
	"sync"
	"time"
)

// Fake is the whole app minus Apple and minus hardware.
//
// THE SHAPES ARE MEASURED, THE IDENTIFIERS ARE NOT. Every behaviour encoded below was observed
// against real devices and the live lookup API — which region codes appear, that a receipt's
// storefront can differ from the device's own region, that a delisted app still carries a numeric
// id in its receipt. The udids and device names are synthetic, because a fake that ships one
// person's hardware inventory in a public repo is a fake that tells you about its author.
//
// A fake built from invented BEHAVIOUR, though, would agree with any implementation of the
// at-risk rule, including a wrong one. So the three store outcomes are kept exactly:
//
//	ru.yandex.mobile.music   us=0  ru=1  ae=1   NOT delisted — just not sold in the US
//	com.burbn.boomerang      us=0  ru=0  ae=0   genuinely gone (Meta discontinued it)
//	com.google.meetings      us=0  ru=0  ae=0   genuinely gone (folded into Google Meet)
//
// The first is the fixture that matters. A single-storefront implementation calls it DELISTED
// and looks completely correct on every other row — so this is the app that fails the build when
// the multi-storefront rule is broken. The other two are publicly discontinued apps rather than
// anybody's private library, and they are genuinely absent from every storefront, so the fixture
// can be re-checked by anyone against the live API.
type Fake struct {
	mu sync.Mutex

	// Devices are keyed by udid. Asleep devices are in the map and out of ListDeviceUDIDs,
	// which is exactly the shape reality has: the pairing record persists, mDNS does not.
	devices map[string]*fakeDevice
	// apps by udid.
	apps map[string][]InstalledApp
	// store maps bundle id -> the storefronts it is present in.
	// storeVer is what those storefronts currently sell, so the update path can be exercised.
	store    map[string]map[string]int64
	storeVer map[string]string
	// authed tracks which HOME directories have completed login, and pending2FA which are
	// mid-2FA. `2fa@example.com` is the address that exercises the two-step form.
	authed    map[string]Account
	pending   map[string]bool
	LookupErr map[string]bool
	// Pairing state, so the device page can be exercised without hardware.
	unpaired   map[string]bool
	trustAsked map[string]bool
	wifiOff    map[string]bool
}

type fakeDevice struct {
	Device
	awake bool
}

// NewFake builds the fixture set described on the type.
func NewFake() *Fake {
	const iphone = "00008110-0011223344556677"
	const ipad = "00008120-0089ABCDEF012345"
	const asleep = "00008101-00FEDCBA98765432"
	// The Apple ID every receipt names. A placeholder: the fixture is about the SHAPE of a
	// receipt, not about whose account it came from.
	const owner = "owner@example.com"

	f := &Fake{
		devices: map[string]*fakeDevice{
			iphone: {Device{
				UDID: iphone, Name: "Example iPhone", ProductType: "iPhone17,1",
				IOS: "26.6", Region: "AE/A",
			}, true},
			// LL/A is the fixture that keeps the storefront mapping honest: read naively
			// it produces country=ll, which the live API answers with HTTP 400.
			//
			// AWAKE AND UNPAIRED, which is the state a device is in at the one moment the
			// pairing screen exists for: just plugged in, not yet trusted. Left asleep it
			// could never reach the Pair button, because pairing needs the cable — so the
			// newest and least-exercised screen in the app would be unreachable without
			// hardware, which is the one thing the fake is for.
			ipad: {Device{
				UDID: ipad, Name: "Example iPad", ProductType: "iPad15,7",
				IOS: "26.6", Region: "LL/A",
			}, true},
			// And one that is asleep, so "paired but not currently reachable" is on screen
			// by default too. Devices come and go; that is normal, not an error.
			asleep: {Device{
				UDID: asleep, Name: "Example iPhone (asleep)", ProductType: "iPhone15,2",
				IOS: "26.5", Region: "ZA/A",
			}, false},
		},
		apps: map[string][]InstalledApp{
			// Real, PUBLIC app identifiers — the numeric ids and bundle ids anyone can look
			// up. The delisted ones carry ids too, which is the whole point: the receipt
			// outlives the listing, so Archive never has to ask for a number.
			//
			// storefront `ru` on an `AE/A` device is not a typo. It is the measurement
			// that says the receipt beats the region.
			iphone: {
				{BundleID: "ru.aviasales.app", Version: "9.28", Name: "Aviasales",
					AppID: 358848275, StoreName: "Aviasales", OwnerAppleID: owner, Storefront: "ru"},
				{BundleID: "ru.yandex.mobile.music", Version: "797", Name: "Yandex Music",
					AppID: 599981012, StoreName: "Яндекс Музыка", OwnerAppleID: owner, Storefront: "ru"},
				{BundleID: "com.burbn.boomerang", Version: "1.8", Name: "Boomerang",
					AppID: 6744684419, StoreName: "Boomerang from Instagram", Artist: "Instagram, Inc.", OwnerAppleID: owner, Storefront: "ru"},
				{BundleID: "com.google.meetings", Version: "2.1", Name: "Google Meet (original)",
					AppID: 6742457200, StoreName: "Google Meet (original)", OwnerAppleID: owner, Storefront: "ru"},
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
			asleep: {},
			ipad: {
				{BundleID: "com.google.ios.youtube", Version: "21.31.3", Name: "YouTube",
					AppID: 544007664, StoreName: "YouTube", OwnerAppleID: owner, Storefront: "ru"},
				{BundleID: "com.burbn.boomerang", Version: "1.8", Name: "Boomerang",
					AppID: 6744684419, StoreName: "Boomerang from Instagram", OwnerAppleID: owner, Storefront: "ru"},
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
			// com.burbn.boomerang and com.google.meetings are in NO storefront.
			// Their absence from this map is the fixture.
		},
		// Deliberately AHEAD of the versions the fake devices report, so "your copy is
		// behind" is on screen by default rather than only when someone constructs it.
		storeVer: map[string]string{
			"com.google.ios.youtube":   "21.40.0",
			"com.tinyspeck.chatlyio":   "26.09.01",
			"ru.aviasales.app":         "9.31",
			"ru.yandex.mobile.music":   "801",
			"io.wio.retail":            "1.69.0",
			"com.flydubai.app.booking": "6.8.29",
			"ru.cardsmobile.wallet":    "6.63",
			"info.tapestry.journal":    "5.2.1",
		},
		authed:    map[string]Account{},
		pending:   map[string]bool{},
		LookupErr: map[string]bool{},
		// The iPad starts UNPAIRED so the pairing flow is on screen by default rather than
		// only when someone constructs it.
		unpaired:   map[string]bool{ipad: true},
		trustAsked: map[string]bool{},
		wifiOff:    map[string]bool{},
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
	// Only devices with a record — so unpairing one in the UI takes it out of the paired set,
	// exactly as deleting the .plist does on a real box. Returning every device regardless made
	// Unpair look like it had done nothing.
	out := make([]string, 0, len(f.devices))
	for udid := range f.pairedRecords() {
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
	// The installed size is filled in here rather than written into every fixture line: it is
	// derived from the bundle id, so it is stable across runs, and it is present for the
	// DELISTED apps too — which is the case that has no store size and therefore the only one
	// where this row is the whole answer.
	out := append([]InstalledApp(nil), f.apps[udid]...)
	for i := range out {
		// Slightly larger than the store's number, which is the direction the real
		// relationship runs: the .ipa is compressed, the installed bundle is not.
		out[i].DiskUsage = fakeSize(out[i].BundleID) * 11 / 10
	}
	return out, nil
}

// DeviceIcons draws a flat square per app instead of asking a device for one.
//
// THE FIXTURE DELIBERATELY LEAVES SOME APPS WITHOUT AN ICON. On a real device a handful of
// bundles return nothing, and an implementation only ever exercised against a complete set is one
// where the missing-icon path — the monogram tile, the negative cache entry that stops the warm
// re-running forever — is never run at all. Every fourth app here has no icon, deterministically.
func (f *Fake) DeviceIcons(ctx context.Context, udid string, bundleIDs []string) (map[string][]byte, error) {
	f.mu.Lock()
	d, ok := f.devices[udid]
	f.mu.Unlock()
	if !ok || !d.awake {
		return nil, ErrDeviceUnreachable
	}

	icons := map[string][]byte{}
	for i, b := range bundleIDs {
		if !safeBundleID(b) || i%4 == 3 {
			continue
		}
		png, err := fakeIcon(b)
		if err != nil {
			return nil, err
		}
		icons[b] = png
	}
	return icons, nil
}

// fakeIcon draws one app's tile: a flat square whose colour is DERIVED FROM THE BUNDLE ID, so the
// same app is the same colour on every render and a mis-keyed cache is visible rather than merely
// wrong.
//
// Shared with the archive builder in Download, and that sharing is the point: an app's icon on the
// device and its icon in the library are then the same picture, which is what the precedence rules
// between those two sources are judged against.
func fakeIcon(bundleID string) ([]byte, error) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(bundleID))
	sum := h.Sum32()
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.NRGBA{
		R: byte(sum), G: byte(sum >> 8), B: byte(sum >> 16), A: 0xff,
	}}, image.Point{}, draw.Src)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
		case <-time.After(400 * time.Millisecond):
		}
		if onProgress != nil {
			onProgress(p)
		}
	}

	// THE APP IS NOW ON THE DEVICE, and the fake has to say so. Without this the fake's
	// install was a no-op that reported success, so the UI state it produces — a device row
	// that should stop offering "Install" and start saying "installed" — could not be
	// exercised here at all, and the gap showed up as a row that never changed.
	//
	// The bundle id is read from the .ipa the fake itself wrote, rather than passed in, which
	// is what the real device does too: the package is the source of truth about what it is.
	meta, err := ipa.Read(ipaPath)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.apps[udid] {
		if existing.BundleID == meta.BundleID {
			return nil
		}
	}
	f.apps[udid] = append(f.apps[udid], InstalledApp{
		BundleID:  meta.BundleID,
		Version:   meta.Version,
		Name:      meta.Name,
		AppID:     meta.ItemID,
		StoreName: meta.Name,
		Artist:    meta.Artist,
	})
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
	// A CODE THAT IS REFUSED, because the screen that handles it is the one that gets stuck.
	// Every other outcome here succeeds, so the two-step form could only ever be exercised in
	// its happy state — and the reported problem was what happens after it fails: three filled
	// fields, an error, and no way back. `000000` is that failure, on demand.
	if authCode == "000000" {
		return fmt.Errorf("that verification code was not accepted")
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

		// AND THEN THE SIGNING PASS, because the real one is where the confusing part is.
		// ipatool rewrites the whole archive after the transfer to add the App Store
		// metadata, reporting nothing — which for years looked like a download stuck at 99%.
		// A fake that stops at 100% never renders the screen that explains it.
		for pct := 0; pct <= 100; pct += 12 {
			select {
			case <-ctx.Done():
				return DownloadResult{}, ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
			onProgress(DownloadProgress{Percent: pct, Stage: "signing"})
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
	// The version the fake store sells, so "library is behind the store" and "device is behind
	// the library" produce real numbers instead of a constant 1.0 that reads as a downgrade.
	ver := f.storeVer[bundle]
	if ver == "" {
		ver = "1.0"
	}
	info := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>%s</string>
<key>CFBundleShortVersionString</key><string>%s</string>
<key>CFBundleDisplayName</key><string>%s</string>
</dict></plist>`, bundle, ver, name)
	meta := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>itemId</key><integer>%d</integer>
<key>itemName</key><string>%s</string>
<key>artistName</key><string>springback fake</string>
<key>bundleShortVersionString</key><string>%s</string>
</dict></plist>`, appID, name, ver)

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

	// AN ICON, AT THE BUNDLE ROOT WHERE A REAL ONE LIVES. Without it the library screen is a
	// column of lettered tiles, and — worse for a fixture layer — ipa.Icon is never once run
	// outside its own unit tests: every archive this fake produced was an archive with no icon
	// in it, which is precisely the case the extractor is least interesting on.
	//
	// The same picture the device draws for this bundle, so the library and the device agree.
	icon, err := fakeIcon(bundle)
	if err != nil {
		return DownloadResult{}, err
	}
	iw, err := zw.Create("Payload/" + name + ".app/AppIcon60x60@2x.png")
	if err != nil {
		return DownloadResult{}, err
	}
	if _, err := iw.Write(icon); err != nil {
		return DownloadResult{}, err
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
	// THE INSTALLED APPS ARE THE BEST ANSWER, AND THEY ARE ASKED FIRST. They carry the numeric
	// id, the bundle id and the app's real name together — which the storefront map does not,
	// and which the DELISTED fixtures are missing from entirely, because being in no storefront
	// is what they are for.
	//
	// Asking the store first is what made archiving Boomerang — the delisted app this whole tool
	// exists for — produce a library item called "App6744684419" with the bundle id
	// `com.example.app6744684419`. It matched nothing installed anywhere, so the demo could walk
	// every step of its own headline flow except the one where the archive comes back.
	for _, apps := range f.apps {
		for _, a := range apps {
			if a.AppID == appID && a.AppID != 0 {
				n := a.StoreName
				if n == "" {
					n = a.Name
				}
				return a.BundleID, n
			}
		}
	}
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

// fakeSize gives each bundle a stable, plausible size so the size rows are on screen in dev.
// Derived from the id so it never moves between runs.
func fakeSize(bundleID string) int64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(bundleID))
	return 40<<20 + int64(h.Sum32()%700)<<20
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
		res.Version = f.storeVer[bundleID]
		res.FileSize = fakeSize(bundleID)
		res.ReleaseDate = fakeReleaseDate(bundleID)
	}
	return res
}

// fakeReleaseDate gives each listed app a stable last-updated date, derived from its bundle id so
// it never moves between runs.
//
// SPREAD ACROSS YEARS ON PURPOSE. The date exists on screen to answer "how stale is my copy", and
// a fixture set where everything was updated last Tuesday cannot show the interesting reading: an
// app whose store version has not moved since 2019 is one nobody is maintaining, and the copy on
// this box may be the last one anybody keeps. Fixtures that only demonstrate the boring case are
// how a screen ships looking fine and saying nothing.
func fakeReleaseDate(bundleID string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(bundleID))
	sum := h.Sum32()
	// A day somewhere in the last seven years, at midnight UTC — the shape Apple's
	// currentVersionReleaseDate has.
	day := time.Date(2019, time.January, 1, 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, int(sum%2555))
	return day.Format("2006-01-02T15:04:05Z")
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

// ---------------------------------------------------------------------------
// Pairing and Wi-Fi sync
//
// THE FAKE PAIRS ON THE SECOND ATTEMPT, NOT THE FIRST. A real pairing almost always comes back
// "accept the trust dialog" once, because nobody is holding the phone when the button is pressed
// — so a fake that succeeds immediately exercises only the path that rarely happens, and the
// screen that has to explain the trust dialog is never rendered during development.
// ---------------------------------------------------------------------------

func (f *Fake) PairStatus(ctx context.Context, udid string) (PairState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[udid]
	if !ok {
		return PairUnknown, nil
	}
	if !d.awake {
		// Asleep says nothing about the pairing record, and saying "unpaired" here would
		// offer a Pair button for a device that is merely somewhere else.
		return PairUnknown, nil
	}
	if f.unpaired[udid] {
		return Unpaired, nil
	}
	return Paired, nil
}

func (f *Fake) Pair(ctx context.Context, udid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.devices[udid]; !ok {
		return ErrNeedsUSB
	}
	if !f.trustAsked[udid] {
		f.trustAsked[udid] = true
		return ErrTrustPending
	}
	delete(f.unpaired, udid)
	delete(f.trustAsked, udid)
	return nil
}

func (f *Fake) Unpair(ctx context.Context, udid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.devices[udid]; !ok {
		return ErrNeedsUSB
	}
	f.unpaired[udid] = true
	return nil
}

// PairingWritable is true here: the fake is for exercising the flow, and the read-only case is a
// deployment fact with no device behind it. main.go reports the real answer.
func (f *Fake) PairingWritable() bool { return true }

// PairingKnown is true: the fake's `unpaired` map IS its pairing-record directory, and it is
// always readable. The false case is a missing mount, which has no meaning without a filesystem.
func (f *Fake) PairingKnown() bool { return true }

// PairedUDIDsExcludingUnpaired is what the record directory would contain — every device except
// the ones Unpair has been called on. Kept in step with PairStatus so the fake cannot drift into
// saying a device is unpaired on its page while listing a record for it.
func (f *Fake) pairedRecords() map[string]bool {
	out := map[string]bool{}
	for udid := range f.devices {
		if !f.unpaired[udid] {
			out[udid] = true
		}
	}
	return out
}

func (f *Fake) WifiSync(ctx context.Context, udid string) (WifiSyncState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[udid]
	if !ok || !d.awake {
		return WifiSyncUnknown, nil
	}
	if f.wifiOff[udid] {
		return WifiSyncOff, nil
	}
	return WifiSyncOn, nil
}

func (f *Fake) SetWifiSync(ctx context.Context, udid string, enable bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[udid]
	if !ok || !d.awake {
		return ErrDeviceUnreachable
	}
	if enable {
		delete(f.wifiOff, udid)
	} else {
		f.wifiOff[udid] = true
	}
	return nil
}

// Transport reports the iPhone fixture as being on the cable and everything else on Wi-Fi, so
// both the "pair me" and the "plug me in first" paths are reachable without hardware.
func (f *Fake) Transport(udid string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.devices[udid]; ok && d.awake {
		return "usb"
	}
	return "network"
}
