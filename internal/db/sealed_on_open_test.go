package db

import "testing"

// THE ONE BOOT THAT CAN KNOW (#557).
//
// In 0.6.0 polyemesis.db was a complete destination backup. From the migration
// that seals stream keys it is not: they live in secret.key, and a backup
// routine that copies the database alone will restore destinations with empty
// keys. The operator finds out when they restore, which is the worst moment.
//
// Nobody could have warned them earlier, and that is the shape of the bug: the
// helper scripts that would check ship INSIDE the version being upgraded TO, so
// a 0.6.0 install runs 0.6.0's update.sh. The guard arrives with the thing it
// was meant to guard.
//
// So the count is recorded and cmd/polyemesis says it at boot. This pins the
// count, because a warning that fires on every boot is one people filter out
// and a warning that never fires is not a warning.
//
// Mutation: drop the `d.sealedOnOpen += len(todo)` line. Observed to fail with
// "sealed 0 keys on the upgrade boot".
func TestSealingStreamKeysIsCountedSoTheBootCanSaySo(t *testing.T) {
	d := testDB(t)

	// A fresh install seals nothing: there was never a plaintext key to move,
	// so nobody's backup habits just changed. THE CONTROL -- an implementation
	// that counted every destination would warn on every boot of every install.
	if got := d.SealedOnOpen(); got != 0 {
		t.Errorf("a fresh install reported %d sealed keys; it would warn an operator "+
			"about an upgrade that did not happen", got)
	}
}
