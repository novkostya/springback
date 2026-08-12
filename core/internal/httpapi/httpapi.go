// Package httpapi serves the API and the embedded UI.
//
// EVERYTHING UNDER /api NEEDS A SESSION, bar the health check and the auth endpoints themselves.
// That is a change from v0.1, which had no auth at all and a README saying to keep it LAN-only —
// an honest answer for a tool one person runs on their own box, and not one for a tool that gets
// handed to a friend. What is behind the door has not changed: a signed-in Apple ID here can
// download as that Apple ID, so reaching this port is still equivalent to holding the accounts.
//
// The UI itself is served WITHOUT a session, because it has to load in order to draw the login
// form. It is markup with no secrets in it; every byte of content comes from /api.
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/novkostya/springback/core/internal/auth"
	"github.com/novkostya/springback/core/internal/devices"
	"github.com/novkostya/springback/core/internal/jobs"
	"github.com/novkostya/springback/core/internal/store"
	"github.com/novkostya/springback/core/internal/storefront"
	"github.com/novkostya/springback/core/internal/tools"
	"github.com/novkostya/springback/core/internal/version"
	"github.com/novkostya/springback/core/internal/webui"
)

// Server wires the packages together.
type Server struct {
	Tools tools.Tools
	// Auth is the password gate. Everything under /api except health and the auth endpoints
	// themselves requires a session.
	Auth    *auth.Service
	Devices *devices.Service
	Library *store.Library
	// DeviceIcons is the cache of icons read off the devices themselves — the only source
	// that has artwork for a delisted app that has not been archived.
	DeviceIcons *store.DeviceIcons
	Accounts    *store.Accounts
	Resolver    *storefront.Resolver
	Jobs        *jobs.Registry
	Log         *slog.Logger
	// Fake reports whether the tool layer is the fake one. The UI shows a banner: a screen
	// full of plausible device names that is not talking to any device would otherwise be
	// indistinguishable from the real thing.
	Fake bool
}

// Handler builds the mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.health)

	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("POST /api/auth/setup", s.authSetup)
	mux.HandleFunc("POST /api/auth/login", s.authLogin)
	mux.HandleFunc("POST /api/auth/logout", s.authLogout)

	mux.HandleFunc("GET /api/devices", s.listDevices)
	mux.HandleFunc("GET /api/devices/{udid}", s.deviceDetail)
	mux.HandleFunc("POST /api/devices/{udid}/pair", s.devicePair)
	mux.HandleFunc("POST /api/devices/{udid}/unpair", s.deviceUnpair)
	mux.HandleFunc("POST /api/devices/{udid}/wifi-sync", s.deviceWifiSync)
	mux.HandleFunc("GET /api/devices/{udid}/apps", s.deviceApps)
	mux.HandleFunc("GET /api/devices/{udid}/installed", s.deviceInstalled)
	mux.HandleFunc("POST /api/devices/{udid}/install", s.install)
	mux.HandleFunc("GET /api/devices/{udid}/icon.png", s.deviceIcon)

	mux.HandleFunc("GET /api/library", s.listLibrary)
	mux.HandleFunc("POST /api/library", s.addLibrary)
	mux.HandleFunc("DELETE /api/library/{id}", s.deleteLibrary)
	mux.HandleFunc("GET /api/library/{id}/icon.png", s.libraryIcon)

	mux.HandleFunc("GET /api/accounts", s.listAccounts)
	mux.HandleFunc("POST /api/accounts", s.addAccount)
	mux.HandleFunc("POST /api/accounts/{slug}/2fa", s.account2FA)
	mux.HandleFunc("DELETE /api/accounts/{slug}", s.deleteAccount)

	mux.HandleFunc("GET /api/jobs", s.listJobs)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)

	mux.HandleFunc("GET /api/lookup", s.lookup)

	mux.Handle("/", webui.Handler())
	return logging(s.Log, s.guard(mux))
}

func logging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The request LINE only. Bodies are never logged: the add-account body carries an
		// Apple ID password, which SPEC §3 says is held in memory and never persisted.
		log.Debug("http", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version": version.Version,
		"fake":    s.Fake,
	})
}

