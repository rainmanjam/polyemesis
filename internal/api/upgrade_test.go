package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/upgrade"
)

// Every case here goes through the REAL router -- s.Handler(), a real session,
// the real requireAuth/requireCSRF chain -- and asserts on bytes on disk rather
// than on the shape of a response. Issue #107 is open about tests that assert
// behaviour by grepping source text; three of those passed while a regression
// shipped. Nothing in this file reads a .go file.
//
// The install being upgraded is a file in a temp directory standing in for
// /usr/local/bin/polyemesis, and the release host is an httptest server. That
// is the whole fixture: internal/upgrade does not care what the bytes are, and
// neither does the handler.

const (
	installedBytes = "the version that is running"
	releasedBytes  = "the version on the release page"
	releaseTag     = "v9.9.9"
)

// fakeInstall writes a stand-in for the installed binary and returns the path
// the handler would be given plus the path internal/upgrade will actually
// touch.
//
// The two differ, and that is not pedantry: on macOS t.TempDir() sits under
// /var, which is a symlink to /private/var, and upgrade.resolve follows it
// before deciding where .previous goes. A test that assumed they were the same
// would look for the rollback point in a directory nothing wrote to.
func fakeInstall(t *testing.T) (path, resolved string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "polyemesis")
	if err := os.WriteFile(path, []byte(installedBytes), 0o755); err != nil {
		t.Fatalf("write fake install: %v", err)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve fake install: %v", err)
	}
	return path, real
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// artefactName is the name `make release` writes and scripts/install.sh reads,
// computed the same way the handler computes it -- for this platform, whatever
// the test is running on.
func artefactName(tag string) string {
	n := fmt.Sprintf("polyemesis-%s-%s-%s", tag, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		n += ".exe"
	}
	return n
}

// stubReleaseDownloads stands in for the GitHub release host and counts how
// many times it is reached, which is the only way to prove an endpoint that
// must not download does not download.
//
// sums is what SHA256SUMS will say the artefact hashes to; the empty string
// means "tell the truth about body".
func stubReleaseDownloads(t *testing.T, tag, body, sums string) *atomic.Int64 {
	t.Helper()
	if sums == "" {
		sum := sha256.Sum256([]byte(body))
		sums = hex.EncodeToString(sum[:])
	}
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/" + tag + "/SHA256SUMS":
			// Two columns and a dist/ prefix, as the release workflow's
			// `sha256sum *` produces and upgrade.ChecksumFor expects.
			fmt.Fprintf(w, "%s  dist/%s\n", sums, artefactName(tag))
		case "/" + tag + "/" + artefactName(tag):
			fmt.Fprint(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	previous := releaseDownloadBase
	releaseDownloadBase = srv.URL
	t.Cleanup(func() { releaseDownloadBase = previous })
	return &hits
}

// seedUpdateCache puts a tag in the process-wide cache the way a completed
// check would, without a check.
func seedUpdateCache(t *testing.T, tag string) {
	t.Helper()
	resetUpdateCache(t)
	updateCache.Lock()
	updateCache.latest = tag
	updateCache.url = "https://example.test/" + tag
	updateCache.Unlock()
}

// stubOnAir makes the on-air survey say whatever the case is about. Production
// runs the identical handler path; only what the survey answers changes.
func stubOnAir(t *testing.T, o engine.OnAir) {
	t.Helper()
	previous := surveyOnAir
	surveyOnAir = func(*Server) engine.OnAir { return o }
	t.Cleanup(func() { surveyOnAir = previous })
}

func liveBroadcast() engine.OnAir {
	return engine.OnAir{Publishers: 1, Destinations: 2, Names: []string{"Twitch", "YouTube"}}
}

// upgradeServer is a signed-in server whose "installed binary" is a temp file
// and whose install method is systemd -- the one method internal/upgrade will
// act on.
func upgradeServer(t *testing.T) (h http.Handler, sign func(*http.Request), path, resolved string) {
	t.Helper()
	s, h, _ := testServer(t, config.Config{})
	path, resolved = fakeInstall(t)
	s.upgradeMethod = upgrade.MethodSystemd
	s.execPath = path
	return h, login(t, h), path, resolved
}

func TestStagingOffAirReplacesTheBinaryAndOffersARollback(t *testing.T) {
	h, sign, path, resolved := upgradeServer(t)
	seedUpdateCache(t, releaseTag)
	stubReleaseDownloads(t, releaseTag, releasedBytes, "")
	stubOnAir(t, engine.OnAir{})

	var out upgradeResult
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag}, http.StatusOK), &out)

	if !out.Staged || !out.RestartRequired {
		t.Errorf("staged=%v restartRequired=%v, want both true: %+v", out.Staged, out.RestartRequired, out)
	}
	// It must never claim the upgrade has been APPLIED. Nothing is running the
	// new code until the unit restarts, and an operator told otherwise believes
	// they have fixed something they have only downloaded.
	if out.Command == "" {
		t.Error("no restart command in the response; the operator is told to restart with no idea how")
	}

	// The bytes, which is the only assertion that distinguishes a working
	// upgrade from a 200.
	if got := readFile(t, path); got != releasedBytes {
		t.Errorf("installed binary = %q, want %q", got, releasedBytes)
	}
	if got := readFile(t, upgrade.PreviousPath(resolved)); got != installedBytes {
		t.Errorf("rollback point = %q, want the version that was running (%q)", got, installedBytes)
	}

	// And the plan now offers the rollback, which is what the UI renders.
	var plan upgradePlanView
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/upgrade/plan", nil, http.StatusOK), &plan)
	if !plan.RollbackAvailable {
		t.Error("rollbackAvailable is false after a stage that wrote .previous")
	}
}

