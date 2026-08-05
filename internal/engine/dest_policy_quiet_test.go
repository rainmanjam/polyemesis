package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// A destination nobody configured must be silent.
//
// destPolicy reads the database row, where "unconfigured" is 0 in all three
// fields. supervisor.New filled those zeroes in with its own defaults before
// the process ever ran. Compared raw, `before == want` was NEVER true for such
// a destination -- so every reconcile logged "reconnect policy retuned without
// a restart" and added a reload note, on a destination the operator had not
// touched. Carrying the policy to the backup as well doubled it.
//
// That matters beyond tidiness. The reload note is what the settings response
// shows an operator to tell them what their save actually moved; a note that
// appears every time is a note that means nothing, and it appears next to the
// ones that do.
//
// Asserts on the NOTES rather than on the log, because the note is the thing
// the operator sees and the log is not addressed to them.
//
// Mutation proving it can fail: in retunePolicy, delete the line
// `want = want.Normalised()`. Measured: FAIL, 2 notes, both reading
// "reconnect policy retuned to 0s..0s, giving up after 0" -- the defect and
// its doubling, verbatim.
//
// It has to be the WANT side. Deleting `.Normalised()` from `before` alone
// leaves this green, because supervisor.New already normalised the process's
// own policy when it built it -- so `before` is the filled-in value either
// way. Only the row-derived side carries the raw zeroes. A guard is worth
// exactly the mutation that was actually observed to break it, and the first
// mutation recorded here was the wrong one.
func TestAnUnconfiguredDestinationIsNotReportedAsRetuned(t *testing.T) {
	e := &Engine{log: testLogger()}
	rec := newReloadRecorder()
	e.reloadRec.Store(rec)

	row := backupRow()
	row.Resilience = db.DestResilience{} // exactly what an untouched row holds.

	d := &destination{
		row:    row,
		proc:   supervisor.New(testLogger(), supervisor.Spec{Name: destSubName(7, "")}),
		backup: supervisor.New(testLogger(), supervisor.Spec{Name: destSubName(7, destRoleBackup)}),
	}
	e.applyDestPolicy(d, row)

	if notes := rec.snapshot(); len(notes) != 0 {
		t.Errorf("an untouched destination produced %d reload notes: %+v.\n"+
			"Every reconcile would report a policy change nobody made, on both "+
			"the primary and the backup.", len(notes), notes)
	}
}

// The other side of the same comparison, so normalising cannot be "fixed" by
// making retunePolicy return early for everything. A real edit must still land.
//
// Mutation proving it can fail: in retunePolicy, replace the body after the
// nil check with `return`. Measured: FAIL, 0 notes.
func TestARealPolicyEditIsStillReported(t *testing.T) {
	e := &Engine{log: testLogger()}
	rec := newReloadRecorder()
	e.reloadRec.Store(rec)

	row := backupRow()
	row.Resilience = db.DestResilience{MinBackoffSeconds: 4, MaxBackoffSeconds: 60, GiveUpAfter: 9}

	d := &destination{
		row:    row,
		proc:   supervisor.New(testLogger(), supervisor.Spec{Name: destSubName(7, "")}),
		backup: supervisor.New(testLogger(), supervisor.Spec{Name: destSubName(7, destRoleBackup)}),
	}
	e.applyDestPolicy(d, row)

	if notes := rec.snapshot(); len(notes) == 0 {
		t.Error("raising giveUpAfter produced no reload note, so an operator who " +
			"changed it is told nothing happened")
	}
}
