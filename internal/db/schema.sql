-- polyemesis schema. Applied idempotently at startup.

-- token_epoch is what makes a session revocable. Sessions are stateless JWTs,
-- so clearing the cookie at logout does not stop anyone holding a copy of the
-- token from continuing to use it until it expires. The epoch is embedded in
-- every token issued and checked on every request; bumping it here invalidates
-- every token already in the wild, which is what "change my password because I
-- think someone else has my session" has to mean.
CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    token_epoch   INTEGER NOT NULL DEFAULT 0
);

-- Long-lived credentials for automation, so a script never needs the admin
-- password or a cookie jar. Only the SHA-256 of the token is stored: the
-- plaintext is 256 bits of CSPRNG output, so it is not guessable and a slow
-- password KDF would only add latency to every API call.
CREATE TABLE IF NOT EXISTS api_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    token_hash   TEXT    NOT NULL UNIQUE,        -- hex SHA-256 of the plaintext
    prefix       TEXT    NOT NULL DEFAULT '',    -- leading chars, so the UI can name a token
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0
);

-- Single-row table holding the JSON-encoded runtime settings blob. Keeping it
-- as one document avoids a migration every time a setting is added, and the
-- settings are read as a unit anyway.
CREATE TABLE IF NOT EXISTS settings (
    id   INTEGER PRIMARY KEY CHECK (id = 1),
    json TEXT    NOT NULL
);

-- One ingested programme. Everything downstream -- destinations, renditions,
-- recordings -- belongs to exactly one of these.
--
-- This exists because a single install has to carry more than one programme at
-- once: the case that forced it is OBS's vertical-canvas plugin, which emits a
-- 16:9 and a 9:16 feed that are genuinely different compositions, not one
-- cropped from the other. Before this table an install could ingest one thing,
-- and the answer to "I stream both" was "run two containers".
--
-- ingest holds a db.IngestSettings JSON blob -- the same shape settings.ingest
-- carried when there was exactly one source. Keeping the shape identical is
-- what lets the migration move an existing install across without rewriting
-- anything, and what lets one validator serve both.
--
-- token is the per-source publish secret, and it is stored in plaintext,
-- unlike api_tokens which are hashed. The difference is deliberate and worth
-- stating: an API token is shown once and typed into a script, but an ingest
-- token is pasted into OBS as part of a stream key or an SRT streamid, and the
-- operator will come back to read it again. A hash cannot be displayed, and a
-- secret nobody can look up is one that gets replaced by an empty string.
-- stream_key on destinations is stored the same way for the same reason.
CREATE TABLE IF NOT EXISTS sources (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    ingest     TEXT    NOT NULL,               -- db.IngestSettings as JSON
    token      TEXT    NOT NULL DEFAULT '',    -- per-source publish secret
    -- The token this one replaced, still accepted until prev_token_until.
    -- Rotation that instantly kills a live stream is rotation nobody performs,
    -- and a credential nobody rotates is the problem the feature was meant to
    -- solve. The grace window lets the new token take effect while the encoder
    -- already connected on the old one keeps running.
    prev_token       TEXT    NOT NULL DEFAULT '',
    prev_token_until INTEGER NOT NULL DEFAULT 0,   -- unix seconds; 0 = none
    position   INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

-- One shared video encode. Destinations SELECT a rendition rather than owning
-- one, so five destinations that all need 1080p60 cost one encode, not five.
-- A rendition re-encodes video only and copies every audio track through
-- untouched; per-destination audio routing still happens at the destination.
CREATE TABLE IF NOT EXISTS renditions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL,
    width         INTEGER NOT NULL DEFAULT 0,    -- 0 = keep source
    height        INTEGER NOT NULL DEFAULT 0,    -- 0 = keep source
    fps           INTEGER NOT NULL DEFAULT 0,    -- 0 = keep source
    video_bitrate INTEGER NOT NULL DEFAULT 0,    -- kbps
    encoder       TEXT    NOT NULL DEFAULT 'libx264',
    preset        TEXT    NOT NULL DEFAULT 'veryfast',  -- encoder-specific quality knob
    gop_seconds   REAL    NOT NULL DEFAULT 2,
    note          TEXT    NOT NULL DEFAULT '',   -- what this tier is for
    -- Deinterlace mode: '' (off), 'auto' (only frames flagged interlaced) or
    -- 'all'. Off by default because progressive sources are the overwhelming
    -- majority and deinterlacing one only softens it.
    deinterlace   TEXT    NOT NULL DEFAULT '',
    -- A rendition re-encodes exactly one source, so it belongs to one.
    -- Nullable for the same ALTER TABLE reason as destinations.source_id.
    source_id     INTEGER,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE CASCADE
);

