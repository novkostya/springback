package demo

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/novkostya/springback/core/internal/auth"
	"github.com/novkostya/springback/core/internal/store"
	"github.com/novkostya/springback/core/internal/tools"
)

func seeded(t *testing.T) (string, *auth.Service, *store.Accounts, *store.Library) {
	t.Helper()
	dir := t.TempDir()
	a, err := auth.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	accounts := store.NewAccounts(dir)
	library := store.NewLibrary(dir + "/library")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Seed(context.Background(), log, tools.NewFake(), a, accounts, library); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	return dir, a, accounts, library
}

// TestSeedLetsAStrangerIn is the point of the mode. springback's first run asks whoever arrives to
// CHOOSE the password, which on a public instance means the first visitor sets it and everyone
// after them meets a door they cannot open.
func TestSeedLetsAStrangerIn(t *testing.T) {
	_, a, _, _ := seeded(t)

	if !a.IsSetUp() {
		t.Fatal("the demo came up with no password: every visitor would meet first-run setup")
	}
	if _, err := a.Login(Password, "1.2.3.4"); err != nil {
		t.Errorf("the published password does not work: %v", err)
	}
	if _, err := a.Login("not the demo password", "5.6.7.8"); err == nil {
		t.Error("any password at all was accepted")
	}
}

// TestSeedUsesACheapKDF guards a measured out-of-memory kill rather than a preference: at the
// production parameters a login costs 64 MiB, and five at once took the demo image to 403 MB on a
// machine specced for 256.
func TestSeedUsesACheapKDF(t *testing.T) {
	dir, _, _, _ := seeded(t)

	// Read the file rather than reaching for an accessor: the parameters travelling INSIDE the
	// stored PHC string is the mechanism, and this asserts the mechanism rather than a field.
	b, err := os.ReadFile(filepath.Join(dir, "password"))
	if err != nil {
		t.Fatal(err)
	}
	if hash := string(b); !strings.Contains(hash, "m=8192,") {
		t.Errorf("stored hash = %q, want argon2id at 8 MiB", hash)
	}
}

// TestSeedStocksTheScreens: a demo whose Library and Accounts screens are empty demonstrates
// nothing, and the empty state is the one screen a visitor cannot act on.
func TestSeedStocksTheScreens(t *testing.T) {
	_, _, accounts, library := seeded(t)

	accs, err := accounts.List()
	if err != nil || len(accs) != 1 {
		t.Fatalf("accounts = %v (%v), want exactly the demo Apple ID", accs, err)
	}
	if accs[0].Email != Account {
		t.Errorf("account = %q, want %q", accs[0].Email, Account)
	}

	items, err := library.List()
	if err != nil || len(items) != 1 {
		t.Fatalf("library = %v (%v), want one archived app", items, err)
	}
	// THE BUNDLE ID IS THE ASSERTION. It has to match what the fixture iPhone has installed, or
	// the device screen cannot mark the app as already archived — and that row, delisted and in
	// the library at once, is the entire argument the demo exists to make.
	if items[0].BundleID != "com.burbn.boomerang" {
		t.Errorf("archived bundle id = %q, want the delisted fixture com.burbn.boomerang", items[0].BundleID)
	}
	if items[0].Name == "" || strings.HasPrefix(items[0].Name, "App6") {
		t.Errorf("archived name = %q, want the app's real name", items[0].Name)
	}
}

// TestSeedIsIdempotent: a restart must not throw an error or a second copy of anything, because
// the machine is stopped and started by the platform rather than by anyone watching.
func TestSeedIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	a, err := auth.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	accounts := store.NewAccounts(dir)
	library := store.NewLibrary(dir + "/library")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	for i := range 2 {
		if err := Seed(context.Background(), log, tools.NewFake(), a, accounts, library); err != nil {
			t.Fatalf("Seed run %d: %v", i+1, err)
		}
	}
	accs, _ := accounts.List()
	if len(accs) != 1 {
		t.Errorf("accounts after two seeds = %d, want 1", len(accs))
	}
	items, _ := library.List()
	if len(items) != 1 {
		t.Errorf("library after two seeds = %d, want 1", len(items))
	}
}

// TestResetThrowsAwayTheLastVisitor is the guarantee the mode is named for. Seeding is idempotent
// — it leaves what it finds — so without this a container that is restarted rather than recreated,
// or a fly machine that suspends rather than stops, comes up looking perfectly fine while serving
// the previous visitor's extra Apple IDs and deleted library items.
func TestResetThrowsAwayTheLastVisitor(t *testing.T) {
	root := t.TempDir()
	junk := filepath.Join(root, "accounts", "somebody-elses")
	if err := os.MkdirAll(junk, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := reset(root); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("the previous run's state survived: %v", err)
	}
}

// TestResetRefusesTheFilesystem covers the single edit that turns a reset into a catastrophe: the
// root arriving empty or "/" from somewhere else. RemoveAll would carry either out in silence.
func TestResetRefusesTheFilesystem(t *testing.T) {
	for _, root := range []string{"", "/", "//", "/."} {
		if err := reset(root); err == nil {
			t.Errorf("reset(%q) was allowed", root)
		}
	}
}

// TestDemoStateIsNotTheRealState is the safety property behind the wipe: a demo must never run on
// the directories a real install keeps its archive in, because it deletes them at startup.
// Measured before the paths were swapped: `--public-demo` with a real library mounted wrote a fake
// Apple ID into the accounts store and a fake .ipa into the archive.
func TestDemoStateIsNotTheRealState(t *testing.T) {
	for _, dir := range []string{LibraryDir(), AccountsDir()} {
		if dir == "/library" || dir == "/accounts" {
			t.Errorf("demo state at %q, which is where a real install keeps its data", dir)
		}
		if !strings.HasPrefix(dir, StateRoot+"/") {
			t.Errorf("demo state at %q, outside the throwaway root %q", dir, StateRoot)
		}
	}
}

// TestSeedRefusesTheRealTools is the guard that survives someone editing the deploy file. Seeding
// signs an Apple ID in and downloads an app; against the real layer that is a made-up password
// sent to Apple, on an instance that publishes its own password to the world.
func TestSeedRefusesTheRealTools(t *testing.T) {
	dir := t.TempDir()
	a, err := auth.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	err = Seed(context.Background(), log, tools.NewReal("", dir), a,
		store.NewAccounts(dir), store.NewLibrary(dir+"/library"))
	if err == nil {
		t.Fatal("seeded a demo against the REAL tool layer")
	}
	if a.IsSetUp() {
		t.Error("refused, but set the published password anyway")
	}
}