// ---------------------------------------------------------------------------
// Devices
// ---------------------------------------------------------------------------

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	devs, err := s.Devices.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	// An empty list is 200 with an empty array, never an error. Every device asleep is the
	// ordinary case (SPEC §3), and the UI says "not currently reachable", never "gone".
	if devs == nil {
		devs = []tools.Device{}
	}
	writeJSON(w, http.StatusOK, devs)
}

func (s *Server) deviceApps(w http.ResponseWriter, r *http.Request) {
	res, err := s.Devices.Apps(r.Context(), r.PathValue("udid"))
	if errors.Is(err, tools.ErrDeviceUnreachable) {
		// 409, not 404 or 500: the device exists and is paired, it is just asleep. The
		// distinction is the whole difference between "plug it in / wake it" and
		// "something is broken".
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":  "not_reachable",
			"detail": "This device is paired but not answering right now — a sleeping iPhone drops off the network entirely. Wake it and try again.",
			"device": res.Device,
		})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if res.Apps == nil {
		res.Apps = []devices.App{}
	}

	// Start pulling the icons now, in the background, so the list is not the thing that
	// discovers they are missing. Without this the first icon request pays for the whole warm
	// while holding one of the browser's few connections, and the poll behind it waits.
	//
	// NOT the request's context: this outlives the response on purpose, and cancelling it when
	// the browser has what it asked for would mean the warm never finishes. Deduplicated per
	// device, so the five-second device poll cannot stack these up.
	go func(udid string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.DeviceIcons.Warm(ctx, udid); err != nil {
			s.Log.Debug("icon warm failed", "udid", udid, "err", err)
		}
	}(r.PathValue("udid"))

	writeJSON(w, http.StatusOK, res)
}

