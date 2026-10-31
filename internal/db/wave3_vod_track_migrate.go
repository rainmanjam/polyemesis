package db

// MigrateWave3VODTrack is the reserved schema slot for #15 Twitch VOD audio track.
//
// THIS SLOT STAYS EMPTY, A DELIBERATE DEVIATION FROM THE KIT. The kit reserved it
// for "the per-destination VOD track setting column". That column already
// exists, so a second one would only duplicate state:
//
//   - the VOD track itself is destinations.vod_profile (db.Destination.VODProfile),
//     which is nil when the destination has no second mix and holds the mix's
//     routing when it does; added by the destinations ALTER list in destinations.go;
//   - the Enhanced Broadcasting opt-in that the Twitch gate reads is
//     destinations.multitrack (db.Destination.Multitrack), added by the same
//     destinations ALTER list.
//
// Adding a third column would give the engine two answers to "is the VOD track
// on", and the gate reads the existing two. The reserved slot is therefore left
// as a no-op, and the body must stay idempotent in case a later change does need
// a column. Its test is wave3_vod_track_migrate_test.go.
func (d *DB) MigrateWave3VODTrack() error { return nil }
