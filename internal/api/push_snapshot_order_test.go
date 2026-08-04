package api

import (
	"os"
	"strings"
	"testing"
)

// handlePushMetadata must snapshot the job BEFORE it starts the workers.
//
// # WHY THIS IS A SOURCE-ORDER TEST AND NOT A BEHAVIOURAL ONE
//
// The behavioural test for this contract already exists --
// TestPushMetadataReturnsImmediatelyWithEveryAccountPending -- and it is the
// one that caught the bug, on a CI runner. It is not a usable guard against
// its return, and that was measured rather than assumed: with the fix reverted,
// 400 local runs produced ZERO failures. The window is a few microseconds wide
// and whether it opens depends on the machine.
//
// A guard that fails one run in several hundred, on some hardware, is not a
// guard. So this asserts the only thing that is deterministic: that the
// snapshot is taken before the goroutine exists, which is what makes the
// window impossible rather than unlikely.
//
// It is crude, and it is honest about what it can prove. If handlePushMetadata
// is restructured so these two lines no longer sit near each other, replace
// this with something better rather than deleting it.
func TestThePushSnapshotIsTakenBeforeTheWorkersStart(t *testing.T) {
	raw, err := os.ReadFile("metadata.go")
	if err != nil {
		t.Fatalf("cannot read metadata.go: %v", err)
	}
	src := string(raw)

	snapAt := strings.Index(src, "snap, _ := metadataRegistry.snapshot(job.ID)")
	goAt := strings.Index(src, "go s.runMetadataPush(")
	if snapAt < 0 || goAt < 0 {
		t.Fatalf("cannot find both lines (snapshot=%d, go=%d); handlePushMetadata "+
			"has been restructured and this guard needs rewriting", snapAt, goAt)
	}
	if snapAt > goAt {
		t.Error("the job is snapshotted AFTER the workers are started. A worker " +
			"that finishes first turns the 202's \"everything pending\" into " +
			"\"whatever happened to be true by then\" -- which is what CI caught " +
			"as: twitch started as \"error\", want pending.")
	}
}