-- Note: the transport tuning columns (tr_*) and the expert-args columns are
-- added by MigrateDestinationExpertArgs (destinations.go) rather than here, so
-- fresh and upgraded databases get them from exactly one place and cannot
-- disagree about the default.
CREATE TABLE IF NOT EXISTS destinations (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL,
    kind          TEXT    NOT NULL,              -- rtmp | srt | file
    platform      TEXT    NOT NULL DEFAULT '',   -- '' | custom | youtube | twitch | kick
    account_id    INTEGER,                       -- platform_accounts.id
    url           TEXT    NOT NULL DEFAULT '',
    stream_key    TEXT    NOT NULL DEFAULT '',
    enabled       INTEGER NOT NULL DEFAULT 0,    -- user intent: should it be running
    audio_bitrate INTEGER NOT NULL DEFAULT 160,  -- kbps
    profile       TEXT    NOT NULL,              -- routing.Profile as JSON
    rendition_id  INTEGER,                       -- NULL = passthrough (no encode)
    -- Which programme this destination belongs to. Nullable rather than
    -- NOT NULL because SQLite refuses to ALTER TABLE ADD COLUMN a REFERENCES
    -- column with a non-NULL default while foreign keys are on, and the
    -- migrated shape and the fresh shape must not diverge. The store fills it
    -- in on create; NULL here means the source was deleted, not "unassigned".
    source_id     INTEGER,
    position      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    FOREIGN KEY (account_id) REFERENCES platform_accounts(id) ON DELETE SET NULL,
    -- CASCADE, unlike rendition_id's SET NULL: a destination describes where
    -- one programme goes, so it has no meaning once that programme is gone.
    FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE CASCADE,
    -- SET NULL, not CASCADE: deleting a rendition must drop its destinations
    -- back to passthrough, never delete the endpoints the user configured.
    FOREIGN KEY (rendition_id) REFERENCES renditions(id) ON DELETE SET NULL
);

-- The MQTT broker password, sealed. Its own table rather than a field in the
-- settings blob because that blob is served to the settings page: a password
-- in it would be handed to every browser that opened Settings.
CREATE TABLE IF NOT EXISTS mqtt_creds (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    password_enc BLOB NOT NULL,          -- secretbox sealed
    updated_at   INTEGER NOT NULL
);

-- The operator's own OAuth developer app. polyemesis cannot ship these.
CREATE TABLE IF NOT EXISTS platform_creds (
    platform          TEXT PRIMARY KEY,      -- youtube | twitch | kick
    client_id         TEXT NOT NULL,
    client_secret_enc BLOB NOT NULL,         -- secretbox sealed
    updated_at        INTEGER NOT NULL
);

-- One row per connected channel. Multiple accounts per platform is the point:
-- two YouTube channels are two rows, two destinations.
CREATE TABLE IF NOT EXISTS platform_accounts (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    platform          TEXT    NOT NULL,
    account_name      TEXT    NOT NULL DEFAULT '',
    account_ref       TEXT    NOT NULL DEFAULT '',  -- channel/broadcaster id
    access_token_enc  BLOB    NOT NULL,
    refresh_token_enc BLOB,
    expires_at        INTEGER NOT NULL DEFAULT 0,
    scopes            TEXT    NOT NULL DEFAULT '',
    -- The provider's ScopeVersion when this account was connected. Compared
    -- against the provider's current version to spot a token issued before a
    -- scope was added; 0 means the row predates the column.
    scope_ver         INTEGER NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    UNIQUE (platform, account_ref)
);

