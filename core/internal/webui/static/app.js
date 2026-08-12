// springback — one page, four views, phone-first. No framework, no build step (SPEC §1).
//
// The Devices screen is the landing view and the reason the tool exists: it is where an app you
// own turns out to be gone from every store, and where one tap does something about it.
//
// NO WIDE TABLES. A device holds 162 apps and the screen that lists them is read on a phone, so
// every list is a stack of rows that tap through to a detail view. The detail view is the one
// place that shows everything about an app and every action on it — from the library or from a
// device, downloaded or not.

"use strict";

const $ = (sel) => document.querySelector(sel);
const el = (tag, props = {}, kids = []) => {
  const n = Object.assign(document.createElement(tag), props);
  for (const k of [].concat(kids)) if (k != null) n.append(k);
  return n;
};

async function api(path, opts = {}) {
  const res = await fetch(path, { headers: { "Content-Type": "application/json" }, ...opts });
  const text = await res.text();
  let body = null;
  try { body = text ? JSON.parse(text) : null; } catch { /* not json */ }
  if (!res.ok) {
    // A SESSION CAN EXPIRE UNDER A PAGE THAT IS ALREADY OPEN — after a fortnight idle, or a
    // server restart. Every screen would otherwise fill with "HTTP 401" and the user would
    // have no way to act on it. Put the gate back instead, from wherever they were.
    //
    // ASK THE SERVER WHICH GATE, rather than assuming. A 401 says "no session"; it does NOT
    // say a password exists. This used to assume "needs_login", which on a fresh install
    // rewrote the setup screen into a sign-in form for a password nobody had set — and then
    // answered the button with "No password has been set yet."
    if (res.status === 401 && !path.startsWith("/api/auth/")) regate();
    const err = new Error((body && (body.detail || body.error)) || `HTTP ${res.status}`);
    err.kind = body && body.error;
    err.status = res.status;
    err.body = body;
    throw err;
  }
  return body;
}

// ---------------------------------------------------------------------------
// The gate
//
// springback holds live Apple ID sessions, so the door matters more than the size of the app
// behind it suggests. Two states share one form: setting the first password, and signing in.
// ---------------------------------------------------------------------------

let booted = false;

function showGate(state) {
  const setup = state === "needs_setup";
  // NOTHING KEEPS FEEDING A GATED PAGE. The server closes the socket within a ping of the
  // session dying, but the gate can also go up for reasons the server has not noticed yet, and
  // a live socket behind a login form would go on drawing somebody's devices underneath it.
  dropLive();
  document.body.classList.add("gated");
  $("#gate").hidden = false;
  $("#gate-intro").textContent = setup
    ? "Choose a password. It is the only thing standing between this box and every Apple ID signed in to it, so make it a real one."
    : "";
  $("#gate-confirm-wrap").hidden = !setup;
  $("#gate-submit").textContent = setup ? "Set password" : "Sign in";
  // The password manager needs to know which of the two this is, or it offers to fill a
  // password on the screen that is creating one.
  $("#gate-pass").setAttribute("autocomplete", setup ? "new-password" : "current-password");
  $("#gate-form").dataset.mode = setup ? "setup" : "login";
}

function hideGate() {
  $("#gate").hidden = true;
  document.body.classList.remove("gated");
  // Straight back on after a sign-in. boot() also connects, but it runs only once per document,
  // so a session that expired and was signed into again would otherwise wait out the backoff.
  liveBackoff = 1000;
  connectLive();
  // Never leave the password sitting in a field behind a page the user is still looking at.
  $("#gate-pass").value = "";
  $("#gate-pass2").value = "";
}

// applyTransport drives both insecure banners from one answer.
function applyTransport(status) {
  // Loopback is a secure context as far as the browser is concerned, and warning about
  // http://localhost would put a red box on the most common way to try springback out.
  const warn = !status.secure && !status.loopback;
  $("#gate-insecure").hidden = !warn;
  $("#insecure-banner").hidden = !warn;
}

async function authStatus() {
  const res = await fetch("/api/auth/status");
  return res.json();
}

// regate puts the right gate back after a 401, having asked which one it should be.
//
// Guarded against re-entry because several requests can fail at once — the device poll and a
// screen's own fetch — and each would otherwise start its own round trip to answer the same
// question.
let regating = false;
async function regate() {
  if (regating) return;
  regating = true;
  try {
    const st = await authStatus();
    applyTransport(st);
    // Someone signed in while this was in flight; leaving the gate up would lock them out of
    // a session they now hold.
    if (st.state !== "authenticated") showGate(st.state);
  } catch {
    // The server is unreachable, so its state is unknowable. Sign-in is the safer of the two
    // to offer: it cannot destroy anything, and setup would 409 if a password did exist.
    showGate("needs_login");
  } finally {
    regating = false;
  }
}

$("#gate-form").onsubmit = async (ev) => {
  ev.preventDefault();
  const setup = $("#gate-form").dataset.mode === "setup";
  const pass = $("#gate-pass").value;
  const btn = $("#gate-submit");

  if (setup && pass !== $("#gate-pass2").value) {
    toast("The two passwords do not match.", true);
    return;
  }
  if (btn.disabled) return;
  btn.disabled = true;
  const label = btn.textContent;
  btn.textContent = "…";
  try {
    await api(setup ? "/api/auth/setup" : "/api/auth/login", {
      method: "POST",
      body: JSON.stringify({ password: pass }),
    });
    hideGate();
    await boot();
  } catch (e) {
    toast(e.message, true);
  } finally {
    btn.disabled = false;
    btn.textContent = label;
  }
};

// leaving suppresses the router for the one navigation that is meant to LEAVE.
//
// The navigate handler intercepts every same-origin navigation, which is right for every link in
// the app and wrong for exactly one thing: signing out. `location.href = "/"` was being turned
// into an in-page route change to the Devices screen, so the document — and every device, app
// and account already in memory — survived. What the user saw was Devices for a couple of
// seconds, then the login form, once a poll happened to hit a 401.
let leaving = false;

$("#sign-out").onclick = async () => {
  // Up FIRST, before any await. Whatever happens next, the screen must not still be showing
  // somebody's devices while a request is in flight.
  showGate("needs_login");
  try { await api("/api/auth/logout", { method: "POST" }); } catch { /* going anyway */ }
  // Then a real reload rather than a route change: dropping every scrap of data already
  // rendered is the point of signing out, and only a new document guarantees it.
  leaving = true;
  location.href = "/";
};

let toastTimer;
// AN ERROR STAYS UNTIL IT IS DISMISSED; a success goes on its own.
//
// They are not the same kind of message. "Archived Aviasales." is worth six seconds and nothing
// more. A failure is often long, sometimes worth copying, and is the one thing on screen the
// reader actually has to act on — putting it on a twelve-second fuse meant the interesting ones
// vanished mid-sentence.
function toast(msg, bad = false) {
  const t = $("#toast");
  $("#toast-text").textContent = msg;
  t.hidden = false;
  t.className = bad ? "bad" : "";
  clearTimeout(toastTimer);
  if (!bad) toastTimer = setTimeout(dismissToast, 6000);
}

function dismissToast() {
  clearTimeout(toastTimer);
  $("#toast").hidden = true;
}

$("#toast-close").onclick = dismissToast;

