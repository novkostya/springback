# springback — spec

Keep the apps you own, after Apple stops offering them.

**Status:** written 2026-08-11 by the supervisor seat, from a full manual run of the whole
flow on real hardware the same morning. Every command below was executed and its output
observed; nothing here is inferred.

**Not quince.** Deliberately unassociated: quince is heading for a public release, this uses
an unofficial client against Apple's private API, and the reputational coupling would run one
way only. Shares a host and a netmuxd with quince; shares no name, no repo, no process.

**Process:** no PRs, no branch protection, push to `main`. Personal tool, distributed as an
image to people who run it on their own box with their own Apple ID.

---

## 1. What it does

1. **Sign in to one or more Apple IDs.**
2. **See what is at risk** — apps installed on your devices that are no longer in any App Store.
3. **Download** owned apps into a local library, keyed by numeric App Store ID.
4. **Install** a library app onto any paired device.

### In scope for v0.1 (target: one day)

Accounts (add / list / remove, incl. 2FA) · library (add, download, list, delete) · devices
(list, install) · the at-risk screen · one page of UI · Dockerfile + compose fragment.

### Explicitly out of v0.1

Download progress streaming · version pinning (`ipatool list-versions`) · retry/backoff ·
auth on the web app itself · purchase-history enumeration (see §8) · anything pretty.

---

## 2. Architecture