-- Short-lived CSRF state for the OAuth authorization-code flow.
CREATE TABLE IF NOT EXISTS oauth_states (
    state      TEXT PRIMARY KEY,
    platform   TEXT    NOT NULL,
    verifier   TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS recordings (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    filename    TEXT    NOT NULL UNIQUE,
    started_at  INTEGER NOT NULL,
    finished_at INTEGER NOT NULL DEFAULT 0,
    bytes       INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    tracks      INTEGER NOT NULL DEFAULT 0,
    -- SET NULL rather than CASCADE, unlike destinations: the recording is a
    -- file on disk that still exists and is still playable after its source is
    -- deleted. Dropping the row would orphan the file and lose the transcript
    -- and clips hanging off it. An unattributed recording is a small loss; a
    -- silently deleted library is not.
    source_id   INTEGER,
    FOREIGN KEY (source_id) REFERENCES sources(id) ON DELETE SET NULL
);

-- One webhook endpoint and what it wants to hear about. The URL is stored as
-- given because a Slack or Discord webhook URL IS the credential and there is
-- nothing to post to without it; it is never returned by the API or written to
-- a payload. See alerts.Rule.RedactedURL.
--
-- debounce_seconds is the window inside which repeats of the same subject
-- become one message, and min_interval_seconds is the floor between two
-- deliveries to this endpoint. Both have non-zero defaults on purpose: a rule
-- with neither is how a flapping destination sends two hundred messages.
CREATE TABLE IF NOT EXISTS alert_rules (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    name                 TEXT    NOT NULL,
    enabled              INTEGER NOT NULL DEFAULT 1,
    url                  TEXT    NOT NULL,
    format               TEXT    NOT NULL DEFAULT 'json',   -- json | discord | slack
    events               TEXT    NOT NULL DEFAULT '[]',     -- JSON array; empty = every event
    min_severity         TEXT    NOT NULL DEFAULT 'info',
    debounce_seconds     INTEGER NOT NULL DEFAULT 10,
    min_interval_seconds INTEGER NOT NULL DEFAULT 30,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL
);

-- When destinations should be live. Instants are UTC; a recurring schedule
-- stores a wall-clock minute plus the IANA zone to read it in, so a show at
-- 19:00 local stays at 19:00 across a daylight-saving boundary.
--
-- last_run_at is the newest occurrence already handled, whether it fired or was
-- skipped for being missed. It is what stops a server that was off all morning
-- from replaying the morning when it comes back.
CREATE TABLE IF NOT EXISTS schedules (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT    NOT NULL,
    enabled         INTEGER NOT NULL DEFAULT 1,
    action          TEXT    NOT NULL DEFAULT 'start',  -- start | stop
    kind            TEXT    NOT NULL DEFAULT 'once',   -- once | daily | weekly
    destination_ids TEXT    NOT NULL DEFAULT '[]',     -- JSON array; empty = every destination
    tz              TEXT    NOT NULL DEFAULT '',       -- IANA zone; empty = UTC
    at_minutes      INTEGER NOT NULL DEFAULT 0,        -- minutes past local midnight
    days            TEXT    NOT NULL DEFAULT '[]',     -- JSON array of weekday numbers, Sunday = 0
    run_at          INTEGER NOT NULL DEFAULT 0,        -- unix seconds, one-shot only
    grace_seconds   INTEGER NOT NULL DEFAULT 300,
    last_run_at     INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

-- The durable background job queue. Every heavy task — transcription, proxy
-- generation, lossless cutting — is a row here rather than work done inline,
-- because a dropped frame on a live broadcast is unrecoverable and a transcript
-- arriving an hour later costs nothing.
--
-- It is persisted for one reason above all others: a four-hour transcription
-- that vanishes because the server bounced is worse than useless. A row left in
-- 'running' by a process that died is requeued at startup, and attempts is what
-- stops a job that crashes the server from crash-looping it forever.
--
-- available_at carries both retry backoff and resource-policy deferral, so
-- there is one mechanism holding work back rather than two, and a deferred job
-- becomes claimable again on its own if whatever deferred it dies.
CREATE TABLE IF NOT EXISTS jobs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    kind          TEXT    NOT NULL,                  -- processors define these; the queue never reads one
    target        TEXT    NOT NULL DEFAULT '',       -- usually 'recording:<id>'
    params        TEXT    NOT NULL DEFAULT '{}',     -- opaque processor JSON
    result        TEXT    NOT NULL DEFAULT '',       -- opaque worker output, JSON
    priority      INTEGER NOT NULL DEFAULT 0,        -- higher first; FIFO within a priority
    state         TEXT    NOT NULL DEFAULT 'queued', -- queued|running|done|failed|cancelled|deferred
    unique_target INTEGER NOT NULL DEFAULT 0,        -- fold a resubmission into the active job
    attempts      INTEGER NOT NULL DEFAULT 0,        -- starts, not failures
    max_attempts  INTEGER NOT NULL DEFAULT 3,
    progress      REAL    NOT NULL DEFAULT 0,        -- 0..1, best effort
    log_tail      TEXT    NOT NULL DEFAULT '[]',     -- JSON array of the newest lines
    last_error    TEXT    NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    available_at  INTEGER NOT NULL DEFAULT 0,        -- earliest claim time
    started_at    INTEGER NOT NULL DEFAULT 0,
    finished_at   INTEGER NOT NULL DEFAULT 0,
    updated_at    INTEGER NOT NULL
);

-- A broadcast is not one file. With hour-long segments a four-hour show is
-- four recordings rows, and the library should show one entry, not four. A
-- session is that grouping: consecutive recordings whose start times chain,
-- plus the metadata a human wants to attach to the whole thing.
--
-- The span columns are derived from the members and are stored anyway, because
-- the library list would otherwise aggregate over every recording on every
-- page load. RecalcSession is the single writer; nothing else may set them.
CREATE TABLE IF NOT EXISTS sessions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    title       TEXT    NOT NULL DEFAULT '',
    description TEXT    NOT NULL DEFAULT '',
    tags        TEXT    NOT NULL DEFAULT '[]',   -- JSON array of strings
    started_at  INTEGER NOT NULL DEFAULT 0,      -- derived: earliest member start
    ended_at    INTEGER NOT NULL DEFAULT 0,      -- derived: latest member end
    duration_ms INTEGER NOT NULL DEFAULT 0,      -- derived: sum of member durations
    bytes       INTEGER NOT NULL DEFAULT 0,      -- derived
    recordings  INTEGER NOT NULL DEFAULT 0,      -- derived: member count
    -- auto distinguishes a session the grouper inferred from one the operator
    -- built by hand. The backfill may extend the former and must never rewrite
    -- the latter: a hand-curated grouping is a decision, not a guess.
    auto        INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- Membership. recording_id is the PRIMARY KEY, not part of a composite one:
-- a recording belongs to at most one session, and making the schema say so is
-- cheaper than every caller remembering it.
CREATE TABLE IF NOT EXISTS session_recordings (
    recording_id INTEGER PRIMARY KEY,
    session_id   INTEGER NOT NULL,
    position     INTEGER NOT NULL DEFAULT 0,     -- order within the session
    FOREIGN KEY (session_id)   REFERENCES sessions(id)   ON DELETE CASCADE,
    FOREIGN KEY (recording_id) REFERENCES recordings(id) ON DELETE CASCADE
);

-- Editable metadata for a single recording. A sidecar table rather than three
-- columns on recordings because this file runs against databases created
-- before the library existed, where CREATE TABLE IF NOT EXISTS is a no-op and
-- added columns would silently not appear — the same trap documented at the
-- foot of this file. A row here is optional; its absence means "no metadata",
-- which is not an error.
CREATE TABLE IF NOT EXISTS recording_meta (
    recording_id INTEGER PRIMARY KEY,
    title        TEXT    NOT NULL DEFAULT '',
    description  TEXT    NOT NULL DEFAULT '',
    tags         TEXT    NOT NULL DEFAULT '[]',  -- JSON array of strings
    updated_at   INTEGER NOT NULL,
    FOREIGN KEY (recording_id) REFERENCES recordings(id) ON DELETE CASCADE
);

-- One row per (recording, audio track) that has been transcribed. The track is
-- the unit because each microphone is recorded on its own track and each is
-- transcribed in isolation: re-running track 2 with a bigger model must
-- replace track 2 and leave track 1 alone.
CREATE TABLE IF NOT EXISTS transcript_tracks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    recording_id INTEGER NOT NULL,
    track        INTEGER NOT NULL,               -- 0-based audio track index
    speaker      TEXT    NOT NULL DEFAULT '',    -- who that track is
    role         TEXT    NOT NULL DEFAULT '',    -- routing role, plain text so it cannot go stale
    language     TEXT    NOT NULL DEFAULT '',
    model        TEXT    NOT NULL DEFAULT '',
    backend      TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    UNIQUE (recording_id, track),
    FOREIGN KEY (recording_id) REFERENCES recordings(id) ON DELETE CASCADE
);

