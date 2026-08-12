package api

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/uploadverify"
)

// #202. Until this route existed the Library could say "Not checked" about a
// file and offer nothing to do about it: the only remedy was to send the bytes
// again, which is no remedy for a file the operator no longer has locally, and
// is actively wrong for one that was refused.

// verifyServer is a server with a real queue and a real uploads directory.
func verifyServer(t *testing.T) (http.Handler, func(*http.Request), *Server, *jobs.Queue) {
	t.Helper()
	h, store, sign := sourceServer(t)
	srv := serverUnderTest(t, h)
	q := jobs.New(slog.New(slog.NewTextHandler(io.Discard, nil)), store)
	srv.jobq = q
	return h, sign, srv, q
}

func verifyJobs(t *testing.T, q *jobs.Queue) []jobs.Job {
	t.Helper()
	all, err := q.List(jobs.Filter{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	var out []jobs.Job
	for _, j := range all {
		if j.Kind == uploadverify.Kind {
			out = append(out, j)
		}
	}
	return out
}

// The route exists, queues exactly one job, and the job names the upload it was
// asked about. A handler that answers 201 and queues nothing is the shape a
// "works" screenshot is taken of.
func TestVerifyingAnUploadQueuesAReCheckForThatFile(t *testing.T) {
	h, sign, srv, q := verifyServer(t)
	seedUpload(t, srv, "show-abcd1234.ts")
	seedUpload(t, srv, "other-99999999.ts")

	send(t, h, sign, http.MethodPost, "/api/v1/media/show-abcd1234.ts/verify", nil, http.StatusCreated)

	got := verifyJobs(t, q)
	if len(got) != 1 {
		t.Fatalf("queued %d re-checks, want 1: %+v", len(got), got)
	}
	if want := "upload:show-abcd1234.ts"; got[0].Target != want {
		t.Fatalf("the queued job targets %q, want %q -- a re-check pointed at the "+
			"wrong file writes a verdict beside bytes nobody asked about",
			got[0].Target, want)
	}
	var params uploadverify.Params
	decodeInto(t, got[0].Params, &params)
	if params.Upload != "show-abcd1234.ts" {
		t.Fatalf("the job's params name %q, want show-abcd1234.ts", params.Upload)
	}
}

// Unique folds a second press into the first. Without it, leaning on the button
// queues one multi-gigabyte FFprobe per click.
func TestVerifyingTwiceAsksOnce(t *testing.T) {
	h, sign, srv, q := verifyServer(t)
	seedUpload(t, srv, "show-abcd1234.ts")

	send(t, h, sign, http.MethodPost, "/api/v1/media/show-abcd1234.ts/verify", nil, http.StatusCreated)
	// 200 rather than 201 on the fold: telling the client it created something
	// has it counting two jobs where there is one.
	send(t, h, sign, http.MethodPost, "/api/v1/media/show-abcd1234.ts/verify", nil, http.StatusOK)

	if got := verifyJobs(t, q); len(got) != 1 {
		t.Fatalf("two presses queued %d jobs, want 1: %+v", len(got), got)
	}
}

// The name is validated the way handleDeleteMedia validates it, and for the
// reason that handler spells out: uploads.Listable owns which names the product
// admits to having. A sidecar name reaching the worker means an FFprobe against
// `.probe-<name>.json` and then a verdict written BESIDE the sidecar -- a file
// this product has no other way to create, and one that would then be read as
// the verdict for a file named `.probe-<name>`.
func TestOnlyANameTheLibraryListsCanBeReChecked(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		seed   bool
		status int
		why    string
	}{
		{
			name: "a verdict sidecar", path: ".probe-show-abcd1234.ts.json", seed: true,
			status: http.StatusBadRequest,
			why: "probing a sidecar and writing a verdict beside it creates a file " +
				"nothing else in the product can create",
		},
		{
			name: "a staged upload still in flight", path: ".partial-show-abcd1234.ts", seed: true,
			status: http.StatusBadRequest,
			why:    "the bytes are incomplete and the name is about to stop existing",
		},
		{
			name: "a name reservation", path: ".partial-claim-show-abcd1234.ts", seed: true,
			status: http.StatusBadRequest,
			why:    "a zero-byte placeholder another Commit is holding",
		},
		// THE ORDERING, not just the rule, and these three rows are what
		// distinguish the HANDLER's guard from the one uploadverify.NewJob
		// makes. With the handler's uploads.Listable check removed the seeded
		// rows above still answer 400 -- NewJob refuses the name too -- so on
		// their own they pin nothing about this handler. Unseeded, the guard's
		// absence lets os.Stat answer FIRST, and 404-vs-400 becomes an
		// existence oracle for names the product does not admit to having:
		// whether a given upload has a verdict sidecar, and whether a given
		// name is mid-upload right now. The same reason handleDeleteMedia
		// validates the name before it removes anything.
		{
			name: "a sidecar that is not there", path: ".probe-never-uploaded.ts.json",
			status: http.StatusBadRequest,
			why:    "refused as a NAME, before anything says whether bytes exist at it",
		},
		{
			name: "a staged name that is not there", path: ".partial-never-uploaded.ts",
			status: http.StatusBadRequest,
			why:    "same, for a name that would be an upload in flight",
		},
		{
			name: "a reservation that is not there", path: ".partial-claim-never-uploaded.ts",
			status: http.StatusBadRequest,
			why:    "same, for a name another Commit would be holding",
		},
		{
			name: "a bare parent directory", path: "..",
			status: http.StatusBadRequest,
			why:    "the confinement uploads.Store.Resolve owns",
		},
		{
			name: "a percent-encoded traversal", path: "..%2F..%2Fetc%2Fpasswd",
			status: http.StatusNotFound, seed: false,
			why: "chi hands the segment over still encoded, so this is a LITERAL " +
				"filename with no separator in it and resolves inside the uploads " +
				"directory -- 404 because no such file, which is the right answer " +
				"and not the same one as the row above; a test asserting 400 here " +
				"would be asserting a decode that does not happen",
		},
		{
			name: "a file that is not there", path: "never-uploaded.ts",
			status: http.StatusNotFound,
			why:    "queueing a job that is going to fail is a worse answer than saying so now",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, sign, srv, q := verifyServer(t)
			// Seeded where the row says so, so the refusal is demonstrably the
			// NAME rule rather than the file merely being absent; and NOT
			// seeded on the three ordering rows, where the absence is the
			// point. Both halves are needed: one pins the rule, the other pins
			// that it is applied first.
			if c.seed {
				seedUpload(t, srv, c.path)
			}

			// mustJSONError rather than send: api.go registers the embedded SPA
			// as the root mux's NotFound handler, so an unrouted /api/v1 path
			// answers 404 with HTML in CI and 200 with the bundle on a machine
			// that has run `make ui`. A status-only assertion here would pass
			// with the route deleted.
			mustJSONError(t, h, sign, http.MethodPost,
				"/api/v1/media/"+c.path+"/verify", nil, c.status)
			if got := verifyJobs(t, q); len(got) != 0 {
				t.Fatalf("%q queued %+v; %s", c.path, got, c.why)
			}
		})
	}
}