const fmtSize = (n) => {
  if (!n) return "—";
  const mb = n / (1024 * 1024);
  return mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${Math.round(mb)} MB`;
};
const fmtDate = (s) => (s ? new Date(s).toLocaleString() : "—");
const statusLabel = (s) => (s === "not_listed" ? "not listed" : s.toUpperCase());

// cmpVersions compares two App Store version strings: >0 if a is newer.
//
// Dot-separated numeric parts, compared as NUMBERS — "21.40.0" is newer than "21.31.3", which a
// string comparison gets backwards. Non-numeric parts fall back to a string comparison of the
// whole thing, which is at least stable; the versions Apple actually ships are numeric.
function cmpVersions(a, b) {
  const pa = String(a).split(".");
  const pb = String(b).split(".");
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const na = parseInt(pa[i] ?? "0", 10);
    const nb = parseInt(pb[i] ?? "0", 10);
    if (Number.isNaN(na) || Number.isNaN(nb)) return a === b ? 0 : (a > b ? 1 : -1);
    if (na !== nb) return na > nb ? 1 : -1;
  }
  return 0;
}

// emailForSlug turns an account directory name back into the Apple ID it stands for. The slug is
// a filesystem-safe mangling (`someone@example.com` -> `someone-at-example.com`) and has no
// business appearing in the UI, but it is what the library records against each download.
const emailForSlug = (slug) => {
  const acc = accounts.find((a) => a.slug === slug);
  return acc ? acc.email : slug;
};

// ---------------------------------------------------------------------------
// Shared state
// ---------------------------------------------------------------------------

let accounts = [];
let devices = [];
// devicesLoaded distinguishes "no devices" from "not asked yet" — see renderDevices.
let devicesLoaded = false;
let library = [];
const appsCache = new Map(); // udid -> payload
let current = "devices";
let detail = null; // { app, deviceUdid } | { item }

// ---------------------------------------------------------------------------
// Jobs — the progress strip.
//
// Downloads and installs no longer hold the request open, so this is where the user finds out
// what is happening. Pushed over the socket while it is up, and polled once a second while
// anything is running when it is not.
// ---------------------------------------------------------------------------

let jobTimer = null;
let knownJobs = new Map();
let runningJobs = [];

// applyJobs takes a job list from EITHER source — a push or a fetch — and is the only place that
// knows what a job list means. Two sources, one interpretation: a socket that delivers a finished
// download and a poll that finds one must produce exactly the same toast, the same cache
// invalidation and the same redraw, and the way to guarantee that is to have one function do it.
async function applyJobs(list) {
  runningJobs = list.filter((j) => j.state === "running");
  const strip = $("#jobs");
  // Downloads keep the top strip — they belong to no row. Installs are drawn IN the device
  // row instead, so showing them twice would be the duplicate-count mistake again.
  // The strip carries downloads only.
  strip.replaceChildren(...runningJobs.filter((j) => j.kind !== "install").map(jobRow));

  let finished = false;
  for (const j of list) {
    const was = knownJobs.get(j.id);
    knownJobs.set(j.id, j.state);
    if (was === "running" && j.state !== "running") {
      finished = true;
      if (j.state === "done") {
        toast(j.kind === "install" ? `Installed ${j.label}.`
          : `Archived ${j.label}.`);
        // The library gained an entry, a device's app list may now say "in library",
        // and an installed-app set is now out of date.
        appsCache.clear();
        installedOn.clear();
        await refreshLibrary();
      } else {
        toast(`${j.label}: ${j.error}`, true);
      }
    }
  }

  // Re-render while jobs are running so the in-row rings advance, and once more when one
  // ends so the row settles into its finished state.
  if (runningJobs.length || finished) rerenderCurrent();
}

// pollJobs is the fallback: it runs only while the socket is down, and stops as soon as nothing
// is happening. With the socket up the server pushes the same list four times a second.
async function pollJobs() {
  clearTimeout(jobTimer);
  if (liveConnected) return;
  let list = [];
  try { list = await api("/api/jobs"); } catch { return; }
  await applyJobs(list);
  if (runningJobs.length) jobTimer = setTimeout(pollJobs, 1000);
}

function jobRow(j) {
  const pct = j.percent >= 0 ? j.percent : null;
  const bar = el("div", { className: "bar" }, [
    el("div", { className: "bar-fill", style: `width:${pct == null ? 0 : pct}%` }),
  ]);
  // THE STAGE NAMES THE VERB. A download ends with ipatool rewriting the whole archive to add
  // the App Store metadata — tens of seconds on a large app, with its own progress — and calling
  // that "Downloading" while the byte counter has stopped moving is what made it look hung.
  const what = j.kind === "install" ? `Installing ${j.label}`
    : j.stage === "signing" ? `Signing ${j.label}`
    : `Downloading ${j.label}`;
  const where = j.target ? ` → ${j.target}` : "";
  return el("div", { className: "job" }, [
    el("div", { className: "job-line" }, [
      el("span", { textContent: what + where }),
      el("span", {
        className: "job-pct",
        // "starting" rather than 0%: ipatool prints nothing for the first second or two,
        // and a bar pinned at zero is indistinguishable from a stalled one.
        textContent: pct == null ? (j.stage || "starting…") : `${pct}%`,
      }),
    ]),
    bar,
    j.detail ? el("div", { className: "job-detail", textContent: j.detail }) : null,
  ]);
}

// startedJob is what a button calls after asking for work. With the socket up there is nothing to
// do: the server publishes the moment the job exists, which is before this even runs.
function startedJob() {
  if (liveConnected) return;
  clearTimeout(jobTimer);
  jobTimer = setTimeout(pollJobs, 300);
}

// ---------------------------------------------------------------------------
// Devices
// ---------------------------------------------------------------------------

// applyDevices takes a device list from either source and redraws only if something actually
// changed — the same rule as applyJobs, and for the sharper reason: the device page holds a search
// box, and rebuilding it takes the caret and the phone's keyboard away mid-word.
//
// The comparison is over the WHOLE list rather than a chosen few fields. An earlier version
// compared udid, name and reachability only, which is every field that changes often and not every
// field that changes — a device whose iOS version or region was read a moment late never redrew.
function applyDevices(list) {
  const before = JSON.stringify(devices);
  devices = list || [];
  devicesLoaded = true;
  if (before === JSON.stringify(devices)) return false;
  // A device coming or going changes more than the list: its pairing state and transport are
  // read separately and go stale with it.
  if (current === "device" && deviceUDID) loadDeviceState(deviceUDID);
  if (current === "devices" || current === "app" || current === "device") rerenderCurrent();
  return true;
}

async function refreshDevices() {
  applyDevices(await api("/api/devices"));
}

function renderDevices() {
  // NOT LOADED YET IS NOT THE SAME AS NONE, and conflating them is what made the landing screen
  // ugly: before the first response `devices` is an empty array, so the screen rendered "No
  // paired devices yet" — a confident wrong answer — and then replaced it a moment later. The
  // markup in index.html is already the skeleton, so leaving it alone is the whole fix.
  if (!devicesLoaded) return;

  const root = $("#screen-devices");
  const frag = [
    el("h2", { className: "screen", textContent: "Devices" }),
    el("p", {
      className: "screen-hint",
      textContent: "Tap a device for its apps, pairing and Wi-Fi sync.",
    }),
  ];

  if (!devices.length) {
    frag.push(el("p", {
      className: "empty",
      textContent:
        "No paired devices yet. springback reads the pairing records this box already has.",
    }));
  }

  frag.push(el("div", { className: "list" }, devices.map(deviceRow)));
  frag.push(rescanFoot());
  root.replaceChildren(...frag);
}

// rescanFoot is the Refresh under the list, and the sentence next to it that says what Refresh
// cannot do.
//
// WHAT IT ACTUALLY DOES is ask the same question the watcher asks every five seconds, immediately.
// That is the whole of springback's power over the matter: it does not own the USB bus and never
// has — it runs `idevice_id` and believes the answer. The case where a device is plugged in and
// genuinely absent is a muxer that has given up on the port, and no button here can fix it. Saying
// so is worth more than the button: it is the difference between one restart and half an hour of
// tapping Refresh.
//
// BELOW THE LIST, NOT ABOVE IT. The list is what the screen is for and it should be the first
// thing under the heading; an action bar above it would push the content down on every visit to
// serve the rare one.
function rescanFoot() {
  const b = el("button", { className: "link plain", type: "button", textContent: "Refresh" });
  b.onclick = async () => {
    if (b.disabled) return;
    b.disabled = true;
    b.textContent = "Refreshing…";
    try {
      applyDevices(await api("/api/devices/rescan", { method: "POST" }));
    } catch (e) {
      toast(e.message, true);
    }
    // renderDevices may already have replaced this node from inside applyDevices, in which
    // case restoring the label is writing to something that has left the document — harmless,
    // and cheaper than working out which case this is.
    b.disabled = false;
    b.textContent = "Refresh";
  };

  return el("div", { className: "list-foot" }, [
    b,
    el("p", { className: "hint", textContent:
      "This list keeps itself up to date. If a device you have just plugged in is still missing, " +
      "the muxer is the thing to restart — springback asks it what is connected and cannot make it " +
      "look again." }),
  ]);
}

// deviceLabel disambiguates devices that share a name.
//
// TWO PHONES CAN HAVE THE SAME NAME, and on the stand this was built against, two did: one name,
// two handsets, two udids, two models. Rendered plainly they are identical rows with no way to
// tell which one you are about to install onto. A name is a label the owner chose; the udid is
// the identity.
function deviceLabel(d) {
  const name = d.name || d.udid;
  const clashes = devices.filter((o) => (o.name || o.udid) === name).length > 1;
  if (!clashes || !d.udid) return name;
  return `${name} · ${d.udid.slice(-6)}`;
}

// deviceRow is a link to the device's own page.
//
// IT USED TO EXPAND IN PLACE, and that stopped being the right shape once a device grew pairing
// controls and a Wi-Fi switch: those are settings for one device, not a list of apps, and an
// accordion holding two hundred rows plus a settings panel is a page pretending to be a row. It
// is an <a href> for the same reason every other row is — the browser drives the navigation, the
// back gesture and the scroll restoration, and none of it has to be reimplemented.
function deviceRow(d) {
  const r = el("a", { className: "row", href: `/device/${encodeURIComponent(d.udid)}` }, [
    el("div", { className: "row-main" }, [
      el("div", { className: "row-title", textContent: deviceLabel(d) }),
      el("div", {
        className: "row-sub",
        textContent: [d.model || d.product_type, d.ios && `iOS ${d.ios}`, d.region].filter(Boolean).join(" · "),
      }),
    ]),
    el("div", { className: "row-right" }, [
      d.reachable
        ? el("span", { className: "pill live", textContent: "reachable" })
        // "not currently reachable", never "gone": a sleeping iPhone drops off mDNS
        // entirely, and that is normal.
        : el("span", { className: "pill offline", textContent: "offline" }),
      el("span", { className: "chev", textContent: "›" }),
    ]),
  ]);
  return r;
}

// ---------------------------------------------------------------------------
// One device: its settings, and its apps.
// ---------------------------------------------------------------------------

// deviceState caches the pairing/Wi-Fi answer per udid. Two extra device round trips, so not
// something to repeat on every poll-driven re-render.
const deviceState = new Map();
let deviceUDID = null;
// appFilter is the search box's text, kept out of the DOM so a re-render cannot lose it.
let appFilter = "";

async function loadDeviceState(udid) {
  try {
    deviceState.set(udid, await api(`/api/devices/${encodeURIComponent(udid)}`));
  } catch { /* the page says what it knows */ }
  if (current === "device") renderDevice();
}

async function loadApps(udid) {
  if (appsCache.has(udid)) return;
  try {
    appsCache.set(udid, await api(`/api/devices/${encodeURIComponent(udid)}/apps`));
  } catch (e) {
    appsCache.set(udid, { error: e.message, apps: [] });
  }
  if (current === "device") renderDevice();
}

function renderDevice() {
  const root = $("#screen-device");
  const udid = deviceUDID;
  if (!udid) { root.replaceChildren(); return; }

  const st = deviceState.get(udid) || {};
  // THE POLLED LIST WINS ON THE FACTS. deviceState is fetched once when the page is opened and
  // holds a snapshot; `devices` is refreshed every five seconds. Reading reachability from the
  // snapshot meant a device unplugged while its page was open still called itself "reachable"
  // here while the Devices list correctly said "offline" — the same device, two answers, and
  // the stale one on the page you were looking at.
  const d = devices.find((x) => x.udid === udid) || st.device || { udid };
  const payload = appsCache.get(udid);

  const back = el("button", { className: "detail-back", type: "button" }, ["‹ Devices"]);
  back.onclick = () => { if (history.length > 1) history.back(); else navigate("/"); };

  // REFRESH IS A BUTTON BECAUSE NOT EVERYTHING ON THIS PAGE POLLS. The device list is re-read
  // every five seconds and the pairing state follows it, but the app scan is deliberately not:
  // it asks Apple about every installed app and takes half a minute, so it is fetched once and
  // cached. Plug a device back in and the rest of the page catches up on its own while the app
  // list stays as it was — which is what a page reload was being used to fix.
  const refresh = el("button", { className: "link plain", type: "button", textContent: "Refresh" });
  refresh.onclick = async () => {
    if (refresh.disabled) return;
    refresh.disabled = true;
    refresh.textContent = "Refreshing…";
    appsCache.delete(udid);
    installedOn.delete(udid);
    try { await refreshDevices(); } catch { /* the page says what it knows */ }
    await loadDeviceState(udid);
    await loadApps(udid);
  };

  const head = el("div", { className: "detail-head" }, [
    el("h2", { className: "screen", textContent: deviceLabel(d) }),
    d.reachable
      ? el("span", { className: "pill live", textContent: "reachable" })
      : el("span", { className: "pill offline", textContent: "offline" }),
    refresh,
  ]);

  const facts = [];
  const fact = (k, v) => { if (v) facts.push(el("div", { className: "fact" }, [
    el("dt", { textContent: k }), el("dd", { textContent: String(v) }),
  ])); };
  fact("Model", d.model || d.product_type);
  // The raw identifier stays on the page: the marketing name comes from a table that will
  // one day not know a device, and this is the thing you would paste into a search.
  if (d.model && d.model !== d.product_type) fact("Identifier", d.product_type);
  fact("iOS", d.ios);
  fact("Region", d.region);
  fact("UDID", d.udid);
  fact("Connected over", d.reachable
    ? (st.transport === "usb" ? "USB" : st.transport === "network" ? "Wi-Fi" : null)
    : null);
  // Only for a device that is NOT answering. For one that is, "last seen" is now — a fact
  // about nothing — and the row above already says how it is connected.
  if (!d.reachable && d.last_seen) fact("Last seen", fmtDate(d.last_seen));

  const blocks = [back, head, el("dl", { className: "facts" }, facts)];
  blocks.push(...pairingBlock(udid, st));
  blocks.push(...appsBlock(udid, d, payload));
  root.replaceChildren(...blocks);
}

// pairingBlock is the settings half of the page: is this host trusted by the device, and will
// the device answer when it is not plugged in.
function pairingBlock(udid, st) {
  const out = [el("h3", { className: "sub-head", textContent: "Pairing" })];

  if (st.can_pair === false) {
    // A read-only pairing directory is the correct setup when something else owns the
    // records, so this explains rather than complains.
    out.push(el("p", { className: "note plain", textContent:
      "The pairing directory is mounted read-only, so springback can read pairing records but not write them. " +
      "Pair the device with whatever owns them, or mount it read-write." }));
  }

  const pair = st.pair || "unknown";
  const label = pair === "paired" ? "Paired with this host"
    : pair === "unpaired" ? "Not paired with this host"
    : "Pairing state unknown — the device is not answering";
  out.push(el("p", { className: "hint", textContent: label }));

  if (pair === "unpaired" && st.can_pair !== false) {
    const usb = st.transport === "usb";
    out.push(el("p", { className: "hint", textContent: usb
      ? "Unlock the device and tap Trust when it asks. Pairing only needs the cable once — after that Wi-Fi is enough."
      : "Connect the device with a USB cable to pair it. Wireless pairing exists in the protocol but only for Apple TV." }));
    const b = el("button", { className: "primary wide", textContent: "Pair this device" });
    b.disabled = !usb;
    b.onclick = async () => {
      b.disabled = true; b.textContent = "Pairing…";
      try {
        await api(`/api/devices/${encodeURIComponent(udid)}/pair`, { method: "POST" });
        toast("Paired.");
      } catch (e) { toast(e.message, true); }
      b.textContent = "Pair this device"; b.disabled = false;
      await loadDeviceState(udid);
    };
    out.push(b);
  }

  if (pair === "paired" && st.can_pair !== false) {
    const b = el("button", { className: "danger wide", textContent: "Unpair" });
    b.onclick = async () => {
      if (!confirm("Unpair this device? springback will not be able to reach it again until it is paired over USB.")) return;
      b.disabled = true;
      try {
        await api(`/api/devices/${encodeURIComponent(udid)}/unpair`, { method: "POST" });
        toast("Unpaired.");
      } catch (e) { toast(e.message, true); }
      b.disabled = false;
      await loadDeviceState(udid);
    };
    out.push(b);
  }

  // Wi-Fi sync is only meaningful once paired: reading the flag needs a trusted session, and
  // writing it needs one too.
  if (pair === "paired") {
    out.push(el("h3", { className: "sub-head", textContent: "Wi-Fi sync" }));
    const wifi = st.wifi_sync || "unknown";
    out.push(el("p", { className: "hint", textContent:
      "With this off the device only answers over USB — it drops off the network entirely, and springback stops seeing it." }));

    if (wifi === "unknown") {
      out.push(el("p", { className: "hint", textContent: "Could not read the setting from this device." }));
    } else {
      const on = wifi === "on";
      const b = el("button", { className: on ? "danger wide" : "primary wide",
        textContent: on ? "Turn Wi-Fi sync off" : "Turn Wi-Fi sync on" });
      b.onclick = async () => {
        if (on && !confirm("Turn Wi-Fi sync off? The device will leave the network and only answer over USB.")) return;
        b.disabled = true;
        try {
          await api(`/api/devices/${encodeURIComponent(udid)}/wifi-sync`, {
            method: "POST", body: JSON.stringify({ enable: !on }),
          });
          toast(on ? "Wi-Fi sync off." : "Wi-Fi sync on.");
        } catch (e) { toast(e.message, true); }
        b.disabled = false;
        await loadDeviceState(udid);
      };
      out.push(b);
    }
  }
  return out;
}

// appsBlock is the at-risk scan, its summary, and the search box over it.
function appsBlock(udid, d, payload) {
  const out = [el("h3", { className: "sub-head", textContent: "Apps" })];

  if (!d.reachable) {
    out.push(el("p", { className: "note plain", textContent:
      "This device is not answering, so its apps cannot be listed. A sleeping iPhone drops off the network entirely; wake it and come back." }));
    return out;
  }
  if (!payload) {
    loadApps(udid);
    out.push(el("p", { className: "hint spinner", textContent:
      "Reading the app list and asking each App Store about it. The first scan takes about half a minute." }));
    return out;
  }
  if (payload.error) {
    out.push(el("div", { className: "error", textContent: payload.error }));
    return out;
  }

  const { apps, storefronts, total, delisted, unknown } = payload;
  const summary = el("div", { className: "summary" });
  // "on novkostya-iphone", not "on this novkostya-iphone" — the name is already specific, so
  // the demonstrative just gets in the way. It comes back only for a device with no name to
  // use, where "on device" would read like a dropped word.
  const where = d.name ? d.name : "this device";
  summary.append(el("p", { className: "summary-line" }, delisted > 0 ? [
    el("strong", { textContent: `${delisted} of ${total} apps` }),
    ` on ${where} ${delisted === 1 ? "is" : "are"} no longer in the App Store.`,
  ] : [`All ${total} apps here are still in an App Store somewhere.`]));
  summary.append(el("p", {
    className: "sub",
    textContent:
      `Checked ${(storefronts || []).join(", ")} plus each app's own storefront. An app counts as ` +
      `delisted only when every one of them comes back empty.` +
      (payload.not_listed ? ` ${payload.not_listed} never had a public listing and are not counted.` : "") +
      (unknown ? ` ${unknown} could not be checked.` : ""),
  }));
  out.push(summary);

  // SEARCH, because two hundred rows is past what scrolling answers. Filtered here rather than
  // by re-asking the server: the whole list is already in hand and a round trip per keystroke
  // would be slower and worse.
  const search = el("input", {
    type: "search", className: "search", id: "app-search",
    placeholder: `Search ${total} apps`, value: appFilter,
    autocapitalize: "none", autocorrect: "off", spellcheck: false,
  });
  search.oninput = () => {
    appFilter = search.value;
    // Only the list is redrawn, so the field keeps focus and the caret stays put — a full
    // re-render would drop the keyboard on a phone after every letter.
    drawAppList(udid, apps, $("#app-list"), $("#app-count"));
  };
  out.push(el("div", { className: "search-wrap" }, [search]));
  out.push(el("p", { className: "hint", id: "app-count" }));

  const list = el("div", { className: "list", id: "app-list" });
  out.push(list);
  // Deferred until the nodes are in the document, since drawAppList looks them up by id.
  queueMicrotask(() => drawAppList(udid, apps, $("#app-list"), $("#app-count")));
  return out;
}