-- One utterance. recording_id, track and speaker are denormalised from the
-- parent row on purpose: every search filters on them, and a hit that had to
-- join to learn who was speaking would join once per result. They are written
-- and rewritten only inside the same transaction as the parent, so they cannot
-- drift.
CREATE TABLE IF NOT EXISTS transcript_segments (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    track_id         INTEGER NOT NULL,
    recording_id     INTEGER NOT NULL,
    track            INTEGER NOT NULL,
    speaker          TEXT    NOT NULL DEFAULT '',
    start_ms         INTEGER NOT NULL DEFAULT 0, -- offset into the recording
    end_ms           INTEGER NOT NULL DEFAULT 0,
    text             TEXT    NOT NULL DEFAULT '',
    confidence       REAL    NOT NULL DEFAULT 0,
    -- Separates "the model was unsure" from "nobody asked". Without it a
    -- missing confidence reads as 0.0, the strongest possible claim of garbage
    -- about a segment that may be perfect.
    confidence_known INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (track_id)     REFERENCES transcript_tracks(id) ON DELETE CASCADE,
    FOREIGN KEY (recording_id) REFERENCES recordings(id)        ON DELETE CASCADE
);

-- The search index. External-content FTS5: the text lives once, in
-- transcript_segments, and this table holds only the inverted index keyed on
-- that row's id.
--
-- The three triggers below are not a convenience. Deleting a recording deletes
-- its segments through a foreign key cascade that never passes through Go, and
-- an index that only Go maintained would keep returning hits for a recording
-- that no longer exists. SQLite fires DELETE triggers for cascaded deletes,
-- so the trigger is the only place that is guaranteed to run.
--
-- remove_diacritics 2 is the Unicode-correct setting; without it "café" and
-- "cafe" are different words, which is never what a person searching a
-- transcript means.
CREATE VIRTUAL TABLE IF NOT EXISTS transcript_fts USING fts5(
    text,
    content='transcript_segments',
    content_rowid='id',
    tokenize="unicode61 remove_diacritics 2"
);

