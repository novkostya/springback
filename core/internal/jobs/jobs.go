// Package jobs tracks slow work so the UI can show progress instead of a blank page.
//
// SPEC §5 allowed v0.1 to hold the request open for downloads and installs, and it was the right
// call for a one-day build — but a ~500 MB fetch behind a spinner with no number on it is the
// worst part of using the tool, and an install is slower still. This is the smallest thing that
// fixes both: the request starts a job and returns immediately, the work carries on in the
// background, and the UI polls.
//
// IN MEMORY, DELIBERATELY. A job is only interesting while it runs and for a moment afterwards;
// persisting them would mean reconciling a "running" job that died with the process, which is a
// state nobody can act on. A restart mid-download loses the job record and leaves a partial file,
// which the library already ignores for want of a meta.json.
package jobs

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Kind distinguishes the two slow operations.
type Kind string

const (
	Download Kind = "download"
	Install  Kind = "install"
	// SignIn retries a login while Apple is refusing them.
	SignIn Kind = "signin"
)

// State is where a job has got to.
type State string

const (
	Running State = "running"
	Done    State = "done"
	Failed  State = "failed"
)

// Job is one unit of slow work, as the UI sees it.
type Job struct {
	ID   string `json:"id"`
	Kind Kind   `json:"kind"`
	// Key identifies the WORK, not the request: "download:6744684419" or
	// "install:6744684419:<udid>". Two requests with the same key are the same job.
	// Exposed so the UI can find the job belonging to a given app or device row.
	Key   string `json:"key,omitempty"`
	State State  `json:"state"`
	// Label is what to call this on screen — the app's name, not its id.
	Label string `json:"label"`
	// Target names where an install is going, and is empty for a download.
	Target string `json:"target,omitempty"`
	// Percent is -1 when the underlying tool has not reported one yet, which is honest:
	// ipatool prints nothing for the first second or two, and a bar sitting at 0% is
	// indistinguishable from a stalled one.
	Percent int    `json:"percent"`
	Detail  string `json:"detail,omitempty"`
	Stage   string `json:"stage,omitempty"`
	Error   string `json:"error,omitempty"`
	// Result carries whatever the finished job produced (a library item, say), so the UI can
	// update without a second round trip.
	Result    any       `json:"result,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
}

// Registry holds the jobs.
type Registry struct {
	mu   sync.Mutex
	jobs map[string]*Job
	seq  int64
	// retain is how long a finished job stays visible. Long enough for a UI that polls every
	// second to see the ending, short enough that the list does not become a log.
	retain time.Duration
}

func NewRegistry() *Registry {
	return &Registry{jobs: map[string]*Job{}, retain: 2 * time.Minute}
}

// Start registers a job and runs fn in the background.
//
// The context is deliberately NOT the HTTP request's: the whole point is that the work outlives
// the request that asked for it. A caller that passed r.Context() here would cancel the download
// the moment the browser got its job id.
//
// DEDUPLICATED BY KEY, AND THIS IS A CORRECTNESS GUARD RATHER THAN AN OPTIMISATION. A tap on a
// slow-looking button gets tapped again, and reported from real use: two taps queued two
// downloads of the same app, which then raced to write the same file in the same library
// directory. No amount of UI responsiveness removes that hazard — a double-submit can come from
// a double-tap, a retried request, or two phones — so the refusal lives here, where it is the
// last word. An identical key that is already running returns THAT job, so the caller's UI
// simply attaches to the work already in flight.
func (r *Registry) Start(kind Kind, key, label, target string, fn func(ctx context.Context, j *Handle) (any, error)) *Job {
	r.mu.Lock()
	if key != "" {
		for _, j := range r.jobs {
			if j.Key == key && j.State == Running {
				cp := *j
				r.mu.Unlock()
				return &cp
			}
		}
	}
	r.seq++
	id := kind0(kind) + "-" + itoa(r.seq)
	job := &Job{
		ID:        id,
		Kind:      kind,
		Key:       key,
		State:     Running,
		Label:     label,
		Target:    target,
		Percent:   -1,
		StartedAt: time.Now().UTC(),
	}
	r.jobs[id] = job
	r.mu.Unlock()

	go func() {
		h := &Handle{reg: r, id: id}
		result, err := fn(context.Background(), h)

		r.mu.Lock()
		j := r.jobs[id]
		if j != nil {
			j.EndedAt = time.Now().UTC()
			if err != nil {
				j.State = Failed
				j.Error = err.Error()
			} else {
				j.State = Done
				j.Percent = 100
				j.Result = result
			}
		}
		r.mu.Unlock()
		r.reap()
	}()

	return r.snapshot(id)
}

// Handle is what a running job uses to report progress.
type Handle struct {
	reg *Registry
	id  string
}

// Progress records how far along the job is. percent < 0 means "not known yet".
func (h *Handle) Progress(percent int, stage, detail string) {
	h.reg.mu.Lock()
	defer h.reg.mu.Unlock()
	j := h.reg.jobs[h.id]
	if j == nil {
		return
	}
	if percent >= 0 {
		j.Percent = percent
	}
	if stage != "" {
		j.Stage = stage
	}
	if detail != "" {
		j.Detail = detail
	}
}

// Get returns one job.
func (r *Registry) Get(id string) (Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// List returns every tracked job, newest first.
func (r *Registry) List() []Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Job, 0, len(r.jobs))
	for _, j := range r.jobs {
		out = append(out, *j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].StartedAt.After(out[k].StartedAt) })
	return out
}

func (r *Registry) snapshot(id string) *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	j := r.jobs[id]
	if j == nil {
		return nil
	}
	cp := *j
	return &cp
}

// reap drops finished jobs once they have been visible long enough to be seen.
func (r *Registry) reap() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, j := range r.jobs {
		if j.State != Running && !j.EndedAt.IsZero() && time.Since(j.EndedAt) > r.retain {
			delete(r.jobs, id)
		}
	}
}

func kind0(k Kind) string {
	if k == Install {
		return "i"
	}
	return "d"
}

// itoa avoids pulling strconv in for one call.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
