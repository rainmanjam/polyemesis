package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"
)

// errMemNotFound stands in for the database package's not-found error. The
// queue only propagates it, so the two need not be the same sentinel.
var errMemNotFound = errors.New("job not found")

// memStore is the Store contract in a map, so every dispatch behaviour below is
// tested without SQLite in the way. It mirrors the SQL semantics deliberately —
// where the two could disagree, internal/db/jobs_test.go pins the real thing.
type memStore struct {
	mu   sync.Mutex
	next int64
	rows map[int64]*Job
	now  func() time.Time

	claimErr error
}

func newMemStore(now func() time.Time) *memStore {
	return &memStore{rows: map[int64]*Job{}, now: now}
}

func copyJob(j *Job) Job {
	out := *j
	out.Log = append([]string(nil), j.Log...)
	out.Params = append(json.RawMessage(nil), j.Params...)
	out.Result = append(json.RawMessage(nil), j.Result...)
	return out
}

func (m *memStore) EnqueueJob(j Job) (*Job, error) {
	n := j.Normalized()
	if err := n.Validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	n.ID = m.next
	if n.CreatedAt.IsZero() {
		n.CreatedAt = m.now()
	}
	n.UpdatedAt = m.now()
	m.rows[n.ID] = &n
	out := copyJob(&n)
	return &out, nil
}

// seed inserts a row exactly as given, which is how a crashed process's
// leftovers are reproduced.
func (m *memStore) seed(j Job) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	j.ID = m.next
	if j.CreatedAt.IsZero() {
		j.CreatedAt = m.now()
	}
	m.rows[j.ID] = &j
	return &j
}

func (m *memStore) GetJob(id int64) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.rows[id]
	if !ok {
		return nil, errMemNotFound
	}
	out := copyJob(j)
	return &out, nil
}

func (m *memStore) ListJobs(f Filter) ([]Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Job{}
	for _, j := range m.rows {
		if !matchState(f.States, j.State) || !matchKind(f.Kinds, j.Kind) {
			continue
		}
		if f.Target != "" && j.Target != f.Target {
			continue
		}
		out = append(out, copyJob(j))
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID > out[b].ID })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func matchState(want []State, got State) bool {
	if len(want) == 0 {
		return true
	}
	for _, s := range want {
		if s == got {
			return true
		}
	}
	return false
}

func matchKind(want []Kind, got Kind) bool {
	if len(want) == 0 {
		return true
	}
	for _, k := range want {
		if k == got {
			return true
		}
	}
	return false
}

func (m *memStore) FindActiveJob(kind Kind, target string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *Job
	for _, j := range m.rows {
		if j.Kind != kind || j.Target != target || j.State.Terminal() {
			continue
		}
		if best == nil || j.ID < best.ID {
			best = j
		}
	}
	if best == nil {
		return nil, nil
	}
	out := copyJob(best)
	return &out, nil
}

func (m *memStore) ClaimJob(kinds []Kind, now time.Time) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimErr != nil {
		return nil, m.claimErr
	}
	if len(kinds) == 0 {
		return nil, nil
	}
	var cand []*Job
	for _, j := range m.rows {
		if j.State != StateQueued && j.State != StateDeferred {
			continue
		}
		if !j.AvailableAt.IsZero() && j.AvailableAt.After(now) {
			continue
		}
		if !matchKind(kinds, j.Kind) {
			continue
		}
		cand = append(cand, j)
	}
	if len(cand) == 0 {
		return nil, nil
	}
	sort.Slice(cand, func(a, b int) bool {
		if cand[a].Priority != cand[b].Priority {
			return cand[a].Priority > cand[b].Priority
		}
		if !cand[a].CreatedAt.Equal(cand[b].CreatedAt) {
			return cand[a].CreatedAt.Before(cand[b].CreatedAt)
		}
		return cand[a].ID < cand[b].ID
	})
	j := cand[0]
	j.State = StateRunning
	j.Attempts++
	j.StartedAt = now
	j.FinishedAt = time.Time{}
	j.UpdatedAt = now
	out := copyJob(j)
	return &out, nil
}

func (m *memStore) UpdateJobProgress(id int64, progress float64, lines []string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.rows[id]
	if !ok {
		return errMemNotFound
	}
	if progress >= 0 {
		j.Progress = ClampProgress(progress)
	}
	if len(lines) > 0 {
		j.Log = TrimLog(append(j.Log, lines...))
	}
	j.UpdatedAt = now
	return nil
}

