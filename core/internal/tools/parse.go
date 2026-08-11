package tools

import (
	"encoding/csv"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// parseAppList reads `ideviceinstaller -n -u <udid> list --user`.
//
// The real shape, captured from a live iPhone on 2026-08-11:
//
//	CFBundleIdentifier, CFBundleShortVersionString, CFBundleDisplayName
//	ru.aviasales.app, "9.28", "Aviasales"
//	ru.cardsmobile.wallet, "6.63", "Кошелёк"
//
// A header line, then one row per app: bare bundle id, then two QUOTED fields separated by
// ", ". csv handles it with TrimLeadingSpace (so ` "9.28"` is read as a quoted field rather
// than a literal one starting with a space) and LazyQuotes (so a display name containing a
// stray quote degrades to a slightly wrong name instead of failing the whole device).
//
// FieldsPerRecord is -1 deliberately: a malformed row should cost that row, not the list. A
// device with 162 apps is the ordinary case, and refusing all of them because one display name
// is strange would take the screen out for a reason the user cannot act on.
func parseAppList(out string) []InstalledApp {
	r := csv.NewReader(strings.NewReader(out))
	r.Comma = ','
	r.TrimLeadingSpace = true
	r.LazyQuotes = true
	r.FieldsPerRecord = -1

	var apps []InstalledApp
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			continue
		}
		if len(rec) < 1 {
			continue
		}
		bundle := strings.TrimSpace(rec[0])
		// The header names itself; skip it rather than pattern-matching a bundle id.
		if bundle == "" || bundle == "CFBundleIdentifier" {
			continue
		}
		app := InstalledApp{BundleID: bundle}
		if len(rec) > 1 {
			app.Version = strings.TrimSpace(rec[1])
		}
		if len(rec) > 2 {
			app.Name = strings.TrimSpace(rec[2])
		}
		// A missing display name is common enough on system-adjacent apps; the bundle id is
		// always there and is a usable label.
		if app.Name == "" {
			app.Name = app.BundleID
		}
		apps = append(apps, app)
	}
	return apps
}

var installProgressRE = regexp.MustCompile(`^Install:\s+([A-Za-z]+)(?:\s+\((\d+)%\))?`)

// parseInstallLine reads one line of ideviceinstaller's install chatter, e.g.
// `Install: InstallingApplication (60%)`. ok is false for lines that are not progress.
func parseInstallLine(line string) (p InstallProgress, ok bool) {
	m := installProgressRE.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return InstallProgress{}, false
	}
	p.Stage = m[1]
	if m[2] != "" {
		p.Percent, _ = strconv.Atoi(m[2])
	}
	return p, true
}

// installComplete reports whether the run reached the one line that means success.
//
// ABSENCE OF "Complete" IS FAILURE (SPEC §3). ideviceinstaller can exit 0 having stopped
// partway, so the exit code is not the signal — this line is.
func installComplete(out string) bool {
	return strings.Contains(out, "Install: Complete")
}

// purchasedRE matches ipatool's report of whether a licence was ACQUIRED by this download.
// Both output shapes are accepted: the default console writer emits `purchased=false` and
// `--format json` emits `"purchased":false`.
var purchasedRE = regexp.MustCompile(`"?purchased"?\s*[=:]\s*(true|false)`)

func parsePurchased(out string) bool {
	m := purchasedRE.FindStringSubmatch(out)
	return m != nil && m[1] == "true"
}

// classify maps a tool's own words onto the SPEC §7 failure modes.
//
// Substring matching over stderr is a fragile contract with an upstream that never promised it,
// so an unrecognised failure falls through to the raw error rather than being forced into one of
// these buckets. A wrong diagnosis sends the user to fix the wrong thing, which is worse than
// showing them what the tool actually said.
func classify(out string, fallback error) error {
	l := strings.ToLower(out)
	switch {
	case containsAny(l, "auth code is required", "2fa code is required", "two-factor", "verification code", "authcode"):
		return ErrNeeds2FA
	case strings.Contains(l, "license not found"):
		return ErrLicenseNotFound
	case strings.Contains(l, "app not found"):
		return ErrAppNotFound
	case containsAny(l, "item not found", "keyring", "not logged in"):
		return ErrNotAuthenticated
	case containsAny(l, "could not connect", "no device found", "device not found"):
		return ErrDeviceUnreachable
	}
	return fallback
}

// needsAuthCodePrompt reports whether ipatool is sitting at a 2FA prompt waiting for input.
//
// Deliberately narrow: it must match a PROMPT and not the word "password", or the first prompt
// would be read as the second and every login would be reported as needing a code. The trailing
// colon is what distinguishes ipatool asking a question from ipatool describing an error.
func needsAuthCodePrompt(out string) bool {
	l := strings.ToLower(out)
	for _, marker := range []string{"2fa code", "auth code", "verification code", "two-factor"} {
		i := strings.Index(l, marker)
		if i < 0 {
			continue
		}
		// A prompt ends in ':' with nothing after it yet — the process is waiting. An
		// error message mentioning the same words is a complete sentence and is left to
		// classify() and the exit path.
		rest := strings.TrimRight(l[i+len(marker):], " \t\r\n")
		if strings.HasSuffix(rest, ":") {
			return true
		}
	}
	return false
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
