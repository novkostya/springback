# Making an SPA feel native on iOS — notes from doing it

Written 2026-08-11, after taking springback's navigation from "obviously a website" to something
that behaves. The architectural advice this started from (real URLs, the Navigation API, document
scrolling, browser-owned scroll restoration, history-entry-scoped state, restore-don't-refetch)
was right and is not repeated here.

What follows is the part that cost time anyway: the traps between knowing the approach and having
it work. Roughly in the order they bit.

## 1. The advice "let the browser own scroll restoration" has a specific trap in it

Reading it, you will nod, and then write this:

```js
event.intercept({ scroll: "manual", handler });   // and history.scrollRestoration = "manual"
```

because you want control. That is the same mistake the advice is warning about, wearing the
clothes of the solution. Adopting the Navigation API and then overriding its scroll behaviour
made things measurably worse here — worse than the naive `pushState` version it replaced.

**Rule of thumb: if you are typing the word `manual`, stop.** Correct scroll restoration in the
final version is zero lines of code. The browser restores the saved offset for a traversal and
the top for a new navigation, on its own, once the intercept handler resolves.

The one thing it does NOT do, at least in current engines, is reset scroll for a *same-document
intercepted push*. A detail screen opened from a list scrolled near the bottom will keep an
offset its own shorter page can only satisfy near the end, and looks like it opened scrolled to
the bottom. So: `if (!traverse) window.scrollTo(0, 0)` — for new navigations only, never for
traversals.

## 2. `position: fixed` and `position: sticky` are the same risk class

This is the big one and it is not in the standard advice.

Both are resolved against the **visual viewport**, and on iOS the visual viewport changes size
when Safari collapses or expands its toolbars. On a page whose content is only *slightly* taller
than the screen, that few-pixel change flips the page between scrollable and not — and anything
positioned against the viewport is drawn at the wrong offset for a frame while it settles.

The symptom signature is precise, and worth memorising because it tells you the cause
immediately:

> it happens on pages just slightly taller than the viewport, and the page looks scrolled to the
> bottom and shifted at the same time

Four separate fixes went in here before that sentence identified it — each removed a real defect,
and each landed back on the same class of bug, because each kept the bar positioned. An element
in **normal flow** cannot do this at all.

That is a genuine product trade, not a technical one: a bar in normal flow scrolls away. We tried
it, disliked it, and deliberately kept the pinned bar with a rare one-frame shift. Make that
decision explicitly and write it next to the CSS rule, because the next person to see the flicker
will reach for exactly the fix that was rejected.

## 3. Never force layer promotion on a pinned bar

`transform: translateZ(0)` / `will-change: transform` on a sticky header is the standard advice
for repaint flicker. Here it made the header **disappear outright** for a frame during a
back-swipe — a sticky element on its own compositor layer can be composited with the page's new
scroll offset applied but its sticky adjustment not yet recomputed, so it is drawn at its static
position hundreds of pixels above the viewport.

A bar that shifts 2px is a nit. A bar that vanishes is a bug. Do not trade the first for the
second.

## 4. Most "flicker" is not compositing — it is the bar changing

Before blaming the renderer, check whether the top bar is genuinely identical on both screens.
Two things mutated it here, and both read as a rendering glitch:

- **A back button added and removed.** Measured: the title moved 54px sideways on every push and
  pop. Either reserve its space permanently (`visibility`, not `hidden`) or — better — put the
  back control on the screen rather than in the shared bar, where it can also name its
  destination.
- **The active tab/nav item blanking out.** A detail route matches no tab, so the selection
  indicator vanished on every push and returned on every pop. **This one is very likely present
  in any tabbed app** and is trivially fixed: a detail screen keeps the tab it was opened from
  lit, which is more correct anyway.

Serialise the whole bar — position, height, a title's x, *and* which item is selected — and
assert it is byte-identical across a traversal. If it is not, that is your flicker, and no amount
of positioning work will touch it.

## 5. Your instrumentation must watch the thing that can break

Twice here a check reported "header stable" while the header was, respectively, moving 54px and
vanishing entirely. It compared `top` and `height`, which never changed.

**For anything visual, record the screen and step through frames.** A 2.5-second screen recording
found in one frame what two rounds of property assertions had missed. `ffmpeg -ss <t> -vsync 0`
into a montage is enough; you do not need tooling.

Corollary: an assertion that passes because it is asking the wrong question is worse than no
assertion, because it ends the investigation.

## 6. Playwright's `click()` will lie to you about scroll

`page.click()` scrolls the target into view first. In a scroll-restoration test that silently
changes the offset *before* the navigation, so the app saves and restores a different number than
you think, and the test reports a bug that is not there. One round was spent on that.

For scroll tests, click programmatically:

```js
await page.evaluate(() => document.querySelectorAll(".row")[3].click());
```

## 7. Two engines, two paths — test both

Chromium has the Navigation API, so that is the path your tests exercise. Safari may take the
`popstate` fallback. Delete the API in the test context and run the suite again:

```js
await context.addInitScript(() => { delete window.navigation; });
```

Also worth knowing: `destination.getState()` is the *Navigation API's* state, not what
`history.pushState` wrote. It comes back `undefined`, and if you key per-entry data on it you get
silent nulls. Use the entry's `key`.

## 8. `overflow-x: hidden` on `<html>` breaks scroll restoration

It makes the element a scroll container, and restoring an offset into a document the browser no
longer treats as the scroller leaves an **unpainted viewport** until a touch forces a repaint —
"blank until I scroll, then it comes back".

Use `overflow-x: clip` on the body instead: same clipping, no scroll container, no effect on
restoration. Worth grepping for, since it is a very common defensive rule.

## 9. Cache headers, or you will debug ghosts

Assets embedded with `go:embed` have a **zero modification time**, so `net/http` emits no
`Last-Modified` and no `ETag`. Browsers fall back to heuristic freshness and keep serving the old
bundle. A fix that is deployed and verified server-side is invisible on the device, and you will
chase a bug you already fixed — screenshots arrive of a version that no longer exists.

Set `Cache-Control: no-cache` plus an `ETag` (the build stamp is fine). The common case is then a
304 with no body. Hashed filenames handle the sub-resources, but `index.html` itself still needs
this.

## 10. "Not loaded" is not "empty"

Before the first response, an empty array renders as "no devices found" — a confident wrong
answer on the screen the app opens to, replaced a moment later. Track loaded-ness separately.

Two things about skeletons, both learned the hard way:

- Put the first one in the **HTML**, not in script, so it is on screen at first paint.
- Make it the **shape of what it becomes**, and check the contrast against the surface it sits
  on. The first version here used the same colour as the row background and rendered as blank
  slabs — worse than nothing, since it reads as a rendering fault. The assertion counting
  skeleton elements passed.

## 11. The meta-lesson

Two changes here made things actively worse and had to be reverted, both because they were
speculative fixes for a symptom that could not be observed from where they were written. The
device is the only oracle for this class of work.

Change one thing. Look at it on the phone. If it cannot be reproduced locally, do not ship a
guess — say so and ask for a recording.
