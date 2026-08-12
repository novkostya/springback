package tools

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// rewriteBegunFor mirrors the predicate the signing watcher uses, so the awkward cases can be
// exercised against real files without running a 400 MB download.
func rewriteBegunFor(before os.FileInfo, beforeErr error, dst os.FileInfo) bool {
	if beforeErr != nil {
		return true
	}
	return dst.Size() != before.Size() || !dst.ModTime().Equal(before.ModTime())
}

// TestSigningWatchIgnoresAStaleDestination is the re-download case. The .ipa from a previous
// download is already on disk at full size while the new one is still streaming into .tmp —
// dividing one by the other would report "signing, 100%" for the whole transfer.
func TestSigningWatchIgnoresAStaleDestination(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "app.ipa")
	tmp := out + ".tmp"

	// Yesterday's archive: full size, old mtime.
	if err := os.WriteFile(out, make([]byte, 4000), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(out, old, old); err != nil {
		t.Fatal(err)
	}

	before, beforeErr := os.Stat(out)

	// The new download, a fifth of the way in.
	if err := os.WriteFile(tmp, make([]byte, 800), 0o644); err != nil {
		t.Fatal(err)
	}
	dst, _ := os.Stat(out)
	if rewriteBegunFor(before, beforeErr, dst) {
		t.Error("reported the rewrite as begun while the download was still running")
	}

	// applyPatches truncates the destination and writes the new archive into it.
	if err := os.WriteFile(out, make([]byte, 400), 0o644); err != nil {
		t.Fatal(err)
	}
	dst, _ = os.Stat(out)
	if !rewriteBegunFor(before, beforeErr, dst) {
		t.Fatal("the rewrite was not detected once the destination changed")
	}
	ts, _ := os.Stat(tmp)
	if pct := int(dst.Size() * 100 / ts.Size()); pct != 50 {
		t.Errorf("signing progress = %d%%, want 50%% (400 of 800)", pct)
	}
}

// TestSigningWatchOnAFirstDownload: nothing is there to begin with, so the destination simply
// appearing is the rewrite starting.
func TestSigningWatchOnAFirstDownload(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "app.ipa")

	before, beforeErr := os.Stat(out) // does not exist
	if beforeErr == nil {
		t.Fatal("the destination unexpectedly exists")
	}
	if err := os.WriteFile(out, make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	dst, _ := os.Stat(out)
	if !rewriteBegunFor(before, beforeErr, dst) {
		t.Error("a destination appearing was not treated as the rewrite starting")
	}
}

// TestSigningWatchSurvivesCoarseMtime pins why this compares against a remembered stat rather
// than against time.Now(). Filesystem mtime granularity is coarser than a Go timestamp, so
// "modified after this instant" is false for a file written in the same second — which a fast
// download would hit, and which is how the first version of this got caught.
func TestSigningWatchSurvivesCoarseMtime(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "app.ipa")
	if err := os.WriteFile(out, make([]byte, 4000), 0o644); err != nil {
		t.Fatal(err)
	}
	// Round the mtime down to the second, as a filesystem with 1s granularity would.
	coarse := time.Now().Truncate(time.Second)
	if err := os.Chtimes(out, coarse, coarse); err != nil {
		t.Fatal(err)
	}
	before, beforeErr := os.Stat(out)

	started := time.Now() // strictly after the recorded mtime
	if err := os.WriteFile(out, make([]byte, 400), 0o644); err != nil {
		t.Fatal(err)
	}
	dst, _ := os.Stat(out)

	if !rewriteBegunFor(before, beforeErr, dst) {
		t.Error("the size change was missed")
	}
	// And the reason the wall-clock form was wrong: this can legitimately be false.
	if !dst.ModTime().After(started) {
		t.Logf("as expected on this filesystem, mtime %v is not After %v — "+
			"a wall-clock comparison would have missed the rewrite", dst.ModTime(), started)
	}
}
