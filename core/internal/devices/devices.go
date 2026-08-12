// Package devices assembles the Devices screen — the landing page, and the only part of
// springback that is a product rather than plumbing.
package devices

import (
	"context"
	"sort"
	"sync"

	"github.com/novkostya/springback/core/internal/store"
	"github.com/novkostya/springback/core/internal/storefront"
	"github.com/novkostya/springback/core/internal/tools"
)

// Service answers the two device questions: what is paired, and what is on it.
type Service struct {
	// Cache remembers what a reachable device said, so an offline one still has a name.
	// Optional: nil simply means an offline device shows its udid, as it used to.
	Cache    *store.DeviceCache
	Tools    tools.Tools
	Resolver *storefront.Resolver
	Library  *store.Library
}

// App is one installed app plus the verdict on it.
type App struct {
	tools.InstalledApp
	Status  storefront.Status `json:"store_status"`
	Checked []string          `json:"checked,omitempty"`
	Errors  []string          `json:"errors,omitempty"`
	// AppID is the numeric App Store id when it is known — from the storefront that still
	// sells the app, or from the library if this app has already been archived once.
	//
	// ZERO IS THE INTERESTING CASE. A delisted app is in no storefront, so nothing can look
	// its id up, and installation_proxy does not carry one either (measured against a live
	// iPhone: requesting the ITunesMetadata attribute returns nothing). Zero here is what
	// makes the Archive button ask for the id once, per SPEC §4.
	AppID int64 `json:"app_id,omitempty"`
	// StoreVersion is what the App Store currently sells, when it is listed. Compared with the
	// library copy to tell whether an archived app has fallen behind.
	StoreVersion string `json:"store_version,omitempty"`
	// StoreSize is the download size the store reports, in bytes. Zero for a delisted app.
	StoreSize int64 `json:"store_size,omitempty"`
	// InLibrary — already downloaded, so the button says so instead of offering to fetch the
	// same ~500 MB again.
	InLibrary bool `json:"in_library"`
}

// DeviceApps is the per-device answer.
type DeviceApps struct {
	Device tools.Device `json:"device"`
	Apps   []App        `json:"apps"`
	// Storefronts is which stores were queried. Shown in the UI because "DELISTED" is a
	// claim about the world, and the user deserves to see what it rests on.
	Storefronts []string `json:"storefronts"`
	Total       int      `json:"total"`
	Delisted    int      `json:"delisted"`
	Unknown     int      `json:"unknown"`
	// NotListed — apps that were never public listings (B2B custom, factory installs). Counted
	// separately so they cannot inflate the number the whole screen is about.
	NotListed int `json:"not_listed"`
}

