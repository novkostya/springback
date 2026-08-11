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

  clearTimeout(jobTimer);
  if (runningJobs.length) jobTimer = setTimeout(pollJobs, 1000);
}

function jobRow(j) {
  const pct = j.percent >= 0 ? j.percent : null;
  const bar = el("div", { className: "bar" }, [
    el("div", { className: "bar-fill", style: `width:${pct == null ? 0 : pct}%` }),
  ]);
  const what = j.kind === "install" ? `Installing ${j.label}`
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
  devicesLoaded = true;
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
  // A ROW THAT NAVIGATES IS AN <a href>, not a button with a click handler: it gives the browser
  // an ordinary navigation to drive, which is what makes its own back gesture, history and scroll
  // restoration work without being reimplemented.
  const r = el("a", {
    className: "row",
    href: `/device/${encodeURIComponent(device.udid)}/${encodeURIComponent(a.bundle_id)}`,
  }, [
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

  // The icon comes out of the ARCHIVE, so it exists only for apps that have been downloaded.
  // An app that is installed on a device but not archived gets the plain heading — which is
  // honest: there is no file here to take an icon from yet.
  const hero = item
    ? el("div", { className: "detail-hero" }, [appIcon(item.id, title, "lg", item.downloaded_at), head])
    : head;

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
          b.onclick = () => archive(item.id, pickedAccount(updateWith), item.name, b);
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
          b.onclick = () => archive(item.id, pickedAccount(updateWith), item.name, b);
          return b;
        })(),
      );
    }
    if (accounts.length) blocks.push(el("h3", { className: "sub-head", textContent: "Update" }), accountPicker(updateWith), upd);
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
    right = el("span", { className: "pill asleep", textContent: "asleep" });
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
function accountPicker(preferredSlug) {
  if (!accounts.length) {
    return el("p", { className: "hint", textContent: "Add an Apple ID on the Accounts screen first." });
  }
  const sel = el("select", { id: "picked-account" }, accounts.map((a) =>
    el("option", { value: a.slug, textContent: a.email })));
  // Only preselect an account that is still signed in. Assigning a slug with no matching
  // option leaves the select on NO option at all — a blank picker, and a download that goes
  // nowhere — which is exactly what a removed account would have produced.
  if (knownSlug(preferredSlug)) sel.value = preferredSlug;
  return el("label", { textContent: "Download with" }, [sel]);
}

function pickedAccount(preferredSlug) {
  const sel = $("#picked-account");
  if (sel && sel.value) return sel.value;
  if (knownSlug(preferredSlug)) return preferredSlug;
  return accounts[0] && accounts[0].slug;
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
function appIcon(id, name, size, version) {
  const kind = size || "sm";
  const letter = (String(name || "?").trim()[0] || "?").toUpperCase();
  const key = `${id || 0}:${kind}`;
  const stamp = version || "";

  const cached = id ? tiles.get(key) : null;
  if (cached && cached.stamp === stamp) {
    // The name can change under a fixed id when an update rewrites meta.json, and the
    // monogram is still visible for apps with no artwork.
    cached.tile.querySelector(".app-icon-letter").textContent = letter;
    return cached.tile;
  }

  const tile = el("div", { className: `app-icon app-icon-${kind}` }, [
    el("span", { className: "app-icon-letter", textContent: letter }),
  ]);
  if (!id) return tile;

  const img = el("img", {
    className: "app-icon-img",
    alt: "",
    // Decorative: the app's name is already the row title, so a screen reader announcing the
    // icon as well would read every app twice.
    loading: "lazy",
    decoding: "async",
    src: `/api/library/${id}/icon.png${stamp ? `?v=${encodeURIComponent(stamp)}` : ""}`,
  });
  img.onload = () => img.classList.add("loaded");
  // An image already in the browser's cache can be complete the moment it is created. Waiting
  // for a load event that has already fired would leave it invisible forever.
  if (img.complete && img.naturalWidth > 0) img.classList.add("loaded");
  tile.append(img);

  tiles.set(key, { stamp, tile });
  return tile;
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
  const lit = screen === "app" ? parentTab() : screen;
  for (const b of document.querySelectorAll("nav a")) {
    b.classList.toggle("active", b.dataset.screen === lit);
  }
  for (const s of ["devices", "library", "accounts", "app"]) {
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
  if (screen === "app") renderAppDetail();
}

// rerenderCurrent is what the pollers call — never a navigation, just fresher data.
function rerenderCurrent() {
  if (current === "devices") renderDevices();
  if (current === "library") renderLibrary();
  if (current === "app") renderAppDetail();
  // Accounts is excluded: it is the only screen holding text someone is part-way through
  // typing, and nothing on it changes on its own.
}

// navigate is used only where a link cannot be (a row that is really a button).
function navigate(path) {
  if (window.navigation) window.navigation.navigate(path);
  else { history.pushState(null, "", path); renderRoute(new URL(path, location.href)); }
}

if (window.navigation && typeof window.navigation.addEventListener === "function") {
  window.navigation.addEventListener("navigate", (ev) => {
    if (!ev.canIntercept || ev.hashChange || ev.downloadRequest !== null) return;
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
  await renderRoute(new URL(location.href));
  pollJobs();
})();

// The header is FIXED, not sticky — see style.css for why — so the content has to reclaim its
// height as padding, and that height is measured rather than hardcoded: it changes with the
// user's text size, and on a notched phone with the safe-area inset.
function measureHeader() {
  const h = document.querySelector("header").getBoundingClientRect().height;
  document.documentElement.style.setProperty("--header-h", `${Math.round(h)}px`);
}
measureHeader();
addEventListener("resize", measureHeader);
addEventListener("orientationchange", measureHeader);
