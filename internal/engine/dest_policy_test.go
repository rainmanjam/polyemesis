package engine

import (
	"context"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// B3. applyDestPolicy was primary-only -- every reference was d.proc -- and
// backupSpecOf deliberately excludes Resilience, so reconcileBackup
// short-circuits on an unchanged spec. Between them, nothing in the file
// delivered a changed reconnect policy to the redundant feed, while
// noteReload(..., reloadLive, "reconnect policy retuned...") told the operator
// it had been.
//
// Mutation: in applyDestPolicy, delete the line
// `e.retunePolicy(d.backup, row, want, destRoleBackup)`. Observed to fail --
// the backup kept the policy it was built with.
func TestRetuningTheReconnectPolicyReachesTheBackupToo(t *testing.T) {
	primary := supervisor.New(testLogger(), supervisor.Spec{Name: destSubName(7, "")})
	backup := supervisor.New(testLogger(), supervisor.Spec{Name: destSubName(7, destRoleBackup)})
	d := &destination{row: backupRow(), proc: primary, backup: backup}
	e := &Engine{log: testLogger()}

	row := backupRow()
	row.Resilience = db.DestResilience{MinBackoffSeconds: 4, MaxBackoffSeconds: 60, GiveUpAfter: 9}
	e.applyDestPolicy(d, row)

	want := supervisor.Policy{MinBackoff: 4 * time.Second, MaxBackoff: 60 * time.Second, MaxRestarts: 9}
	if got := primary.Policy(); got != want {
		t.Fatalf("the PRIMARY's policy = %+v, want %+v; the fixture is wrong, not the code", got, want)
	}
	if got := backup.Policy(); got != want {
		t.Errorf("the backup's policy = %+v, want %+v: the operator was told the "+
			"reconnect policy was retuned and the redundant feed never heard about it", got, want)
	}
}

// The half that hurts. A backup that has exhausted the old limit sits in
// StateFailed for ever: Start() is a no-op down that path, and nothing but this
// revival calls Restart on it. So "I raised giveUpAfter because the platform
// was flapping" brought the primary back and left redundancy dead, with the
// card the only thing that knew.
//
// Mutation: in applyDestPolicy, delete the line
// `e.retunePolicy(d.backup, row, want, destRoleBackup)`. Observed to fail --
// the backup was still StateFailed at the timeout.
func TestABackupThatGaveUpIsRevivedWhenTheLimitIsRaised(t *testing.T) {
	// A binary that cannot be executed fails every spawn, so one restart is
	// enough to exhaust the limit.
	backup := supervisor.New(testLogger(), supervisor.Spec{
		Name: destSubName(7, destRoleBackup), Bin: t.TempDir() + "/no-such-ffmpeg",
		AutoRestart: true,
		MinBackoff:  time.Millisecond, MaxBackoff: time.Millisecond, MaxRestarts: 1,
	})
	backup.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		backup.Stop(ctx)
	})
	waitUntil(t, func() bool {
		return backup.Status().State == supervisor.StateFailed
	}, "the backup to give up")
	gaveUpAt := backup.Status().Restarts

	d := &destination{row: backupRow(), backup: backup}
	e := &Engine{log: testLogger()}
	row := backupRow()
	// More forgiving than the 1 it gave up under.
	row.Resilience = db.DestResilience{MinBackoffSeconds: 1, MaxBackoffSeconds: 1, GiveUpAfter: 50}
	e.applyDestPolicy(d, row)

	waitUntil(t, func() bool {
		return backup.Status().Restarts > gaveUpAt
	}, "the backup to be spawned again after the limit was raised")
}
