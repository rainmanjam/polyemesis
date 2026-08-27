// Package jobs is the durable background work queue that every heavy task in
// polyemesis runs on.
//
// The governing rule of this subsystem: nothing here may ever degrade the live
// stream. Transcription, proxy generation and re-encoding are all CPU hungry,
// and a dropped frame on a live broadcast is unrecoverable while a transcript
// arriving an hour later costs nothing. So heavy work is never done inline. It
// is queued, it is bounded, and it yields.
//
// Durability is the other half of that promise. A four-hour transcription that
// vanishes because the server bounced is worse than useless, so state lives in
// SQLite and a job that was mid-flight when the process died is requeued at
// startup rather than orphaned.
//
// The queue knows nothing about whisper, FFmpeg or scene detection. Processors
// register themselves against a Kind and are handed a context they must die
// on; everything domain-specific lives on their side of the Worker interface.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Kind names a class of work. The queue never interprets one — processors
// define their own constants and register against them.
type Kind string

// State is where a job is in its life.
type State string

const (
	// StateQueued is waiting for a free slot.
	StateQueued State = "queued"
	// StateRunning has a worker on it right now.
	StateRunning State = "running"
	// StateDone finished successfully.
	StateDone State = "done"
	// StateFailed gave up: either a permanent failure or the attempt ceiling.
	StateFailed State = "failed"
	// StateCancelled was stopped by a human.
	StateCancelled State = "cancelled"
	// StateDeferred is eligible work the resource policy is holding back so
	// the live stream keeps the machine. It becomes claimable again of its own
	// accord once AvailableAt passes, so a governor that dies mid-deferral
	// cannot strand work forever.
	StateDeferred State = "deferred"
)

// AllStates is every State declared above, in declaration order.
//
// Poka-yoke audit #15: State is a string, not an iota, so Go gives no
// exhaustiveness check over it at all -- a switch with an explicit case list
// silently falls to its default for a state nobody remembered to add. This
// list is what breaks that silence: TestAllStatesIsTheWholeConstBlock
// (jobs_states_test.go) AST-parses this file and fails if AllStates ever
// drifts from the const block, and Valid and stateTerminal below are both
// driven by it rather than carrying their own case list to go stale.
//
// Converting State itself to an iota was considered and rejected: it is
// persisted as the literal string ("queued", "running", ...) in the jobs
// table and read back with jobs.State(state) (internal/db/jobs.go), and
// carried over the API as JSON via the same string. An iota changes both
// wire formats the moment MarshalJSON stops being the free string conversion
// it is today, and neither internal/db nor internal/api is this fix's to
// touch. The list-plus-test below is the affordable device; RULE 3 in the
// fix brief calls that Warning against Control if it announces itself
// immediately at `go test ./...` -- which TestAllStatesIsTheWholeConstBlock
// does not defer to production, so it stands with the same weight as
// events.AllTypes()'s guard in internal/api/ws_policy.go, the pattern this
// copies.
func AllStates() []State {
	return []State{
		StateQueued,
		StateRunning,
		StateDone,
		StateFailed,
		StateCancelled,
		StateDeferred,
	}
}

// stateTerminal is the closed table of which states end a job's life, kept as
// a map rather than a switch's case list so TestEveryStateHasATerminalEntry
// can force every member of AllStates() to have an explicit answer here. A
// state missing from a switch's cases falls through to that switch's default
// with no signal; a state missing from this map is caught by that test
// instead, by checking for the key's PRESENCE rather than trusting its
// zero-value bool -- an unclassified state must fail loudly, not read as
// "not terminal" by accident.
var stateTerminal = map[State]bool{
	StateQueued:    false,
	StateRunning:   false,
	StateDone:      true,
	StateFailed:    true,
	StateCancelled: true,
	StateDeferred:  false,
}

// Terminal reports whether a job in this state will never run again.
func (s State) Terminal() bool {
	return stateTerminal[s]
}

// Valid reports whether s is a state this package writes.
//
// Driven by AllStates rather than its own case list: a state added to the
// const block and to AllStates is valid here automatically, with nothing
// left for an author to forget in a second place.
func (s State) Valid() bool {
	for _, v := range AllStates() {
		if s == v {
			return true
		}
	}
	return false
}

// Priority orders the queue. Higher runs first; equal priorities are FIFO.
type Priority int

