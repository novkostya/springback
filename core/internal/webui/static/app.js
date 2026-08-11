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

async function pollJobs() {
  let list = [];
  try { list = await api("/api/jobs"); } catch { return; }

  const strip = $("#jobs");
  strip.replaceChildren(...list.filter((j) => j.state === "running").map(jobRow));

  for (const j of list) {
    const was = knownJobs.get(j.id);
    knownJobs.set(j.id, j.state);
    if (was === "running" && j.state !== "running") {
      if (j.state === "done") {
        toast(`${j.kind === "install" ? "Installed" : "Archived"} ${j.label}.`);
        // The library gained an entry, and a device's app list may now say "in library".
        appsCache.clear();
        await refreshLibrary();
        rerenderCurrent();
      } else {
        toast(`${j.label}: ${j.error}`, true);
      }
    }
  }

  const anyRunning = list.some((j) => j.state === "running");
  clearTimeout(jobTimer);
  if (anyRunning) jobTimer = setTimeout(pollJobs, 1000);
}

function jobRow(j) {
  const pct = j.percent >= 0 ? j.percent : null;
  const bar = el("div", { className: "bar" }, [
    el("div", { className: "bar-fill", style: `width:${pct == null ? 0 : pct}%` }),
  ]);
  const what = j.kind === "install" ? `Installing ${j.label}` : `Downloading ${j.label}`;
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

function deviceCard(d) {
  // NO "N at risk" BADGE HERE. It duplicated the summary line directly beneath it — and the
  // summary is the better statement of the two, because it carries the denominator ("11 of 162")
  // and what was checked. Two counts of the same thing in one card is one too many.
  const head = el("button", { className: "row device-row" }, [
    el("div", { className: "row-main" }, [
      el("div", { className: "row-title", textContent: d.name || d.udid }),
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
    el("div", { className: "row-right" }, [
      el("span", { className: `status ${a.store_status}`, textContent: statusLabel(a.store_status) }),
      a.in_library ? el("span", { className: "tick", textContent: "in library" }) : null,
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
  show("app");
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
    item ? el("span", { className: "tick", textContent: "in library" }) : null,
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
          b.onclick = () => archive(appID, pickedAccount(match && match.slug), title);
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
            archive(id, pickedAccount(null), title);
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

function installRow(d, item) {
  const r = el("button", { className: "row" }, [
    el("div", { className: "row-main" }, [
      el("div", { className: "row-title", textContent: d.name || d.udid }),
      el("div", { className: "row-sub", textContent: [d.product_type, d.ios && `iOS ${d.ios}`].filter(Boolean).join(" · ") }),
    ]),
    el("div", { className: "row-right" }, [
      d.reachable
        ? el("span", { className: "pill live", textContent: "install" })
        : el("span", { className: "pill asleep", textContent: "asleep" }),
    ]),
  ]);
  r.disabled = !d.reachable;
  r.onclick = async () => {
    try {
      const res = await api(`/api/devices/${encodeURIComponent(d.udid)}/install`, {
        method: "POST",
        body: JSON.stringify({ library_id: item.id }),
      });
      toast(res.note);
      startedJob();
    } catch (e) { toast(e.message, true); }
  };
  return r;
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

async function archive(appID, slug, label) {
  if (!slug) { toast("Add an Apple ID on the Accounts screen first.", true); return; }
  try {
    await api("/api/library", {
      method: "POST",
      body: JSON.stringify({ app_id: appID, account_slug: slug, label }),
    });
    toast(`Downloading ${label} — progress is at the top of the screen.`);
    startedJob();
  } catch (e) { toast(e.message, true); }
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
    archive(id, sel && sel.value, `id ${id}`);
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

function renderAccounts() {
  const root = $("#screen-accounts");

  const rows = accounts.map((a) => el("div", { className: "row static" }, [
    el("div", { className: "row-main" }, [
      el("div", { className: "row-title", textContent: a.email }),
      el("div", { className: "row-sub", textContent: `${a.name || "—"} · added ${fmtDate(a.added_at)}` }),
    ]),
    el("div", { className: "row-right" }, [
      a.signed_in
        ? el("span", {
            className: "status available", textContent: "signed in",
            // Honest about what was checked: ipatool read the local credential file. It
            // did not ask Apple.
            title: "Credentials are stored here. Apple can still expire the session — the first sign of that is a download failing.",
          })
        : el("span", { className: "status delisted", textContent: "SIGN IN AGAIN" }),
      (() => {
        const b = el("button", { className: "link danger", textContent: "Remove" });
        b.onclick = async (ev) => {
          ev.stopPropagation();
          if (!confirm(`Remove ${a.email}? The stored session is deleted from disk.`)) return;
          try {
            await api(`/api/accounts/${encodeURIComponent(a.slug)}`, { method: "DELETE" });
            toast(`Removed ${a.email}.`);
            await refreshAccounts();
            renderAccounts();
          } catch (e) { toast(e.message, true); }
        };
        return b;
      })(),
    ]),
  ]));

  const form = el("form", { className: "stack" }, [
    el("label", { textContent: "Apple ID email" }, [
      el("input", { type: "email", id: "acc-email", autocomplete: "username", required: true }),
    ]),
    el("label", { textContent: "Password" }, [
      el("input", { type: "password", id: "acc-pass", autocomplete: "current-password", required: true }),
    ]),
    pendingSlug
      ? el("label", { textContent: "Verification code" }, [
          el("input", { type: "text", id: "acc-code", inputMode: "numeric", autocomplete: "one-time-code" }),
        ])
      : null,
    el("button", { className: "primary wide", type: "submit", id: "acc-submit",
      textContent: pendingSlug ? "Finish sign-in" : "Sign in" }),
  ]);

  form.onsubmit = async (ev) => {
    ev.preventDefault();
    const email = $("#acc-email").value.trim();
    const password = $("#acc-pass").value;
    const code = $("#acc-code") ? $("#acc-code").value.trim() : "";
    const submit = $("#acc-submit");
    submit.disabled = true;
    try {
      if (pendingSlug && code) {
        // ipatool re-runs the whole login with the code attached, so the password goes
        // with it. springback does not hold it between requests.
        await api(`/api/accounts/${encodeURIComponent(pendingSlug)}/2fa`, {
          method: "POST", body: JSON.stringify({ code, password }),
        });
      } else {
        await api("/api/accounts", { method: "POST", body: JSON.stringify({ email, password }) });
      }
      toast(`Signed in as ${email}.`);
      pendingSlug = null;
      await refreshAccounts();
      renderAccounts();
    } catch (e) {
      if (e.kind === "needs_2fa") {
        pendingSlug = e.body.slug;
        renderAccounts();
        toast(e.body.detail);
      } else {
        toast(e.message, true);
        submit.disabled = false;
      }
    }
  };

  root.replaceChildren(
    el("h2", { className: "screen", textContent: "Accounts" }),
    el("p", { className: "screen-hint", textContent: "One directory per Apple ID. Only the session is stored — never the password." }),
    rows.length ? el("div", { className: "list" }, rows)
                : el("p", { className: "empty", textContent: "No Apple IDs yet." }),
    el("h3", { className: "sub-head", textContent: pendingSlug ? "Enter the code Apple sent" : "Add an Apple ID" }),
    el("p", { className: "hint", textContent:
      "Sessions expire. To renew one, sign in with the same address — the account is reused and nothing is lost." }),
    form,
    el("p", { className: "hint spaced", textContent:
      "This signs in to Apple's Store API with an unofficial client, which is against Apple's terms; accounts have been flagged for it. The risk is yours and stays on this box." }),
  );
}

// ---------------------------------------------------------------------------
// Navigation + auto-refresh
// ---------------------------------------------------------------------------

function rerenderCurrent() {
  if (current === "devices") renderDevices();
  if (current === "library") renderLibrary();
  if (current === "accounts") renderAccounts();
  if (current === "app") renderAppDetail();
}

function show(screen) {
  current = screen;
  for (const b of document.querySelectorAll("nav button")) {
    // The detail view belongs to whichever list you came from, so no tab owns it.
    b.classList.toggle("active", b.dataset.screen === screen);
  }
  for (const s of ["devices", "library", "accounts", "app"]) {
    $(`#screen-${s}`).hidden = s !== screen;
  }
  $("#back").hidden = screen !== "app";
  window.scrollTo(0, 0);
  rerenderCurrent();
}

$("#back").onclick = () => show(detail && detail.item && !detail.app ? "library" : "devices");

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
  show("devices");
  pollJobs();
})();
