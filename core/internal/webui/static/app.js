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
    const err = new Error((body && (body.detail || body.error)) || `HTTP ${res.status}`);
    err.kind = body && body.error;
    err.status = res.status;
    err.body = body;
    throw err;
  }
  return body;
}

let toastTimer;
function toast(msg, bad = false) {
  const t = $("#toast");
  t.textContent = msg;
  t.hidden = false;
  t.className = bad ? "bad" : "";
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, bad ? 12000 : 6000);
}

const fmtSize = (n) => {
  if (!n) return "—";
  const mb = n / (1024 * 1024);
  return mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${Math.round(mb)} MB`;
};
const fmtDate = (s) => (s ? new Date(s).toLocaleString() : "—");
const statusLabel = (s) => (s === "not_listed" ? "not listed" : s.toUpperCase());

// emailForSlug turns an account directory name back into the Apple ID it stands for. The slug is
// a filesystem-safe mangling (`novkostya@gmail.com` -> `novkostya-at-gmail.com`) and has no
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
let library = [];
const appsCache = new Map(); // udid -> payload
let current = "devices";
let detail = null; // { app, deviceUdid } | { item }

// ---------------------------------------------------------------------------
// Jobs — the progress strip.
//
// Downloads and installs no longer hold the request open, so this is where the user finds out
// what is happening. Polled once a second while anything is running, and not at all when
// nothing is.
// ---------------------------------------------------------------------------

let jobTimer = null;
let knownJobs = new Map();
let runningJobs = [];

async function pollJobs() {
  let list = [];
  try { list = await api("/api/jobs"); } catch { return; }

  runningJobs = list.filter((j) => j.state === "running");
  const strip = $("#jobs");
  // Downloads keep the top strip — they belong to no row. Installs are drawn IN the device
  // row instead, so showing them twice would be the duplicate-count mistake again.
  // Sign-in jobs also belong in the strip: they have no row of their own and can run for
  // half an hour, so they need somewhere visible to live.
  strip.replaceChildren(...runningJobs.filter((j) => j.kind !== "install").map(jobRow));

  let finished = false;
  for (const j of list) {
    const was = knownJobs.get(j.id);
    knownJobs.set(j.id, j.state);
    if (was === "running" && j.state !== "running") {
      finished = true;
      if (j.state === "done") {
        toast(j.kind === "install" ? `Installed ${j.label}.`
          : j.kind === "signin" ? `Signed in as ${j.label}.`
          : `Archived ${j.label}.`);
        if (j.kind === "signin") { await refreshAccounts(); renderAccountsList(); }
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

  clearTimeout(jobTimer);
  if (runningJobs.length) jobTimer = setTimeout(pollJobs, 1000);
}

function jobRow(j) {
  const pct = j.percent >= 0 ? j.percent : null;
  const bar = el("div", { className: "bar" }, [
    el("div", { className: "bar-fill", style: `width:${pct == null ? 0 : pct}%` }),
  ]);
  const what = j.kind === "install" ? `Installing ${j.label}`
    : j.kind === "signin" ? `Signing in as ${j.label}`
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

function startedJob() {
  clearTimeout(jobTimer);
  jobTimer = setTimeout(pollJobs, 300);
}

// ---------------------------------------------------------------------------
// Devices
// ---------------------------------------------------------------------------

async function refreshDevices() {
  devices = await api("/api/devices");
}

function renderDevices() {
  const root = $("#screen-devices");
  const frag = [
    el("h2", { className: "screen", textContent: "Devices" }),
    el("p", {
      className: "screen-hint",
      textContent: "Tap a device to see which of its apps are no longer in any App Store.",
    }),
  ];

  if (!devices.length) {
    frag.push(el("p", {
      className: "empty",
      textContent:
        "No paired devices yet. springback reads the pairing records this box already has.",
    }));
  }

  for (const d of devices) frag.push(deviceCard(d));
  root.replaceChildren(...frag);
}

// deviceLabel disambiguates devices that share a name.
//
// TWO PHONES CAN HAVE THE SAME NAME, and on this stand two do: `alina-iphone` is two different
// handsets with different udids and different models. Rendered plainly they are two identical
// rows and there is no way to tell which one you are about to install onto. A name is a label
// the owner chose; the udid is the identity.
function deviceLabel(d) {
  const name = d.name || d.udid;
  const clashes = devices.filter((o) => (o.name || o.udid) === name).length > 1;
  if (!clashes || !d.udid) return name;
  return `${name} · ${d.udid.slice(-6)}`;
}

function deviceCard(d) {
  // NO "N at risk" BADGE HERE. It duplicated the summary line directly beneath it — and the
  // summary is the better statement of the two, because it carries the denominator ("11 of 162")
  // and what was checked. Two counts of the same thing in one card is one too many.
  const head = el("button", { className: "row device-row" }, [
    el("div", { className: "row-main" }, [
      el("div", { className: "row-title", textContent: deviceLabel(d) }),
      el("div", {
        className: "row-sub",
        textContent: [d.product_type, d.ios && `iOS ${d.ios}`, d.region].filter(Boolean).join(" · "),
      }),
    ]),
    el("div", { className: "row-right" }, [
      d.reachable
        ? el("span", { className: "pill live", textContent: "reachable" })
        // "not currently reachable", never "gone" (SPEC §3): a sleeping iPhone drops off
        // mDNS entirely, and that is normal.
        : el("span", { className: "pill asleep", textContent: "asleep" }),
    ]),
  ]);

  const body = el("div", { className: "device-body", hidden: !expanded.has(d.udid) });
  head.onclick = async () => {
    if (!d.reachable) {
      toast("That device is asleep or off the network. Wake it and it will turn up here.", true);
      return;
    }
    if (expanded.has(d.udid)) {
      expanded.delete(d.udid);
      body.hidden = true;
      return;
    }
    expanded.add(d.udid);
    body.hidden = false;
    await loadApps(d, body);
  };
  if (expanded.has(d.udid)) loadApps(d, body);

  return el("div", { className: "card" }, [head, body]);
}

const expanded = new Set();

async function loadApps(d, body) {
  const cached = appsCache.get(d.udid);
  if (cached) { renderApps(d, cached, body); return; }

  body.replaceChildren(el("p", {
    className: "hint spinner",
    textContent: "Reading the app list and asking each App Store about it. The first scan takes about half a minute.",
  }));
  try {
    const payload = await api(`/api/devices/${encodeURIComponent(d.udid)}/apps`);
    appsCache.set(d.udid, payload);
    renderApps(d, payload, body);
    renderDevices();
  } catch (e) {
    body.replaceChildren(el("div", { className: "error", textContent: e.message }));
  }
}

function renderApps(d, payload, body) {
  const { apps, storefronts, total, delisted, unknown } = payload;

  const summary = el("div", { className: "summary" });
  if (delisted > 0) {
    // The count line SPEC §6 asks for, in its own words.
    summary.append(el("p", { className: "summary-line" }, [
      el("strong", { textContent: `${delisted} of ${total} apps` }),
      ` on this ${d.name || "device"} ${delisted === 1 ? "is" : "are"} no longer in the App Store.`,
    ]));
  } else {
    summary.append(el("p", {
      className: "summary-line",
      textContent: `All ${total} apps here are still in an App Store somewhere.`,
    }));
  }
  summary.append(el("p", {
    className: "sub",
    textContent:
      `Checked ${storefronts.join(", ")} plus each app's own storefront. An app counts as ` +
      `delisted only when every one of them comes back empty.` +
      (payload.not_listed ? ` ${payload.not_listed} never had a public listing and are not counted.` : "") +
      (unknown ? ` ${unknown} could not be checked.` : ""),
  }));

  body.replaceChildren(summary, el("div", { className: "list" }, apps.map((a) => appRow(a, d))));
}

