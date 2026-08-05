package engine

import (
	"slices"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// B2. Processes() appended only d.proc, so GET /processes/dest:<id>:backup/logs
// answered "no such process" -- both API consumers go through this function.
// The card shows the backup's state, so an operator could see that redundancy
// was broken and had no way to find out why, at the one moment those logs exist
// for.
//
// Asserted by NAME rather than by count: a count would also be satisfied by the
// e.backup that was already appended, which is the source-side backup-INGEST
// tier and a different thing entirely.
//
// Mutation: in Processes, delete the `if d.backup != nil { out = append(out,
// d.backup) }` block. Observed to fail -- dest:9:backup was absent.
func TestTheMonitoringPageListsTheBackupOutput(t *testing.T) {
	primary := destSubName(9, "")
	backup := destSubName(9, destRoleBackup)
	e := &Engine{
		dests: map[int64]*destination{9: {
			row:    backupRow(),
			proc:   supervisor.New(testLogger(), supervisor.Spec{Name: primary}),
			backup: supervisor.New(testLogger(), supervisor.Spec{Name: backup}),
		}},
		rends:     map[int64]*rendition{},
		loud:      map[int64]*loudnessMon{},
		playProcs: map[string]*supervisor.Process{},
	}

	var names []string
	for _, p := range e.Processes() {
		names = append(names, p.Name())
	}
	if !slices.Contains(names, primary) {
		t.Fatalf("the primary is missing from %v; the fixture is wrong, not the code", names)
	}
	if !slices.Contains(names, backup) {
		t.Errorf("Processes() = %v: the backup's logs and command line are "+
			"unreachable, so the one comparison destArgs exists to make cannot be made", names)
	}
}
