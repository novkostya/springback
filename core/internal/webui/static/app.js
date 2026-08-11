// springback — one page, three screens. No framework, no build step (SPEC §1).
//
// The Devices screen is the landing screen and the reason the tool exists: it is where an app
// you own turns out to be gone from every store, and where one button does something about it.

"use strict";

const $ = (sel) => document.querySelector(sel);
const el = (tag, props = {}, kids = []) => {
  const n = Object.assign(document.createElement(tag), props);
  for (const k of [].concat(kids)) n.append(k);
  return n;
};

async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...opts,
  });
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
  // Errors get long enough to read; a "license not found" needs the user to go do something.
  toastTimer = setTimeout(() => { t.hidden = true; }, bad ? 12000 : 5000);
}

const fmtSize = (n) => {
  if (!n) return "—";
  const mb = n / (1024 * 1024);
  return mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${Math.round(mb)} MB`;
};
const fmtDate = (s) => (s ? new Date(s).toLocaleString() : "—");

// ---------------------------------------------------------------------------
// Devices — the landing screen.
// ---------------------------------------------------------------------------

const appsCache = new Map(); // udid -> payload
let accounts = [];
let devices = [];

async function renderDevices() {
  const root = $("#screen-devices");
  root.replaceChildren(
    el("h2", { className: "screen", textContent: "Devices" }),
    el("p", {
      className: "screen-hint",
      textContent:
        "Every device this box is paired with. Open one to see which of its apps are no longer in any App Store.",
    }),
    el("p", { className: "hint spinner", textContent: "Looking for devices" }),
  );

  try {
    devices = await api("/api/devices");
  } catch (e) {
    root.replaceChildren(el("div", { className: "error", textContent: e.message }));
    return;
  }

  const frag = [
    el("h2", { className: "screen", textContent: "Devices" }),
    el("p", {
      className: "screen-hint",
      textContent:
        "Every device this box is paired with. Open one to see which of its apps are no longer in any App Store.",
    }),
  ];

  if (devices.length === 0) {
    frag.push(
      el("p", {
        className: "empty",
        textContent:
          "No paired devices. springback reads the pairing records this host already has — pair a device with the tool that owns them, and it turns up here.",
      }),
    );
  }

  for (const d of devices) frag.push(deviceCard(d));
  root.replaceChildren(...frag);
}

function deviceCard(d) {
  const name = d.name || d.udid;
  const head = el("div", { className: "device-head" }, [
    el("h2", { textContent: name }),
    el("span", {
      className: "meta",
      textContent: [d.product_type, d.ios && `iOS ${d.ios}`, d.region].filter(Boolean).join(" · "),
    }),
    el("span", { className: "spacer" }),
    // "not currently reachable", never "gone" (SPEC §3): a sleeping iPhone drops off mDNS
    // entirely, and that is normal.
    d.reachable
      ? el("span", { className: "pill live", textContent: "reachable" })
      : el("span", { className: "pill asleep", textContent: "not currently reachable" }),
  ]);

  const body = el("div", { className: "device-body", hidden: true });
  const card = el("div", { className: "device" }, [head, body]);

  head.onclick = async () => {
    if (!d.reachable) {
      toast("That device is asleep or off the network. Wake it and reload.", true);
      return;
    }
    if (!body.hidden) { body.hidden = true; return; }
    body.hidden = false;
    await loadApps(d, body, head);
  };
  return card;
}

async function loadApps(d, body, head) {
  if (appsCache.has(d.udid)) {
    renderApps(d, appsCache.get(d.udid), body, head);
    return;
  }
  body.replaceChildren(
    el("p", {
      className: "hint spinner",
      textContent:
        "Reading the app list, then asking each App Store about it. The first scan of a device takes a minute; after that the answers are cached.",
    }),
  );
  try {
    const payload = await api(`/api/devices/${encodeURIComponent(d.udid)}/apps`);
    appsCache.set(d.udid, payload);
    renderApps(d, payload, body, head);
  } catch (e) {
    body.replaceChildren(el("div", { className: "error", textContent: e.message }));
  }
}

function renderApps(d, payload, body, head) {
  const { apps, storefronts, total, delisted, unknown } = payload;

  // The count line SPEC §6 asks for, in its own words.
  const summary = el("div", { className: "summary" });
  if (delisted > 0) {
    summary.append(
      el("span", {}, [
        el("strong", { textContent: `${delisted} of ${total} apps` }),
        ` on this ${d.name || "device"} ${delisted === 1 ? "is" : "are"} no longer in the App Store.`,
      ]),
    );
    head.querySelector(".pill").after(
      el("span", { className: "pill risk", textContent: `${delisted} at risk` }),
    );
  } else {
    summary.append(
      el("span", {}, [`All ${total} apps on this device are still in an App Store somewhere.`]),
    );
  }
  const sub = el("span", { className: "sub" });
  sub.textContent =
    `Checked ${storefronts.join(", ")}. An app counts as delisted only when every one of them ` +
    `comes back empty — an app that is simply not sold in your country is still fine.` +
    (unknown ? ` ${unknown} could not be checked.` : "");
  summary.append(sub);

  const rows = apps.map((a) => appRow(d, a));
  const table = el("table", {}, [
    el("thead", {}, [
      el("tr", {}, [
        el("th", { textContent: "App" }),
        el("th", { textContent: "Bundle id" }),
        el("th", { textContent: "Version" }),
        el("th", { textContent: "Store" }),
        el("th", { textContent: "" }),
      ]),
    ]),
    el("tbody", {}, rows),
  ]);

  body.replaceChildren(summary, table);
}

function appRow(d, a) {
  const status = el("span", { className: `status ${a.store_status}`, textContent: a.store_status.toUpperCase() });
  if (a.checked && a.checked.length) status.title = `Checked: ${a.checked.join(", ")}`;
  if (a.errors && a.errors.length) status.title = a.errors.join("\n");

  const actions = el("td", { className: "actions" });
  if (a.in_library) {
    actions.append(el("span", { className: "hint", textContent: "in library" }));
  } else if (a.store_status === "delisted" || a.store_status === "unknown") {
    // The Archive button is the whole product in one gesture (SPEC §6). For a delisted app
    // the numeric id has to be typed once — the dialog explains why.
    const b = el("button", { className: "action", textContent: "Archive" });
    b.onclick = () => openArchive(a);
    actions.append(b);
  } else {
    const b = el("button", { className: "action quiet", textContent: "Archive" });
    b.onclick = () => openArchive(a);
    actions.append(b);
  }

  return el("tr", { className: a.store_status === "delisted" ? "delisted" : "" }, [
    el("td", { className: "name", textContent: a.name || a.bundle_id }),
    el("td", { className: "bundle", textContent: a.bundle_id }),
    el("td", { textContent: a.version || "—" }),
    el("td", {}, [status]),
    actions,
  ]);
}

// ---------------------------------------------------------------------------
// Archive dialog — the one gesture.
// ---------------------------------------------------------------------------

function openArchive(a) {
  const dlg = $("#archive-dialog");
  $("#archive-name").textContent = a.name || a.bundle_id;

  const known = a.app_id > 0;
  $("#archive-known").hidden = !known;
  $("#archive-unknown").hidden = known;
  if (known) $("#archive-id").textContent = a.app_id;
  $("#archive-appid").value = "";

  const sel = $("#archive-account");
  sel.replaceChildren(
    ...accounts.map((acc) => el("option", { value: acc.slug, textContent: acc.email })),
  );
  const go = $("#archive-go");
  go.disabled = accounts.length === 0;
  if (accounts.length === 0) toast("Add an Apple ID on the Accounts screen first.", true);

  dlg.onclose = async () => {
    if (dlg.returnValue !== "go") return;
    const appID = known ? a.app_id : parseInt($("#archive-appid").value, 10);
    if (!appID) { toast("A numeric App Store id is required for a delisted app.", true); return; }
    await archive(appID, sel.value, a.name || a.bundle_id);
  };
  dlg.showModal();
}

async function archive(appID, slug, label) {
  toast(`Downloading ${label} — this holds the page open for a while.`);
  try {
    const res = await api("/api/library", {
      method: "POST",
      body: JSON.stringify({ app_id: appID, account_slug: slug }),
    });
    appsCache.clear();
    toast(`Archived ${res.item.name} ${res.item.version} (${fmtSize(res.item.size)}).`);
    if (currentScreen === "library") renderLibrary();
    if (currentScreen === "devices") renderDevices();
  } catch (e) {
    toast(e.message, true);
  }
}

// ---------------------------------------------------------------------------
// Library
// ---------------------------------------------------------------------------

async function renderLibrary() {
  const root = $("#screen-library");
  let items = [];
  try {
    items = await api("/api/library");
  } catch (e) {
    root.replaceChildren(el("div", { className: "error", textContent: e.message }));
    return;
  }

  const addByID = el("form", { className: "stack" }, [
    el("label", { textContent: "App Store id" }, [
      el("input", { type: "number", id: "lib-appid", min: "1", placeholder: "123456789" }),
    ]),
    el("label", { textContent: "Download with" }, [
      el("select", { id: "lib-account" }, accounts.map((a) => el("option", { value: a.slug, textContent: a.email }))),
    ]),
    el("button", { className: "primary", type: "submit", textContent: "Add to library" }),
  ]);
  addByID.onsubmit = async (ev) => {
    ev.preventDefault();
    const id = parseInt($("#lib-appid").value, 10);
    if (!id) { toast("Enter the numeric App Store id.", true); return; }
    await archive(id, $("#lib-account").value, `id ${id}`);
  };

  const rows = items.map((it) =>
    el("tr", {}, [
      el("td", { className: "name", textContent: it.name || it.bundle_id }),
      el("td", { className: "bundle", textContent: it.bundle_id }),
      el("td", { textContent: it.version || "—" }),
      el("td", { textContent: fmtSize(it.size) }),
      el("td", { textContent: fmtDate(it.downloaded_at) }),
      el("td", { textContent: it.account_slug }),
      el("td", { className: "actions" }, [
        (() => {
          const b = el("button", { className: "action", textContent: "Install" });
          b.onclick = () => openInstall(it);
          return b;
        })(),
        (() => {
          const b = el("button", { className: "action danger", textContent: "Delete" });
          b.onclick = async () => {
            if (!confirm(`Delete ${it.name} from the library? The .ipa is removed from disk.`)) return;
            try {
              await api(`/api/library/${it.id}`, { method: "DELETE" });
              toast(`Deleted ${it.name}.`);
              renderLibrary();
            } catch (e) { toast(e.message, true); }
          };
          return b;
        })(),
      ]),
    ]),
  );

  root.replaceChildren(
    el("h2", { className: "screen", textContent: "Library" }),
    el("p", {
      className: "screen-hint",
      textContent:
        "Apps downloaded to this box, keyed by App Store id. Once an .ipa is here its numeric id is never needed again.",
    }),
    items.length
      ? el("table", {}, [
          el("thead", {}, [
            el("tr", {}, [
              el("th", { textContent: "App" }),
              el("th", { textContent: "Bundle id" }),
              el("th", { textContent: "Version" }),
              el("th", { textContent: "Size" }),
              el("th", { textContent: "Downloaded" }),
              el("th", { textContent: "Account" }),
              el("th", { textContent: "" }),
            ]),
          ]),
          el("tbody", {}, rows),
        ])
      : el("p", { className: "empty", textContent: "Nothing downloaded yet." }),
    el("h2", { className: "screen", style: "margin-top:28px", textContent: "Add by App Store id" }),
    el("p", {
      className: "screen-hint",
      textContent: "For an app that is not installed on any reachable device.",
    }),
    addByID,
  );
}

function openInstall(item) {
  const dlg = $("#install-dialog");
  $("#install-name").textContent = `${item.name} ${item.version || ""}`.trim();
  const sel = $("#install-device");
  const reachable = devices.filter((d) => d.reachable);
  sel.replaceChildren(
    ...reachable.map((d) => el("option", { value: d.udid, textContent: d.name || d.udid })),
  );
  $("#install-go").disabled = reachable.length === 0;
  if (reachable.length === 0) toast("No device is reachable right now.", true);

  dlg.onclose = async () => {
    if (dlg.returnValue !== "go") return;
    toast(`Installing ${item.name} — installs are slow; the page waits.`);
    try {
      const res = await api(`/api/devices/${encodeURIComponent(sel.value)}/install`, {
        method: "POST",
        body: JSON.stringify({ library_id: item.id }),
      });
      appsCache.clear();
      toast(res.note);
    } catch (e) { toast(e.message, true); }
  };
  dlg.showModal();
}

// ---------------------------------------------------------------------------
// Accounts
// ---------------------------------------------------------------------------

async function renderAccounts() {
  const root = $("#screen-accounts");
  try {
    accounts = await api("/api/accounts");
  } catch (e) {
    root.replaceChildren(el("div", { className: "error", textContent: e.message }));
    return;
  }

  const list = accounts.length
    ? el("table", {}, [
        el("thead", {}, [
          el("tr", {}, [
            el("th", { textContent: "Apple ID" }),
            el("th", { textContent: "Name" }),
            el("th", { textContent: "Added" }),
            el("th", { textContent: "" }),
          ]),
        ]),
        el("tbody", {}, accounts.map((a) =>
          el("tr", {}, [
            el("td", { className: "name", textContent: a.email }),
            el("td", { textContent: a.name || "—" }),
            el("td", { textContent: fmtDate(a.added_at) }),
            el("td", { className: "actions" }, [
              (() => {
                const b = el("button", { className: "action danger", textContent: "Remove" });
                b.onclick = async () => {
                  if (!confirm(`Remove ${a.email}? The stored session is deleted from disk.`)) return;
                  try {
                    await api(`/api/accounts/${encodeURIComponent(a.slug)}`, { method: "DELETE" });
                    toast(`Removed ${a.email}.`);
                    renderAccounts();
                  } catch (e) { toast(e.message, true); }
                };
                return b;
              })(),
            ]),
          ]),
        )),
      ])
    : el("p", { className: "empty", textContent: "No Apple IDs yet." });

  const form = el("form", { className: "stack" }, [
    el("label", { textContent: "Apple ID email" }, [
      el("input", { type: "email", id: "acc-email", autocomplete: "username", required: true }),
    ]),
    el("label", { textContent: "Password" }, [
      el("input", { type: "password", id: "acc-pass", autocomplete: "current-password", required: true }),
    ]),
    el("div", { id: "acc-2fa-wrap", hidden: true }, [
      el("label", { textContent: "Verification code" }, [
        el("input", { type: "text", id: "acc-code", inputMode: "numeric", autocomplete: "one-time-code" }),
      ]),
    ]),
    el("button", { className: "primary", type: "submit", id: "acc-submit", textContent: "Sign in" }),
  ]);

  let pendingSlug = null;
  form.onsubmit = async (ev) => {
    ev.preventDefault();
    const email = $("#acc-email").value.trim();
    const password = $("#acc-pass").value;
    const code = $("#acc-code") ? $("#acc-code").value.trim() : "";
    const submit = $("#acc-submit");
    submit.disabled = true;
    try {
      if (pendingSlug && code) {
        // ipatool's 2FA flow is "re-run the same command with --auth-code", so the
        // password goes with it. springback does not hold it between requests.
        await api(`/api/accounts/${encodeURIComponent(pendingSlug)}/2fa`, {
          method: "POST",
          body: JSON.stringify({ code, password }),
        });
      } else {
        await api("/api/accounts", { method: "POST", body: JSON.stringify({ email, password }) });
      }
      toast(`Signed in as ${email}.`);
      pendingSlug = null;
      renderAccounts();
    } catch (e) {
      if (e.kind === "needs_2fa") {
        pendingSlug = e.body.slug;
        $("#acc-2fa-wrap").hidden = false;
        $("#acc-submit").textContent = "Finish sign-in";
        toast(e.body.detail);
      } else {
        toast(e.message, true);
      }
    } finally {
      submit.disabled = false;
    }
  };

  root.replaceChildren(
    el("h2", { className: "screen", textContent: "Accounts" }),
    el("p", {
      className: "screen-hint",
      textContent:
        "One directory per Apple ID. Only the session is stored — never the password.",
    }),
    list,
    el("h2", { className: "screen", style: "margin-top:28px", textContent: "Add an Apple ID" }),
    form,
    el("p", {
      className: "hint",
      style: "margin-top:16px;max-width:520px",
      textContent:
        "This signs in to Apple's Store API with an unofficial client, which is against Apple's terms; accounts have been flagged for it. The risk is yours and stays on this box.",
    }),
  );
}

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------

let currentScreen = "devices";

function show(screen) {
  currentScreen = screen;
  for (const b of document.querySelectorAll("nav button")) {
    b.classList.toggle("active", b.dataset.screen === screen);
  }
  for (const s of ["devices", "library", "accounts"]) {
    $(`#screen-${s}`).hidden = s !== screen;
  }
  if (screen === "devices") renderDevices();
  if (screen === "library") renderLibrary();
  if (screen === "accounts") renderAccounts();
}

for (const b of document.querySelectorAll("nav button")) {
  b.onclick = () => show(b.dataset.screen);
}

(async () => {
  try {
    const health = await api("/api/health");
    // A screen full of plausible device names that is not talking to any device would
    // otherwise be indistinguishable from the real thing.
    $("#fake-banner").hidden = !health.fake;
  } catch { /* the screens will report it */ }
  try { accounts = await api("/api/accounts"); } catch { /* shown on the accounts screen */ }
  show("devices");
})();
