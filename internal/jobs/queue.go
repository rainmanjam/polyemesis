package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Queue defaults.
const (
	// DefaultTick is how often the queue looks for work it was not told
	// about — a deferral that expired, a backoff that elapsed. Submissions and
	// completions wake it immediately, so this is a safety net rather than the
	// main path and can be coarse.
	DefaultTick = time.Second

	// DefaultConcurrency is one. This is the governing principle expressed as
	// a number: the machine's job is to keep the stream up, and a second
	// transcode running beside the first buys throughput nobody asked for at a
	// risk nobody accepted. Operators with headroom raise it deliberately.
	DefaultConcurrency = 1

	// DefaultBackoff and DefaultMaxBackoff bound retry waits. The base is long
	// enough that a busy disk has actually stopped being busy.
	DefaultBackoff    = 15 * time.Second
	DefaultMaxBackoff = 15 * time.Minute

	// DefaultProgressInterval coalesces progress writes. A transcoder reports
	// per frame; SQLite does not need to hear it per frame.
	DefaultProgressInterval = 2 * time.Second
)

// Stats is a snapshot of what the queue has done, for a status endpoint.
type Stats struct {
	Running   int            `json:"running"`
	Paused    bool           `json:"paused"`
	Started   int64          `json:"started"`
	Completed int64          `json:"completed"`
	Failed    int64          `json:"failed"`
	Retried   int64          `json:"retried"`
	Cancelled int64          `json:"cancelled"`
	Requeued  int64          `json:"requeued"`
	ByKind    map[string]int `json:"byKind,omitempty"`
}

// Option configures a Queue.
type Option func(*Queue)

// WithClock replaces time.Now, for tests.
func WithClock(fn func() time.Time) Option {
	return func(q *Queue) {
		if fn != nil {
			q.now = fn
		}
	}
}

// WithTick sets the idle poll interval.
func WithTick(d time.Duration) Option {
	return func(q *Queue) {
		if d > 0 {
			q.tick = d
		}
	}
}

// WithConcurrency sets the global limit on jobs running at once, across every
// kind.
func WithConcurrency(n int) Option {
	return func(q *Queue) {
		if n > 0 {
			q.limit = n
		}
	}
}

// WithBackoff bounds the retry wait.
func WithBackoff(base, max time.Duration) Option {
	return func(q *Queue) {
		if base > 0 {
			q.backoff = base
		}
		if max > 0 {
			q.maxBackoff = max
		}
	}
}

// WithProgressInterval sets how often a worker's progress reaches the database.
func WithProgressInterval(d time.Duration) Option {
	return func(q *Queue) {
		if d >= 0 {
			q.progressEvery = d
		}
	}
}

// WithOnChange registers a callback fired on every state transition, for the
// event bus. It must not block.
func WithOnChange(fn func(Job)) Option { return func(q *Queue) { q.onChange = fn } }

type registration struct {
	worker Worker
	// limit is this kind's own concurrency ceiling; 0 means it is bounded only
	// by the global one.
	limit int
}

// execution is one job currently running under this process.
type execution struct {
	job    Job
	cancel context.CancelFunc
	// cancelled distinguishes a human stopping this job from the server
	// shutting down. They look identical to the worker and must not look
	// identical to the queue: one is terminal, the other is resumable.
	cancelled bool
}

// Queue dispatches durable jobs to registered workers.
//
// It owns no timers beyond one idle ticker and knows nothing about what the
// work is. Everything domain-specific is on the far side of Worker, and
// everything durable is on the far side of Store.
type Queue struct {
	log   *slog.Logger
	store Store
	now   func() time.Time

	tick          time.Duration
	limit         int
	backoff       time.Duration
	maxBackoff    time.Duration
	progressEvery time.Duration
	onChange      func(Job)

	// dispatchMu serialises Tick so two wakeups cannot both fill the last slot.
	dispatchMu sync.Mutex

	mu      sync.Mutex
	workers map[Kind]registration
	running map[int64]*execution
	byKind  map[Kind]int
	paused  bool
	stats   Stats
	// admit is the resource policy's veto, asked at the moment of claiming.
	// Nil means every registered kind may start, which is what an ungoverned
	// queue is.
	admit func(Kind) bool

	wakeCh chan struct{}
	wg     sync.WaitGroup
}

