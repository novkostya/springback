// Package ipa reads an .ipa's own description of itself.
//
// SPEC §4: meta.json is written from the .ipa itself after download — "read them, do not trust
// what the user typed". The numeric id is typed in by hand for delisted apps, and a typo there
// would otherwise propagate into the library as a wrong name and a wrong bundle id, on the one
// copy of an app that cannot be re-fetched by searching for it.
package ipa

import (
	"archive/zip"
	"fmt"
	"io"
	"path"
	"strings"

	"howett.net/plist"
)

// Meta is what the archive says about itself.
type Meta struct {
	BundleID string
	Name     string
	Version  string
	ItemID   int64
	Artist   string
}

// infoPlist is the subset of Payload/*.app/Info.plist springback reads.
type infoPlist struct {
	BundleID    string `plist:"CFBundleIdentifier"`
	Version     string `plist:"CFBundleShortVersionString"`
	DisplayName string `plist:"CFBundleDisplayName"`
	Name        string `plist:"CFBundleName"`
}

// itunesPlist is the subset of the archive-root iTunesMetadata.plist springback reads.
type itunesPlist struct {
	ItemID   int64  `plist:"itemId"`
	ItemName string `plist:"itemName"`
	Artist   string `plist:"artistName"`
	Version  string `plist:"bundleShortVersionString"`
}

// Read opens an .ipa and extracts what SPEC §4 names.
//
// Both files are optional in the sense that a readable archive missing one of them still yields
// something useful. What is NOT optional is the archive being readable: a truncated download
// that produced an unopenable zip must fail here rather than land in the library as an entry
// that looks fine until the day someone tries to install it.
func Read(ipaPath string) (Meta, error) {
	zr, err := zip.OpenReader(ipaPath)
	if err != nil {
		return Meta{}, fmt.Errorf("open ipa: %w", err)
	}
	defer func() { _ = zr.Close() }()

	var m Meta

	for _, f := range zr.File {
		name := path.Clean(f.Name)
		switch {
		case name == "iTunesMetadata.plist":
			var it itunesPlist
			if err := decodePlist(f, &it); err == nil {
				m.ItemID = it.ItemID
				m.Artist = it.Artist
				if it.ItemName != "" {
					m.Name = it.ItemName
				}
				if m.Version == "" {
					m.Version = it.Version
				}
			}
		case isAppInfoPlist(name):
			var ip infoPlist
			if err := decodePlist(f, &ip); err == nil {
				m.BundleID = ip.BundleID
				if ip.Version != "" {
					m.Version = ip.Version
				}
				// Prefer the display name; fall back to CFBundleName. iTunesMetadata's
				// itemName wins over both when present, since it is the store's own
				// name for the thing and is what the user went looking for.
				if m.Name == "" {
					if ip.DisplayName != "" {
						m.Name = ip.DisplayName
					} else {
						m.Name = ip.Name
					}
				}
			}
		}
	}

	if m.BundleID == "" && m.ItemID == 0 {
		return m, fmt.Errorf("%s: neither Info.plist nor iTunesMetadata.plist could be read — not an ipa, or a truncated download", path.Base(ipaPath))
	}
	return m, nil
}

// isAppInfoPlist matches Payload/<something>.app/Info.plist and nothing deeper. The nested
// Info.plist files inside embedded frameworks and app extensions describe those components, not
// the app, and matching one of those would put a framework's bundle id in the library.
func isAppInfoPlist(name string) bool {
	if !strings.HasPrefix(name, "Payload/") || !strings.HasSuffix(name, "/Info.plist") {
		return false
	}
	parts := strings.Split(name, "/")
	return len(parts) == 3 && strings.HasSuffix(parts[1], ".app")
}

func decodePlist(f *zip.File, v any) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()

	// plist decoding needs a ReaderAt, and these are small files (a few KB) inside an archive
	// that is ~500 MB, so reading one into memory is the cheap option. Bounded anyway: a
	// hostile or corrupt archive must not be able to claim an Info.plist is a gigabyte.
	buf, err := io.ReadAll(io.LimitReader(rc, 8<<20))
	if err != nil {
		return err
	}
	// Both binary and XML plists appear in real archives; the decoder sniffs the format.
	_, err = plist.Unmarshal(buf, v)
	return err
}
