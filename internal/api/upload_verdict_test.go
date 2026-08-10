package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// THE THIRD STATE.
//
// An upload is in one of three states: inspected and accepted, refused, and
// STORED WITHOUT BEING INSPECTED. The third one is new, it is reachable ON
// DEMAND by a remote client -- the probe runs under the request's context, so
// dropping the connection after the body has landed cancels it -- and until
// this file it was invisible: `media` was simply absent from the listing, which
// is also what every upload stored before probing existed looks like, and
// nothing downstream asked.
//
// Every test here is the independent reviewer's own reproduction, kept as
// written and turned round to assert the fixed behaviour rather than deleted.
// The originals printed "REMOTE ATTACK SUCCEEDED" and passed.
//
// WHAT IS NOT COVERED ON WINDOWS. Three of them need a probe whose lifetime the
// test controls, which is fakeProbe, which is a POSIX shell script -- so on
// Windows they skip, exactly as the rest of the probe suite does (#190). The
// verdict RECORD itself is plain Go over plain files and is covered on every
// platform by the tests that use no fake probe: the socket test below, the
// playlist refusal, and internal/uploads' own verdict tests.

// mustStore opens the uploads store under a test's data directory.
func mustStore(t *testing.T, dataDir string) *uploads.Store {
	t.Helper()
	s, err := uploads.New(dataDir)
	if err != nil {
		t.Fatalf("uploads.New: %v", err)
	}
	return s
}