// New creates a Queue. It touches neither the database nor a goroutine until
// Run is called, so a caller can register workers first.
func New(log *slog.Logger, store Store, opts ...Option) *Queue {
	if log == nil {
		log = slog.Default()
	}
	q := &Queue{
		log:           log,
		store:         store,
		now:           time.Now,
		tick:          DefaultTick,
		limit:         DefaultConcurrency,
		backoff:       DefaultBackoff,
		maxBackoff:    DefaultMaxBackoff,
		progressEvery: DefaultProgressInterval,
		workers:       map[Kind]registration{},
		running:       map[int64]*execution{},
		byKind:        map[Kind]int{},
		wakeCh:        make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(q)
	}
	return q
}

// Register attaches a worker to a kind. limit is that kind's own concurrency
// ceiling; zero means it is bounded only by the global one.
//
// Registering twice for one kind is an error rather than a silent replacement:
// two processors fighting over a kind is a wiring bug and a queue that hides it
// runs the wrong one.
func (q *Queue) Register(kind Kind, limit int, w Worker) error {
	if kind == "" {
		return errors.New("job kind is required")
	}
	if w == nil {
		return fmt.Errorf("worker for kind %q is nil", kind)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, dup := q.workers[kind]; dup {
		return fmt.Errorf("a worker is already registered for kind %q", kind)
	}
	q.workers[kind] = registration{worker: w, limit: limit}
	return nil
}

// Submit enqueues a job. created is false when Unique folded it into a job
// that was already active, in which case that job is returned.
func (q *Queue) Submit(j Job) (job *Job, created bool, err error) {
	j = j.Normalized()
	// A submission is always queued, whatever the caller filled in: nobody
	// gets to insert a row that is already "running".
	j.State = StateQueued
	j.Attempts = 0
	if err := j.Validate(); err != nil {
		return nil, false, err
	}
	if j.Unique && j.Target != "" {
		existing, err := q.store.FindActiveJob(j.Kind, j.Target)
		if err != nil {
			return nil, false, err
		}
		if existing != nil {
			return existing, false, nil
		}
	}
	out, err := q.store.EnqueueJob(j)
	if err != nil {
		return nil, false, err
	}
	q.wake()
	return out, true, nil
}

// Run dispatches until ctx ends, then waits for the workers it started.
//
// It recovers orphaned jobs first, before any new work is claimed: a restart
// must resume what was interrupted, not bury it under whatever arrived since.
func (q *Queue) Run(ctx context.Context) {
	if q == nil {
		return
	}
	q.Recover()

	t := time.NewTicker(q.tick)
	defer t.Stop()
	for {
		q.Tick(ctx)
		select {
		case <-ctx.Done():
			// Workers observe the same cancellation and requeue themselves;
			// waiting here is what makes shutdown ordered rather than a race
			// between the process exiting and the rows being written.
			q.wg.Wait()
			return
		case <-t.C:
		case <-q.wakeCh:
		}
	}
}

// Recover requeues jobs a dead process left marked running. It is called by
// Run and exported so the behaviour is testable without a ticker.
func (q *Queue) Recover() (requeued, failed int) {
	r, f, err := q.store.RequeueRunningJobs(q.now())
	if err != nil {
		q.log.Error("cannot recover interrupted jobs", "err", err)
		return 0, 0
	}
	if r > 0 || f > 0 {
		q.log.Info("recovered jobs interrupted by a restart", "requeued", r, "failed", f)
		q.mu.Lock()
		q.stats.Requeued += int64(r)
		q.stats.Failed += int64(f)
		q.mu.Unlock()
	}
	return r, f
}

// Tick claims and starts as much work as the limits allow, and reports how
// many jobs it started.
//
// Exported and taking a context so the whole dispatch behaviour — priority
// order, global and per-kind limits, pausing — is a table test with no ticker
// and no sleep in it.
func (q *Queue) Tick(ctx context.Context) int {
	if q == nil {
		return 0
	}
	q.dispatchMu.Lock()
	defer q.dispatchMu.Unlock()

	started := 0
	for {
		if ctx.Err() != nil {
			return started
		}
		kinds := q.eligibleKinds()
		if len(kinds) == 0 {
			return started
		}
		j, err := q.store.ClaimJob(kinds, q.now())
		if err != nil {
			q.log.Warn("cannot claim a job", "err", err)
			return started
		}
		if j == nil {
			return started
		}
		if q.start(ctx, *j) {
			started++
		}
	}
}

// SetAdmit installs the resource policy's veto.
//
// This exists because deferral alone is retroactive and therefore raced. The
// queue wakes immediately on Submit and again the instant a job finishes, and
// both of those happen between two governor ticks; a queue that consulted only
// the stored AvailableAt would claim and start a job the governor was about to
// hold back — which, measured against a live ingest, is the whole feature
// failing silently. Asking at the moment of claiming closes that window.
//
// The callback must not block and must fail OPEN: a policy that cannot decide
// has to let work through, or an unanswerable question becomes a stopped queue.
// Nil clears it.
func (q *Queue) SetAdmit(fn func(Kind) bool) {
	q.mu.Lock()
	q.admit = fn
	q.mu.Unlock()
	// A policy that just opened may have work waiting on it, and the idle
	// ticker is up to a second away.
	q.wake()
}

// eligibleKinds is the set of kinds that could start right now: registered,
// under their own limit, admitted by the resource policy, with the global limit
// not yet reached. Sorted so a test sees a deterministic claim query.
func (q *Queue) eligibleKinds() []Kind {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.paused || len(q.running) >= q.limit {
		return nil
	}
	out := make([]Kind, 0, len(q.workers))
	for k, reg := range q.workers {
		if reg.limit > 0 && q.byKind[k] >= reg.limit {
			continue
		}
		if q.admit != nil && !q.admit(k) {
			continue
		}
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// start launches one claimed job, and reports whether it really started.
func (q *Queue) start(parent context.Context, j Job) bool {
	q.mu.Lock()
	reg, ok := q.workers[j.Kind]
	q.mu.Unlock()
	if !ok {
		// Claimed a kind nothing can run. Put it back with the attempt
		// refunded rather than failing it: an unregistered kind is a startup
		// ordering problem, not a bad job, and failing it would throw away
		// work because a processor was slow to wire itself up.
		if err := q.store.RequeueJob(j.ID, q.now().Add(q.tick),
			"no worker is registered for this kind yet", q.now()); err != nil {
			q.log.Warn("cannot requeue a job with no worker", "job", j.ID, "kind", j.Kind, "err", err)
		}
		return false
	}

	ctx, cancel := context.WithCancel(parent)
	ex := &execution{job: j, cancel: cancel}

	q.mu.Lock()
	q.running[j.ID] = ex
	q.byKind[j.Kind]++
	q.stats.Started++
	q.mu.Unlock()

	j.State = StateRunning
	q.emit(j)
	q.log.Info("job started", "job", j.ID, "kind", string(j.Kind), "target", j.Target,
		"attempt", j.Attempts, "of", j.MaxAttempts)

	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		defer cancel()
		rep := newReporter(q, j.ID)
		err := runWorker(ctx, reg.worker, j, rep)
		rep.flush()
		q.finish(parent, ex, err)
	}()
	return true
}

// runWorker turns a panicking processor into a failed job instead of a dead
// server. The panic is retryable, not permanent: the attempt ceiling already
// bounds it, and a crash on one segment of a long recording is often a crash
// on that segment only.
func runWorker(ctx context.Context, w Worker, j Job, rep Reporter) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("worker panicked: %v", r)
		}
	}()
	return w.Run(ctx, j, rep)
}