function matchesFilter(a, needle) {
  if (!needle) return true;
  const hay = `${a.store_name || ""} ${a.name || ""} ${a.bundle_id || ""} ${a.artist || ""}`.toLowerCase();
  return hay.includes(needle);
}

function drawAppList(udid, apps, list, count) {
  if (!list) return;
  const needle = appFilter.trim().toLowerCase();
  const shown = apps.filter((a) => matchesFilter(a, needle));
  const device = devices.find((x) => x.udid === udid) || { udid };
  list.replaceChildren(...shown.map((a) => appRow(a, device)));
  if (count) {
    count.textContent = needle
      ? `${shown.length} of ${apps.length} match “${appFilter.trim()}”.`
      : "";
  }
  if (needle && shown.length === 0) {
    list.replaceChildren(el("p", { className: "empty", textContent: "Nothing matches that." }));
  }
}


// appRow is deliberately three lines and a chip — it has to be readable on a phone, and the
// details live one tap away rather than in a row that scrolls sideways.
function appRow(a, device) {
  // A ROW THAT NAVIGATES IS AN <a href>, not a button with a click handler: it gives the browser
  // an ordinary navigation to drive, which is what makes its own back gesture, history and scroll
  // restoration work without being reimplemented.
  const r = el("a", {
    className: "row",
    href: `/device/${encodeURIComponent(device.udid)}/${encodeURIComponent(a.bundle_id)}`,
  }, [
    deviceIcon(device.udid, a.bundle_id, a.version, a.store_name || a.name || a.bundle_id, "sm"),
    el("div", { className: "row-main" }, [
      el("div", { className: "row-title", textContent: a.store_name || a.name || a.bundle_id }),
      el("div", { className: "row-sub", textContent: a.bundle_id }),
    ]),
    // Stacked, not side by side: the store verdict is the primary fact and "in library" is a
    // second, independent one. In a row they competed for the same horizontal space and the
    // trailing grey text read as a fragment of the badge next to it.
    el("div", { className: "row-right stack" }, [
      el("span", { className: `status ${a.store_status}`, textContent: statusLabel(a.store_status) }),
      a.in_library ? el("span", { className: "badge library", textContent: "in library" }) : null,
    ]),
  ]);
  r.onclick = () => { detail = { app: a, device }; };
  return r;
}