// onlyFile is the one file in the listing, and fails if there is not
// exactly one.
func onlyFile(t *testing.T, dataDir string) uploads.File {
	t.Helper()
	store, err := uploads.New(dataDir)
	if err != nil {
		t.Fatalf("uploads.New: %v", err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly one stored upload, got %+v", list)
	}
	return list[0]
}

// F1, THE ACCEPTANCE CRITERION, driven exactly as the reviewer drove it: a real
// socket, the whole multipart body written, SetLinger(0) so the connection is
// RST rather than closed politely, and then nothing.
//
// The reviewer's run of this printed
//
//	REMOTE ATTACK SUCCEEDED: published "show-421148f8.mp4" media=<nil> after the client RST
//	content: "ffconcat version 1.0\nfile victim.mp4\n"
//
// and the file was a legal playlist item on its way to an FFmpeg with no format
// allowlist. The bytes are STILL KEPT -- that is must-fix 2 and it must not be
// undone by going back to deleting -- and what changed is that the result is no
// longer silently usable: it is recorded as uninspected, the API says so, and
// playlistUploadProblems refuses it.
func TestATTACK_RealSocketDisconnectPublishesUnprobed(t *testing.T) {
	dataDir := t.TempDir()
	s, h, _ := testServer(t, config.Config{DataDir: dataDir})
	// Outlives the disconnect, so the probe is guaranteed to still be running
	// when the client vanishes. A script, so this skips on Windows (#190).
	s.probeBin = fakeProbe(t, filepath.Join(t.TempDir(), "started"), "exec sleep 8")

	srv := httptest.NewServer(h)
	defer srv.Close()

	// Log in over the wire to collect real cookies.
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": testPassword})
	resp, err := http.Post(srv.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	var cookieHdr []string
	var csrf string
	for _, c := range resp.Cookies() {
		cookieHdr = append(cookieHdr, c.Name+"="+c.Value)
		if strings.Contains(strings.ToLower(c.Name), "csrf") {
			csrf = c.Value
		}
	}

	const script = "ffconcat version 1.0\nfile victim.mp4\n"
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "show.mp4")
	fw.Write([]byte(script))
	mw.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	req := fmt.Sprintf("POST /api/v1/media HTTP/1.1\r\nHost: %s\r\nContent-Type: %s\r\n"+
		"Content-Length: %d\r\nCookie: %s\r\nX-CSRF-Token: %s\r\nConnection: close\r\n\r\n",
		host, mw.FormDataContentType(), buf.Len(), strings.Join(cookieHdr, "; "), csrf)
	conn.Write([]byte(req))
	conn.Write(buf.Bytes())
	// The client vanishes the instant the body is out.
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetLinger(0) // RST, not a graceful FIN
	}
	conn.Close()

	var f uploads.File
	deadline := time.Now().Add(20 * time.Second)
	for {
		store, _ := uploads.New(dataDir)
		if list, _ := store.List(); len(list) == 1 {
			f = list[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the completed transfer was not kept; must-fix 2 has been undone")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// (1) MUST-FIX 2 SURVIVES. The bytes the client sent are on disk, whole. A
	// fix that answered F1 by deleting again would fail here, which is why this
	// assertion is first.
	full := filepath.Join(dataDir, "uploads", f.Name)
	onDisk, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("the upload is listed but not on disk: %v", err)
	}
	if string(onDisk) != script {
		t.Fatalf("the stored bytes are %q, not what the client sent", string(onDisk))
	}

	// (2) IT IS NO LONGER SILENTLY USABLE. The record beside it says nobody read
	// it, and the API says so in a field that is always present -- not by the
	// ABSENCE of `media`, which is also how a pre-probing upload looks.
	if f.Verified {
		t.Fatal("a file the server never inspected reports verified=true")
	}
	if f.Media != nil {
		t.Errorf("an uninspected file carries metadata: %+v", f.Media)
	}
	if f.UnverifiedReason == "" {
		t.Error("the listing does not say why the file is unverified")
	}
	if v, recorded := mustStore(t, dataDir).Verdict(f.Name); !recorded || v.Verified {
		t.Errorf("the verdict on disk is (%+v, recorded=%v); want a recorded refusal to inspect",
			v, recorded)
	}

	// (3) A DOWNSTREAM CONSUMER REFUSES IT. playlistUploadProblems is the gate
	// PUT /api/v1/settings runs a playlist through, and it returned nil for this
	// exact file.
	if err := s.playlistUploadProblems(
		db.PlaylistSettings{Items: []db.PlaylistItem{{Upload: f.Name}}},
		db.PlaylistSettings{}); err == nil {
		t.Fatal("a file the server never inspected is still a legal playlist item")
	}

	// (4) THE CONTROL, without which (3) is satisfied by a validator that
	// refuses everything -- a different bug that would look identical.
	if err := os.WriteFile(filepath.Join(dataDir, "uploads", "control.ts"),
		[]byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.playlistUploadProblems(
		db.PlaylistSettings{Items: []db.PlaylistItem{{Upload: "control.ts"}}},
		db.PlaylistSettings{}); err != nil {
		t.Fatalf("the validator refuses an ordinary upload too, so (3) proves nothing: %v", err)
	}
}

// The DeadlineExceeded twin. Same hole, different route in: probeTimeout
// expiring rather than the client leaving.
//
// The reviewer's run answered 201 Created with media=nil and the ffconcat on
// disk. It still answers 201 -- a slow disk must not delete valid media -- and
// what it no longer does is leave the result indistinguishable.
func TestATTACK_SlowProbeTimeoutPublishesUnprobed(t *testing.T) {
	dataDir := t.TempDir()
	s, h, _ := testServer(t, config.Config{DataDir: dataDir})
	s.probeBin = fakeProbe(t, filepath.Join(t.TempDir(), "started"), "exec sleep 60")
	s.probeTimeout = 200 * time.Millisecond
	auth := login(t, h)

	r := uploadRequest(t, "file", "show.mp4", "ffconcat version 1.0\nfile victim.mp4\n")
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: a timed-out probe is not a verdict about the "+
			"file: %s", w.Code, w.Body.String())
	}

	// The 201 body itself carries the state, so a client that never lists is
	// still told. It was `{"name":...,"origin":"uploaded","bytes":37}` with the
	// media key simply absent, which is byte-identical in shape to a pre-probing
	// upload.
	var created uploads.File
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Verified {
		t.Error("the 201 body claims the file was verified")
	}
	if !strings.Contains(w.Body.String(), `"verified":false`) {
		t.Errorf("the 201 body does not state the verdict: %s", w.Body.String())
	}

	f := onlyFile(t, dataDir)
	if f.Verified || f.UnverifiedReason == "" {
		t.Errorf("the listing does not mark the file: %+v", f)
	}
	if err := s.playlistUploadProblems(
		db.PlaylistSettings{Items: []db.PlaylistItem{{Upload: f.Name}}},
		db.PlaylistSettings{}); err == nil {
		t.Error("a file whose inspection timed out is still a legal playlist item")
	}
}

// BOTH DIRECTIONS OF MUST-FIX 2, IN ONE RUN.
//
// The property that must survive: a client disconnect on a VALID upload does
// not destroy it. The property that must hold: the same bytes, inspected, are
// refused when they are not media. Asserting only one of those is how a fix for
// either becomes a regression of the other.
func TestADisconnectKeepsAValidUploadAndTheSameBytesAreStillRefusedWhenChecked(t *testing.T) {
	ffprobe := ffprobeOrSkip(t)
	realMedia := sampleMedia(t)

	t.Run("the probe runs: real media is accepted, an ffconcat is refused", func(t *testing.T) {
		_, h, dataDir, auth := probeServer(t, ffprobe)

		r := uploadBytesRequest(t, "show.mkv", realMedia)
		auth(r)
		if w := do(t, h, r); w.Code != http.StatusCreated {
			t.Fatalf("real media was refused: %d %s", w.Code, w.Body.String())
		}
		good := onlyFile(t, dataDir)
		if !good.Verified || good.Media == nil {
			t.Fatalf("an inspected, accepted upload is not marked verified: %+v", good)
		}

		sr := uploadRequest(t, "file", "innocent.mp4", "ffconcat version 1.0\nfile "+good.Name+"\n")
		auth(sr)
		if w := do(t, h, sr); w.Code != http.StatusBadRequest {
			t.Fatalf("an ffconcat script was accepted with a live context: %d %s",
				w.Code, w.Body.String())
		}
	})

	t.Run("the probe is cut short: the completed transfer is kept", func(t *testing.T) {
		dataDir := t.TempDir()
		s, h, _ := testServer(t, config.Config{DataDir: dataDir})
		auth := login(t, h)
		started := filepath.Join(t.TempDir(), "started")
		s.probeBin = fakeProbe(t, started, "exec sleep 60")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := uploadBytesRequest(t, "long-show.mkv", realMedia)
		auth(r)
		r = r.WithContext(ctx)

		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			done <- w
		}()
		waitFor(t, "the probe to start", func() bool {
			_, err := os.Stat(started)
			return err == nil
		})
		cancel() // every byte has already arrived

		select {
		case w := <-done:
			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the handler never returned after the client disconnected")
		}

		f := onlyFile(t, dataDir)
		onDisk, err := os.ReadFile(filepath.Join(dataDir, "uploads", f.Name))
		if err != nil {
			t.Fatalf("the operator's completed transfer was destroyed: %v", err)
		}
		if len(onDisk) != len(realMedia) {
			t.Fatalf("the kept file is %d bytes, the client sent %d", len(onDisk), len(realMedia))
		}
		if f.Verified {
			t.Error("a file whose inspection was cut short reports verified=true")
		}
	})
}

