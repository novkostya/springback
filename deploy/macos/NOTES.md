# springback on macOS, natively

An experiment: springback's daemon compiled for `darwin/arm64` and shipped as a `.app`, with no
container and no VM. It works, and the interesting results are the two things that got easier and
the one thing that broke.

Nothing here is proposed for merge. It is written down so the next person does not re-derive it.

## What it took

The Go daemon is portable. A `darwin/arm64` cross-compile in the pinned Linux toolchain container
produced **two** errors, both the same ioctl-spelling difference, both in `disableEcho`:

    internal/tools/real.go:489: undefined: unix.TCGETS
    internal/tools/real.go:495: undefined: unix.TCSETS

Linux spells them `TCGETS`/`TCSETS`; the BSDs `TIOCGETA`/`TIOCSETA`. Everything else compiled.
There is no cgo, and every external call already goes through the `tools` seam.

The other changes are about a `.app` being launched with no arguments and no useful environment:
platform defaults for the data directories, and `resolveTool` below.

## What got easier

**The muxer chapter disappears.** macOS *is* the muxer host: Apple's `usbmuxd` owns the bus and
listens on `/var/run/usbmuxd`, which is libimobiledevice's default. springback found it with
`mux=""` and no configuration — no netmuxd, no sidecar, no `device_cgroup_rules`, and none of the
`SystemConfiguration.plist` workaround. Two iPhones appeared over Wi-Fi with names, models, iOS
versions and region codes.

**quince#1226's failure does not occur here.** That issue's diagnosis is that libimobiledevice
casts the muxer's opaque `NetworkAddress` to a native `sockaddr` and dials it directly, so a Wi-Fi
device is unreachable whenever the muxer does not share the client's kernel or network namespace.
A darwin client against Apple's darwin muxer shares both. `idevicepair -n validate` succeeds over
Wi-Fi, unpatched.

**No pairing directory is needed**, which is quince#1309's finding arriving from the other side.
libimobiledevice stores no pairing records of its own; it asks the muxer. `/var/db/lockdown` is
unreadable without root and reading it would answer from a copy of the truth. `-lockdown ""` makes
`PairingKnown()` false, which the pairing code already degrades to correctly.

## What broke: ipatool cannot be made to use the file backend

**springback can hold exactly one Apple ID on macOS.** This is the finding that matters.

springback isolates Apple IDs by `HOME` — one directory per account, each with its own
`.ipatool/account`, unlocked by a per-account `KeychainPP`. That works because in a container
ipatool falls through to keyring's **file** backend.

ipatool 2.3.2 asks for backends in this order (`cmd/common.go`):

    AllowedBackends: []keyring.BackendType{
        keyring.KeychainBackend,      // macOS — always available, opener never fails
        keyring.SecretServiceBackend, // needs D-Bus; absent in a container
        keyring.FileBackend,
    }

`keyring.Open` takes the first backend whose opener succeeds. In a container the first two are
unavailable, so it lands on the file backend. On macOS the Keychain opener only builds a struct and
returns `nil`, so it always wins — and the item is stored at a **fixed** service and key:

    KeychainServiceName = "ipatool-auth.service"
    keychain.Set("account", data)

The macOS Keychain is per-user, not per-`HOME`. So every account writes the same item, `HOME`
isolation has no effect, and a second sign-in overwrites the first. It also means **sessions are
not portable**: copying `accounts.json` and the `.ipatool` directories from a Linux instance
restores the account list and nothing else, because the credentials were never in those files.

### Why there is no switch to flip

- **No flag, and no env var.** `AllowedBackends` is a literal in ipatool. The only `DISABLE_*` in
  the keyring library is `DISABLE_KWALLET`.
- **`CGO_ENABLED=0` would do it.** `keychain_macos.go` is `//go:build darwin && !ios && cgo`, so
  with cgo off the backend is never registered and `Open` falls through to the file backend.
- **But that is the build the Dockerfile already documents as impossible.**
  `client_builder_no_cgo.go` in the 1Password SDK is `//go:build !cgo && (darwin || linux)` and
  contains only a deliberate compile error. The Dockerfile forces `CGO_ENABLED=1` for this reason
  and notes "there is no build tag that avoids it". On Linux that costs nothing, because the
  Keychain backend does not exist there. On darwin it is the entire problem.

The two constraints conflict only on this platform. Routes out, none taken here: a patched ipatool
or a `go mod replace` stubbing the 1Password SDK, which springback never uses; or upstream adding
backend selection.

## A bug this found in the Linux code

`exec.Command` resolves a bare name against **the calling process's** `$PATH`, before `cmd.Env` is
read. springback set a `PATH` in `cmd.Env` and executed a file chosen by a different `PATH`
entirely. On Linux the two agreed by accident — the image installs the tools where the inherited
`PATH` already points — so it never showed. In a `.app` they do not agree, and the symptom is
`executable file not found in $PATH` naming a file that is present and executable.

Fixed by `resolveTool` in `core/internal/tools/real.go`, which resolves against `toolPATH` and
passes an absolute path. Verified by running the daemon under `env -i`, with no `PATH` in
existence, and listing both devices.

## Not established

- **The sign-in path past authentication.** Apple is currently refusing ipatool's client outright
  (HTTP 403/204/503, upstream ipatool#513). Reproduced here with springback removed entirely and an
  address that does not exist, so it is not springback's doing — but it also means 2FA, session
  storage and the download path have never run on darwin.
- **Installing to a device.** `ideviceinstaller` is bundled and runs; no install has been performed.
- **Anything on Intel**, or on a Mac other than the one this was built on.

## Loose ends in the packaging

- **Ad-hoc signed, not notarized.** It will not travel to another Mac.
- **The libimobiledevice CLIs come from Homebrew** at whatever version brew holds. They are copied
  into the bundle and their install names rewritten, so the finished `.app` depends on nothing
  outside itself — but only ipatool is version-pinned.
- **`install_name_tool` invalidates signatures**, and Apple silicon `SIGKILL`s unsigned binaries.
  The symptom is a tool that runs and prints nothing. Everything is re-signed after relocation.
- **`Pairing — unknown, the device is not answering` is now misleading.** The device is answering;
  springback has no record store to consult. That copy conflates two different situations, and only
  one of them was reachable before.
