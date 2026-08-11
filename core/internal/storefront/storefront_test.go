package storefront

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/novkostya/springback/core/internal/tools"
)

// stubTools answers lookups from a table, and can be told to fail a storefront outright.
//
// The mutex is not ceremony: Resolve queries every storefront CONCURRENTLY, so a Tools
// implementation must be safe for concurrent use. The first version of this stub incremented
// calls unguarded and `go test -race` failed on it immediately — which is the contract being
// enforced rather than merely written down.
type stubTools struct {
	tools.Tools
	present map[string]map[string]int64
	fail    map[string]bool
	// http400 names storefronts that answer "unknown storefront" — the real API's reply to a
	// country code it does not have, which is NOT the same as an empty result.
	http400 map[string]bool

	mu    sync.Mutex
	calls int
}

func (s *stubTools) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *stubTools) Lookup(_ context.Context, bundleID, country string) tools.StoreLookup {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()

	res := tools.StoreLookup{Country: country}
	if s.fail[country] {
		res.Err = errors.New("network is unreachable")
		return res
	}
	if s.http400[country] {
		res.Err = errors.New("itunes lookup " + country + ": http 400")
		return res
	}
	res.Checked = true
	if id, ok := s.present[bundleID][country]; ok {
		res.Found = true
		res.TrackID = id
	}
	return res
}

func resolver(s *stubTools) *Resolver {
	return NewResolver(s, time.Hour, nil)
}

// The measurement SPEC §3 is built on, as a test. Yandex Music is absent from the US store and
// present elsewhere; anything that reports it DELISTED has broken the tool's headline feature.
func TestNotSoldInOneStorefrontIsNotDelisted(t *testing.T) {
	s := &stubTools{present: map[string]map[string]int64{
		"ru.yandex.mobile.music": {"ru": 599981012, "ae": 599981012},
	}}
	got := resolver(s).Resolve(context.Background(), "ru.yandex.mobile.music", []string{"us", "ru", "ae"})
	if got.Status != Available {
		t.Fatalf("Yandex Music: got %q, want %q — a US-only check calls this delisted, which is the false positive the multi-storefront rule exists to prevent", got.Status, Available)
	}
	if got.TrackID != 599981012 {
		t.Errorf("track id: got %d, want 599981012 (an available app's numeric id comes free with the answer)", got.TrackID)
	}
}

func TestGoneEverywhereIsDelisted(t *testing.T) {
	s := &stubTools{present: map[string]map[string]int64{}}
	for _, bundle := range []string{"com.dreamgoods.officecapital", "com.assetsonline.ios"} {
		got := resolver(s).Resolve(context.Background(), bundle, []string{"us", "ru", "ae"})
		if got.Status != Delisted {
			t.Errorf("%s: got %q, want %q", bundle, got.Status, Delisted)
		}
		if len(got.Checked) != 3 {
			t.Errorf("%s: checked %v, want all three storefronts recorded as evidence", bundle, got.Checked)
		}
	}
}

// The guard that keeps a bad region code from inventing delisted apps. `LL/A` naively yields
// country=ll, which the live API answers 400 — if that counted as "not in the store", one
// unrecognised storefront plus a genuine US absence would be enough to accuse an app.
func TestUnknownStorefrontIsNotEvidence(t *testing.T) {
	s := &stubTools{
		present: map[string]map[string]int64{"io.wio.retail": {"ae": 1592748917}},
		http400: map[string]bool{"ll": true},
	}
	got := resolver(s).Resolve(context.Background(), "io.wio.retail", []string{"us", "ll"})
	if got.Status == Delisted {
		t.Fatalf("got %q: a storefront that answered HTTP 400 was counted as 'not in the store'", got.Status)
	}
	if got.Status != Unknown {
		t.Fatalf("got %q, want %q — only one storefront (us) actually answered", got.Status, Unknown)
	}
}

// A network failure must degrade to Unknown, never to Delisted. Getting this backwards sends
// the user off to archive apps that were never at risk.
func TestNetworkFailureDegradesToUnknownNotDelisted(t *testing.T) {
	s := &stubTools{
		present: map[string]map[string]int64{},
		fail:    map[string]bool{"us": true, "ru": true, "ae": true},
	}
	got := resolver(s).Resolve(context.Background(), "com.example.thing", []string{"us", "ru", "ae"})
	if got.Status != Unknown {
		t.Fatalf("got %q, want %q", got.Status, Unknown)
	}
	if len(got.Errors) != 3 {
		t.Errorf("errors: got %d, want 3 recorded so the UI can say why", len(got.Errors))
	}
}

