# Credits and third-party licences

springback is a thin thing standing on some very good work. It does not talk to an iPhone or to
Apple by itself — it drives other people's tools and reads the results. This file names them, and
records the licence position, because the container image ships several of them.

springback's own code is [MIT](LICENSE).

---

## The tools the image carries

| Project | What it does here | Licence | Pinned at |
|---|---|---|---|
| [ipatool](https://github.com/majd/ipatool) | Signs in to Apple's Store API and downloads apps you own | MIT | `versions.env` → `IPATOOL_REF` |
| [libimobiledevice](https://github.com/libimobiledevice/libimobiledevice) | Talks to devices: `idevice_id`, `ideviceinfo`, `idevicepair` | LGPL-2.1-or-later | Alpine package |
| [ideviceinstaller](https://github.com/libimobiledevice/ideviceinstaller) | Lists installed apps and installs `.ipa` files | **GPL-2.0** | `versions.env` → `IDEVICEINSTALLER_REF` |
| [libplist](https://github.com/libimobiledevice/libplist) | Reads Apple's property lists | LGPL-2.1-or-later (library) | Alpine package |
| [libimobiledevice-glue](https://github.com/libimobiledevice/libimobiledevice-glue), [libusbmuxd](https://github.com/libimobiledevice/libusbmuxd), [libtatsu](https://github.com/libimobiledevice/libtatsu) | Support libraries for the above | LGPL-2.1 | Alpine packages |
| [libzip](https://libzip.org/) | `ideviceinstaller`'s archive handling | BSD-3-Clause | Alpine package |
| [Alpine Linux](https://alpinelinux.org/) | Base image; musl is MIT, zlib is Zlib | various permissive | `versions.env` → `ALPINE_VERSION` |

## Go libraries compiled into the binary

| Module | What it does here | Licence |
|---|---|---|
| [howett.net/plist](https://github.com/DHowett/go-plist) | Parses the `iTunesMetadata` receipts that make one-tap Archive possible | BSD-2-Clause |
| [github.com/creack/pty](https://github.com/creack/pty) | Gives ipatool a terminal, so it prompts for 2FA and prints a progress bar | MIT |
| [github.com/coder/websocket](https://github.com/coder/websocket) | The event socket that pushes device and job changes to the browser | ISC |
| [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto) | argon2id, for the one password | BSD-3-Clause |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | Terminal control while driving ipatool | BSD-3-Clause |

That is the whole list — `go list -deps` on the binary returns nothing else. No web framework, no
bundler, no CSS library: the UI is three hand-written files. (ISC is the two-clause permissive
licence OpenBSD uses; it imposes nothing MIT does not.)

---

## The licence position, stated plainly

**springback's own code is MIT and does not link anything copyleft into itself.** ipatool,
ideviceinstaller and the libimobiledevice CLIs are invoked as separate programs — springback
builds an argv, runs it, and reads the output. Every one of those calls goes through a single
interface (`core/internal/tools`), which is where you can check that claim.

**The two small C helpers in `deploy/` do link libimobiledevice**, which is LGPL-2.1. They link
it *dynamically*, against the shared library the image installs from Alpine — which is what the
LGPL asks for, since anyone can replace that library and relink. The helpers exist because no
shipped tool exposes what they do: reading an app's home-screen icon off the device, and writing
the Wi-Fi sync flag.

**ideviceinstaller is GPL-2.0, and the image distributes it.** It is a separate executable that
springback runs, not a library it links — mere aggregation, so it does not make springback's own
code GPL. But shipping the binary carries the obligation to make its source available, and this
is how that is met: the exact upstream tag is pinned in [`versions.env`](versions.env), the
[Dockerfile](deploy/Dockerfile) builds it from that tag with no patches, and the source is at
<https://github.com/libimobiledevice/ideviceinstaller>. If you want the corresponding source for
the binary in a given image, the tag in that image's `versions.env` is the answer.

If you think any of the above is wrong, please open an issue — getting it right matters more than
being able to say it was checked.

---

## Not affiliated with Apple

springback is not endorsed by, sponsored by, or connected with Apple Inc. "App Store", "iPhone",
"iPad" and "Apple" are trademarks of Apple Inc., used here only to describe what the software
interoperates with.