func TestRollbackRestoresTheVersionThatWasRunning(t *testing.T) {
	h, sign, path, resolved := upgradeServer(t)
	seedUpdateCache(t, releaseTag)
	stubReleaseDownloads(t, releaseTag, releasedBytes, "")
	stubOnAir(t, engine.OnAir{})

	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag}, http.StatusOK)

	var out upgradeResult
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/upgrade/rollback",
		upgradeAction{}, http.StatusOK), &out)
	if !out.RolledBack || !out.RestartRequired {
		t.Errorf("rolledBack=%v restartRequired=%v, want both true", out.RolledBack, out.RestartRequired)
	}
	if got := readFile(t, path); got != installedBytes {
		t.Errorf("binary after rollback = %q, want %q", got, installedBytes)
	}
	// A rollback taken by mistake has to be undoable, so the version that was
	// live a moment ago becomes the new rollback point.
	if got := readFile(t, upgrade.PreviousPath(resolved)); got != releasedBytes {
		t.Errorf("rollback point after rollback = %q, want %q", got, releasedBytes)
	}
}

func TestRollbackIsRefusedWhenThereIsNothingToRollBackTo(t *testing.T) {
	h, sign, path, _ := upgradeServer(t)
	seedUpdateCache(t, releaseTag)
	stubOnAir(t, engine.OnAir{})

	msg := mustJSONError(t, h, sign, http.MethodPost, "/api/v1/upgrade/rollback",
		upgradeAction{}, http.StatusConflict)
	if msg == "" {
		t.Error("refused with no sentence")
	}
	if got := readFile(t, path); got != installedBytes {
		t.Errorf("a refused rollback changed the binary to %q", got)
	}
}

