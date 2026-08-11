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
	"syscall"
	"time"

	"github.com/novkostya/springback/core/internal/devices"
	"github.com/novkostya/springback/core/internal/httpapi"
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
	muxAddr := fs.String("mux", env("USBMUXD_SOCKET_ADDRESS", "127.0.0.1:27015"), "netmuxd address")
	cacheTTL := fs.Duration("cache-ttl", 7*24*time.Hour, "how long a store verdict stays cached")
	fake := fs.Bool("fake", os.Getenv("SPRINGBACK_FAKE") != "", "use the fake tool layer (no hardware, no Apple)")
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

	var t tools.Tools
	if *fake {
		// The whole app minus Apple and minus hardware, so the at-risk screen can be
		// developed and exercised on a box with neither.
		t = tools.NewFake()
		log.Warn("running with the FAKE tool layer — nothing here talks to a real device or Apple")
	} else {
		t = tools.NewReal(*muxAddr, *lockdownDir)
	}

	for _, dir := range []string{*libraryDir, *accountsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Error("cannot create directory", "dir", dir, "err", err)
			os.Exit(1)
		}
	}

	library := store.NewLibrary(*libraryDir)
	accounts := store.NewAccounts(*accountsDir)
	resolver := storefront.NewResolver(t, *cacheTTL, store.NewStatusCache(*libraryDir))

	srv := &httpapi.Server{
		Tools:    t,
		Devices:  &devices.Service{Tools: t, Resolver: resolver, Library: library},
		Library:  library,
		Accounts: accounts,
		Resolver: resolver,
		Log:      log,
		Fake:     *fake,
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