// FINDING A. The fail-open rule did not cover "the probe could not be STARTED",
// and the documentation said it did.
//
// With probeBin pointing at a path that is not there, every upload got
// 400 "this file could not be read as media: fork/exec ...: no such file or
// directory" and the uploads directory was EMPTY afterwards -- the identical
// harm as must-fix 2 (a completed transfer destroyed while the server asserts
// something false about the bytes) on the one route that fix did not reach.
// exec's start failures are *exec.Error and fs.ErrPermission, never
// *exec.ExitError, and ctx.Err() is nil for them, so neither existing guard
// fired. Reachable without misconfiguration: a fork that fails with EAGAIN on a
// box that is also encoding live video.
func TestAProbeThatCannotBeRunKeepsTheFileAndSaysSo(t *testing.T) {
	for _, tc := range []struct{ name, bin string }{
		{"a binary that is not there", filepath.Join(t.TempDir(), "no-such-ffprobe")},
		{"a directory", t.TempDir()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			s, h, _ := testServer(t, config.Config{DataDir: dataDir})
			s.probeBin = tc.bin
			auth := login(t, h)

			r := uploadRequest(t, "file", "show.mkv", "pretend media bytes")
			auth(r)
			w := do(t, h, r)
			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201: a probe this server could not run is not "+
					"a verdict about the operator's file: %s", w.Code, w.Body.String())
			}
			f := onlyFile(t, dataDir)
			if f.Verified {
				t.Error("the file is marked verified although nothing inspected it")
			}
			if f.UnverifiedReason != uploads.ReasonProbeUnusable {
				t.Errorf("reason = %q, want %q", f.UnverifiedReason, uploads.ReasonProbeUnusable)
			}
			// AND THE PATH IS NOT IN THE RESPONSE. The 400 body used to carry
			// the server's absolute ffprobe path, from the Go exec error rather
			// than from ffprobe's stderr.
			if strings.Contains(w.Body.String(), tc.bin) {
				t.Errorf("the response leaks the server's probe path: %s", w.Body.String())
			}
		})
	}
}