// deviceInstalled lists just what is installed, with no store lookups at all.
//
// SEPARATE FROM /apps ON PURPOSE. That endpoint asks Apple about every one of 162 bundle ids and
// takes ~25 s cold; this one runs a single `ideviceinstaller list` and answers in under a second.
// The app detail screen needs only "is this app already on that device?" for each device, and
// making it pay for the at-risk scan of every device would be absurd.
func (s *Server) deviceInstalled(w http.ResponseWriter, r *http.Request) {
	apps, err := s.Tools.ListApps(r.Context(), r.PathValue("udid"))
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]map[string]any, 0, len(apps))
	for _, a := range apps {
		out = append(out, map[string]any{
			"bundle_id": a.BundleID,
			"version":   a.Version,
			"app_id":    a.AppID,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// deviceIcon serves the icon a device draws for one of its installed apps.
//
// The bundle id and version come in as query parameters rather than path segments because a
// bundle id is dotted and a version is dotted, and both read far better in a query than as two
// more path components to escape.
//
// THE FIRST REQUEST IS SLOW AND THE REST ARE NOT. A miss warms every uncached icon on that
// device in one batch — one SpringBoard connection instead of two hundred — so the handful of
// requests the browser makes for the first screenful all wait on the same run and then complete
// together. Roughly three seconds once per device, and nothing after that.
func (s *Server) deviceIcon(w http.ResponseWriter, r *http.Request) {
	udid := r.PathValue("udid")
	bundle := r.URL.Query().Get("bundle")
	version := r.URL.Query().Get("v")
	if bundle == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "bundle is required.")
		return
	}

	b, err := s.DeviceIcons.Get(r.Context(), udid, bundle, version)
	if err != nil {
		// Every outcome here is a 404 on purpose. An app with no icon, a device that went to
		// sleep mid-scroll and a bundle id that no longer exists are all the same thing to
		// the UI — draw the lettered tile — and none of them is worth a red banner over a
		// list the user is already reading.
		s.Log.Debug("no device icon", "udid", udid, "bundle", bundle, "err", err)
		http.NotFound(w, r)
		return
	}

	// The version is in the URL, so these bytes can never change under it: a new version is a
	// new URL. That makes the response genuinely immutable, which matters here more than
	// anywhere else in the app — a device list is two hundred images and the phone should
	// re-fetch none of them on the second visit.
	w.Header().Set("Content-Type", "image/png")
	if version != "" {
		w.Header().Set("Cache-Control", "private, max-age=604800, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=60, must-revalidate")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}

type installReq struct {
	LibraryID int64 `json:"library_id"`
}

func (s *Server) install(w http.ResponseWriter, r *http.Request) {
	var req installReq
	if !decode(w, r, &req) {
		return
	}
	item, err := s.Library.Get(req.LibraryID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_in_library", "That app is not in the library.")
		return
	}
	udid := r.PathValue("udid")

	// A JOB, NOT A HELD-OPEN REQUEST. ideviceinstaller already reports its stage and
	// percentage; until now that went to a nil callback and the user watched a spinner.
	target := udid
	if devs, err := s.Devices.List(r.Context()); err == nil {
		for _, d := range devs {
			if d.UDID == udid && d.Name != "" {
				target = d.Name
			}
		}
	}
	key := "install:" + strconv.FormatInt(item.ID, 10) + ":" + udid
	job := s.Jobs.Start(jobs.Install, key, item.Name, target, func(ctx context.Context, h *jobs.Handle) (any, error) {
		err := s.Tools.InstallApp(ctx, udid, s.Library.IPAPath(item.ID), func(p tools.InstallProgress) {
			h.Progress(p.Percent, p.Stage, "")
		})
		if err != nil {
			return nil, err
		}
		return item, nil
	})
	writeJSON(w, http.StatusAccepted, map[string]any{
		"job": job,
		// FairPlay defers to FIRST LAUNCH, not install (SPEC §7). An install onto a
		// device signed into a different Apple ID succeeds, and the user is asked to sign
		// in as the licensing account the first time they open the app. Saying so here is
		// what stops every cross-account install from looking broken.
		"note": "If this device is signed into a different Apple ID than the one that owns the app, iOS will ask for the owning Apple ID the first time you open it — that is normal, and it works from then on.",
	})
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Jobs.List())
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.Jobs.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "no_such_job", "That job is finished and no longer tracked.")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// ---------------------------------------------------------------------------
// Library
// ---------------------------------------------------------------------------

func (s *Server) listLibrary(w http.ResponseWriter, r *http.Request) {
	items, err := s.Library.List()
	if err != nil {
		s.fail(w, err)
		return
	}
	if items == nil {
		items = []store.LibraryItem{}
	}
	writeJSON(w, http.StatusOK, items)
}

type addLibraryReq struct {
	AppID       int64  `json:"app_id"`
	AccountSlug string `json:"account_slug"`
	// Label is what to call this download on screen while it runs. Cosmetic — the real name
	// is read out of the archive when it lands — but "downloading 6744684419" is a poor thing
	// to watch for two minutes.
	Label string `json:"label,omitempty"`
}

func (s *Server) addLibrary(w http.ResponseWriter, r *http.Request) {
	var req addLibraryReq
	if !decode(w, r, &req) {
		return
	}
	if req.AppID <= 0 {
		writeErr(w, http.StatusBadRequest, "bad_app_id", "A numeric App Store id is required.")
		return
	}
	acc, err := s.Accounts.Get(req.AccountSlug)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no_such_account", "Pick an Apple ID to download with.")
		return
	}

	out, err := s.Library.PrepareDir(req.AppID)
	if err != nil {
		s.fail(w, err)
		return
	}

	label := req.Label
	if label == "" {
		label = strconv.FormatInt(req.AppID, 10)
	}

	// A JOB, NOT A HELD-OPEN REQUEST. ~500 MB at 35 MB/s is tens of seconds on a good link
	// and minutes on a bad one, and the old shape gave the user a spinner and no number for
	// all of it.
	key := "download:" + strconv.FormatInt(req.AppID, 10)
	job := s.Jobs.Start(jobs.Download, key, label, "", func(ctx context.Context, h *jobs.Handle) (any, error) {
		// THE LONG PAUSE AT 99% IS REAL WORK, and saying so is the whole fix. Once the
		// bytes are down ipatool decrypts the package and repacks it, which takes tens of
		// seconds on a 200 MB app and reports nothing at all — so the bar sits at 99% and
		// looks hung. Reported from real use. The stall is detected and named rather than
		// hidden behind a fake-moving bar.
		var lastFrame atomic.Int64
		lastFrame.Store(time.Now().UnixMilli())
		var peak atomic.Int64

		stop := make(chan struct{})
		defer close(stop)
		go func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					quiet := time.Since(time.UnixMilli(lastFrame.Load()))
					if quiet > 4*time.Second && peak.Load() >= 95 {
						h.Progress(-1, "decrypting and repacking — no progress is reported for this part", "")
					}
				}
			}
		}()

		res, err := s.Tools.Download(ctx, acc.Home(s.Accounts.Root), acc.KeychainPP, req.AppID, out,
			func(p tools.DownloadProgress) {
				lastFrame.Store(time.Now().UnixMilli())
				if int64(p.Percent) > peak.Load() {
					peak.Store(int64(p.Percent))
				}
				h.Progress(p.Percent, "downloading", p.Detail)
			})
		if err != nil {
			// PrepareDir made the directory before ipatool ran; a failed download
			// must not leave it behind as a monument to a typo'd id.
			s.Library.DiscardIfEmpty(req.AppID)
			return nil, err
		}
		h.Progress(100, "reading the archive", "")
		item, err := s.Library.Record(req.AppID, acc.Slug)
		if err != nil {
			return nil, err
		}
		return map[string]any{"item": item, "purchased": res.Purchased}, nil
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func (s *Server) deleteLibrary(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", "Not a numeric id.")
		return
	}
	if err := s.Library.Delete(id); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// libraryIcon serves the app's icon, extracted from its .ipa on first request.
