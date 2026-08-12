package store

// The store artwork cache.
//
// WHY A THIRD ICON SOURCE EXISTS. A device answers for an app it has not rendered by sending a
// generic tile rather than by refusing, so the rows for those apps all drew the same grey square
// and named nothing. The App Store has the real picture for every app that still has a listing,
// and springback already asks the store about every installed app to decide whether it is
// delisted — so the artwork URL arrives with an answer it was fetching anyway.
//
// The precedence, decided in the HTTP handler:
//
//	the device's own icon        the app as it looks on the phone, and usually the only one needed
//	the store's artwork          when the device sent its generic tile
//	the device's generic tile    when there is no listing either — every delisted app
//
// Which means a delisted app still shows what iOS shows, and everything else shows its real icon.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ErrNoStoreIcon means the store has no artwork for this app — the ordinary case for a delisted
// one, which has no listing to carry any.
var ErrNoStoreIcon = errors.New("no store artwork for this app")

// StoreIcons caches artwork under <root>/.store-icons/<bundle>.png.
type StoreIcons struct {
	Root string
	HTTP *http.Client
}

func NewStoreIcons(root string, c *http.Client) *StoreIcons {
	if c == nil {
		c = &http.Client{Timeout: 20 * time.Second}
	}
	return &StoreIcons{Root: filepath.Join(root, ".store-icons"), HTTP: c}
}

func (s *StoreIcons) path(bundleID string) string {
	return filepath.Join(s.Root, safeName(bundleID)+".png")
}

// Get returns the artwork, fetching it once and caching it.
//
// url is what the storefront lookup reported. An empty one is not an error worth logging: it is
// what a delisted app has, and the caller has a fallback for exactly that.
func (s *StoreIcons) Get(ctx context.Context, bundleID, url string) ([]byte, error) {
	if bundleID == "" {
		return nil, ErrNoStoreIcon
	}
	if b, err := os.ReadFile(s.path(bundleID)); err == nil && len(b) > 0 {
		return b, nil
	}
	if url == "" {
		return nil, ErrNoStoreIcon
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: http %d", ErrNoStoreIcon, resp.StatusCode)
	}

	// 4 MB is far more than a 512px icon needs and stops a redirected URL from becoming a
	// memory problem.
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || len(b) == 0 {
		return nil, ErrNoStoreIcon
	}
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return nil, err
	}
	// A failed write is not a failed fetch — the bytes are good and the caller wants them.
	_ = writeFileAtomic(s.path(bundleID), b, 0o644)
	return b, nil
}
