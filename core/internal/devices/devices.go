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
			if d.Reachable {
				// Only an awake device can be asked. For a sleeping one the udid is
				// all there is, and the UI names it by that.
				d.Name, _ = s.Tools.DeviceValue(ctx, udid, "DeviceName")
				d.ProductType, _ = s.Tools.DeviceValue(ctx, udid, "ProductType")
				d.IOS, _ = s.Tools.DeviceValue(ctx, udid, "ProductVersion")
				d.Region, _ = s.Tools.DeviceValue(ctx, udid, "RegionInfo")
			}
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
			if ia.NotPublic {
				a.Status = storefront.NotListed
				apps[i] = a
				return
			}

			res := s.Resolver.Resolve(ctx, ia.BundleID, appFronts)
			a.Status, a.Checked, a.Errors = res.Status, res.Checked, res.Errors
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