// ---------------------------------------------------------------------------
// App detail — one view for both states, reached from a device or from the library.
// ---------------------------------------------------------------------------

function renderAppDetail() {
  const root = $("#screen-app");
  if (!detail) { root.replaceChildren(); return; }

  const a = detail.app || null;
  const item = detail.item || library.find((i) => a && i.bundle_id === a.bundle_id) || null;
  const appID = (a && a.app_id) || (item && item.id) || 0;
  const title = (a && (a.store_name || a.name)) || (item && item.name) || (a && a.bundle_id) || "App";

  const facts = [];
  const fact = (k, v) => { if (v) facts.push(el("div", { className: "fact" }, [
    el("dt", { textContent: k }), el("dd", { textContent: String(v) }),
  ])); };

  fact("Bundle id", (a && a.bundle_id) || (item && item.bundle_id));
  fact("Version", (a && a.version) || (item && item.version));
  fact("App Store id", appID || null);
  fact("Developer", (a && a.artist) || (item && item.artist));
  fact("Bought with", a && a.owner_apple_id);
  fact("Storefront", a && a.storefront && a.storefront.toUpperCase());

  // TWO SIZES, BECAUSE THEY ARE DIFFERENT FACTS AND EITHER CAN BE MISSING.
  //
  //   Download   — what the store says the .ipa weighs. Exact, and the number that answers
  //                "how long will this take". Does not exist for a delisted app: there is no
  //                store record left to carry one, which is the whole premise of this tool.
  //   On device  — what the installed app occupies. Available for anything installed,
  //                INCLUDING delisted apps, so it is the only estimate for exactly the case
  //                where the store has nothing to say.
  //
  // Not the same number, and not presented as one. Measured across seven real archives, the
  // .ipa came out at 79-95% of the installed size — close enough to plan a download around,
  // far enough that quoting one as the other would be wrong.
  // fmtSize renders 0 as an em dash, which fact() would happily show as a row saying nothing —
  // so the number is tested before it is formatted, not after.
  const lookedUp = a && storeInfo.get(a.bundle_id);
  const storeSize = (a && a.store_size) || (lookedUp && lookedUp.size) || 0;
  const onDevice = (a && a.disk_usage) || 0;
  if (storeSize > 0) fact("Download", fmtSize(storeSize));
  if (onDevice > 0) fact("On device", fmtSize(onDevice));

  if (item) {
    fact("Downloaded", fmtDate(item.downloaded_at));
    fact("Size", fmtSize(item.size));
    // The SLUG is a directory name — `novkostya-at-gmail.com` — and it was being shown to
    // the user as though it were an address. Resolve it back to the real Apple ID; fall
    // back to the slug only if the account has since been removed.
    fact("With account", emailForSlug(item.account_slug));
  }

  // BACK LIVES ON THE SCREEN, NOT IN THE HEADER. In the header it had to either take space on
  // every screen or be added and removed, and the second reflowed the title by 54px on every
  // navigation. Here it belongs to the thing it goes back from, names where it goes, and the
  // header never changes at all.
  const backLabel = parentTab() === "library" ? "Library" : "Devices";
  const back = el("button", { className: "detail-back", type: "button" }, [`‹ ${backLabel}`]);
  back.onclick = () => {
    // history.back() when there IS somewhere to go back to, so the entry is popped rather than
    // a duplicate pushed. On a cold deep link there is not, and going to the list is right.
    if (history.length > 1) history.back();
    else navigate(parentTab() === "library" ? "/library" : "/");
  };

  const head = el("div", { className: "detail-head" }, [
    el("h2", { className: "screen", textContent: title }),
    a ? el("span", { className: `status ${a.store_status}`, textContent: statusLabel(a.store_status) }) : null,
    item ? el("span", { className: "badge library", textContent: "in library" }) : null,
  ]);

  // TWO SOURCES, ARCHIVE FIRST. A downloaded app's icon comes out of its .ipa; anything else
  // that is installed somewhere can be asked of the device itself. The archive wins when both
  // exist because it is on local disk and needs no device to be awake — and because it is the
  // flat store artwork, which matches the library list this screen is usually reached from.
  const heroIcon = item
    ? appIcon(item.id, title, "lg", item.downloaded_at)
    : (a && detail.device
        ? deviceIcon(detail.device.udid, a.bundle_id, a.version, title, "lg")
        : null);
  const hero = heroIcon ? el("div", { className: "detail-hero" }, [heroIcon, head]) : head;

  const blocks = [back, hero, el("dl", { className: "facts" }, facts)];

  if (a && a.store_status === "delisted") {
    blocks.push(el("p", { className: "note warn-note" }, [
      "This app is in none of the stores that were checked",
      a.checked && a.checked.length ? ` (${a.checked.join(", ")})` : "",
      ". If you want to keep it, archive it now — the download only works while your Apple ID's ",
      "licence still resolves.",
    ]));
  }
  if (a && a.store_status === "not_listed") {
    blocks.push(el("p", { className: "note", textContent:
      "This was never a public App Store listing — an in-house or preinstalled app. Nothing is wrong with it." }));
  }

  // --- archive ---
  if (!item) {
    if (appID) {
      const owner = a && a.owner_apple_id;
      const match = owner && accounts.find((acc) => acc.email.toLowerCase() === owner.toLowerCase());
      blocks.push(el("div", { className: "actions-block" }, [
        el("p", { className: "hint", textContent:
          owner
            ? (match
                ? `Bought with ${owner}, which is signed in here.`
                : `Bought with ${owner} — that Apple ID is not signed in, so the download will fail with "license not found".`)
            : "springback never buys anything: if the account does not already own this, the download fails." }),
        accountPicker(match && match.slug, `archive:${appID}`),
        (() => {
          const b = el("button", { className: "primary wide", textContent: "Archive to library" });
          b.disabled = !accounts.length;
          b.onclick = () => archive(appID, pickedAccount(match && match.slug, `archive:${appID}`), title, b);
          return b;
        })(),
      ]));
    } else {
      blocks.push(el("div", { className: "actions-block" }, [
        el("p", { className: "note", textContent:
          "This device holds no purchase receipt for this app — usually a sideloaded or developer-signed build, which the App Store has no copy of to download." }),
        el("label", { textContent: "App Store id, if you know it" }, [
          el("input", { type: "number", id: "manual-appid", min: "1", placeholder: "123456789" }),
        ]),
        accountPicker(null, `manual:${(a && a.bundle_id) || "x"}`),
        (() => {
          const b = el("button", { className: "primary wide", textContent: "Archive to library" });
          b.disabled = !accounts.length;
          b.onclick = () => {
            const id = parseInt($("#manual-appid").value, 10);
            if (!id) { toast("A numeric App Store id is required.", true); return; }
            archive(id, pickedAccount(null, `manual:${(a && a.bundle_id) || "x"}`), title, b);
          };
          return b;
        })(),
      ]));
    }
  }

  // --- update ---
  //
  // WHAT THIS IS ACTUALLY FOR, stated accurately after being checked on a device.
  //
  // An app installed from another storefront DOES appear in the App Store's own Updates list —
  // it is not stranded. What happens is that updating it prompts "Sign In to the App Store" for
  // the Apple ID that OWNS it, which is not the one the device is signed into. So the App Store
  // route works if you have that password and are willing to type it on the phone, every time.
  //
  // Updating from here needs neither: springback already holds that account's session, and the
  // install is a plain install. That is a smaller claim than "the only way to update it" — which
  // is what this comment said first, and it was wrong.
  if (item) {
    const upd = el("div", { className: "actions-block" });
    // RE-DOWNLOAD WITH THE ACCOUNT THAT DOWNLOADED IT. An update is the same fetch as the
    // original, so the licence that worked before is the one that will work again; any other
    // account fails with "license not found". meta.json recorded which it was, and this screen
    // is already showing it as "With account" two blocks up — defaulting to a different one
    // contradicted the page's own facts.
    const updateWith = item.account_slug;
    // Two sources for the current store version, because this screen is reached two ways: from a
    // device (the app record carries it) and from the Library (nothing does, so it is looked up).
    const si = storeInfo.get(item.bundle_id);
    const storeVersion = (a && a.store_version) || (si && si.version) || "";
    const known = !!storeVersion;
    loadStoreInfo(item.bundle_id);
    if (known && storeVersion !== item.version) {
      upd.append(
        el("p", { className: "note warn-note" }, [
          `The App Store has ${storeVersion}; your copy is ${item.version}. `,
          "Re-download to update it — then install it again on any device below.",
        ]),
        (() => {
          const b = el("button", { className: "primary wide", textContent: `Update to ${storeVersion}` });
          b.onclick = () => archive(item.id, pickedAccount(updateWith, `update:${item.id}`), item.name, b);
          return b;
        })(),
      );
    } else {
      upd.append(
        el("p", { className: "hint", textContent:
          known
            ? `Up to date — the App Store has ${storeVersion} and so do you.`
            : "Re-download to pick up a newer version, if there is one. Updating an app from another " +
              "storefront here avoids the App Store's prompt for the owning Apple ID's password." }),
        (() => {
          const b = el("button", { className: "danger wide", textContent: "Re-download latest" });
          b.onclick = () => archive(item.id, pickedAccount(updateWith, `update:${item.id}`), item.name, b);
          return b;
        })(),
      );
    }
    if (accounts.length) blocks.push(el("h3", { className: "sub-head", textContent: "Update" }), accountPicker(updateWith, `update:${item.id}`), upd);
  }

  // --- install, as a LIST OF DEVICES with one tap each ---
  if (item) {
    blocks.push(el("h3", { className: "sub-head", textContent: "Install on" }));
    if (!devices.length) {
      blocks.push(el("p", { className: "hint", textContent: "No paired devices." }));
    }
    blocks.push(el("div", { className: "list" }, devices.map((d) => installRow(d, item))));
    // Ask each reachable device what it already has, so a device that has this app says so
    // instead of offering to install it again. One cheap call per device, no store lookups.
    loadInstalledSets();
    loadStoreInfo(item.bundle_id);
    blocks.push(el("p", { className: "note", textContent:
      "A different Apple ID on the device is fine. iOS asks for the Apple ID that owns the app the first time it is opened, not now — and it works from then on." }));

    const del = el("button", { className: "danger wide", textContent: "Delete from library" });
    del.onclick = async () => {
      if (!confirm(`Delete ${item.name} from the library? The .ipa is removed from disk.`)) return;
      try {
        await api(`/api/library/${item.id}`, { method: "DELETE" });
        toast(`Deleted ${item.name}.`);
        await refreshLibrary();
        appsCache.clear();
        navigate("/library");
      } catch (e) { toast(e.message, true); }
    };
    blocks.push(el("div", { className: "actions-block" }, [del]));
  }

  root.replaceChildren(...blocks);
}