func (m *memStore) SetJobResult(id int64, result json.RawMessage, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.rows[id]
	if !ok {
		return errMemNotFound
	}
	j.Result = append(json.RawMessage(nil), result...)
	j.UpdatedAt = now
	return nil
}

func (m *memStore) FinishJob(id int64, state State, errText string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.rows[id]
	if !ok {
		return errMemNotFound
	}
	j.State = state
	j.Error = errText
	j.FinishedAt = now
	j.UpdatedAt = now
	if state == StateDone {
		j.Progress = 1
	}
	return nil
}

func (m *memStore) RescheduleJob(id int64, at time.Time, errText string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.rows[id]
	if !ok {
		return errMemNotFound
	}
	j.State = StateQueued
	j.AvailableAt = at
	j.Error = errText
	j.FinishedAt = time.Time{}
	j.UpdatedAt = now
	return nil
}

func (m *memStore) RequeueJob(id int64, at time.Time, reason string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.rows[id]
	if !ok {
		return errMemNotFound
	}
	j.State = StateQueued
	if j.Attempts > 0 {
		j.Attempts--
	}
	j.AvailableAt = at
	j.StartedAt = time.Time{}
	j.FinishedAt = time.Time{}
	if reason != "" {
		j.Log = TrimLog(append(j.Log, reason))
	}
	j.UpdatedAt = now
	return nil
}

func (m *memStore) RequeueRunningJobs(now time.Time) (int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var requeued, failed int
	for _, j := range m.rows {
		if j.State != StateRunning {
			continue
		}
		if j.Exhausted() {
			j.State = StateFailed
			j.Error = "interrupted by a restart, with no attempts left"
			j.FinishedAt = now
			failed++
			continue
		}
		j.State = StateQueued
		j.AvailableAt = now
		j.StartedAt = time.Time{}
		j.Error = "interrupted by a restart"
		requeued++
	}
	return requeued, failed, nil
}

func (m *memStore) CancelJob(id int64, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.rows[id]
	if !ok {
		return errMemNotFound
	}
	if j.State.Terminal() {
		return fmt.Errorf("cannot cancel job %d: it is %s", id, j.State)
	}
	j.State = StateCancelled
	j.FinishedAt = now
	j.UpdatedAt = now
	return nil
}

func (m *memStore) DeferJob(id int64, at time.Time, reason string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.rows[id]
	if !ok {
		return errMemNotFound
	}
	if j.State != StateQueued && j.State != StateDeferred {
		return fmt.Errorf("cannot defer job %d: it is %s", id, j.State)
	}
	j.State = StateDeferred
	j.AvailableAt = at
	if reason != "" {
		j.Log = TrimLog(append(j.Log, reason))
	}
	j.UpdatedAt = now
	return nil
}

func (m *memStore) RetryJob(id int64, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.rows[id]
	if !ok {
		return errMemNotFound
	}
	if !j.State.Terminal() {
		return fmt.Errorf("cannot retry job %d: it is %s", id, j.State)
	}
	j.State = StateQueued
	j.Attempts = 0
	j.Progress = 0
	j.Error = ""
	j.AvailableAt = now
	j.StartedAt = time.Time{}
	j.FinishedAt = time.Time{}
	j.UpdatedAt = now
	return nil
}

func (m *memStore) DeleteJob(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[id]; !ok {
		return errMemNotFound
	}
	delete(m.rows, id)
	return nil
}

func (m *memStore) PurgeJobs(cutoff time.Time, keep int) ([]Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var done []*Job
	for _, j := range m.rows {
		if j.State.Terminal() && !j.FinishedAt.IsZero() {
			done = append(done, j)
		}
	}
	sort.Slice(done, func(a, b int) bool { return done[a].FinishedAt.After(done[b].FinishedAt) })
	var purged []Job
	for i, j := range done {
		if i < keep || !j.FinishedAt.Before(cutoff) {
			continue
		}
		purged = append(purged, *j)
		delete(m.rows, j.ID)
	}
	return purged, nil
}

