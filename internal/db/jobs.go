package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// The SQLite half of the background job queue. The queue package owns the
// policy — ordering, limits, retry classification — and this file owns the SQL
// and nothing else.
var _ jobs.Store = (*DB)(nil)

const jobColumns = `id, kind, target, params, result, priority, state, unique_target,
	attempts, max_attempts, progress, log_tail, last_error,
	created_at, available_at, started_at, finished_at, updated_at`

// The reads below, as whole compile-time constants.
//
// Go folds `"a" + constB + "c"` at compile time when every operand is a const,
// so these cost nothing at runtime and cannot vary. A query assembled at the
// call site is indistinguishable, to a reader and to a static analyser, from
// one that interpolates a variable; a constant is safe BY CONSTRUCTION,
// because there is no expression left for a value to reach. Fuller argument in
// chat.go.
const (
	jobByIDQuery   = `SELECT ` + jobColumns + ` FROM jobs WHERE id = ?`
	jobActiveQuery = `SELECT ` + jobColumns + ` FROM jobs
		WHERE kind = ? AND target = ? AND state IN ('queued','running','deferred')
		ORDER BY id LIMIT 1`
)

// claimableStates is what ClaimJob will take. Deferred is in here on purpose:
// a deferral is a time, not a lock, so work held back by a resource policy that
// then died still comes back on its own.
const claimableStates = `('queued','deferred')`

func scanJob(s interface{ Scan(...any) error }) (*jobs.Job, error) {
	var (
		j              jobs.Job
		kind, state    string
		params, result string
		uniqueTarget   int
		logJSON        string
		priority       int
		created, avail int64
		started, fin   int64
		updated        int64
	)
	if err := s.Scan(&j.ID, &kind, &j.Target, &params, &result, &priority, &state,
		&uniqueTarget, &j.Attempts, &j.MaxAttempts, &j.Progress, &logJSON, &j.Error,
		&created, &avail, &started, &fin, &updated); err != nil {
		return nil, err
	}
	j.Kind = jobs.Kind(kind)
	j.State = jobs.State(state)
	j.Priority = jobs.Priority(priority)
	j.Unique = uniqueTarget != 0
	if params != "" {
		j.Params = json.RawMessage(params)
	}
	if result != "" {
		j.Result = json.RawMessage(result)
	}
	// A log tail that will not parse is dropped rather than failing the read.
	// The job itself is what the operator came for; its chatter is not worth
	// taking the list down over.
	if logJSON != "" && logJSON != "[]" {
		var lines []string
		if err := json.Unmarshal([]byte(logJSON), &lines); err == nil {
			j.Log = lines
		}
	}
	j.CreatedAt = time.Unix(created, 0)
	j.UpdatedAt = time.Unix(updated, 0)
	j.AvailableAt = timeOrZero(avail)
	j.StartedAt = timeOrZero(started)
	j.FinishedAt = timeOrZero(fin)
	return &j, nil
}

