package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/upgrade"
)

// The three endpoints that let an operator act on an available release.
//
// internal/upgrade has always had the mechanics -- detect the install method,
// verify a checksum, swap the binary crash-safely, keep the old one for a
// rollback -- and until this file nothing in the product called it (issue #145).
// These handlers are the callers. They add the two things the package
// deliberately refuses to decide for itself: WHO may ask, and WHETHER now is a
// moment at which a restart is acceptable.
//
// NOTHING HERE RESTARTS ANYTHING, and that is not a gap. The staged binary
// becomes the running one when systemd starts the unit again, which is a
// deliberate act by somebody who knows what is on air. The unit runs
// NoNewPrivileges=true as an unprivileged user (deploy/polyemesis.service), so
// this process could not run `systemctl restart` even if it wanted to; the
// response says the command instead of pretending.

// releaseDownloadBase is where release artefacts are fetched from. A var so a
// test can point it at a local server, exactly as updateFeedURL is (see
// handlers.go); nothing reads it until an operator asks to stage an upgrade.
var releaseDownloadBase = "https://github.com/rainmanjam/polyemesis/releases/download"

// upgradeMu serialises the two mutating endpoints against each other.
//
// internal/upgrade is crash-safe against being KILLED, which is a different
// property from being safe against being run twice at once: two concurrent
// stages both copy the outgoing binary aside, and the second one's copy is the
// FIRST one's new binary, so .previous ends up holding the version that was
// just installed and the rollback goes nowhere. Two open browser tabs are
// enough to produce that, so the requests are serialised rather than the risk
// being documented.
//
// Package scope, like updateCache and for the same reason: there is one server
// per process and this is the only state the feature has.
var upgradeMu sync.Mutex

// surveyOnAir asks what a restart would interrupt, right now.
//
// A var because s.mgr is a concrete *engine.Manager whose OnAir answer comes
// from supervised FFmpeg children, and standing one up inside an api test means
// starting real processes. The seam mirrors stubReleaseFeed's treatment of
// updateFeedURL: production runs the identical handler path, and a test can
// make the survey say "a broadcast is live" without a broadcast.
//
// A nil manager reports nothing on air. That is right rather than merely
// convenient -- a server with no engine has nothing to interrupt.
var surveyOnAir = func(s *Server) engine.OnAir {
	if s.mgr == nil {
		return engine.OnAir{}
	}
	return s.mgr.OnAir()
}

// upgradeDownloadTimeout bounds the whole fetch: SHA256SUMS plus one binary of
// forty-odd megabytes, over whatever connection a self-hosted box has. Far
// longer than updateCheckTimeout because this is an action an operator is
// waiting on deliberately, not a background convenience -- but bounded, because
// a handler that never returns holds a connection until the client gives up.
const upgradeDownloadTimeout = 10 * time.Minute

// maxArtefactBytes caps what will be written to disk from the release host.
// Generous against the real artefact and still a bound, so a hijacked or
// mis-redirected download cannot fill the install disk before the checksum gets
// a chance to reject it.
const maxArtefactBytes = 512 << 20

var upgradeHTTPClient = &http.Client{
	Timeout: upgradeDownloadTimeout,
	// A redirect must not downgrade the transport. releaseDownloadBase is
	// https and GitHub redirects release downloads to its asset host, so the
	// hop is normal and the scheme it lands on is the thing worth pinning --
	// the same guarantee scripts/install.sh buys with curl's
	// --proto-redir '=https'.
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		// Compared against the FIRST request's scheme rather than against
		// "https" outright, so a test pointing the base at a local http server
		// is still a test of the real policy: a redirect may not change the
		// transport, whatever it started as.
		if len(via) > 0 && req.URL.Scheme != via[0].URL.Scheme {
			return fmt.Errorf("refusing a redirect from %s to %s", via[0].URL.Scheme, req.URL.Scheme)
		}
		return nil
	},
}

// upgradePlanView is the plan plus what a restart would cost right now.
//
// The two travel together for the same reason versionInfo carries OnAir: an
// operator deciding whether to upgrade is answering one question, and two round
// trips would let a UI render an enabled button while a broadcast was starting
// between them.
type upgradePlanView struct {
	upgrade.Plan
	// Version is the release this plan is about -- the tag the last check
	// found, which is also the tag the Command strings name. Empty when no
	// check has run.
	Version      string       `json:"version,omitempty"`
	OnAir        engine.OnAir `json:"onAir"`
	OnAirSummary string       `json:"onAirSummary,omitempty"`
}