func TestOneStorefrontAloneIsNeverEnoughToAccuse(t *testing.T) {
	s := &stubTools{
		present: map[string]map[string]int64{},
		fail:    map[string]bool{"ru": true, "ae": true},
	}
	got := resolver(s).Resolve(context.Background(), "ru.yandex.mobile.music", []string{"us", "ru", "ae"})
	if got.Status != Unknown {
		t.Fatalf("got %q, want %q — 'absent from the US store' alone is exactly the false positive SPEC §3 measured", got.Status, Unknown)
	}
}

// Unknown must not be cached: it is a statement about the network, not about the app, and
// pinning it for the whole TTL would stop the next (possibly successful) refresh from running.
func TestUnknownIsNotCached(t *testing.T) {
	s := &stubTools{present: map[string]map[string]int64{}, fail: map[string]bool{"us": true, "ru": true}}
	r := resolver(s)
	r.Resolve(context.Background(), "com.example.thing", []string{"us", "ru"})
	first := s.callCount()
	r.Resolve(context.Background(), "com.example.thing", []string{"us", "ru"})
	if s.callCount() == first {
		t.Fatal("second Resolve served a cached Unknown; a transient failure would then be pinned for the whole TTL")
	}
}

func TestSettledVerdictsAreCached(t *testing.T) {
	s := &stubTools{present: map[string]map[string]int64{}}
	r := resolver(s)
	r.Resolve(context.Background(), "com.dreamgoods.officecapital", []string{"us", "ru"})
	after := s.callCount()
	r.Resolve(context.Background(), "com.dreamgoods.officecapital", []string{"us", "ru"})
	if s.callCount() != after {
		t.Fatalf("cache miss: %d calls then %d — 162 apps x 3 storefronts per refresh is why this is cached", after, s.callCount())
	}
	r.Forget("com.dreamgoods.officecapital")
	r.Resolve(context.Background(), "com.dreamgoods.officecapital", []string{"us", "ru"})
	if s.callCount() == after {
		t.Fatal("Forget did not drop the entry, so a user who thinks the answer is stale has no way to say so")
	}
}

func TestRegionToStorefront(t *testing.T) {
	cases := []struct{ region, want string }{
		{"LL/A", "us"},    // the staging iPad. Naively "ll", which the API answers 400.
		{"AE/A", "ae"},    // the staging iPhone.
		{"CH/A", "cn"},    // Apple's China code. ISO would read it as Switzerland.
		{"ZA/A", "sg"},    // Apple's Singapore code. ISO would read it as South Africa.
		{"ZP/A", "hk"},    //
		{"X/A", "au"},     // single-letter codes are real.
		{"gb/a", "gb"},    // already a country code, lower case.
		{"", ""},          //
		{"ZZZZ/A", ""},    // too long to be a country code, not in the table: dropped.
		{"  AE/A ", "ae"}, // whitespace from ideviceinfo.
	}
	for _, c := range cases {
		if got := RegionToStorefront(c.region); got != c.want {
			t.Errorf("RegionToStorefront(%q) = %q, want %q", c.region, got, c.want)
		}
	}
}

func TestStorefrontsAlwaysIncludeTheSpecFloor(t *testing.T) {
	// SPEC §3: "Query at least: the device's own region, plus us and ru."
	got := Storefronts("AE/A")
	want := map[string]bool{"us": true, "ru": true, "ae": true}
	if len(got) != 3 {
		t.Fatalf("got %v, want three storefronts", got)
	}
	for _, cc := range got {
		if !want[cc] {
			t.Errorf("unexpected storefront %q in %v", cc, got)
		}
	}
	// A device already in one of the floor regions must not produce a duplicate query.
	if got := Storefronts("RU/A"); len(got) != 2 {
		t.Errorf("Storefronts(RU/A) = %v, want no duplicate of ru", got)
	}
	// An unmappable code must not add a storefront that will only ever answer 400.
	if got := Storefronts("ZZZZ/A"); len(got) != 2 {
		t.Errorf("Storefronts(ZZZZ/A) = %v, want just the floor", got)
	}
}
