package tools

import (
	"errors"
	"strings"
	"testing"
)

// The exact bytes `ideviceinstaller -n -u <udid> list --user` printed for the staging iPhone on
// 2026-08-11. Captured rather than invented: the shape is an undocumented side effect of how the
// tool formats a plist, and the only thing that makes this parser correct is that it was read
// off the tool springback actually runs.
const realListOutput = `CFBundleIdentifier, CFBundleShortVersionString, CFBundleDisplayName
ru.aviasales.app, "9.28", "Aviasales"
ru.yandex.mobile.music, "797", "Yandex Music"
com.dreamgoods.officecapital, "1.8", "OfficeCapital"
io.wio.retail, "1.69.0", "Wio Personal"
com.google.ios.youtube, "21.31.3", "YouTube"
ru.cardsmobile.wallet, "6.63", "Кошелёк"
com.tinyspeck.chatlyio, "26.08.10", "Slack"
`

func TestParseAppListRealOutput(t *testing.T) {
	apps := parseAppList(realListOutput)
	if len(apps) != 7 {
		t.Fatalf("got %d apps, want 7 — the header must be skipped and every row kept", len(apps))
	}
	if apps[0].BundleID != "ru.aviasales.app" || apps[0].Version != "9.28" || apps[0].Name != "Aviasales" {
		t.Errorf("first row: %+v", apps[0])
	}
	// A quoted display name with a space, which is why this is parsed as CSV and not split on
	// commas.
	if apps[1].Name != "Yandex Music" {
		t.Errorf("quoted name with a space: got %q", apps[1].Name)
	}
	// Non-ASCII display names are ordinary, not an edge case.
	if apps[5].Name != "Кошелёк" {
		t.Errorf("non-ascii name: got %q", apps[5].Name)
	}
	for _, a := range apps {
		if a.BundleID == "CFBundleIdentifier" {
			t.Error("the header row was parsed as an app")
		}
	}
}

func TestParseAppListEmptyAndHeaderOnly(t *testing.T) {
	// A device with no user apps prints just the header. That is zero apps, not an error.
	if apps := parseAppList("CFBundleIdentifier, CFBundleShortVersionString, CFBundleDisplayName\n"); len(apps) != 0 {
		t.Errorf("header-only: got %d apps, want 0", len(apps))
	}
	if apps := parseAppList(""); len(apps) != 0 {
		t.Errorf("empty: got %d apps, want 0", len(apps))
	}
}

// One strange row must cost that row, not the whole device. Refusing 162 apps because one
// display name has an unbalanced quote would take the screen out for a reason the user cannot
// act on.
func TestParseAppListSurvivesAOneBadRow(t *testing.T) {
	out := `CFBundleIdentifier, CFBundleShortVersionString, CFBundleDisplayName
com.good.first, "1.0", "First"
com.weird.quote, "2.0", "He said "hi" to me"
com.good.last, "3.0", "Last"
`
	apps := parseAppList(out)
	var seen []string
	for _, a := range apps {
		seen = append(seen, a.BundleID)
	}
	has := func(id string) bool {
		for _, s := range seen {
			if s == id {
				return true
			}
		}
		return false
	}
	if !has("com.good.first") || !has("com.good.last") {
		t.Fatalf("a malformed row took out its neighbours: parsed %v", seen)
	}
}

// A row with no display name still yields a usable label rather than a blank cell.
func TestParseAppListFallsBackToBundleIDForName(t *testing.T) {
	apps := parseAppList("CFBundleIdentifier, CFBundleShortVersionString, CFBundleDisplayName\ncom.no.name, \"1.0\", \"\"\n")
	if len(apps) != 1 || apps[0].Name != "com.no.name" {
		t.Fatalf("got %+v, want the bundle id as the label", apps)
	}
}

func TestParseInstallLine(t *testing.T) {
	p, ok := parseInstallLine("Install: InstallingApplication (60%)")
	if !ok || p.Stage != "InstallingApplication" || p.Percent != 60 {
		t.Errorf("got %+v ok=%v", p, ok)
	}
	// The completion line carries no percentage.
	if p, ok := parseInstallLine("Install: Complete"); !ok || p.Stage != "Complete" || p.Percent != 0 {
		t.Errorf("completion line: got %+v ok=%v", p, ok)
	}
	if _, ok := parseInstallLine("something else entirely"); ok {
		t.Error("matched a non-progress line")
	}
}

// SPEC §3: treat absence of `Install: Complete` as failure. The exit code is not the signal —
// ideviceinstaller can stop partway and still exit 0.
func TestInstallCompleteIsTheOnlySuccessSignal(t *testing.T) {
	full := "Install: CreatingStagingDirectory (5%)\nInstall: InstallingApplication (60%)\nInstall: Complete\n"
	if !installComplete(full) {
		t.Error("a run that reached Complete was read as a failure")
	}
	partial := "Install: CreatingStagingDirectory (5%)\nInstall: InstallingApplication (60%)\n"
	if installComplete(partial) {
		t.Error("a run that stopped at 60% was read as a success")
	}
}

func TestParsePurchased(t *testing.T) {
	// Both output shapes: the default console writer and --format json.
	if parsePurchased(`INF download output=/library/1/1.ipa purchased=false`) {
		t.Error("console form: purchased=false read as true")
	}
	if !parsePurchased(`{"level":"info","purchased":true,"message":"download"}`) {
		t.Error("json form: purchased true not detected")
	}
	// No statement at all is not a purchase.
	if parsePurchased("some unrelated output") {
		t.Error("absence of the field read as a purchase")
	}
}