func TestStagingIsRefusedWhileSomethingIsOnAirAndForceIsTheWayPast(t *testing.T) {
	h, sign, path, _ := upgradeServer(t)
	seedUpdateCache(t, releaseTag)
	stubReleaseDownloads(t, releaseTag, releasedBytes, "")
	stubOnAir(t, liveBroadcast())

	raw := send(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag}, http.StatusConflict)
	var refusal upgradeRefusal
	decodeInto(t, raw, &refusal)
	if refusal.Error == "" {
		t.Error("the on-air refusal carried no sentence; the SPA would show 'request failed (409)'")
	}
	if refusal.OnAirSummary == "" {
		t.Error("the refusal did not say what is live, which is the whole point of the gate")
	}
	if got := readFile(t, path); got != installedBytes {
		t.Fatalf("a refused stage replaced the binary with %q", got)
	}

	// The identical request with the explicit override goes through. Without
	// this half the test would pass on a handler that refuses everything.
	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag, Force: true}, http.StatusOK)
	if got := readFile(t, path); got != releasedBytes {
		t.Errorf("forced stage left the binary at %q", got)
	}
}

func TestRollbackIsRefusedWhileSomethingIsOnAirAndForceIsTheWayPast(t *testing.T) {
	h, sign, path, _ := upgradeServer(t)
	seedUpdateCache(t, releaseTag)
	stubReleaseDownloads(t, releaseTag, releasedBytes, "")
	stubOnAir(t, engine.OnAir{})

	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag}, http.StatusOK)

	stubOnAir(t, liveBroadcast())
	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/rollback", upgradeAction{}, http.StatusConflict)
	if got := readFile(t, path); got != releasedBytes {
		t.Fatalf("a refused rollback changed the binary to %q", got)
	}
	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/rollback",
		upgradeAction{Force: true}, http.StatusOK)
	if got := readFile(t, path); got != installedBytes {
		t.Errorf("forced rollback left the binary at %q", got)
	}
}

func TestABadChecksumLeavesTheInstallAndItsRollbackPointAlone(t *testing.T) {
	h, sign, path, resolved := upgradeServer(t)
	seedUpdateCache(t, releaseTag)
	stubOnAir(t, engine.OnAir{})

	// A good upgrade first, so there is a rollback point with something worth
	// losing in it.
	stubReleaseDownloads(t, releaseTag, releasedBytes, "")
	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag}, http.StatusOK)

	// Now a release whose published checksum does not describe what arrives --
	// a tampered mirror, a truncated download, a mixed-up SHA256SUMS.
	stubReleaseDownloads(t, releaseTag, "not what the checksum says",
		"0000000000000000000000000000000000000000000000000000000000000000")
	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag}, http.StatusInternalServerError)

	if got := readFile(t, path); got != releasedBytes {
		t.Errorf("a failed checksum changed the live binary to %q", got)
	}
	if got := readFile(t, upgrade.PreviousPath(resolved)); got != installedBytes {
		t.Errorf("a failed checksum disturbed the rollback point, now %q", got)
	}
	// And nothing was left behind for the next upgrade to trip over.
	entries, err := os.ReadDir(filepath.Dir(resolved))
	if err != nil {
		t.Fatalf("read install dir: %v", err)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("install directory holds %v, want just the binary and its .previous", names)
	}
}

func TestAReleaseWithNoPublishedChecksumIsRefused(t *testing.T) {
	h, sign, path, _ := upgradeServer(t)
	seedUpdateCache(t, releaseTag)
	stubOnAir(t, engine.OnAir{})
	// SHA256SUMS exists but names some other platform's artefact, which is what
	// a partial release upload looks like. install.sh warns and proceeds; a
	// server reachable from a browser must not.
	stubReleaseDownloads(t, releaseTag, releasedBytes, "")
	previous := releaseDownloadBase
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  dist/polyemesis-%s-plan9-mips\n", "00", releaseTag)
	}))
	t.Cleanup(srv.Close)
	releaseDownloadBase = srv.URL
	t.Cleanup(func() { releaseDownloadBase = previous })

	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag}, http.StatusBadGateway)
	if got := readFile(t, path); got != installedBytes {
		t.Errorf("an unchecksummed release was installed anyway: %q", got)
	}
}

