package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// Real shells out to the tools SPEC §3 verified, and talks to the iTunes lookup API.
type Real struct {
	// MuxAddr is the muxer to use, exported to every device call as
	// USBMUXD_SOCKET_ADDRESS. springback never runs a muxer of its own: the host's usbmuxd
	// already owns the USB bus, and a second daemon fights it for the devices (SPEC §2).
	MuxAddr string
	// HTTP is the client used for the lookup API only.
	HTTP *http.Client
	// DeviceTimeout bounds a single device command. Devices come and go; a call to a device
	// that went to sleep mid-command must not hold an HTTP handler open forever.
	DeviceTimeout time.Duration
	// DownloadTimeout bounds one ipatool download. ~500 MB per app over a store CDN
	// (SPEC §3 measured 487 MB), so this is minutes, not seconds.
	DownloadTimeout time.Duration
	// InstallTimeout bounds one install, which is slower than a download.
	InstallTimeout time.Duration
	// LockdownDir holds the pairing records. Mounted read-write when springback pairs devices
	// itself, and read-only when another tool on the host owns them (SPEC §2).
	LockdownDir string
	// Debug, when set, receives each auth attempt's raw output — ANSI stripped and with the
	// password scrubbed out.
	//
	// It exists because "it says 2FA but no code arrived" is unanswerable without seeing what
	// ipatool actually printed, and that output was being discarded on every path. Off unless
	// --debug is passed, since the alternative is writing Apple's replies to a log by default.
	Debug func(string)

	// transports remembers which link each device last answered on, because every CLI here
	// has to be told: `ideviceinfo -n` fails on a device that is only on the cable, and the
	// bare form fails on one that is only on Wi-Fi.
	transports sync.Map
}

// NewReal builds a Real with the defaults every deployment uses.
func NewReal(muxAddr, lockdownDir string) *Real {
	return &Real{
		MuxAddr:         muxAddr,
		LockdownDir:     lockdownDir,
		HTTP:            &http.Client{Timeout: 20 * time.Second},
		DeviceTimeout:   60 * time.Second,
		DownloadTimeout: 30 * time.Minute,
		InstallTimeout:  30 * time.Minute,
	}
}

