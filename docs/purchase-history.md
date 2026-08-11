# Purchase-history enumeration — research notes

**Status:** partial. The endpoint is FOUND and confirmed live; the session handshake is not solved.
Investigated 2026-08-11 against a real signed-in Apple ID on the staging stand.

SPEC §8 deferred this with:

> iMazing lists purchased apps, so an endpoint exists, but ipatool does not implement it and
> finding it means reverse-engineering the Store API against a live session. Research, not a day.

That judgement still holds. What follows is the distance covered, so the next attempt starts here
rather than at the beginning.

## Why it is worth having

The device-enumeration route (SPEC §6) finds delisted apps **installed on a reachable device**.
Purchase history is the only way to reach apps you own but have not installed — the ones already
lost, which are exactly the ones nobody can get back by other means. It also stops depending on a
device being awake.

## The endpoint, and how to find it again

Apple publishes its own endpoint directory — the "bag" — and ipatool already fetches it, reading
exactly one key from it (`authenticateAccount`). The other 88 are unexplored:

    GET https://init.itunes.apple.com/bag.xml?guid=<MAC-UPPERCASE-NO-COLONS>

Inside it, `purchase-daap`:

    DaapDatabaseName  "Purchased"
    database-name     "Purchased"
    database-id       101
    base-url          https://pd.itunes.apple.com/WebObjects/MZPurchaseDaap.woa/purchase
    update-polling-frequency-secs  3600

`MZPurchaseDaap` is the service behind iTunes' **Purchased** list, and it speaks DAAP (Digital
Audio Access Protocol) rather than the plist-over-HTTP that the rest of the Store API uses —
`Content-Type: application/x-dmap-tagged`, 4-character tags with big-endian lengths.

## What was measured

| request | result |
|---|---|
| `/server-info` | **200**, valid DAAP |
| `/login` (with and without `guid`, `hasFP`, `pairing-guid`) | **400** — `merr` / `mstt` 0x190 |
| `/databases` | 500 |
| `/databases/101/items` | 500 |
| `/content-codes` | 200, but an HTML error page |

`/server-info` decodes to:

    mslr (login required)  0        <- login is NOT required
    msdc (database count)  1
    msas (auth schemes)    0x80
    mpro (dmap version)    2.6
    apro (daap version)    3.8
    mstt (status)          200

Two things follow. The service is live and answers us: a 400 in well-formed DAAP is a protocol
error, not a rejected credential — we are being understood and told the request is wrong. And
`mslr = 0` says the `/login` handshake may be the wrong door entirely; the 500s on `/databases`
are more likely a missing `session-id` than an authorisation failure.

## What is already in hand

Nothing below needs new work — it was all obtained during this investigation:

- **DSID** — from two independent places that agree: the `DSPersonID` inside a device's own
  `iTunesMetadata` receipt (springback already parses this, see `tools/applist.go`), and the
  `X-Dsid` cookie in ipatool's jar.
- **Pod** — `itspod` in the same jar (48 for the test account); the Store API pod-prefixes hosts
  as `p<N>-buy.itunes.apple.com`.
- **GUID** — the host's MAC address, uppercased with colons stripped. This is how ipatool derives
  it (`machine.MacAddress()`), and the Store binds sessions to it.
- **Session cookies** — `/accounts/<slug>/.ipatool/cookies`, plain JSON, written by ipatool's
  successful login. The account file beside it is a JWE encrypted with the keychain passphrase;
  the DSID inside it is available more cheaply from the receipt above.
- **Storefront** — `storefrontCountryCode` in each receipt (`ru` for the test account, which is
  storefront id 143469).

## What is missing

The exact shape of the request that opens a session. Every `/login` variant tried returned the
same 400, which suggests a required parameter or header that is not being guessed at.

**The reliable way to get it is to watch a real client rather than guess**: run iTunes, Apple
Configurator or iMazing through a TLS-intercepting proxy (mitmproxy with the Apple root trusted)
and capture the actual `MZPurchaseDaap` exchange — the login, the `session-id` it returns, and the
`meta=` field list used for `/databases/101/items`. That needs a Mac, which is why this is
research rather than an afternoon.

Cheaper things worth trying first, in rough order of promise:

1. `/databases?session-id=N` and `/databases/101/items?session-id=N&meta=...` with an arbitrary
   session id, since `mslr = 0` implies no login is needed. The 500s may simply be a missing
   parameter.
2. `/update?session-id=N&revision-number=1` — the standard DAAP handshake step between login and
   databases.
3. The `X-Token` / `passwordToken` from ipatool's encrypted account blob, sent as a header. The
   MZFinance endpoints authenticate by cookie, but this DAAP service may want the token directly.
4. The other bag keys. `accountSummary`
   (`buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/accountSummary`) is plist-based and already
   reachable with the cookies ipatool holds; it may carry purchase data without any DAAP work at
   all. This is the least exotic path and was not tried.

## A caution for whoever picks this up

Probing this sends a real Apple ID's session to Apple. Keep it to GETs; anything under
`MZBuy.woa` or named `buyProduct` acquires a licence and is a state change on someone's account.
Apple was refusing new sign-ins for this client while these notes were written (ipatool issue
#513), so distinguish "my request is wrong" from "Apple is refusing everything today" before
concluding anything — the same request returned 204, 403 and 503 within two minutes that day.