func TestInstallsThatCannotUpgradeThemselvesAreToldWhatToRun(t *testing.T) {
	cases := []struct {
		name   string
		method upgrade.Method
		// unwritable points the install at a directory this process cannot
		// create files in, which is the STOCK systemd install:
		// deploy/polyemesis.service sets ProtectSystem=strict with
		// ReadWritePaths=/var/lib/polyemesis, so /usr/local/bin is read-only to
		// the service. The refusal is the feature working.
		unwritable bool
	}{
		{name: "docker", method: upgrade.MethodDocker},
		{name: "manual", method: upgrade.MethodManual},
		{name: "systemd with an install directory it cannot write", method: upgrade.MethodSystemd, unwritable: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, h, _ := testServer(t, config.Config{})
			path, _ := fakeInstall(t)
			s.upgradeMethod = c.method
			s.execPath = path
			if c.unwritable {
				// A directory that is not there, rather than
				// os.Chmod(dir, 0o500). Mode bits are not a portable way to say
				// "this process cannot create a file here" and this case was
				// built on them:
				//
				//   - On Windows syscall.Chmod reads no bit but S_IWRITE and
				//     only toggles FILE_ATTRIBUTE_READONLY, which NTFS ignores
				//     when a file is created INSIDE a directory. The chmod
				//     succeeded, the directory stayed writable, and this case
				//     silently installed the release it was written to prove
				//     gets refused.
				//   - As uid 0 -- any container-based runner -- 0o500 does not
				//     stop root either, so the same hole is one CI change away
				//     on Linux.
				//
				// Nobody can create a file in a directory that does not exist,
				// on any OS as any user, which is the idiom
				// upgrade.TestSystemdRefusesAnUnwritableDirectory already uses.
				s.execPath = filepath.Join(filepath.Dir(path), "read-only", filepath.Base(path))
			}
			sign := login(t, h)
			seedUpdateCache(t, releaseTag)
			stubOnAir(t, engine.OnAir{})
			hits := stubReleaseDownloads(t, releaseTag, releasedBytes, "")

			// THE PREMISE, ASSERTED RATHER THAN ASSUMED. Every assertion below
			// is about what the server does with an install it cannot upgrade,
			// and all of them pass vacuously against an install it CAN. When
			// the precondition stopped holding on windows-latest this case did
			// not go quiet, it went green-then-red for a reason nobody could
			// read off the failure. Ask the product itself, before asking the
			// endpoint.
			if plan := s.upgradePlan(releaseTag); plan.Automatic {
				t.Fatalf("precondition not built: this install still reports as automatically "+
					"upgradable (%+v); the rest of this case would prove nothing", plan.Plan)
			}

			raw := send(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
				upgradeAction{Version: releaseTag}, http.StatusConflict)
			var refusal upgradeRefusal
			decodeInto(t, raw, &refusal)
			if refusal.Automatic {
				t.Fatal("reported automatic for an install that is not")
			}
			// The operator has to leave with something they can act on, and
			// WHICH of the two depends on why this install was refused: a
			// method that upgrades elsewhere gets the command to run, a systemd
			// install that cannot write its own directory gets that directory
			// named, because "upgrade with sudo" is useless without it.
			switch {
			case c.unwritable:
				if refusal.Command != "" {
					t.Errorf("offered a command for an install that has no working one: %q", refusal.Command)
				}
				if !strings.Contains(refusal.Reason, filepath.Dir(s.execPath)) {
					t.Errorf("the reason does not name the directory that cannot be written: %q", refusal.Reason)
				}
			default:
				if refusal.Command == "" {
					t.Error("refused with no command for an install method that has one; a dead end")
				}
			}
			if refusal.Error == "" {
				t.Error("refused with no sentence")
			}
			if n := hits.Load(); n != 0 {
				t.Errorf("release host reached %d times by a refused upgrade, want 0", n)
			}
			if got := readFile(t, path); got != installedBytes {
				t.Errorf("a refused upgrade changed the binary to %q", got)
			}
		})
	}
}

