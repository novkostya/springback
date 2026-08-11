// Package storefront decides whether an app is still buyable anywhere.
//
// This is the only part of springback that is a product rather than plumbing, and it is the part
// with a wrong answer that looks exactly like a right one. SPEC §3 states the rule:
//
//	An app counts as delisted only when EVERY queried storefront returns resultCount: 0.
//
// and the measurement behind it:
//
//	ru.yandex.mobile.music        us=0  ru=1  ae=1   <- NOT delisted, just not sold in the US
//	com.dreamgoods.officecapital  us=0  ru=0  ae=0   <- genuinely gone
//
// Everything here exists to keep the first row from being reported as the second.
package storefront

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/novkostya/springback/core/internal/tools"
)

// Status is what the Devices screen shows against each installed app.
type Status string

const (
	// Available — at least one storefront still sells it.
	Available Status = "available"
	// Delisted — every storefront that ANSWERED said no, and enough of them answered.
	//
	// WHAT THIS ACTUALLY MEANS, precisely: no App Store listing was found for that bundle id
	// in any queried storefront, for an app the device holds a purchase receipt for.
	//
	// The receipt is what makes that a strong claim rather than a guess. An app with no
	// receipt is not judged at all, and one whose receipt marks it B2B or factory-installed
	// is NotListed — so the two ways an app can be "in no store" without ever having been
	// pulled are both handled before this verdict is reached.
	//
	// The remaining gap, stated because it is real: a TestFlight build carries a receipt like
	// any other, so a beta of an app that has no public listing under the same bundle id
	// still lands here. The device does not distinguish it — measured 2026-08-11, every
	// installed app reports the same SignerIdentity and ApplicationType whatever its origin.
	Delisted Status = "delisted"
	// Unknown — not enough storefronts answered to say either way. This is a real answer,
	// not a placeholder: a network blip must degrade to "unknown", never to "delisted",
	// because the second one invites the user to spend half an hour archiving an app that
	// was never at risk.
	Unknown Status = "unknown"
	// NotListed — never a public App Store listing, so "not in any store" is the expected
	// answer rather than a finding. A B2B custom app or a factory install, per the device's
	// own receipt. Distinguished from Delisted because the user can do nothing about it and
	// there is nothing wrong: reporting it as at-risk is noise in the one list that must be
	// all signal.
	NotListed Status = "not_listed"
)

// minCheckedForDelisted is how many storefronts must actually answer before an all-zero result
// is allowed to mean "gone".
//
// TWO, not one. One storefront answering zero is precisely the false positive SPEC §3 measured
// — it is what "not sold in the US" looks like. It is also not three: requiring every queried
// storefront to answer would let one flaky region turn the whole screen to "unknown", and the
// spec's own worked example (us + ru + the device region) has all three agreeing anyway.
const minCheckedForDelisted = 2

// Result is one app's store status plus the evidence for it.
//
// Checked is carried into the API response on purpose. "Delisted" is a claim about the world
// that a user is about to spend time and disk on, and being able to see WHICH storefronts were
// asked is the difference between a verdict and an assertion.
type Result struct {
	Status Status `json:"status"`
	// TrackID is the numeric App Store id, when a storefront gave one up. It is 0 for a
	// delisted app by definition — no storefront has it. That used to mean the id had to be
	// typed in by hand (SPEC §4); it no longer does, because the device's own purchase
	// receipt carries the id whether or not the listing still exists. See tools/applist.go.
	TrackID int64 `json:"track_id,omitempty"`
	// Version is what the store sells RIGHT NOW, which is how a stale library copy is spotted.
	Version string   `json:"version,omitempty"`
	Checked []string `json:"checked"`
	Errors  []string `json:"errors,omitempty"`
}

