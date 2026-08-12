# springback

**Keep the apps you own, after Apple stops offering them.**

An app you paid for can just vanish. The developer folds, the listing is pulled, the app leaves
your region. The copy on your phone keeps working — until the day you replace the phone, and then
it is gone for good, because there is nothing left to download.

springback finds those apps, downloads the ones you have a licence for onto a box you control,
and puts them back on a device whenever you need them.

It runs on a Linux machine with your iPhone or iPad reachable from it, and you use it from a
browser — usually the phone's.

---

## What it actually does

**Tells you which of your apps are already gone.** It reads the app list off each device and asks
several App Stores about every one of them. An app counts as delisted only when *every* storefront
checked comes back empty — checking one is how you get a screenful of false alarms, because plenty
of apps are simply not sold in the US.

**Archives them in one tap.** A delisted app cannot be found by searching, because it is not in
any store to search. But your device kept the purchase receipt, and the receipt has the numeric
App Store id in it — so springback can still ask for the file by number. That is the whole trick,
and it is why archiving is one gesture and not a hunt for an id.

**Puts them back.** Install an archived app onto any paired device. A different Apple ID on the
device is fine; iOS asks for the owning Apple ID the first time the app is opened, not at install.

**Manages the devices themselves.** Pair a new one over USB, and turn Wi-Fi sync on or off so a
device answers when it is not plugged in.

---

## Before you start, three honest warnings