// installRow is a device with ONE affordance and one state, and every state is distinguishable
// without reading: already installed, installing (with a ring showing how far), asleep, or a
// button that plainly says Install.
function installRow(d, item) {
  const job = jobFor(`install:${item.id}:${d.udid}`);
  const already = installedOn.get(d.udid);
  const installedVersion = already ? already.get(item.bundle_id) : undefined;
  const has = installedVersion !== undefined;
  // NEWER, not merely DIFFERENT. Comparing for inequality offered "21.31.3 → 1.0" as an update,
  // which is a downgrade — iOS refuses it, and proposing it is worse than saying nothing.
  const stale = has && item.version && cmpVersions(item.version, installedVersion) > 0;

  let right;
  if (job) {
    // The progress lives IN THE ROW, next to the device it belongs to — a strip at the top
    // of the page cannot say which of three devices is being written to.
    right = ring(job.percent, job.stage);
  } else if (has && !stale) {
    right = el("span", { className: "tick done", textContent: "installed" });
  } else if (stale && d.reachable) {
    // The device has an older build than the library copy — offer the update by name.
    right = el("span", { className: "btn-inline", textContent: "Update" });
  } else if (!d.reachable) {
    right = el("span", { className: "pill offline", textContent: "offline" });
  } else {
    right = el("span", { className: "btn-inline", textContent: "Install" });
  }

  const r = el("button", { className: "row" + (job ? " busy" : "") }, [
    el("div", { className: "row-main" }, [
      el("div", { className: "row-title", textContent: deviceLabel(d) }),
      el("div", {
        className: "row-sub",
        // THE VERSION DELTA IS THE SECOND HALF OF UPDATING. Store -> library is only useful
        // if library -> device follows, and this row is where that happens: it names what is
        // on the device and what is about to replace it.
        textContent: job
          ? (job.stage || "starting…")
          : stale
            ? `${installedVersion} → ${item.version}`
            : has
              ? `${installedVersion} · up to date`
              : [d.product_type, d.ios && `iOS ${d.ios}`].filter(Boolean).join(" · "),
      }),
    ]),
    el("div", { className: "row-right" }, [right]),
  ]);

  // A stale copy is installable even though the app is present — that IS the update.
  r.disabled = !d.reachable || !!job || (has && !stale);
  r.onclick = async () => {
    // Disable ON THE WAY IN, before the request is even sent. The reported bug was two taps
    // queuing two downloads, and the round trip is exactly the window a second tap lands in.
    // The server refuses duplicates too — this is the half that stops it feeling broken.
    if (r.disabled) return;
    r.disabled = true;
    r.classList.add("busy");
    r.querySelector(".row-right").replaceChildren(ring(-1, "starting…"));
    try {
      await api(`/api/devices/${encodeURIComponent(d.udid)}/install`, {
        method: "POST",
        body: JSON.stringify({ library_id: item.id }),
      });
      // NO TOAST HERE. The response's FairPlay note is already on this screen as a
      // permanent block, so raising it again as an overlay said the same thing twice AND
      // covered the device row the user had just tapped — the one place the progress ring
      // now lives. The row itself is the feedback.
      startedJob();
    } catch (e) {
      toast(e.message, true);
      r.disabled = false;
      r.classList.remove("busy");
      rerenderCurrent();
    }
  };
  return r;
}

// ring draws a small circular progress indicator. percent < 0 spins instead, because a ring
// pinned at zero is indistinguishable from a stalled one.
function ring(percent, title) {
  const wrap = el("span", { className: "ring", title: title || "" });
  if (percent == null || percent < 0) {
    wrap.classList.add("spin");
    return wrap;
  }
  const pct = Math.max(0, Math.min(100, percent));
  // A conic gradient is the whole implementation — no SVG, no library.
  wrap.style.setProperty("--pct", `${pct * 3.6}deg`);
  wrap.append(el("span", { className: "ring-num", textContent: String(pct) }));
  return wrap;
}

function jobFor(key) {
  return runningJobs.find((j) => j.key === key) || null;
}

// installedOn: udid -> Map of bundle id -> installed version.
//
// The VERSION is what turns "installed" into "out of date" — otherwise a row saying "installed"
// is silent about a device sitting three versions behind the library copy.
const installedOn = new Map();
const installedLoading = new Set();

// storeInfo: bundle id -> { status, version } from the lookup, for a library item opened from the
// Library screen, where there is no device app record to carry it.
const storeInfo = new Map();
const storeLoading = new Set();

function loadStoreInfo(bundleID) {
  if (!bundleID || storeInfo.has(bundleID) || storeLoading.has(bundleID)) return;
  storeLoading.add(bundleID);
  api(`/api/lookup?bundle_id=${encodeURIComponent(bundleID)}`)
    .then((r) => {
      storeInfo.set(bundleID, r);
      if (current === "app") renderAppDetail();
    })
    .catch(() => { /* the screen simply offers a re-download instead of naming a version */ })
    .finally(() => storeLoading.delete(bundleID));
}

// loadInstalledSets fills it lazily for the reachable devices, then re-renders once. Fetched
// rather than derived from appsCache because that cache is only populated for devices the user
// has expanded, and this screen is reachable straight from the library.
function loadInstalledSets() {
  for (const d of devices) {
    if (!d.reachable || installedOn.has(d.udid) || installedLoading.has(d.udid)) continue;
    installedLoading.add(d.udid);
    api(`/api/devices/${encodeURIComponent(d.udid)}/installed`)
      .then((list) => {
        installedOn.set(d.udid, new Map(list.map((a) => [a.bundle_id, a.version])));
        if (current === "app") renderAppDetail();
      })
      .catch(() => { /* a device that stopped answering simply shows Install */ })
      .finally(() => installedLoading.delete(d.udid));
  }
}

// accountPicker defaults to the Apple ID that actually bought the app.
//
// The device's own receipt names it, so there is no reason to make the user choose: the licence
// belongs to exactly one account, and picking any other produces "license not found". Preselected
// rather than forced, because a family-shared or re-purchased app can legitimately be fetched
// with a different Apple ID.
// accountChoice remembers a picker the user has changed BY HAND, and which app they changed it
// for.
//
// THE DEFAULT IS ONLY A DEFAULT. This screen is rebuilt from scratch by anything that refreshes
// it — the job poll every second, the device poll, and the two lookups this screen fires on open
// that each re-render when they land — and every rebuild made a new <select> sitting on the
// owning account again. So choosing a different Apple ID and then tapping Re-download sent the
// ORIGINAL one: the selection had been reverted underneath, usually before the tap. Reported as
// "it selects another account automatically", which is exactly what it did.
//
// Scoped to one app, so moving to a different app does not inherit a choice made for this one —
// and only an explicit change is remembered, so the recorded owner still wins by default.
let accountChoice = { key: null, slug: null };

function rememberedAccount(key) {
  if (!key || accountChoice.key !== key) return null;
  return knownSlug(accountChoice.slug) ? accountChoice.slug : null;
}

