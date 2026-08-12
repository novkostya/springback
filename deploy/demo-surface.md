# What a stranger can reach on the public demo

A route-by-route disposition of everything reachable on a `--public-demo` instance, written before
the instance exists. Prompted by quince#444, whose ruling puts the review *first*: **a preset public
password makes every authenticated endpoint effectively unauthenticated.** Auth stops being a
boundary and becomes a formality that documents itself on the login screen.

So the question for each route is not "is it behind the password" — everything is, and the password
is printed on the screen. It is "what can a stranger do with it, and what bounds the damage".

Each disposition is one of:

- **safe** — fixtures only, or bounded by something that is not the password
- **accept** — a visitor can spoil their own demo, and a restart undoes it
- **fixed** — needed a change before exposure; the change is named

Everything below was measured against the image, not read off the source.

## The two that needed fixing

**`POST /api/auth/login` — fixed.** An unbounded memory amplifier for anyone who can reach the
port, and not a demo problem: it is in the shipping product, and the demo would have put it on the
internet.

argon2id costs 64 MiB per derivation by design. The per-IP throttle does not bound it, because
springback believes `X-Forwarded-For` unconditionally — deliberate, and what makes the limiter bill
the right visitor behind a proxy, but it means an attacker who varies one header gets a fresh
bucket every time:

```
60 wrong passwords, a new forwarded address each  ->  60 x 401   (throttle never fires)
20 wrong passwords, the same address              ->  429        (throttle works, honestly)
```

Sixteen concurrent wrong guesses of ~100 bytes each, on an ordinary install with a password set:

```
before   idle 138 MB  ->  1059 MB
after    idle 138 MB  ->   295 MB   and 296 MB at 64 concurrent — flat
```

The fix bounds the *resource* rather than the asker: at most two derivations run at once, process
wide, where no header can move it and no caller can forget it. Over the limit, requests wait —
kilobytes each — so under attack an honest sign-in is slow instead of impossible.

**`POST /api/library` and `POST /api/devices/{udid}/install` — fixed.** Jobs are single-flighted by
key, which stops the same app being fetched twice and is the mechanism everyone points at. It does
nothing for *different* keys:

```
40 POSTs naming 40 app ids   ->  40 accepted, 40 goroutines, 40 ipatool processes
after the cap                ->   8 accepted, the rest refused with an explanation
```

Nobody legitimately downloads forty apps at once; on an instance whose password is public,
"nobody legitimately" is not a bound. The cap is checked inside the same lock that creates the job,
because counting first and starting second lets a whole burst through — which would hold for two
people clicking at once and fail for exactly the case it exists to stop.

## The one that was already right

**`POST /api/auth/setup` — safe.** This is quince#463, whose demo turned it into a permanent
amplifier: there, `SetPassword` derives the hash *before* the check that guarantees a 409, so 60
requests took the process from 9 MB to 2063 MB. springback checks `s.hash != ""` first and returns
`ErrAlreadySetUp` without deriving anything. Measured on the demo, where every such request is the
throw-away path:

```
60 setup requests  ->  15428 kB to 16296 kB, all 409
```

## The rest

| route | disposition | why |
| --- | --- | --- |
| `GET /api/health` | safe | version and the fake flag |
| `GET /api/auth/status` | safe | carries the demo password ON PURPOSE — that is what puts it on the login screen — and only ever on an instance started with `--public-demo` |
| `POST /api/auth/logout` | safe | ends the caller's own session |
| `GET /api/ws` | accept | server→client only; strict same-origin, an empty `Origin` is refused, and the session is re-checked on every ping. The connection ceiling is **unmeasured** |
| `GET /api/devices`, `/{udid}`, `/apps`, `/installed` | safe | fixture devices; no hardware exists to reach |
| `POST /api/devices/rescan` | safe | the fake enumerates a map |
| `POST /api/devices/{udid}/pair`, `/unpair`, `/wifi-sync` | accept | fixture state a visitor can flip; the restart undoes it. Each runs inside the request rather than as a job, so there is no goroutine to accumulate |
| `GET /api/devices/{udid}/icon.png` | safe | warms a per-device batch, single-flighted per device, cached on disk |
| `GET /api/library`, `/{id}/icon.png` | safe | reads |
| `DELETE /api/library/{id}` | accept | a visitor can empty the library. This is the vandalism the reset is designed around, accepted deliberately — and the reason the reset had to stop depending on the platform |
| `GET /api/accounts` | safe | emails of fixture accounts |
| `POST /api/accounts`, `/{slug}/2fa` | accept | the fake accepts any credentials and reaches nothing. Unbounded in count — a visitor can add many, bounded only by the restart. **Not fixed:** each is a directory and a JSON entry, kilobytes, and capping it would cost the demo its most instructive flow |
| `DELETE /api/accounts/{slug}` | accept | as above |
| `GET /api/jobs`, `/{id}` | safe | reads |
| `GET /api/lookup` | safe | the fake answers from a fixture map — no request leaves the machine |
| `/` | safe | the embedded UI |

## What this review does not establish

Stated so it is not read as broader than it is.

- **The `X-Forwarded-For` behaviour was measured against the image with the header set by hand**,
  not behind fly's proxy. What the first deploy *did* confirm is the other header: the live
  instance reports `"secure":true` over HTTPS, so `X-Forwarded-Proto` is arriving and read, and
  session cookies are marked `Secure`. Whether fly's forwarded-for value is the visitor rather than
  its own proxy is still documentation rather than measurement.
- **The WebSocket connection ceiling is unmeasured.** `GET /api/ws` is disposed *accept* on the
  strength of its origin and session checks, not on any measurement of how many sockets one machine
  will hold.
- **Disk exhaustion is bounded but not measured to its limit.** The job cap bounds the *rate*
  (8 concurrent, ~10 KB each against the fake); a patient attacker still accumulates until the
  machine sleeps. The reset is what collects it.
- **The real tool layer was not reviewed here.** Every disposition above assumes `--public-demo`,
  which forces the fake. The two *fixed* findings are in the shipping product; the rest are not
  claims about an ordinary install.
