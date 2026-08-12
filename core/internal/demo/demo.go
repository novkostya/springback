// Package demo brings a fresh box up as a public demo: a password anybody can use, and enough
// data on it that no screen is empty.
//
// WHY THIS EXISTS AT ALL. springback's first run asks whoever arrives to CHOOSE the password — the
// right behaviour for a box on someone's shelf, and impossible on a public instance, where the
// first visitor would set it and every visitor after them would meet a login form they cannot
// pass. The password has to be decided before anyone arrives, and then printed on the screen where
// it is needed.
//
// EVERYTHING HERE IS THROWAWAY BY CONSTRUCTION. Seeding runs against the fake tool layer, on a
// filesystem the host resets whenever the machine stops, so there is no state worth protecting and
// no visitor who can damage another's. That is the whole security model, and it is why this
// package refuses to run against the real tools rather than trusting a flag.
package demo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/novkostya/springback/core/internal/auth"
	"github.com/novkostya/springback/core/internal/store"
	"github.com/novkostya/springback/core/internal/tools"
)

// StateRoot is where a demo instance keeps its library and accounts, and it is NOT the configured
// paths.
//
// THE SWAP IS WHAT MAKES THE WIPE SAFE. Reset deletes whatever it is handed, so pointing this at
// the ordinary directories would give a reset that appeared to work perfectly — right up to the
// first person who ran `--public-demo` with their real library mounted to see what the demo looked
// like, and had their archive deleted for it. Measured before the swap existed: that command wrote
// a fake Apple ID into a real accounts store and a fake .ipa into a real library.
//
// Under /tmp because nothing in any documented deployment mounts it: compose.yml and
// compose.netmuxd.yml mount /library, /accounts and /var/lib/lockdown, and this is none of them.
const StateRoot = "/tmp/springback-demo"

// LibraryDir and AccountsDir are the throwaway paths a demo instance runs on.
func LibraryDir() string  { return filepath.Join(StateRoot, "library") }
func AccountsDir() string { return filepath.Join(StateRoot, "accounts") }

// Reset throws away everything a previous run left behind. It runs at STARTUP, and that timing is
// the whole guarantee rather than a tidy-up.
//
// The tempting alternative is to let the platform do it: fly resets the rootfs on a cold start, so
// a machine that stops and starts comes back clean without this. That covers the common case and
// nothing else. A machine that SUSPENDS resumes without restarting the process; a container that is
// restarted rather than recreated keeps its filesystem; and `docker run` on somebody's laptop keeps
// it for as long as they leave the container around. In every one of those the demo comes up
// looking perfectly fine, serving the last visitor's mess — extra Apple IDs, deleted library items
// — because seeding is idempotent and idempotent means it leaves what it finds.
//
// So the reset is a property of THIS PROCESS. Whatever the platform does, a start is a clean demo.
//
// There is deliberately no matching wipe on the way out. A container stop is entitled to be a
// SIGKILL, so an exit hook is exactly the half a real reset cannot depend on — and with the state
// under a throwaway path there is nothing left behind worth the false confidence.
func Reset() error {
	return reset(StateRoot)
}

// reset takes the root as an argument so a test can drive the real thing over a temp directory
// rather than a reimplementation of it — and so no test has to delete a path outside its own.
func reset(root string) error {
	// A guard against the one edit that turns this into a catastrophe: root coming from
	// somewhere else and arriving empty or "/". RemoveAll would carry it out without complaint.
	if root == "" || filepath.Clean(root) == "/" {
		return errors.New("refusing to reset the whole filesystem")
	}
	return os.RemoveAll(root)
}

// Password is published, on the login screen and in the README. A demo whose password is a secret
// is a login form with no way in.
//
// It still goes through the ordinary argon2id path rather than bypassing the gate, so the demo
// exercises the same sign-in every other install uses — including the session cookie, which is
// where a mode with its own shortcut would quietly diverge.
const Password = "springback-demo"