CREATE TRIGGER IF NOT EXISTS transcript_segments_ai AFTER INSERT ON transcript_segments BEGIN
    INSERT INTO transcript_fts(rowid, text) VALUES (new.id, new.text);
END;

CREATE TRIGGER IF NOT EXISTS transcript_segments_ad AFTER DELETE ON transcript_segments BEGIN
    INSERT INTO transcript_fts(transcript_fts, rowid, text) VALUES ('delete', old.id, old.text);
END;

CREATE TRIGGER IF NOT EXISTS transcript_segments_au AFTER UPDATE ON transcript_segments BEGIN
    INSERT INTO transcript_fts(transcript_fts, rowid, text) VALUES ('delete', old.id, old.text);
    INSERT INTO transcript_fts(rowid, text) VALUES (new.id, new.text);
END;

-- Unified cross-platform chat. One row per message from any platform, kept
-- only so a browser that connects mid-broadcast sees the last few minutes
-- instead of an empty pane — this is a replay buffer with a schema, not an
-- archive. PurgeChatMessages bounds it and is expected to run often.
--
-- message_id is the platform's own id and is NOT unique on its own: two
-- platforms will collide on "1" sooner or later. The uniqueness that matters
-- is (platform, account, message_id), which is also exactly what makes an
-- adapter that redelivers an event after a reconnect idempotent.
--
-- badges and emotes are JSON because they are per-platform shapes that only
-- the renderer reads; giving them columns would mean a migration every time a
-- platform adds a flag.
CREATE TABLE IF NOT EXISTS chat_messages (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    platform     TEXT    NOT NULL,                 -- youtube | twitch | kick | facebook
    account      TEXT    NOT NULL DEFAULT '',      -- platform_accounts.account_ref
    message_id   TEXT    NOT NULL DEFAULT '',
    channel      TEXT    NOT NULL DEFAULT '',
    author_id    TEXT    NOT NULL DEFAULT '',
    author_name  TEXT    NOT NULL DEFAULT '',
    author_color TEXT    NOT NULL DEFAULT '',      -- '#rrggbb', empty when the platform sends none
    moderator    INTEGER NOT NULL DEFAULT 0,
    subscriber   INTEGER NOT NULL DEFAULT 0,
    broadcaster  INTEGER NOT NULL DEFAULT 0,
    text         TEXT    NOT NULL DEFAULT '',
    badges       TEXT    NOT NULL DEFAULT '[]',
    emotes       TEXT    NOT NULL DEFAULT '[]',
    reply_to_id  TEXT    NOT NULL DEFAULT '',
    reply_to     TEXT    NOT NULL DEFAULT '',      -- display name, so a reply renders without a second lookup
    echo         INTEGER NOT NULL DEFAULT 0,       -- polyemesis sent this one
    -- Milliseconds since epoch: chat arrives in bursts and a second-resolution
    -- sort scrambles the order of a fast exchange.
    at_ms        INTEGER NOT NULL,
    UNIQUE (platform, account, message_id)
);