// A build with no queue answers 503 rather than 404. A client that sees 404
// concludes the server is too old and stops asking; the route exists and the
// capability may be back on the next start.
func TestReCheckingWithoutAQueueIsUnavailableRatherThanMissing(t *testing.T) {
	h, sign, srv, _ := verifyServer(t)
	seedUpload(t, srv, "show-abcd1234.ts")
	srv.jobq = nil

	send(t, h, sign, http.MethodPost, "/api/v1/media/show-abcd1234.ts/verify", nil,
		http.StatusServiceUnavailable)
}

// The kind has to be in the catalogue or the jobs page shows a raw
// "media.verify" with no label, no description and -- because jobKindInfo is
// listed FROM the catalogue -- no per-kind policy control at all, so the
// operator cannot schedule or throttle it.
func TestTheReCheckKindIsInTheCatalogue(t *testing.T) {
	var found bool
	for _, c := range kindCatalogue {
		if c.Kind == uploadverify.Kind {
			found = true
			if c.Label == "" || c.Description == "" {
				t.Errorf("the catalogue entry for %q is blank: label=%q description=%q",
					c.Kind, c.Label, c.Description)
			}
		}
	}
	if !found {
		t.Fatalf("%q is not in kindCatalogue, so the jobs page has no label for it "+
			"and the per-kind policy editor cannot offer it -- an operator cannot "+
			"put it in a window or stop it competing with a live encode",
			uploadverify.Kind)
	}
	if got := kindLabel(uploadverify.Kind); got == string(uploadverify.Kind) {
		t.Errorf("kindLabel(%q) returned the raw kind, so job rows read as an "+
			"identifier rather than as a name", uploadverify.Kind)
	}
}
