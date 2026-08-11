package tools

import (
	"errors"
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
