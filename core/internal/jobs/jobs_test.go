package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// THE REPORTED BUG, AS A TEST. Two taps on a slow-looking button queued two downloads of the
// same app, which then raced to write the same file in the same library directory. UI guards
// help, but they cannot be the only defence: a double-submit also arrives from a retried
// request or a second phone. The registry is the last word.
func TestSameKeyIsNotQueuedTwice(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var runs int
	var mu sync.Mutex

	work := func(context.Context, *Handle) (any, error) {
		mu.Lock()
		runs++
		mu.Unlock()
		started <- struct{}{}
		<-release
		return nil, nil
	}

	first := r.Start(Download, "download:123", "App", "", work)
	second := r.Start(Download, "download:123", "App", "", work)

	if first.ID != second.ID {
		t.Fatalf("two job ids for one key: %s and %s — the work was queued twice", first.ID, second.ID)
	}

	// The work runs in a goroutine, so wait for it to actually begin before counting. Reading
	// the counter immediately races the scheduler and proves nothing either way.
	<-started
	select {
	case <-started:
		t.Fatal("the work started twice for one key")
	case <-time.After(150 * time.Millisecond):
	}
	mu.Lock()
	got := runs
	mu.Unlock()
	if got != 1 {
		t.Fatalf("the work ran %d times, want 1", got)
	}
	close(release)
}

// Different apps, and the same app onto different devices, are different work.
func TestDifferentKeysRunConcurrently(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	work := func(context.Context, *Handle) (any, error) { <-release; return nil, nil }

	a := r.Start(Download, "download:1", "A", "", work)
	b := r.Start(Download, "download:2", "B", "", work)
	c := r.Start(Install, "install:1:udid-x", "A", "x", work)
	d := r.Start(Install, "install:1:udid-y", "A", "y", work)

	ids := map[string]bool{a.ID: true, b.ID: true, c.ID: true, d.ID: true}
	if len(ids) != 4 {
		t.Fatalf("distinct work collapsed into %d jobs, want 4", len(ids))
	}
	close(release)
}

// Once a job has finished, the same work may be started again — a retry after a failure, or a
// deliberate re-download. Deduplication is about work IN FLIGHT, not a permanent ban.
func TestFinishedKeyCanRunAgain(t *testing.T) {
	r := NewRegistry()
	done := make(chan struct{})
	first := r.Start(Download, "download:9", "App", "", func(context.Context, *Handle) (any, error) {
		close(done)
		return nil, errors.New("failed")
	})
	<-done
	waitFor(t, func() bool { j, _ := r.Get(first.ID); return j.State == Failed })

	second := r.Start(Download, "download:9", "App", "", func(context.Context, *Handle) (any, error) { return nil, nil })
	if second.ID == first.ID {
		t.Fatal("a finished job blocked a retry of the same work")
	}
}

func TestProgressAndCompletionAreRecorded(t *testing.T) {
	r := NewRegistry()
	gate := make(chan struct{})
	job := r.Start(Download, "download:5", "App", "", func(_ context.Context, h *Handle) (any, error) {
		h.Progress(42, "downloading", "84/197 MB")
		close(gate)
		return "result", nil
	})
	// percent starts at -1, not 0: the tool has not reported yet, and a bar pinned at zero is
	// indistinguishable from a stalled one.
	if job.Percent != -1 {
		t.Errorf("initial percent %d, want -1", job.Percent)
	}
	<-gate
	waitFor(t, func() bool { j, _ := r.Get(job.ID); return j.State == Done })

	j, _ := r.Get(job.ID)
	if j.Percent != 100 || j.Result != "result" || j.Detail != "84/197 MB" {
		t.Errorf("finished job: %+v", j)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the job to settle")
}