// run executes a command and returns its combined output. env entries are ADDED to a minimal
// environment rather than inheriting the server's: the child processes here are given exactly
// the variables they need, and nothing else the web process happens to be carrying.
func (r *Real) run(ctx context.Context, timeout time.Duration, env []string, stdin string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	bin, err := resolveTool(name)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append([]string{toolPATH}, env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	out := buf.String()
	if err != nil {
		// sanitize only on the path where the text becomes a MESSAGE. The raw output is
		// still returned for parsing, because the parsers key on the tool's own words.
		return out, classify(out, fmt.Errorf("%s: %w: %s", name, err, sanitize(out)))
	}
	return out, nil
}

// deviceEnv points the libimobiledevice tools at the muxer.
//
// EMPTY MEANS "DON'T SAY", not "say nothing". With no MuxAddr configured the variable is left
// off the child's environment entirely, so libusbmuxd falls back to its own default — the unix
// socket at /var/run/usbmuxd, which is what a plain `usbmuxd` on the host provides and the
// simplest way to run this. Setting the variable to an empty string instead would override that
// default with nothing and break every device call.
func (r *Real) deviceEnv() []string {
	if r.MuxAddr == "" {
		return nil
	}
	return []string{"USBMUXD_SOCKET_ADDRESS=" + r.MuxAddr}
}

// ListDeviceUDIDs returns everything the muxer can currently see, over either transport.
//
// BOTH TRANSPORTS, WHICH IS A CHANGE. It used to ask only `idevice_id -n`, because another tool
// owned the USB bus and springback's whole job arrived over netmuxd. Standing alone, the device
// someone has just plugged in to pair is on USB and nowhere else — asking only the network would
// show them an empty screen and no way out of it.
//
// The transport each device answered on is remembered, because every other CLI needs to be told:
// `ideviceinfo -n` fails on a USB device and `ideviceinfo` without it fails on a network one.
func (r *Real) ListDeviceUDIDs(ctx context.Context) ([]string, error) {
	seen := map[string]bool{}
	var udids []string

	collect := func(flag, transport string) error {
		out, err := r.run(ctx, r.DeviceTimeout, r.deviceEnv(), "", "idevice_id", flag)
		if err != nil {
			// An empty list exits non-zero on some builds. Nothing on stdout plus a
			// failure is the ordinary "nothing on this transport" case, NOT an error.
			if strings.TrimSpace(out) == "" {
				return nil
			}
			return err
		}
		for _, line := range strings.Split(out, "\n") {
			u := strings.TrimSpace(line)
			if u == "" || seen[u] {
				continue
			}
			seen[u] = true
			udids = append(udids, u)
			r.transports.Store(u, transport)
		}
		return nil
	}

	// USB first, so a device on both wins the cable: it is the faster link and the only one
	// that can pair.
	usbErr := collect("-l", transportUSB)
	netErr := collect("-n", transportNetwork)
	if usbErr != nil && netErr != nil {
		// BOTH TRANSPORTS FAILING USUALLY MEANS THERE IS NO MUXER, not that something is
		// wrong with the devices — the muxer has not started yet, or is somewhere other than
		// where springback was told to look. `idevice_id` says only "Unable to retrieve
		// device list!", which names neither the cause nor the address it tried.
		//
		// It is not fatal and it self-heals: nothing here holds a connection open, so the
		// next poll succeeds the moment a muxer appears. Measured — springback started with
		// no muxer at all picked up four devices within three seconds of one starting, with
		// no restart. So this is a message, not a catastrophe.
		if strings.Contains(strings.ToLower(usbErr.Error()+netErr.Error()), "unable to retrieve device list") {
			return nil, fmt.Errorf("%w: no muxer answering at %s", ErrNoMuxer, r.muxDescription())
		}
		return nil, netErr
	}
	return udids, nil
}

// muxDescription names where springback is looking for a muxer, for an error message.
func (r *Real) muxDescription() string {
	if r.MuxAddr == "" {
		return "the default socket /var/run/usbmuxd"
	}
	return r.MuxAddr
}

const (
	transportUSB     = "usb"
	transportNetwork = "network"
)

// netFlag is the transport flag for one device: `-n` for a device reached over the network, and
// nothing at all for one on the cable. Defaults to `-n` for a device never seen in a listing,
// which is the case for every device that is currently asleep — and the transport it will come
// back on.
func (r *Real) netFlag(udid string) []string {
	if v, ok := r.transports.Load(udid); ok && v.(string) == transportUSB {
		return nil
	}
	return []string{"-n"}
}

// Transport reports how a device was last seen, for the UI to show and for pairing to refuse.
func (r *Real) Transport(udid string) string {
	if v, ok := r.transports.Load(udid); ok {
		return v.(string)
	}
	return transportNetwork
}

// PairedUDIDs lists the pairing records. Each is <udid>.plist; SystemConfiguration.plist is the
// host's own record and names no device, so it is skipped.
//
// A missing or unreadable directory is NOT an error. springback runs on boxes where the mount
// was not wired up, and the honest answer there is "no pairing records, so only devices that are
// awake right now" — a degraded Devices screen, not a dead one.
func (r *Real) PairedUDIDs(ctx context.Context) ([]string, error) {
	if r.LockdownDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(r.LockdownDir)
	if err != nil {
		return nil, nil
	}
	var udids []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".plist") || name == "SystemConfiguration.plist" {
			continue
		}
		udids = append(udids, strings.TrimSuffix(name, ".plist"))
	}
	return udids, nil
}

