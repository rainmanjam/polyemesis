// Package uploadverify re-inspects an upload that is already on disk and
// records what it found.
//
// #202. The Library can say "Not checked" about a file since #118, and until
// now that was the end of the sentence: the only way to get a verdict was to
// upload the bytes again, which for a file that is the operator's only copy
// means finding it on the machine they uploaded it from a year ago. This is the
// job that makes the marker actionable.
//
// WHY IT IS A JOB AND NOT A HANDLER. The inspection is an ffprobe against a file
// that may be several gigabytes, on a box that is also encoding a live
// broadcast. api/playlist_normalise.go argues that out at length for the
// normalise worker and every word of it applies here: nothing is waiting for
// the answer, so nothing should be holding a request open for it, and the
// governor is what decides when the machine can afford it.
//
// ============================================================================
// THE RULE THIS PACKAGE EXISTS TO ENFORCE: IT RECORDS ONLY WHAT IT ESTABLISHED.
// ============================================================================
//
// An inspection has three outcomes (see uploadprobe.Classify) and this worker
// writes exactly two of them. OutcomeVerified and OutcomeRefused are findings
// ABOUT THE FILE and they are recorded, overwriting whatever was there.
// OutcomeUnverified is a finding about THIS SERVER -- no ffprobe, a fork that
// failed, a probe cut short -- and it is NOT WRITTEN AT ALL. The job fails
// instead, with the reason in its error, where an operator can see it.
//
// That asymmetry is the whole design and it is not shared with the upload
// handler, which records all three. The difference is what already exists on
// disk:
//
//   - At upload time there is no record, and "stored without being inspected"
//     is the honest description of a file that has just arrived unread.
//   - Here there IS a record, or a deliberate absence of one. Writing
//     UnverifiedVerdict over an established OutcomeRefused would replace
//     "this server read these bytes and they are not media" with "nobody read
//     this" -- destroying knowledge the server had, and handing the operator
//     back the one remedy that cannot work. Writing it over an upload with NO
//     record would be worse still: every install has uploads that predate
//     verdicts entirely, uploadNotice and playlistUploadProblems both treat
//     "unrecorded" differently from "unverified", and a re-verify that could
//     not run would silently move a year-old working file from one to the
//     other.
//
// So: no record stays no record, and a real verdict is never downgraded by a
// failure to reach one. A re-verify that lies about what it found is worse than
// no re-verify.
//
// WHAT IT DOES NOT DO, stated because the absence is deliberate: it does not
// touch playlists. A file that is refused here while a stored playlist item
// names it leaves that item unusable, and handleDeleteMedia answers 409 while
// the item exists, so the operator must edit the playlist. Recording the
// refusal anyway is the right half of that trade -- suppressing a true finding
// because acting on it is inconvenient is the failure mode this package is
// named after -- but the consequence is real and is written down in #202.
package uploadverify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/uploadprobe"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// Kind is this job's kind.
const Kind jobs.Kind = "media.verify"

// Limit is how many of these may run at once.
//
// One, chosen the way NormaliseLimit and MaxConcurrentUploadProbes are chosen:
// this is work that competes with an encoder that must not stutter, and reading
// four large files off one disk in parallel is not four times the throughput.
const Limit = 1

// ProbeTimeout bounds one inspection.
//
// Much larger than api.probeUploadTimeout's 30 seconds, and the difference is
// the reason that constant is small: there, a REQUEST is held open for the
// duration and a slow probe costs the operator a stalled browser tab, so the
// timeout is tuned against a human's patience. Here nothing is waiting, the
// governor already decided the machine could afford this, and the only thing a
// bound protects is the worker slot -- so it is set where a genuinely hung
// ffprobe is caught and a merely slow one on a multi-gigabyte file is not.
//
// Hitting it produces NO RECORD, exactly like every other failure to inspect.
const ProbeTimeout = 10 * time.Minute

// Target is the canonical jobs.Target for a re-verification.
//
// It names the UPLOAD, which is what makes the queue's Unique fold do the
// deduplication: pressing "Check again" twice produces one job. The spelling is
// shared with playlistmedia.NormaliseTarget on purpose -- the two are about the
// same thing and an operator reading the jobs page should see one noun -- and
// they cannot collide, because Unique folds on Kind AND Target.
func Target(upload string) string { return "upload:" + upload }

// Registry is the part of *jobs.Queue this package needs.
type Registry interface {
	Register(kind jobs.Kind, limit int, w jobs.Worker) error
}

var _ Registry = (*jobs.Queue)(nil)

// Store is the part of *uploads.Store this package needs.
//
// An interface so the worker's every path is reachable in a test without a real
// store, and so this package never re-derives the path confinement that
// internal/uploads already owns -- Resolve is the only way a name here becomes
// a path.
type Store interface {
	// Resolve turns a stored name into an absolute path, refusing anything
	// that escapes the uploads directory.
	Resolve(name string) (string, error)
	// Verdict reports what is currently recorded, and whether anything is.
	Verdict(name string) (uploads.Verdict, bool)
	// PutVerdict records a finding, replacing any earlier one.
	PutVerdict(name string, v uploads.Verdict) error
}