func TestStagingRefusesAVersionTheServerIsNotOffering(t *testing.T) {
	h, sign, path, _ := upgradeServer(t)
	seedUpdateCache(t, releaseTag)
	hits := stubReleaseDownloads(t, releaseTag, releasedBytes, "")
	stubOnAir(t, engine.OnAir{})

	// The banner said v9.9.8; a check has since found v9.9.9. The operator read
	// the notes for a release the server would not be installing, so it stops
	// rather than downloading whichever of the two it prefers.
	msg := mustJSONError(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: "v9.9.8"}, http.StatusConflict)
	if msg == "" {
		t.Error("refused with no sentence")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("release host reached %d times on a version mismatch, want 0", n)
	}
	if got := readFile(t, path); got != installedBytes {
		t.Errorf("binary changed on a version mismatch: %q", got)
	}
}

func TestStagingRefusesWhenNoCheckHasEverRun(t *testing.T) {
	h, sign, path, _ := upgradeServer(t)
	resetUpdateCache(t)
	stubOnAir(t, engine.OnAir{})
	hits := stubReleaseDownloads(t, releaseTag, releasedBytes, "")

	mustJSONError(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag}, http.StatusConflict)
	if n := hits.Load(); n != 0 {
		t.Errorf("release host reached %d times with no known release, want 0", n)
	}
	if got := readFile(t, path); got != installedBytes {
		t.Errorf("binary changed with no known release: %q", got)
	}
}

// The security property that motivated calling requireSession explicitly. The
// three upgrade routes sit inside the requireCSRF group, and requireCSRF passes
// a token-authenticated request straight through -- so without the explicit
// gate a leaked API token could replace the server's own binary.
func TestAnAPITokenCannotStageOrRollBack(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	path, _ := fakeInstall(t)
	s.upgradeMethod = upgrade.MethodSystemd
	s.execPath = path
	sign := login(t, h)
	plaintext := createToken(t, h, sign, "ci runner")

	seedUpdateCache(t, releaseTag)
	stubOnAir(t, engine.OnAir{})
	hits := stubReleaseDownloads(t, releaseTag, releasedBytes, "")

	for _, p := range []string{"/api/v1/upgrade/stage", "/api/v1/upgrade/rollback"} {
		t.Run(p, func(t *testing.T) {
			r := jsonRequest(t, http.MethodPost, p, upgradeAction{Version: releaseTag, Force: true})
			r.Header.Set("Authorization", "Bearer "+plaintext)
			w := do(t, h, r)
			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403: a token got past the session gate", w.Code)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("release host reached %d times by a token, want 0", n)
	}
	if got := readFile(t, path); got != installedBytes {
		t.Errorf("a token-authenticated request changed the binary to %q", got)
	}

	// The read is allowed: a token is for automation, and "is there an upgrade,
	// and could this box take it" is exactly what automation asks.
	r := jsonRequest(t, http.MethodGet, "/api/v1/upgrade/plan", nil)
	r.Header.Set("Authorization", "Bearer "+plaintext)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Errorf("plan status for a token = %d, want 200", w.Code)
	}
}

func TestThePlanEndpointNeverLeavesTheBox(t *testing.T) {
	h, sign, path, _ := upgradeServer(t)
	seedUpdateCache(t, releaseTag)
	feed := stubReleaseFeed(t, releaseJSON(releaseTag, "https://example.test/9"))
	downloads := stubReleaseDownloads(t, releaseTag, releasedBytes, "")
	stubOnAir(t, liveBroadcast())

	var plan upgradePlanView
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/upgrade/plan", nil, http.StatusOK), &plan)

	if !plan.Automatic {
		t.Errorf("plan is not automatic for a writable systemd install: %+v", plan)
	}
	if plan.Version != releaseTag {
		t.Errorf("plan version = %q, want the tag the last check found (%q)", plan.Version, releaseTag)
	}
	if plan.OnAirSummary == "" {
		t.Error("the plan did not report what is live; the UI has nothing to warn with")
	}
	if n := feed.Load(); n != 0 {
		t.Errorf("release feed reached %d times, want 0: asking what could be done must not phone home", n)
	}
	if n := downloads.Load(); n != 0 {
		t.Errorf("release host reached %d times, want 0", n)
	}
	if got := readFile(t, path); got != installedBytes {
		t.Errorf("a plan request changed the binary to %q", got)
	}
}

// Nothing in this package can create a temp file in the install directory of a
// server that never established one, so a zero-valued Server must refuse rather
// than act on an empty path.
func TestAServerThatCannotIdentifyItsInstallRefuses(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	s.upgradeMethod = ""
	s.execPath = ""
	sign := login(t, h)
	seedUpdateCache(t, releaseTag)
	stubOnAir(t, engine.OnAir{})

	var plan upgradePlanView
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/upgrade/plan", nil, http.StatusOK), &plan)
	if plan.Automatic || plan.Reason == "" {
		t.Errorf("an unidentified install reported automatic=%v reason=%q", plan.Automatic, plan.Reason)
	}
	mustJSONError(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag}, http.StatusConflict)
}