// DeviceValue reads one lockdown key.
//
// GUARDED, AND THIS IS THE ONE THAT CAUSED THE BUG. `ideviceinfo -k DeviceName` looks like a
// read-only question and is not: libimobiledevice does a lockdown HANDSHAKE to ask it, and a
// handshake with no pairing record pairs. springback asks four keys of every reachable device on
// every scan, so a phone plugged into the box raised "Trust This Computer?" within seconds, with
// nothing on any screen to explain why. See ErrNotPaired.
func (r *Real) DeviceValue(ctx context.Context, udid, key string) (string, error) {
	if err := r.requirePaired(udid); err != nil {
		return "", err
	}
	out, err := r.run(ctx, r.DeviceTimeout, r.deviceEnv(), "", "ideviceinfo", append(r.netFlag(udid), "-u", udid, "-k", key)...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ListApps returns the user apps, asking for the purchase receipt alongside the bundle info.
//
// `-a iTunesMetadata` is the whole reason this is an XML call rather than the simple CSV one:
// the receipt carries the numeric App Store id, and without it a delisted app has to have its id
// typed in by hand. See applist.go.
//
// THE CSV PATH REMAINS AS A FALLBACK, deliberately. The attribute request is the more elaborate
// call and it is the one that could fail on an older device or a future ideviceinstaller — and
// if it does, the right outcome is a Devices screen that still lists apps and merely asks for an
// id, not an empty screen. Degrading beats disappearing.
func (r *Real) ListApps(ctx context.Context, udid string) ([]InstalledApp, error) {
	if err := r.requirePaired(udid); err != nil {
		return nil, err
	}
	out, err := r.run(ctx, r.DeviceTimeout, r.deviceEnv(), "",
		"ideviceinstaller", append(r.netFlag(udid), "-u", udid, "list", "--user", "--xml",
			"-a", "CFBundleIdentifier",
			"-a", "CFBundleShortVersionString",
			"-a", "CFBundleDisplayName",
			"-a", "CFBundleName",
			"-a", "iTunesMetadata",
			// The installed size of the app bundle. Free — it rides along on a call that was
			// already being made — and it is the ONLY size available for a delisted app,
			// which by definition has no store record to ask.
			"-a", "StaticDiskUsage")...)
	if err == nil {
		if apps := parseAppListXML(out); len(apps) > 0 {
			return apps, nil
		}
	}

	out, csvErr := r.run(ctx, r.DeviceTimeout, r.deviceEnv(), "", "ideviceinstaller", append(r.netFlag(udid), "-u", udid, "list", "--user")...)
	if csvErr != nil {
		// Report the FIRST failure if there was one: it is the call that was supposed to
		// work, and its error is the one that explains the device.
		if err != nil {
			return nil, err
		}
		return nil, csvErr
	}
	return parseAppList(out), nil
}

func (r *Real) InstallApp(ctx context.Context, udid, ipaPath string, onProgress func(InstallProgress)) error {
	if err := r.requirePaired(udid); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, r.InstallTimeout)
	defer cancel()

	bin, err := resolveTool("ideviceinstaller")
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, append(r.netFlag(udid), "-u", udid, "install", ipaPath)...)
	cmd.Env = append([]string{toolPATH}, r.deviceEnv()...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}

	var seen bytes.Buffer
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		seen.WriteString(line)
		seen.WriteByte('\n')
		if p, ok := parseInstallLine(line); ok && onProgress != nil {
			onProgress(p)
		}
	}
	waitErr := cmd.Wait()

	// THE EXIT CODE IS NOT THE SIGNAL. ideviceinstaller can stop partway and still exit 0, so
	// success is the presence of the completion line and nothing else (SPEC §3). When it is
	// missing, the last line names the stage that failed — so it is what the user is shown.
	if !installComplete(seen.String()) {
		if err := classify(seen.String(), nil); err != nil {
			return err
		}
		last := sanitize(lastMeaningfulLine(seen.String()))
		if waitErr != nil && last == "" {
			return fmt.Errorf("%w: %v", ErrInstallIncomplete, waitErr)
		}
		return fmt.Errorf("%w: %s", ErrInstallIncomplete, last)
	}
	return nil
}

func lastMeaningfulLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// ipatoolEnv isolates one Apple ID by HOME, which SPEC §3 measured as the isolation boundary:
// ipatool keeps .ipatool/{account,cookies} under HOME, so one directory per Apple ID keeps
// sessions from treading on each other.
func ipatoolEnv(home string) []string {
	return []string{"HOME=" + home}
}

// AuthLogin signs in, with the password delivered over a PTY.
//
// WHY A PTY, AND NOT STDIN OR --password. SPEC §3 says "password over stdin, never argv", on the
// reasoning that argv is world-readable in /proc and lands in `ps`. That requirement is right and
// stands. The MECHANISM in the spec does not work against ipatool 2.3.2, and both halves of it
// were measured on 2026-08-11:
//
//   - With --non-interactive, ipatool does not prompt at all. It refuses immediately:
//     "password is required when not running in interactive mode; use the \"--password\" flag".
//     Nothing is ever read from stdin.
//   - Without --non-interactive it does prompt — but it reads the password with a TERMINAL read
//     on fd 0, so a pipe fails with "failed to read password: inappropriate ioctl for device".
//
// So the two documented options are mutually exclusive: argv, or a real terminal. A PTY is the
// third door, and it satisfies what the spec actually asked for — the secret goes through a
// file descriptor pair private to these two processes, never appears in argv, and is gone when
// the call returns. --non-interactive is therefore DROPPED for this one call, and only this one:
// every other ipatool invocation still passes it, because none of them needs a password.
func (r *Real) AuthLogin(ctx context.Context, home, passphrase, email, password, authCode string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	args := []string{"auth", "login", "-e", email, "--keychain-passphrase", passphrase}
	if authCode != "" {
		// The 2FA code is a short-lived, single-use value and the spec's own invocation
		// passes it as a flag. It is the PASSWORD that must not reach argv.
		args = append(args, "--auth-code", authCode)
	}

	bin, err := resolveTool("ipatool")
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append([]string{toolPATH, "TERM=dumb"}, ipatoolEnv(home)...)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("ipatool: cannot allocate a terminal for the password prompt: %w", err)
	}
	defer func() { _ = ptmx.Close() }()

	// ECHO OFF BEFORE THE PASSWORD IS WRITTEN. ipatool's own reader disables echo while it
	// prompts, but that happens on ITS schedule, and anything echoed before then comes back
	// on the master and lands in the captured output below — which is surfaced in errors. This
	// closes that window rather than trusting the timing. Best-effort: if the terminal will not
	// take the setting, scrubSecret below is still between the password and any output.
	disableEcho(ptmx)

	// Read the master concurrently with writing. A pty has a small kernel buffer, so writing
	// the password while nothing drains the other side can deadlock before the prompt appears.
	var mu sync.Mutex
	var buf bytes.Buffer
	var killedForAuthCode bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		chunk := make([]byte, 4096)
		prompted := false
		for {
			n, err := ptmx.Read(chunk)
			if n > 0 {
				mu.Lock()
				buf.Write(chunk[:n])
				seen := buf.String()
				mu.Unlock()

				// THE SECOND PROMPT IS A HANG, NOT AN ERROR, and only because this
				// call is interactive now. With --non-interactive (every other
				// ipatool call here) a missing 2FA code is an immediate refusal. On
				// a terminal, ipatool asks for the code and WAITS — and the only
				// thing on the other end is this process, which has nothing more to
				// send. That is a five-minute timeout on the single most ordinary
				// path there is: the first sign-in to any 2FA-protected Apple ID.
				//
				// So the prompt is treated as the answer it actually is: this
				// account needs a code. Kill it and say so, and the UI shows its
				// second form. Killing here also means the caller sees ErrNeeds2FA
				// rather than a context deadline, which is the difference between
				// "enter your code" and "something timed out".
				if !prompted && authCode == "" && needsAuthCodePrompt(seen) {
					prompted = true
					mu.Lock()
					killedForAuthCode = true
					mu.Unlock()
					_ = cmd.Process.Kill()
				}
			}
			if err != nil {
				return
			}
		}
	}()

	if _, err := io.WriteString(ptmx, password+"\n"); err != nil {
		_ = cmd.Process.Kill()
		<-done
		return fmt.Errorf("ipatool: could not send the password: %w", err)
	}

	waitErr := cmd.Wait()
	_ = ptmx.Close()
	<-done

	mu.Lock()
	out := buf.String()
	killed := killedForAuthCode
	mu.Unlock()

	// BELT AND BRACES. If the password ever came back on the master — a terminal that ignored
	// the echo setting, a future ipatool that echoes it deliberately — it must not travel any
	// further. Everything downstream of this line is safe to log, wrap in an error, or send to
	// a browser.
	out = scrubSecret(out, password)

	// Logged BEFORE any early return, and that ordering is the point. It sat after the
	// `killed` branch, so the one path anybody actually needed to inspect — "it says 2FA and
	// no code arrived" — was the single path that logged nothing at all.
	if r.Debug != nil {
		r.Debug(sanitize(out))
	}

	// Checked BEFORE the exit status, because the exit status here is "killed" — an accurate
	// description of what this code did and a useless one for the user, who needs to be asked
	// for a code.
	if killed {
		return ErrNeeds2FA
	}

	if waitErr != nil {
		return classify(out, fmt.Errorf("ipatool: %w: %s", waitErr, strings.TrimSpace(sanitize(out))))
	}
	// ipatool exits 0 on a failed login and reports it in the output, so the exit code alone
	// would call a rejected password a success.
	if err := classify(out, nil); err != nil {
		return err
	}
	if strings.Contains(strings.ToLower(out), "success=false") {
		return fmt.Errorf("ipatool: %s", strings.TrimSpace(sanitize(lastMeaningfulLine(out))))
	}
	return nil
}