function accountPicker(preferredSlug, key) {
  if (!accounts.length) {
    return el("p", { className: "hint", textContent: "Add an Apple ID on the Accounts screen first." });
  }
  const sel = el("select", { id: "picked-account" }, accounts.map((a) =>
    el("option", { value: a.slug, textContent: a.email })));
  // Only preselect an account that is still signed in. Assigning a slug with no matching
  // option leaves the select on NO option at all — a blank picker, and a download that goes
  // nowhere — which is exactly what a removed account would have produced.
  const want = rememberedAccount(key) || preferredSlug;
  if (knownSlug(want)) sel.value = want;
  sel.onchange = () => { accountChoice = { key: key || null, slug: sel.value }; };
  return el("label", { textContent: "Download with" }, [sel]);
}

function pickedAccount(preferredSlug, key) {
  const sel = $("#picked-account");
  if (sel && sel.value) return sel.value;
  return rememberedAccount(key) || (knownSlug(preferredSlug) ? preferredSlug : (accounts[0] && accounts[0].slug));
}

const knownSlug = (slug) => !!slug && accounts.some((a) => a.slug === slug);

async function archive(appID, slug, label, btn) {
  if (!slug) { toast("Add an Apple ID on the Accounts screen first.", true); return; }
  // Same double-submit guard as the install rows: disable before the request goes out, since
  // the round trip is exactly the window a second tap lands in. The server deduplicates too.
  if (btn) {
    if (btn.disabled) return;
    btn.disabled = true;
    btn.textContent = "Starting…";
  }
  try {
    await api("/api/library", {
      method: "POST",
      body: JSON.stringify({ app_id: appID, account_slug: slug, label }),
    });
    toast(`Downloading ${label} — progress is at the top of the screen.`);
    startedJob();
  } catch (e) {
    toast(e.message, true);
    if (btn) { btn.disabled = false; btn.textContent = "Archive to library"; }
  }
}

// ---------------------------------------------------------------------------
// Icons
// ---------------------------------------------------------------------------

// tiles caches the icon element per app, keyed by `<id>:<size>`.
//
// THIS IS WHAT STOPS THE ICONS FLICKERING, and the flicker was not subtle: every screen is drawn
// with replaceChildren, and the job poll redraws the current screen ONCE A SECOND for the whole
// length of a download or install. Each redraw built a new <img>, and a new element starts
// without the `loaded` class no matter how thoroughly the browser has the file — measured
// immediately after a redraw as `complete=true, opacity=0`. So every icon on screen replayed its
// 180ms fade-in every second, for minutes at a time.
//
// Handing back the SAME NODE fixes it at the root: replaceChildren re-parents an existing
// element rather than replacing it, which does not reload, re-decode or restyle the image.
// Nothing about it is in flight, so there is nothing to fade.
//
// The invariant this relies on: one (id, size) pair appears at most once in any single render. A
// DOM node cannot be in two places, so a second use would silently steal it from the first.
// Holds today — the list uses `sm`, the detail hero uses `lg`, one row per app.
const tiles = new Map();

// appIcon returns the icon tile for a library app: the real artwork if the archive had any, and
// a lettered tile if not.
//
// THE MONOGRAM IS RENDERED FIRST AND THE IMAGE FADES IN OVER IT. The alternative — an empty box
// that becomes an icon — reflows nothing but flashes on every list paint, and roughly one app in
// this library legitimately has no icon to load, so the empty state has to look deliberate
// rather than broken. Because the tile is always the same size, nothing on the row moves either
// way.
//
// A 404 is expected, not exceptional: the server has already looked inside the archive and found
// nothing, and it says so in a fraction of the time it takes to fail a download. `onerror` just
// leaves the monogram showing.
//
// `version` is the item's download timestamp. It is in the cache key AND in the URL so that
// re-downloading an app that has since rebranded actually shows the new icon: without it, the
// cached node would keep the old image and the browser would keep serving the old bytes from the
// unchanged URL.
function iconTile(key, name, size, src) {
  const kind = size || "sm";
  const letter = (String(name || "?").trim()[0] || "?").toUpperCase();
  const slot = `${key}:${kind}`;

  const cached = src ? tiles.get(slot) : null;
  // The src carries its own version, so comparing it is the whole staleness check: same URL
  // means the same bytes, and a different URL means the picture genuinely changed.
  if (cached && cached.src === src) {
    // The name can change under a fixed key when an update rewrites the app's metadata, and
    // the monogram is still visible for apps with no artwork.
    cached.tile.querySelector(".app-icon-letter").textContent = letter;
    return cached.tile;
  }

  const tile = el("div", { className: `app-icon app-icon-${kind}` }, [
    el("span", { className: "app-icon-letter", textContent: letter }),
  ]);
  if (!src) return tile;

  const img = el("img", {
    className: "app-icon-img",
    alt: "",
    // Decorative: the app's name is already the row title, so a screen reader announcing the
    // icon as well would read every app twice.
    //
    // LAZY MATTERS HERE. A device holds a couple of hundred apps; fetching every icon for a
    // list the user has seen four rows of would be megabytes off an iPhone over wifi to draw
    // nothing.
    loading: "lazy",
    decoding: "async",
    src,
  });
  img.onload = () => img.classList.add("loaded");
  // An image already in the browser's cache can be complete the moment it is created. Waiting
  // for a load event that has already fired would leave it invisible forever.
  if (img.complete && img.naturalWidth > 0) img.classList.add("loaded");
  tile.append(img);

  tiles.set(slot, { src, tile });
  return tile;
}

// appIcon is the icon of an ARCHIVED app, read out of the .ipa on this box.
function appIcon(id, name, size, version) {
  if (!id) return iconTile("lib:0", name, size, null);
  const v = version ? `?v=${encodeURIComponent(version)}` : "";
  return iconTile(`lib:${id}`, name, size, `/api/library/${id}/icon.png${v}`);
}

// deviceIcon is the icon the DEVICE draws for an app it has installed.
//
// This is the source that covers what the library cannot: an app that is delisted and has never
// been archived exists in no store and in no .ipa here, so the phone holding it is the only thing
// left that still has the picture.
function deviceIcon(udid, bundleID, version, name, size) {
  if (!udid || !bundleID) return iconTile("dev:0", name, size, null);
  const q = `?bundle=${encodeURIComponent(bundleID)}&v=${encodeURIComponent(version || "")}`;
  return iconTile(`dev:${udid}:${bundleID}`, name, size,
    `/api/devices/${encodeURIComponent(udid)}/icon.png${q}`);
}

// ---------------------------------------------------------------------------
// Library
// ---------------------------------------------------------------------------

async function refreshLibrary() {
  try { library = await api("/api/library"); } catch { /* shown on the screen */ }
}

function renderLibrary() {
  const root = $("#screen-library");
  const rows = library.map((it) => {
    const r = el("a", { className: "row", href: `/library/${it.id}` }, [
      appIcon(it.id, it.name || it.bundle_id, "sm", it.downloaded_at),
      el("div", { className: "row-main" }, [
        el("div", { className: "row-title", textContent: it.name || it.bundle_id }),
        el("div", { className: "row-sub", textContent: `${it.version || "—"} · ${fmtSize(it.size)}` }),
      ]),
      el("div", { className: "row-right" }, [el("span", { className: "chev", textContent: "›" })]),
    ]);
    r.onclick = () => { detail = { item: it }; };
    return r;
  });

  const addForm = el("form", { className: "stack" }, [
    el("label", { textContent: "App Store id" }, [
      el("input", { type: "number", id: "lib-appid", min: "1", placeholder: "123456789" }),
    ]),
    accounts.length
      ? el("label", { textContent: "Download with" }, [
          el("select", { id: "lib-account" }, accounts.map((a) =>
            el("option", { value: a.slug, textContent: a.email }))),
        ])
      : el("p", { className: "hint", textContent: "Add an Apple ID first." }),
    el("button", { className: "primary wide", type: "submit", textContent: "Add to library" }),
  ]);
  addForm.onsubmit = (ev) => {
    ev.preventDefault();
    const id = parseInt($("#lib-appid").value, 10);
    if (!id) { toast("Enter the numeric App Store id.", true); return; }
    const sel = $("#lib-account");
    archive(id, sel && sel.value, `id ${id}`, addForm.querySelector("button"));
  };

  root.replaceChildren(
    el("h2", { className: "screen", textContent: "Library" }),
    el("p", { className: "screen-hint", textContent: "Apps archived on this box. Tap one to install it." }),
    rows.length ? el("div", { className: "list" }, rows)
                : el("p", { className: "empty", textContent: "Nothing downloaded yet." }),
    el("h3", { className: "sub-head", textContent: "Add by App Store id" }),
    el("p", { className: "hint", textContent: "For an app that is not installed on any reachable device." }),
    addForm,
  );
}

// ---------------------------------------------------------------------------
// Accounts
// ---------------------------------------------------------------------------

async function refreshAccounts() {
  try { accounts = await api("/api/accounts"); } catch { /* shown on the screen */ }
}

let pendingSlug = null;


// ---------------------------------------------------------------------------
// Navigation + auto-refresh
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Routing — the browser owns navigation; this app only owns rendering.
//
// The invariant worth optimising for: going BACK should reveal the screen you left, not
// reconstruct a new one that happens to have the same URL. Everything below follows from that.
//
//   1. Every screen is a real URL and a real history entry, reached by a real <a href>. Rows are
//      links, not click handlers, so Safari sees an ordinary navigation and drives its own back
//      gesture with its own machinery.
//   2. The Navigation API is the primary mechanism. It fires BEFORE a traversal commits, so the
//      destination can be restored while the gesture is still on screen rather than after it.
//   3. SAFARI OWNS SCROLL RESTORATION. This is the part I got wrong before: I set
//      scrollRestoration = "manual", passed `scroll: "manual"` to intercept(), and then restored
//      offsets myself two animation frames later. That is using the right API to fight the
//      browser. Left alone, an intercepted navigation restores scroll AFTER the handler resolves
//      — the saved offset for a traversal, the top for a new navigation — which is exactly the
//      wanted behaviour and needs no code at all.
//   4. Back does not rebuild and does not re-fetch. Screens stay in the document, so restoring
//      one is unhiding what is already there; fresh data arrives afterwards from the pollers.
//   5. The document scrolls, not a nested container, so there is one scrolling area for the
//      browser to save and restore.
//
// No custom edge-pan, no fake navigation controller. The browser is better at its own gesture
// than an imitation of it, and competing produces exactly the glitches this is meant to remove.