// List returns every device this host is paired with, reachable or not.
//
// The union of two sources, and both halves matter. `idevice_id -n` gives what is awake right
// now; the pairing records give what exists at all. Without the records a sleeping iPhone simply
// disappears from the screen, which is the "gone" reading SPEC §3 explicitly forbids — devices
// come and go, and that is normal, not an error.
func (s *Service) List(ctx context.Context) ([]tools.Device, error) {
	reachable, err := s.Tools.ListDeviceUDIDs(ctx)
	if err != nil {
		return nil, err
	}
	awake := make(map[string]bool, len(reachable))
	for _, u := range reachable {
		awake[u] = true
	}

	paired, err := s.Tools.PairedUDIDs(ctx)
	if err != nil {
		return nil, err
	}
	// hasRecord is the pairing state for every row, free: the records have just been read, and
	// nothing about them needs the device. Only meaningful when the records are readable at
	// all — see PairingKnown, and the comment below about what "no record" means when they
	// are not.
	recordsKnown := s.Tools.PairingKnown()
	hasRecord := make(map[string]bool, len(paired))
	for _, u := range paired {
		hasRecord[u] = true
	}

	seen := map[string]bool{}
	var all []string
	for _, u := range append(append([]string{}, reachable...), paired...) {
		if !seen[u] {
			seen[u] = true
			all = append(all, u)
		}
	}

	out := make([]tools.Device, len(all))
	var wg sync.WaitGroup
	for i, udid := range all {
		wg.Add(1)
		go func(i int, udid string) {
			defer wg.Done()
			d := tools.Device{UDID: udid, Reachable: awake[udid]}

			// THE PAIRING STATE COMES FIRST, BECAUSE IT DECIDES WHETHER THE DEVICE MAY BE
			// TOUCHED AT ALL. Every read below is a lockdown session, and a lockdown session
			// with no pairing record PAIRS — libimobiledevice does the handshake to answer
			// even a read-only question, and the handshake makes the phone ask "Trust This
			// Computer?". Asking four keys of every reachable device every five seconds meant
			// plugging a phone in raised a trust prompt nobody asked for. Pairing has a
			// button; nothing else may start one.
			//
			// Unknown when the records cannot be read: then "no record" is not a fact about
			// the device, and refusing every device over a missing mount would be worse than
			// the prompt.
			switch {
			case !recordsKnown:
				d.Pair = tools.PairUnknown
			case hasRecord[udid]:
				d.Pair = tools.Paired
			default:
				d.Pair = tools.Unpaired
			}

			if d.Reachable && d.Pair != tools.Unpaired {
				// Only an awake device can be asked — every one of these is a lockdown
				// read and lockdown needs the device.
				d.Name, _ = s.Tools.DeviceValue(ctx, udid, "DeviceName")
				d.ProductType, _ = s.Tools.DeviceValue(ctx, udid, "ProductType")
				d.IOS, _ = s.Tools.DeviceValue(ctx, udid, "ProductVersion")
				d.Region, _ = s.Tools.DeviceValue(ctx, udid, "RegionInfo")
				if s.Cache != nil {
					s.Cache.Remember(udid, store.DeviceFacts{
						Name: d.Name, ProductType: d.ProductType, IOS: d.IOS, Region: d.Region,
					})
				}
			} else if s.Cache != nil {
				// AND FOR AN OFFLINE DEVICE, WHAT IT SAID LAST TIME. Without this the
				// udid is all there is, so a phone in someone's pocket renders as forty
				// characters of hex with no model and no version — which reads as a
				// fault rather than as a device that is simply elsewhere.
				if f, ok := s.Cache.Recall(udid); ok {
					d.Name, d.ProductType, d.IOS, d.Region = f.Name, f.ProductType, f.IOS, f.Region
					d.LastSeen = f.LastSeen
				}
			}
			d.Model = ModelName(d.ProductType)
			out[i] = d
		}(i, udid)
	}
	wg.Wait()

	sort.Slice(out, func(i, j int) bool {
		// Reachable devices first — they are the ones that can be acted on.
		if out[i].Reachable != out[j].Reachable {
			return out[i].Reachable
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Get returns one device's facts.
//
// Built out of List rather than asking the device directly, so the reachable/asleep rule lives in
// exactly one place. A device that is asleep is still a device: it has a pairing record and a
// page, and the page says it is not answering rather than pretending it does not exist.
func (s *Service) Get(ctx context.Context, udid string) (tools.Device, error) {
	all, err := s.List(ctx)
	if err != nil {
		return tools.Device{}, err
	}
	for _, d := range all {
		if d.UDID == udid {
			return d, nil
		}
	}
	// Not in the list at all: no pairing record and not currently visible. Still worth a page —
	// this is exactly the device someone has just plugged in to pair, or one that was unpaired
	// while its page was open.
	//
	// THE CACHE STILL KNOWS WHAT IT IS CALLED, and using it here is what stops the page becoming
	// forty characters of hex the moment the device leaves the list. The Devices list has always
	// done this for an offline device; a page reached for the same device should not be a
	// worse-informed view of it. Reported as two screens disagreeing about the same phone.
	d := tools.Device{UDID: udid}
	if s.Cache != nil {
		if f, ok := s.Cache.Recall(udid); ok {
			d.Name, d.ProductType, d.IOS, d.Region = f.Name, f.ProductType, f.IOS, f.Region
			d.LastSeen = f.LastSeen
			d.Model = ModelName(d.ProductType)
		}
	}
	return d, nil
}

// Apps lists a device's user apps with a store status each.
//
// Delisted first, then unknown, then available: the whole point of the screen is the handful of
// apps at risk among 162 that are fine, and making the user scroll for them would be a strange
// way to build a tool whose only job is surfacing them.
func (s *Service) Apps(ctx context.Context, udid string) (DeviceApps, error) {
	devs, err := s.List(ctx)
	if err != nil {
		return DeviceApps{}, err
	}
	var dev tools.Device
	for _, d := range devs {
		if d.UDID == udid {
			dev = d
			break
		}
	}
	if dev.UDID == "" {
		dev = tools.Device{UDID: udid}
	}
	if !dev.Reachable {
		return DeviceApps{Device: dev}, tools.ErrDeviceUnreachable
	}

	installed, err := s.Tools.ListApps(ctx, udid)
	if err != nil {
		return DeviceApps{Device: dev}, err
	}

	fronts := storefront.Storefronts(dev.Region)
	inLibrary, err := s.Library.BundleIDs()
	if err != nil {
		inLibrary = map[string]int64{}
	}

	// DID THIS DEVICE GIVE US RECEIPTS AT ALL? The question decides what "no receipt" means
	// for a single app, and the two readings are opposite:
	//
	//   - Receipts came back for other apps, so this device does report them. An app without
	//     one was never bought from the App Store — sideloaded, developer-signed — and
	//     "not in any store" is the expected answer, not a finding.
	//   - No app on this device has one, so the receipt path did not work here at all (the
	//     CSV fallback, an older iOS). Then a missing receipt says nothing about any
	//     individual app, and judging by storefront alone is the best available answer.
	//
	// Without this distinction the first reading is unavailable and a sideloaded build gets a
	// confident DELISTED — caught on the fake's own fixtures, where "Sideloaded Thing" was
	// being reported as an app the App Store had pulled.
	haveReceipts := false
	for _, ia := range installed {
		if ia.AppID != 0 {
			haveReceipts = true
			break
		}
	}

	apps := make([]App, len(installed))
	var wg sync.WaitGroup
	for i, ia := range installed {
		wg.Add(1)
		go func(i int, ia tools.InstalledApp) {
			defer wg.Done()

			// Query the storefront the app's own receipt names, on top of the floor.
			// The receipt is authoritative about where the app came from; the device's
			// region is not.
			appFronts := storefront.ForApp(dev.Region, ia.Storefront)

			a := App{InstalledApp: ia, AppID: ia.AppID}

			// A B2B custom app or a factory install was never a public listing, so
			// asking every storefront about it would produce a confident DELISTED for
			// an app that was never on sale. Answer the question that was actually
			// asked instead of the one the lookup can answer.
			// Never a public listing, either because the receipt says so (B2B, factory)
			// or because there is no receipt on a device that supplies them.
			if ia.NotPublic || (haveReceipts && ia.AppID == 0) {
				a.Status = storefront.NotListed
				apps[i] = a
				return
			}

			res := s.Resolver.Resolve(ctx, ia.BundleID, appFronts)
			a.Status, a.Checked, a.Errors = res.Status, res.Checked, res.Errors
			a.StoreVersion = res.Version
			a.StoreSize = res.FileSize
			// The receipt's id wins: it is the id of the app INSTALLED HERE. A
			// storefront match is a good cross-check but can name a different edition.
			if a.AppID == 0 {
				a.AppID = res.TrackID
			}
			if id, ok := inLibrary[ia.BundleID]; ok {
				a.InLibrary = true
				if a.AppID == 0 {
					a.AppID = id
				}
			}
			apps[i] = a
		}(i, ia)
	}
	wg.Wait()

	out := DeviceApps{Device: dev, Apps: apps, Storefronts: fronts, Total: len(apps)}
	for _, a := range apps {
		switch a.Status {
		case storefront.Delisted:
			out.Delisted++
		case storefront.Unknown:
			out.Unknown++
		case storefront.NotListed:
			out.NotListed++
		}
	}

	sort.SliceStable(out.Apps, func(i, j int) bool {
		ri, rj := rank(out.Apps[i].Status), rank(out.Apps[j].Status)
		if ri != rj {
			return ri < rj
		}
		return out.Apps[i].Name < out.Apps[j].Name
	})
	return out, nil
}

func rank(s storefront.Status) int {
	switch s {
	case storefront.Delisted:
		return 0
	case storefront.Unknown:
		return 1
	default:
		return 2
	}
}