// disableEcho turns off terminal echo on the pty, best-effort.
func disableEcho(f *os.File) {
	var t unix.Termios
	raw, err := unix.IoctlGetTermios(int(f.Fd()), ioctlGetTermios)
	if err != nil {
		return
	}
	t = *raw
	t.Lflag &^= unix.ECHO
	_ = unix.IoctlSetTermios(int(f.Fd()), ioctlSetTermios, &t)
}

// scrubSecret removes a secret from text that is about to be shown to somebody.
func scrubSecret(s, secret string) string {
	// A short or empty secret would turn this into a find-and-replace on common substrings.
	if len(secret) < 4 {
		return s
	}
	return strings.ReplaceAll(s, secret, "[redacted]")
}

// ansiRE matches the SGR colour escapes ipatool's console writer emits.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// sanitize makes a tool's raw output fit to show a person.
//
// ipatool colours its output unconditionally — it does not check whether the destination is a
// terminal — so the escapes travel with the message and render in a browser as a row of empty
// boxes around the words that matter. Observed in the UI on a real failed login.
func sanitize(s string) string {
	return strings.TrimSpace(ansiRE.ReplaceAllString(s, ""))
}

func (r *Real) AuthInfo(ctx context.Context, home, passphrase string) (Account, error) {
	out, err := r.run(ctx, r.DeviceTimeout, ipatoolEnv(home), "",
		"ipatool", "auth", "info", "--keychain-passphrase", passphrase, "--non-interactive")
	if err != nil {
		return Account{}, err
	}
	return parseAuthInfo(out), nil
}

// parseAuthInfo pulls the identity out of ipatool's report. It accepts the JSON shape and the
// default console shape, because which one appears depends on a global flag whose default has
// moved between releases; matching both costs a few lines and removes a version dependency.
func parseAuthInfo(out string) Account {
	var acc Account
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "{") {
			var j struct {
				Email string `json:"email"`
				Name  string `json:"name"`
			}
			if json.Unmarshal([]byte(line), &j) == nil {
				if j.Email != "" {
					acc.Email = j.Email
				}
				if j.Name != "" {
					acc.Name = j.Name
				}
				continue
			}
		}
		if v, ok := logfmtValue(line, "email"); ok {
			acc.Email = v
		}
		if v, ok := logfmtValue(line, "name"); ok {
			acc.Name = v
		}
	}
	return acc
}

