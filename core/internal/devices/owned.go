package devices

// Everything you own, from the only evidence springback can actually stand behind.
//
// "SEARCH THE APP STORE" IS THE WRONG SHAPE FOR THIS TOOL, and the reason is worth stating because
// it is the obvious feature to build. A download only works for something the account already owns,
// so a store search returns mostly things you cannot have — and it cannot return the delisted app
// you came here for, because a delisted app is in no store by definition. It searches the wrong
// set, and it fails hardest on exactly the case springback exists for.
//
// The set that matters is what you OWN, and springback already holds proof of it: the receipt on
// each device names the app and its numeric id, and it stays true after Apple pulls the listing.
// Union those across every device ever seen — including the ones asleep, in a drawer, or replaced —
// and that is a list nobody can get from Apple's search, assembled from evidence rather than a
// guess.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/novkostya/springback/core/internal/storefront"
	"github.com/novkostya/springback/core/internal/tools"
)

// Sighting is one device's copy of an app.
//
// PER-INSTALLATION FACTS LIVE HERE, NOT ON THE APP. The same app on two devices can have been
// bought by different Apple IDs, from different storefronts, and it occupies different space on
// each — so these belong to the copy, not to the listing. The distinction is not academic: the
// Archive button downloads with the account named on the RECEIPT, and offering the wrong one
// fails with "license not found".
type Sighting struct {
	UDID         string    `json:"udid"`
	DeviceName   string    `json:"device_name,omitempty"`
	Version      string    `json:"version,omitempty"`
	OwnerAppleID string    `json:"owner_apple_id,omitempty"`
	Storefront   string    `json:"storefront,omitempty"`
	DiskUsage    int64     `json:"disk_usage,omitempty"`
	SeenAt       time.Time `json:"seen_at"`
	// Here reports whether that device is reachable RIGHT NOW. It is the difference between
	// "you can install this in a moment" and "this is remembered from an iPad that is not
	// here", and the screen would be misleading without it.
	Here bool `json:"here"`
}

// OwnedApp is one app, wherever it was found.
type OwnedApp struct {
	BundleID string            `json:"bundle_id"`
	Name     string            `json:"name"`
	AppID    int64             `json:"app_id,omitempty"`
	Status   storefront.Status `json:"store_status,omitempty"`
	// The rest of what the STORE says about this app, as of the sighting that recorded it.
	// These are properties of the listing rather than of any one copy, so they sit here.
	//
	// THEY ARE CARRIED SO THE DETAIL PAGE IS THE SAME PAGE whichever list it was opened from.
	// Reported: opened from a device it showed the developer, the owning Apple ID, the storefront
	// and the installed size; opened from here it showed three rows and defaulted the download to
	// the wrong account.
	Artist       string   `json:"artist,omitempty"`
	StoreVersion string   `json:"store_version,omitempty"`
	StoreSize    int64    `json:"store_size,omitempty"`
	StoreUpdated string   `json:"store_updated,omitempty"`
	Checked      []string `json:"checked,omitempty"`
	// InLibrary and LibraryID are recomputed live rather than read from the remembered list:
	// archiving an app is the one thing the user does that changes this answer, and a screen
	// that told them it had not worked would be worse than one that never mentioned it.
	InLibrary bool  `json:"in_library"`
	LibraryID int64 `json:"library_id,omitempty"`
	// Devices is every device this app was seen on, most recently seen first.
	Devices []Sighting `json:"devices"`
	// Archived means the app is in the library and on NO device — the copy on this box is the
	// only one left. That is the state this whole tool is trying to reach, so it is named.
	Archived bool `json:"archived_only"`
}

// Owned is the whole answer, plus what it rests on.
type Owned struct {
	Apps []OwnedApp `json:"apps"`
	// DevicesSeen is how many devices contributed, and LastSeen the most recent sighting of
	// any of them. Both are on screen because this list is assembled from memory, and a claim
	// about what you own should say how old its evidence is.
	DevicesSeen int `json:"devices_seen"`
	// A POINTER, because `omitempty` does nothing for a struct: a zero time.Time marshals as
	// "0001-01-01T00:00:00Z", and the screen would report a library last seen in the year 1
	// rather than not seen at all.
	LastSeen  *time.Time `json:"last_seen,omitempty"`
	Total     int        `json:"total"`
	Delisted  int        `json:"delisted"`
	InLibrary int        `json:"in_library"`
}

