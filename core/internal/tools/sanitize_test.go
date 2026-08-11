package tools

import (
	"errors"
	"strings"
	"testing"
)

// The exact bytes a failed `ipatool auth login` produced, escapes and all. ipatool colours its
// output unconditionally — it does not check whether the destination is a terminal — so these
// travelled all the way into the browser, where they rendered as a row of empty boxes around the
// words the user needed to read.
const realIpatoolError = "\x1b[90m11:32AM\x1b[0m \x1b[1m\x1b[31mERR\x1b[0m\x1b[0m " +
	"\x1b[36merror=\x1b[0m\x1b[31m\"password is required when not running in interactive mode; " +
	"use the \\\"--password\\\" flag\"\x1b[0m \x1b[36msuccess=\x1b[0mfalse"

func TestSanitizeStripsAnsi(t *testing.T) {
	got := sanitize(realIpatoolError)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("escape sequences survived: %q", got)
	}
	// The message itself must be intact — stripping is not the same as truncating.
	for _, want := range []string{"password is required", "--password", "success=false", "11:32AM"} {
		if !strings.Contains(got, want) {
			t.Errorf("sanitize dropped %q from the message: %q", want, got)
		}
	}
}

func TestSanitizeLeavesPlainTextAlone(t *testing.T) {
	in := "Install: Complete"
	if got := sanitize(in); got != in {
		t.Errorf("got %q, want %q unchanged", got, in)
	}
}

// The password reaches ipatool over a pty. If it ever comes back on the master — a terminal that
// ignored the echo setting, or a future ipatool that echoes deliberately — it must not travel
// into an error message, a log line, or a browser.
func TestScrubSecretRemovesThePassword(t *testing.T) {
	out := "enter password: hunter2sekrit\nERR error=\"bad login\""
	got := scrubSecret(out, "hunter2sekrit")
	if strings.Contains(got, "hunter2sekrit") {
		t.Fatalf("the password survived scrubbing: %q", got)
	}
	if !strings.Contains(got, "[redacted]") || !strings.Contains(got, "bad login") {
		t.Errorf("scrubbing damaged the surrounding message: %q", got)
	}
}

// A short secret would turn the scrubber into a find-and-replace on common substrings, mangling
// output for no benefit — a two-character password's appearance in "ERR" is not a leak worth
// corrupting every message over.
func TestScrubSecretIgnoresVeryShortSecrets(t *testing.T) {
	out := "ERR error=\"bad login\""
	if got := scrubSecret(out, "ER"); got != out {
		t.Errorf("got %q, want %q unchanged", got, out)
	}
	if got := scrubSecret(out, ""); got != out {
		t.Errorf("empty secret changed the output: %q", got)
	}
}

// Apple answering the Store API with something unusable is NOT a wrong password, and must not be
// reported as one. Taken verbatim from a real failed sign-in and from upstream issue #513.
func TestClassifyAppleRejection(t *testing.T) {
	out := `INF enter password: ERR error="request failed: unexpected response from Apple (HTTP 204): empty or non-plist body" success=false`
	if got := classify(out, nil); !errors.Is(got, ErrAppleRejected) {
		t.Fatalf("classify = %v, want ErrAppleRejected", got)
	}
	// The status code fluctuates between attempts (#513 reports 204/403/404/301), so the
	// match must be on the shape of the message rather than on one number.
	for _, s := range []string{
		`ERR error="request failed: unexpected response from Apple (HTTP 403): 403 Forbidden"`,
		`ERR error="request failed: unexpected response from Apple (HTTP 301): 301 Moved Permanently"`,
	} {
		if got := classify(s, nil); !errors.Is(got, ErrAppleRejected) {
			t.Errorf("classify(%q) = %v, want ErrAppleRejected", s, got)
		}
	}
	// The specific failures must still win — this catch-all must not swallow them.
	if got := classify(`ERR error="license not found" success=false`, nil); !errors.Is(got, ErrLicenseNotFound) {
		t.Errorf("license error was swallowed by the Apple catch-all: %v", got)
	}
	if got := classify(`ERR error="2FA code is required"`, nil); !errors.Is(got, ErrNeeds2FA) {
		t.Errorf("2FA prompt was swallowed by the Apple catch-all: %v", got)
	}
}