// A redirect may not change the transport. releaseDownloadBase is https in
// production and GitHub redirects release downloads to its asset host, so the
// hop itself is normal -- what must not happen is landing on a different
// scheme, which is the guarantee scripts/install.sh buys with curl's
// --proto-redir '=https'.
//
// THE REDIRECT TARGET HERE SERVES A PERFECTLY GOOD RELEASE, and that is the
// whole design of this case. The first version of it redirected to a dead port,
// which meant the request failed whether the policy was there or not: it passed
// with the CheckRedirect body deleted, measured. The only way to tell a refusal
// apart from a failure is to make everything except the refusal work.
func TestADownloadWillNotFollowARedirectToAnotherScheme(t *testing.T) {
	h, sign, path, _ := upgradeServer(t)
	seedUpdateCache(t, releaseTag)
	stubOnAir(t, engine.OnAir{})

	// The destination: a working release host on the OTHER scheme.
	sum := sha256.Sum256([]byte(releasedBytes))
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + releaseTag + "/SHA256SUMS":
			fmt.Fprintf(w, "%s  dist/%s\n", hex.EncodeToString(sum[:]), artefactName(releaseTag))
		case "/" + releaseTag + "/" + artefactName(releaseTag):
			fmt.Fprint(w, releasedBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(elsewhere.Close)

	// The client keeps PRODUCTION's redirect policy and gains only the trust
	// needed to reach a self-signed test certificate -- so what is exercised is
	// the policy itself and not a TLS failure standing in for it.
	client := elsewhere.Client()
	previousClient := upgradeHTTPClient
	client.CheckRedirect = previousClient.CheckRedirect
	client.Timeout = previousClient.Timeout
	upgradeHTTPClient = client
	t.Cleanup(func() { upgradeHTTPClient = previousClient })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	previous := releaseDownloadBase
	releaseDownloadBase = srv.URL
	t.Cleanup(func() { releaseDownloadBase = previous })

	send(t, h, sign, http.MethodPost, "/api/v1/upgrade/stage",
		upgradeAction{Version: releaseTag}, http.StatusBadGateway)
	if got := readFile(t, path); got != installedBytes {
		t.Errorf("binary changed after a refused redirect: %q", got)
	}
}

// The three routes must be registered, and a status alone does not prove that:
// api.go registers the SPA as the root mux's NotFound handler, so an unrouted
// /api/v1/... path is answered either way. See mustJSONError.
func TestTheUpgradeRoutesAreRegisteredAndAuthenticated(t *testing.T) {
	h, _, _, _ := upgradeServer(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/upgrade/plan"},
		{http.MethodPost, "/api/v1/upgrade/stage"},
		{http.MethodPost, "/api/v1/upgrade/rollback"},
	} {
		t.Run(c.path, func(t *testing.T) {
			w := do(t, h, jsonRequest(t, c.method, c.path, nil))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 for an unauthenticated caller", w.Code)
			}
			var out map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatalf("body is not JSON (%v); the SPA fallback answered instead of a handler: %.80s",
					err, w.Body.String())
			}
		})
	}
}