// appRow is deliberately three lines and a chip — it has to be readable on a phone, and the
// details live one tap away rather than in a row that scrolls sideways.
function appRow(a, device) {
  const r = el("button", { className: "row" }, [
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
  r.onclick = () => showAppDetail({ app: a, device });
  return r;
}

// ---------------------------------------------------------------------------
// App detail — one view for both states, reached from a device or from the library.
// ---------------------------------------------------------------------------

function showAppDetail(ctx) {
  detail = ctx;
  // A URL that describes what is on screen, so the entry the swipe goes back FROM is also one
  // a reload or a shared link can land on.
  const path = ctx.item
    ? `/library/${ctx.item.id}`
    : `/device/${encodeURIComponent(ctx.device.udid)}/${encodeURIComponent(ctx.app.bundle_id)}`;
  show("app", { path });
}

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
  if (item) {
    fact("Downloaded", fmtDate(item.downloaded_at));
    fact("Size", fmtSize(item.size));
    // The SLUG is a directory name — `novkostya-at-gmail.com` — and it was being shown to
    // the user as though it were an address. Resolve it back to the real Apple ID; fall
    // back to the slug only if the account has since been removed.
    fact("With account", emailForSlug(item.account_slug));
  }

  const head = el("div", { className: "detail-head" }, [
    el("h2", { className: "screen", textContent: title }),
    a ? el("span", { className: `status ${a.store_status}`, textContent: statusLabel(a.store_status) }) : null,
    item ? el("span", { className: "badge library", textContent: "in library" }) : null,
  ]);

  const blocks = [head, el("dl", { className: "facts" }, facts)];

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
        accountPicker(match && match.slug),
        (() => {
          const b = el("button", { className: "primary wide", textContent: "Archive to library" });
          b.disabled = !accounts.length;
          b.onclick = () => archive(appID, pickedAccount(match && match.slug), title, b);
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
        accountPicker(null),
        (() => {
          const b = el("button", { className: "primary wide", textContent: "Archive to library" });
          b.disabled = !accounts.length;
          b.onclick = () => {
            const id = parseInt($("#manual-appid").value, 10);
            if (!id) { toast("A numeric App Store id is required.", true); return; }
            archive(id, pickedAccount(null), title, b);
          };
          return b;
        })(),
      ]));
    }
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
        show("library");
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
  const has = already && already.has(item.bundle_id);

  let right;
  if (job) {
    // The progress lives IN THE ROW, next to the device it belongs to — a strip at the top
    // of the page cannot say which of three devices is being written to.
    right = ring(job.percent, job.stage);
  } else if (has) {
    right = el("span", { className: "tick done", textContent: "installed" });
  } else if (!d.reachable) {
    right = el("span", { className: "pill asleep", textContent: "asleep" });
  } else {
    right = el("span", { className: "btn-inline", textContent: "Install" });
  }

  const r = el("button", { className: "row" + (job ? " busy" : "") }, [
    el("div", { className: "row-main" }, [
      el("div", { className: "row-title", textContent: deviceLabel(d) }),
      el("div", {
        className: "row-sub",
        textContent: job
          ? (job.stage || "starting…")
          : [d.product_type, d.ios && `iOS ${d.ios}`].filter(Boolean).join(" · "),
      }),
    ]),
    el("div", { className: "row-right" }, [right]),
  ]);

  r.disabled = !d.reachable || !!job;
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