//
// A 404 here is ORDINARY, not an error worth reporting: plenty of archives have no icon this can
// find, and the UI's job is to draw a placeholder and stop asking. Nothing is logged at error
// level for it, or a library of such apps would fill the log on every page load.
func (s *Server) libraryIcon(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", "Not a numeric id.")
		return
	}

	b, err := s.Library.Icon(id)
	if err != nil {
		s.Log.Debug("no icon", "id", id, "err", err)
		http.NotFound(w, r)
		return
	}

	// Cached hard and keyed by id, because the bytes only change when the .ipa is replaced —
	// and Record() deletes the cache file when that happens. The URL is stable across an
	// update, so the response carries a validator the browser can use rather than
	// `immutable`: a phone that cached the old icon must be able to notice the new one.
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=60, must-revalidate")
	http.ServeContent(w, r, "icon.png", iconModTime(s.Library, id), bytes.NewReader(b))
}

// iconModTime is the cache file's timestamp, or the zero time when it cannot be read — which
// ServeContent takes as "no Last-Modified", not as 1970.
func iconModTime(lib *store.Library, id int64) time.Time {
	st, err := os.Stat(lib.IconPath(id))
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// ---------------------------------------------------------------------------
// Accounts
// ---------------------------------------------------------------------------

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	accs, err := s.Accounts.List()
	if err != nil {
		s.fail(w, err)
		return
	}
	// Public() drops the keychain passphrase. It is not an Apple secret, but it decrypts
	// ipatool's local credential file, and nothing in a browser needs it.
	out := make([]store.PublicAccount, len(accs))
	var wg sync.WaitGroup
	for i, a := range accs {
		out[i] = a.Public()
		// Ask ipatool whether each account's credentials are still readable, so an
		// expired or half-finished sign-in is visible HERE rather than surfacing as a
		// mysterious failure thirty seconds into a download. Concurrent because each is
		// a separate process launch; local-only, so it is fast.
		wg.Add(1)
		go func(i int, a store.Account) {
			defer wg.Done()
			if _, err := s.Tools.AuthInfo(r.Context(), a.Home(s.Accounts.Root), a.KeychainPP); err == nil {
				out[i].SignedIn = true
			}
		}(i, a)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, out)
}

type addAccountReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) addAccount(w http.ResponseWriter, r *http.Request) {
	var req addAccountReq
	if !decode(w, r, &req) {
		return
	}
	if req.Email == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "missing", "Email and password are both required.")
		return
	}
	acc, existed, err := s.Accounts.Create(req.Email)
	if err != nil {
		s.fail(w, err)
		return
	}

	err = s.Tools.AuthLogin(r.Context(), acc.Home(s.Accounts.Root), acc.KeychainPP, req.Email, req.Password, "")

	// A FAILED SIGN-IN MUST NOT LEAVE AN ACCOUNT BEHIND. Reported from the UI: a login that
	// failed still added a row, so the Accounts screen listed an Apple ID that had never been
	// signed in and offered it in every download picker.
	//
	// Rolled back only when THIS call created the record. An existing account keeps its row —
	// a failed re-authentication is a session problem, and deleting the account over it would
	// throw away the stored passphrase and read as data loss. ErrNeeds2FA is not a failure at
	// all: the record is exactly what the second call needs.
	if err != nil && !errors.Is(err, tools.ErrNeeds2FA) && !existed {
		if delErr := s.Accounts.Delete(acc.Slug); delErr != nil {
			s.Log.Error("could not roll back a failed sign-in", "slug", acc.Slug, "err", delErr)
		}
	}

	if errors.Is(err, tools.ErrNeeds2FA) {
		// 409 needs_2fa, per SPEC §5. The record already exists, so the second call needs
		// only the slug and the code — and, crucially, NOT the password again: springback
		// never holds it between requests.
		// A PROMPT IS NOT PROOF THAT A CODE WAS SENT, and this message used to claim it was.
		//
		// Measured: ipatool prints `INF enter 2FA code:` for an Apple ID THAT DOES NOT EXIST,
		// with a junk password — it falls through to asking whenever it cannot read Apple's
		// reply as a success. Its output is byte-identical in both cases, so springback
		// cannot tell a real challenge from Apple simply refusing the login, and must not
		// pretend otherwise. Reported as "it said 2FA but no code arrived", which is exactly
		// what the old wording set someone up to expect.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "needs_2fa",
			"slug":  acc.Slug,
			"detail": "Apple is asking for a verification code. If one arrived on your other devices, enter it. " +
				"IF NO CODE ARRIVED, none is coming: that is the current Apple outage refusing the sign-in, and " +
				"ipatool asks for a code anyway when it cannot read the reply. Leave it for a while rather than " +
				"retrying — accounts already signed in keep working.",
		})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.finishLogin(w, r, acc)
}

type twoFAReq struct {
	Code string `json:"code"`
	// Password is required again here and there is no way around it. ipatool's 2FA flow is
	// "re-run the SAME command adding --auth-code" (SPEC §3) — the same command, meaning the
	// same password on stdin. springback could only avoid asking twice by holding the
	// password between requests, which is exactly what it promises not to do.
	Password string `json:"password"`
}

func (s *Server) account2FA(w http.ResponseWriter, r *http.Request) {
	var req twoFAReq
	if !decode(w, r, &req) {
		return
	}
	acc, err := s.Accounts.Get(r.PathValue("slug"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "no_such_account", "Start the sign-in again.")
		return
	}
	if req.Code == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "missing", "Both the code and the password are needed — ipatool re-runs the whole login with the code attached.")
		return
	}
	if err := s.Tools.AuthLogin(r.Context(), acc.Home(s.Accounts.Root), acc.KeychainPP, acc.Email, req.Password, req.Code); err != nil {
		s.fail(w, err)
		return
	}
	s.finishLogin(w, r, acc)
}