var _ Store = (*uploads.Store)(nil)

// Config is what the processor needs from the rest of the server.
type Config struct {
	// FFprobe is the detected binary. Empty fails the job with a sentence that
	// names the problem rather than preventing startup -- the same fail-open
	// rule registerProcessors applies to every other kind.
	FFprobe string
	// FFmpeg is optional and is what lets a raw elementary stream have its
	// length COUNTED rather than being refused (#218). An install without one
	// refuses those files exactly as it did before.
	FFmpeg string
	// Uploads is the store, normally *uploads.Store. Nil fails the job.
	Uploads Store
}

// Prober is the inspection, as a field, so every path through the worker is
// reachable on a machine with no media and no FFprobe on it. The default is
// ffmpeg.ProbeFile.
type Prober func(ctx context.Context, path string) (*ffmpeg.ProbeResult, error)

// Processor runs the re-verification job.
type Processor struct {
	log   *slog.Logger
	cfg   Config
	probe Prober
	// now is time.Now, as a field only so a test can pin the ProbedAt it
	// records. Unset means time.Now.
	now func() time.Time
}

// Option customises a Processor.
type Option func(*Processor)

// WithProber replaces the inspection.
func WithProber(p Prober) Option { return func(pr *Processor) { pr.probe = p } }

// New builds the processor.
func New(log *slog.Logger, cfg Config, opts ...Option) *Processor {
	p := &Processor{log: log, cfg: cfg}
	for _, o := range opts {
		o(p)
	}
	if p.probe == nil {
		p.probe = func(ctx context.Context, path string) (*ffmpeg.ProbeResult, error) {
			return ffmpeg.ProbeFile(ctx,
				ffmpeg.Bins{FFprobe: p.cfg.FFprobe, FFmpeg: p.cfg.FFmpeg}, path)
		}
	}
	return p
}

// Register wires the worker into a queue.
func (p *Processor) Register(r Registry) error {
	return r.Register(Kind, Limit, jobs.WorkerFunc(p.RunVerify))
}

// Params is a re-verification job's payload.
type Params struct {
	// Upload is the stored name, as the Library lists it.
	Upload string `json:"upload"`
}

