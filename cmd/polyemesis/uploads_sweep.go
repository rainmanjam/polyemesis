package main

import (
	"log/slog"

	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// sweepUploadLeftovers clears what a killed process left in the uploads
// directory, once, at startup.
//
// #185. Nothing in the product ever swept <dataDir>/uploads. A ".partial-"
// file survives a process that died mid-upload or a Discard whose os.Remove
// failed, and a ".probe-" sidecar survives an upload removed by something
// outside this process. Neither is listable, selectable or nameable by a
// playlist item -- uploads.Listable is what bought that -- so neither can reach
// air or create a dangling reference. What they do is occupy the volume the
// database, the recorder and the HLS preview share, up to MaxUploadBytes per
// incident, with the free-space floor unaware of them and nothing anywhere
// reporting the total.
//
// AT STARTUP RATHER THAN ON A TIMER, and the age gate is what makes the choice
// reversible rather than load-bearing. At this point in run() no upload can be
// in flight -- the HTTP server has not been built, let alone started -- so the
// only ".partial-" files that can exist are leftovers. uploads.SweepAfter is
// still applied, so a future caller on a timer inherits a sweep that already
// cannot race a live upload; see uploads.Sweep.
//
// IT DOES NOT FAIL STARTUP. A directory that cannot be read is worth a warning
// and nothing more: the uploads path is not on the critical path for ingest or
// recording, and refusing to boot over a tidy-up would take a live broadcast
// off air for the sake of disk space. The same reasoning migrateLegacyPlaylist-
// FilePath's refusals use.
//
// IT LOGS WHAT IT REMOVED, and only when it removed something. A line every
// boot saying "nothing to do" is a line operators learn to skip, and this one
// has to be readable on the boot where it matters -- the one after the crash
// that stranded 8 GiB.
func sweepUploadLeftovers(dataDir string, log *slog.Logger) {
	store, err := uploads.New(dataDir)
	if err != nil {
		log.Warn("cannot open the uploads store to clear leftovers", "err", err)
		return
	}
	res, err := store.Sweep(uploads.SweepAfter)
	if err != nil {
		log.Warn("cannot read the uploads directory to clear leftovers", "err", err)
		return
	}
	if res.Empty() {
		return
	}
	log.Info("cleared leftovers from the uploads directory",
		"stagedFiles", res.Staged, "stagedBytes", res.StagedBytes,
		"orphanedProbeRecords", res.Sidecars)
}