// Account is the Apple ID the demo arrives with. example.com is reserved by RFC 2606 and can
// belong to nobody, which matters more here than it looks: this address is on a public screen, and
// a plausible-looking one at a real domain would eventually reach a real person's inbox.
const Account = "demo@example.com"

// archived is what the library starts with — and it is DELISTED ON PURPOSE.
//
// Boomerang is the fixture that carries springback's whole argument: Apple pulled it, it is still
// installed on the fixture iPhone, and no storefront will admit it exists. A demo library stocked
// with apps anyone can still download from Apple would demonstrate the one thing this tool is not
// for.
var archived = []int64{6744684419}

// Seed makes the box usable by a stranger. It is idempotent in the only sense that matters — a box
// that already has a password is left alone — because the state it writes lives on a disk that is
// wiped between boots anyway.
func Seed(ctx context.Context, log *slog.Logger, t tools.Tools, a *auth.Service, accounts *store.Accounts, library *store.Library) error {
	// REFUSE THE REAL TOOLS. A demo seeds an Apple ID and downloads an app, which against the
	// real layer means signing in to Apple with a made-up password and pulling an .ipa. The
	// caller already forces the fake; this is the check that survives someone changing that.
	if _, fake := t.(*tools.Fake); !fake {
		return errors.New("the demo can only be seeded against the fake tool layer")
	}

	// A CHEAP KDF, BECAUSE THERE IS NO SECRET FOR AN EXPENSIVE ONE TO PROTECT.
	//
	// The production parameters are RFC 9106's second recommendation — 64 MiB per derivation,
	// the right price for a hash standing between a stranger and every Apple ID on someone's
	// box. Here the password is printed on the login screen, so the work factor buys nothing at
	// all, and it costs something real. Measured against this image, on the 256 MB machine the
	// demo is specced for:
	//
	//	64 MiB params   idle 73 MB   five concurrent logins  403 MB   <- OOM kill
	//	 8 MiB params   idle 15 MB   five concurrent logins   49 MB
	//
	// Sharing the link is what produces simultaneous logins, so the failure would arrive exactly
	// when the demo started working. The idle figures differ because seeding derives the hash
	// too: under the old parameters an untouched demo was already carrying that arena.
	//
	// 8 MiB is still a real argon2id hash through the real sign-in path. The parameters travel
	// inside the PHC string, so verification costs whatever the stored hash says and this does
	// not have to be set anywhere else.
	a.Params = auth.Params{Memory: 8 * 1024, Iterations: 2, Parallelism: 1, SaltLen: 16, KeyLen: 32}

	if err := a.SetPassword(Password); err != nil && !errors.Is(err, auth.ErrAlreadySetUp) {
		return fmt.Errorf("demo password: %w", err)
	}

	acc, _, err := accounts.Create(Account)
	if err != nil {
		return fmt.Errorf("demo account: %w", err)
	}
	// The passphrase and home directory are the real ones this account would use, so the
	// download below goes through the same call the Archive button makes.
	if err := t.AuthLogin(ctx, acc.Home(accounts.Root), acc.KeychainPP, Account, "demo", ""); err != nil {
		return fmt.Errorf("demo sign-in: %w", err)
	}

	for _, id := range archived {
		if library.Has(id) {
			continue
		}
		out, err := library.PrepareDir(id)
		if err != nil {
			return fmt.Errorf("demo library %d: %w", id, err)
		}
		// nil progress: nobody is watching a startup, and the fake's progress pacing would
		// otherwise hold the listener back by the ~14 seconds it spends imitating a real
		// download.
		if _, err := t.Download(ctx, acc.Home(accounts.Root), acc.KeychainPP, id, out, nil); err != nil {
			library.DiscardIfEmpty(id)
			return fmt.Errorf("demo download %d: %w", id, err)
		}
		item, err := library.Record(id, acc.Slug)
		if err != nil {
			return fmt.Errorf("demo record %d: %w", id, err)
		}
		log.Info("demo library seeded", "app", item.Name, "bundle", item.BundleID)
	}
	return nil
}