**1. This drives an unofficial client against Apple's private API.**
[ipatool](https://github.com/majd/ipatool) signs in to Apple's Store API the way iTunes used to.
That is against Apple's terms, and accounts have been flagged for it. You run your own instance,
on your own box, with your own Apple ID. The risk is yours and it stays there — nobody is
offering to carry it for you.

**2. It only downloads apps you already own.** springback never buys anything: the flag that
would acquire a licence is never passed. If an account does not already own an app, the download
fails and that is the end of it. This is not a way to get apps you did not pay for.

**3. Whoever can reach the port can act as every Apple ID signed in to it.** There is a password
on the door, and that is all there is. Do not put it on the public internet.

---

## Running it

You need a Linux box with Docker, and a cable for the first pairing.

springback does not talk to iPhones by itself — it drives Apple's userland
([libimobiledevice](https://github.com/libimobiledevice/libimobiledevice)), and that needs a
*muxer*: the daemon that owns the USB bus and multiplexes connections to devices. This setup runs
one, [netmuxd](https://github.com/jkcoxson/netmuxd), in its own container. Nothing is installed on
the host, and it handles devices on Wi-Fi as well as on the cable.

Grab [`deploy/compose.netmuxd.yml`](deploy/compose.netmuxd.yml) — it is commented with the
reasoning — or start from this:

```yaml
# compose.yml
services:
  netmuxd:
    image: ghcr.io/jkcoxson/netmuxd:latest
    container_name: netmuxd
    restart: unless-stopped
    network_mode: host          # mDNS is link-local; a bridged container never hears it
    device_cgroup_rules:
      - "c 189:* rmw"           # USB device major. Enough — no `privileged: true` needed
    environment:
      RUST_LOG: info            # the first place to look when a device does not appear
    volumes:
      - /dev/bus/usb:/dev/bus/usb
      - ./data/lockdown:/var/lib/lockdown
      - mux:/run/mux
    command: ["--socket-path", "/run/mux/usbmuxd", "--plist-storage", "/var/lib/lockdown"]

  springback:
    image: ghcr.io/novkostya/springback:latest
    container_name: springback
    restart: unless-stopped
    depends_on: [netmuxd]
    ports:
      - "8971:8971"
    environment:
      USBMUXD_SOCKET_ADDRESS: "UNIX:/run/mux/usbmuxd"
    volumes:
      - ./data/library:/library      # the archives — this is the one that grows
      - ./data/accounts:/accounts    # Apple ID sessions and your password. Back this up.
      - ./data/lockdown:/var/lib/lockdown
      - mux:/run/mux

volumes:
  mux:
```

```sh
docker compose up -d
```

Open `http://<the box>:8971`. The first page asks you to choose a password — there is nothing to
configure before starting, and no password sitting in the compose file.

**Three things about that muxer, all of which will otherwise waste your afternoon.**

*Restart netmuxd after plugging a device in.*

```sh
docker compose restart netmuxd
```

Not because the device is invisible — it isn't; netmuxd sees it straight away. netmuxd tries to
claim the USB interface about five times over five seconds and then gives up permanently, so if
anything else is holding the bus at that moment (a host `usbmuxd`, another container's muxer) it
never tries again, even after the interface is free. Look for `interface is busy (errno 16)` in
`docker logs netmuxd`. Stop whatever else owns the bus, then restart netmuxd.

*Wi-Fi devices only appear once they are paired.* An mDNS advertisement carries no serial number,
so netmuxd matches it against the pairing records it holds to work out which phone it is looking
at. With no record it discovers every device on the network and serves none of them — it logs
`No paired device matched service …`. Pair over the cable once; Wi-Fi is enough after that.

*netmuxd starts pairing by itself.* Plug in a device it has no record for and it calls
`lockdown.pair()` straight away, so the phone asks "Trust This Computer?" without anyone pressing
anything. There is no option to disable it. Nothing can happen without someone tapping the phone,
and pairing is what you plugged it in for — but it does mean springback's own **Pair** button is
not the only thing that can raise that prompt. If you would rather pairing were always deliberate,
use the usbmuxd setup below: usbmuxd never pairs on its own, so the button is the only trigger.

### If you would rather not run netmuxd

`usbmuxd` from your distribution works too, on the host, and springback then needs only its
socket. Two differences, one in each direction: it only ever sees devices on the cable, so no
Wi-Fi — and it never pairs on its own, so the **Pair** button is the only thing that can ask a
device to trust this computer.

```sh
# Debian/Ubuntu: apt install usbmuxd  ·  Arch: pacman -S usbmuxd  ·  Alpine: apk add usbmuxd
sudo systemctl enable --now usbmuxd
```

with [`deploy/compose.yml`](deploy/compose.yml), which mounts `/var/run/usbmuxd` straight into
springback.

### Then, roughly

1. **Accounts** → add your Apple ID. It will ask for a 2FA code.
2. **Devices** → tap your phone. If it says *not paired*, plug it in with a cable and tap
   **Pair**, then tap **Trust** on the phone.
3. The first scan takes about half a minute — it is asking Apple about every installed app. After
   that it is instant.
4. Anything marked **DELISTED** is at risk. Tap it, then **Archive to library**.

---

## Two things worth setting up

### Put it behind TLS

springback works fine over plain `http` and puts a warning banner on every screen while you do.
That banner is not decoration: your password and every Apple ID session cross the network in the
clear.

It does *not* refuse to run, deliberately — a session cookie marked `Secure` is silently discarded
by the browser over `http`, which would give you a login that appears to succeed and then does
nothing, with no error anywhere. Working with a warning beats failing mysteriously.

Any reverse proxy that terminates TLS and forwards `X-Forwarded-Proto: https` will do. springback
reads that header, marks the cookie `Secure`, and drops the banner. With Caddy that is one line:

```
springback.example.com {
    reverse_proxy localhost:8971
}
```

### Keeping devices reachable over Wi-Fi

Once a device is paired, it answers over Wi-Fi as long as its Wi-Fi sync flag is on — there is a
switch for it on each device's page. Turn it off and the device only answers on the cable: it
leaves the network entirely and springback stops seeing it.

---

## Things that will surprise you

**A sleeping iPhone disappears.** It drops off the network entirely, so it shows as *asleep* and
its apps cannot be listed. That is normal, not a fault. springback remembers the device exists
because the pairing record is still on disk.

**Apple sometimes refuses sign-ins outright.** You will see a message about Apple rejecting the
request rather than a wrong-password error, because that is what it is: the Store API declining an
unofficial client, with the status code changing between attempts. There is nothing to fix at this
end; wait and try again.

**Not everything missing is delisted.** An app that was never a public listing — an in-house
build, something preinstalled — is reported as *not listed* and left out of the count. It is not
at risk; there was simply never a store page.

**An archived app can still be updated normally.** Installing from springback does not cut the app
off from the App Store. It will appear in Updates as usual; iOS will ask for the owning Apple ID's
password when you update it there. Re-downloading through springback avoids that prompt, which is
convenient — not the only route.

---

## Building it yourself

The box that builds springback needs `make` and a container runtime, and nothing else — no Go, no
linter, no toolchain to keep in step. Every gate runs inside the same image stages the release is
built from.

```sh
make gates     # gofmt, vet, golangci-lint, go test -race
make image     # the production container
make dev       # run it against the FAKE tool layer — no hardware, no Apple ID, no network
```

`make dev` is worth knowing about: it serves the whole app against fixtures, so you can click
through every screen on a machine with no iPhone anywhere near it. The fixtures deliberately
include an app that is sold in some countries and not others, because that is the case a naive
implementation gets wrong while looking completely correct.

All version pins live in [`versions.env`](versions.env).

---

## More reading

- [`SPEC.md`](SPEC.md) — the measured record. Which commands work, which flags matter, what each
  failure means. Every command in it was run against real hardware before it was written down.
- [`CREDITS.md`](CREDITS.md) — the projects springback stands on, and the licence position.
- [`docs/purchase-history.md`](docs/purchase-history.md) — an unfinished investigation into
  listing apps you own but have *not* installed. The endpoint is found; the handshake is not.
- [`docs/ios-spa-notes.md`](docs/ios-spa-notes.md) — notes on making a web app feel right on iOS,
  written up after getting it wrong several times.

---

## Licence

[MIT](LICENSE). Not affiliated with Apple Inc.
