package jobs

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestConcurrentJobsAreCapped is the demo's version of "unbounded job spawning", and the finding
// that matters is where it ISN'T: single-flighting by key — the mechanism everybody points at —
// only stops the SAME app being fetched twice. Forty POSTs naming forty different app ids were all
// accepted, each with its own goroutine and its own ipatool. Measured before this cap existed.
func TestConcurrentJobsAreCapped(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	defer close(release)

	var started, rejected int
	for i := range MaxRunning + 12 {
		// A distinct key per job: this is the path single-flight does not cover.
		j := r.Start(Download, "download:"+itoa(int64(i)), "app", "", func(context.Context, *Handle) (any, error) {
			<-release
			return nil, nil
		})
		if j.State == Failed {
			rejected++
			if j.Error == "" {
				t.Error("a refused job carries no explanation")
			}
		} else {
			started++
		}
	}

	if started != MaxRunning {
		t.Errorf("started %d jobs, want the cap of %d", started, MaxRunning)
	}
	if rejected != 12 {
		t.Errorf("refused %d, want the 12 over the cap", rejected)
	}
	// A refusal is not work and must not be recorded as any.
	if n := len(r.List()); n != MaxRunning {
		t.Errorf("the registry holds %d jobs, want %d — refusals were recorded", n, MaxRunning)
	}
}

// TestTheCapHoldsUnderABurst covers the reason the check lives inside Start's lock. A caller that
// counts first and starts second lets a whole burst pass the count before any of them starts —
// which holds for two people clicking at once and fails for the case the cap exists to stop.
func TestTheCapHoldsUnderABurst(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	defer close(release)

	var wg sync.WaitGroup
	var mu sync.Mutex
	started := 0
	for i := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j := r.Start(Download, "download:"+itoa(int64(i)), "app", "", func(context.Context, *Handle) (any, error) {
				<-release
				return nil, nil
			})
			if j.State != Failed {
				mu.Lock()
				started++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if started > MaxRunning {
		t.Errorf("%d jobs got through a cap of %d", started, MaxRunning)
	}
}

// TestTheCapLetsGoWhenWorkFinishes: a cap that never released would turn one busy evening into a
// springback that refuses downloads until it is restarted.
func TestTheCapLetsGoWhenWorkFinishes(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})

	for i := range MaxRunning {
		r.Start(Download, "download:"+itoa(int64(i)), "app", "", func(context.Context, *Handle) (any, error) {
			<-release
			return nil, nil
		})
	}
	if j := r.Start(Download, "download:over", "app", "", nil); j.State != Failed {
		t.Fatal("the cap did not apply at all")
	}

	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for r.Running() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	done := make(chan struct{})
	j := r.Start(Download, "download:after", "app", "", func(context.Context, *Handle) (any, error) {
		close(done)
		return nil, nil
	})
	if j.State == Failed {
		t.Fatal("still refusing after every job finished")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the job that was accepted never ran")
	}
}