// fakeClock makes backoff and deferral assertions exact instead of timed.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// testQueue wires a queue to a memory store and a stopped clock.
func testQueue(t *testing.T, opts ...Option) (*Queue, *memStore, *fakeClock) {
	t.Helper()
	clk := newClock()
	st := newMemStore(clk.Now)
	base := []Option{WithClock(clk.Now), WithProgressInterval(0)}
	q := New(quietLog(), st, append(base, opts...)...)
	return q, st, clk
}

func mustRegister(t *testing.T, q *Queue, kind Kind, limit int, fn WorkerFunc) {
	t.Helper()
	if err := q.Register(kind, limit, fn); err != nil {
		t.Fatalf("Register(%q): %v", kind, err)
	}
}

func mustSubmit(t *testing.T, q *Queue, j Job) *Job {
	t.Helper()
	out, created, err := q.Submit(j)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !created {
		t.Fatalf("Submit folded %v into an existing job, which this test did not expect", j)
	}
	return out
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// drain ticks until nothing is running and nothing more can start.
func drain(t *testing.T, q *Queue, ctx context.Context) {
	t.Helper()
	for i := 0; i < 500; i++ {
		n := q.Tick(ctx)
		if n == 0 && q.Stats().Running == 0 {
			return
		}
		waitUntil(t, "the queue to go idle", func() bool { return q.Stats().Running == 0 })
	}
	t.Fatal("the queue never drained")
}

func getJob(t *testing.T, st *memStore, id int64) Job {
	t.Helper()
	j, err := st.GetJob(id)
	if err != nil {
		t.Fatalf("GetJob(%d): %v", id, err)
	}
	return *j
}

func TestQueueRunsHighestPriorityFirstAndFIFOWithinAPriority(t *testing.T) {
	q, _, _ := testQueue(t)
	var (
		mu    sync.Mutex
		order []string
	)
	mustRegister(t, q, "work", 0, func(_ context.Context, j Job, _ Reporter) error {
		mu.Lock()
		order = append(order, j.Target)
		mu.Unlock()
		return nil
	})

	// Submitted deliberately out of priority order, and with two pairs sharing
	// a priority so the tie-break is exercised too.
	mustSubmit(t, q, Job{Kind: "work", Target: "normal-first", Priority: PriorityNormal})
	mustSubmit(t, q, Job{Kind: "work", Target: "user-first", Priority: PriorityUser})
	mustSubmit(t, q, Job{Kind: "work", Target: "bulk", Priority: PriorityBulk})
	mustSubmit(t, q, Job{Kind: "work", Target: "normal-second", Priority: PriorityNormal})
	mustSubmit(t, q, Job{Kind: "work", Target: "user-second", Priority: PriorityUser})

	drain(t, q, context.Background())

	want := []string{"user-first", "user-second", "normal-first", "normal-second", "bulk"}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != len(want) {
		t.Fatalf("ran %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("ran %v, want %v — priority first, then the order they were asked for", order, want)
		}
	}
}

func TestQueueHonoursGlobalAndPerKindConcurrency(t *testing.T) {
	q, _, _ := testQueue(t, WithConcurrency(3))
	release := make(chan struct{})
	block := func(_ context.Context, _ Job, _ Reporter) error {
		<-release
		return nil
	}
	// "heavy" is the transcode-shaped kind: allowed one at a time however much
	// global headroom there is.
	mustRegister(t, q, "heavy", 1, block)
	mustRegister(t, q, "light", 0, block)

	for i := 0; i < 3; i++ {
		mustSubmit(t, q, Job{Kind: "heavy", Target: fmt.Sprintf("h%d", i)})
		mustSubmit(t, q, Job{Kind: "light", Target: fmt.Sprintf("l%d", i)})
	}

	ctx := context.Background()
	q.Tick(ctx)
	waitUntil(t, "three jobs to be running", func() bool { return q.Stats().Running == 3 })

	// A second tick must not squeeze anything past the ceiling.
	if n := q.Tick(ctx); n != 0 {
		t.Fatalf("a second tick started %d more jobs with every slot taken", n)
	}
	s := q.Stats()
	if s.Running != 3 {
		t.Fatalf("running = %d, want the global limit of 3", s.Running)
	}
	if s.ByKind["heavy"] != 1 {
		t.Errorf("heavy running = %d, want its per-kind limit of 1", s.ByKind["heavy"])
	}
	if s.ByKind["light"] != 2 {
		t.Errorf("light running = %d, want the remaining 2 global slots", s.ByKind["light"])
	}

	close(release)
	drain(t, q, ctx)
	if got := q.Stats().Completed; got != 6 {
		t.Errorf("completed = %d, want all 6 once the limits stopped binding", got)
	}
}

func TestQueueRetriesRetryableFailuresWithBackoffThenGivesUp(t *testing.T) {
	q, st, clk := testQueue(t, WithBackoff(10*time.Second, time.Minute))
	mustRegister(t, q, "flaky", 0, func(context.Context, Job, Reporter) error {
		return errors.New("ffmpeg exited 1")
	})
	job := mustSubmit(t, q, Job{Kind: "flaky", Target: "recording:1", MaxAttempts: 3})
	ctx := context.Background()

	runOnce := func() {
		q.Tick(ctx)
		waitUntil(t, "the attempt to finish", func() bool { return q.Stats().Running == 0 })
	}

	runOnce()
	got := getJob(t, st, job.ID)
	if got.State != StateQueued || got.Attempts != 1 {
		t.Fatalf("after one failure state/attempts = %s/%d, want queued/1", got.State, got.Attempts)
	}
	if want := clk.Now().Add(10 * time.Second); !got.AvailableAt.Equal(want) {
		t.Fatalf("availableAt = %v, want a %s backoff", got.AvailableAt, 10*time.Second)
	}
	if got.Error == "" {
		t.Error("the failure reason must survive the retry so an operator can see it going wrong")
	}

	// Still inside the backoff: the job must not be claimed.
	if n := q.Tick(ctx); n != 0 {
		t.Fatal("a job was claimed before its backoff elapsed")
	}

	clk.Advance(10 * time.Second)
	runOnce()
	got = getJob(t, st, job.ID)
	if got.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", got.Attempts)
	}
	if want := clk.Now().Add(20 * time.Second); !got.AvailableAt.Equal(want) {
		t.Fatalf("availableAt = %v, want the backoff doubled to %s", got.AvailableAt, 20*time.Second)
	}

	clk.Advance(20 * time.Second)
	runOnce()
	got = getJob(t, st, job.ID)
	if got.State != StateFailed {
		t.Fatalf("state = %s, want failed once the attempt ceiling was reached", got.State)
	}
	if got.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", got.Attempts)
	}
	if n := q.Tick(ctx); n != 0 {
		t.Fatal("a failed job was claimed again; the ceiling means nothing if it is")
	}
}