// routeFor parses a URL into what to render.
function routeFor(url) {
  const path = url.pathname;
  let m;
  if ((m = path.match(/^\/library\/(\d+)$/))) return { screen: "app", libraryID: m[1] };
  if ((m = path.match(/^\/device\/([^/]+)$/))) return { screen: "device", udid: decodeURIComponent(m[1]) };
  if ((m = path.match(/^\/device\/([^/]+)\/([^/]+)$/))) {
    return { screen: "app", udid: decodeURIComponent(m[1]), bundle: decodeURIComponent(m[2]) };
  }
  if (path === "/library" || path === "/accounts") return { screen: path.slice(1) };
  return { screen: "devices" };
}

function pathForScreen(screen) {
  return screen === "devices" ? "/" : `/${screen}`;
}

// startAtTop puts a NEW screen at its initial position — and only a new one.
//
// This is not the mistake of the previous attempt, which overrode scroll on every navigation
// including traversals. A traversal still gets nothing from us: the browser restores the offset
// it saved. This supplies the other half, which the browser does NOT do for a same-document
// intercepted push — measured, a detail opened from a list scrolled to 1834 stayed near the
// bottom of its own much shorter page, so it appeared to open scrolled to the end.
function startAtTop() {
  window.scrollTo(0, 0);
}

// renderRoute puts the right screen on screen. `traverse` is true for back/forward, and is the
// whole reason this takes an argument: a traversal must restore, a new navigation must build.
async function renderRoute(url, { traverse = false } = {}) {
  const route = routeFor(url);

  // DROP THE FOCUS THE TAP LEFT BEHIND. A row is an <a>, so tapping it focuses it, and Safari
  // keeps that focus through a back-swipe — the row you left from comes back looking picked, on
  // a list where nothing is ever selected. Only for links: blurring an input would take the
  // keyboard away from someone mid-word.
  const active = document.activeElement;
  if (active && active !== document.body && active.tagName === "A") active.blur();

  if (route.screen === "app") {
    // The detail view is rebuilt either way — it shows one specific app, and reusing the
    // previous render would show the wrong one. It is also short, so there is nothing to
    // restore beyond the top of the page.
    if (route.libraryID) {
      const item = library.find((i) => String(i.id) === route.libraryID);
      if (item) detail = { item };
    } else {
      const device = devices.find((d) => d.udid === route.udid);
      const payload = appsCache.get(route.udid);
      const app = payload && payload.apps.find((a) => a.bundle_id === route.bundle);
      if (device && app) detail = { app, device };
    }
    if (!detail) {
      // Nothing cached to show — a cold load of a deep link. Fall back rather than block on
      // a scan the visitor did not ask for.
      showScreen(route.libraryID ? "library" : "devices");
      if (!traverse) renderScreen(route.libraryID ? "library" : "devices");
      return;
    }
    showScreen("app");
    renderAppDetail();
    if (!traverse) startAtTop();
    return;
  }

  if (route.screen === "device") {
    // A DIFFERENT DEVICE MEANS A DIFFERENT PAGE, so the search box does not carry a filter
    // from the last one over to a list it was never typed against.
    if (deviceUDID !== route.udid) {
      deviceUDID = route.udid;
      appFilter = "";
    }
    showScreen("device");
    renderDevice();
    if (!deviceState.has(route.udid)) loadDeviceState(route.udid);
    if (!traverse) startAtTop();
    return;
  }

  showScreen(route.screen);
  if (!traverse) startAtTop();
  // ON A TRAVERSAL, DO NOT REBUILD. The content is still in the document from last time, and
  // rebuilding it here would empty the screen for the duration of the gesture and refill it at
  // the end — which is the blank-swipe symptom. A screen that has never been built still has to
  // be, hence the emptiness check rather than a blanket skip.
  const root = $(`#screen-${route.screen}`);
  if (!traverse || root.childElementCount === 0) renderScreen(route.screen);
}

// THE HEADER IS NOW INVARIANT. Nothing in it changes between screens except which tab is
// underlined, and even that no longer blinks out: a detail screen keeps the tab it came from
// lit. Previously "app" matched no tab, so the underline VANISHED on every push and returned on
// every pop — a change in the top bar at exactly the moment a flicker was being reported. The
// back arrow has left the header entirely and now lives on the detail screen itself.
function showScreen(screen) {
  current = screen;
  const lit = (screen === "app" || screen === "device") ? parentTab() : screen;
  for (const b of document.querySelectorAll("nav a")) {
    b.classList.toggle("active", b.dataset.screen === lit);
  }
  for (const s of ["devices", "library", "accounts", "device", "app"]) {
    $(`#screen-${s}`).hidden = s !== screen;
  }
}

// parentTab: a detail screen belongs to the list it was opened from.
function parentTab() {
  return detail && detail.item && !detail.app ? "library" : "devices";
}

function renderScreen(screen) {
  if (screen === "devices") renderDevices();
  if (screen === "library") renderLibrary();
  if (screen === "accounts") refreshAccounts().then(renderAccountsList);
  if (screen === "device") renderDevice();
  if (screen === "app") renderAppDetail();
}

// rerenderCurrent is what the pollers call — never a navigation, just fresher data.
//
// TWO SCREENS ARE EXCLUDED WHILE SOMEONE IS TYPING ON THEM, which is the same defect twice over:
// Accounts holds an email, a password and a verification code, and the device page holds a
// search box. Rebuilding either on a timer takes the text, the caret and — on a phone — the
// keyboard away mid-word. Accounts is excluded outright because nothing on it changes on its
// own; the device page only while its search field has the focus, since its pairing state and
// reachability genuinely do change underneath.
function rerenderCurrent() {
  if (current === "devices") renderDevices();
  if (current === "library") renderLibrary();
  if (current === "app") renderAppDetail();
  if (current === "device" && document.activeElement !== $("#app-search")) renderDevice();
}

// navigate is used only where a link cannot be (a row that is really a button).
function navigate(path) {
  if (window.navigation) window.navigation.navigate(path);
  else { history.pushState(null, "", path); renderRoute(new URL(path, location.href)); }
}

if (window.navigation && typeof window.navigation.addEventListener === "function") {
  window.navigation.addEventListener("navigate", (ev) => {
    // `leaving` is the sign-out case: that navigation must be allowed to throw the document
    // away rather than being turned into a route change. See #sign-out.
    if (leaving || !ev.canIntercept || ev.hashChange || ev.downloadRequest !== null) return;
    const url = new URL(ev.destination.url);
    if (url.origin !== location.origin || url.pathname.startsWith("/api/")) return;

    ev.intercept({
      // NO `scroll` OPTION. The default hands scroll back to the browser once this handler
      // resolves: the saved offset for a traversal, the top for a new navigation. Setting it
      // to "manual" here is precisely the mistake that made the previous attempt worse.
      handler: async () => {
        await renderRoute(url, { traverse: ev.navigationType === "traverse" });
      },
    });
  });
} else {
  // Browsers without the Navigation API: ordinary links plus popstate, and the browser's own
  // automatic scroll restoration left switched on.
  document.addEventListener("click", (ev) => {
    const a = ev.target.closest && ev.target.closest("a[href]");
    if (!a || ev.metaKey || ev.ctrlKey || ev.shiftKey || a.target) return;
    const url = new URL(a.href, location.href);
    if (url.origin !== location.origin) return;
    ev.preventDefault();
    if (url.pathname === location.pathname) return;
    history.pushState(null, "", url.pathname);
    renderRoute(url);
  });
  window.addEventListener("popstate", () => {
    renderRoute(new URL(location.href), { traverse: true });
  });
}

// The header arrow is the same action as the swipe and the browser's back button.


// ---------------------------------------------------------------------------
// Accounts — list rendering only. The form lives in index.html and is wired once, below.
// ---------------------------------------------------------------------------

function renderAccountsList() {
  const root = $("#accounts-list");
  if (!accounts.length) {
    root.replaceChildren(el("p", { className: "empty", textContent: "No Apple IDs yet." }));
    return;
  }
  root.replaceChildren(el("div", { className: "list" }, accounts.map((a) =>
    el("div", { className: "row static" }, [
      el("div", { className: "row-main" }, [
        el("div", { className: "row-title", textContent: a.email }),
        el("div", { className: "row-sub", textContent: [a.name, `added ${fmtDate(a.added_at)}`].filter(Boolean).join(" · ") }),
      ]),
      el("div", { className: "row-right stack" }, [
        a.signed_in
          ? el("span", {
              className: "status available", textContent: "signed in",
              // Honest about what was checked: ipatool read the local credential file.
              // It did not ask Apple.
              title: "Credentials are stored here. Apple can still expire the session — the first sign of that is a download failing.",
            })
          : el("span", { className: "status delisted", textContent: "SIGN IN AGAIN" }),
        (() => {
          const b = el("button", { className: "link danger", textContent: "Remove" });
          b.onclick = async () => {
            if (!confirm(`Remove ${a.email}? The stored session is deleted from disk.`)) return;
            try {
              await api(`/api/accounts/${encodeURIComponent(a.slug)}`, { method: "DELETE" });
              toast(`Removed ${a.email}.`);
              await refreshAccounts();
              renderAccountsList();
            } catch (e) { toast(e.message, true); }
          };
          return b;
        })(),
      ]),
    ]))));
}

