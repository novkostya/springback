package httpapi

import (
	"errors"
	"net/http"

	"github.com/novkostya/springback/core/internal/tools"
)

// deviceDetail is everything the device page shows that is not the app list.
//
// SEPARATE FROM THE DEVICE LIST ON PURPOSE. Pairing state and the Wi-Fi flag are two more round
// trips to the device, and the list refreshes every five seconds across every device — paying
// for them there would turn a cheap poll into a dozen device calls a second for facts nobody is
// looking at.
func (s *Server) deviceDetail(w http.ResponseWriter, r *http.Request) {
	udid := r.PathValue("udid")

	dev, err := s.Devices.Get(r.Context(), udid)
	if err != nil {
		s.fail(w, err)
		return
	}

	// THE RECORDS ANSWER FIRST, AND ONLY THEN THE DEVICE.
	//
	// `idevicepair validate` asks the DEVICE whether the record still works, which is the better
	// question — and useless for the case that matters, because a muxer will not connect to a
	// device it holds no record for. It returns "No device found", springback read that as
	// "could not ask", and the page said *Pairing state unknown — the device is not answering*
	// about a phone sitting on the cable in plain sight. Reported with a screenshot.
	//
	// Having no record IS the answer to "is this host paired", and it needs nothing from the
	// device to know it. Validate is then only asked when a record exists, which is exactly when
	// it can be trusted to reply.
	pair := dev.Pair
	if pair == tools.Paired {
		got, err := s.Tools.PairStatus(r.Context(), udid)
		if err != nil {
			// Not fatal: the rest of the page is still worth drawing, and a record that
			// exists is better evidence than a device that would not answer.
			s.Log.Debug("pair status failed", "udid", udid, "err", err)
		} else if got != tools.PairUnknown {
			// A record that the device rejects — restored from a backup, or the phone was
			// wiped — is genuinely unpaired, and only the device can say so.
			pair = got
		}
	}

	// Only asked of a device that is actually paired and awake. On an unpaired device the read
	// needs a trusted session it does not have, so it would fail every time and report
	// "unknown" — which reads as a fault rather than as "pair it first".
	wifi := tools.WifiSyncUnknown
	if pair == tools.Paired && dev.Reachable {
		if got, err := s.Tools.WifiSync(r.Context(), udid); err == nil {
			wifi = got
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"device":    dev,
		"pair":      pair,
		"wifi_sync": wifi,
		// Kept for compatibility with anything reading this endpoint directly. The UI takes it
		// from the device list instead, where it is refreshed with everything else — this is a
		// snapshot from whenever the page happened to be opened.
		"transport": dev.Transport,
		// Whether the controls can work at all, so the UI can explain a disabled button
		// instead of offering one that always fails.
		"can_pair": s.Tools.PairingWritable(),
	})
}

func (s *Server) devicePair(w http.ResponseWriter, r *http.Request) {
	if err := s.Tools.Pair(r.Context(), r.PathValue("udid")); err != nil {
		s.failPairing(w, err)
		return
	}
	// A device that has just been paired is reachable in a way it was not a moment ago. Tell the
	// watcher now rather than letting the list disagree with the page for five seconds.
	s.Kick()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deviceUnpair(w http.ResponseWriter, r *http.Request) {
	if err := s.Tools.Unpair(r.Context(), r.PathValue("udid")); err != nil {
		s.failPairing(w, err)
		return
	}
	// The icon cache is keyed by udid and is meaningless for a device this host can no longer
	// talk to. Dropping it here keeps an unpair from leaving several megabytes of pictures of
	// somebody else's apps on disk.
	if s.DeviceIcons != nil {
		_ = s.DeviceIcons.Forget(r.PathValue("udid"))
	}
	// And the remembered name, model and iOS version. That cache exists so an OFFLINE device
	// still renders as a phone rather than as forty characters of hex; a device this host has
	// been told to forget has no row to render, and keeping somebody's phone name on disk after
	// being asked to forget their phone is the wrong default.
	if s.Devices != nil && s.Devices.Cache != nil {
		s.Devices.Cache.Forget(r.PathValue("udid"))
	}
	// And its remembered apps, for the same reason and more strongly: that list names every app
	// on somebody's phone. "Forget this device" has to mean it, or the Apps screen would go on
	// listing a device that springback has just claimed not to know.
	if s.Devices != nil && s.Devices.Seen != nil {
		_ = s.Devices.Seen.Forget(r.PathValue("udid"))
	}
	s.Kick()
	w.WriteHeader(http.StatusNoContent)
}

type wifiSyncReq struct {
	Enable bool `json:"enable"`
}

func (s *Server) deviceWifiSync(w http.ResponseWriter, r *http.Request) {
	var req wifiSyncReq
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.Tools.SetWifiSync(r.Context(), r.PathValue("udid"), req.Enable); err != nil {
		if errors.Is(err, tools.ErrWifiSyncNotApplied) {
			writeErr(w, http.StatusConflict, "not_applied",
				"The device accepted the change and did not apply it. Unlock it and try again.")
			return
		}
		s.fail(w, err)
		return
	}
	// Turning Wi-Fi sync OFF takes the device off the network entirely — it stops being
	// reachable within seconds, and the list should say so without being asked.
	s.Kick()
	w.WriteHeader(http.StatusNoContent)
}

// failPairing maps the pairing errors onto the four things a person can actually do about them.
// Each one is a different physical action, which is the whole reason they are separate values.
func (s *Server) failPairing(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tools.ErrTrustPending):
		writeErr(w, http.StatusConflict, "trust_pending",
			"The device is asking whether to trust this computer. Unlock it, tap Trust, then pair again.")
	case errors.Is(err, tools.ErrPasscodeLocked):
		writeErr(w, http.StatusConflict, "locked",
			"The device is locked. Unlock it and try again.")
	case errors.Is(err, tools.ErrNeedsUSB):
		writeErr(w, http.StatusConflict, "needs_usb",
			"Pairing happens over USB. Connect the device with a cable and try again — after it is paired, Wi-Fi is enough.")
	case errors.Is(err, tools.ErrPairingReadOnly):
		writeErr(w, http.StatusConflict, "read_only",
			"The pairing directory is mounted read-only, so springback cannot write a pairing record. Mount it read-write, or pair the device with whatever else owns it.")
	default:
		s.fail(w, err)
	}
}
