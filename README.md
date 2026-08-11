# springback

Keep the apps you own, after Apple stops offering them.

An app you bought can vanish from the App Store — the developer folds, the listing is pulled, the
region changes. The copy on your phone keeps working until the day you replace the phone, and
then it is gone. springback finds those apps, downloads the ones you have a licence for into a
local library, and puts them back on a device when you need them.

**Read [`SPEC.md`](SPEC.md) first.** It is the measured account of what works, what does not, and
which flags matter — every command in it was run against real hardware before it was written down.

---

## Risk, stated once

springback drives [`ipatool`](https://github.com/majd/ipatool), an **unofficial client against
Apple's private Store API**. That is against Apple's terms, and accounts have been flagged for
it. You run your own instance, on your own box, with your own Apple ID, so the risk is yours and
stays there. Nobody is offering to carry it for you.

**Keep it LAN-only.** There is no authentication on the web app in v0.1 — that is out of scope,
not overlooked. Anyone who can reach the port can act as every Apple ID signed in to it, because
a session cookie under `/accounts/` is enough to download as that account.

springback never buys anything. `--purchase` acquires a licence, which is a state change on
someone's Apple account, and it is never passed. If an account does not already own an app, the
download fails and says so.

---

## The three screens

**Devices** (the landing screen) is the reason this exists. Each paired device, reachable or not;
for a reachable one, its installed apps with a store status each, delisted ones first:

> **4 of 162 apps on this iPhone are no longer in the App Store.**

Every delisted app carries an **Archive** button, which is the whole product in one gesture.

**Library** is what has been downloaded — name, version, size, when, which account — with an
install-to-device picker and a delete. Adding an app by numeric App Store id lives here too, for
apps not installed on any reachable device.

**Accounts** adds an Apple ID (email → password → 2FA code), lists them, and removes them.

### Why a delisted app asks for a number once

The Archive button needs the app's numeric App Store id, and for a **delisted** app nothing can
look it up: it is in no storefront by definition, and the device does not carry the number either
— iOS's `installation_proxy` returns `Info.plist` keys and container attributes, and asking it
for `ITunesMetadata` returns nothing at all (measured against a live iPhone, 2026-08-11). So the
first time you archive a delisted app, springback asks for the id — it is the digits in the app's
old `apps.apple.com/app/id123456789` link, from a bookmark, an old receipt, or a web archive.

That cost is paid **once per app, ever**. The library becomes the lookup table: the same app on a
second device is a one-click archive.

### How "delisted" is decided

An app counts as delisted only when **every queried storefront** comes back empty. springback
queries the device's own region plus `us` and `ru`, at minimum.

This is the whole difference between a useful tool and a misleading one:

| bundle id | us | ru | ae | verdict |
|---|---|---|---|---|
| `ru.yandex.mobile.music` | 0 | 1 | 1 | **not delisted** — just not sold in the US |
| `com.dreamgoods.officecapital` | 0 | 0 | 0 | genuinely gone |

Two further guards, because a wrong "DELISTED" costs the user half an hour and 500 MB:

- A storefront that **fails to answer** is recorded as *not checked*, never as *not in the
  store*. An unknown storefront code answers HTTP 400, not `resultCount: 0` — and `RegionInfo`
  values like `LL/A` are **Apple part-number codes, not ISO country codes** (`LL` is the United
  States; `CH` is China, not Switzerland), so a naive pass-through produces exactly that 400.
- Fewer than two storefronts answering yields **unknown**, never delisted. A network blip must
  not be able to accuse an app.

What the verdict does **not** distinguish: an app that was pulled from the store, and an app that
never had a store listing under that bundle id — a TestFlight beta, an enterprise or
developer-signed build. Both are installed and in no storefront. The device cannot settle it
either: measured on a live iPhone, every installed app reports the same `SignerIdentity` and
`ApplicationType` whatever its origin, and `installation_proxy` exposes no store-provenance
attribute. The screen says so rather than overclaiming, and a wrong guess is cheap — archiving a
never-listed app just fails at download with `app not found`.

On a real 162-app iPhone this returns 11 delisted in ~23 s cold, 0 unknown; the answers are then
cached.

---

## Running it

Sibling container to quince on the same host. See [`deploy/compose.yml`](deploy/compose.yml) —
the header there explains each of the three constraints and what breaks if you ignore one:

```yaml
volumes:
  - ./springback/library:/library
  - ./springback/accounts:/accounts
  - /root/quince/data/lockdown:/var/lib/lockdown:ro   # quince's pairing records, READ-ONLY
network_mode: host
environment:
  USBMUXD_SOCKET_ADDRESS: "127.0.0.1:27015"           # quince's netmuxd, already running
```

**Do not run a second usbmuxd.** quince's container already runs one and a second fights it for
the USB bus. springback never needs USB — everything goes over netmuxd's TCP port. The image does
not install the package.

Expect ~500 MB per app. Downloads take ~30 s and installs longer; v0.1 holds the request open
rather than queueing a job, and the UI says so before it starts one.

### Cross-account installs are fine

FairPlay defers to **first launch**, not install. Installing an app onto a device signed into a
different Apple ID succeeds; iOS asks for the owning Apple ID the first time the app is opened,
and it works from then on. The UI says this at the moment it matters, because otherwise every
cross-account install looks broken.

---

## Development

The dev host is a **pure container host** — no Go toolchain installed anywhere. Every gate runs
inside a pinned toolchain container built from the production `Dockerfile`'s own stage, so dev
and the release image compile with identical toolchains. All pins live in `versions.env`.

```sh
make gates      # gofmt + vet + golangci-lint + go test -race, in the toolchain container
make fmt        # gofmt -w + go mod tidy
make image      # the production container
make dev        # serve this branch with the FAKE tool layer — no hardware, no Apple ID
```

`make dev` is the one worth knowing about. Every external call — ipatool, ideviceinstaller,
`idevice_id`, `ideviceinfo`, the iTunes lookup API — sits behind a single interface with a fake
implementation, so the whole app including the at-risk screen can be exercised on a box with no
iPhone and no Apple ID. The fake's fixtures are real: device names, region codes and bundle ids
read off actual hardware, carrying all three store outcomes deliberately — including the
not-sold-in-this-storefront app that turns into a false `DELISTED` the moment the
multi-storefront rule is broken.

### Layout

```
core/                    Go module — single binary, embedded UI
  cmd/springback/        main
  internal/tools/        THE SEAM: every external call, + the fake
  internal/storefront/   the at-risk rule and its cache
  internal/devices/      the Devices screen's data
  internal/store/        accounts.json + the library on disk
  internal/ipa/          reads an .ipa's own plists
  internal/httpapi/      SPEC §5
  internal/webui/        hand-written static UI, go:embed'd
deploy/                  Dockerfile + compose fragment
```

---

## Not quince

Deliberately unassociated. quince is heading for a public release; this uses an unofficial client
against Apple's private API, and the reputational coupling would run one way only. They share a
host, a netmuxd and a build convention. They share no name, no repo and no process.