func (q *Queue) finish(parent context.Context, ex *execution, runErr error) {
	now := q.now()
	j := ex.job

	q.mu.Lock()
	cancelled := ex.cancelled
	q.mu.Unlock()

	var (
		state State
		text  string
		err   error
	)
	switch {
	case runErr == nil:
		state = StateDone
		err = q.store.FinishJob(j.ID, StateDone, "", now)
		q.bump(func(s *Stats) { s.Completed++ })
		q.log.Info("job finished", "job", j.ID, "kind", string(j.Kind))

	case cancelled:
		state, text = StateCancelled, runErr.Error()
		err = q.store.FinishJob(j.ID, StateCancelled, text, now)
		q.bump(func(s *Stats) { s.Cancelled++ })
		q.log.Info("job cancelled", "job", j.ID, "kind", string(j.Kind))

	case parent.Err() != nil:
		// Our own shutdown, not the job's fault. The attempt is refunded and
		// the job goes back to the front of the queue, because a four-hour
		// transcription that dies with the server must resume, not vanish.
		state = StateQueued
		err = q.store.RequeueJob(j.ID, now, "interrupted by server shutdown", now)
		q.bump(func(s *Stats) { s.Requeued++ })
		q.log.Info("job requeued for shutdown", "job", j.ID, "kind", string(j.Kind))

	case IsPermanent(runErr):
		state, text = StateFailed, runErr.Error()
		err = q.store.FinishJob(j.ID, StateFailed, text, now)
		q.bump(func(s *Stats) { s.Failed++ })
		q.log.Warn("job failed permanently", "job", j.ID, "kind", string(j.Kind), "err", runErr)

	case j.Exhausted():
		state = StateFailed
		text = fmt.Sprintf("%s (gave up after %d attempts)", runErr.Error(), j.Attempts)
		err = q.store.FinishJob(j.ID, StateFailed, text, now)
		q.bump(func(s *Stats) { s.Failed++ })
		q.log.Warn("job failed", "job", j.ID, "kind", string(j.Kind),
			"attempts", j.Attempts, "err", runErr)

	default:
		wait := q.backoffFor(j.Attempts)
		state, text = StateQueued, runErr.Error()
		err = q.store.RescheduleJob(j.ID, now.Add(wait), text, now)
		q.bump(func(s *Stats) { s.Retried++ })
		q.log.Warn("job will be retried", "job", j.ID, "kind", string(j.Kind),
			"attempt", j.Attempts, "of", j.MaxAttempts, "in", wait.String(), "err", runErr)
	}

	if err != nil {
		q.log.Error("cannot write job outcome", "job", j.ID, "state", string(state), "err", err)
	}

	// The slot is released only now, after the outcome is durable. Freeing it
	// any earlier would let the next job start while this one's row still says
	// it is running, which is a state nothing else in the system can explain.
	q.mu.Lock()
	delete(q.running, j.ID)
	if q.byKind[j.Kind] > 0 {
		q.byKind[j.Kind]--
	}
	q.mu.Unlock()

	j.State = state
	j.Error = text
	j.FinishedAt = now
	q.emit(j)
	q.wake()
}