func TestQueueBackoffIsCapped(t *testing.T) {
	q, _, _ := testQueue(t, WithBackoff(10*time.Second, 30*time.Second))
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 10 * time.Second},
		{attempt: 2, want: 20 * time.Second},
		{attempt: 3, want: 30 * time.Second},
		{attempt: 9, want: 30 * time.Second},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("attempt %d", tc.attempt), func(t *testing.T) {
			if got := q.backoffFor(tc.attempt); got != tc.want {
				t.Errorf("backoffFor(%d) = %s, want %s", tc.attempt, got, tc.want)
			}
		})
	}
}

func TestQueueDistinguishesPermanentFromRetryableFailures(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantState   State
		wantRetried bool
	}{
		{
			name:        "a tool crash is retried",
			err:         errors.New("whisper crashed"),
			wantState:   StateQueued,
			wantRetried: true,
		},
		{
			name:      "a missing input is permanent",
			err:       Permanent(errors.New("recording file is gone")),
			wantState: StateFailed,
		},
		{
			name:        "an error nobody classified is retried, not thrown away",
			err:         fmt.Errorf("wrapped: %w", errors.New("disk busy")),
			wantState:   StateQueued,
			wantRetried: true,
		},
		{
			name:        "a panicking worker is retried rather than killing the server",
			err:         nil, // the worker panics instead
			wantState:   StateQueued,
			wantRetried: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q, st, _ := testQueue(t)
			mustRegister(t, q, "k", 0, func(context.Context, Job, Reporter) error {
				if tc.err == nil {
					panic("processor blew up")
				}
				return tc.err
			})
			job := mustSubmit(t, q, Job{Kind: "k", MaxAttempts: 3})

			q.Tick(context.Background())
			waitUntil(t, "the attempt to finish", func() bool { return q.Stats().Running == 0 })

			got := getJob(t, st, job.ID)
			if got.State != tc.wantState {
				t.Fatalf("state = %s, want %s", got.State, tc.wantState)
			}
			if got.Attempts != 1 {
				t.Errorf("attempts = %d, want the one attempt that ran", got.Attempts)
			}
			if retried := q.Stats().Retried > 0; retried != tc.wantRetried {
				t.Errorf("retried = %v, want %v", retried, tc.wantRetried)
			}
			if got.Error == "" {
				t.Error("the failure must be recorded")
			}
		})
	}
}

