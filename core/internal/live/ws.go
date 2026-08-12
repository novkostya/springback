package live

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const (
	// queueDepth is how far behind a browser may fall before it is dropped. Frames are whole
	// snapshots, so a client that is behind by twenty of them is behind by one — but it is also
	// plainly not reading, and reconnecting is how it recovers.
	queueDepth = 32
	// writeTimeout bounds a single frame. A phone that has walked out of wifi range accepts
	// bytes into a socket that will never drain; without this the writer would sit there.
	writeTimeout = 10 * time.Second
	// pingInterval keeps the connection alive through anything that reaps idle sockets — which
	// is most reverse proxies, at 60 seconds by default. Nothing may be published for hours,
	// so without a ping this socket's normal state is indistinguishable from a dead one.
	pingInterval = 30 * time.Second
)

// Handler serves the event socket.
//
// AUTHENTICATION IS NOT HERE, AND THAT IS ON PURPOSE. This handler is registered on the same mux
// as everything else under /api, so the session guard has already run and already rejected
// anything without a cookie — before the upgrade, which is the only moment a WebSocket can be
// refused with a status code the browser will show. Putting a second check here would be a second
// place to get it wrong.
//
// `initial` supplies the frames every new connection gets before it starts listening: the greeting
// and a snapshot of everything it would otherwise have to wait for a change to learn.
//
// `stillValid` is re-asked on every ping, because the upgrade check is a check of ONE MOMENT and
// this connection outlives it by hours. A session that expires — or a sign-out in another tab —
// must take the socket with it, or a page nobody is signed in to carries on being told the names
// of somebody's phones. It must not count as activity: see auth.Valid.
func (b *Bus) Handler(initial func() []Envelope, stillValid func(*http.Request) bool, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ORIGIN, STRICTLY, AND THIS ONE DOES MATTER. A WebSocket handshake is not subject to
		// CORS: another site can open one against this port and the browser will attach the
		// cookies it would attach to any other subresource request. SameSite=Lax already stops
		// that, so this is the second lock rather than the first — but it costs one comparison
		// and it closes the hole outright.
		//
		// A MISSING Origin IS REFUSED. Browsers always send one on a WebSocket handshake, so
		// the only things it excludes are non-browser clients, and this socket has no
		// non-browser users.
		if !sameOrigin(r) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// Origin is checked above, so the library's own check is redundant rather than
			// skipped. Its version compares against a list of patterns that would have to be
			// configured; ours compares against the Host the request actually arrived on,
			// which needs no configuration and is right behind a proxy as well.
			InsecureSkipVerify: true,
		})
		if err != nil {
			log.Debug("websocket upgrade failed", "err", err)
			return
		}
		defer func() { _ = conn.CloseNow() }()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// SUBSCRIBE BEFORE SENDING THE SNAPSHOT, or there is a window between reading the
		// state and starting to listen in which a change is lost — and a lost change here is
		// permanent, because nothing re-sends. Subscribing first can only duplicate a frame,
		// which costs a redraw.
		sub := b.Subscribe(queueDepth)
		defer b.Unsubscribe(sub)

		// Read, purely to notice the client leaving. Nothing is ever sent up this socket, but
		// close and pong frames only arrive if something reads, and a read error is the
		// earliest sign the browser is gone.
		go func() {
			for {
				if _, _, err := conn.Read(ctx); err != nil {
					cancel()
					return
				}
			}
		}()

		for _, env := range initial() {
			if err := write(ctx, conn, env); err != nil {
				return
			}
		}

		ping := time.NewTicker(pingInterval)
		defer ping.Stop()

		for {
			select {
			case <-ctx.Done():
				_ = conn.Close(websocket.StatusNormalClosure, "bye")
				return
			case <-sub.Dropped():
				// Say why. The client reconnects either way, but a close reason is the
				// difference between diagnosing this in one look and guessing at it.
				_ = conn.Close(websocket.StatusPolicyViolation, "not keeping up")
				return
			case env := <-sub.C():
				if err := write(ctx, conn, env); err != nil {
					return
				}
			case <-ping.C:
				if stillValid != nil && !stillValid(r) {
					_ = conn.Close(websocket.StatusPolicyViolation, "session expired")
					return
				}
				pctx, pcancel := context.WithTimeout(ctx, writeTimeout)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil {
					return
				}
			}
		}
	}
}

func write(ctx context.Context, conn *websocket.Conn, env Envelope) error {
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return wsjson.Write(wctx, conn, env)
}

// sameOrigin compares the Origin's host against the Host the request arrived on.
//
// HOST AND PORT, NOT THE SCHEME — the same rule the CSRF check uses, and for the same reason: a
// reverse proxy terminating TLS makes the browser say `https://` while the request reaches
// springback as plain http, and that is the deployment the README recommends.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}