const (
	// PriorityBulk is for work nobody is waiting on — a backfill sweep over
	// every old recording.
	PriorityBulk Priority = -10
	// PriorityNormal is the default: work that was triggered automatically.
	PriorityNormal Priority = 0
	// PriorityUser is for work a human asked for and is watching for.
	PriorityUser Priority = 10

	// MinPriority and MaxPriority bound the stored value. They exist only to
	// keep a typo out of the database, not to express policy.
	MinPriority Priority = -100
	MaxPriority Priority = 100
)

// Retry and size bounds.
const (
	// DefaultMaxAttempts is deliberately small. A transcode that failed twice
	// for a retryable reason usually has a third reason waiting, and a job
	// that retries forever is indistinguishable from a hung one.
	DefaultMaxAttempts = 3
	MinMaxAttempts     = 1
	MaxMaxAttempts     = 20

	// MaxLogLines is how much of the worker's chatter is kept. It is a tail,
	// not a transcript: enough for a human to see why something failed,
	// bounded so a chatty encoder cannot grow a row without limit.
	MaxLogLines = 200
	// MaxLogLineLen truncates one absurd line rather than dropping it.
	MaxLogLineLen = 1000

	MaxKindLen   = 64
	MaxTargetLen = 512
	// MaxParamsBytes bounds the JSON a caller can attach to a job.
	MaxParamsBytes = 64 << 10
	// MaxResultBytes bounds what a worker can hand back.
	MaxResultBytes = 64 << 10
)

// RecordingTarget is the canonical Target for a job about one recording.
// Every processor in this workstream must use it, because "list the jobs for
// this recording" is a string comparison and two spellings would silently
// return nothing.
func RecordingTarget(id int64) string { return "recording:" + strconv.FormatInt(id, 10) }