// SPEC §7's table, as a test. Each of these sends the user somewhere different, so conflating
// any two of them is worse than showing the raw error.
func TestClassifyFailureModes(t *testing.T) {
	fallback := errors.New("raw")
	cases := []struct {
		out  string
		want error
	}{
		{"ERR app not found", ErrAppNotFound},
		{"ERR license not found", ErrLicenseNotFound},
		{"ERR keyring: item not found", ErrNotAuthenticated},
		{"Could not connect to lockdownd", ErrDeviceUnreachable},
		{"ERR 2FA code is required", ErrNeeds2FA},
	}
	for _, c := range cases {
		if got := classify(c.out, fallback); !errors.Is(got, c.want) {
			t.Errorf("classify(%q) = %v, want %v", c.out, got, c.want)
		}
	}
	// An unrecognised failure must NOT be forced into a bucket — a wrong diagnosis sends the
	// user to fix the wrong thing.
	if got := classify("something nobody predicted", fallback); !errors.Is(got, fallback) {
		t.Errorf("unrecognised output was classified as %v instead of passed through", got)
	}
}

// The 2FA prompt has to be told apart from the PASSWORD prompt and from an error message that
// happens to mention the same words. Getting the first wrong reports every login as needing a
// code; getting the second wrong kills a process that was already finishing.
func TestNeedsAuthCodePrompt(t *testing.T) {
	waiting := []string{
		"INF enter 2FA code:",
		"INF enter auth code: ",
		"enter verification code:\n",
	}
	for _, s := range waiting {
		if !needsAuthCodePrompt(s) {
			t.Errorf("did not detect a waiting prompt: %q — this hangs until the timeout", s)
		}
	}
	notWaiting := []string{
		"INF enter password:",                                  // the FIRST prompt
		`ERR error="2FA code is required" success=false`,       // an error, already exiting
		`ERR error="invalid auth code provided" success=false`, // ditto
		"Install: Complete",
		"",
	}
	for _, s := range notWaiting {
		if needsAuthCodePrompt(s) {
			t.Errorf("false positive on %q", s)
		}
	}
}

// Frames captured from the real ipatool on the staging host, progress bar and all.
func TestParseDownloadFrame(t *testing.T) {
	p, ok := parseDownloadFrame("downloading  99% |████ | (195/197 MB, 35 MB/s)")
	if !ok || p.Percent != 99 || p.Detail != "195/197 MB, 35 MB/s" {
		t.Errorf("got %+v ok=%v", p, ok)
	}
	// The frame drawn BEFORE the content length is known. The percentage is real; the
	// "1 B" total is not, and showing it reads as a download of nothing.
	p, ok = parseDownloadFrame("downloading   0% |    | (0/ 1 B)")
	if !ok || p.Percent != 0 {
		t.Fatalf("placeholder frame: got %+v ok=%v", p, ok)
	}
	if p.Detail != "" {
		t.Errorf("placeholder detail %q was passed through to the UI", p.Detail)
	}
	// Not progress at all.
	if _, ok := parseDownloadFrame("INF download complete"); ok {
		t.Error("matched a line with no percentage")
	}
	// A nonsense percentage must not become a bar past 100%.
	if _, ok := parseDownloadFrame("999% |x| (1/2 MB)"); ok {
		t.Error("accepted an out-of-range percentage")
	}
}

func TestStripBarLeavesTheMessage(t *testing.T) {
	got := stripBar("downloading 99% |█████| (195/197 MB)\rERR error=\"license not found\"")
	if strings.Contains(got, "█") {
		t.Errorf("bar glyphs survived: %q", got)
	}
	if !strings.Contains(got, "license not found") {
		t.Errorf("the message was damaged: %q", got)
	}
}

// Captured from a live run against an Apple ID THAT DOES NOT EXIST, with a junk password.
// ipatool still prompts for a code — it falls through to asking whenever it cannot read Apple's
// reply as a success. The detector must fire (otherwise the process hangs until the timeout),
// but nothing built on it may claim that Apple sent a code, because this output is identical to
// a genuine challenge.
func TestRealPromptFromAFailedLoginStillDetected(t *testing.T) {
	const captured = "2:10PM INF enter password:\r\n2:10PM INF enter 2FA code:"
	if !needsAuthCodePrompt(captured) {
		t.Fatal("the prompt was missed, so the process would hang until the 5-minute timeout")
	}
	// The password prompt on its own must never trip it, or every login would report 2FA.
	if needsAuthCodePrompt("2:10PM INF enter password:") {
		t.Error("the password prompt was read as a 2FA prompt")
	}
}

// TestClassifyLicenseWordings pins BOTH of ipatool's spellings.
//
// "license is required" fell through to the raw fallback and put
// `exit status 1: downloading 0% || ( 0/ 1 B) ERR error="license is required" success=false`
// in front of a user whose actual problem was "that account does not own this app".
func TestClassifyLicenseWordings(t *testing.T) {
	for _, out := range []string{
		`ERR error="license not found" success=false`,
		`downloading 0% || ( 0/ 1 B) 4:44AM ERR error="license is required" success=false`,
		`ERR error="License is required" success=false`,
	} {
		if got := classify(out, errors.New("raw")); !errors.Is(got, ErrLicenseNotFound) {
			t.Errorf("classify(%q) = %v, want ErrLicenseNotFound", out, got)
		}
	}
}
