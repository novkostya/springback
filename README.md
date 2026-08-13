# springback

**Keep the apps you own, after Apple stops offering them.**

An app you paid for can just vanish. The developer folds, the listing is pulled, the app leaves
your region. The copy on your phone keeps working — until the day you replace the phone, and then
it is gone for good, because there is nothing left to download.

springback finds those apps, downloads the ones you have a licence for onto a box you control,
and puts them back on a device whenever you need them.

It runs on a Linux machine with your iPhone or iPad reachable from it, and you use it from a
browser — usually the phone's.

**[Try it without installing anything →](https://springback-demo.fly.dev)** — a throwaway demo on
fixture data. The password is on the login screen. Nothing there talks to a real device or to
Apple, and it resets itself.

---

## What it does

**Tells you which of your apps are already gone.** It reads the app list off each device and asks
several App Stores about every one. An app counts as delisted only when *every* storefront comes
back empty — checking one gives you a screenful of false alarms, because plenty of apps simply are
not sold in the US.

**Archives them in one tap.** A delisted app cannot be searched for, because it is in no store to
search. But your device kept the purchase receipt, and the receipt has the numeric App Store id —
so springback asks for the file by number. That is why archiving is one gesture rather than a hunt
for an id.

**Puts them back.** Install an archived app onto any paired device. A different Apple ID on the
device is fine: iOS asks for the owning Apple ID the first time the app is opened, not at install.

**Manages the devices.** Pair a new one over USB, and turn Wi-Fi sync on or off.

---

## Three honest warnings

**1. This drives an unofficial client against Apple's private API.**
[ipatool](https://github.com/majd/ipatool) signs in to Apple's Store API the way iTunes used to.
That is against Apple's terms, and accounts have been flagged for it. The risk is yours.

**2. It only downloads apps you already own.** springback never buys anything — the flag that
would acquire a licence is never passed. If an account does not already own an app, the download
fails.

**3. Whoever can reach the port can act as every Apple ID signed in to it.** There is a password
on the door, and that is all. Do not put it on the public internet.

---

## Running it

You need a Linux box with Docker, and a cable for the first pairing.

springback talks to iPhones through a *muxer* — the daemon that owns the USB bus. It does not
contain one, because **only one muxer may own the bus**: start a second alongside an existing one
and they fight over it. So check first:

```sh
ss -lx | grep usbmux
```

### If that found something, use it

Something is already muxing — `usbmuxd` ships as a libimobiledevice dependency and many distros
enable it. Point springback at its socket:

```yaml
# compose.yml
services:
  springback:
    image: ghcr.io/novkostya/springback:latest
    container_name: springback
    restart: unless-stopped
    ports:
      - "8971:8971"
    volumes:
      - ./data/library:/library      # the archives — this is the one that grows
      - ./data/accounts:/accounts    # Apple ID sessions and your password. Back this up.
      - /var/run/usbmuxd:/var/run/usbmuxd
      - /var/lib/lockdown:/var/lib/lockdown
```

That is the whole file; [`deploy/compose.yml`](deploy/compose.yml) is the same with comments.
Devices are reachable on the cable only — no Wi-Fi. If another tool on the box owns the pairing
records, mount `/var/lib/lockdown` read-only and let it keep ownership.

No muxer yet and you only care about USB? Install one and the file above works:

```sh
# Debian/Ubuntu: apt install usbmuxd  ·  Arch: pacman -S usbmuxd  ·  Alpine: apk add usbmuxd
sudo systemctl enable --now usbmuxd
```

### If it found nothing, run netmuxd — and get Wi-Fi

[netmuxd](https://github.com/jkcoxson/netmuxd) runs in a container, installs nothing on the host,
and reaches devices over Wi-Fi as well as on the cable. Use
[`deploy/compose.netmuxd.yml`](deploy/compose.netmuxd.yml), or:

```yaml
# compose.yml
services:
  netmuxd:
    image: ghcr.io/jkcoxson/netmuxd:latest
    container_name: netmuxd
    restart: unless-stopped
    network_mode: host          # mDNS is link-local; a bridged container never hears it
    device_cgroup_rules:
      - "c 189:* rmw"           # USB devices. Enough — no `privileged: true` needed
    environment:
      RUST_LOG: info            # the first place to look when a device does not appear
    volumes:
      - /dev/bus/usb:/dev/bus/usb
      - ./data/lockdown:/var/lib/lockdown
      # Hides this computer's own identity file, which netmuxd would otherwise mistake for a
      # pairing record. Leave it out and Wi-Fi devices go missing.
      - /dev/null:/var/lib/lockdown/SystemConfiguration.plist
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

Three netmuxd habits worth knowing:

- **Restart it after plugging a device in** (`docker compose restart netmuxd`) if the device does
  not appear. It gives up on a busy USB port after a few seconds and does not retry.
- **It pairs by itself.** Plug in an unknown device and the phone asks "Trust This Computer?"
  without anyone pressing anything, and there is no way to turn that off. Nothing happens unless
  you tap Trust — but if you want pairing to be deliberate, use plain usbmuxd, which never does
  this.
- **Its log repeats a scary-looking warning, and it is expected.** Every few seconds:

  ```
  WARN netmuxd::pairing_file] Failed to parse SystemConfiguration.plist
       (… UnexpectedEof … FilePosition(0) …), regenerating
  ```

  That is the `/dev/null` line in the compose file doing its job. netmuxd reads that path for its
  own identity, finds an empty file, says so, and carries on with a fresh one. Devices still pair,
  still attach over Wi-Fi and still install — the same log shows the pairing records being read a
  line later. The only thing it costs is that any pairing **netmuxd itself** performs uses a host
  identity that is not written down anywhere.

  It cannot be silenced without giving netmuxd a readable file there, and that brings back the
  Wi-Fi bug the mount exists to prevent — see the comment on the mount for the mechanism.

### Then

```sh
docker compose up -d
```

Open `http://<the box>:8971` and choose a password. There is nothing to configure first, and no
password in the compose file.

1. **Accounts** → add your Apple ID. It will ask for a 2FA code.
2. **Devices** → tap your phone. If it says *not paired*, plug it in and tap **Pair**, then tap
   **Trust** on the phone.
3. The first scan takes about half a minute — it is asking Apple about every installed app. After
   that it is instant.
4. Anything marked **DELISTED** is at risk. Tap it, then **Archive to library**.

---

## Worth setting up

### TLS

springback works over plain `http` and shows a warning banner while you do — your password and
every Apple ID session cross the network in the clear. It does not refuse to run, because a
`Secure` cookie is silently discarded over `http` and you would get a login that fails with no
error at all.

Any reverse proxy that terminates TLS and forwards `X-Forwarded-Proto: https` will do — springback
then marks the cookie `Secure` and drops the banner. With Caddy:

```
springback.example.com {
    reverse_proxy localhost:8971
}
```

springback keeps a WebSocket open for live updates, so the proxy must pass upgrades through. Caddy
and Traefik do; nginx needs `proxy_set_header Upgrade $http_upgrade;` and `proxy_set_header
Connection "upgrade";`. Forgetting it costs you nothing but the immediacy — the page falls back to
polling.

### Wi-Fi

A paired device answers over Wi-Fi as long as its Wi-Fi sync flag is on. There is a switch on each
device's page. Off, and it only answers on the cable.

---

## Things that will surprise you

**A sleeping iPhone disappears.** It leaves the network entirely, so it shows as *asleep* and its
apps cannot be listed. Normal, not a fault — springback still knows it exists from the pairing
record.

**Apple sometimes refuses sign-ins outright.** You will see a message about Apple rejecting the
request rather than a wrong password, because that is what it is. Nothing to fix at this end; wait
and try again.

**Not everything missing is delisted.** An app that was never a public listing — an in-house
build, something preinstalled — is reported as *not listed* and left out of the count.

**An archived app still updates normally.** Installing from springback does not cut it off from
the App Store; it appears in Updates as usual, and iOS asks for the owning Apple ID's password
there. Re-downloading through springback avoids that prompt.

---

## Building it yourself

You need `make` and a container runtime, and nothing else — every gate runs inside the same image
stages the release is built from.

```sh
make gates     # gofmt, vet, golangci-lint, go test -race
make image     # the production container
make dev       # run it against the FAKE tool layer — no hardware, no Apple ID, no network
```

`make dev` serves the whole app against fixtures, so you can click through every screen on a
machine with no iPhone near it. Version pins live in [`versions.env`](versions.env).

---

## More reading

- [`SPEC.md`](SPEC.md) — which commands work, which flags matter, what each failure means. Every
  command in it was run against real hardware.
- [`CREDITS.md`](CREDITS.md) — the projects springback stands on, and the licence position.
- [`docs/purchase-history.md`](docs/purchase-history.md) — an unfinished investigation into
  listing apps you own but have *not* installed.
- [`docs/ios-spa-notes.md`](docs/ios-spa-notes.md) — notes on making a web app feel right on iOS.
- [`deploy/demo.md`](deploy/demo.md) — the throwaway public demo: what it is seeded with, and what
  makes it safe to point a stranger at.

---

## Licence

[MIT](LICENSE). Not affiliated with Apple Inc.
