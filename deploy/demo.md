# The public demo

`springback serve --public-demo` on fly.io. This file and `fly.toml` at the repository root are the
whole deployment.

**This is not how anyone runs springback for real.** That is `compose.yml` or
`compose.netmuxd.yml`, both of which need an iPhone and a muxer. The demo has neither: every device
on it is a fixture, and nothing it does reaches Apple.

## What it is

| | |
| --- | --- |
| app | `springback-demo` (fly.io) |
| region | `fra` |
| size | `shared-cpu-1x`, 256 MB |
| mode | `--public-demo`, which **forces** the fake tool layer |
| password | `springback-demo`, printed on the login screen |
| state | throwaway, under `/tmp/springback-demo`, wiped and re-seeded at every process start |
| address | `springback-demo.fly.dev` |

## Deploying it

```sh
fly launch --no-deploy      # first time only; the app name comes from fly.toml
fly deploy
```

`fly deploy` builds `deploy/Dockerfile` — the same image the release publishes, with no demo-only
build. The mode is a flag, so what runs publicly is what runs everywhere else.

## Why the flag forces the fake

`--public-demo` sets `-fake` itself rather than trusting the deploy file, and `demo.Seed` refuses
outright if it is handed the real tool layer. Both checks guard one mistake: this instance
publishes its own password, so everything behind that password is reachable by anyone. Against
fixtures that is the point. Against the real tools, the same screens sign in to Apple with a
stranger's typing and install software onto whatever is plugged in.

## The reset belongs to the process

**A start is a clean demo.** `demo.Reset` deletes `/tmp/springback-demo` before seeding, so
whatever the last visitor did — extra Apple IDs, a deleted library item — is gone by the time the
listener opens.

Letting the platform do it instead is the tempting version and it covers only one case. fly resets
the rootfs on a cold start, so a machine that stops and starts comes back clean; a machine that
*suspends* resumes without restarting the process, a container restarted rather than recreated
keeps its filesystem, and `docker run` on a laptop keeps it for as long as the container is left
around. In every one of those the demo comes up looking fine and serving somebody else's mess,
because seeding is idempotent and idempotent means it leaves what it finds.

Measured, on a container restarted rather than recreated — the case the platform does not cover:

```
visitor adds an account and deletes the library item
  ->  demo@example.com, vandal@example.com   library []
docker restart
  ->  demo@example.com                       library ["Boomerang from Instagram"]
```

`fly.toml` still says `stop` rather than `suspend`, because suspend is the one setting that resumes
service without a process start, and there is no interval to promise: the login screen says "it
resets itself" and nothing more.

**The state is deliberately not `/library` and `/accounts`.** A wipe is only safe over a directory
that belongs to the demo. Before the paths were swapped, `--public-demo` with a real library
mounted wrote a fake Apple ID into the accounts store and a fake .ipa into the archive — measured,
not imagined, and anybody may run this flag to see what the demo looks like, including on the box
that holds everything they have saved. Now those mounts are ignored entirely.

## What the demo is seeded with

Seeding runs before the listener opens, so nobody can arrive at a half-built instance.

- the password, so there is no first-run setup to lock the second visitor out
- one Apple ID, `demo@example.com`, signed in — otherwise every download picker is empty
- one library item: **Boomerang from Instagram**, which is *delisted* in the fixtures

The library item is deliberately an app Apple pulled. It is installed on the fixture iPhone, no
storefront admits it exists, and it is already archived here — so the device screen shows
`DELISTED` and `IN LIBRARY` on one row, which is springback's entire argument without anybody
having to click.

## The login limiter bills the right visitor

Worth stating because the equivalent went wrong on quince's demo (quince#464): a login limiter that
buckets by the peer address treats every visitor as one client when it sits behind a proxy, so ten
wrong passwords from anybody lock out everybody.

springback reads `X-Forwarded-For`'s first entry, which on fly is the visitor. Measured against
this image, twelve wrong passwords from `1.2.3.4`, then:

```
attacker  (X-Forwarded-For: 1.2.3.4), CORRECT password  ->  429   still limited
victim    (X-Forwarded-For: 5.6.7.8), CORRECT password  ->  200
victim    (no X-Forwarded-For at all), CORRECT password ->  200
```

The first line matters as much as the others: the limiter still works, it just charges the right
party. No trust list is needed because springback believes the header unconditionally — which is
the documented trade the other way (a visitor can rotate the header to evade their own throttle),
and on an instance whose password is published there is nothing to evade.

## The exposed surface was reviewed first

[`demo-surface.md`](demo-surface.md) is a route-by-route disposition of everything a stranger can
reach, written before the instance existed — prompted by quince#444, whose ruling is that a preset
public password makes every authenticated endpoint effectively unauthenticated. Two findings needed
code before exposure: an unbounded memory amplifier on the login route, and unbounded concurrent
jobs. Both are in the shipping product rather than the demo, and both are fixed.

## The KDF is deliberately cheap here

Sign-in is the largest allocation in the process: argon2id at the production parameters costs
64 MiB per derivation. On a 256 MB machine that is an out-of-memory kill as soon as a few people
follow a shared link at once. Measured against this image:

```
64 MiB params   idle 73 MB   five concurrent logins  403 MB
 8 MiB params   idle 15 MB   five concurrent logins   49 MB
```

So `demo.Seed` sets 8 MiB before storing the password. The work factor exists to slow down guessing
of a secret, and the demo's password is on its own login screen — there is no secret to protect.
The parameters travel inside the stored PHC string, so nothing else has to know.

**This applies to the demo only.** Every other install keeps `auth.DefaultParams`.

## Running it locally

```sh
make image IMAGE_TAG=demotest
docker run --rm -p 8979:8971 springback:demotest serve --public-demo
# Nothing is mounted, and mounting anything would not matter: the demo runs on its own paths.
```

Then <http://localhost:8979>, password `springback-demo`. Restarting it is the reset, and leaving
the container in place between runs is fine — the wipe is at startup, so it does not depend on how
the last one ended.
