-- polyemesis schema. Applied idempotently at startup.

CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
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
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

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
    position      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    FOREIGN KEY (account_id) REFERENCES platform_accounts(id) ON DELETE SET NULL,
    -- SET NULL, not CASCADE: deleting a rendition must drop its destinations
    -- back to passthrough, never delete the endpoints the user configured.
    FOREIGN KEY (rendition_id) REFERENCES renditions(id) ON DELETE SET NULL
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
    tracks      INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_recordings_started ON recordings(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_destinations_position ON destinations(position, id);

-- NOTE: nothing here may reference destinations.rendition_id. This file runs
-- against databases created before renditions existed, where CREATE TABLE IF
-- NOT EXISTS is a no-op and the column is therefore still missing. Everything
-- that depends on that column lives in MigrateRenditions (renditions.go),
-- which runs after this script and adds the column when it is absent.

-- Same note for destinations.extra_input_args, extra_output_args and
-- expert_ack_reencode: they are added by MigrateDestinationExpertArgs
-- (destinations.go) rather than here, so that fresh and upgraded databases
-- get them from exactly one place and cannot disagree about the default.