// The same rule for a probe that RUNS, exits 0, and prints something this
// process cannot read. "parse ffprobe output: invalid character 'o'" is a
// sentence about the binary this server was pointed at, and it was being
// reported to the operator as a verdict about their file -- and deleting it.
func TestAProbeThatPrintsRubbishKeepsTheFileAndSaysSo(t *testing.T) {
	dataDir := t.TempDir()
	s, h, _ := testServer(t, config.Config{DataDir: dataDir})
	s.probeBin = fakeProbe(t, filepath.Join(t.TempDir(), "started"), "echo not json at all")
	auth := login(t, h)

	r := uploadRequest(t, "file", "show.mkv", "pretend media bytes")
	auth(r)
	if w := do(t, h, r); w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}
	f := onlyFile(t, dataDir)
	if f.Verified || f.UnverifiedReason != uploads.ReasonProbeUnusable {
		t.Errorf("verdict = (verified=%v, %q), want an unusable-prober record",
			f.Verified, f.UnverifiedReason)
	}
}

// THE CONTROL FOR ALL OF THE ABOVE. A probe that runs and DISAGREES still
// refuses and still leaves nothing behind. Without this, every test in this
// file is satisfied by a handler that had simply stopped rejecting anything.
func TestAProbeThatRunsAndDisagreesStillRejectsAndLeavesNothing(t *testing.T) {
	dataDir := t.TempDir()
	s, h, _ := testServer(t, config.Config{DataDir: dataDir})
	s.probeBin = fakeProbe(t, filepath.Join(t.TempDir(), "started"),
		"echo 'Invalid data found when processing input' >&2; exit 1")
	auth := login(t, h)

	r := uploadRequest(t, "file", "junk.mkv", "not media at all")
	auth(r)
	if w := do(t, h, r); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if names := uploadsDirEntries(t, dataDir); len(names) != 0 {
		t.Errorf("left on disk after rejection: %v", names)
	}
}

// F2. The unprobed file was a legal playlist item -- Listable passed, os.Stat
// passed, and playlistUploadProblems returned nil. It was the reviewer's
// TestATTACK_UnprobedFileBecomesPlaylistItem, and it passed.
//
// The refusal is on a RECORDED verdict, not on the absence of one, and the
// second half of this test is what pins that distinction: an upload with no
// record at all -- everything an install stored before verdicts existed -- is
// still allowed, because refusing it would strand media the operator has had
// for a year over a file that was never written. Those are covered by the
// normalise worker's own re-check instead; see internal/playlistmedia.
func TestATTACK_UnprobedFileBecomesPlaylistItem(t *testing.T) {
	dataDir := t.TempDir()
	s, _, _ := testServer(t, config.Config{DataDir: dataDir})
	store := mustStore(t, dataDir)

	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dataDir, "uploads", name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	problem := func(name string) error {
		return s.playlistUploadProblems(
			db.PlaylistSettings{Items: []db.PlaylistItem{{Upload: name}}},
			db.PlaylistSettings{})
	}

	write("unchecked.ts")
	if err := store.PutVerdict("unchecked.ts",
		uploads.UnverifiedVerdict(uploads.ReasonInterrupted)); err != nil {
		t.Fatal(err)
	}
	if err := problem("unchecked.ts"); err == nil {
		t.Error("an upload recorded as never inspected is a legal playlist item")
	} else if !strings.Contains(err.Error(), "upload it again") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}

	write("checked.ts")
	if err := store.PutMedia("checked.ts", uploads.MediaInfo{AudioTracks: 2}); err != nil {
		t.Fatal(err)
	}
	if err := problem("checked.ts"); err != nil {
		t.Errorf("an inspected upload was refused, so the assertion above proves nothing: %v", err)
	}

	// No record at all: an upload from before verdicts existed. Allowed here,
	// and re-checked at the moment of use by the normalise worker.
	write("legacy.ts")
	if err := problem("legacy.ts"); err != nil {
		t.Errorf("an upload predating verdicts was refused: %v", err)
	}

	// INHERITED ITEMS ARE STILL SKIPPED. The scoping rule this validator has
	// always had -- refuse what the operator is introducing, never punish them
	// for state they inherited -- is not quietly changed by adding a check.
	if err := s.playlistUploadProblems(
		db.PlaylistSettings{Items: []db.PlaylistItem{{Upload: "unchecked.ts"}}},
		db.PlaylistSettings{Items: []db.PlaylistItem{{Upload: "unchecked.ts"}}}); err != nil {
		t.Errorf("an inherited item was refused: %v", err)
	}
}