// backoffFor is exponential and capped. No jitter: this is one process running
// a handful of heavy jobs, so there is no herd to spread out and a
// deterministic schedule is testable.
func (q *Queue) backoffFor(attempt int) time.Duration {
	d := q.backoff
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= q.maxBackoff {
			return q.maxBackoff
		}
	}
	if d > q.maxBackoff {
		return q.maxBackoff
	}
	return d
}

// Cancel stops a job. A running job has its context cancelled — the worker is
// responsible for killing its own child process — and the row is written when
// it returns. A job that has not started yet is cancelled in the store.
func (q *Queue) Cancel(id int64) error {
	q.mu.Lock()
	ex, running := q.running[id]
	if running {
		ex.cancelled = true
	}
	q.mu.Unlock()
	if running {
		ex.cancel()
		return nil
	}
	return q.store.CancelJob(id, q.now())
}

// Defer holds a job back until at. It is the hook the resource policy pulls
// when the machine is needed for the stream; the job becomes claimable again
// on its own once the deadline passes, so a governor that dies cannot strand
// work.
func (q *Queue) Defer(id int64, at time.Time, reason string) error {
	return q.store.DeferJob(id, at, reason, q.now())
}

// Retry re-arms a terminal job with a fresh attempt budget.
func (q *Queue) Retry(id int64) error {
	if err := q.store.RetryJob(id, q.now()); err != nil {
		return err
	}
	q.wake()
	return nil
}

// Pause stops new claims without touching what is already running. Resume is
// the other half; both exist for the resource policy.
func (q *Queue) Pause() {
	q.mu.Lock()
	q.paused = true
	q.mu.Unlock()
}

// Resume allows claims again.
func (q *Queue) Resume() {
	q.mu.Lock()
	q.paused = false
	q.mu.Unlock()
	q.wake()
}

// Paused reports whether claiming is suspended.
func (q *Queue) Paused() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.paused
}

