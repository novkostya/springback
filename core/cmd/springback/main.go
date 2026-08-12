// Command springback serves the library manager.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/novkostya/springback/core/internal/auth"
	"github.com/novkostya/springback/core/internal/demo"
	"github.com/novkostya/springback/core/internal/devices"
	"github.com/novkostya/springback/core/internal/httpapi"
	"github.com/novkostya/springback/core/internal/jobs"
	"github.com/novkostya/springback/core/internal/store"
	"github.com/novkostya/springback/core/internal/storefront"
	"github.com/novkostya/springback/core/internal/tools"
	"github.com/novkostya/springback/core/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version.Version)
		return
	}
	// One subcommand, `serve`, so that adding a second later does not change how the first
	// is invoked. Accepting it optionally keeps `springback` alone working too.
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}

	fs := flag.NewFlagSet("springback", flag.ExitOnError)
	listen := fs.String("listen", env("SPRINGBACK_LISTEN", ":8971"), "listen address")
	libraryDir := fs.String("library", env("SPRINGBACK_LIBRARY", "/library"), "library directory")
	accountsDir := fs.String("accounts", env("SPRINGBACK_ACCOUNTS", "/accounts"), "accounts directory")
	lockdownDir := fs.String("lockdown", env("SPRINGBACK_LOCKDOWN", "/var/lib/lockdown"), "pairing records, mounted read-only")
	muxAddr := fs.String("mux", os.Getenv("USBMUXD_SOCKET_ADDRESS"), "muxer address, e.g. 127.0.0.1:27015 for netmuxd; empty uses libimobiledevice's default unix socket")
	cacheTTL := fs.Duration("cache-ttl", 7*24*time.Hour, "how long a store verdict stays cached")
	fake := fs.Bool("fake", os.Getenv("SPRINGBACK_FAKE") != "", "use the fake tool layer (no hardware, no Apple)")
	publicDemo := fs.Bool("public-demo", os.Getenv("SPRINGBACK_PUBLIC_DEMO") != "", "run as a throwaway public demo: fake tools, a published password, fixture data")
	debug := fs.Bool("debug", false, "verbose logging")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	// REFUSE rather than ignore. Without this, `springback bogus` parses no flags, leaves
	// "bogus" in the residue, and starts a perfectly normal server — so a typo'd flag in a
	// compose file silently runs with defaults instead of what was written there.
	if rest := fs.Args(); len(rest) > 0 {
		fmt.Fprintf(os.Stderr, "springback: unexpected argument %q\n", rest[0])
		fs.Usage()
		os.Exit(2)
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	// THE DEMO FORCES THE FAKE, and does not merely default to it. This mode publishes its own
	// password, so everything reachable behind that password is reachable by anyone at all —
	// which is fine against fixtures and catastrophic against the real tool layer, where the
	// same screens sign in to Apple and install software onto whatever is plugged in. A missing
	// `-fake` in a deploy file must not be the difference.
	if *publicDemo {
		*fake = true
	}

	var t tools.Tools
	if *fake {
		// The whole app minus Apple and minus hardware, so the at-risk screen can be
		// developed and exercised on a box with neither.
		t = tools.NewFake()
		log.Warn("running with the FAKE tool layer — nothing here talks to a real device or Apple")
	} else {
		real := tools.NewReal(*muxAddr, *lockdownDir)
		if *debug {
			// Only with --debug: these lines carry Apple's replies verbatim.
			real.Debug = func(out string) { log.Debug("ipatool auth output", "out", out) }
		}
		t = real
	}

	for _, dir := range []string{*libraryDir, *accountsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Error("cannot create directory", "dir", dir, "err", err)
			os.Exit(1)
		}
	}

	authSvc, err := auth.New(*accountsDir)
	if err != nil {
		log.Error("cannot read the password file", "err", err)
		os.Exit(1)
	}

	library := store.NewLibrary(*libraryDir)
	accounts := store.NewAccounts(*accountsDir)
	// Under the library root because that is the volume with room on it, and in a dot-directory
	// because Library.List() keys on directory names that parse as numeric App Store ids — so
	// this one is skipped by the same rule that already skips store-status-cache.json.
	deviceIcons := store.NewDeviceIcons(filepath.Join(*libraryDir, ".device-icons"), t)
	// Beside the device icons, and for the same reason they are on disk rather than in memory:
	// a couple of hundred small pictures that never change.
	storeIcons := store.NewStoreIcons(*libraryDir, nil)
	// Last-known device facts, so a phone that is elsewhere still has a name on screen.
	deviceCache := store.NewDeviceCache(*libraryDir)
	resolver := storefront.NewResolver(t, *cacheTTL, store.NewStatusCache(*libraryDir))
	jobRegistry := jobs.NewRegistry()

	// Seeded BEFORE the listener opens, so the first request cannot arrive at a half-built demo
	// — an empty library for one visitor and a full one for the next reads as a broken instance
	// rather than a slow start.
	if *publicDemo {
		if err := demo.Seed(context.Background(), log, t, authSvc, accounts, library); err != nil {
			// Fatal on purpose. A demo that came up without its password set is a public
			// login form that nobody can pass, and one without fixtures is three empty
			// screens; both are worse than a deploy that visibly failed.
			log.Error("cannot seed the demo", "err", err)
			os.Exit(1)
		}
		log.Warn("PUBLIC DEMO: fake tools, and the password is published on the login screen",
			"password", demo.Password)
	}

	srv := &httpapi.Server{
		Tools:       t,
		Auth:        authSvc,
		Devices:     &devices.Service{Tools: t, Resolver: resolver, Library: library, Cache: deviceCache},
		Library:     library,
		DeviceIcons: deviceIcons,
		StoreIcons:  storeIcons,
		Accounts:    accounts,
		Resolver:    resolver,
		Jobs:        jobRegistry,
		Log:         log,
		Fake:        *fake,
	}
	if *publicDemo {
		srv.DemoPassword = demo.Password
	}

	httpSrv := &http.Server{
		Addr:    *listen,
		Handler: srv.Handler(),
		// No WriteTimeout, deliberately. v0.1 holds the request open for downloads
		// (~30 s and up) and installs (slower still) — SPEC §5 allows it and the UI says
		// so. A write timeout here would cut exactly those two requests off mid-flight.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	log.Info("springback",
		"version", version.Version, "listen", *listen,
		"library", *libraryDir, "accounts", *accountsDir,
		"lockdown", *lockdownDir, "mux", *muxAddr, "fake", *fake)

	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