// Owned unions every device's remembered app list with the library.
//
// It asks no hardware anything and reaches no network: it is a read of what springback already
// learned, which is what makes it answerable with every device asleep. The device list IS consulted,
// but only to mark which sightings are reachable now — and a failure there degrades the screen
// rather than emptying it.
func (s *Service) Owned(ctx context.Context) (Owned, error) {
	seen, err := s.Seen.All()
	if err != nil {
		return Owned{}, err
	}

	here := map[string]bool{}
	names := map[string]string{}
	if devs, err := s.List(ctx); err == nil {
		for _, d := range devs {
			here[d.UDID] = d.Reachable
			if d.Name != "" {
				names[d.UDID] = d.Name
			}
		}
	}

	inLibrary, err := s.Library.BundleIDs()
	if err != nil {
		inLibrary = map[string]int64{}
	}

	byBundle := map[string]*OwnedApp{}
	for _, dev := range seen {
		var apps []App
		if err := json.Unmarshal(dev.Apps, &apps); err != nil {
			continue
		}
		name := names[dev.UDID]
		if name == "" {
			name = dev.DeviceName
		}
		for _, a := range apps {
			if a.BundleID == "" {
				continue
			}
			o := byBundle[a.BundleID]
			if o == nil {
				o = &OwnedApp{
					BundleID: a.BundleID, Name: appName(a), Status: a.Status,
					Artist: a.Artist, StoreVersion: a.StoreVersion,
					StoreSize: a.StoreSize, StoreUpdated: a.StoreUpdated, Checked: a.Checked,
				}
				byBundle[a.BundleID] = o
			}
			// THE FIRST NON-ZERO ID WINS AND IS NEVER OVERWRITTEN. It comes from a
			// receipt, and a receipt is the only source that still names a delisted app.
			if o.AppID == 0 {
				o.AppID = a.AppID
			}
			// A newer sighting's verdict beats an older one; entries arrive newest first.
			if o.Status == "" {
				o.Status = a.Status
			}
			o.Devices = append(o.Devices, Sighting{
				UDID: dev.UDID, DeviceName: name, Version: a.Version,
				OwnerAppleID: a.OwnerAppleID, Storefront: a.Storefront,
				DiskUsage: a.DiskUsage,
				SeenAt:    dev.SeenAt, Here: here[dev.UDID],
			})
		}
	}

	// Library items that are on no device at all still belong here — in fact they are the
	// point. An app that Apple pulled AND that you have since removed from every device exists
	// nowhere else in the world but this box.
	for bundle, id := range inLibrary {
		if byBundle[bundle] == nil {
			item, err := s.Library.Get(id)
			if err != nil {
				continue
			}
			a := &OwnedApp{BundleID: bundle, Name: item.Name, AppID: id, Archived: true}
			// NO VERDICT UNLESS ONE HAS ACTUALLY BEEN REACHED. This app is in the library
			// and on no device, so nothing in this path has ever looked it up — and the
			// first version assumed DELISTED, on the reasoning that an app you archived and
			// removed everywhere is probably gone.
			//
			// That is a guess wearing the clothes of a measurement, and it fails in the
			// direction that matters: five apps archived from a real library, every one of
			// them still on sale, would each have carried a red DELISTED chip on a screen
			// whose entire value is that the chip means something. A cached answer is used
			// when there is one; otherwise the row simply carries no claim.
			if s.Resolver != nil {
				if res, ok := s.Resolver.Cached(bundle, storefront.Storefronts("")); ok {
					a.Status = res.Status
				}
			}
			byBundle[bundle] = a
		}
	}

	out := Owned{DevicesSeen: len(seen)}
	for _, dev := range seen {
		if out.LastSeen == nil || dev.SeenAt.After(*out.LastSeen) {
			t := dev.SeenAt
			out.LastSeen = &t
		}
	}
	for _, o := range byBundle {
		if id, ok := inLibrary[o.BundleID]; ok {
			o.InLibrary, o.LibraryID = true, id
			if o.AppID == 0 {
				o.AppID = id
			}
			out.InLibrary++
		}
		o.Archived = o.InLibrary && len(o.Devices) == 0
		if o.Status == storefront.Delisted {
			out.Delisted++
		}
		sort.SliceStable(o.Devices, func(i, j int) bool {
			// Reachable devices first — those are the ones something can be done with.
			if o.Devices[i].Here != o.Devices[j].Here {
				return o.Devices[i].Here
			}
			return o.Devices[i].SeenAt.After(o.Devices[j].SeenAt)
		})
		out.Apps = append(out.Apps, *o)
	}

	// Delisted first, because that is what the screen is for, then by name.
	sort.SliceStable(out.Apps, func(i, j int) bool {
		ri, rj := rank(out.Apps[i].Status), rank(out.Apps[j].Status)
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(out.Apps[i].Name) < strings.ToLower(out.Apps[j].Name)
	})
	out.Total = len(out.Apps)
	return out, nil
}