// The sign-in form is wired ONCE, against markup that was in the document at load. Nothing here
// ever recreates an input, so nothing can clear what the user (or Safari) has put in one.
$("#signin").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const emailEl = $("#acc-email");
  const passEl = $("#acc-pass");
  const codeEl = $("#acc-code");
  const submit = $("#acc-submit");

  const email = emailEl.value.trim();
  const password = passEl.value;
  const code = codeEl.value.trim();

  if (!email || !password) {
    // Safari can leave a field visually filled but empty to script until it is touched, so
    // say which one is missing rather than sending a blank credential to Apple.
    toast(!email ? "Enter the Apple ID email." : "Enter the password.", true);
    return;
  }

  submit.disabled = true;
  const original = submit.textContent;
  submit.textContent = "Signing in…";
  try {
    if (pendingSlug && code) {
      // ipatool re-runs the whole login with the code attached, so the password goes with
      // it. springback does not hold it between requests.
      await api(`/api/accounts/${encodeURIComponent(pendingSlug)}/2fa`, {
        method: "POST", body: JSON.stringify({ code, password }),
      });
    } else {
      await api("/api/accounts", { method: "POST", body: JSON.stringify({ email, password }) });
    }
    toast(`Signed in as ${email}.`);
    // Clearing here is deliberate and is the ONLY place it happens: the sign-in succeeded, so
    // the password has no further use and should not sit in the DOM.
    pendingSlug = null;
    passEl.value = "";
    codeEl.value = "";
    $("#acc-2fa-wrap").hidden = true;
    submit.textContent = "Sign in";
    await refreshAccounts();
    renderAccountsList();
  } catch (e) {
    if (e.kind === "needs_2fa") {
      pendingSlug = e.body.slug;
      $("#acc-2fa-wrap").hidden = false;
      codeEl.focus();
      submit.textContent = "Finish sign-in";
      toast(e.body.detail);
    } else {
      toast(e.message, true);
      submit.textContent = original;
    }
  } finally {
    submit.disabled = false;
  }
});

// The header arrow is the SAME action as the swipe and the browser's back button, rather than a
// third thing that happens to look similar. Anything else and going "back" twice by two
// different means lands somewhere neither of them promised.


// AUTO-REFRESH. Devices come and go — a phone that wakes up should turn up without
// a reload. Only the DEVICE LIST is polled: it is two cheap calls per device, whereas the app
// scan is 162 store lookups and must stay an explicit action.
//
// Five seconds is slow enough to be free and fast enough to feel live. Paused when hidden,
// polling a background tab is how a personal tool quietly becomes a load.
const DEVICE_POLL_MS = 5000;

async function pollDevices() {
  if (document.hidden) return;
  // NOT WHILE THE GATE IS UP. The pollers only start after signing in, but a session can expire
  // under an open page — and then this would keep firing 401s at a login screen every five
  // seconds, for as long as the tab stayed open.
  if (!$("#gate").hidden) return;
  // NOR WHILE THE SOCKET IS UP, which is the ordinary case now. The server runs this same scan
  // once for everybody and pushes what it finds; this interval is what covers the minutes
  // between a socket dying and the next reconnect succeeding.
  if (liveConnected) return;
  try { await refreshDevices(); } catch { /* the screens report it */ }
}

// THE POLLERS START IN boot(), NOT HERE. Registered at module load they ran while the gate was
// still up, so on a fresh install the five-second device poll hit /api/devices, got its 401, and
// replaced the "choose a password" screen with a sign-in form about ten seconds after the page
// appeared. Nothing on a gated page has any business talking to an API it cannot reach.
function startPolling() {
  setInterval(pollDevices, DEVICE_POLL_MS);
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) return;
    // COMING BACK TO A TAB IS THE ONE MOMENT WORTH ASKING ANYWAY. A socket does not always
    // announce its own death — a phone that slept through a wifi handover holds one that is
    // open to the browser and connected to nothing, and the first sign of it is silence, which
    // looks exactly like nothing having happened. One fetch on foregrounding settles it, and
    // costs one request per time the user looks at the page.
    if (!$("#gate").hidden) return;
    if (liveConnected) refreshDevices().catch(() => {});
    else { connectLive(); pollDevices(); }
  });
}

// ---------------------------------------------------------------------------
// The event socket
//
// The server pushes device and job changes; everything the user DOES is still an ordinary HTTP
// request. That asymmetry is the whole design: there are no commands on this socket, so a browser
// that cannot open one — an ancient proxy that will not upgrade, a network that eats them — loses
// only the immediacy, and the intervals above still carry it. Nothing is unreachable without it.
//
// The frames carry the same bodies as GET /api/devices and GET /api/jobs, so they feed the
// functions that already existed rather than a second implementation of the same screens.
// ---------------------------------------------------------------------------

let socket = null;
// liveConnected is set by the hello frame rather than by onopen: an upgrade that succeeds and then
// says nothing is not a working connection, and the pollers must not stand down for one.
let liveConnected = false;
let liveRetry = null;
let liveBackoff = 1000;
const LIVE_BACKOFF_MAX = 30000;

function connectLive() {
  if (socket) return;
  // Not from behind the gate, for the same reason the pollers do not run there. A 401 on the
  // upgrade is invisible to script — the browser reports a generic failure — so retrying would
  // be blind hammering at a door it cannot open. No retry is scheduled either: hideGate
  // reconnects the moment there is a session to connect with.
  if (!$("#gate").hidden) return;

  const url = new URL("/api/ws", location.href);
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";

  let ws;
  try { ws = new WebSocket(url); } catch { retryLive(); return; }
  socket = ws;

  ws.onmessage = (ev) => {
    let env;
    try { env = JSON.parse(ev.data); } catch { return; }
    if (env.type === "hello") {
      liveConnected = true;
      liveBackoff = 1000;
      // The one-second job poll is now redundant. Stopping it here rather than letting it
      // expire keeps a download that was running through a reconnect from being polled and
      // pushed at the same time.
      clearTimeout(jobTimer);
      return;
    }
    if (env.type === "devices") applyDevices(env.data);
    if (env.type === "jobs") applyJobs(env.data);
  };

  ws.onclose = () => {
    if (socket !== ws) return;
    socket = null;
    const wasLive = liveConnected;
    liveConnected = false;
    retryLive();
    // Cover the gap now rather than at the next tick — a socket usually dies at the moment
    // something interesting is happening to the network it was watching.
    if (wasLive) { pollDevices(); pollJobs(); }
  };
  // An error is always followed by a close, so there is nothing to do here but make sure the
  // close actually comes.
  ws.onerror = () => { try { ws.close(); } catch { /* already gone */ } };
}

// retryLive backs off to half a minute. A socket that will not open usually will not open for a
// while — a proxy that strips upgrades, a session that has expired — and hammering it would be a
// request a second forever behind a login screen nobody is looking at.
function retryLive() {
  clearTimeout(liveRetry);
  liveRetry = setTimeout(connectLive, liveBackoff);
  liveBackoff = Math.min(liveBackoff * 2, LIVE_BACKOFF_MAX);
}

// dropLive closes the socket and stops trying to reopen it. The pollers take over on their own,
// since every one of them is guarded on liveConnected.
function dropLive() {
  clearTimeout(liveRetry);
  liveConnected = false;
  const ws = socket;
  socket = null;
  if (ws) { try { ws.close(); } catch { /* already gone */ } }
}

// boot runs once, AFTER the gate is satisfied. Everything it does needs a session, so running it
// first would just be a screenful of 401s.
async function boot() {
  if (booted) return;
  booted = true;

  try {
    const health = await api("/api/health");
    $("#fake-banner").hidden = !health.fake;
  } catch { /* the screens will report it */ }
  await Promise.all([refreshAccounts(), refreshLibrary()]);
  try { await refreshDevices(); } catch (e) {
    $("#screen-devices").replaceChildren(el("div", { className: "error", textContent: e.message }));
  }
  // The entry the page LOADED on has no state of its own, so without this it is the one entry
  // that cannot remember a scroll position — and it is the one people scroll furthest down.
  if (!history.state) history.replaceState({ key: 0 }, "", location.pathname);

  // Restore whatever the URL names rather than always starting at Devices, so a reload — or a
  // link someone kept — lands where it says it will.
  await renderRoute(new URL(location.href));
  pollJobs();
  startPolling();
  connectLive();
}

(async () => {
  let status;
  try {
    status = await authStatus();
  } catch {
    // The server is unreachable. Show the login form rather than a blank page: it is the one
    // screen that explains itself, and the next attempt will say what is wrong.
    status = { state: "needs_login", secure: true, loopback: false };
  }
  applyTransport(status);
  if (status.state === "authenticated") {
    hideGate();
    await boot();
  } else {
    showGate(status.state);
  }
})();

// The header is FIXED, not sticky — see style.css for why — so the content has to reclaim its
// height as padding, and that height is measured rather than hardcoded: it changes with the
// user's text size, and on a notched phone with the safe-area inset.
function measureHeader() {
  const h = document.querySelector("header").getBoundingClientRect().height;
  // A HEIGHT OF ZERO IS NOT A MEASUREMENT. While the gate is up the header is display:none, and
  // this used to run once at parse time — which is exactly then. The result was a body with no
  // top padding and a banner tucked underneath the header once the app appeared. Ignoring zero
  // keeps the last real value until there is a new one.
  if (h > 0) document.documentElement.style.setProperty("--header-h", `${Math.round(h)}px`);
}
measureHeader();
addEventListener("resize", measureHeader);
addEventListener("orientationchange", measureHeader);
// Re-measured whenever the header actually changes size, which covers the moment it stops being
// hidden as well as a text-size change. Cheaper and more reliable than remembering to call this
// from every place that might affect it.
if (window.ResizeObserver) new ResizeObserver(measureHeader).observe(document.querySelector("header"));
