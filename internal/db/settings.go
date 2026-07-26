package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// IngestMode selects which listener the ingest supervisor runs.
type IngestMode string

const (
	// IngestSRT is the primary path: MPEG-TS over SRT, up to six AAC tracks.
	IngestSRT IngestMode = "srt"
	// IngestRTMP is the fallback for encoders that cannot do SRT. Single
	// audio track, by protocol.
	IngestRTMP IngestMode = "rtmp"
)

// SRTSettings configures the SRT listener.
type SRTSettings struct {
	Port int `json:"port"`
	// Passphrase enables AES encryption. SRT requires 10..79 characters.
	Passphrase string `json:"passphrase"`
	// LatencyMS is SRT's receive buffer, in milliseconds. Higher survives
	// worse networks at the cost of glass-to-glass delay.
	LatencyMS int `json:"latencyMs"`
}

// RTMPSettings configures the fallback RTMP listener.
type RTMPSettings struct {
	Port int    `json:"port"`
	App  string `json:"app"`
	// StreamKey is matched against the publisher's playpath, so a stranger
	// who finds the port still cannot publish.
	StreamKey string `json:"streamKey"`
}

// IngestSettings is the whole ingest configuration.
type IngestSettings struct {
	Mode IngestMode   `json:"mode"`
	SRT  SRTSettings  `json:"srt"`
	RTMP RTMPSettings `json:"rtmp"`
}

// RecordingSettings controls the recorder and the retention sweeper.
type RecordingSettings struct {
	Enabled bool `json:"enabled"`
	// SegmentSeconds is the length of each MKV segment. Segmenting means a
	// crash costs you one segment, not the whole session.
	SegmentSeconds int `json:"segmentSeconds"`
	// MaxGB is the total size cap for the recordings directory. 0 = no cap.
	MaxGB float64 `json:"maxGb"`
	// MaxAgeHours deletes segments older than this. 0 = never.
	MaxAgeHours int `json:"maxAgeHours"`
}

// PreviewSettings controls the low-latency HLS preview shown on the dashboard.
type PreviewSettings struct {
	Enabled bool `json:"enabled"`
	// SegmentSeconds trades preview latency against playback stability.
	SegmentSeconds int `json:"segmentSeconds"`
	// VideoHeight is the preview's scaled height. The preview is the only
	// place polyemesis re-encodes video, and it never touches a destination.
	VideoHeight int `json:"videoHeight"`
	VideoKbps   int `json:"videoKbps"`
}

// MeterSettings controls the audio-level sidecar.
type MeterSettings struct {
	Enabled bool `json:"enabled"`
	// IntervalMS is how often levels are pushed over the WebSocket.
	IntervalMS int `json:"intervalMs"`
}

// Settings is everything the user can change from the web UI.
type Settings struct {
	Ingest    IngestSettings    `json:"ingest"`
	Recording RecordingSettings `json:"recording"`
	Preview   PreviewSettings   `json:"preview"`
	Meters    MeterSettings     `json:"meters"`
}

// DefaultSettings is what a fresh install runs with.
func DefaultSettings() Settings {
	return Settings{
		Ingest: IngestSettings{
			Mode: IngestSRT,
			SRT:  SRTSettings{Port: 6000, LatencyMS: 200},
			RTMP: RTMPSettings{Port: 1935, App: "live", StreamKey: "stream"},
		},
		Recording: RecordingSettings{
			Enabled:        false,
			SegmentSeconds: 3600,
			MaxGB:          50,
			MaxAgeHours:    24 * 30,
		},
		Preview: PreviewSettings{
			Enabled:        true,
			SegmentSeconds: 2,
			VideoHeight:    360,
			VideoKbps:      800,
		},
		Meters: MeterSettings{Enabled: true, IntervalMS: 100},
	}
}

// Validate rejects settings that would produce a process that cannot start.
func (s Settings) Validate() error {
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	switch s.Ingest.Mode {
	case IngestSRT, IngestRTMP:
	default:
		add("unknown ingest mode %q", s.Ingest.Mode)
	}
	if s.Ingest.SRT.Port < 1 || s.Ingest.SRT.Port > 65535 {
		add("srt port %d out of range", s.Ingest.SRT.Port)
	}
	// SRT's own constraint, enforced here so the user sees it in a form field
	// rather than in an FFmpeg stderr line.
	if p := s.Ingest.SRT.Passphrase; p != "" && (len(p) < 10 || len(p) > 79) {
		add("srt passphrase must be 10-79 characters (got %d)", len(p))
	}
	if s.Ingest.SRT.LatencyMS < 20 || s.Ingest.SRT.LatencyMS > 8000 {
		add("srt latency %dms out of range (20-8000)", s.Ingest.SRT.LatencyMS)
	}
	if s.Ingest.RTMP.Port < 1 || s.Ingest.RTMP.Port > 65535 {
		add("rtmp port %d out of range", s.Ingest.RTMP.Port)
	}
	if s.Ingest.Mode == IngestRTMP && s.Ingest.RTMP.App == "" {
		add("rtmp app name is required")
	}
	if s.Ingest.SRT.Port == s.Ingest.RTMP.Port {
		add("srt and rtmp cannot share port %d", s.Ingest.SRT.Port)
	}
	if s.Recording.SegmentSeconds < 10 || s.Recording.SegmentSeconds > 24*3600 {
		add("recording segment length %ds out of range (10-86400)", s.Recording.SegmentSeconds)
	}
	if s.Recording.MaxGB < 0 {
		add("recording size cap cannot be negative")
	}
	if s.Recording.MaxAgeHours < 0 {
		add("recording age cap cannot be negative")
	}
	if s.Preview.SegmentSeconds < 1 || s.Preview.SegmentSeconds > 10 {
		add("preview segment length %ds out of range (1-10)", s.Preview.SegmentSeconds)
	}
	if s.Preview.VideoHeight < 144 || s.Preview.VideoHeight > 1080 {
		add("preview height %d out of range (144-1080)", s.Preview.VideoHeight)
	}
	if s.Meters.IntervalMS < 40 || s.Meters.IntervalMS > 2000 {
		add("meter interval %dms out of range (40-2000)", s.Meters.IntervalMS)
	}

	if len(probs) > 0 {
		return fmt.Errorf("invalid settings: %v", probs)
	}
	return nil
}

// GetSettings returns the stored settings, seeding defaults on first run.
func (d *DB) GetSettings() (Settings, error) {
	var raw string
	err := d.sql.QueryRow(`SELECT json FROM settings WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		s := DefaultSettings()
		return s, d.PutSettings(s)
	}
	if err != nil {
		return Settings{}, err
	}
	// Start from defaults so a settings blob written by an older build gains
	// sane values for fields it has never heard of.
	s := DefaultSettings()
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Settings{}, fmt.Errorf("decode settings: %w", err)
	}
	return s, nil
}

// PutSettings stores the settings blob.
func (d *DB) PutSettings(s Settings) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(
		`INSERT INTO settings (id, json) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET json = excluded.json`,
		string(b))
	return err
}