// Rescan asks every device that is HERE for its app list again, then returns the fresh union.
//
// ON THE SERVER, IN ONE REQUEST, rather than as a loop in the browser. A phone that is asked to
// walk four devices would abandon the walk the moment the reader switched tabs or the screen
// locked — leaving some devices refreshed and others not, with nothing to say which. This either
// finishes or reports what it managed.
//
// ONLY DEVICES THAT ARE REACHABLE, and the count of the others is returned rather than hidden: a
// button that claims to refresh everything, on a screen whose whole point is that it remembers
// devices which are elsewhere, must not imply it reached them.
func (s *Service) Rescan(ctx context.Context) (Owned, RescanResult, error) {
	devs, err := s.List(ctx)
	if err != nil {
		return Owned{}, RescanResult{}, err
	}

	var res RescanResult
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, d := range devs {
		res.Total++
		// THE TWO REASONS FOR SKIPPING ARE COUNTED SEPARATELY, because they are different
		// facts and the remedy differs. A device that is elsewhere needs nothing from anyone;
		// an unpaired device is sitting right here and is skipped ON PURPOSE, since asking it
		// anything is what raises the Trust prompt — it needs one tap on its own page.
		// Reporting both as "not here" would be untrue of the second and unhelpful about it.
		if !d.Reachable {
			res.Away++
			continue
		}
		if d.Pair == tools.Unpaired {
			res.Unpaired++
			continue
		}
		wg.Add(1)
		go func(udid string) {
			defer wg.Done()
			// Apps writes the remembered list as a side effect of succeeding, which is
			// the whole point of calling it here.
			_, err := s.Apps(ctx, udid)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				res.Failed = append(res.Failed, udid)
				return
			}
			res.Scanned++
		}(d.UDID)
	}
	wg.Wait()

	owned, err := s.Owned(ctx)
	return owned, res, err
}

// RescanResult is what the button can honestly say afterwards.
type RescanResult struct {
	// Total is every device springback knows of; the rest say what happened to each.
	Total    int `json:"total"`
	Scanned  int `json:"scanned"`
	Away     int `json:"away"`
	Unpaired int `json:"unpaired"`
	// Failed are the ones that were here, paired, and still did not answer — a device that
	// went to sleep mid-scan, usually.
	Failed []string `json:"failed,omitempty"`
}

// appName prefers the store's name for an app over the one the device reports.
//
// They differ more often than you would think — the device carries CFBundleDisplayName ("Boomerang")
// and the store carries the listing's title ("Boomerang from Instagram") — and the store's is the
// one somebody typing into a search box will have in mind.
func appName(a App) string {
	switch {
	case a.StoreName != "":
		return a.StoreName
	case a.Name != "":
		return a.Name
	default:
		return a.BundleID
	}
}