// F5. Nothing bounded how many ffprobe children one operator session could have
// alive at once. 25 concurrent uploads spawned 25, each holding its request
// goroutine for the full 30 seconds, measured.
//
// The bound is on CHILDREN, so that is what is counted: the fake probe appends
// a line on entry and removes it on exit, and the high-water mark must never
// exceed MaxConcurrentUploadProbes. Skips on Windows with the rest of the fake
// probe suite (#190).
func TestConcurrentUploadsDoNotSpawnUnboundedProbes(t *testing.T) {
	dataDir := t.TempDir()
	s, h, _ := testServer(t, config.Config{DataDir: dataDir})
	live := filepath.Join(t.TempDir(), "live")
	peak := filepath.Join(t.TempDir(), "peak")
	// One line per live child; the peak file keeps the high-water mark. Done
	// with a lock directory so two children cannot interleave a read-modify-
	// write of it.
	s.probeBin = fakeProbe(t, filepath.Join(t.TempDir(), "started"),
		"until mkdir "+live+".lock 2>/dev/null; do :; done\n"+
			"echo x >> "+live+"\n"+
			"n=$(wc -l < "+live+")\n"+
			"p=$(cat "+peak+" 2>/dev/null || echo 0)\n"+
			"[ \"$n\" -gt \"$p\" ] && echo \"$n\" > "+peak+"\n"+
			"rmdir "+live+".lock\n"+
			"sleep 0.4\n"+
			"until mkdir "+live+".lock 2>/dev/null; do :; done\n"+
			"sed -e '$d' "+live+" > "+live+".t && mv "+live+".t "+live+"\n"+
			"rmdir "+live+".lock\n"+
			"exit 1")
	auth := login(t, h)

	const n = 16
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			r := uploadRequest(t, "file", fmt.Sprintf("f%d.ts", i), strings.Repeat("A", 512))
			auth(r)
			do(t, h, r)
		}(i)
	}
	for i := 0; i < n; i++ {
		select {
		case <-done:
		case <-time.After(120 * time.Second):
			t.Fatal("uploads did not finish")
		}
	}
	b, err := os.ReadFile(peak)
	if err != nil {
		t.Fatalf("the fake probe never recorded a peak, so this measures nothing: %v", err)
	}
	var got int
	fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &got)
	if got == 0 {
		t.Fatal("peak concurrency read as 0, so this measures nothing")
	}
	if got > MaxConcurrentUploadProbes {
		t.Errorf("%d concurrent uploads had %d ffprobe children alive at once, "+
			"want at most %d", n, got, MaxConcurrentUploadProbes)
	}
	t.Logf("%d concurrent uploads peaked at %d live probe children (bound is %d)",
		n, got, MaxConcurrentUploadProbes)
}

// F7. A store error must not put the server's paths in the response body.
//
// writeUploadError's default arm returned err.Error(), and everything that
// reaches it is an os.PathError over a path this server chose. Filling the
// volume mid-write produced `write upload: write
// /srv/data/uploads/.partial-1216776868.ts: no space left on device` with a 500
// attached, where the pre-check path for the same condition answers 507 and
// says nothing about paths.
func TestAStoreFailureDoesNotLeakServerPaths(t *testing.T) {
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	secret := "/srv/data/uploads/.partial-1216776868.ts"

	w := httptest.NewRecorder()
	writeUploadError(w, log, fmt.Errorf("write upload: write %s: no space left on device", secret))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Errorf("the response body carries a server path: %s", w.Body.String())
	}
	// The detail is not thrown away, it is moved to where the operator reads it.
	if !strings.Contains(logged.String(), secret) {
		t.Errorf("the detail was dropped rather than logged: %s", logged.String())
	}

	// The classified arms are unchanged and still say what they always said.
	w = httptest.NewRecorder()
	writeUploadError(w, log, uploads.ErrNoSpace)
	if w.Code != http.StatusInsufficientStorage {
		t.Errorf("ErrNoSpace status = %d, want 507", w.Code)
	}
}
