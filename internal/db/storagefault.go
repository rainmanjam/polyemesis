package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"sync"
	"time"

	sqlite "modernc.org/sqlite"
)

// StorageFault is the most recent failure SQLite reported about the storage
// under the database, as opposed to about a query.
//
// WHY THIS EXISTS: Ping reads page one, and a volume that is full, or a file
// with a damaged page somewhere past page one, serves page one perfectly well.
// So /health said "database ok" while every save answered "database or disk is
// full" (exploratory CH-03), and on a database whose hooks table had a corrupt
// root page it said ok while GET /hooks answered 500 and every hook had silently
// stopped (CH-09). Nothing asked the queries the application was actually
// running how they were going.
type StorageFault struct {
	// Err is SQLite's own sentence, e.g. "database or disk is full (13)".
	Err string
	// Code is the primary SQLite result code.
	Code int
	// At is when it was last seen.
	At time.Time
	// Damaged is true for corruption: the file itself is wrong, and nothing
	// the process does will make that go away. A full or unwritable volume is
	// not damaged, and the fault clears on the next write that succeeds.
	Damaged bool
}

// The primary result codes that describe the storage rather than the query.
// Extended codes (SQLITE_IOERR_WRITE and the rest) carry the primary code in
// the low byte.
const (
	sqliteReadOnly = 8
	sqliteIOErr    = 10
	sqliteCorrupt  = 11
	sqliteFull     = 13
	sqliteNotADB   = 26
)

// StorageFault reports the current storage fault, if there is one.
func (d *DB) StorageFault() (StorageFault, bool) {
	if d.faults == nil {
		return StorageFault{}, false
	}
	return d.faults.current()
}

// faultLog is shared by every connection of one DB.
type faultLog struct {
	mu    sync.Mutex
	fault *StorageFault
}

func (l *faultLog) current() (StorageFault, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fault == nil {
		return StorageFault{}, false
	}
	return *l.fault, true
}

// note records err if it is about the storage. Anything else -- a constraint,
// a syntax error, a busy timeout -- is about the query and is the caller's to
// report.
func (l *faultLog) note(err error) {
	var se *sqlite.Error
	if err == nil || !errors.As(err, &se) {
		return
	}
	code := se.Code() & 0xff
	var damaged bool
	switch code {
	case sqliteCorrupt, sqliteNotADB:
		damaged = true
	case sqliteFull, sqliteIOErr, sqliteReadOnly:
	default:
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// Damage is not overwritten by a lesser fault: a full disk on a corrupt
	// file is still a corrupt file.
	if l.fault != nil && l.fault.Damaged && !damaged {
		l.fault.At = time.Now()
		return
	}
	l.fault = &StorageFault{Err: se.Error(), Code: code, At: time.Now(), Damaged: damaged}
}

// wrote records a write that reached the file. It is the only thing that ends
// a full or unwritable fault: the space came back, or the volume was remounted.
// Damage outlives it -- one table can be written while another's pages are
// still corrupt.
func (l *faultLog) wrote() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fault != nil && !l.fault.Damaged {
		l.fault = nil
	}
}

// observedConnector opens connections through the registered sqlite driver and
// wraps each one so every statement's outcome passes through a faultLog.
//
// A DRIVER WRAPPER, NOT A HELPER THE STORE CALLS. The store has well over a
// hundred call sites on d.sql; a helper would be a hundred chances to forget
// it. This sits under all of them, including the ones not written yet.
type observedConnector struct {
	dsn  string
	base driver.Driver
	log  *faultLog
}

func newObservedConnector(dsn string, log *faultLog) (*observedConnector, error) {
	// The driver registered as "sqlite" by modernc's init. Taken from a handle
	// rather than constructed, so any configuration the registration carries
	// is kept. sql.Open does not connect.
	probe, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	base := probe.Driver()
	_ = probe.Close()
	return &observedConnector{dsn: dsn, base: base, log: log}, nil
}

func (c *observedConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.base.Open(c.dsn)
	if err != nil {
		c.log.note(err)
		return nil, err
	}
	return &observedConn{Conn: conn, log: c.log}, nil
}

func (c *observedConnector) Driver() driver.Driver { return c.base }

// observedConn forwards every interface modernc's conn implements. It must
// implement exactly those and no others: database/sql chooses its code path by
// type assertion, so a method added here that the real conn lacks changes
// behaviour, and one left off makes database/sql fall back to a slower path.
type observedConn struct {
	driver.Conn
	log *faultLog
	// inTx and txWrote track whether the open transaction changed anything, so
	// its commit can count as a write. A read-only transaction commits too.
	inTx, txWrote bool
}

func (c *observedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	ex, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	res, err := ex.ExecContext(ctx, query, args)
	return c.execDone(res, err)
}

// execDone is the outcome of a statement that may have written, whether it ran
// on the connection or through a prepared statement.
func (c *observedConn) execDone(res driver.Result, err error) (driver.Result, error) {
	if err != nil {
		c.log.note(err)
		return res, err
	}
	// Only a statement that changed rows proves the file takes writes. A
	// PRAGMA or a CREATE IF NOT EXISTS that did nothing proves nothing.
	if n, rerr := res.RowsAffected(); rerr == nil && n > 0 {
		if c.inTx {
			c.txWrote = true
		} else {
			c.log.wrote()
		}
	}
	return res, nil
}