// Get loads one job.
func (q *Queue) Get(id int64) (*Job, error) { return q.store.GetJob(id) }

// List returns jobs matching f.
func (q *Queue) List(f Filter) ([]Job, error) { return q.store.ListJobs(f) }

// Delete removes a job. Cancelling a running one first is the caller's choice,
// not something done silently here.
func (q *Queue) Delete(id int64) error { return q.store.DeleteJob(id) }

// Purge drops terminal jobs older than cutoff, keeping the newest keep of them
// whatever their age.
func (q *Queue) Purge(cutoff time.Time, keep int) (int, error) {
	return q.store.PurgeJobs(cutoff, keep)
}

// Stats snapshots the counters.
func (q *Queue) Stats() Stats {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := q.stats
	s.Running = len(q.running)
	s.Paused = q.paused
	s.ByKind = make(map[string]int, len(q.byKind))
	for k, n := range q.byKind {
		if n > 0 {
			s.ByKind[string(k)] = n
		}
	}
	return s
}

func (q *Queue) bump(fn func(*Stats)) {
	q.mu.Lock()
	fn(&q.stats)
	q.mu.Unlock()
}

func (q *Queue) emit(j Job) {
	if q.onChange != nil {
		q.onChange(j)
	}
}

// wake nudges the dispatch loop without blocking. A full channel already means
// "there is a pending wakeup", so dropping this one loses nothing.
func (q *Queue) wake() {
	select {
	case q.wakeCh <- struct{}{}:
	default:
	}
}

// reporter is the Reporter handed to one running worker. It coalesces: a
// transcoder reporting per frame must not turn into a write per frame.
type reporter struct {
	q  *Queue
	id int64

	mu       sync.Mutex
	progress float64
	hasProg  bool
	pending  []string
	lastAt   time.Time
}

func newReporter(q *Queue, id int64) *reporter {
	return &reporter{q: q, id: id, lastAt: q.now()}
}

func (r *reporter) Progress(fraction float64) {
	r.mu.Lock()
	r.progress = ClampProgress(fraction)
	r.hasProg = true
	due := r.due()
	r.mu.Unlock()
	if due {
		r.flush()
	}
}

func (r *reporter) Logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	r.mu.Lock()
	r.pending = append(r.pending, line)
	// A worker that logs faster than the flush interval must not grow this
	// slice without bound; the store keeps a tail anyway, so trimming here
	// loses nothing that would have survived.
	if len(r.pending) > MaxLogLines {
		r.pending = r.pending[len(r.pending)-MaxLogLines:]
	}
	due := r.due()
	r.mu.Unlock()
	if due {
		r.flush()
	}
}

func (r *reporter) SetResult(v any) {
	var raw json.RawMessage
	switch t := v.(type) {
	case nil:
		return
	case json.RawMessage:
		raw = t
	case []byte:
		raw = json.RawMessage(t)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			// Not fatal: a finished transcode is not made worthless by a bad
			// summary of it.
			r.Logf("could not record job result: %v", err)
			return
		}
		raw = b
	}
	if !json.Valid(raw) || len(raw) > MaxResultBytes {
		r.Logf("job result was rejected: not valid JSON, or larger than %d bytes", MaxResultBytes)
		return
	}
	if err := r.q.store.SetJobResult(r.id, raw, r.q.now()); err != nil {
		r.q.log.Warn("cannot store job result", "job", r.id, "err", err)
	}
}

// due reports whether enough time has passed to write. Caller holds r.mu.
func (r *reporter) due() bool {
	if r.q.progressEvery <= 0 {
		return true
	}
	return !r.q.now().Before(r.lastAt.Add(r.q.progressEvery))
}

// flush writes whatever has accumulated. It is called on every completion, so
// the last progress report always lands.
func (r *reporter) flush() {
	r.mu.Lock()
	prog := -1.0
	if r.hasProg {
		prog = r.progress
	}
	lines := r.pending
	r.pending = nil
	r.hasProg = false
	r.lastAt = r.q.now()
	r.mu.Unlock()

	if prog < 0 && len(lines) == 0 {
		return
	}
	if err := r.q.store.UpdateJobProgress(r.id, prog, lines, r.q.now()); err != nil {
		r.q.log.Warn("cannot record job progress", "job", r.id, "err", err)
	}
}
