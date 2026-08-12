package store

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStoreIconIsFetchedOnceAndCached: the artwork is the picture for every app the device will
// not draw one for, and it never changes for a given listing — so it is worth exactly one request.
func TestStoreIconIsFetchedOnceAndCached(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("the store's artwork"))
	}))
	defer srv.Close()

	s := NewStoreIcons(t.TempDir(), srv.Client())
	for range 3 {
		got, err := s.Get(context.Background(), "com.example.app", srv.URL+"/512.png")
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "the store's artwork" {
			t.Fatalf("got %q", got)
		}
	}
	if hits != 1 {
		t.Errorf("fetched %d times, want 1 — the cache is the point", hits)
	}
}

// TestNoArtworkIsNotAnError: a delisted app has no listing and therefore no artwork. That is the
// ordinary case for the apps springback exists for, and the caller has a fallback for it.
func TestNoArtworkIsNotAnError(t *testing.T) {
	s := NewStoreIcons(t.TempDir(), nil)
	if _, err := s.Get(context.Background(), "com.example.delisted", ""); !errors.Is(err, ErrNoStoreIcon) {
		t.Errorf("got %v, want ErrNoStoreIcon", err)
	}
}

// TestStoreRefusalIsNotCached: a 404 from the CDN must not be written to disk as artwork, or the
// app would show a broken picture forever.
func TestStoreRefusalIsNotCached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s := NewStoreIcons(t.TempDir(), srv.Client())
	if _, err := s.Get(context.Background(), "com.example.app", srv.URL+"/missing.png"); !errors.Is(err, ErrNoStoreIcon) {
		t.Errorf("got %v, want ErrNoStoreIcon", err)
	}
	if _, err := s.Get(context.Background(), "com.example.app", ""); !errors.Is(err, ErrNoStoreIcon) {
		t.Error("a refusal was cached and is now served as an icon")
	}
}
