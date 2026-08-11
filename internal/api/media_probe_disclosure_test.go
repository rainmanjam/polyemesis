package api

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
)

// The two properties of the upload probe that are about the CALLER rather than
// about the file: what the refusal tells them (#181), and whether they can
// decide that the probe does not run at all (#216).

// ---------------------------------------------------------------- #181, the unit

// TestScrubProbePathsKeepsTheSentenceAndDropsThePath is the deterministic half.
//
// It runs no subprocess and reads no disk, so it covers Windows, where the rest
// of the probe suite skips (#190), and it is the only place the PRESERVATION
// half is asserted against a fixed string: the integration test below cannot
// pin ffprobe's exact wording across three platforms and several ffprobe
// versions without becoming a test of ffprobe's release notes.
//
// The strings in the table are real. The first is what ffprobe 8.1.2 printed
// for a PDF named .mp4, measured on this branch, wrapped exactly as
// ffmpeg.ProbeFile and probeUpload wrap it.
func TestScrubProbePathsKeepsTheSentenceAndDropsThePath(t *testing.T) {
	dataDir := filepath.Join(string(filepath.Separator)+"srv", "data")
	staged := filepath.Join(dataDir, "uploads", ".partial-1216776868.ts")

	for _, tc := range []struct {
		name       string
		in         string
		wantHas    []string
		wantHasNot []string
	}{
		{
			name: "the measured rejection",
			in: "this file could not be read as media: " + staged +
				": Invalid data found when processing input: exit status 1",
			// ffprobe's diagnosis is the half the operator can act on, and a
			// blanket scrub would take it. See #181: the fix strips the path,
			// not the sentence.
			wantHas: []string{
				"this file could not be read as media",
				"Invalid data found when processing input",
				"holiday.mp4",
			},
			wantHasNot: []string{dataDir, staged, ".partial-", "uploads"},
		},
		{
			name: "a message that names the directory but not the file",
			in:   "open " + filepath.Join(dataDir, "uploads") + ": permission denied",
			// The staged path is not present here, so a scrub that only handled
			// the one measured shape would pass this string through whole.
			wantHas:    []string{"permission denied"},
			wantHasNot: []string{dataDir},
		},
		{
			name: "the truncated-mp4 message an operator needs verbatim",
			in: "this file could not be read as media: " + staged +
				": moov atom not found: exit status 1",
			wantHas:    []string{"moov atom not found"},
			wantHasNot: []string{dataDir, ".partial-"},
		},
		{
			name: "a refusal that names no path at all is untouched",
			in:   "this file carries no video or audio stream",
			wantHas: []string{
				"this file carries no video or audio stream",
			},
			wantHasNot: []string{dataDir},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := scrubProbePaths(tc.in, staged, "holiday.mp4", dataDir)
			for _, want := range tc.wantHas {
				if !strings.Contains(got, want) {
					t.Errorf("the scrubbed message has lost %q, which is the half of "+
						"ffprobe's sentence the operator can act on.\n"+
						"#181's whole argument is that the PATH is the disclosure and "+
						"the WORDS are load-bearing; genericising the message is the fix "+
						"that issue rules out.\ngot: %s", want, got)
				}
			}
			for _, bad := range tc.wantHasNot {
				if strings.Contains(got, bad) {
					t.Errorf("the scrubbed message still carries %q, which is a path "+
						"this server chose and the caller has no business receiving.\n"+
						"got: %s", bad, got)
				}
			}
		})
	}

	// THE POSITIVE CONTROL ON THE TABLE. Every wantHasNot above is an absence
	// check, and a scrubber that returned the empty string would satisfy all of
	// them. This is the assertion that the inputs really did contain what the
	// table says they must lose.
	unscrubbed := "this file could not be read as media: " + staged + ": Invalid data found"
	if !strings.Contains(unscrubbed, dataDir) {
		t.Fatal("the fixture does not contain the data directory, so every absence " +
			"check above passes against an input that was already clean")
	}
	if scrubProbePaths(unscrubbed, staged, "holiday.mp4", dataDir) == unscrubbed {
		t.Fatal("scrubProbePaths returned its input unchanged for a message that " +
			"carries the staged path; the table above is measuring nothing")
	}
}

// ------------------------------------------------------- #181, through the router