// upgradeRefusal is a 409 that carries BOTH the sentence and the plan it came
// from.
//
// The sentence, because internal/api's whole error surface is {"error": ...}
// and the SPA's fetch wrapper reads that field and nothing else -- a refusal
// that omitted it would reach an operator as "request failed (409)". The plan,
// because a caller that is not the SPA -- a terminal, a script -- gets the
// Command and the Reason in the same response rather than having to go and ask
// for them.
type upgradeRefusal struct {
	upgradePlanView
	Error string `json:"error"`
}

// refuse answers with the plan and a sentence, never one without the other.
func refuse(w http.ResponseWriter, plan upgradePlanView, msg string) {
	writeJSON(w, http.StatusConflict, upgradeRefusal{upgradePlanView: plan, Error: msg})
}

// notAutomatic is why this box cannot upgrade itself, in the words
// internal/upgrade chose.
//
// Reason and Command are alternatives there rather than a pair: a Docker or
// manual install carries a Command and no Reason, because there is nothing
// wrong -- that is simply how those installs are upgraded. A systemd install
// that cannot write its own directory carries a Reason. Verbatim in both cases;
// paraphrasing would produce a second wording that drifts from the one the
// terminal shows.
func notAutomatic(plan upgradePlanView) string {
	if plan.Reason != "" {
		return plan.Reason
	}
	if plan.Command != "" {
		return "this install is not upgraded from here; run: " + plan.Command
	}
	return "this install cannot be upgraded automatically"
}

// onAirRefusal is the wording for "something is live". The SERVER owns it, for
// the reason versionInfo's OnAirSummary field already states: the same refusal
// has to reach a terminal as well as a browser, and two phrasings is how they
// come to disagree.
//
// SWAPPING THE BINARY DOES NOT ITSELF INTERRUPT ANYTHING -- the install is a
// rename and the running process keeps executing the inode it already opened.
// The gate is about what follows. Applying the change needs a restart, which
// drops every destination mid-stream; and in the window before that restart,
// the unit's Restart=on-failure means an unrelated crash brings the service
// back on a version nobody chose, in the middle of a broadcast. Both are
// reasons to pick the moment deliberately.
func onAirRefusal(plan upgradePlanView) string {
	return fmt.Sprintf("not now: %s. Applying this needs a restart, which would interrupt that, "+
		"and until that restart an unrelated crash would bring the service back on the other "+
		"version mid-broadcast. Stop the broadcast, or send force to do it anyway", plan.OnAirSummary)
}

// upgradeAction is the body both mutating endpoints accept.
type upgradeAction struct {
	// Version CONFIRMS the release the operator was looking at; it does not
	// choose one. The server downloads whatever the last check found, and a
	// mismatch means the banner is describing a release that is no longer the
	// latest -- the operator is asked to look again rather than being handed a
	// version they did not read the notes for. Letting the body pick the tag
	// would make this endpoint a "download and install any string you like"
	// primitive, which is not what a session should imply.
	Version string `json:"version"`
	// Force is the explicit, audited override of the on-air refusal. There is
	// no config flag for it: interrupting a live broadcast should cost a
	// deliberate click every single time.
	Force bool `json:"force"`
}

// handleUpgradePlan reports what could be done about an available release.
//
// ON DEMAND ONLY, and deliberately not folded into /version. upgrade.PlanFor
// establishes writability by CREATING A TEMPORARY FILE in the install directory
// (see upgrade.writable, which explains why checking the mode bits answers a
// different question). The update banner reads /version on every page load, so
// folding this in would mean a file created and unlinked in /usr/local/bin on
// every load of every page, forever, to answer a question nobody asked.
//
// It never touches the network either. Everything it reports comes from this
// box and from the cache the operator's last check filled.
func (s *Server) handleUpgradePlan(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.upgradePlan(cachedLatestTag()))
}

func (s *Server) upgradePlan(tag string) upgradePlanView {
	v := upgradePlanView{
		Plan:    upgrade.PlanFor(s.upgradeMethod, s.execPath, tag),
		Version: tag,
		OnAir:   surveyOnAir(s),
	}
	v.OnAirSummary = v.OnAir.Summary()
	return v
}

// cachedLatestTag is the tag the last check found, or "" if none has run. Read
// under updateCache's lock because two tabs will race it.
func cachedLatestTag() string {
	updateCache.Lock()
	defer updateCache.Unlock()
	if updateCache.failed {
		return ""
	}
	return updateCache.latest
}

