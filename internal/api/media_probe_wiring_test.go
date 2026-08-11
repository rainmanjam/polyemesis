package api

// ISSUE #183. The PRODUCTION route to ffprobe, which nothing exercised.
//
// media_probe_test.go proves the upload gate rejects a PDF, and it does so
// through Server.probeBin -- a seam that exists only because this package's
// tests have no engine manager. Every one of those tests takes the `if bin ==
// ""` branch's exit at the top of probeUpload and never reaches the four lines
// under it:
//
//	if s.mgr == nil        -> unchecked
//	eng := s.eng(); nil    -> unchecked
//	tools := eng.Tools()   -> nil or empty FFprobe -> unchecked
//	bin = tools.FFprobe
//
// That last line is how EVERY REAL INSTALL finds its prober, and it had no
// test. Recurring defect #4 in this repository: correct code with no proof. The
// changes that would sever it silently are a refactor of engine.Manager,
// Engine.Tools() or the ffmpeg.Tools struct -- none of which would touch
// media_probe_test.go, and all of which would leave it green while every upload
// on every install was accepted unchecked.
//
// The gate is FAIL-OPEN by design, which is exactly what makes the absence of a
// test expensive rather than merely untidy: a severed wire does not produce an
// error anywhere. It produces uploads that are quietly not checked, and one WARN
// line per upload that nobody is reading.
//
// So the two cases below are written as a pair, and the second is the reason the
// first is worth anything. Without it "400 on a PDF" could be satisfied by any
// number of things; with it, the ONLY difference between the run that rejects
// and the run that accepts is the FFprobe field on the tools the manager was
// built with.

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// managerServer is renditionServer's fixture with the probe seam deliberately
// LEFT ALONE: probeBin stays empty, so probeUpload has to find ffprobe the way
// production does, through s.eng().Tools().
//
// The returned buffer is the API server's own log and nothing else's. The
// engine manager keeps io.Discard, so the WARN this test looks for cannot be
// confused with anything the engine said, and no goroutine of the engine's is
// writing into a bytes.Buffer while the test reads it.
func managerServer(t *testing.T, tools *ffmpeg.Tools) (*Server, http.Handler, *bytes.Buffer, func(*http.Request)) {
	t.Helper()

	dir := t.TempDir()
	store := dbtest.OpenAt(t, filepath.Join(dir, "polyemesis.db"))
	if _, err := store.CreateUser("admin", testPassword); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	box, err := secrets.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	cfg := config.Config{DataDir: dir}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	// A port this test owns; see renditionServer for why the 6000 default is not
	// safe to assume free.
	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Listeners.SRTPort = freeUDPPort(t)
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}

	bus := events.NewBroker()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := engine.NewManager(quiet, cfg, store, tools, bus)
	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("engine manager Start: %v", err)
	}
	t.Cleanup(eng.Stop)

	s := New(Options{
		Log: quiet, Config: cfg, DB: store, Secrets: box,
		Engine: eng, Events: bus, Version: "test",
	})
	h := s.Handler()
	lastTestServer = s

	if s.probeBin != "" {
		t.Fatalf("the fixture set probeBin to %q; this test exists to exercise the path "+
			"that runs when it is EMPTY", s.probeBin)
	}
	// The engine has to have produced a default engine, or every case below
	// takes the "no default engine" exit and the tools are never consulted.
	if s.eng() == nil {
		t.Fatal("the manager started but has no default engine, so probeUpload cannot " +
			"reach Tools() at all and both cases below would pass for the same wrong reason")
	}

	auth := login(t, h)
	// Swapped AFTER login so the buffer holds only what the upload said.
	var logs bytes.Buffer
	s.log = slog.New(slog.NewTextHandler(&logs, nil))
	return s, h, &logs, auth
}

// notMedia is the exact shape that used to reach the Library looking like a
// video: a PDF whose name ends in .mp4.
const notMedia = "%PDF-1.7\n1 0 obj\n<<>>\nendobj\n"

const uncheckedWarn = "no ffprobe available"