func TestQueueCancelsARunningJob(t *testing.T) {
	q, st, _ := testQueue(t)
	started := make(chan struct{})
	var sawCancel bool
	mustRegister(t, q, "long", 0, func(ctx context.Context, _ Job, _ Reporter) error {
		close(started)
		<-ctx.Done()
		// A real worker kills its child process here; the queue's contract is
		// only that it returns promptly.
		sawCancel = true
		return ctx.Err()
	})
	job := mustSubmit(t, q, Job{Kind: "long", Target: "recording:3"})

	q.Tick(context.Background())
	<-started
	if err := q.Cancel(job.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitUntil(t, "the cancelled job to stop", func() bool { return q.Stats().Running == 0 })

	got := getJob(t, st, job.ID)
	if got.State != StateCancelled {
		t.Fatalf("state = %s, want cancelled", got.State)
	}
	if !sawCancel {
		t.Error("the worker was never told to stop")
	}
	if q.Stats().Cancelled != 1 {
		t.Errorf("cancelled count = %d, want 1", q.Stats().Cancelled)
	}
}

func TestQueueCancelsAJobThatHasNotStarted(t *testing.T) {
	q, st, _ := testQueue(t)
	mustRegister(t, q, "k", 0, func(context.Context, Job, Reporter) error {
		t.Error("a cancelled job must never run")
		return nil
	})
	job := mustSubmit(t, q, Job{Kind: "k"})

	if err := q.Cancel(job.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if n := q.Tick(context.Background()); n != 0 {
		t.Fatal("a cancelled job was claimed")
	}
	if got := getJob(t, st, job.ID); got.State != StateCancelled {
		t.Fatalf("state = %s, want cancelled", got.State)
	}
}

// The important one: a queue built over rows a dead process left behind must
// resume them, not orphan them.
func TestQueueRequeuesJobsLeftRunningByACrash(t *testing.T) {
	q, st, clk := testQueue(t)

	resumable := st.seed(Job{
		Kind: "transcribe", Target: RecordingTarget(1), State: StateRunning,
		Attempts: 1, MaxAttempts: 3, Progress: 0.4, Params: json.RawMessage("{}"),
		StartedAt: clk.Now().Add(-time.Hour),
	})
	burned := st.seed(Job{
		Kind: "transcribe", Target: RecordingTarget(2), State: StateRunning,
		Attempts: 3, MaxAttempts: 3, Params: json.RawMessage("{}"),
	})
	untouched := st.seed(Job{
		Kind: "transcribe", Target: RecordingTarget(3), State: StateDone,
		Attempts: 1, MaxAttempts: 3, Params: json.RawMessage("{}"),
	})

	requeued, failed := q.Recover()
	if requeued != 1 || failed != 1 {
		t.Fatalf("Recover = %d requeued, %d failed; want 1 and 1", requeued, failed)
	}

	got := getJob(t, st, resumable.ID)
	if got.State != StateQueued {
		t.Fatalf("interrupted job state = %s, want queued — an orphaned row is work silently lost", got.State)
	}
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want the interrupted attempt still counted", got.Attempts)
	}
	if got.Error == "" {
		t.Error("the interruption must be recorded so it is not a mystery later")
	}

	if got := getJob(t, st, burned.ID); got.State != StateFailed {
		t.Errorf("a job with no attempts left came back as %s, want failed — "+
			"requeuing it forever is how a poison job stops the server booting", got.State)
	}
	if got := getJob(t, st, untouched.ID); got.State != StateDone {
		t.Errorf("a finished job was touched by recovery: %s", got.State)
	}

	// And the resumed job really is claimable again.
	var ran bool
	mustRegister(t, q, "transcribe", 0, func(_ context.Context, j Job, _ Reporter) error {
		if j.ID == resumable.ID {
			ran = true
		}
		return nil
	})
	drain(t, q, context.Background())
	if !ran {
		t.Error("the requeued job never ran")
	}
}

func TestQueueRequeuesAnInFlightJobOnShutdown(t *testing.T) {
	q, st, _ := testQueue(t)
	started := make(chan struct{})
	mustRegister(t, q, "long", 0, func(ctx context.Context, _ Job, _ Reporter) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	job := mustSubmit(t, q, Job{Kind: "long", Target: RecordingTarget(9)})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		q.Run(ctx)
		close(done)
	}()

	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}

	got := getJob(t, st, job.ID)
	if got.State != StateQueued {
		t.Fatalf("state = %s, want queued: a shutdown must not lose four hours of work", got.State)
	}
	if got.Attempts != 0 {
		t.Errorf("attempts = %d, want the attempt refunded — the server stopped it, not the job", got.Attempts)
	}
	if !got.FinishedAt.IsZero() {
		t.Error("a requeued job must not look finished")
	}
}