// queryDone wraps a query's rows so an error met while iterating them is seen
// too. Damage past the first page a query touches does not fail the query: it
// fails the rows.Next that reaches it, after the rows before it came back.
func (c *observedConn) queryDone(rows driver.Rows, err error) (driver.Rows, error) {
	if err != nil {
		c.log.note(err)
		return rows, err
	}
	return &observedRows{rows: rows, log: c.log}, nil
}

func (c *observedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return c.queryDone(q.QueryContext(ctx, query, args))
}

func (c *observedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var (
		st  driver.Stmt
		err error
	)
	if p, ok := c.Conn.(driver.ConnPrepareContext); ok {
		st, err = p.PrepareContext(ctx, query)
	} else {
		st, err = c.Conn.Prepare(query)
	}
	if err != nil {
		c.log.note(err)
		return nil, err
	}
	// The statement is wrapped too: tx.Prepare is how the chat batch insert
	// and the destination reorders write, and a full disk met there, or a
	// commit of what they wrote, must count like any other statement's.
	return &observedStmt{Stmt: st, conn: c}, nil
}

func (c *observedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	var (
		tx  driver.Tx
		err error
	)
	if b, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = b.BeginTx(ctx, opts)
	} else {
		tx, err = c.Conn.Begin() //nolint:staticcheck // the fallback database/sql itself uses
	}
	if err != nil {
		c.log.note(err)
		return nil, err
	}
	c.inTx, c.txWrote = true, false
	return &observedTx{Tx: tx, conn: c}, nil
}

func (c *observedConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		err := p.Ping(ctx)
		c.log.note(err)
		return err
	}
	return nil
}

func (c *observedConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *observedConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

type observedTx struct {
	driver.Tx
	conn *observedConn
}

func (t *observedTx) Commit() error {
	wrote := t.conn.txWrote
	t.conn.inTx, t.conn.txWrote = false, false
	if err := t.Tx.Commit(); err != nil {
		// A full disk usually surfaces HERE, not at the INSERT: in WAL mode
		// the pages are only written out when the transaction commits.
		t.conn.log.note(err)
		return err
	}
	if wrote {
		t.conn.log.wrote()
	}
	return nil
}

func (t *observedTx) Rollback() error {
	t.conn.inTx, t.conn.txWrote = false, false
	err := t.Tx.Rollback()
	t.conn.log.note(err)
	return err
}

// observedStmt forwards exactly what modernc's stmt implements: Close,
// NumInput, Exec, Query and the two Context forms. The legacy Exec and Query
// are what database/sql falls back to without the Context forms, so they are
// observed as well rather than left as a way around the log.
type observedStmt struct {
	driver.Stmt
	conn *observedConn
}

func (s *observedStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.conn.execDone(s.Stmt.Exec(args)) //nolint:staticcheck // forwarding the legacy form the interface still carries
}

func (s *observedStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.conn.queryDone(s.Stmt.Query(args)) //nolint:staticcheck // forwarding the legacy form the interface still carries
}

func (s *observedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	ex, ok := s.Stmt.(driver.StmtExecContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return s.conn.execDone(ex.ExecContext(ctx, args))
}

func (s *observedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := s.Stmt.(driver.StmtQueryContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return s.conn.queryDone(q.QueryContext(ctx, args))
}

// observedRows passes each row through and notes the error that ends the
// iteration early. io.EOF is the normal end of the rows, not a fault.
//
// It forwards Columns, Close and Next, plus the ColumnType* interfaces
// modernc's rows implements, so sql.Rows.ColumnTypes reports the same types
// through the wrapper as without it.
type observedRows struct {
	rows driver.Rows
	log  *faultLog
}

func (r *observedRows) Columns() []string { return r.rows.Columns() }
func (r *observedRows) Close() error      { return r.rows.Close() }

func (r *observedRows) Next(dest []driver.Value) error {
	err := r.rows.Next(dest)
	if err != nil && !errors.Is(err, io.EOF) {
		r.log.note(err)
	}
	return err
}

func (r *observedRows) ColumnTypeDatabaseTypeName(index int) string {
	if t, ok := r.rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return t.ColumnTypeDatabaseTypeName(index)
	}
	return ""
}

func (r *observedRows) ColumnTypeLength(index int) (int64, bool) {
	if t, ok := r.rows.(driver.RowsColumnTypeLength); ok {
		return t.ColumnTypeLength(index)
	}
	return 0, false
}

func (r *observedRows) ColumnTypeNullable(index int) (bool, bool) {
	if t, ok := r.rows.(driver.RowsColumnTypeNullable); ok {
		return t.ColumnTypeNullable(index)
	}
	return false, false
}

func (r *observedRows) ColumnTypePrecisionScale(index int) (int64, int64, bool) {
	if t, ok := r.rows.(driver.RowsColumnTypePrecisionScale); ok {
		return t.ColumnTypePrecisionScale(index)
	}
	return 0, 0, false
}

func (r *observedRows) ColumnTypeScanType(index int) reflect.Type {
	if t, ok := r.rows.(driver.RowsColumnTypeScanType); ok {
		return t.ColumnTypeScanType(index)
	}
	return reflect.TypeOf(new(any)).Elem()
}
