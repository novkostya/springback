package ipa

// Icon extraction — where a delisted app's artwork actually comes from.
//
// The obvious source is the iTunes lookup API's artworkUrl512, and it is exactly backwards for
// this tool: the lookup only answers for apps that are STILL LISTED, so the apps springback
// exists to preserve are precisely the ones it returns nothing for. A library of grey placeholders
// with a handful of icons on the apps that need springback least is worse than none.
//
// The .ipa has no such problem. It is the app: whatever iOS draws on the home screen is a file
// inside the bundle, it was downloaded before the listing was pulled, and it keeps working after.
//
// (The third candidate, sbservices_get_icon_pngdata, reads the icon off the device itself and
// would cover apps that are installed but not archived. No shipped libimobiledevice CLI exposes
// it, so it would mean adding a helper binary to the image — worth revisiting for the Devices
// screen, but not for the library, where the archive is already on disk.)

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// ErrNoIcon means the archive was read fine and contains nothing usable as an icon. It is a
// normal outcome, not a failure: the caller shows a placeholder and does not retry.
var ErrNoIcon = errors.New("no icon in archive")

// maxIconBytes caps what is read out of the archive. An icon is a few tens of KB; anything
// wildly past that is not one, and this is a zip whose entry sizes are attacker-controlled in
// the sense that they come off the internet.
const maxIconBytes = 4 << 20

// Icon returns the app's home-screen icon as a PNG that any browser can render.
//
// Opening the archive is cheap even at 800 MB — zip reads the central directory at the end of
// the file, so this seeks rather than scans, and only the chosen icon is inflated.
func Icon(ipaPath string) ([]byte, error) {
	zr, err := zip.OpenReader(ipaPath)
	if err != nil {
		return nil, fmt.Errorf("open ipa: %w", err)
	}
	defer func() { _ = zr.Close() }()

	// Collect the bundle-root files and the declared icon names in one pass.
	var (
		bundleRoot string
		rootFiles  = map[string]*zip.File{}
		declared   []string
	)
	for _, f := range zr.File {
		name := path.Clean(f.Name)
		if !strings.HasPrefix(name, "Payload/") {
			continue
		}
		parts := strings.Split(name, "/")
		// Exactly Payload/<X>.app/<file> — nothing inside PlugIns, Frameworks or a nested
		// .bundle. An app extension has its own icons, and picking one of those would put the
		// widget's artwork on the app.
		if len(parts) != 3 || !strings.HasSuffix(parts[1], ".app") {
			continue
		}
		if bundleRoot != "" && parts[1] != bundleRoot {
			continue
		}
		bundleRoot = parts[1]

		if parts[2] == "Info.plist" {
			var ip infoPlist
			if err := decodePlist(f, &ip); err == nil {
				declared = ip.iconBaseNames()
			}
			continue
		}
		if strings.EqualFold(path.Ext(parts[2]), ".png") {
			rootFiles[parts[2]] = f
		}
	}
	if len(rootFiles) == 0 {
		return nil, ErrNoIcon
	}

	best := pickIcon(rootFiles, declared)
	if best == nil {
		return nil, ErrNoIcon
	}

	raw, err := readZipFile(best, maxIconBytes)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", best.Name, err)
	}
	out, err := normalizePNG(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path.Base(best.Name), err)
	}
	return out, nil
}

// iconBaseNames returns the icon file stems Info.plist declares, best first.
//
// THE NAME IS NOT "AppIcon". That is only Xcode's default, and assuming it silently loses the
// icon for anything that renamed its asset — measured here on an app whose primary icon is
// `ToastmasterMajorSymbol60x60@2x.png`. The plist is the only authority on which of a bundle's
// PNGs iOS actually draws.
func (ip *infoPlist) iconBaseNames() []string {
	var out []string
	add := func(names []string) {
		for _, n := range names {
			if n != "" {
				out = append(out, n)
			}
		}
	}
	// iPhone's set first: its @3x variants are the largest artwork most bundles carry.
	add(ip.Icons.Primary.Files)
	add(ip.IconsIPad.Primary.Files)
	add(ip.IconFiles)
	if ip.IconFile != "" {
		add([]string{strings.TrimSuffix(ip.IconFile, ".png")})
	}
	return out
}

// sizeInName pulls the rendered pixel size out of an icon filename: "AppIcon60x60@2x.png" is
// 120 px, "AppIcon76x76@2x~ipad.png" is 152. Returns 0 when the name says nothing.
var iconDims = regexp.MustCompile(`(\d+)x(\d+)`)
var iconScale = regexp.MustCompile(`@(\d+)x`)

func sizeInName(name string) int {
	m := iconDims.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0
	}
	scale := 1
	if s := iconScale.FindStringSubmatch(name); s != nil {
		if v, err := strconv.Atoi(s[1]); err == nil && v > 0 {
			scale = v
		}
	}
	return n * scale
}

// pickIcon chooses the largest icon among the declared names, falling back to the Xcode default
// stem when the plist named nothing usable.
//
// Largest wins because the grid is drawn on a 3x phone screen: a 120 px icon in a 60 pt cell is
// visibly soft, and the 76x76@2x~ipad variant that most bundles also carry is 152 px for free.
// Upscaling is never worth it, so there is no reason to prefer the "right" idiom over the big one.
func pickIcon(rootFiles map[string]*zip.File, declared []string) *zip.File {
	stems := declared
	if len(stems) == 0 {
		stems = []string{"AppIcon"}
	}

	var best *zip.File
	bestSize := -1
	for name, f := range rootFiles {
		stem := strings.TrimSuffix(name, path.Ext(name))
		matched := false
		for _, s := range stems {
			// Prefix rather than equality: the plist declares a stem ("AppIcon60x60") and
			// the bundle holds its scale and idiom variants ("AppIcon60x60@2x~ipad.png").
			if strings.HasPrefix(strings.ToLower(stem), strings.ToLower(s)) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		size := sizeInName(name)
		if size > bestSize {
			best, bestSize = f, size
		}
	}
	return best
}

func readZipFile(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(io.LimitReader(rc, limit))
}