func TestQueueReportsProgressAndLogFromTheWorker(t *testing.T) {
	q, st, _ := testQueue(t)
	mustRegister(t, q, "k", 0, func(_ context.Context, _ Job, rep Reporter) error {
		rep.Logf("starting on %d tracks", 4)
		rep.Progress(0.5)
		rep.Progress(2) // clamped, not rejected
		rep.SetResult(map[string]string{"path": "/x/y.srt"})
		return nil
	})
	job := mustSubmit(t, q, Job{Kind: "k"})
	drain(t, q, context.Background())

	got := getJob(t, st, job.ID)
	if got.State != StateDone {
		t.Fatalf("state = %s, want done", got.State)
	}
	if got.Progress != 1 {
		t.Errorf("progress = %v, want a finished job to read 1 whatever the worker last said", got.Progress)
	}
	if len(got.Log) != 1 || got.Log[0] != "starting on 4 tracks" {
		t.Errorf("log = %v, want the worker's line", got.Log)
	}
	if string(got.Result) != `{"path":"/x/y.srt"}` {
		t.Errorf("result = %s, want the worker's output description", got.Result)
	}
}

func TestQueueFoldsAUniqueResubmissionIntoTheActiveJob(t *testing.T) {
	q, _, _ := testQueue(t)
	first := mustSubmit(t, q, Job{Kind: "transcribe", Target: RecordingTarget(1), Unique: true})

	again, created, err := q.Submit(Job{Kind: "transcribe", Target: RecordingTarget(1), Unique: true})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if created {
		t.Fatal("a second click queued a second transcription of the same recording")
	}
	if again.ID != first.ID {
		t.Fatalf("returned job %d, want the active one %d", again.ID, first.ID)
	}

	// A different recording, and a non-unique job, are both still new work.
	if _, created, _ := q.Submit(Job{Kind: "transcribe", Target: RecordingTarget(2), Unique: true}); !created {
		t.Error("a different target must be its own job")
	}
	if _, created, _ := q.Submit(Job{Kind: "transcribe", Target: RecordingTarget(1)}); !created {
		t.Error("without Unique, a resubmission is a new job")
	}
}

func TestQueueDeferredWorkComesBackOnItsOwn(t *testing.T) {
	q, st, clk := testQueue(t)
	var ran int
	mustRegister(t, q, "k", 0, func(context.Context, Job, Reporter) error {
		ran++
		return nil
	})
	job := mustSubmit(t, q, Job{Kind: "k"})

	if err := q.Defer(job.ID, clk.Now().Add(time.Minute), "yielding to the live stream"); err != nil {
		t.Fatalf("Defer: %v", err)
	}
	if got := getJob(t, st, job.ID); got.State != StateDeferred {
		t.Fatalf("state = %s, want deferred", got.State)
	}
	if n := q.Tick(context.Background()); n != 0 {
		t.Fatal("a deferred job was claimed before its deadline")
	}

	// Nothing releases it: it releases itself, so a governor that dies cannot
	// strand the work forever.
	clk.Advance(time.Minute)
	drain(t, q, context.Background())
	if ran != 1 {
		t.Fatalf("ran %d times, want the deferred job to run once its deadline passed", ran)
	}
}

