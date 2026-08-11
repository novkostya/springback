package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// safeBundleID reports whether a bundle id can be used as a filename.
//
// The ids come off the device, and sbicon builds an output path out of each one. Reverse-DNS ids
// are letters, digits, dots and hyphens, so this rejects nothing real — but a bundle id
// containing a slash or a leading dot would be a device deciding where this process writes, and
// that is not a decision a device gets to make.
func safeBundleID(id string) bool {
	if id == "" || len(id) > 255 || strings.HasPrefix(id, ".") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// DeviceIcons runs the sbicon helper and reads back what it wrote.
//
// A TEMPORARY DIRECTORY RATHER THAN A PIPE. The helper could frame the images onto stdout, but
// that would be a wire format invented for one caller and debuggable only by this code; files in
// a directory can be listed, opened and looked at by a person trying to work out why an icon is
// missing. The directory is this process's own, so nothing outside can read the icons out of it.
func (r *Real) DeviceIcons(ctx context.Context, udid string, bundleIDs []string) (map[string][]byte, error) {
	wanted := make([]string, 0, len(bundleIDs))
	for _, b := range bundleIDs {
		if safeBundleID(b) {
			wanted = append(wanted, b)
		}
	}
	if len(wanted) == 0 {
		return map[string][]byte{}, nil
	}

	dir, err := os.MkdirTemp("", "sbicon-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// Scaled to the batch: the connection dominates, and each icon after it is milliseconds.
	// Generous enough that a slow device does not lose a whole warm, bounded so a device that
	// stops answering mid-run cannot pin the helper open.
	timeout := r.DeviceTimeout + time.Duration(len(wanted))*250*time.Millisecond
	args := append([]string{udid, dir}, wanted...)
	out, err := r.run(ctx, timeout, r.deviceEnv(), "", "sbicon", args...)
	if err != nil {
		return nil, fmt.Errorf("sbicon: %w: %s", err, strings.TrimSpace(out))
	}

	icons := make(map[string][]byte, len(wanted))
	for _, b := range wanted {
		png, err := os.ReadFile(filepath.Join(dir, b+".png"))
		if err != nil || len(png) == 0 {
			// Absent is the normal way an app with no icon reports itself. The helper has
			// already said so on stderr.
			continue
		}
		icons[b] = png
	}
	return icons, nil
}