// handleUpgradeStage downloads, verifies and installs the release the last
// check found, without restarting anything.
func (s *Server) handleUpgradeStage(w http.ResponseWriter, r *http.Request) {
	// FIRST, and not left to the route group. requireCSRF waves a
	// token-authenticated request through -- correctly, because nothing
	// attaches an Authorization header on its own -- so membership of the
	// authenticated group is not the property this endpoint needs. Replacing
	// the server's binary is at least as grave as minting a token, and token
	// minting is behind the password for a reason requireSession states.
	if !requireSession(w, r) {
		return
	}
	var body upgradeAction
	if !decodeUpgradeAction(w, r, &body) {
		return
	}

	upgradeMu.Lock()
	defer upgradeMu.Unlock()

	tag := cachedLatestTag()
	if tag == "" {
		writeError(w, http.StatusConflict, "no release has been checked for yet; run a check first")
		return
	}
	// Recomputed here rather than trusted from whatever the UI last read: the
	// directory could have been remounted read-only, or a rollback point could
	// have appeared, since the operator opened the page.
	plan := s.upgradePlan(tag)
	if !plan.Automatic {
		refuse(w, plan, notAutomatic(plan))
		return
	}
	if body.Version != tag {
		// Before the on-air gate on purpose. A stale page describes a release
		// the operator read the notes for and the server no longer offers, and
		// that must not be able to reach the force override and end a broadcast
		// for a version nobody chose.
		refuse(w, plan, fmt.Sprintf(
			"the available release is %s, not %s; re-check and read the notes again", tag, body.Version))
		return
	}
	if plan.OnAir.Busy() && !body.Force {
		refuse(w, plan, onAirRefusal(plan))
		return
	}

	staged, sum, err := downloadRelease(r.Context(), tag)
	if err != nil {
		// The install is untouched: nothing is moved until Stage has a
		// verified file, which is the ordering upgrade.Stage documents.
		s.log.Warn("upgrade download failed", "version", tag, "err", err)
		writeError(w, http.StatusBadGateway, "the release could not be downloaded: "+err.Error())
		return
	}
	defer os.RemoveAll(filepath.Dir(staged))

	if err := upgrade.Stage(s.execPath, staged, sum); err != nil {
		s.log.Error("upgrade failed", "version", tag, "err", err)
		// 500 rather than 409: a checksum mismatch and a failed rename are both
		// "the server could not do this", and upgrade.Stage's own message says
		// which -- including, on the one half-failure it can produce, where the
		// previous binary was left.
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("upgrade staged",
		"version", tag, "binary", plan.BinaryPath,
		"forced", body.Force, "onAir", plan.OnAirSummary,
		"by", actorFor(r), "ip", s.clientIP(r))

	writeJSON(w, http.StatusOK, upgradeResult{
		Staged:          true,
		Version:         tag,
		RestartRequired: true,
		Command:         restartCommand,
	})
}

// handleUpgradeRollback puts the previous binary back, without restarting.
func (s *Server) handleUpgradeRollback(w http.ResponseWriter, r *http.Request) {
	if !requireSession(w, r) {
		return
	}
	var body upgradeAction
	if !decodeUpgradeAction(w, r, &body) {
		return
	}

	upgradeMu.Lock()
	defer upgradeMu.Unlock()

	plan := s.upgradePlan(cachedLatestTag())
	if !plan.Automatic {
		refuse(w, plan, notAutomatic(plan))
		return
	}
	if !plan.RollbackAvailable {
		refuse(w, plan, "there is no previous binary to roll back to")
		return
	}
	// No version confirmation here, and there is nothing to confirm: a rollback
	// names no release. It restores the binary this box was running before the
	// last stage, whatever that was.
	if plan.OnAir.Busy() && !body.Force {
		refuse(w, plan, onAirRefusal(plan))
		return
	}

	if err := upgrade.Rollback(s.execPath); err != nil {
		s.log.Error("rollback failed", "err", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("upgrade rolled back",
		"binary", plan.BinaryPath, "forced", body.Force, "onAir", plan.OnAirSummary,
		"by", actorFor(r), "ip", s.clientIP(r))

	writeJSON(w, http.StatusOK, upgradeResult{
		RolledBack:      true,
		RestartRequired: true,
		Command:         restartCommand,
	})
}

// restartCommand is what an operator runs to actually move onto the staged
// binary. Named once so the two responses cannot come to disagree.
const restartCommand = "sudo systemctl restart polyemesis"

// upgradeResult never says "updated". Nothing is running the new code until the
// unit restarts, and a message that implied otherwise would leave an operator
// believing they had applied a fix they had only downloaded.
type upgradeResult struct {
	Staged          bool   `json:"staged,omitempty"`
	RolledBack      bool   `json:"rolledBack,omitempty"`
	Version         string `json:"version,omitempty"`
	RestartRequired bool   `json:"restartRequired"`
	Command         string `json:"command"`
}

// actorFor names who asked, for the log line. Always a session here --
// requireSession has already run -- so this is the username, and the empty
// string only if the principal vanished between the two, which cannot happen.
func actorFor(r *http.Request) string {
	if p, ok := principalFrom(r.Context()); ok {
		return p.username
	}
	return ""
}

// decodeUpgradeAction decodes the action body, tolerating an empty one.
//
// decodeJSON refuses an empty body, and for these two endpoints that would be
// pedantry: `POST /upgrade/rollback` with nothing to say is a perfectly
// well-formed request meaning "roll back, and no I am not forcing it". Anything
// that is not empty is still decoded strictly, unknown fields and all, so a
// misspelt "forced" cannot silently mean "not forced".
func decodeUpgradeAction(w http.ResponseWriter, r *http.Request, v *upgradeAction) bool {
	raw, ok := readJSONBody(w, r)
	if !ok {
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	if err := decodeJSONInto(raw, v); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

// downloadRelease fetches SHA256SUMS and this platform's binary for tag, and
// returns the path it was written to with the checksum it must match.
//
// The artefact NAME is built here rather than read from anywhere: it is the
// convention `make release` writes and scripts/install.sh already reads
// (polyemesis-<tag>-<goos>-<goarch>, plus .exe on Windows), and the checksum is
// looked up by that name in the release's own SHA256SUMS. Verification is not
// optional -- see upgrade.Verify on why an unverified self-updater on a box with
// a public ingest port is a real attack surface.
//
// Nothing is verified HERE, deliberately. The checksum travels to upgrade.Stage,
// which hashes the bytes as it copies them into the install directory, so the
// file that is hashed is necessarily the file that is installed. Checking it
// here as well would only add a second, weaker check on a path an attacker with
// write access to the temp directory could still swap afterwards.
func downloadRelease(ctx context.Context, tag string) (path, sum string, err error) {
	artefact := fmt.Sprintf("polyemesis-%s-%s-%s", tag, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		artefact += ".exe"
	}

	sums, err := fetchRelease(ctx, tag, "SHA256SUMS", 1<<20)
	if err != nil {
		return "", "", err
	}
	want, err := upgrade.ChecksumFor(string(sums), artefact)
	if err != nil {
		// A release with no checksum for this platform is one this box must not
		// install. scripts/install.sh warns and proceeds; a server that can be
		// upgraded over the network from a browser does not get that latitude.
		return "", "", err
	}

	body, err := fetchRelease(ctx, tag, artefact, maxArtefactBytes)
	if err != nil {
		return "", "", err
	}
	// Its own directory so the deferred cleanup can remove the file whatever
	// name it ended up with, and under the process temp dir -- which the unit
	// gives us privately (PrivateTmp=true), so no other account on the box can
	// reach the download between here and the install.
	dir, err := os.MkdirTemp("", "polyemesis-upgrade-")
	if err != nil {
		return "", "", err
	}
	// 0o600: this is an unverified download until Stage says otherwise, and it
	// must never be executable by anybody in the meantime.
	path = filepath.Join(dir, artefact)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		os.RemoveAll(dir)
		return "", "", err
	}
	return path, want, nil
}

// fetchRelease reads one file out of a release, with a cap on what it will hold.
func fetchRelease(ctx context.Context, tag, name string, limit int64) ([]byte, error) {
	// Escaped rather than concatenated. The tag comes from GitHub's own feed
	// and the name is built from constants, so neither is caller-controlled
	// today -- but a path segment assembled by hand is exactly the thing that
	// stops being safe when somebody later lets a body choose the version.
	u := fmt.Sprintf("%s/%s/%s", releaseDownloadBase, url.PathEscape(tag), url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "polyemesis")

	resp, err := upgradeHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", name, resp.Status)
	}
	// LimitReader plus one byte, so a body that is exactly at the cap is told
	// apart from one that was truncated at it. Silently installing the first
	// 512 MB of something larger is how a corrupt binary reaches a checksum
	// that then fails for a reason nobody can explain.
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, limit)
	}
	return b, nil
}