// appleRegion maps an Apple part-number region code onto an iTunes storefront.
//
// THESE ARE NOT ISO COUNTRY CODES, and treating them as such is a live bug rather than a
// theoretical one. `ideviceinfo -k RegionInfo` on the staging iPad returns "LL/A", whose first
// two letters are Apple's code for the United States; passed through as a country code it
// becomes `country=ll`, which the lookup API answers with HTTP 400 (measured 2026-08-11). Worse
// is "CH", which Apple uses for China and ISO uses for Switzerland — a plausible-looking code
// that silently queries the wrong country and answers the user's question about the wrong store.
//
// So: this table first, an ISO-shaped guess second, and a non-200 from the API means NOT CHECKED
// either way. Three layers, because the failure this prevents is invisible in the output.
var appleRegion = map[string]string{
	"LL": "us", // United States
	"AE": "ae", // United Arab Emirates
	"RU": "ru", // Russia
	"B":  "gb", // United Kingdom / Ireland
	"BZ": "br", // Brazil
	"C":  "ca", // Canada
	"CH": "cn", // China mainland — NOT Switzerland
	"CN": "cn",
	"D":  "de", // Germany
	"DN": "de",
	"F":  "fr", // France
	"FD": "fr",
	"GR": "gr", // Greece
	"HN": "in", // India
	"IN": "in",
	"IP": "it", // Italy
	"T":  "it",
	"J":  "jp", // Japan
	"JP": "jp",
	"KH": "kr", // South Korea
	"KS": "fi", // Finland
	"MG": "hu", // Hungary
	"MY": "my", // Malaysia
	"NF": "fr",
	"PP": "ph", // Philippines
	"PL": "pl", // Poland
	"QN": "dk", // Denmark / Nordics
	"RS": "ru",
	"SE": "rs", // Serbia
	"SO": "za", // South Africa
	"TA": "tw", // Taiwan
	"TH": "th", // Thailand
	"TU": "tr", // Turkey
	"TY": "it",
	"VN": "vn", // Vietnam
	"X":  "au", // Australia / New Zealand
	"Y":  "es", // Spain
	"ZA": "sg", // Singapore — NOT South Africa
	"ZD": "nl", // Netherlands
	"ZP": "hk", // Hong Kong
}

// Storefronts returns the list to query for a device in the given region.
//
// us and ru are ALWAYS included, per SPEC §3's floor, and the device's own region is added when
// it can be mapped. An unmappable code is dropped rather than guessed at: the query would come
// back 400 and contribute nothing but a scary-looking error next to every app.
func Storefronts(deviceRegion string) []string {
	return withStorefront([]string{"us", "ru"}, RegionToStorefront(deviceRegion))
}

// ForApp returns the storefronts to query for one app, given the storefront the device's own
// purchase receipt says it came from.
//
// THE RECEIPT BEATS THE REGION, and the difference is measured rather than theoretical: the
// staging iPhone reports RegionInfo "AE/A", while every app on it was bought from `ru`. Region
// is where the hardware was sold; the storefront is where the app was. Asking the wrong one is
// how an app that is alive and well in its own store gets reported as gone.
//
// The us/ru floor is kept underneath it, so this only ever ADDS evidence.
func ForApp(deviceRegion, appStorefront string) []string {
	fronts := Storefronts(deviceRegion)
	return withStorefront(fronts, strings.ToLower(strings.TrimSpace(appStorefront)))
}

func withStorefront(fronts []string, cc string) []string {
	if cc == "" {
		return fronts
	}
	for _, f := range fronts {
		if f == cc {
			return fronts
		}
	}
	return append(fronts, cc)
}

// RegionToStorefront maps a raw RegionInfo value ("AE/A") to a storefront ("ae"). It returns ""
// when the code is not recognised and does not look like a plain country code.
func RegionToStorefront(region string) string {
	code := strings.ToUpper(strings.TrimSpace(region))
	if i := strings.IndexByte(code, '/'); i >= 0 {
		code = code[:i]
	}
	if code == "" {
		return ""
	}
	if cc, ok := appleRegion[code]; ok {
		return cc
	}
	// Fallback for codes not in the table. Only two-letter codes are tried, and only after
	// the table has had its say, so "CH" can never reach here and be read as Switzerland.
	// If the guess is wrong the API answers 400 and the storefront is recorded as unchecked
	// — the same safe direction as everything else in this file.
	if len(code) == 2 {
		return strings.ToLower(code)
	}
	return ""
}

// Resolver answers store-status questions, with a cache.
//
// Cached because a single iPhone has 162 apps and three storefronts each is 486 requests to
// Apple for one screen refresh. SPEC §3: "Cache results; they change rarely."
type Resolver struct {
	tools tools.Tools
	ttl   time.Duration

	mu    sync.Mutex
	cache map[string]CacheEntry

	// sem bounds concurrent lookups. Fanning 486 requests at Apple as fast as the box can
	// open sockets is how a personal tool earns a rate limit for everybody running it.
	sem chan struct{}

	// store persists the cache across restarts. Without it every restart re-asks Apple for
	// the same 486 answers, which is both slow and rude for data the spec says changes
	// rarely. Optional: a Resolver with no store just keeps the cache in memory.
	store CacheStore
}

