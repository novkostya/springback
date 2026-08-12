package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/novkostya/springback/core/internal/auth"
	"github.com/novkostya/springback/core/internal/live"
	"github.com/novkostya/springback/core/internal/tools"
	"github.com/novkostya/springback/core/internal/version"
)

const (
	// deviceScanInterval is how often the ONE server-side watcher asks who is there. The same
	// five seconds each browser used to spend on its own — slow enough to be free, fast enough
	// that plugging a phone in feels like it did something.
	deviceScanInterval = 5 * time.Second
	// jobPublishInterval is the floor between two job frames. ipatool reports progress many
	// times a second and every frame would otherwise be a push to every open tab, to move a
	// bar by less than a pixel. Four a second is smoother than the one-second poll it replaces
	// and still a fraction of the traffic.
	jobPublishInterval = 250 * time.Millisecond
)

// setupLive builds the event bus and the watcher behind it.
//
// NOTHING RUNS UNTIL A BROWSER CONNECTS. The watcher goroutine is started by the first subscriber
// and then stays up, idling in one select while nobody is listening. A springback sitting in a
// container with no page open must not be asking a phone for its name every five seconds until
// somebody stops it.
func (s *Server) setupLive() {
	s.kick = make(chan struct{}, 1)
	s.jobsDirty = make(chan struct{}, 1)
	var start sync.Once

	s.events = live.NewBus(func() {
		// Called on the transition from nobody to somebody. sync.Once rather than a bool
		// because that transition happens again every time the last browser closes and
		// another opens, and two of them can overlap.
		start.Do(func() {
			go s.watchDevices(context.Background())
			go s.watchJobs(context.Background())
		})
		s.Kick()
	})

	// Every change to a job is a change worth pushing, and the registry is the only thing that
	// knows when one happens. Coalesced downstream — this is called per progress frame.
	//
	// Nil-tolerant because the auth tests build a Server with only the fields the gate needs,
	// and a live socket is not one of them.
	if s.Jobs == nil {
		return
	}
	s.Jobs.Notify(func() {
		select {
		case s.jobsDirty <- struct{}{}:
		default:
		}
	})
}

// sessionStillValid re-answers, for an already-open socket, the question the guard answered once
// at the upgrade. Deliberately auth.Valid and not auth.Check: a socket must be able to notice its
// session dying without being the thing that keeps it alive.
func (s *Server) sessionStillValid(r *http.Request) bool {
	return s.Auth.Valid(auth.TokenFrom(r))
}

// Kick asks for a device scan now rather than at the next tick. Used by the Refresh button and by
// anything that changes a device's state — pairing, unpairing, Wi-Fi sync — since those change
// what the list says and waiting five seconds to admit it looks like the action failed.
func (s *Server) Kick() {
	if s.kick == nil {
		return
	}
	select {
	case s.kick <- struct{}{}:
	default:
		// A scan is already pending. Two kicks are one scan.
	}
}

func (s *Server) watchDevices(ctx context.Context) {
	t := time.NewTicker(deviceScanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// The tick is for change nobody asked about — a phone waking up, a cable
			// pulled. With no listener there is nobody to tell.
			if s.events.Listeners() == 0 {
				continue
			}
		case <-s.kick:
			// A kick is always honoured, listener or not: something just changed a
			// device's state and the list is about to disagree with it.
		}
		_, _ = s.scanDevices(ctx)
	}
}

// scanDevices publishes the device list, and ONLY WHEN IT HAS CHANGED.
//
// The browser rebuilds a screen from each frame it receives, and the device page holds a search
// box that is destroyed and recreated with it. Publishing an identical list every five seconds
// would take the keyboard away from anyone typing into it — which is the same defect the old
// client-side poll had to guard against, moved here where one comparison serves every tab.
func (s *Server) scanDevices(ctx context.Context) ([]tools.Device, error) {
	if s.Devices == nil {
		return []tools.Device{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	devs, err := s.Devices.List(ctx)
	if err != nil {
		// No muxer, most likely, and it is self-healing: the next scan succeeds the moment
		// one appears. Publishing an empty list over the last good one would render every
		// device as gone for the duration of a container restart.
		s.Log.Debug("device scan failed", "err", err)
		return nil, err
	}
	if devs == nil {
		devs = []tools.Device{}
	}
	b, err := json.Marshal(devs)
	if err != nil {
		return devs, nil
	}

	s.mu.Lock()
	same := s.lastDevices == string(b)
	s.lastDevices = string(b)
	s.mu.Unlock()
	if !same {
		s.events.Publish(live.Envelope{Type: live.TypeDevices, Data: devs})
	}
	return devs, nil
}

func (s *Server) watchJobs(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.jobsDirty:
		}
		s.events.Publish(live.Envelope{Type: live.TypeJobs, Data: s.Jobs.List()})

		// Then hold off, so a torrent of progress frames becomes four pushes a second. The
		// dirty channel holds one pending signal while this sleeps, so the LAST state change
		// in the quiet window is always published afterwards — a job finishing during the
		// pause is never the one that gets swallowed.
		select {
		case <-ctx.Done():
			return
		case <-time.After(jobPublishInterval):
		}
	}
}

// liveHello is what a new connection is told before anything happens.
//
// A snapshot, not just a greeting. Without it a browser that connects while nothing is changing
// waits for the first event to learn anything — and "nothing is changing" is the normal state of
// this app. With it, opening the socket is as good as a fetch.
func (s *Server) liveHello() []live.Envelope {
	hello := map[string]any{"version": version.Version, "fake": s.Fake}
	// WHETHER PAIRING RECORDS CAN BE WRITTEN IS A FACT ABOUT THE BOX, not about a device — the
	// same answer for every one of them. It used to be reported only per-device, so the device
	// page drew its pairing controls from what it could see and then withdrew them a moment later
	// when the fetch admitted the directory was read-only. Sent once, up front, it is known before
	// any device page is opened.
	if s.Tools != nil {
		hello["pairing_writable"] = s.Tools.PairingWritable()
	}
	out := []live.Envelope{{Type: live.TypeHello, Data: hello}}

	s.mu.Lock()
	last := s.lastDevices
	s.mu.Unlock()
	if last != "" {
		// Re-sent as the raw message the last scan produced, rather than re-listed: this is
		// the state every other listener already has, and asking the devices again here
		// would put a dozen lockdown round trips in front of a page that is trying to open.
		out = append(out, live.Envelope{Type: live.TypeDevices, Data: json.RawMessage(last)})
	}
	if s.Jobs != nil {
		out = append(out, live.Envelope{Type: live.TypeJobs, Data: s.Jobs.List()})
	}
	return out
}