// installedOn: udid -> Set of bundle ids currently on that device.
const installedOn = new Map();
const installedLoading = new Set();

// loadInstalledSets fills it lazily for the reachable devices, then re-renders once. Fetched
// rather than derived from appsCache because that cache is only populated for devices the user
// has expanded, and this screen is reachable straight from the library.
function loadInstalledSets() {
  for (const d of devices) {
    if (!d.reachable || installedOn.has(d.udid) || installedLoading.has(d.udid)) continue;
    installedLoading.add(d.udid);
    api(`/api/devices/${encodeURIComponent(d.udid)}/installed`)
      .then((list) => {
        installedOn.set(d.udid, new Set(list.map((a) => a.bundle_id)));
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
function accountPicker(preferredSlug) {
  if (!accounts.length) {
    return el("p", { className: "hint", textContent: "Add an Apple ID on the Accounts screen first." });
  }
  const sel = el("select", { id: "picked-account" }, accounts.map((a) =>
    el("option", { value: a.slug, textContent: a.email })));
  if (preferredSlug) sel.value = preferredSlug;
  return el("label", { textContent: "Download with" }, [sel]);
}

function pickedAccount(preferredSlug) {
  const sel = $("#picked-account");
  if (sel && sel.value) return sel.value;
  return preferredSlug || (accounts[0] && accounts[0].slug);
}

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
// Library
// ---------------------------------------------------------------------------

async function refreshLibrary() {
  try { library = await api("/api/library"); } catch { /* shown on the screen */ }
}

function renderLibrary() {
  const root = $("#screen-library");
  const rows = library.map((it) => {
    const r = el("button", { className: "row" }, [
      el("div", { className: "row-main" }, [
        el("div", { className: "row-title", textContent: it.name || it.bundle_id }),
        el("div", { className: "row-sub", textContent: `${it.version || "—"} · ${fmtSize(it.size)}` }),
      ]),
      el("div", { className: "row-right" }, [el("span", { className: "chev", textContent: "›" })]),
    ]);
    r.onclick = () => showAppDetail({ item: it });
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
    el("p", { className: "screen-hint", textContent: "Apps downloaded to this box. Tap one for details and to install it." }),
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

// rerenderCurrent is what the TIMERS call — the 5s device poll and the 1s job poll.
//
// THE ACCOUNTS SCREEN IS DELIBERATELY EXCLUDED. It is the only screen holding text the user is
// part-way through typing, and rebuilding it would clear the email, the password and the
// verification code from under them — the same defect as the 2FA re-render, arriving on a timer
// instead of a response. Nothing on that screen changes on its own, so there is nothing to
// refresh; it is re-rendered explicitly after an action completes.
function rerenderCurrent() {
  if (current === "devices") renderDevices();
  if (current === "library") renderLibrary();
  if (current === "app") renderAppDetail();
}

// EVERY VIEW CHANGE IS A HISTORY ENTRY, which is what makes the iOS edge-swipe work.
//
// A single page that swaps views with JavaScript has one history entry, so a back swipe leaves
// the app entirely — there is nothing behind it to go back to. Pushing an entry per view gives
// the gesture, the browser's own back button and the header arrow all the same meaning, and
// costs a URL that describes what is on screen, which a reload or a shared link can then land on.
//
// `push` is false when the navigation IS a back/forward event: re-pushing there would fight the
// user, appending an entry each time they tried to leave.
// SCROLL POSITION IS PER HISTORY ENTRY, kept by us rather than by the browser.
//
// `history.scrollRestoration = "manual"` turns off the browser's own attempt, which cannot work
// here: it restores the offset at popstate time, when this app has not yet rebuilt the list, so
// it lands past the end of a short page and leaves a blank viewport. We restore after the
// content is back, which is the only moment the offset means anything.
//
// Going BACK returns you where you were; going FORWARD to something new starts at the top. That
// is what native apps and ordinary pages do, and the difference is exactly whether the entry has
// been seen before.
history.scrollRestoration = "manual";

const scrollPositions = new Map(); // history key -> scrollY
let historyKey = 0;
let nextHistoryKey = 1;

// scrollKey identifies the entry a position belongs to.
//
// Two schemes, because the two navigation mechanisms number entries differently and mixing them
// silently loses every position: the Navigation API gives each entry a stable `key` of its own,
// while the popstate path has to carry a counter in history.state. Whichever is in use must be
// used on BOTH sides — saving under one and looking up under the other is how the first version
// of this restored every screen to the top.
function scrollKey() {
  if (window.navigation && window.navigation.currentEntry) return window.navigation.currentEntry.key;
  return historyKey;
}

function rememberScroll() {
  scrollPositions.set(scrollKey(), window.scrollY);
}

// restoreScroll waits for layout before setting the offset. A single frame is not enough: the
// render happens in this task, and the page has not been laid out until the frame after it, so
// scrolling immediately clamps against the OLD height and silently lands at the top.
function restoreScroll(y) {
  requestAnimationFrame(() => requestAnimationFrame(() => window.scrollTo(0, y || 0)));
}

// `rebuild: false` — DO NOT RE-RENDER A SCREEN YOU ARE RETURNING TO.
//
// This is what fixes the blank frame during the interactive back-swipe. Safari drives that
// gesture itself and only tells us it happened, via popstate, when it has FINISHED. Everything
// the user sees mid-swipe is whatever the document already contains. If the destination screen's
// DOM is rebuilt in response to popstate, it is empty for the whole gesture and fills in at the
// end — which is exactly the reported symptom.
//
// The screens are all in the document already, hidden rather than destroyed, so returning to one
// is a `hidden` toggle over content that is still there. Rebuilt only when the content might be
// wrong: the app-detail view, which shows a specific app and could otherwise display the previous
// one. Lists refresh themselves from the pollers anyway.
function show(screen, { path, push = true, restore = null, rebuild = true } = {}) {
  if (push) rememberScroll();

  current = screen;
  for (const b of document.querySelectorAll("nav button")) {
    // The detail view belongs to whichever list you came from, so no tab owns it.
    b.classList.toggle("active", b.dataset.screen === screen);
  }
  for (const s of ["devices", "library", "accounts", "app"]) {
    $(`#screen-${s}`).hidden = s !== screen;
  }
  $("#back").hidden = screen !== "app";

  if (push) {
    const url = path || (screen === "devices" ? "/" : `/${screen}`);
    const key = nextHistoryKey++;
    const state = { screen, key, detail: detail && (detail.item
      ? { item: detail.item.id }
      : { udid: detail.device.udid, bundle: detail.app.bundle_id }) };
    // replaceState when the target is where we already are, so tapping the current tab does
    // not stack identical entries the user then has to swipe through one by one.
    if (location.pathname === url) history.replaceState(state, "", url);
    else history.pushState(state, "", url);
    historyKey = key;
  }

  // Already-populated screen being returned to: show it as it stands.
  if (!rebuild && screen !== "app" && $(`#screen-${screen}`).childElementCount > 0) {
    restoreScroll(restore);
    return;
  }

  // The accounts LIST is refreshed on arrival; the form is static markup and is never touched.
  if (screen === "accounts") {
    refreshAccounts().then(() => { renderAccountsList(); restoreScroll(restore); });
    return;
  }
  rerenderCurrent();
  restoreScroll(restore);
}

// applyRoute restores a view from a history entry — on a back/forward, or on a cold load of a
// deep link. The state object is preferred when present; the path is the fallback, because a
// reload arrives with no state at all.
async function applyRoute(state, restoreOverride) {
  const path = location.pathname;
  // The offset this entry was left at, if it has been visited before. Absent for a cold load
  // or a forward move into something new, which correctly start at the top.
  const restore = restoreOverride != null
    ? restoreOverride
    : (state && state.key != null ? scrollPositions.get(state.key) : null);
  if (state && state.key != null) historyKey = state.key;

  if (state && state.screen && state.screen !== "app") {
    show(state.screen, { push: false, restore, rebuild: false });
    return;
  }

  const lib = path.match(/^\/library\/(\d+)$/);
  if (lib) {
    const item = library.find((i) => String(i.id) === lib[1]);
    if (item) { detail = { item }; show("app", { push: false, restore, rebuild: false }); return; }
    show("library", { push: false, restore, rebuild: false });
    return;
  }

  const dev = path.match(/^\/device\/([^/]+)\/([^/]+)$/);
  if (dev) {
    const udid = decodeURIComponent(dev[1]);
    const bundle = decodeURIComponent(dev[2]);
    const device = devices.find((d) => d.udid === udid);
    // The device's app list may not be loaded — a reload lands here with nothing cached, and
    // that scan is slow, so fall back to the device list rather than making the user wait
    // on a view they only arrived at by going backwards.
    const payload = appsCache.get(udid);
    const app = payload && payload.apps.find((a) => a.bundle_id === bundle);
    if (device && app) { detail = { app, device }; show("app", { push: false, restore, rebuild: false }); return; }
    show("devices", { push: false, restore, rebuild: false });
    return;
  }

  if (path === "/library" || path === "/accounts") {
    show(path.slice(1), { push: false, restore, rebuild: false });
    return;
  }
  show("devices", { push: false, restore, rebuild: false });
}

// WHY THE BACK-SWIPE IS SMOOTH ON AN ORDINARY SITE AND NOT HERE.
//
// google.com is many DOCUMENTS. Going back restores the previous one from the back/forward
// cache — a whole live page the browser kept, already painted, already at the right scroll — so
// it can composite it under your thumb as you drag. Nothing is being rebuilt; it never went away.
//
// This app is ONE document that swaps its own contents. There is no previous page for the
// browser to bring back, and with plain popstate it is not even told the gesture is happening
// until it has finished — so mid-drag there is nothing to show. That is the cost of pushState
// routing, and it is why this is awkward in every SPA rather than something done wrong here.
//
// The Navigation API is the part of the platform that closes the gap: for a traversal it fires
// BEFORE the navigation commits, and `intercept()` lets the DOM be updated as part of it, so the
// browser can paint the destination during the gesture instead of after it. Used when present,
// with popstate as the fallback — and only for `traverse`, because pushState navigations are
// already handled by show() and intercepting them would render everything twice.
if (window.navigation && typeof window.navigation.addEventListener === "function") {
  window.navigation.addEventListener("navigate", (ev) => {
    if (ev.navigationType !== "traverse" || !ev.canIntercept || ev.hashChange) return;
    if (new URL(ev.destination.url).origin !== location.origin) return;
    ev.intercept({
      // "after-transition" would restore scroll once the browser has finished its own
      // animation, undoing the point of intercepting at all.
      scroll: "manual",
      handler: async () => {
        // destination.getState() is the NAVIGATION api's own state, which is NOT what
        // history.pushState wrote — it comes back undefined here. The entry's `key` is
        // the identifier that actually exists on both sides of a traversal.
        await applyRoute(null, scrollPositions.get(ev.destination.key));
      },
    });
  });
} else {
  window.addEventListener("popstate", (ev) => { applyRoute(ev.state); });
}

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
        el("div", { className: "row-sub", textContent: `${a.name || "—"} · added ${fmtDate(a.added_at)}` }),
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
      const keep = $("#acc-keep").checked;
      const res = await api("/api/accounts", {
        method: "POST",
        body: JSON.stringify({ email, password, keep_trying: keep }),
      });
      if (keep) {
        // A background job now owns this. The password stays in the field, because a
        // verification code may still arrive and finishing that needs the password again.
        toast(res.detail);
        startedJob();
        submit.disabled = false;
        submit.textContent = "Sign in";
        return;
      }
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
$("#back").onclick = () => history.back();

for (const b of document.querySelectorAll("nav button")) {
  b.onclick = () => show(b.dataset.screen);
}

// AUTO-REFRESH, like quince. Devices come and go — a phone that wakes up should turn up without
// a reload. Only the DEVICE LIST is polled: it is two cheap calls per device, whereas the app
// scan is 162 store lookups and must stay an explicit action.
//
// The five-second cadence matches quince's staleTime. Paused when the tab is hidden, because
// polling a background tab is how a personal tool quietly becomes a load.
const DEVICE_POLL_MS = 5000;

async function pollDevices() {
  if (document.hidden) return;
  const before = JSON.stringify(devices.map((d) => [d.udid, d.reachable, d.name]));
  try { await refreshDevices(); } catch { return; }
  const after = JSON.stringify(devices.map((d) => [d.udid, d.reachable, d.name]));
  // Re-render only on a real change, so an expanded device's app list is not torn down and
  // rebuilt under the reader every five seconds.
  if (before !== after && (current === "devices" || current === "app")) rerenderCurrent();
}

setInterval(pollDevices, DEVICE_POLL_MS);
document.addEventListener("visibilitychange", () => { if (!document.hidden) pollDevices(); });

(async () => {
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
  await applyRoute(history.state);
  pollJobs();
})();
