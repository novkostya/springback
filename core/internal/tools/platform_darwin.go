//go:build darwin

package tools

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// See platform_linux.go for what these are and why they are the only two.

// The BSD spelling of the terminal ioctls.
const (
	ioctlGetTermios uint = unix.TIOCGETA
	ioctlSetTermios uint = unix.TIOCSETA
)

// toolPATH PUTS THE BUNDLE'S OWN COPIES FIRST, and that is the whole reason to ship a .app rather
// than a brew formula: ipatool, ideviceinstaller and the libimobiledevice CLIs sit in
// Contents/MacOS beside this binary with their dylibs in Contents/Frameworks, so installing
// springback installs nothing else, and removing it leaves nothing behind.
//
// Homebrew's prefixes stay on the end as a DEVELOPMENT fallback, so the bare cross-compiled binary
// run out of a build directory — before there is a bundle around it — still finds tools and works.
// Ordered so the bundle always wins: a machine with both must not silently run a version of
// ipatool this build was never tested against.
var toolPATH = buildToolPATH()

func buildToolPATH() string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		// Resolved, because a .app is routinely reached through a symlink and the tools sit
		// beside the REAL binary rather than beside the link.
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		dirs = append(dirs, filepath.Dir(exe))
	}
	// /opt/homebrew on Apple silicon, /usr/local on Intel. Neither is on a default PATH.
	dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin")
	return "PATH=" + strings.Join(dirs, ":")
}