// Validate rejects a payload that could never name a stored upload.
func (p Params) Validate() error {
	name := strings.TrimSpace(p.Upload)
	switch {
	case name == "":
		return errors.New("no upload was named")
	case name != p.Upload:
		return fmt.Errorf("upload name %q has surrounding whitespace", p.Upload)
	case strings.ContainsAny(name, `/\`):
		// A SEPARATOR IS REFUSED HERE AS WELL AS AT Resolve, and the redundancy
		// is the point rather than an oversight. uploads.Store.Resolve is the
		// confinement and it is what actually stops a traversal; this stops the
		// traversal from ever being WRITTEN INTO A JOB ROW, which outlives the
		// process, survives a restart, and is re-executed by Recover against
		// whatever Resolve looks like by then. Both separators, because
		// filepath.Base(`..\victim.ts`) is `victim.ts` on Windows -- the same
		// platform split handleDeleteMedia's ordering comment names.
		return fmt.Errorf("upload name %q contains a path separator", name)
	case !uploads.Listable(name):
		// Listable is the same question the Library's listing asks, so a
		// sidecar, a ".partial-" staging file or a name reservation cannot be
		// addressed here. A probe against `.probe-<name>.json` followed by a
		// verdict written beside it is a file this product has no other way to
		// create.
		return fmt.Errorf("%q is not a stored upload", name)
	}
	return nil
}

// NewJob builds the job.
//
// Unique on the upload, so asking twice asks once. PriorityUser, because the
// only thing that submits one of these is an operator who is looking at the row
// and waiting to find out: this is a header read, not a transcode, and putting
// it behind a bulk archive sweep would make the button appear broken.
func NewJob(params Params) (jobs.Job, error) {
	if err := params.Validate(); err != nil {
		return jobs.Job{}, err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return jobs.Job{}, fmt.Errorf("encode %s params: %w", Kind, err)
	}
	return jobs.Job{
		Kind:     Kind,
		Target:   Target(params.Upload),
		Params:   raw,
		Priority: jobs.PriorityUser,
		Unique:   true,
	}.Normalized(), nil
}

// Result is what the job hands back, and it is written for a human reading the
// jobs page rather than for a machine.
type Result struct {
	Upload string `json:"upload"`
	// Outcome is what was recorded: "verified" or "refused". Never
	// "unverified" -- see the package comment.
	Outcome uploads.Outcome `json:"outcome"`
	// Reason is the operator-facing sentence for a refusal, empty otherwise.
	Reason string `json:"reason,omitempty"`
	// Previous is what was recorded before, or "unrecorded". This is the field
	// that makes the row worth reading: "refused -> verified" is the whole
	// point of the feature and "refused -> refused" says the second look
	// agreed.
	Previous uploads.Outcome `json:"previous"`
	// Changed reports whether the outcome moved.
	Changed bool `json:"changed"`
}

// RunVerify inspects one stored upload and records the finding.
func (p *Processor) RunVerify(ctx context.Context, job jobs.Job, rep jobs.Reporter) error {
	var params Params
	if err := json.Unmarshal(paramsOf(job), &params); err != nil {
		return jobs.Permanent(fmt.Errorf("decode %s params: %w", Kind, err))
	}
	if err := params.Validate(); err != nil {
		return jobs.Permanent(err)
	}
	name := params.Upload

	// EVERY GUARD BELOW RETURNS BEFORE ANYTHING IS WRITTEN. That ordering is
	// the package's rule made structural rather than remembered: there is no
	// path from a failure to a PutVerdict.
	if p.cfg.Uploads == nil {
		return jobs.Permanent(errors.New("no upload store is configured, so nothing can be re-checked"))
	}
	if p.cfg.FFprobe == "" {
		// Permanent, and it is worth saying why rather than retrying: the
		// binaries are detected once at startup, so no number of attempts
		// against this process will find one. The operator installs ffprobe and
		// restarts, and the failed job tells them that in as many words.
		return jobs.Permanent(errors.New(
			"ffprobe was not detected on this server, so this file cannot be inspected; " +
				"install FFmpeg and restart, then check again"))
	}
	path, err := p.cfg.Uploads.Resolve(name)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("%s cannot be located: %w", name, err))
	}
	if _, err := os.Stat(path); err != nil {
		// The file went away between submission and execution -- deleted, or on
		// a volume that is not mounted. NOT a verdict about anything: there are
		// no bytes to have a verdict about, and a sidecar left describing a file
		// that no longer exists is a lie with a longer life than this job.
		if errors.Is(err, os.ErrNotExist) {
			return jobs.Permanent(fmt.Errorf(
				"%s is no longer in the uploads directory, so there is nothing to check", name))
		}
		return fmt.Errorf("%s could not be opened: %w", name, err)
	}

	before, hadBefore := p.cfg.Uploads.Verdict(name)
	previous := uploads.OutcomeUnrecorded
	if hadBefore {
		previous = before.Outcome
	}

	rep.Logf("inspecting %s (currently %s)", name, previous)
	rep.Progress(0.1)

	probeCtx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	res, probeErr := p.probe(probeCtx, path)

	// ctx.Err() of the PROBE's context, not the job's: the question this answers
	// is "was this inspection cut short", and exec.CommandContext reports a
	// killed child as a bare *exec.ExitError carrying no context error at all.
	verdict := uploadprobe.Classify(res, probeErr, probeCtx.Err())
	rep.Progress(0.9)

	if verdict.Outcome == uploads.OutcomeUnverified {
		// THE ARM THE PACKAGE EXISTS FOR. The inspection did not happen, so
		// there is no finding, so NOTHING IS RECORDED -- not even to say the
		// attempt was made. Whatever was on disk before this job ran is still
		// on disk, including nothing at all.
		//
		// Not Permanent: a fork that failed with EAGAIN under a live encode, or
		// a shutdown that cancelled the probe, is exactly the shape a retry
		// fixes. The attempt ceiling bounds it, and a job that exhausts it is
		// visible on the jobs page carrying this sentence.
		p.log.Warn("a re-check established nothing; the existing record is left alone",
			"name", name, "reason", verdict.Reason, "previous", previous, "err", probeErr)
		rep.Logf("nothing was established, so %s is left recorded as %s", name, previous)
		return fmt.Errorf("%s could not be inspected (%s), so nothing was recorded about it",
			name, verdict.Reason)
	}

	if err := p.cfg.Uploads.PutVerdict(name, verdict); err != nil {
		return fmt.Errorf("recording the verdict for %s: %w", name, err)
	}

	changed := verdict.Outcome != previous
	// Logged at Info rather than Debug because both directions matter to
	// somebody: a refusal newly recorded is a file that just became unusable as
	// a playlist item, and a refusal cleared is a file that just became usable.
	p.log.Info("re-checked an upload",
		"name", name, "outcome", verdict.Outcome, "previous", previous, "changed", changed)
	switch {
	case verdict.Outcome == uploads.OutcomeVerified && previous == uploads.OutcomeRefused:
		rep.Logf("%s passed this time; the earlier refusal has been replaced", name)
	case verdict.Outcome == uploads.OutcomeRefused && previous == uploads.OutcomeRefused:
		rep.Logf("%s was refused again: %s", name, verdict.Reason)
	case verdict.Outcome == uploads.OutcomeRefused:
		rep.Logf("%s was inspected and refused: %s", name, verdict.Reason)
	default:
		rep.Logf("%s was inspected and accepted", name)
	}
	rep.SetResult(Result{
		Upload:   name,
		Outcome:  verdict.Outcome,
		Reason:   verdict.Reason,
		Previous: previous,
		Changed:  changed,
	})
	rep.Progress(1)
	return nil
}

// paramsOf gives Unmarshal something to chew on for a job whose Params were
// never set. jobs.Job.Normalized fills "{}", but a job built by hand in a test
// or recovered from an older row may not have been through it.
func paramsOf(job jobs.Job) []byte {
	if len(job.Params) == 0 {
		return []byte("{}")
	}
	return job.Params
}