// EnqueueJob stores a new job.
//
// A unique job whose active twin already exists is NOT stored twice and is not
// an error: the twin is returned, which is what the caller wanted -- one job
// doing that work, and its id. queue.Submit asks FindActiveJob first and only
// reaches here when it found nothing, so this path is the race it cannot
// close, not the ordinary duplicate.
//
// WHY THE RACE EXISTS AT ALL. Submit does FindActiveJob, then EnqueueJob, with
// no transaction spanning the two. db.go's SetMaxOpenConns(1) serialises the
// STATEMENTS but not the GAP between them: two HTTP handlers both search, both
// find nothing, and both then insert. Two identical transcriptions of one
// recording start, burn CPU against each other, and write over one another's
// output. The partial unique index MigrateJobUniqueTarget installs is what
// makes the second insert impossible rather than merely unlikely; this
// function is what turns the refusal into an answer instead of a 400 whose
// text is the name of an index.
func (d *DB) EnqueueJob(j jobs.Job) (*jobs.Job, error) {
	n := j.Normalized()
	if err := n.Validate(); err != nil {
		return nil, err
	}
	logJSON, err := marshalJobLog(n.Log)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	created := now
	if !n.CreatedAt.IsZero() {
		created = n.CreatedAt.Unix()
	}
	res, err := d.sql.Exec(`INSERT INTO jobs
		(kind, target, params, result, priority, state, unique_target,
		 attempts, max_attempts, progress, log_tail, last_error,
		 created_at, available_at, started_at, finished_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(n.Kind), n.Target, string(n.Params), string(n.Result), int(n.Priority),
		string(n.State), boolToInt(n.Unique), n.Attempts, n.MaxAttempts, n.Progress,
		logJSON, n.Error, created, unixOrZero(n.AvailableAt),
		unixOrZero(n.StartedAt), unixOrZero(n.FinishedAt), now)
	if err != nil {
		if !isJobUniqueTargetViolation(err) {
			return nil, err
		}
		// The index refused it, so an active job with this kind and target is
		// already there. Hand back the one that won.
		existing, findErr := d.FindActiveJob(n.Kind, n.Target)
		if findErr != nil {
			return nil, findErr
		}
		if existing == nil {
			// Only reachable if the winner reached a terminal state between
			// the refusal and this read. Returning the original violation is
			// honest: there is no job to point the caller at, and inventing
			// one would be worse than saying so.
			return nil, err
		}
		return existing, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return d.GetJob(id)
}

// jobUniqueTargetIndex is the partial unique index that makes two active jobs
// for one unique target impossible. Named here because three things have to
// agree about it: the migration that creates it, the guard that checks whether
// it is already there, and the error matcher below.
const jobUniqueTargetIndex = "idx_jobs_unique_target"

// isJobUniqueTargetViolation reports whether err is SQLite refusing a second
// active job for a unique target.
//
// Matched on the index and on the columns, never on "UNIQUE constraint failed"
// alone: any other unique violation on this table is a different bug and must
// not be quietly rewritten into "here is your existing job", which would hide
// it behind a successful-looking response. Both spellings are checked for the
// reason asTokenTaken records -- SQLite names the columns in some versions and
// the index in others, and a message that changes shape must not silently stop
// matching.
func isJobUniqueTargetViolation(err error) bool {
	if err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, jobUniqueTargetIndex) ||
		(strings.Contains(msg, "jobs.kind") && strings.Contains(msg, "jobs.target"))
}

// MigrateJobUniqueTarget creates the partial unique index that stops two
// active jobs existing for one unique target, folding any duplicates already
// there into the job the queue already considers canonical.
//
// WHY IT IS NOT IN schema.sql, which is where a reader will look for it: the
// rule is partial -- it applies only to rows with unique_target = 1, a
// non-empty target and an unfinished state -- and CREATE TABLE has no syntax
// for that. A bare CREATE UNIQUE INDEX at the foot of that file would run on
// every open of every install, including one whose jobs table already holds a
// pair from the old race, where it would fail and abort the whole script; the
// operator would get "apply schema: UNIQUE constraint failed" and a server
// that will not boot. MigrateSourceTokenUnique carries the same reasoning for
// the same shape of index one table over. This runs on every open, fresh
// installs included, so both populations end up with the same rule.
//
// EVERY WRITER THAT CAN BE REFUSED BY THIS INDEX, enumerated because getting
// this list wrong is how #746 shipped: the first draft of this analysis named
// EnqueueJob's INSERT and reasoned that RescheduleJob was the only UPDATE worth
// a second look. It is not, and it is not even one of the risky ones.
//
// The test is not "does this statement write state" but "can it move a row from
// OUTSIDE the predicate to INSIDE it", because only such a row is new to the
// index. That splits the writers cleanly:
//
//   - Arriving from outside, by their own WHERE: EnqueueJob (INSERT,
//     state='queued') and RetryJob (WHERE state IN ('done','failed','cancelled')
//     -> 'queued'). Both handle the refusal — EnqueueJob folds to the active
//     twin, RetryJob answers ErrStateConflict naming it.
//
//   - Already inside BY CALLER CONVENTION, which is a weaker thing and is why
//     they are no longer listed as safe: ClaimJob (queued/deferred -> running)
//     and DeferJob (queued/deferred -> deferred) are guarded by their own WHERE
//     and genuinely cannot collide. RescheduleJob and RequeueJob are not:
//     nothing in their UPDATE restricts the row's state, and an earlier draft
//     of this comment said they were "reached only with a running job" and
//     therefore safe. That is a property of their callers, not of them, and the
//     callers have a hole.
//
//     THE HOLE, because a reviewer found it by probing rather than reading:
//     dispatchOnce calls ClaimJob (row -> running) and only then does q.start
//     register the job in q.running (internal/jobs/queue.go:376-383).
//     Queue.Cancel checks q.running, so inside that window it takes the
//     !running branch and calls CancelJob, which accepts a running row. The row
//     is now cancelled with a live worker still on it, its target is free, a
//     resubmission is accepted — and when the worker returns a retryable error,
//     finish reaches RescheduleJob with a TERMINAL row and an active twin.
//     Which is to say: from outside the predicate. The same window reaches
//     RequeueJob through start's unregistered-kind branch.
//
//     Worse than #746 when it fires, because finish only logs the error
//     (q.log.Error("cannot write job outcome")) — the job stays cancelled
//     forever, the worker's outcome is discarded, and nothing appears on
//     screen. So both now carry the same violation branch RetryJob has. It
//     cannot change an accepted write; it converts the one reachable collision
//     from a swallowed driver string into ErrStateConflict.
//
//   - Already inside and incapable of colliding: RequeueRunningJobs (WHERE
//     state='running'). Its (kind, target) is already the entry the index
//     holds; moving between the three covered states changes nothing it
//     indexes on.
//
//   - Leaving: FinishJob, CancelJob, and the fold below. These free an entry.
//
// A future writer belongs in the first group if its WHERE can select a terminal
// row, or if it sets state to one of the three covered ones from a row that was
// not already covered. Such a writer needs the same treatment RetryJob has.
//
// THE PREDICATE IS EXACTLY THE QUEUE'S OWN RULE, deliberately not one grain
// stricter. queue.Submit folds a submission only when `j.Unique &&
// j.Target != ""`, and FindActiveJob looks only at queued, running and
// deferred -- so those three conditions are the index's WHERE. A job with
// unique_target = 0 is a job somebody asked to be able to run twice (two clips
// out of one recording, say); an empty target is not a thing two jobs can
// share; a finished job is history. Widening the index past any of those would
// refuse work the product is supposed to accept.
//
// FOLDING RATHER THAN REFUSING TO START, which is the opposite of what
// MigrateSourceTokenUnique does with duplicate source tokens, and the
// difference is what the rows are. A duplicate publish token is operator
// configuration, and picking which source keeps it decides which programme a
// live encoder is admitted into -- a judgement no migration can make. Two
// active jobs for one target are not configuration; they are the defect this
// index exists to remove, already in the table, and the queue itself has
// always had an answer for which of them is the real one: FindActiveJob orders
// by id and takes the first. So the lowest id survives and its twins are
// cancelled with a last_error saying why. Nothing is deleted, the cancellation
// is visible in the jobs list, and a worker still running a cancelled twin is
// left entirely alone -- its terminal write lands on the row by id and simply
// moves it from cancelled to done or failed, which the index does not cover
// either way.
func (d *DB) MigrateJobUniqueTarget() error {
	// Checked before anything is written, and outside any transaction, for the
	// reason MigrateSourceTokenUnique records: db.go sets SetMaxOpenConns(1),
	// so a read issued while a transaction holds the one connection waits for
	// a connection that transaction will not release, and startup hangs for
	// ever rather than failing.
	has, err := indexExists(d.sql, jobUniqueTargetIndex)
	if err != nil {
		return fmt.Errorf("inspect jobs indexes: %w", err)
	}
	if has {
		return nil
	}

	superseded, err := supersededUniqueJobs(d.sql)
	if err != nil {
		return fmt.Errorf("check for duplicate active jobs: %w", err)
	}
	if len(superseded) > 0 {
		now := time.Now().Unix()
		args := []any{
			"cancelled as a duplicate: another job was already active for this " +
				"target when this one was submitted",
			now, now,
		}
		args = append(args, int64Args(superseded)...)
		if _, err := d.sql.Exec(`UPDATE jobs
			SET state = 'cancelled', last_error = ?, finished_at = ?, updated_at = ?
			WHERE id IN (`+placeholders(len(superseded))+`)`, args...); err != nil {
			return fmt.Errorf("fold duplicate active jobs: %w", err)
		}
	}

	if _, err := d.sql.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + jobUniqueTargetIndex +
			` ON jobs(kind, target)
			 WHERE unique_target = 1 AND target <> ''
			   AND state IN ('queued','running','deferred')`,
	); err != nil {
		return fmt.Errorf("create %s: %w", jobUniqueTargetIndex, err)
	}
	return nil
}

// supersededUniqueJobs is every active unique job that is NOT the lowest-id one
// for its kind and target -- that is, every row the index is about to refuse.
//
// Lowest id, because that is the one FindActiveJob returns (`ORDER BY id LIMIT
// 1`): the queue has always treated it as the job that represents this target,
// every caller that asked before this migration was handed that one, and
// keeping any other would retroactively change which job an operator is
// already watching.
func supersededUniqueJobs(sqldb *sql.DB) ([]int64, error) {
	const active = `unique_target = 1 AND target <> '' AND
		state IN ('queued','running','deferred')`
	rows, err := sqldb.Query(`SELECT id FROM jobs WHERE ` + active + `
		AND id NOT IN (SELECT MIN(id) FROM jobs WHERE ` + active + `
			GROUP BY kind, target)
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// GetJob loads one job.
func (d *DB) GetJob(id int64) (*jobs.Job, error) {
	j, err := scanJob(d.sql.QueryRow(jobByIDQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return j, err
}

// ListJobs returns jobs matching f, newest first.
func (d *DB) ListJobs(f jobs.Filter) ([]jobs.Job, error) {
	var (
		where []string
		args  []any
	)
	if len(f.States) > 0 {
		for _, s := range f.States {
			args = append(args, string(s))
		}
		where = append(where, `state IN (`+placeholders(len(f.States))+`)`)
	}
	if len(f.Kinds) > 0 {
		for _, k := range f.Kinds {
			args = append(args, string(k))
		}
		where = append(where, `kind IN (`+placeholders(len(f.Kinds))+`)`)
	}
	if f.Target != "" {
		where = append(where, `target = ?`)
		args = append(args, f.Target)
	}

	// This one query stays assembled at run time, unlike every other read in
	// this package, because the shape of the WHERE genuinely varies with the
	// filter: a caller may ask for any combination of states, kinds and target.
	//
	// It is still safe, and the reason is worth stating rather than trusting:
	// every fragment appended above is a STRING LITERAL, and the only part that
	// varies in length is a run of "?" generated from len(f.States) and
	// len(f.Kinds). No caller value is ever concatenated — each one is appended
	// to args and travels as a bound parameter. A static analyser cannot see
	// that and will flag this line; a reader can check it in the twenty lines
	// above.
	q := `SELECT ` + jobColumns + ` FROM jobs`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY created_at DESC, id DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}

	rows, err := d.sql.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []jobs.Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// FindActiveJob returns the unfinished job with this kind and target, or
// (nil, nil) when there is none. It is what stops a second click from starting
// a second transcription of the same recording.
func (d *DB) FindActiveJob(kind jobs.Kind, target string) (*jobs.Job, error) {
	return activeJobFor(d.sql, string(kind), target)
}

// activeJobFor is FindActiveJob's body, taken out so the error paths below can
// reach it through whatever handle they already hold. It takes a rowQuerier for
// the reason that interface exists at all: a helper reached from inside a
// transaction must use the transaction's handle, because db.go's
// SetMaxOpenConns(1) turns a read issued on d.sql while a transaction holds the
// one connection into a deadlock rather than an error.
func activeJobFor(q rowQuerier, kind, target string) (*jobs.Job, error) {
	j, err := scanJob(q.QueryRow(jobActiveQuery, kind, target))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return j, err
}

// ClaimJob atomically takes the next eligible job and marks it running.
//
// Ordering is priority first, then FIFO: a high-priority job jumps the queue,
// but two jobs of the same priority run in the order they were asked for, so a
// backfill sweep cannot starve the one submitted after it.
func (d *DB) ClaimJob(kinds []jobs.Kind, now time.Time) (*jobs.Job, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	args := []any{now.Unix()}
	for _, k := range kinds {
		args = append(args, string(k))
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Assembled rather than constant for the same reason as ListJobs: the
	// number of accepted kinds is known only at the call. jobColumns and
	// claimableStates are both consts, and strings.Join(ph, ",") produces
	// nothing but "?" and commas — ph is filled with the literal "?" above,
	// one per kind, and the kinds themselves go into args as bound parameters.
	j, err := scanJob(tx.QueryRow(`SELECT `+jobColumns+` FROM jobs
		WHERE state IN `+claimableStates+` AND available_at <= ? AND kind IN (`+placeholders(len(kinds))+`)
		ORDER BY priority DESC, created_at ASC, id ASC LIMIT 1`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		// An idle queue is the normal case and must not look like a fault.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	ts := now.Unix()
	res, err := tx.Exec(`UPDATE jobs SET state='running', attempts=attempts+1,
		started_at=?, finished_at=0, updated_at=?
		WHERE id=? AND state IN `+claimableStates, ts, ts, j.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	j.State = jobs.StateRunning
	j.Attempts++
	j.StartedAt = now
	j.FinishedAt = time.Time{}
	j.UpdatedAt = now
	return j, nil
}

// UpdateJobProgress records progress and appends log lines. A negative
// progress means "leave it alone", so a worker that only wants to log does not
// have to invent a number.
func (d *DB) UpdateJobProgress(id int64, progress float64, logLines []string, now time.Time) error {
	if progress < 0 && len(logLines) == 0 {
		return nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	logJSON := ""
	if len(logLines) > 0 {
		logJSON, err = appendJobLogTx(tx, id, logLines)
		if err != nil {
			return err
		}
	}

	var res sql.Result
	switch {
	case progress >= 0 && logJSON != "":
		res, err = tx.Exec(`UPDATE jobs SET progress=?, log_tail=?, updated_at=? WHERE id=?`,
			jobs.ClampProgress(progress), logJSON, now.Unix(), id)
	case progress >= 0:
		res, err = tx.Exec(`UPDATE jobs SET progress=?, updated_at=? WHERE id=?`,
			jobs.ClampProgress(progress), now.Unix(), id)
	default:
		res, err = tx.Exec(`UPDATE jobs SET log_tail=?, updated_at=? WHERE id=?`,
			logJSON, now.Unix(), id)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// SetJobResult stores the worker's output description.
func (d *DB) SetJobResult(id int64, result json.RawMessage, now time.Time) error {
	if len(result) > jobs.MaxResultBytes {
		return fmt.Errorf("job result is larger than %d bytes", jobs.MaxResultBytes)
	}
	if len(result) > 0 && !json.Valid(result) {
		return errors.New("job result is not valid JSON")
	}
	res, err := d.sql.Exec(`UPDATE jobs SET result=?, updated_at=? WHERE id=?`,
		string(result), now.Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// FinishJob is the terminal write.
func (d *DB) FinishJob(id int64, state jobs.State, errText string, now time.Time) error {
	if !state.Terminal() {
		return fmt.Errorf("%q is not a terminal job state", state)
	}
	ts := now.Unix()
	// A job that finished is at 100% whether or not its worker said so, so a
	// forgotten last report does not leave a bar stuck at 97 percent.
	res, err := d.sql.Exec(`UPDATE jobs SET state=?, last_error=?, finished_at=?, updated_at=?,
		progress = CASE WHEN ? = 'done' THEN 1.0 ELSE progress END
		WHERE id=?`, string(state), errText, ts, ts, string(state), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RescheduleJob returns a retryable failure to the queue. The attempt stays
// counted: ClaimJob already spent it.
func (d *DB) RescheduleJob(id int64, availableAt time.Time, errText string, now time.Time) error {
	res, err := d.sql.Exec(`UPDATE jobs SET state='queued', available_at=?, last_error=?,
		finished_at=0, updated_at=? WHERE id=?`,
		unixOrZero(availableAt), errText, now.Unix(), id)
	if err != nil {
		// DEFENCE IN DEPTH, and the comment above says why it is needed.
		//
		// This branch cannot change an accepted write: a reschedule that does
		// not collide never reaches it. What it changes is the ONE reachable
		// collision -- a terminal row reached through the claim-to-register
		// window in Queue.Cancel -- which without it returns the driver's
		// "UNIQUE constraint failed: jobs.kind, jobs.target (2067)" to
		// jobs.finish, which only LOGS it. The job then stays cancelled
		// forever with a live worker's outcome thrown away and nothing on
		// screen, which is a worse ending than #746's 500.
		if isJobUniqueTargetViolation(err) {
			return jobUniqueTargetConflict(d.sql, id, "reschedule", err)
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RequeueJob returns a job to the queue AND refunds the attempt.
//
// The refund is why this is a separate method from RescheduleJob: it is only
// correct when the queue knows the attempt never got a fair run — its own clean
// shutdown, or a kind whose processor had not registered yet. A failure must
// never come through here, or the attempt ceiling stops meaning anything.
//
// THE UPDATE HAS NO STATE GUARD, and this was documented as not needing one on
// the strength of a caller-side invariant that turns out to have a hole. See
// the claim-to-register window described in the writer inventory above. It
// carries the same violation branch RetryJob does.
//
// The index covers queued, running and deferred. A row that is ALREADY in one
// of those states cannot collide by moving to another of them: its (kind,
// target) is already the one the index holds, and the index does not care which
// of the three it is. Only a row arriving from OUTSIDE the predicate can
// collide — that is RetryJob's whole problem, and it is the only writer here
// whose WHERE selects terminal rows.
//
// Both of this function's callers pass a job the queue itself just claimed:
// queue.start, when no worker is registered for the kind, and queue.finish, on
// the server's own shutdown. ClaimJob sets state='running' before either can
// run, so the row is running when it gets here. Same for RescheduleJob (called
// only from queue.finish on a retryable failure) and RequeueRunningJobs (whose
// WHERE is state='running'). Adding a guard would therefore change nothing
// about which writes are accepted, and would turn the shutdown path — where the
// alternative to requeuing is losing a four-hour transcode — into one with a
// new way to silently do nothing.
func (d *DB) RequeueJob(id int64, availableAt time.Time, reason string, now time.Time) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	logJSON := ""
	if reason != "" {
		logJSON, err = appendJobLogTx(tx, id, []string{reason})
		if err != nil {
			return err
		}
	}

	var res sql.Result
	if logJSON != "" {
		res, err = tx.Exec(`UPDATE jobs SET state='queued', attempts=MAX(attempts-1,0),
			available_at=?, started_at=0, finished_at=0, log_tail=?, updated_at=? WHERE id=?`,
			unixOrZero(availableAt), logJSON, now.Unix(), id)
	} else {
		res, err = tx.Exec(`UPDATE jobs SET state='queued', attempts=MAX(attempts-1,0),
			available_at=?, started_at=0, finished_at=0, updated_at=? WHERE id=?`,
			unixOrZero(availableAt), now.Unix(), id)
	}
	if err != nil {
		// The same defence RescheduleJob carries, for the same window and for
		// the same reason.
		//
		// READ ON tx, NOT ON d.sql, and this is not a preference. The pool is
		// SetMaxOpenConns(1): this transaction holds the only connection, so a
		// query issued on d.sql from in here waits for a connection that cannot
		// be released until this function returns, and the call hangs for ever
		// rather than failing. The first draft of this branch did exactly that
		// and deadlocked the test that covers it.
		//
		// tx can answer, because the row that won was committed BEFORE this
		// transaction began -- that is what made this UPDATE collide -- so it
		// is visible from inside. And SQLite does not abort a transaction on a
		// constraint violation, so the handle is still usable for a read.
		if isJobUniqueTargetViolation(err) {
			return jobUniqueTargetConflict(tx, id, "requeue", err)
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// RequeueRunningJobs is crash recovery, run once at startup.
//
// A row still marked running means the process died holding it. Requeuing it is
// the whole point of persisting jobs at all. The attempt is NOT refunded, and
// one that has already burned its budget is failed rather than requeued: a job
// that kills the server would otherwise kill it again on every boot, and a
// server that cannot finish starting is worse than a job that never ran.
func (d *DB) RequeueRunningJobs(now time.Time) (requeued, failed int, err error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	ts := now.Unix()
	res, err := tx.Exec(`UPDATE jobs SET state='failed',
		last_error='interrupted by a restart, with no attempts left',
		finished_at=?, updated_at=?
		WHERE state='running' AND attempts >= max_attempts`, ts, ts)
	if err != nil {
		return 0, 0, err
	}
	nFailed, _ := res.RowsAffected()

	res, err = tx.Exec(`UPDATE jobs SET state='queued', available_at=?,
		last_error='interrupted by a restart', started_at=0, updated_at=?
		WHERE state='running'`, ts, ts)
	if err != nil {
		return 0, 0, err
	}
	nRequeued, _ := res.RowsAffected()

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return int(nRequeued), int(nFailed), nil
}

// CancelJob marks a job cancelled. Only a non-terminal job can be cancelled,
// and the error says which state got in the way rather than just "no".
func (d *DB) CancelJob(id int64, now time.Time) error {
	ts := now.Unix()
	res, err := d.sql.Exec(`UPDATE jobs SET state='cancelled', finished_at=?, updated_at=?
		WHERE id=? AND state IN ('queued','deferred','running')`, ts, ts, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return jobStateConflict(d.sql, id, "cancel")
	}
	return nil
}

// DeferJob holds an eligible job back until at.
//
// A running job is deliberately not deferrable: deferring one would mean
// killing it, and throwing away half a transcode is a decision for a human
// through Cancel, not a side effect of a resource policy noticing load.
func (d *DB) DeferJob(id int64, at time.Time, reason string, now time.Time) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	logJSON := ""
	if reason != "" {
		logJSON, err = appendJobLogTx(tx, id, []string{reason})
		if err != nil {
			return err
		}
	}

	var res sql.Result
	if logJSON != "" {
		res, err = tx.Exec(`UPDATE jobs SET state='deferred', available_at=?, log_tail=?, updated_at=?
			WHERE id=? AND state IN `+claimableStates, unixOrZero(at), logJSON, now.Unix(), id)
	} else {
		res, err = tx.Exec(`UPDATE jobs SET state='deferred', available_at=?, updated_at=?
			WHERE id=? AND state IN `+claimableStates, unixOrZero(at), now.Unix(), id)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return jobStateConflict(tx, id, "defer")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// RetryJob re-arms a terminal job with a fresh attempt budget. It is the
// operator saying "the disk is fixed now, try again", so the attempt counter
// starts over rather than resuming against a ceiling already reached.
//
// THE ONE WRITER THAT MOVES A ROW INTO idx_jobs_unique_target FROM OUTSIDE IT.
// Its WHERE names the three terminal states, all of which the index's predicate
// excludes, and it sets state='queued', which the predicate includes. So a
// retry is an insert as far as that index is concerned, and it can be refused
// exactly as EnqueueJob's INSERT can -- when a SECOND job for the same kind and
// target became active while this one sat terminal. Two ordinary ways for that
// to happen, both measured (#746):
//
//   - a unique job fails, the operator resubmits (which is allowed: 'failed' is
//     outside the predicate, so the new job is not a duplicate), and then clicks
//     Retry on the old one;
//   - a twin that MigrateJobUniqueTarget itself cancelled sits beside its
//     survivor, carrying a last_error explaining that it was cancelled as a
//     duplicate -- which is an explanation that invites a Retry click, on the
//     path most likely to be taken right after the upgrade.
//
// Unhandled, the driver's violation walks out of the store and reaches
// internal/api's writeStoreError as an unrecognised error, which answers 500
// with "UNIQUE constraint failed: jobs.kind, jobs.target (2067)" as the body:
// precisely the outcome EnqueueJob's comment says must not happen -- an
// operator told the server is broken, in the vocabulary of an index.
//
// So the violation is matched and turned into ErrStateConflict, which
// writeStoreError already maps to 409, with a sentence naming the job that is
// already active. It is NOT folded the way EnqueueJob folds one: folding means
// "here is the job doing that work", and this signature returns only an error,
// so a nil would claim a re-arm that did not happen. 409 with the winner's id
// is the honest answer, and it is the one the operator can act on.
func (d *DB) RetryJob(id int64, now time.Time) error {
	ts := now.Unix()
	res, err := d.sql.Exec(`UPDATE jobs SET state='queued', attempts=0, progress=0,
		available_at=?, last_error='', started_at=0, finished_at=0, updated_at=?
		WHERE id=? AND state IN ('done','failed','cancelled')`, ts, ts, id)
	if err != nil {
		if isJobUniqueTargetViolation(err) {
			return jobUniqueTargetConflict(d.sql, id, "retry", err)
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return jobStateConflict(d.sql, id, "retry")
	}
	return nil
}

// DeleteJob removes a job row.
func (d *DB) DeleteJob(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM jobs WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeJobs drops finished jobs older than cutoff, keeping the newest keep of
// them whatever their age so the history page is never empty after a quiet
// month.
//
// It returns the rows it deleted, not a count, and that is the whole point of
// the signature. Some jobs own a file that outlives their row -- a clip.export
// writes into the exports directory, which nothing else ever sweeps -- and a
// caller cannot clean up after a row it can no longer read. A count told the
// caller how much it had just made unreachable and nothing about what (#222).
//
// The select and the delete run in one transaction so the rows returned are
// exactly the rows removed: this database takes a single writer, but a caller
// that deleted a file for a row a failed commit had put back would be worse
// than one that leaked it.
func (d *DB) PurgeJobs(cutoff time.Time, keep int) ([]jobs.Job, error) {
	if keep < 0 {
		keep = 0
	}
	// The same predicate twice, deliberately spelled once here.
	const where = `state IN ('done','failed','cancelled') AND finished_at > 0 AND finished_at < ?
		AND id NOT IN (
			SELECT id FROM jobs WHERE state IN ('done','failed','cancelled')
			ORDER BY finished_at DESC, id DESC LIMIT ?
		)`
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT `+jobColumns+` FROM jobs WHERE `+where,
		unixOrZero(cutoff), keep)
	if err != nil {
		return nil, err
	}
	var purged []jobs.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		purged = append(purged, *j)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if _, err := tx.Exec(`DELETE FROM jobs WHERE `+where,
		unixOrZero(cutoff), keep); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return purged, nil
}

// JobCounts is the jobs-by-state summary a status page wants without pulling
// every row.
func (d *DB) JobCounts() (map[jobs.State]int, error) {
	rows, err := d.sql.Query(`SELECT state, COUNT(*) FROM jobs GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[jobs.State]int{}
	for rows.Next() {
		var (
			state string
			n     int
		)
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		out[jobs.State(state)] = n
	}
	return out, rows.Err()
}

// appendJobLogTx reads, appends and trims the log tail, returning the JSON to
// write. Read-modify-write rather than SQL string surgery: the tail has to be
// bounded by lines, the rows are tiny, and this database takes one writer.
func appendJobLogTx(tx *sql.Tx, id int64, lines []string) (string, error) {
	var existing string
	err := tx.QueryRow(`SELECT log_tail FROM jobs WHERE id = ?`, id).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	var have []string
	if existing != "" && existing != "[]" {
		_ = json.Unmarshal([]byte(existing), &have)
	}
	return marshalJobLog(jobs.TrimLog(append(have, lines...)))
}

func marshalJobLog(lines []string) (string, error) {
	lines = jobs.TrimLog(lines)
	if len(lines) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(lines)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// rowQuerier is satisfied by both *sql.DB and *sql.Tx.
//
// It exists because this database runs on a single connection: a helper that
// reached for d.sql while a transaction held that connection would not return
// an error, it would deadlock, and it did.
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// ErrStateConflict marks "the row is there, but its state does not allow what
// was asked" -- a retry of a job that never finished, a cancel of one already
// cancelled, a delete of one with a worker on it.
//
// It exists so callers can tell that case apart from a genuine store failure
// WITHOUT matching on the sentence. Until it existed, every one of these
// reached internal/api's writeStoreError as an unrecognised error and was
// answered 500, which tells an operator "the server is broken" about a request
// that was merely inapplicable (#221).
//
// The sentence still carries the detail; this only classifies it.
var ErrStateConflict = errors.New("job state conflict")

// jobStateConflict turns "the UPDATE matched nothing" into an error that says
// why: the job is gone, or it is in a state this operation does not apply to.
//
// The two answers are deliberately different KINDS of error, not two spellings
// of one: a missing row is ErrNotFound (404), a wrong state is ErrStateConflict
// (409). Collapsing them would make "there is no such job" and "that job is
// already running" indistinguishable to the caller.
func jobStateConflict(q rowQuerier, id int64, verb string) error {
	var state string
	err := q.QueryRow(`SELECT state FROM jobs WHERE id = ?`, id).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: cannot %s job %d: it is %s", ErrStateConflict, verb, id, state)
}

// jobUniqueTargetConflict turns idx_jobs_unique_target refusing a state change
// into the same KIND of answer jobStateConflict gives: ErrStateConflict, which
// writeStoreError maps to 409.
//
// It is the sibling of the fold in EnqueueJob and exists for the same reason --
// the index's refusal is information the operator can use, and the driver's
// rendering of it is not. The sentence names the job that is already active,
// because "you cannot retry this" without saying what is in the way leaves the
// operator with nothing to click.
//
// cause is carried only so a read that fails on the way to the better sentence
// can hand back the original violation rather than an invented one; it is never
// wrapped into the conflict, which would put the index's name back in the body.
func jobUniqueTargetConflict(q rowQuerier, id int64, verb string, cause error) error {
	var kind, target string
	err := q.QueryRow(`SELECT kind, target FROM jobs WHERE id = ?`, id).Scan(&kind, &target)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return cause
	}

	active, findErr := activeJobFor(q, kind, target)
	if findErr != nil || active == nil {
		// Either the read failed, or the winner reached a terminal state
		// between the refusal and this line -- in which case the retry would
		// succeed if asked again. Both get the conflict rather than the raw
		// violation: 409 is still the right code (the request was inapplicable
		// when it ran, not the server failing), and a retriable one is exactly
		// what a caller should do with it.
		return fmt.Errorf("%w: cannot %s job %d: another %s job for %s was already active",
			ErrStateConflict, verb, id, kind, target)
	}
	return fmt.Errorf("%w: cannot %s job %d: job %d is already active for %s of %s",
		ErrStateConflict, verb, id, active.ID, kind, target)
}

func timeOrZero(unix int64) time.Time {
	if unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}
