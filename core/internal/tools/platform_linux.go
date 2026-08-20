//go:build linux

package tools

import "golang.org/x/sys/unix"

// The two things about shelling out that are not the same on every kernel.
//
// Everything else in this package is portable because it shells out: the CLIs absorb the
// platform differences, and springback only ever builds an argv. These two leak through anyway
// — one is an ioctl number, the other is where a package manager puts a binary.

// Terminal ioctls: Linux spells them TCGETS/TCSETS, the BSDs TIOCGETA/TIOCSETA. Only the request
// numbers differ, so disableEcho itself is shared.
const (
	ioctlGetTermios uint = unix.TCGETS
	ioctlSetTermios uint = unix.TCSETS
)

// toolPATH is the PATH handed to every child. Deliberately not inherited: springback runs as a
// container's PID 1 with whatever environment the runtime happened to give it, and the tools it
// needs are installed at known places by its own Dockerfile.
const toolPATH = "PATH=/usr/local/bin:/usr/bin:/bin"
