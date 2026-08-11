// Package httpapi serves SPEC §5 and the embedded UI.
//
// THERE IS NO AUTH HERE, and that is a decision rather than an omission (SPEC §1 puts it
// explicitly out of v0.1). Keep it LAN-only: a session cookie in /accounts/ is enough to
// download as that Apple ID, so anyone who can reach this port can act as every Apple ID signed
// in to it. The README says so in the same words.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/novkostya/springback/core/internal/devices"
	"github.com/novkostya/springback/core/internal/store"
	"github.com/novkostya/springback/core/internal/storefront"
	"github.com/novkostya/springback/core/internal/tools"
	"github.com/novkostya/springback/core/internal/version"
	"github.com/novkostya/springback/core/internal/webui"
)

// Server wires the packages together.
type Server struct {
	Tools    tools.Tools
	Devices  *devices.Service
	Library  *store.Library
	Accounts *store.Accounts
	Resolver *storefront.Resolver
	Log      *slog.Logger
	// Fake reports whether the tool layer is the fake one. The UI shows a banner: a screen
	// full of plausible device names that is not talking to any device would otherwise be
	// indistinguishable from the real thing.
	Fake bool
}

// Handler builds the mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.health)

	mux.HandleFunc("GET /api/devices", s.listDevices)
	mux.HandleFunc("GET /api/devices/{udid}/apps", s.deviceApps)
	mux.HandleFunc("POST /api/devices/{udid}/install", s.install)

	mux.HandleFunc("GET /api/library", s.listLibrary)
	mux.HandleFunc("POST /api/library", s.addLibrary)
	mux.HandleFunc("DELETE /api/library/{id}", s.deleteLibrary)

	mux.HandleFunc("GET /api/accounts", s.listAccounts)
	mux.HandleFunc("POST /api/accounts", s.addAccount)
	mux.HandleFunc("POST /api/accounts/{slug}/2fa", s.account2FA)
	mux.HandleFunc("DELETE /api/accounts/{slug}", s.deleteAccount)

	mux.HandleFunc("GET /api/lookup", s.lookup)

	mux.Handle("/", webui.Handler())
	return logging(s.Log, mux)
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
	writeJSON(w, http.StatusOK, res)
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
	// The request is held open: installs are slow and v0.1 has no job queue (SPEC §5). The
	// UI says so before starting one.
	err = s.Tools.InstallApp(r.Context(), r.PathValue("udid"), s.Library.IPAPath(item.ID), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installed": item,
		// FairPlay defers to FIRST LAUNCH, not install (SPEC §7). An install onto a
		// device signed into a different Apple ID succeeds, and the user is asked to sign
		// in as the licensing account the first time they open the app. Saying so here is
		// what stops every cross-account install from looking broken.
		"note": "Installed. If this device is signed into a different Apple ID than the one that owns the app, iOS will ask for the owning Apple ID the first time you open it — that is normal, and it works from then on.",
	})
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
	// Held open for ~30 s or more (SPEC §5 allows it for v0.1; the UI says so).
	res, err := s.Tools.Download(r.Context(), acc.Home(s.Accounts.Root), acc.KeychainPP, req.AppID, out)
	if err != nil {
		s.fail(w, err)
		return
	}
	item, err := s.Library.Record(req.AppID, acc.Slug)
	if err != nil {
		s.fail(w, err)
		return
	}
	// A newly archived app teaches the resolver nothing, but it does teach the LIBRARY a
	// bundle-id -> numeric-id pair, which is what makes the next device's archive one click.
	writeJSON(w, http.StatusOK, map[string]any{"item": item, "purchased": res.Purchased})
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
	out := make([]store.PublicAccount, 0, len(accs))
	for _, a := range accs {
		out = append(out, a.Public())
	}
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
	acc, err := s.Accounts.Create(req.Email)
	if err != nil {
		s.fail(w, err)
		return
	}

	err = s.Tools.AuthLogin(r.Context(), acc.Home(s.Accounts.Root), acc.KeychainPP, req.Email, req.Password, "")
	if errors.Is(err, tools.ErrNeeds2FA) {
		// 409 needs_2fa, per SPEC §5. The record already exists, so the second call needs
		// only the slug and the code — and, crucially, NOT the password again: springback
		// never holds it between requests.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "needs_2fa",
			"slug":  acc.Slug,
			"detail": "Apple sent a verification code to your other devices. " +
				"Enter it to finish signing in.",
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
		writeErr(w, http.StatusUnauthorized, "not_authenticated",
			"That account is not signed in any more. Remove it on the Accounts screen and add it again.")
	case errors.Is(err, tools.ErrDeviceUnreachable):
		writeErr(w, http.StatusConflict, "not_reachable",
			"The device is not answering. A sleeping iPhone drops off the network entirely — wake it and try again.")
	case errors.Is(err, tools.ErrInstallIncomplete):
		writeErr(w, http.StatusBadGateway, "install_incomplete", err.Error())
	case errors.Is(err, tools.ErrNeeds2FA):
		writeErr(w, http.StatusConflict, "needs_2fa", "A verification code is required.")
	default:
		s.Log.Error("request failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "error", err.Error())
	}
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