// logfmtValue reads `key=value` or `key="value with spaces"` out of a console log line.
func logfmtValue(line, key string) (string, bool) {
	i := strings.Index(line, key+"=")
	if i < 0 || (i > 0 && line[i-1] != ' ') {
		return "", false
	}
	rest := line[i+len(key)+1:]
	if strings.HasPrefix(rest, `"`) {
		rest = rest[1:]
		if j := strings.Index(rest, `"`); j >= 0 {
			return rest[:j], true
		}
		return rest, true
	}
	if j := strings.IndexByte(rest, ' '); j >= 0 {
		rest = rest[:j]
	}
	return rest, rest != ""
}

// Download fetches an owned app by numeric id, reporting progress as it goes.
//
// RUN UNDER A PTY, FOR THE PROGRESS. ipatool only draws its progress bar when it believes it is
// talking to a terminal; with --non-interactive it prints nothing at all until the download is
// over, which is what made a ~500 MB fetch a blank page for minutes. On a terminal it emits
// frames like:
//
//	downloading  99% |███████████...███ | (195/197 MB, 35 MB/s)
//
// separated by carriage returns. Captured from the real tool on the staging host.
//
// The same PTY trick as AuthLogin, for an unrelated reason — there it was the only way to pass a
// secret without argv, here it is the only way to see progress at all.
func (r *Real) Download(ctx context.Context, home, passphrase string, appID int64, outPath string, onProgress func(DownloadProgress)) (DownloadResult, error) {
	if onProgress == nil {
		return r.downloadPlain(ctx, home, passphrase, appID, outPath)
	}

	ctx, cancel := context.WithTimeout(ctx, r.DownloadTimeout)
	defer cancel()

	bin, err := resolveTool("ipatool")
	if err != nil {
		return DownloadResult{}, err
	}
	cmd := exec.CommandContext(ctx, bin, "download",
		"-i", fmt.Sprintf("%d", appID),
		"-o", outPath,
		"--keychain-passphrase", passphrase)
	// TERM matters: ipatool's progress library renders differently, or not at all, without
	// one. `xterm` is a safe, universally understood value.
	cmd.Env = append([]string{toolPATH, "TERM=xterm"}, ipatoolEnv(home)...)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("ipatool: cannot allocate a terminal: %w", err)
	}
	defer func() { _ = ptmx.Close() }()

	var mu sync.Mutex
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		chunk := make([]byte, 8192)
		var frame bytes.Buffer
		last := -1
		for {
			n, err := ptmx.Read(chunk)
			if n > 0 {
				mu.Lock()
				buf.Write(chunk[:n])
				mu.Unlock()

				// Frames are separated by CR, not LF — the bar redraws in place.
				for _, b := range chunk[:n] {
					if b == '\r' || b == '\n' {
						if p, ok := parseDownloadFrame(frame.String()); ok && p.Percent != last {
							last = p.Percent
							onProgress(p)
						}
						frame.Reset()
						continue
					}
					// A bar is thousands of block glyphs; cap the frame so a
					// missing CR cannot grow this without bound.
					if frame.Len() < 4096 {
						frame.WriteByte(b)
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// WATCH THE FILES, BECAUSE ipatool GOES QUIET FOR THE LAST STEP.
	//
	// Its download command is, from the source of the pinned version:
	//
	//	tmpPath := destination + ".tmp"
	//	downloadFile(item.URL, tmpPath, input.Progress)   // the progress bar lives here
	//	applyPatches(item, account, tmpPath, destination) // no progress at all
	//
	// applyPatches rewrites the WHOLE archive — every entry copied into a new zip, plus the
	// iTunesMetadata that makes the .ipa installable. On a 434 MB app that is most of a
	// gigabyte of I/O and about ten seconds, during which the bar sits frozen at whatever its
	// last frame said. Reported twice as "stuck at 99%", which is precisely what it looks like.
	//
	// There is nothing printed to parse, but the two files say everything: while the .tmp
	// exists and the destination is growing, the rewrite is running and its progress is the
	// ratio between them.
	stopWatch := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		tmpPath := outPath + ".tmp"
		// THE DESTINATION MAY ALREADY EXIST, and that is the case this has to survive: a
		// re-download to pick up a new version writes over an archive that is already there.
		// Comparing sizes naively would divide yesterday's full 434 MB by a .tmp that has
		// just started growing and announce "Signing 100%" for the whole transfer.
		//
		// So the rewrite is recognised by the destination CHANGING from how it was found,
		// rather than by it merely existing. Against a remembered stat, not against a
		// wall-clock instant: mtime granularity on a real filesystem is coarser than
		// time.Now(), so "modified after this moment" is false for a file written in the
		// same second — which a fast download would hit, and which cost this a test.
		before, beforeErr := os.Stat(outPath)
		rewriteBegun := func(dst os.FileInfo) bool {
			if beforeErr != nil {
				return true // it did not exist; its appearance IS the rewrite starting
			}
			return dst.Size() != before.Size() || !dst.ModTime().Equal(before.ModTime())
		}
		lastPct := -1
		for {
			select {
			case <-stopWatch:
				return
			case <-time.After(400 * time.Millisecond):
			}
			tmp, err := os.Stat(tmpPath)
			if err != nil || tmp.Size() == 0 {
				continue // not downloading yet, or finished and already cleaned up
			}
			dst, err := os.Stat(outPath)
			if err != nil || !rewriteBegun(dst) {
				continue // still transferring; the destination is untouched
			}
			pct := int(dst.Size() * 100 / tmp.Size())
			if pct > 100 {
				pct = 100
			}
			if pct != lastPct {
				lastPct = pct
				onProgress(DownloadProgress{Percent: pct, Stage: "signing"})
			}
		}
	}()

	waitErr := cmd.Wait()
	close(stopWatch)
	<-watchDone
	_ = ptmx.Close()
	<-done

	mu.Lock()
	out := buf.String()
	mu.Unlock()

	if waitErr != nil {
		return DownloadResult{Output: out}, classify(out, fmt.Errorf("ipatool: %w: %s", waitErr, sanitize(lastMeaningfulLine(stripBar(out)))))
	}
	if err := classify(out, nil); err != nil {
		return DownloadResult{Output: out}, err
	}
	return DownloadResult{Purchased: parsePurchased(out), Path: outPath, Output: out}, nil
}

func (r *Real) downloadPlain(ctx context.Context, home, passphrase string, appID int64, outPath string) (DownloadResult, error) {
	// -i, NOT -b. This is the single most important line in the spec: -b resolves a bundle id
	// by SEARCHING the store, and a delisted app is not in search, so -b fails with "app not
	// found" for exactly the apps springback exists to fetch. Measured both ways (SPEC §3).
	//
	// --purchase is ABSENT and must stay absent. It acquires a licence, which is a state
	// change on someone's Apple account, and springback never makes one without the user
	// having explicitly asked for it.
	out, err := r.run(ctx, r.DownloadTimeout, ipatoolEnv(home), "",
		"ipatool", "download",
		"-i", fmt.Sprintf("%d", appID),
		"-o", outPath,
		"--keychain-passphrase", passphrase,
		"--non-interactive")
	if err != nil {
		return DownloadResult{Output: out}, err
	}
	return DownloadResult{Purchased: parsePurchased(out), Path: outPath, Output: out}, nil
}

// lookupResponse is the slice of the iTunes lookup payload springback reads.
type lookupResponse struct {
	ResultCount int `json:"resultCount"`
	Results     []struct {
		TrackID     int64  `json:"trackId"`
		TrackName   string `json:"trackName"`
		Version     string `json:"version"`
		ReleaseDate string `json:"currentVersionReleaseDate"`
		BundleID    string `json:"bundleId"`
		// A STRING in Apple's JSON, not a number. Decoding it as int64 fails the whole
		// response, which would have taken the store version and the delisted verdict with it.
		FileSizeBytes string `json:"fileSizeBytes"`
		// The store's own artwork, which is the only picture some apps have: a device
		// returns a generic placeholder for an app it has not rendered.
		ArtworkURL512 string `json:"artworkUrl512"`
		ArtworkURL100 string `json:"artworkUrl100"`
	} `json:"results"`
	ErrorMessage string `json:"errorMessage"`
}

func (r *Real) Lookup(ctx context.Context, bundleID, country string) StoreLookup {
	res := StoreLookup{Country: country}

	u := "https://itunes.apple.com/lookup?" + url.Values{
		"bundleId": {bundleID},
		"country":  {country},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		res.Err = err
		return res
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		res.Err = err
		return res
	}
	defer func() { _ = resp.Body.Close() }()

	// A NON-200 IS "NOT CHECKED", NEVER "NOT IN THE STORE", and this is the guard that keeps
	// the headline feature honest. An unknown storefront code answers 400 — measured against
	// the live API on 2026-08-11 with country=ll, which is what the iPad's RegionInfo "LL/A"
	// naively yields. Counting that as resultCount 0 would mark every app on that iPad as
	// delisted, with the tool sounding most confident exactly where it was most wrong.
	if resp.StatusCode != http.StatusOK {
		res.Err = fmt.Errorf("itunes lookup %s: http %d", country, resp.StatusCode)
		return res
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		res.Err = err
		return res
	}
	var lr lookupResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		res.Err = fmt.Errorf("itunes lookup %s: %w", country, err)
		return res
	}
	if lr.ErrorMessage != "" {
		res.Err = fmt.Errorf("itunes lookup %s: %s", country, lr.ErrorMessage)
		return res
	}

	res.Checked = true
	if lr.ResultCount > 0 && len(lr.Results) > 0 {
		res.Found = true
		res.TrackID = lr.Results[0].TrackID
		res.TrackName = lr.Results[0].TrackName
		res.Version = lr.Results[0].Version
		res.ReleaseDate = lr.Results[0].ReleaseDate
		// Unparseable is simply zero: a missing size costs one row on a detail page, and
		// is not worth failing a lookup that decides whether an app counts as delisted.
		res.FileSize, _ = strconv.ParseInt(lr.Results[0].FileSizeBytes, 10, 64)
		// 512 for preference; 100 is the fallback for an old listing that has no large
		// artwork. Either beats the generic tile a device hands back for an app it has no
		// picture for.
		if res.ArtworkURL = lr.Results[0].ArtworkURL512; res.ArtworkURL == "" {
			res.ArtworkURL = lr.Results[0].ArtworkURL100
		}
	}
	return res
}

// resolveTool turns a tool's name into the absolute path of the file that will actually be run.
//
// IT EXISTS BECAUSE cmd.Env DOES NOT DECIDE THE LOOKUP. exec.Command resolves a bare name against
// THIS process's $PATH, immediately, before cmd.Env is ever consulted — so setting a PATH in the
// child's environment changes what the child sees and nothing at all about which file is executed.
//
// On Linux the difference was invisible: the image installs every tool where the inherited PATH
// already points, so both PATHs agreed by accident. In a macOS .app they do not agree — the tools
// sit beside the binary, in a directory only toolPATH knows about, and a launched app inherits
// whatever PATH the launcher had. The symptom is "executable file not found in $PATH" naming a
// file that is present, executable, and on the PATH the error just printed.
func resolveTool(name string) (string, error) {
	for _, dir := range strings.Split(strings.TrimPrefix(toolPATH, "PATH="), ":") {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: not found in %s", name, strings.TrimPrefix(toolPATH, "PATH="))
}