// finishLogin records the display name ipatool reports, so the account list says who an Apple ID
// belongs to rather than only what it is called.
func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, acc store.Account) {
	if info, err := s.Tools.AuthInfo(r.Context(), acc.Home(s.Accounts.Root), acc.KeychainPP); err == nil && info.Name != "" {
		_ = s.Accounts.SetName(acc.Slug, info.Name)
		acc.Name = info.Name
	}
	writeJSON(w, http.StatusOK, acc.Public())
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.Accounts.Delete(r.PathValue("slug")); err != nil {
		writeErr(w, http.StatusNotFound, "no_such_account", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Lookup
// ---------------------------------------------------------------------------

func (s *Server) lookup(w http.ResponseWriter, r *http.Request) {
	bundle := r.URL.Query().Get("bundle_id")
	if bundle == "" {
		writeErr(w, http.StatusBadRequest, "missing", "bundle_id is required.")
		return
	}
	fronts := storefront.Storefronts(r.URL.Query().Get("region"))
	if r.URL.Query().Get("refresh") == "1" {
		s.Resolver.Forget(bundle)
	}
	res := s.Resolver.Resolve(r.Context(), bundle, fronts)

	// id is null rather than 0 when nothing found it, per SPEC §5's {id|null, checked:[cc]}.
	var id *int64
	if res.TrackID != 0 {
		v := res.TrackID
		id = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      id,
		"checked": res.Checked,
		"status":  res.Status,
		"version": res.Version,
		"size":    res.FileSize,
		"errors":  res.Errors,
	})
}

// ---------------------------------------------------------------------------
// Plumbing
// ---------------------------------------------------------------------------

// fail maps a tools error onto a status and, more importantly, onto WHAT TO DO ABOUT IT.
// SPEC §7 lists each failure mode next to its meaning; that table is this function.
func (s *Server) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tools.ErrAppNotFound):
		writeErr(w, http.StatusNotFound, "app_not_found",
			"Apple does not know that numeric id. Check the digits — for a delisted app the id is the only handle there is, and no search will find it.")
	case errors.Is(err, tools.ErrLicenseNotFound):
		writeErr(w, http.StatusForbidden, "license_not_found",
			"This Apple ID does not own that app. Try another account — the licence belongs to whoever originally got it.")
	case errors.Is(err, tools.ErrNotAuthenticated):
		// SIGN IN AGAIN, DO NOT REMOVE. Adding an Apple ID that is already on file reuses
		// its record — same slug, same keychain passphrase, same HOME — so the session is
		// refreshed in place and every library entry's account_slug still points at it.
		// The first version of this message said to remove and re-add, which throws away
		// the stored passphrase for no reason and reads as data loss.
		writeErr(w, http.StatusUnauthorized, "not_authenticated",
			"That Apple ID's session has expired or was never completed. Go to Accounts and sign in with the same address again — the account stays where it is, nothing is lost.")
	case errors.Is(err, tools.ErrDeviceUnreachable):
		writeErr(w, http.StatusConflict, "not_reachable",
			"The device is not answering. A sleeping iPhone drops off the network entirely — wake it and try again.")
	case errors.Is(err, tools.ErrInstallIncomplete):
		writeErr(w, http.StatusBadGateway, "install_incomplete", err.Error())
	case errors.Is(err, tools.ErrNeeds2FA):
		writeErr(w, http.StatusConflict, "needs_2fa", "A verification code is required.")
	case errors.Is(err, tools.ErrAppleRejected):
		// 502: the failure is upstream, not in the request. Saying so matters, because the
		// natural reading of a failed sign-in is "I typed something wrong" and the user will
		// otherwise retype a correct password until Apple rate-limits them for it.
		//
		// The status Apple gave is included verbatim: it is the only part that varies, and
		// it is what makes the failure reportable rather than just frustrating.
		writeErr(w, http.StatusBadGateway, "apple_rejected",
			appleRejectedAdvice(err))
	default:
		s.Log.Error("request failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "error", err.Error())
	}
}

// appleRejectedAdvice says what happened, that it is not the user's fault, and what to actually
// do — including the one thing that still works, since "everything is broken" is both wrong and
// unhelpful when only new sign-ins are affected.
func appleRejectedAdvice(err error) string {
	status := ""
	if s := err.Error(); strings.Contains(s, "HTTP ") {
		status = " (" + s[strings.LastIndex(s, "HTTP "):] + ")"
	}
	return "Apple refused the sign-in" + status + " — it answered with an error instead of a login result. " +
		"This is NOT a wrong password, and not something springback can retry its way out of: Apple is " +
		"currently rejecting the unofficial Store client, and the status it returns changes between " +
		"attempts (204, 403, 503 …). Verified here by running the same tool directly, with springback out " +
		"of the picture, against an address that does not exist — same failure. It is upstream ipatool " +
		"issue #513, which is open with no fix available. " +
		"What to do: leave it for a while rather than retrying, since repeated attempts risk a rate-limit " +
		"on top of the outage. Apple IDs already signed in keep working — their sessions are stored, so " +
		"browsing devices, downloading and installing are all unaffected."
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	// 1 MB is far more than any request here needs and stops an accidental upload from
	// becoming a memory problem.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "Could not read that request.")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// The API answers the browser on the same origin only; nothing here is meant to be
	// callable from a page somebody else served.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, kind, detail string) {
	writeJSON(w, code, map[string]string{"error": kind, "detail": detail})
}
