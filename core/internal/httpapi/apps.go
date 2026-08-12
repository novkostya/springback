package httpapi

import (
	"net/http"
	"strings"

	"github.com/novkostya/springback/core/internal/devices"
)

// ownedApps answers "what do I own", from what springback has already seen rather than from Apple.
//
// SEARCH HAPPENS HERE RATHER THAN IN THE BROWSER, and that is a deliberate difference from the
// per-device list, which filters client-side because it already has every row. This list can span
// several devices and years of sightings, so the browser would be filtering a payload it did not
// need — and the phone asking is usually the slowest thing in the room.
func (s *Server) ownedApps(w http.ResponseWriter, r *http.Request) {
	if s.Devices == nil || s.Devices.Seen == nil {
		writeErr(w, http.StatusNotFound, "no_history", "This springback is not remembering device app lists.")
		return
	}

	owned, err := s.Devices.Owned(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}

	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		owned.Apps = filterApps(owned.Apps, q)
	}
	// Total, Delisted and InLibrary deliberately keep describing the WHOLE set rather than the
	// filtered one: they are the counts the screen's summary line reports, and a summary that
	// changed as you typed would be describing your search rather than your library.
	writeJSON(w, http.StatusOK, owned)
}

// filterApps matches on name and bundle id, case-insensitively.
//
// TWO FIELDS, because people search with either and do not think of them as different things. The
// name is what they remember; the bundle id is what they have if they came from a screenshot, a
// forum post or springback's own device list.
func filterApps(apps []devices.OwnedApp, q string) []devices.OwnedApp {
	q = strings.ToLower(q)
	out := make([]devices.OwnedApp, 0, len(apps))
	for _, a := range apps {
		if strings.Contains(strings.ToLower(a.Name), q) ||
			strings.Contains(strings.ToLower(a.BundleID), q) {
			out = append(out, a)
		}
	}
	return out
}