// TestUploadRejectionDoesNotDiscloseTheDataDirectory drives the real handler
// with the real ffprobe, because the unit test above cannot show that the
// handler CALLS it.
//
// That distinction is not academic in this file's history: media_probe_test.go
// records that review once deleted the probe call from the handler outright and
// every test in two packages stayed green.
//
// POST /api/v1/media is session-only, so the caller here is already an
// authenticated operator who could read the data directory off the settings
// page -- which is why #181 was filed rather than treated as a blocker. What it
// closes is the shape: an unscrubbed subprocess stderr reaching a response
// body, in a repo that has just been through argv scrubbing and path-disclosure
// removal. The deployment that makes it matter is the one that forwards upload
// failures to a shared channel or a support bundle.
func TestUploadRejectionDoesNotDiscloseTheDataDirectory(t *testing.T) {
	ffprobe := ffprobeOrSkip(t)
	_, h, dataDir, auth := probeServer(t, ffprobe)

	// A PDF named .mp4: the shape whose refusal message ffprobe prefixes with
	// the input path.
	r := uploadRequest(t, "file", "holiday.mp4", "%PDF-1.7\n1 0 obj\n<<>>\nendobj\n")
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("upload status = %d, want 400: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// (1) THE PROBE PATH IS THE ONE UNDER TEST. Without this the assertions
	// below would be satisfied by a 400 from the multipart parser, which names
	// no path and would make every absence check vacuous.
	const prefix = "this file could not be read as media"
	if !strings.Contains(body, prefix) {
		t.Fatalf("the refusal did not come from the probe, so this test is not "+
			"reading the message #181 is about: %s", body)
	}

	// (2) THE DISCLOSURE. The data directory is the server's layout and this is
	// the body that carried it.
	if strings.Contains(body, dataDir) {
		t.Errorf("the rejection body carries the server's data directory %q.\n"+
			"ffprobe names its input in front of nearly everything it prints, and "+
			"probeUpload passed those words through verbatim (#181). Scrub the path "+
			"at the handler egress and keep the sentence.\nbody: %s", dataDir, body)
	}
	// The uploads directory and the internal temp name are the same disclosure
	// one segment down, and a scrub that only removed the configured prefix
	// would leave both.
	for _, bad := range []string{filepath.Join(dataDir, "uploads"), ".partial-"} {
		if strings.Contains(body, bad) {
			t.Errorf("the rejection body carries %q: %s", bad, body)
		}
	}

	// (3) AND THE SENTENCE SURVIVED. #181 rules out genericising the message:
	// `moov atom not found` is what tells an operator their download was
	// truncated. The exact wording is ffprobe's and is not pinned here -- the
	// unit test above does that against fixed strings -- but there must be
	// something after the generic prefix, and it must not be only the exit
	// status.
	detail := strings.TrimSpace(strings.TrimPrefix(
		strings.SplitN(body, prefix, 2)[1], ":"))
	detail = strings.TrimSuffix(strings.TrimSuffix(detail, `"}`), `"`)
	if detail == "" || strings.HasPrefix(detail, "exit status") {
		t.Errorf("the rejection says only %q, so the scrub took ffprobe's diagnosis "+
			"with the path. #181 asks for the prefix to be stripped, not for the "+
			"message to be genericised.\nbody: %s", detail, body)
	}
	t.Logf("operator sees: %s", detail)
}

// -------------------------------------------------------------------- #216

// TestOccupyingEveryProbeSlotDoesNotStoreAnUninspectedFile is #216 driven.
//
// THE SHAPE: a branch whose taken-ness a remote caller chooses. `ctx.Err()` let
// a remote DISCONNECT decide whether the upload gate ran; a remote DELETE let a
// caller decide which of four states a file was in; and the four-slot semaphore
// added to bound ffprobe children put the WAIT FOR A SLOT inside the probe's own
// 30-second deadline -- so REMOTE CONCURRENCY decided. Sixteen concurrent
// uploads meant twelve waiting, and a caller who wanted their file to go
// uninspected no longer needed to disconnect: they needed eleven tabs.
//
// This drives the mechanism rather than the tab count. Occupying the slots
// directly is what sixteen friends buy an attacker, without sixteen ffprobe
// children and sixteen seconds of CI; MaxConcurrentUploadProbes is read rather
// than assumed, so raising the constant does not quietly stop this from being
// an occupied queue.
//
// THE FILE IS A PDF, so the correct outcome is a REFUSAL. That is the assertion
// that cannot be satisfied by accident: a 400 means ffprobe ran and disagreed,
// which is the only outcome that proves the gate was not skipped. Before the
// fix this answered 201 with verified:false and left the file on disk.
//
// No fake probe, so this runs on Windows too.
func TestOccupyingEveryProbeSlotDoesNotStoreAnUninspectedFile(t *testing.T) {
	ffprobe := ffprobeOrSkip(t)
	const pdf = "%PDF-1.7\n1 0 obj\n<<>>\nendobj\n"

	// THE CONTROL FIRST, on its own server. Every assertion below is "the file
	// was refused", and a fixture that refuses uploads for some unrelated
	// reason -- a broken login, a rejected multipart body -- would satisfy them
	// having proved nothing about the queue. This run has the slots free.
	{
		_, h, dataDir, auth := probeServer(t, ffprobe)
		r := uploadRequest(t, "file", "control.mp4", pdf)
		auth(r)
		if w := do(t, h, r); w.Code != http.StatusBadRequest {
			t.Fatalf("with the probe slots FREE, a PDF got status %d; this fixture "+
				"cannot demonstrate anything about a busy queue: %s",
				w.Code, w.Body.String())
		}
		if names := uploadsDirEntries(t, dataDir); len(names) != 0 {
			t.Fatalf("with the probe slots free, the refused PDF is still on disk: %v", names)
		}
	}

	dataDir := t.TempDir()
	s, h, _ := testServer(t, config.Config{DataDir: dataDir})
	s.probeBin = ffprobe

	// THE BUDGET AND THE HOLD, and the relation between them is the whole
	// experiment. The hold is longer than the budget, so under the old code the
	// deadline expired while the request was still queued and the file was
	// stored unchecked. Once the slot is in hand the probe gets the WHOLE budget
	// again -- which is the fix -- and a budget of two seconds for one ffprobe
	// over a forty-byte file is not a wall-clock race on any of the three CI
	// platforms. Nothing here charges process-spawn cost against the deadline
	// that decides the verdict.
	const budget = 2 * time.Second
	const hold = budget + time.Second
	s.probeTimeout = budget

	if MaxConcurrentUploadProbes < 1 {
		t.Fatalf("MaxConcurrentUploadProbes is %d; there is no queue to occupy",
			MaxConcurrentUploadProbes)
	}
	for i := 0; i < MaxConcurrentUploadProbes; i++ {
		probeSlots <- struct{}{}
	}
	// probeSlots is package state. Returning it empty is not tidiness: the next
	// test in this package would otherwise start against a semaphore with no
	// slots left and block for its whole timeout.
	defer func() {
		for {
			select {
			case <-probeSlots:
			default:
				return
			}
		}
	}()

	type result struct {
		code int
		body string
	}
	done := make(chan result, 1)
	auth := login(t, h)
	go func() {
		r := uploadRequest(t, "file", "queued.mp4", pdf)
		auth(r)
		w := do(t, h, r)
		done <- result{w.Code, w.Body.String()}
	}()

	select {
	case got := <-done:
		// THIS IS WHERE #216 LANDS. Under the old ordering the request queued,
		// its deadline expired while it was still queued, and it answered 201
		// with verified:false -- measured, and the body is printed because it is
		// the finding: a file the server never looked at, stored, because
		// somebody else was uploading.
		//
		// A 400 here would mean something different and would also be a failure:
		// it would mean the request never queued at all and the fixture is not
		// holding the slots it thinks it is.
		t.Fatalf("the upload finished with status %d while every probe slot was "+
			"occupied. A queued upload must WAIT for a slot; the wait must not run "+
			"inside the probe's own deadline, or remote concurrency decides whether "+
			"the gate runs (#216).\nbody: %s", got.code, got.body)
	case <-time.After(hold):
	}
	// Hand the slots back. Under the fixed code the waiter is still waiting;
	// under the old code its deadline expired a second ago and it has already
	// answered 201.
	for i := 0; i < MaxConcurrentUploadProbes; i++ {
		<-probeSlots
	}

	var got result
	select {
	case got = <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the upload never finished after the probe slots were released")
	}

	if got.code != http.StatusBadRequest {
		t.Errorf("a caller who occupied every probe slot got status %d for a PDF, "+
			"where a caller who did not gets 400.\n"+
			"That is #216: the wait for a probe slot ran inside the probe's own "+
			"deadline, so REMOTE CONCURRENCY decided whether the upload gate ran at "+
			"all. Queue on the request's context and start the deadline when the "+
			"probe does.\nbody: %s", got.code, got.body)
	}
	if !strings.Contains(got.body, "could not be read as media") &&
		!strings.Contains(got.body, "no video or audio stream") {
		t.Errorf("the refusal does not say ffprobe disagreed, so the gate may not "+
			"have run: %s", got.body)
	}
	// AND NOTHING WAS KEPT. The 201 this used to answer left the PDF in the
	// Library recorded as unverified -- distinguishable, which is why #216 is a
	// denial of verification rather than a bypass, but stored all the same, and
	// #201 records the consumer that already assumes stored-implies-checked.
	if names := uploadsDirEntries(t, dataDir); len(names) != 0 {
		t.Errorf("a file that was never inspected is on disk: %v", names)
	}
}
