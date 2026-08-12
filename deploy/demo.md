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
| state | none. No volume: the rootfs is the state, and it is thrown away |
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

## Why there is no reset timer

**The platform is the reset.** The demo writes to the container filesystem, and `auto_stop_machines
= "stop"` terminates the VM when nobody is visiting; the next visitor gets a fresh rootfs and a
fresh seed. There is no interval to promise and the login screen promises none — "it resets itself"
is the whole claim, and it is true whenever the machine has slept.

This is also why `fly.toml` says `stop` and never `suspend`: a suspended machine resumes from a
memory snapshot without restarting the process, so the seed never runs again and whatever the last
visitor left behind stays.

**Do not attach a volume.** It would preserve every visitor's mess forever, and nothing inside
springback could tell.

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
```

Then <http://localhost:8979>, password `springback-demo`. Nothing is mounted, so stopping the
container is the reset — the same shape fly gives it.