// ParseRecordingTarget is the inverse. ok is false for a target that names
// something other than a recording.
func ParseRecordingTarget(target string) (id int64, ok bool) {
	rest, found := strings.CutPrefix(target, "recording:")
	if !found {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// Job is one unit of durable background work.
type Job struct {
	ID   int64 `json:"id"`
	Kind Kind  `json:"kind"`
	// Target is what the job is about, usually RecordingTarget(id). It is
	// free-form so a processor can address a clip or a path, and it is what
	// Unique dedupes on.
	Target string `json:"target"`
	// Params is the processor's own JSON. The queue stores and returns it and
	// never looks inside.
	Params json.RawMessage `json:"params,omitempty"`
	// Result is what the worker handed back — an output path, a duration, a
	// track count. Also opaque here.
	Result json.RawMessage `json:"result,omitempty"`

	Priority Priority `json:"priority"`
	State    State    `json:"state"`
	// Unique asks the queue to fold a submission into an already-active job
	// with the same Kind and Target instead of creating a second one. It is
	// how clicking "transcribe" twice does not transcribe twice.
	Unique bool `json:"unique,omitempty"`

	// Attempts counts how many times this job has been STARTED, not how many
	// times it has failed. A process that died mid-job therefore consumed one,
	// which is exactly what stops a job that crashes the server from crashing
	// it forever.
	Attempts    int `json:"attempts"`
	MaxAttempts int `json:"maxAttempts"`

	// Progress is 0..1, best effort, reported by the worker.
	Progress float64 `json:"progress"`
	// Log is the tail of the worker's human-readable output.
	Log []string `json:"log,omitempty"`
	// Error is why the last attempt ended, kept even while retrying so the
	// operator can see what is going wrong before the ceiling is hit.
	Error string `json:"error,omitempty"`

	CreatedAt time.Time `json:"createdAt"`

	// THESE THREE ARE `omitzero`, NOT `omitempty`, AND THE DIFFERENCE IS THE
	// WHOLE POINT.
	//
	// `omitempty` DOES NOTHING ON A time.Time. encoding/json has no empty case
	// for a struct, so an unset instant marshalled as "0001-01-01T00:00:00Z" --
	// a NON-EMPTY string that parses cleanly, so every client guard of the form
	// `job.startedAt && ...` passed and rendered it. Through a local-time
	// offset that is 12/31/1, 16:07:02. A QUEUED job served three of them at
	// once: it has not been deferred, has not started and has not finished, so
	// availableAt, startedAt and finishedAt were all year 1 on the same row.
	//
	// Go 1.24's `omitzero` calls IsZero and drops the key, so the zero cannot
	// reach the wire at all -- the shape refuses to serialise it rather than
	// the reader coping with it. Pointers would fix it too and would break the
	// store: internal/db/jobs.go assigns and compares these as values, and
	// Reset (jobs.go there) clears FinishedAt with `time.Time{}`.
	//
	// ui/src/lib/types.ts already declares all three optional, so the key going
	// missing is what the client was always typed for.
	//
	// AvailableAt is the earliest this job may be claimed. It carries both
	// retry backoff and resource-policy deferral, so there is one mechanism
	// holding work back rather than two.
	AvailableAt time.Time `json:"availableAt,omitzero"`
	StartedAt   time.Time `json:"startedAt,omitzero"`
	FinishedAt  time.Time `json:"finishedAt,omitzero"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Normalized fills defaults and clamps the rest, so a caller that only set
// Kind and Target gets a job the store will accept.
func (j Job) Normalized() Job {
	j.Kind = Kind(strings.TrimSpace(string(j.Kind)))
	j.Target = strings.TrimSpace(j.Target)
	if j.State == "" {
		j.State = StateQueued
	}
	if j.MaxAttempts == 0 {
		j.MaxAttempts = DefaultMaxAttempts
	}
	j.MaxAttempts = clampInt(j.MaxAttempts, MinMaxAttempts, MaxMaxAttempts)
	j.Priority = Priority(clampInt(int(j.Priority), int(MinPriority), int(MaxPriority)))
	if j.Attempts < 0 {
		j.Attempts = 0
	}
	j.Progress = ClampProgress(j.Progress)
	if len(j.Params) == 0 {
		j.Params = json.RawMessage("{}")
	}
	j.Log = TrimLog(j.Log)
	return j
}

// Validate rejects what must never reach the database. It runs on write, not
// on read: a row that somehow got in is shown to the operator rather than
// taking the whole list down with it.
func (j Job) Validate() error {
	switch {
	case j.Kind == "":
		return errors.New("job kind is required")
	case len(j.Kind) > MaxKindLen:
		return fmt.Errorf("job kind is longer than %d characters", MaxKindLen)
	case len(j.Target) > MaxTargetLen:
		return fmt.Errorf("job target is longer than %d characters", MaxTargetLen)
	case !j.State.Valid():
		return fmt.Errorf("unknown job state %q", j.State)
	case len(j.Params) > MaxParamsBytes:
		return fmt.Errorf("job params are larger than %d bytes", MaxParamsBytes)
	case len(j.Result) > MaxResultBytes:
		return fmt.Errorf("job result is larger than %d bytes", MaxResultBytes)
	case len(j.Params) > 0 && !json.Valid(j.Params):
		return errors.New("job params are not valid JSON")
	case len(j.Result) > 0 && !json.Valid(j.Result):
		return errors.New("job result is not valid JSON")
	case j.MaxAttempts < MinMaxAttempts || j.MaxAttempts > MaxMaxAttempts:
		return fmt.Errorf("maxAttempts must be between %d and %d", MinMaxAttempts, MaxMaxAttempts)
	}
	return nil
}

// Exhausted reports whether another attempt is allowed.
func (j Job) Exhausted() bool { return j.Attempts >= j.MaxAttempts }

// TrimLog bounds a log tail: the newest MaxLogLines lines, each no longer than
// MaxLogLineLen.
func TrimLog(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > MaxLogLines {
		lines = lines[len(lines)-MaxLogLines:]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimRight(l, "\r\n")
		if len(l) > MaxLogLineLen {
			l = l[:MaxLogLineLen] + "…"
		}
		out = append(out, l)
	}
	return out
}

// Filter selects jobs for a listing. A zero Filter means everything.
type Filter struct {
	States []State
	Kinds  []Kind
	Target string
	// Limit caps the result; 0 means no cap.
	Limit int
}

// Active is the filter for work that has not finished, which is what a status
// page and the Unique check both want.
func Active() Filter {
	return Filter{States: []State{StateQueued, StateRunning, StateDeferred}}
}

// permanentError marks a failure that will never succeed on a retry: the file
// is gone, the input codec is unsupported, the parameters are nonsense.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks err as not worth retrying. Retrying a permanent failure
// forever is a bug: it burns the machine the live stream needs on work that
// cannot succeed.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

// IsPermanent reports whether err was marked by Permanent.
//
// Note the direction of the default: an error nobody classified is treated as
// RETRYABLE. A check that is wrong in the restrictive direction — declaring a
// transient disk-busy error permanent and throwing the job away — is worse
// than one that is wrong in the generous direction, because the attempt
// ceiling already bounds the generous case.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// Reporter is how a running worker talks back to the queue. Calls are
// coalesced, so a worker may call Progress as often as it likes.
type Reporter interface {
	// Progress records fractional completion, clamped to 0..1.
	Progress(fraction float64)
	// Logf appends one line to the job's log tail.
	Logf(format string, args ...any)
	// SetResult stores the worker's output description. It must be valid
	// JSON; anything else is dropped with a log line rather than failing the
	// job, because a finished transcode is not made worthless by a bad
	// summary of it.
	SetResult(v any)
}

// Worker processes one Kind of job.
//
// Two rules, both load-bearing:
//
//   - Run must return promptly when ctx is done, and must kill any child
//     process it started before it does. Cancellation that leaks an FFmpeg is
//     cancellation that still competes with the live stream.
//   - Return Permanent(err) for a failure a retry cannot fix. Anything else is
//     retried until the attempt ceiling.
type Worker interface {
	Run(ctx context.Context, job Job, rep Reporter) error
}

// WorkerFunc adapts a function to Worker.
type WorkerFunc func(ctx context.Context, job Job, rep Reporter) error

// Run implements Worker.
func (f WorkerFunc) Run(ctx context.Context, job Job, rep Reporter) error {
	return f(ctx, job, rep)
}

// Store is the durable half of the queue. An interface rather than *db.DB so
// the queue is testable without SQLite, and so the database package can own
// the SQL without the queue knowing any.
//
// Implementations must treat ClaimJob as atomic: it is the only method two
// callers could race on.
type Store interface {
	// EnqueueJob stores a new job and returns it with its ID and timestamps.
	EnqueueJob(j Job) (*Job, error)
	// GetJob loads one job.
	GetJob(id int64) (*Job, error)
	// ListJobs returns jobs matching f, newest first.
	ListJobs(f Filter) ([]Job, error)
	// FindActiveJob returns the queued, running or deferred job with this kind
	// and target, or (nil, nil) when there is none. It is what makes
	// Job.Unique work.
	FindActiveJob(kind Kind, target string) (*Job, error)

	// ClaimJob atomically takes the highest-priority eligible job whose kind
	// is in kinds and marks it running, counting an attempt. It returns
	// (nil, nil) — not an error — when there is nothing to do, because an idle
	// queue is the normal case and must not look like a fault.
	ClaimJob(kinds []Kind, now time.Time) (*Job, error)

	// UpdateJobProgress records progress and appends log lines. Either may be
	// empty; progress below zero means "leave it alone".
	UpdateJobProgress(id int64, progress float64, logLines []string, now time.Time) error
	// SetJobResult stores the worker's output description.
	SetJobResult(id int64, result json.RawMessage, now time.Time) error

	// FinishJob is the terminal write. A job finished as StateDone also has
	// its progress set to 1, so a worker that forgot to report the last chunk
	// does not leave a bar at 97%.
	FinishJob(id int64, state State, errText string, now time.Time) error
	// RescheduleJob returns a failed-but-retryable job to the queue at
	// availableAt. The attempt was already counted by ClaimJob.
	RescheduleJob(id int64, availableAt time.Time, errText string, now time.Time) error
	// RequeueJob returns a running job to the queue AND refunds the attempt.
	// It is for the one case where the queue knows the attempt never got a
	// fair run: its own clean shutdown.
	RequeueJob(id int64, availableAt time.Time, reason string, now time.Time) error
	// RequeueRunningJobs is crash recovery, run once at startup. Rows left in
	// StateRunning by a process that died are requeued; ones that have already
	// burned their attempts are failed rather than requeued forever.
	RequeueRunningJobs(now time.Time) (requeued, failed int, err error)

	// CancelJob marks a non-terminal job cancelled. Cancelling a job that is
	// running under this process is the Queue's business, not the store's.
	CancelJob(id int64, now time.Time) error
	// DeferJob holds an eligible job back until at, for the resource policy.
	DeferJob(id int64, at time.Time, reason string, now time.Time) error
	// RetryJob re-arms a terminal job with a fresh attempt budget.
	RetryJob(id int64, now time.Time) error
	// DeleteJob removes a job row.
	DeleteJob(id int64) error
	// PurgeJobs deletes terminal jobs that finished before cutoff, keeping at
	// least keep of the newest regardless of age. It returns the rows it
	// deleted so a caller can clean up whatever they owned outside the
	// database -- a count names nothing, and the row is the last reference.
	PurgeJobs(cutoff time.Time, keep int) ([]Job, error)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ClampProgress bounds a progress fraction to 0..1.
//
// NaN is caught explicitly because it fails every comparison, so a clamp alone
// would let it straight through and a worker's arithmetic mistake would become
// a progress bar nothing could render.
func ClampProgress(f float64) float64 {
	if math.IsNaN(f) || f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
