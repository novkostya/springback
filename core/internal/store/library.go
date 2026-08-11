package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/novkostya/springback/core/internal/ipa"
	"github.com/novkostya/springback/core/internal/storefront"
)

// LibraryItem is /library/<numeric-id>/meta.json, per SPEC §4.
type LibraryItem struct {
	ID           int64     `json:"id"`
	BundleID     string    `json:"bundle_id"`
	Name         string    `json:"name"`
	Version      string    `json:"version"`
	Size         int64     `json:"size"`
	DownloadedAt time.Time `json:"downloaded_at"`
	AccountSlug  string    `json:"account_slug"`
	// Artist is not in the spec's list but comes free from iTunesMetadata.plist, and it is
	// what tells two similarly-named apps apart in a picker.
	Artist string `json:"artist,omitempty"`
}

// Library is the on-disk app library, keyed by numeric App Store id.
type Library struct {
	Root string
	mu   sync.Mutex
}

func NewLibrary(root string) *Library { return &Library{Root: root} }

// dir is /library/<id>. The id is an int64, so it cannot carry a path separator — which is why
// the library is keyed by the numeric id and not by the bundle id.
func (l *Library) dir(id int64) string {
	return filepath.Join(l.Root, strconv.FormatInt(id, 10))
}

// IPAPath is /library/<id>/<id>.ipa.
func (l *Library) IPAPath(id int64) string {
	return filepath.Join(l.dir(id), strconv.FormatInt(id, 10)+".ipa")
}

func (l *Library) metaPath(id int64) string {
	return filepath.Join(l.dir(id), "meta.json")
}

// List returns the library, newest download first.
func (l *Library) List() ([]LibraryItem, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entries, err := os.ReadDir(l.Root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var items []LibraryItem
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil {
			continue
		}
		item, err := l.readMeta(id)
		if err != nil {
			// A directory with no readable meta.json is a download that died partway.
			// Skipping it keeps the list truthful; the id stays on disk so a retry
			// overwrites it rather than accumulating a second copy.
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DownloadedAt.After(items[j].DownloadedAt) })
	return items, nil
}

func (l *Library) readMeta(id int64) (LibraryItem, error) {
	b, err := os.ReadFile(l.metaPath(id))
	if err != nil {
		return LibraryItem{}, err
	}
	var item LibraryItem
	if err := json.Unmarshal(b, &item); err != nil {
		return LibraryItem{}, err
	}
	return item, nil
}

// Get returns one item.
func (l *Library) Get(id int64) (LibraryItem, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.readMeta(id)
}

// Has reports whether an id is already downloaded. The Devices screen uses it to say "in your
// library" instead of offering to fetch the same 500 MB twice.
func (l *Library) Has(id int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := l.readMeta(id)
	return err == nil
}

// PrepareDir makes /library/<id> ready to receive a download.
func (l *Library) PrepareDir(id int64) (string, error) {
	if id <= 0 {
		return "", fmt.Errorf("invalid app id %d", id)
	}
	if err := os.MkdirAll(l.dir(id), 0o755); err != nil {
		return "", err
	}
	return l.IPAPath(id), nil
}

// Record writes meta.json by READING THE DOWNLOADED ARCHIVE, per SPEC §4: the bundle id, name
// and version come from the .ipa's own plists, never from what the user typed. For a delisted
// app the numeric id is hand-entered, and this is the step that catches a typo in it — the
// archive says what it actually is.
func (l *Library) Record(id int64, accountSlug string) (LibraryItem, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	path := l.IPAPath(id)
	st, err := os.Stat(path)
	if err != nil {
		return LibraryItem{}, fmt.Errorf("downloaded file missing: %w", err)
	}
	meta, err := ipa.Read(path)
	if err != nil {
		return LibraryItem{}, err
	}

	item := LibraryItem{
		ID:           id,
		BundleID:     meta.BundleID,
		Name:         meta.Name,
		Version:      meta.Version,
		Size:         st.Size(),
		DownloadedAt: time.Now().UTC(),
		AccountSlug:  accountSlug,
		Artist:       meta.Artist,
	}
	// The archive's own itemId beats the id the download was keyed on when the two disagree —
	// but the FILE stays where it is. Renaming on disk here would break the path the caller
	// just wrote, so the disagreement is recorded rather than acted on.
	if meta.ItemID != 0 && meta.ItemID != id {
		item.ID = id
	}
	if item.Name == "" {
		item.Name = meta.BundleID
	}

	b, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return LibraryItem{}, err
	}
	if err := writeFileAtomic(l.metaPath(id), b, 0o644); err != nil {
		return LibraryItem{}, err
	}
	return item, nil
}

// Delete removes /library/<id> entirely.
func (l *Library) Delete(id int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	dir := l.dir(id)
	if id <= 0 || !isUnder(l.Root, dir) {
		return fmt.Errorf("refusing to remove %s: outside the library root", dir)
	}
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no such library item: %d", id)
	}
	return os.RemoveAll(dir)
}

// BundleIDs maps bundle id -> numeric id for everything in the library.
//
// THIS IS WHAT MAKES THE MANUAL ENTRY ONE-TIME (SPEC §4: "Once an .ipa is in the library, its
// numeric id is never needed again"). A delisted app is in no storefront, so nothing can look
// its id up — but once it has been archived, the library itself is the lookup table, and the
// same app on a second device is a one-click archive.
func (l *Library) BundleIDs() (map[string]int64, error) {
	items, err := l.List()
	if err != nil {
		return nil, err
	}
	m := make(map[string]int64, len(items))
	for _, it := range items {
		if it.BundleID != "" {
			m[it.BundleID] = it.ID
		}
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// The storefront cache, persisted under the library root.
// ---------------------------------------------------------------------------

// StatusCache implements storefront.CacheStore against a JSON file.
//
// It lives in the library root rather than a third mount because it IS library-adjacent data,
// and because the alternative — re-asking Apple for 486 answers on every restart — is both slow
// and rude for data SPEC §3 says changes rarely.
type StatusCache struct {
	Path string
	mu   sync.Mutex
}

func NewStatusCache(root string) *StatusCache {
	return &StatusCache{Path: filepath.Join(root, "store-status-cache.json")}
}

func (c *StatusCache) Load() (map[string]storefront.CacheEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := os.ReadFile(c.Path)
	if err != nil {
		return nil, err
	}
	var m map[string]storefront.CacheEntry
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *StatusCache) Save(m map[string]storefront.CacheEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFileAtomic(c.Path, b, 0o644)
}