func TestQueuePauseStopsClaimingWithoutTouchingRunningWork(t *testing.T) {
	q, _, _ := testQueue(t)
	mustRegister(t, q, "k", 0, func(context.Context, Job, Reporter) error { return nil })
	mustSubmit(t, q, Job{Kind: "k"})

	q.Pause()
	if !q.Paused() {
		t.Fatal("Paused() disagrees with Pause()")
	}
	if n := q.Tick(context.Background()); n != 0 {
		t.Fatal("a paused queue claimed work")
	}

	q.Resume()
	drain(t, q, context.Background())
	if q.Stats().Completed != 1 {
		t.Error("resuming did not let the job run")
	}
}

func TestQueueRequeuesAJobWithNoRegisteredWorker(t *testing.T) {
	// Failing it would throw away work because a processor was slow to wire
	// itself up, which is exactly the restrictive-check mistake.
	q, st, clk := testQueue(t, WithTick(time.Second))
	mustRegister(t, q, "known", 0, func(context.Context, Job, Reporter) error { return nil })

	orphan := st.seed(Job{Kind: "unknown", State: StateQueued, MaxAttempts: 3, Params: json.RawMessage("{}")})
	// Claiming is by kind, so the queue only reaches this row if it registers
	// the kind mid-flight; simulate that and then take the worker away.
	if err := q.Register("unknown", 0, WorkerFunc(func(context.Context, Job, Reporter) error { return nil })); err != nil {
		t.Fatalf("Register: %v", err)
	}
	q.mu.Lock()
	delete(q.workers, "unknown")
	q.mu.Unlock()

	// The kind is gone from the registry but a claim can still surface it if
	// another kind shares the tick, so drive the claim directly.
	claimed, err := st.ClaimJob([]Kind{"unknown"}, clk.Now())
	if err != nil || claimed == nil {
		t.Fatalf("ClaimJob: %v, %v", claimed, err)
	}
	if started := q.start(context.Background(), *claimed); started {
		t.Fatal("a job with no worker was reported as started")
	}

	got := getJob(t, st, orphan.ID)
	if got.State != StateQueued {
		t.Fatalf("state = %s, want it back in the queue", got.State)
	}
	if got.Attempts != 0 {
		t.Errorf("attempts = %d, want the attempt refunded: no worker is not the job's fault", got.Attempts)
	}
	if !got.AvailableAt.After(clk.Now()) {
		t.Error("the retry must be held off briefly, or the queue spins on it")
	}
}

func TestQueueRegisterRefusesADuplicateKind(t *testing.T) {
	q, _, _ := testQueue(t)
	w := WorkerFunc(func(context.Context, Job, Reporter) error { return nil })
	tests := []struct {
		name    string
		kind    Kind
		worker  Worker
		wantErr bool
	}{
		{name: "first registration", kind: "k", worker: w},
		{name: "same kind twice", kind: "k", worker: w, wantErr: true},
		{name: "no kind", kind: "", worker: w, wantErr: true},
		{name: "nil worker", kind: "other", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := q.Register(tc.kind, 0, tc.worker)
			if tc.wantErr != (err != nil) {
				t.Fatalf("Register = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestQueueSubmitRefusesAnInvalidJobAndNeverInsertsRunning(t *testing.T) {
	q, st, _ := testQueue(t)
	if _, _, err := q.Submit(Job{Target: "no kind"}); err == nil {
		t.Error("a job with no kind was accepted")
	}
	// A caller cannot smuggle in a row that is already running.
	job := mustSubmit(t, q, Job{Kind: "k", State: StateRunning, Attempts: 7})
	got := getJob(t, st, job.ID)
	if got.State != StateQueued || got.Attempts != 0 {
		t.Fatalf("submitted job state/attempts = %s/%d, want queued/0", got.State, got.Attempts)
	}
}

func TestQueueSurvivesAStoreThatCannotClaim(t *testing.T) {
	q, st, _ := testQueue(t)
	mustRegister(t, q, "k", 0, func(context.Context, Job, Reporter) error { return nil })
	st.mu.Lock()
	st.claimErr = errors.New("database is locked")
	st.mu.Unlock()

	if n := q.Tick(context.Background()); n != 0 {
		t.Fatal("a claim error must not start anything")
	}
	if q.Stats().Running != 0 {
		t.Error("a claim error left the queue thinking something was running")
	}
}