// CacheStore persists resolved statuses across restarts.
type CacheStore interface {
	Load() (map[string]CacheEntry, error)
	Save(map[string]CacheEntry) error
}

// CacheEntry is the on-disk shape of one cached verdict.
type CacheEntry struct {
	Result Result    `json:"result"`
	At     time.Time `json:"at"`
}

// NewResolver builds a resolver. store may be nil.
func NewResolver(t tools.Tools, ttl time.Duration, store CacheStore) *Resolver {
	r := &Resolver{
		tools: t,
		ttl:   ttl,
		cache: map[string]CacheEntry{},
		sem:   make(chan struct{}, 6),
		store: store,
	}
	if store != nil {
		if loaded, err := store.Load(); err == nil {
			for k, v := range loaded {
				r.cache[k] = v
			}
		}
	}
	return r
}

func key(bundleID string, fronts []string) string {
	return bundleID + "|" + strings.Join(fronts, ",")
}

// Resolve answers for one bundle id across one set of storefronts.
func (r *Resolver) Resolve(ctx context.Context, bundleID string, fronts []string) Result {
	k := key(bundleID, fronts)

	r.mu.Lock()
	if e, ok := r.cache[k]; ok && time.Since(e.At) < r.ttl {
		r.mu.Unlock()
		return e.Result
	}
	r.mu.Unlock()

	res := r.query(ctx, bundleID, fronts)

	// A result nobody could verify is not worth remembering — caching Unknown would pin a
	// transient network failure in place for the whole TTL, and the next refresh (which might
	// well succeed) would never run.
	if res.Status != Unknown {
		r.mu.Lock()
		r.cache[k] = CacheEntry{Result: res, At: time.Now()}
		r.mu.Unlock()
		r.persist()
	}
	return res
}

func (r *Resolver) query(ctx context.Context, bundleID string, fronts []string) Result {
	results := make([]tools.StoreLookup, len(fronts))

	var wg sync.WaitGroup
	for i, cc := range fronts {
		wg.Add(1)
		go func(i int, cc string) {
			defer wg.Done()
			select {
			case r.sem <- struct{}{}:
				defer func() { <-r.sem }()
			case <-ctx.Done():
				results[i] = tools.StoreLookup{Country: cc, Err: ctx.Err()}
				return
			}
			results[i] = r.tools.Lookup(ctx, bundleID, cc)
		}(i, cc)
	}
	wg.Wait()

	res := Result{Status: Unknown}
	checked := 0
	for _, l := range results {
		if l.Err != nil {
			res.Errors = append(res.Errors, l.Err.Error())
			continue
		}
		if !l.Checked {
			continue
		}
		checked++
		res.Checked = append(res.Checked, l.Country)
		if l.Found {
			// ONE storefront selling it is enough to settle the question. The app is
			// not at risk; the user's copy is still buyable somewhere, and the numeric
			// id comes free with the answer.
			res.Status = Available
			if res.TrackID == 0 {
				res.TrackID = l.TrackID
				res.Version = l.Version
			}
		}
	}
	sort.Strings(res.Checked)

	if res.Status == Available {
		return res
	}
	if checked >= minCheckedForDelisted {
		res.Status = Delisted
		return res
	}
	// Fewer than two storefronts answered. Not enough to accuse.
	res.Status = Unknown
	return res
}

// Forget drops a bundle id from the cache, so the next Resolve re-asks Apple. Used by the
// refresh action: a user who thinks the answer is stale needs a way to say so.
func (r *Resolver) Forget(bundleID string) {
	r.mu.Lock()
	for k := range r.cache {
		if strings.HasPrefix(k, bundleID+"|") {
			delete(r.cache, k)
		}
	}
	r.mu.Unlock()
	r.persist()
}

func (r *Resolver) persist() {
	if r.store == nil {
		return
	}
	r.mu.Lock()
	snapshot := make(map[string]CacheEntry, len(r.cache))
	for k, v := range r.cache {
		snapshot[k] = v
	}
	r.mu.Unlock()
	_ = r.store.Save(snapshot)
}