Sibling container to quince on the same LXC. **Both on host network.**

    springback ──shells out to──> ipatool          (Apple Store client, Go, MIT)
           ──shells out to──> ideviceinstaller (install to device)
           ──reads──────────> 127.0.0.1:27015  (quince's netmuxd, already running)
           ──mounts ro──────> quince's /var/lib/lockdown  (pairing records)

**Do not run a second usbmuxd.** quince's container already runs one; a second fights it for
the USB bus. springback never needs USB — everything goes over netmuxd's TCP port.

**Mount the pairing records READ-ONLY.** A day-old tool must not be able to corrupt the
pairing state quince depends on.

    volumes:
      - ./springback/library:/library
      - ./springback/accounts:/accounts
      - /var/lib/lockdown:/var/lib/lockdown:ro   # from quince's container
    network_mode: host

Every device call needs:

    USBMUXD_SOCKET_ADDRESS=127.0.0.1:27015

---

## 3. The commands that actually work

All verified 2026-08-11. Flags matter; the wrong one fails in ways that look like something else.

### Accounts

One directory per Apple ID; isolation is by `HOME`, measured:

    HOME=/accounts/<slug> ipatool auth login -e <email> --keychain-passphrase <pp> --non-interactive
    HOME=/accounts/<slug> ipatool auth info  --keychain-passphrase <pp> --non-interactive

- **Password over stdin, never argv.** Omitting `-p` makes ipatool prompt; the backend writes
  the password to its stdin. It is typed in the web UI and held in memory only — never argv
  (visible in `ps`), never logged, never persisted.
- **`--keychain-passphrase` is mandatory.** There is no keyring daemon in a container. Generate
  one per account at creation and store it beside the account record; it encrypts a local file,
  it is not an Apple secret.
- **2FA:** first call returns an error; re-run the same command adding `--auth-code <code>`.
  The UI needs a two-step form. Only session cookies are stored, never the password.

### Resolve a bundle id to a numeric App Store id

Free, unauthenticated:

    https://itunes.apple.com/lookup?bundleId=<bundle>&country=<cc>

**QUERY SEVERAL STOREFRONTS.** It defaults to US, and that produces false positives on the
tool's headline feature. Measured:

    ru.yandex.mobile.music        us=0  ru=1  ae=1   <- NOT delisted, just not sold in the US
    com.dreamgoods.officecapital  us=0  ru=0  ae=0   <- genuinely gone
    com.assetsonline.ios          us=0  ru=0  ae=0   <- genuinely gone

**An app counts as delisted only when EVERY queried storefront returns `resultCount: 0`.**
Query at least: the device's own region (`ideviceinfo -k RegionInfo`, e.g. `AE/A`), plus `us`
and `ru`. Cache results; they change rarely.

### Download

    HOME=/accounts/<slug> ipatool download -i <numeric-id> -o /library/<id>/<id>.ipa \
        --keychain-passphrase <pp> --non-interactive

**`-i`, NOT `-b`.** `-b` resolves a bundle id by SEARCHING the store, and a delisted app is not
in search — it fails with `app not found`. This is the single most important line in this spec:
`-b` fails for exactly the apps springback exists to fetch. Measured both ways.

`purchased=false` in the output means the account already held the licence and nothing was
bought. **Never pass `--purchase`** without explicit user consent — it acquires a licence, which
is a state change on someone's Apple account.

Expect ~500 MB per app (the measured sample was 487 MB).

### Devices

    USBMUXD_SOCKET_ADDRESS=127.0.0.1:27015 idevice_id -n                      # list udids
    USBMUXD_SOCKET_ADDRESS=127.0.0.1:27015 ideviceinfo -n -u <udid> -k DeviceName
    USBMUXD_SOCKET_ADDRESS=127.0.0.1:27015 ideviceinstaller -n -u <udid> list --user
    USBMUXD_SOCKET_ADDRESS=127.0.0.1:27015 ideviceinstaller -n -u <udid> install <path.ipa>

Install emits progress lines to stdout (`Install: InstallingApplication (60%)` …) ending in
`Install: Complete`. Parse for the percentage if you want a bar; treat absence of `Complete`
as failure.

**Devices come and go.** A sleeping iPhone drops off mDNS entirely and `idevice_id -n` returns
empty. That is normal, not an error — the UI must say "not currently reachable", never "gone".

---

## 4. On-disk layout

    /accounts/<slug>/            HOME for ipatool; contains .ipatool/{account,cookies}
    /accounts/accounts.json      [{slug, email, name, keychain_pp, added_at}]
    /library/<numeric-id>/
        <numeric-id>.ipa
        meta.json                {id, bundle_id, name, version, size, downloaded_at, account_slug}

`meta.json` is written from the `.ipa` itself after download — `Payload/*.app/Info.plist` gives
`CFBundleIdentifier`, `CFBundleShortVersionString`, `CFBundleDisplayName`; `iTunesMetadata.plist`
at the archive root gives `itemId`, `itemName`, `artistName`. Both are plists inside a zip; read
them, do not trust what the user typed.

**Once an `.ipa` is in the library, its numeric id is never needed again.** The manual-entry cost
is one-time per delisted app.

---

## 5. HTTP API

    GET    /api/accounts                    list
    POST   /api/accounts                    {email, password}      -> 200 | 409 needs_2fa
    POST   /api/accounts/<slug>/2fa         {code}                 -> 200
    DELETE /api/accounts/<slug>

    GET    /api/devices                     [{udid, name, reachable, product_type, ios}]
    GET    /api/devices/<udid>/apps         installed apps + store_status per app
    POST   /api/devices/<udid>/install      {library_id}

    GET    /api/library                     list
    POST   /api/library                     {app_id, account_slug}  -> download
    DELETE /api/library/<id>

    GET    /api/lookup?bundle_id=<b>        multi-storefront resolve -> {id|null, checked:[cc]}

Downloads are slow (~30 s) and installs slower. Either return a job id and poll, or hold the
request open — v0.1 may hold it open; say so in the UI.

---

## 6. UI — three screens

**Devices** (landing). Each paired device, reachable or not. Per device, its installed apps with
a store status: `available` / **`DELISTED`** / `unknown`. Delisted ones sort first and carry an
**Archive** button — which is the whole product in one gesture. Show the count: *"4 of 162 apps
on this iPhone are no longer in the App Store."*

**Library.** What has been downloaded: name, version, size, when, which account. Install-to-device
picker. Delete.

**Accounts.** Add (email → password → 2FA), list, remove.

Add-by-numeric-id lives on the Library screen as a secondary action, for apps not installed on
any reachable device.

---

## 7. Failure modes, and what each one means

    ipatool "app not found"          used -b on a delisted app. Use -i. Or the id is wrong.
    ipatool "license not found"      this Apple ID does not own it. Try another account.
    ipatool keyring "item not found" not authenticated on that account, or wrong passphrase.
    idevice_id -n empty              device asleep / off Wi-Fi. Not an error.
    ideviceinstaller "Could not connect"  no pairing record, or netmuxd not reachable.
    install stops before Complete    read the last line; it names the stage that failed.

**FairPlay defers to FIRST LAUNCH, not install.** An install onto a device signed into a
*different* Apple ID succeeds; the user is asked to sign in as the licensing account the first
time they open the app, and it works thereafter. **The UI must say this**, or every
cross-account install will look broken. Measured: installed onto an iPad from a different
account's library copy, iPad prompted for the owning Apple ID at launch, app ran.

---

## 8. Deliberately not built

**Purchase-history enumeration.** iMazing lists purchased apps, so an endpoint exists, but
ipatool does not implement it and finding it means reverse-engineering the Store API against a
live session. Research, not a day. The device-enumeration route in §6 covers the real case and
is arguably better: purchase history would list hundreds of apps last opened in 2014, where
"installed and delisted" is exactly the actionable set.

**Extracting apps off a device.** Dead. `ideviceinstaller` still ships `archive`, but current
iOS answers `UnknownCommand` — the device no longer implements the Archive API. Measured; do not
spend time on it.

---

## 9. Risk, stated once

Unofficial clients against Apple's private API are against Apple's terms and accounts have been
flagged for it. Each person runs their own instance with their own Apple ID, so the risk is
theirs and stays on their box. Say so in the README rather than leaving them to find out.

Keep it LAN-only. There is no auth on the web app in v0.1, and a session cookie in
`/accounts/` is enough to download as that Apple ID.