// Mutation: internal/api/media.go:308, `bin = tools.FFprobe` -> `bin = ""`,
// which is what severing the wiring looks like from the handler's side.
// Observed to fail with "the upload was ACCEPTED (201)" and to be the only
// failing test in the package.
//
// Also mutation-verified against `tools := eng.Tools()` -> `var tools
// *ffmpeg.Tools`, same failure, same sole-failure property.
func TestTheEnginesFFprobeReachesTheUploadGate(t *testing.T) {
	ffprobe := ffprobeOrSkip(t)
	tools := defaultTools()
	tools.FFprobe = ffprobe

	_, h, logs, auth := managerServer(t, tools)

	r := uploadRequest(t, "file", "holiday.mp4", notMedia)
	auth(r)
	w := do(t, h, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("the upload was ACCEPTED (%d) on an install whose engine reports a real "+
			"ffprobe at %s: the handler is not reading the binary off the engine's tools, "+
			"so every upload on every real install is stored unchecked. Body: %s",
			w.Code, ffprobe, w.Body.String())
	}
	// The negative, and the half that cannot be faked: if the gate had taken the
	// fail-open exit it would have said so in this line, and the upload would
	// have been kept. A test that only checked the status could be satisfied by
	// a 400 raised for some entirely different reason.
	if got := logs.String(); strings.Contains(got, uncheckedWarn) {
		t.Errorf("the gate ran but logged %q: it took the fail-open path and something else "+
			"produced the 400. Log: %s", uncheckedWarn, got)
	}
}

// The control, and the reason the test above is not vacuous: the ONLY thing that
// changes between the two is ffmpeg.Tools.FFprobe.
//
// It also pins the fail-open behaviour itself, which is deliberate and
// documented -- refusing every upload because a build has no prober would be a
// worse outage than the one it guards against -- together with the WARN that is
// the only way an operator can find out. docs/TROUBLESHOOTING.md points at that
// line by name.
//
// Mutation: internal/api/media.go:305, drop the `|| tools.FFprobe == ""` from
// the guard, so an empty path is passed to exec.
// Observed to fail on BOTH log assertions and to be the only failing test in
// the package. The upload is still accepted -- exec fails and the interrupted
// path keeps the file -- but the line an operator is told to grep for never
// appears; what appears instead is `the upload probe could not be run ...
// err="exec: no command"`, which describes a broken prober rather than an
// absent one. Same outcome, wrong diagnosis, and the status alone cannot tell
// them apart. That is why this test asserts on the reason and not just the code.
func TestAnEngineWithNoFFprobeAcceptsTheUploadAndSaysSo(t *testing.T) {
	tools := defaultTools()
	tools.FFprobe = ""

	_, h, logs, auth := managerServer(t, tools)

	r := uploadRequest(t, "file", "holiday.mp4", notMedia)
	auth(r)
	w := do(t, h, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("no ffprobe: the upload was rejected (%d). The gate is fail-open on "+
			"purpose -- an install whose engine has no prober must keep storing media -- "+
			"and this is a total outage of the upload feature instead. Body: %s",
			w.Code, w.Body.String())
	}
	got := logs.String()
	if !strings.Contains(got, uncheckedWarn) {
		t.Errorf("an upload was accepted unchecked and NOTHING SAID SO. That line is what "+
			"docs/TROUBLESHOOTING.md tells an operator to look for, and it is the only "+
			"detector this fail-open gate has. Log: %s", got)
	}
	// The internal reason names WHICH of the four fail-open exits was taken. Any
	// other one means this test is measuring a different hole than the one it
	// describes.
	if !strings.Contains(got, "the engine reports no ffprobe binary") {
		t.Errorf("the upload was accepted unchecked for some reason other than the engine's "+
			"empty FFprobe, so this case is not the control it claims to be. Log: %s", got)
	}
}

// A guard on the fixture rather than on the product: defaultTools() is shared
// with a dozen other tests, and if its FFprobe ever became empty the positive
// case above would be setting a field that was already what it needed to be
// while the control silently stopped controlling anything.
func TestTheProbeWiringFixtureChangesExactlyOneField(t *testing.T) {
	a, b := defaultTools(), defaultTools()
	a.FFprobe = "/somewhere/ffprobe"
	b.FFprobe = ""
	if a.FFmpeg != b.FFmpeg || a.Version != b.Version ||
		strings.Join(a.VideoEncoders, ",") != strings.Join(b.VideoEncoders, ",") {
		t.Fatal("the two cases differ in more than FFprobe, so the pair above no longer " +
			"isolates the wiring it is named after")
	}
	if defaultTools().FFprobe == "" {
		t.Error("defaultTools() now reports no ffprobe, so the control case sets a field " +
			"that was already empty and proves nothing about the positive one")
	}
}
