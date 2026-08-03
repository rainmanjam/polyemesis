package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// migrateLegacyPlaylistFilePath runs once at startup, after the database is
// open and the data directory is known, to carry a pre-Items playlist
// FilePath (DESIGN 2026-08-01-playlist-items) forward.
//
// Deliberately NOT part of internal/db.GetSettings, which is where an
// earlier version of this migration lived. GetSettings has around twenty
// callers, several of them per-request API handlers, and running a
// filesystem resolve, a Stat and a settings write on every one of them
// would: stall every settings read on a slow or hung uploads volume (a
// write-shaped cost -- uploads.New calls MkdirAll -- on what is supposed to
// be a read path); redo the same work forever, because nothing on that path
// ever persists the result; and warn through slog's unconfigured default
// logger rather than the app's real one, since ten of db.Open's eleven call
// sites wire no logger at all -- "your filler is gone" landing somewhere
// nobody is reading is the exact silence the warning exists to prevent.
// Doing it here instead -- once, before the server starts accepting
// anything, with the real DataDir and the real configured logger -- is what
// makes it one-shot, durable (PutSettings persists it) and audible.
//
// FilePath and Upload are not the same namespace: FilePath was relative to
// the data directory, and Upload is a bare name inside the uploads
// subdirectory beneath it. So this only trusts a legacy value that is a bare
// filename AND already exists as a real file under the uploads directory --
// the one case that is honestly the same file under a narrower namespace.
// Everything else is refused, loudly, rather than guessed at: a playlist
// left unmigrated is at least visible in the logs and the settings form; one
// that starts pointed at the wrong file, or fails validation on the next
// save, is a mystery an operator meets during the outage the playlist exists
// to cover. See db.PlaylistItem and db.PlaylistSettings for the rest of that
// reasoning.
//
// Naturally runs at most once with an effect: db.DB.LegacyPlaylistFilePath
// reports "" once Items is non-empty, because PutSettings marshals from the
// current Settings struct, which has no FilePath field left to write -- so a
// second call on a later restart finds no legacy key in the stored blob at
// all and returns immediately.
func migrateLegacyPlaylistFilePath(store *db.DB, dataDir string, log *slog.Logger) error {
	legacy, err := store.LegacyPlaylistFilePath()
	if err != nil {
		return fmt.Errorf("read legacy playlist filePath: %w", err)
	}
	if legacy == "" {
		return nil
	}

	const help = "upload the file through the uploads page and re-select it in the playlist"
	refuse := func(reason string, args ...any) {
		log.Warn("playlist: legacy filePath left unmigrated; "+reason+". "+help,
			append([]any{"filePath", legacy}, args...)...)
	}

	up, err := uploads.New(dataDir)
	if err != nil {
		refuse("could not open the uploads store", "err", err)
		return nil
	}
	resolved, err := up.Resolve(legacy)
	if err != nil {
		// Store.Resolve is the authority on what escapes the uploads
		// directory or is shaped wrong (a separator, "", ".", ".."); refusing
		// here rather than erroring out of startup, because a legacy value
		// that cannot be migrated is not a reason to refuse to boot.
		refuse("is not a bare upload name inside the uploads directory", "err", err)
		return nil
	}
	if _, err := os.Stat(resolved); err != nil {
		refuse("no upload named this exists yet", "err", err)
		return nil
	}

	s, err := store.GetSettings()
	if err != nil {
		return fmt.Errorf("load settings for playlist migration: %w", err)
	}
	if len(s.Failover.Playlist.Items) > 0 {
		// Items was populated some other way between the two reads above --
		// in principle, an operator saving new settings through the API
		// while this runs -- so there is nothing left for this migration to
		// contribute.
		return nil
	}
	// Validated with the SAME validator the settings API applies, immediately
	// before the write. Resolve and the stat above answer "does this name a
	// real upload"; they do not answer "is this a value db.Settings.Validate
	// will accept", and the two differ -- most concretely at
	// MaxPlaylistItemUpload, which a legacy filePath predates and is not bound
	// by. A 600-character bare filename that exists on disk would migrate
	// happily here and then make the operator's NEXT settings save fail
	// validation on a value they never typed. Vanishingly unlikely; refusing it
	// is one call and makes this migration's guarantee total, which is worth
	// more than the case is likely.
	migrated := db.PlaylistSettings{Items: []db.PlaylistItem{{Upload: legacy}}}
	if err := migrated.PlaylistFileProblem(); err != nil {
		refuse("is not a value the settings validator will accept", "err", err)
		return nil
	}
	s.Failover.Playlist.Items = migrated.Items
	// PutSettings rather than db.UpdateSettings, which is what every
	// read-modify-write in the RUNNING server goes through. This one runs once
	// at startup, before the HTTP listener is up and before the engine's
	// scheduler exists, so there is no second writer for it to be serialised
	// against -- and it has already done UpdateSettings' other job by hand
	// above, validating the migrated value with the settings validator itself.
	if err := store.PutSettings(s); err != nil {
		return fmt.Errorf("persist migrated playlist: %w", err)
	}
	log.Warn("playlist: migrated legacy filePath to an upload item", "filePath", legacy)
	return nil
}
