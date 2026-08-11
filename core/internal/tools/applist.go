package tools

import (
	"strings"

	"howett.net/plist"
)

// THE NUMERIC APP STORE ID IS ON THE DEVICE, and finding it removes the worst thing about this
// tool: SPEC §4's "manual-entry cost is one-time per delisted app".
//
// It is reached by asking installation_proxy for the `iTunesMetadata` attribute explicitly —
// it is NOT in the default attribute set, so a plain `list --user` shows no sign of it. The
// spelling is load-bearing and cost this project a wrong conclusion: `-a iTunesMetadata` returns
// the blob, `-a ITunesMetadata` returns an empty dict, and the second one is what was tried
// first. "The device does not carry the number" was written down as measured fact on the
// strength of one capital letter.
//
// The value is a base64 <data> field whose contents are THEMSELVES a plist — the same
// iTunesMetadata.plist that sits at the root of an .ipa. Verified against all 162 apps on a live
// iPhone (2026-08-11): every one carried it, including every app the store no longer lists.
//
// What it gives, and what each is worth:
//
//	itemId                  the numeric App Store id. Archive becomes one click, always.
//	itemName / artistName   the STORE's name for the app, which outlives the listing.
//	AppleID                 which Apple ID bought it — so springback can pick the right
//	                        account instead of failing with "license not found".
//	storefrontCountryCode   the storefront it was actually bought from. Authoritative, and
//	                        measured to differ from the device's region: an AE/A iPhone whose
//	                        apps all came from `ru`.
//	isB2BCustomApp          a custom B2B app was never a public listing, so "not in any store"
//	isFactoryInstall        is the expected answer for it rather than evidence of anything.
type itunesMetadata struct {
	ItemID       int64  `plist:"itemId"`
	ItemName     string `plist:"itemName"`
	ArtistName   string `plist:"artistName"`
	Storefront   string `plist:"storefrontCountryCode"`
	Kind         string `plist:"kind"`
	PurchaseDate string `plist:"purchaseDate"`
	// THE OWNING APPLE ID IS THREE LEVELS DOWN:
	//
	//	com.apple.iTunesStore.downloadInfo -> accountInfo -> AppleID
	//
	// Both shallower spellings were tried first and both returned empty, which is the whole
	// hazard of decoding plists into structs: a key that is not where you looked produces a
	// zero value, not an error. The blank owner column had nothing in it to explain itself.
	// Read the nesting off the file rather than guessing at it.
	DownloadInfo   downloadInfo `plist:"com.apple.iTunesStore.downloadInfo"`
	IsB2BCustomApp bool         `plist:"isB2BCustomApp"`
	IsFactory      bool         `plist:"isFactoryInstall"`
}

type downloadInfo struct {
	AccountInfo accountInfo `plist:"accountInfo"`
}

type accountInfo struct {
	AppleID    string `plist:"AppleID"`
	DSPersonID int64  `plist:"DSPersonID"`
}

// installedAppPlist is one entry of `ideviceinstaller list --user --xml`.
type installedAppPlist struct {
	BundleID    string `plist:"CFBundleIdentifier"`
	Version     string `plist:"CFBundleShortVersionString"`
	DisplayName string `plist:"CFBundleDisplayName"`
	Name        string `plist:"CFBundleName"`
	// Metadata is the nested plist described above. A device or iOS version that does not
	// return it leaves this empty, and everything still works — just without the numeric id.
	Metadata []byte `plist:"iTunesMetadata"`
}

// parseAppListXML reads the plist form of the installed-app list.
func parseAppListXML(out string) []InstalledApp {
	// ideviceinstaller prints its own chatter before the plist on some paths; start at the
	// XML declaration rather than assuming the output is pure plist.
	if i := strings.Index(out, "<?xml"); i > 0 {
		out = out[i:]
	}
	var entries []installedAppPlist
	if _, err := plist.Unmarshal([]byte(out), &entries); err != nil {
		return nil
	}

	apps := make([]InstalledApp, 0, len(entries))
	for _, e := range entries {
		if e.BundleID == "" {
			continue
		}
		app := InstalledApp{
			BundleID: e.BundleID,
			Version:  e.Version,
			Name:     e.DisplayName,
		}
		if app.Name == "" {
			app.Name = e.Name
		}

		if len(e.Metadata) > 0 {
			var m itunesMetadata
			if _, err := plist.Unmarshal(e.Metadata, &m); err == nil {
				app.AppID = m.ItemID
				app.OwnerAppleID = m.DownloadInfo.AccountInfo.AppleID
				app.Storefront = strings.ToLower(strings.TrimSpace(m.Storefront))
				app.Artist = m.ArtistName
				app.PurchaseDate = m.PurchaseDate
				// NOT A PUBLIC LISTING BY CONSTRUCTION. A B2B custom app or a
				// factory install is not sold in any store, so finding it in no
				// store says nothing about it having been pulled.
				app.NotPublic = m.IsB2BCustomApp || m.IsFactory
				// The store's own name outlives the listing, and is what the user
				// went looking for. It beats CFBundleDisplayName, which is whatever
				// fits under an icon.
				if m.ItemName != "" {
					app.StoreName = m.ItemName
				}
			}
		}

		if app.Name == "" {
			app.Name = app.BundleID
		}
		apps = append(apps, app)
	}
	return apps
}
