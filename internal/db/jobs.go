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
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return d.GetJob(id)
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
		ph := make([]string, len(f.States))
		for i, s := range f.States {
			ph[i] = "?"
			args = append(args, string(s))
		}
		where = append(where, `state IN (`+strings.Join(ph, ",")+`)`)
	}
	if len(f.Kinds) > 0 {
		ph := make([]string, len(f.Kinds))
		for i, k := range f.Kinds {
			ph[i] = "?"
			args = append(args, string(k))
		}
		where = append(where, `kind IN (`+strings.Join(ph, ",")+`)`)
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
	j, err := scanJob(d.sql.QueryRow(jobActiveQuery, string(kind), target))
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
	ph := make([]string, len(kinds))
	args := []any{now.Unix()}
	for i, k := range kinds {
		ph[i] = "?"
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
		WHERE state IN `+claimableStates+` AND available_at <= ? AND kind IN (`+strings.Join(ph, ",")+`)
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
func (d *DB) RetryJob(id int64, now time.Time) error {
	ts := now.Unix()
	res, err := d.sql.Exec(`UPDATE jobs SET state='queued', attempts=0, progress=0,
		available_at=?, last_error='', started_at=0, finished_at=0, updated_at=?
		WHERE id=? AND state IN ('done','failed','cancelled')`, ts, ts, id)
	if err != nil {
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
func (d *DB) PurgeJobs(cutoff time.Time, keep int) (int, error) {
	if keep < 0 {
		keep = 0
	}
	res, err := d.sql.Exec(`DELETE FROM jobs
		WHERE state IN ('done','failed','cancelled') AND finished_at > 0 AND finished_at < ?
		AND id NOT IN (
			SELECT id FROM jobs WHERE state IN ('done','failed','cancelled')
			ORDER BY finished_at DESC, id DESC LIMIT ?
		)`, unixOrZero(cutoff), keep)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
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

func timeOrZero(unix int64) time.Time {
	if unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}
