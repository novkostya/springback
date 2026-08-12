// Package live pushes state changes to open browsers instead of making them ask.
//
// WHAT THIS REPLACES, AND WHY IT WAS NOT GOOD ENOUGH. Every screen was driven by two timers: the
// device list every five seconds and the job list every second while anything was running. That
// is workable and it is what shipped, but it has three faults that no amount of tuning removes.
// A device plugged back in took up to five seconds to appear, and the lag was blamed on the tool
// rather than on the interval. Progress moved in one-second steps, so a fast install was three
// jumps. And the cost scaled with the number of open tabs — each one independently asking a
// device for its name, its model, its iOS version and its region, four lockdown round trips per
// device per poll, forever.
//
// Now ONE watcher polls, in the server, and only while at least one browser is listening. What it
// finds is fanned out to every listener at once. The polling has not disappeared — nothing here
// can make usbmuxd push events, because springback reaches devices by running `idevice_id` — but
// it happens once instead of once per tab, and what it learns arrives immediately.
//
// SERVER TO CLIENT ONLY. Every command is still an ordinary HTTP request: this socket carries no
// instructions, so a client that cannot open one loses nothing but the immediacy, and the timers
// are still there to fall back on.
package live

import (
	"sync"
)

// Envelope is the frame shape: a type and a payload, and nothing else.
//
// The payloads are deliberately the SAME BODIES the REST endpoints return — a `devices` frame is
// what `GET /api/devices` would have said, a `jobs` frame is `GET /api/jobs`. That is what keeps
// the browser from having two ways to understand the same fact: the socket feeds the code that
// already existed to handle the fetch, and the fallback path is not a second implementation.
type Envelope struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// The frame types. A short list on purpose — this socket exists to say "look again", not to
// replicate the server's state into the browser.
const (
	// TypeHello is the first frame on every connection, so a client can tell a working socket
	// from one that opened and then sat silent because nothing has changed yet.
	TypeHello = "hello"
	// TypeDevices carries the whole device list. Whole, not a delta: it is a handful of small
	// objects, and a delta protocol would need a resync path for the one case — a dropped
	// frame — that sending everything makes impossible.
	TypeDevices = "devices"
	// TypeJobs carries every tracked job, for the same reason.
	TypeJobs = "jobs"
)

// Subscription is one listener's queue.
type Subscription struct {
	ch      chan Envelope
	dropped chan struct{}
	once    sync.Once
}

// C delivers envelopes until Unsubscribe closes it.
func (s *Subscription) C() <-chan Envelope { return s.ch }

// Dropped closes if this subscriber fell behind and was cut loose.
func (s *Subscription) Dropped() <-chan struct{} { return s.dropped }

func (s *Subscription) drop() { s.once.Do(func() { close(s.dropped) }) }

// Bus fans envelopes out to every current subscriber.
type Bus struct {
	mu   sync.RWMutex
	subs map[*Subscription]struct{}
	// onSubscribe is called when the FIRST subscriber arrives, so whatever produces events can
	// stay idle while nobody is listening. A springback with no browser open should do nothing
	// at all, not poll a phone every five seconds until the container is stopped.
	onSubscribe func()
}

// NewBus returns an empty bus. wake is called when the subscriber count rises from zero, and may
// be nil.
func NewBus(wake func()) *Bus {
	return &Bus{subs: map[*Subscription]struct{}{}, onSubscribe: wake}
}

// Subscribe registers a listener with the given queue depth.
func (b *Bus) Subscribe(depth int) *Subscription {
	if depth < 1 {
		depth = 1
	}
	s := &Subscription{ch: make(chan Envelope, depth), dropped: make(chan struct{})}

	b.mu.Lock()
	b.subs[s] = struct{}{}
	first := len(b.subs) == 1
	wake := b.onSubscribe
	b.mu.Unlock()

	// Outside the lock: the woken producer will want to publish, and publishing takes the read
	// lock. Calling it while holding the write lock would be a deadlock waiting for a slow day.
	if first && wake != nil {
		wake()
	}
	return s
}

// Unsubscribe removes a listener and closes its channel. Idempotent.
//
// Publish holds the READ lock while sending and this holds the WRITE lock while closing, so a
// send can never race a close: send-on-closed-channel is impossible by construction rather than
// by timing.
func (b *Bus) Unsubscribe(s *Subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[s]; ok {
		delete(b.subs, s)
		close(s.ch)
	}
}

// Listeners is how many browsers are currently connected.
func (b *Bus) Listeners() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Publish delivers to every subscriber WITHOUT EVER BLOCKING. A listener whose queue is full is
// dropped instead: its socket is torn down, the browser reconnects, and the reconnect refetches
// everything. Blocking here would let one wedged phone on a bad wifi hold up the device watcher
// for every other tab, which is a far worse failure than one client having to reconnect.
func (b *Bus) Publish(env Envelope) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		select {
		case s.ch <- env:
		default:
			s.drop()
		}
	}
}