CREATE INDEX IF NOT EXISTS idx_recordings_started ON recordings(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_alert_rules_enabled ON alert_rules(enabled, id);
CREATE INDEX IF NOT EXISTS idx_schedules_enabled ON schedules(enabled, id);
CREATE INDEX IF NOT EXISTS idx_destinations_position ON destinations(position, id);

-- The claim index has to answer "the oldest highest-priority eligible job of
-- these kinds" on every dispatch, which is the one query in this schema that
-- runs in a loop.
CREATE INDEX IF NOT EXISTS idx_jobs_claim ON jobs(state, available_at, priority DESC, created_at, id);
CREATE INDEX IF NOT EXISTS idx_jobs_target ON jobs(kind, target, state);
CREATE INDEX IF NOT EXISTS idx_jobs_recent ON jobs(created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_sessions_span ON sessions(started_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_session_recordings_session ON session_recordings(session_id, position, recording_id);
-- Playback and the context window around a search hit both walk one track of
-- one recording in time order; this is the index that makes both a seek.
CREATE INDEX IF NOT EXISTS idx_transcript_segments_time ON transcript_segments(recording_id, track, start_ms, id);
CREATE INDEX IF NOT EXISTS idx_transcript_segments_track ON transcript_segments(track_id, start_ms, id);
CREATE INDEX IF NOT EXISTS idx_transcript_tracks_recording ON transcript_tracks(recording_id, track);

-- Both chat reads are "the newest N", either across everything or for one
-- platform, and the purge walks the same order backwards.
CREATE INDEX IF NOT EXISTS idx_chat_messages_recent ON chat_messages(at_ms DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_chat_messages_platform ON chat_messages(platform, at_ms DESC, id DESC);
-- "Everything this person has said", which is what a moderator reads before
-- deciding whether one bad message was a bad moment or a pattern. Without this
-- the query is a full scan of the table on every card open, and the card opens
-- from a hover.
--
-- author_id and not author_name: a display name is not an identity, and on every
-- platform here a name can change while the id cannot.
CREATE INDEX IF NOT EXISTS idx_chat_messages_author ON chat_messages(platform, author_id, at_ms DESC, id DESC);

-- NOTE: nothing here may reference destinations.rendition_id. This file runs
-- against databases created before renditions existed, where CREATE TABLE IF
-- NOT EXISTS is a no-op and the column is therefore still missing. Everything
-- that depends on that column lives in MigrateRenditions (renditions.go),
-- which runs after this script and adds the column when it is absent.

-- Same note for destinations.extra_input_args, extra_output_args and
-- expert_ack_reencode: they are added by MigrateDestinationExpertArgs
-- (destinations.go) rather than here, so that fresh and upgraded databases
-- get them from exactly one place and cannot disagree about the default.

-- And again for renditions.aspect_mode and pad_color: MigrateRenditionAspect
-- (renditions.go) owns them, for the same reason.
