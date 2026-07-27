package db

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Transcript storage and full-text search.
//
// The shape here mirrors internal/transcribe's Transcript / TrackTranscript /
// Segment field for field, deliberately: the transcriber owns the model and
// this file owns the SQL, and a storage layer that reshaped the data would be
// a second place for the two to disagree. It does not import transcribe —
// storage must not depend on the thing that happens to fill it — so the API
// layer does a mechanical field copy in each direction.
//
// The differentiator this exists to serve: each microphone is recorded on its
// own audio track, so a segment already knows which track it came from and
// therefore who said it. Every query below can be scoped to a track or a
// speaker for free, with no diarization model anywhere in the pipeline.

// TranscriptSegment is one stored utterance.
type TranscriptSegment struct {
	ID          int64 `json:"id"`
	RecordingID int64 `json:"recordingId"`
	// Track is the 0-based audio track index: the N in FFmpeg's `-map 0:a:N`.
	Track int `json:"track"`
	// Speaker is the human name for that track. Denormalised onto the segment
	// because the interesting view — the merged, time-ordered conversation —
	// flattens the tracks away, and a segment that has lost its track identity
	// has lost the speaker attribution with it.
	Speaker string `json:"speaker,omitempty"`

	StartMS int64  `json:"startMs"`
	EndMS   int64  `json:"endMs"`
	Text    string `json:"text"`

	Confidence float64 `json:"confidence,omitempty"`
	// ConfidenceKnown separates "the model was unsure" from "nobody asked".
	ConfidenceKnown bool `json:"confidenceKnown,omitempty"`
}

// TranscriptTrack is everything one audio track said, plus how it was made.
type TranscriptTrack struct {
	ID          int64     `json:"id"`
	RecordingID int64     `json:"recordingId"`
	Track       int       `json:"track"`
	Speaker     string    `json:"speaker,omitempty"`
	Role        string    `json:"role,omitempty"`
	Language    string    `json:"language,omitempty"`
	Model       string    `json:"model,omitempty"`
	Backend     string    `json:"backend,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`

	// Segments is nil on a listing and populated by GetTranscript. Count and
	// DurationMS are always filled so the recordings page can say "3 tracks,
	// 412 segments" without loading a word of text.
	Segments   []TranscriptSegment `json:"segments,omitempty"`
	Count      int                 `json:"count"`
	DurationMS int64               `json:"durationMs"`
}

// Transcript is every transcribed track of one recording.
type Transcript struct {
	RecordingID int64             `json:"recordingId"`
	Recording   string            `json:"recording,omitempty"` // filename, for display
	Tracks      []TranscriptTrack `json:"tracks"`
}

// Merged is every track's segments in one time-ordered slice: the free
// diarization view. Ties break on track index so the ordering is stable.
func (t Transcript) Merged() []TranscriptSegment {
	var out []TranscriptSegment
	for _, tr := range t.Tracks {
		out = append(out, tr.Segments...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartMS != out[j].StartMS {
			return out[i].StartMS < out[j].StartMS
		}
		return out[i].Track < out[j].Track
	})
	return out
}

// Speakers lists the distinct speakers, in track order.
func (t Transcript) Speakers() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, tr := range t.Tracks {
		if tr.Speaker == "" || seen[tr.Speaker] {
			continue
		}
		seen[tr.Speaker] = true
		out = append(out, tr.Speaker)
	}
	return out
}

// SegmentCount is the total across every track.
func (t Transcript) SegmentCount() int {
	n := 0
	for _, tr := range t.Tracks {
		n += tr.Count
	}
	return n
}

// Highlight markers wrapping the matched terms in a search snippet.
//
// Unicode private-use code points, not brackets or <mark> tags, because a
// transcript is arbitrary human speech: any printable delimiter can occur in
// the text itself, and the consumer would have no way to tell a real bracket
// from a highlight. Nothing can type these, so splitting on them is exact and
// no escaping is needed on the way to the UI.
const (
	HighlightOpen  = "\ue000"
	HighlightClose = "\ue001"
)

// ErrEmptyQuery is returned when a search string contains nothing searchable —
// only punctuation, or only bare operators.
var ErrEmptyQuery = errors.New("empty search query")

const transcriptTrackColumns = `id, recording_id, track, speaker, role, language, model, backend, created_at`

// SaveTranscript replaces the stored transcript for the tracks it carries.
//
// Per track, not per recording: re-running track 2 with a bigger model must
// replace track 2 and leave track 1 — possibly transcribed with different
// settings, possibly hours of work — exactly where it was.
func (d *DB) SaveTranscript(t *Transcript) error {
	if t == nil || t.RecordingID == 0 {
		return fmt.Errorf("save transcript: recording id required")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	for _, tr := range t.Tracks {
		// Dropping the old row cascades its segments away, and the delete
		// trigger takes their FTS entries with them.
		if _, err := tx.Exec(`DELETE FROM transcript_tracks WHERE recording_id = ? AND track = ?`,
			t.RecordingID, tr.Track); err != nil {
			return err
		}
		created := now
		if !tr.CreatedAt.IsZero() {
			created = tr.CreatedAt.Unix()
		}
		res, err := tx.Exec(`INSERT INTO transcript_tracks
			(recording_id, track, speaker, role, language, model, backend, created_at)
			VALUES (?,?,?,?,?,?,?,?)`,
			t.RecordingID, tr.Track, tr.Speaker, tr.Role, tr.Language, tr.Model, tr.Backend, created)
		if err != nil {
			return err
		}
		trackID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for _, s := range tr.Segments {
			// A segment with no text would take up a row and an FTS entry and
			// could never be a search hit; whisper emits them for silence.
			if strings.TrimSpace(s.Text) == "" {
				continue
			}
			if _, err := tx.Exec(`INSERT INTO transcript_segments
				(track_id, recording_id, track, speaker, start_ms, end_ms, text, confidence, confidence_known)
				VALUES (?,?,?,?,?,?,?,?,?)`,
				trackID, t.RecordingID, tr.Track, tr.Speaker, s.StartMS, s.EndMS,
				s.Text, s.Confidence, boolToInt(s.ConfidenceKnown)); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// GetTranscript loads every track of a recording, segments included.
func (d *DB) GetTranscript(recordingID int64) (*Transcript, error) {
	tracks, err := d.ListTranscriptTracks(recordingID)
	if err != nil {
		return nil, err
	}
	out := &Transcript{RecordingID: recordingID, Tracks: tracks}
	// Filename is best effort: a transcript outlives nothing, but a recording
	// row that has been swept while the transcript is being read is not a
	// reason to fail the read.
	var name string
	if err := d.sql.QueryRow(`SELECT filename FROM recordings WHERE id = ?`, recordingID).Scan(&name); err == nil {
		out.Recording = name
	}
	for i := range out.Tracks {
		segs, err := d.ListTranscriptSegments(recordingID, &out.Tracks[i].Track)
		if err != nil {
			return nil, err
		}
		out.Tracks[i].Segments = segs
	}
	return out, nil
}

// ListTranscriptTracks returns the per-track headers with their counts, and no
// segment text at all. This is what the recordings page asks for.
func (d *DB) ListTranscriptTracks(recordingID int64) ([]TranscriptTrack, error) {
	rows, err := d.sql.Query(`SELECT `+transcriptTrackColumns+`,
			(SELECT COUNT(*) FROM transcript_segments s WHERE s.track_id = t.id),
			(SELECT COALESCE(MAX(s.end_ms), 0) FROM transcript_segments s WHERE s.track_id = t.id)
		FROM transcript_tracks t WHERE recording_id = ? ORDER BY track`, recordingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []TranscriptTrack{}
	for rows.Next() {
		var (
			t       TranscriptTrack
			created int64
		)
		if err := rows.Scan(&t.ID, &t.RecordingID, &t.Track, &t.Speaker, &t.Role,
			&t.Language, &t.Model, &t.Backend, &created, &t.Count, &t.DurationMS); err != nil {
			return nil, err
		}
		t.CreatedAt = time.Unix(created, 0)
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListTranscriptSegments returns one recording's segments in time order,
// optionally narrowed to a single track. Pass nil for every track.
func (d *DB) ListTranscriptSegments(recordingID int64, track *int) ([]TranscriptSegment, error) {
	q := `SELECT id, recording_id, track, speaker, start_ms, end_ms, text, confidence, confidence_known
		FROM transcript_segments WHERE recording_id = ?`
	args := []any{recordingID}
	if track != nil {
		q += ` AND track = ?`
		args = append(args, *track)
	}
	q += ` ORDER BY start_ms, track, id`

	rows, err := d.sql.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTranscriptSegments(rows)
}

func scanTranscriptSegments(rows *sql.Rows) ([]TranscriptSegment, error) {
	out := []TranscriptSegment{}
	for rows.Next() {
		var (
			s     TranscriptSegment
			known int
		)
		if err := rows.Scan(&s.ID, &s.RecordingID, &s.Track, &s.Speaker,
			&s.StartMS, &s.EndMS, &s.Text, &s.Confidence, &known); err != nil {
			return nil, err
		}
		s.ConfidenceKnown = known != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// HasTranscript reports whether a recording has any transcribed track, without
// loading one.
func (d *DB) HasTranscript(recordingID int64) (bool, error) {
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM transcript_tracks WHERE recording_id = ?`, recordingID).Scan(&n)
	return n > 0, err
}

// TranscribedRecordings reports which of the given recordings have a
// transcript, so a listing can badge them in one query rather than N.
func (d *DB) TranscribedRecordings(ids []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	q := `SELECT DISTINCT recording_id FROM transcript_tracks WHERE recording_id IN (` +
		placeholders(len(ids)) + `)`
	rows, err := d.sql.Query(q, int64Args(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// DeleteTranscript drops every track of a recording. Deleting the recording
// itself does the same thing through the foreign key.
func (d *DB) DeleteTranscript(recordingID int64) error {
	_, err := d.sql.Exec(`DELETE FROM transcript_tracks WHERE recording_id = ?`, recordingID)
	return err
}

// DeleteTranscriptTrack drops one track's transcript.
func (d *DB) DeleteTranscriptTrack(recordingID int64, track int) error {
	res, err := d.sql.Exec(`DELETE FROM transcript_tracks WHERE recording_id = ? AND track = ?`, recordingID, track)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTranscriptSpeaker relabels a track after the fact — "track 2 is Dana,
// not Guest". Both the header and every segment are rewritten in one
// transaction because the segment copy is what search filters on.
func (d *DB) SetTranscriptSpeaker(recordingID int64, track int, speaker string) error {
	speaker = strings.TrimSpace(speaker)
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE transcript_tracks SET speaker = ? WHERE recording_id = ? AND track = ?`,
		speaker, recordingID, track)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`UPDATE transcript_segments SET speaker = ? WHERE recording_id = ? AND track = ?`,
		speaker, recordingID, track); err != nil {
		return err
	}
	return tx.Commit()
}

// TranscriptSpeakers lists every distinct speaker known to the index, so the
// search UI can offer a filter without the caller enumerating recordings.
func (d *DB) TranscriptSpeakers() ([]string, error) {
	rows, err := d.sql.Query(`SELECT DISTINCT speaker FROM transcript_tracks
		WHERE speaker <> '' ORDER BY speaker COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// TranscriptOrder is how search results are sorted.
type TranscriptOrder string

const (
	// OrderRelevance is BM25, best match first. The default: "find where I
	// said X" wants the best X, not the oldest one.
	OrderRelevance TranscriptOrder = "relevance"
	// OrderTime is oldest first, for reading a session through.
	OrderTime TranscriptOrder = "time"
	// OrderRecent is newest first, for "what did I say about this lately".
	OrderRecent TranscriptOrder = "recent"
)

// TranscriptQuery is a full-text search over stored transcripts.
type TranscriptQuery struct {
	// Text is what the human typed. It is not passed to FTS5 as written; see
	// MatchQuery for why.
	Text string `json:"text"`
	// Prefix makes the final term a prefix match, which is what makes
	// search-as-you-type useful.
	Prefix bool `json:"prefix,omitempty"`

	// Raw passes Text to FTS5 untouched, for an operator who knows the query
	// syntax. A syntax error becomes a returned error, never a panic.
	Raw bool `json:"raw,omitempty"`

	RecordingID int64  `json:"recordingId,omitempty"`
	SessionID   int64  `json:"sessionId,omitempty"`
	Track       *int   `json:"track,omitempty"`
	Speaker     string `json:"speaker,omitempty"`

	// Since and Until bound the recording's wall-clock start time.
	Since time.Time `json:"since,omitempty"`
	Until time.Time `json:"until,omitempty"`

	Order  TranscriptOrder `json:"order,omitempty"`
	Limit  int             `json:"limit,omitempty"`
	Offset int             `json:"offset,omitempty"`

	// Context is how many neighbouring segments from the same track to glue
	// on either side of the hit. A bare matched utterance is often three words
	// long and tells the reader nothing; this is what makes a result useful.
	// Zero means "use the default"; set it negative for none at all.
	Context int `json:"context,omitempty"`
	// SnippetTokens is the width of the FTS5 snippet around the match.
	SnippetTokens int `json:"snippetTokens,omitempty"`
}

// Search defaults. The context window is small on purpose: two segments either
// side is roughly a sentence of lead-in, and more turns a result list into a
// transcript dump.
const (
	DefaultSearchLimit    = 50
	MaxSearchLimit        = 500
	DefaultSearchContext  = 2
	MaxSearchContext      = 20
	DefaultSnippetTokens  = 24
	maxSnippetTokensFTS5  = 64
	minSnippetTokensFTS5  = 1
	defaultSnippetEllipse = "…"
)

// TranscriptHit is one search result: the segment that matched, where it is,
// and enough surrounding words to know whether it is the one you meant.
type TranscriptHit struct {
	SegmentID   int64  `json:"segmentId"`
	RecordingID int64  `json:"recordingId"`
	Recording   string `json:"recording"`
	SessionID   int64  `json:"sessionId,omitempty"`

	Track   int    `json:"track"`
	Speaker string `json:"speaker,omitempty"`

	StartMS int64 `json:"startMs"`
	EndMS   int64 `json:"endMs"`
	// At is the wall-clock instant of the utterance: the recording's start
	// plus the offset. What a human actually wants to be told.
	At time.Time `json:"at"`

	Text string `json:"text"`
	// Snippet is Text with the matched terms wrapped in HighlightOpen and
	// HighlightClose, elided to SnippetTokens words.
	Snippet string `json:"snippet"`
	// Context is the neighbouring segments and this one, in time order,
	// joined by a space. Empty when Context was disabled.
	Context string `json:"context,omitempty"`

	// Score is BM25 relevance, higher is better. SQLite's bm25() returns
	// smaller-is-better negatives; this is negated so it reads the way a
	// person expects.
	Score float64 `json:"score"`
}

// SearchTranscripts answers "find where I said X".
func (d *DB) SearchTranscripts(q TranscriptQuery) ([]TranscriptHit, error) {
	match, err := q.match()
	if err != nil {
		return nil, err
	}

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	snip := q.SnippetTokens
	if snip <= 0 {
		snip = DefaultSnippetTokens
	}
	if snip > maxSnippetTokensFTS5 {
		snip = maxSnippetTokensFTS5
	}
	if snip < minSnippetTokensFTS5 {
		snip = minSnippetTokensFTS5
	}

	where, args := q.filters()
	// snippet() and bm25() are auxiliary functions and must be evaluated
	// against the FTS table in the same query as the MATCH; there is no way to
	// add them afterwards.
	sqlText := `SELECT s.id, s.recording_id, r.filename, s.track, s.speaker,
			s.start_ms, s.end_ms, s.text, r.started_at,
			COALESCE(sr.session_id, 0),
			snippet(transcript_fts, 0, ?, ?, ?, ?),
			bm25(transcript_fts)
		FROM transcript_fts
		JOIN transcript_segments s ON s.id = transcript_fts.rowid
		JOIN recordings r ON r.id = s.recording_id
		LEFT JOIN session_recordings sr ON sr.recording_id = s.recording_id
		WHERE transcript_fts MATCH ?`
	full := append([]any{HighlightOpen, HighlightClose, defaultSnippetEllipse, snip, match}, args...)
	if where != "" {
		sqlText += ` AND ` + where
	}
	switch q.Order {
	case OrderTime:
		sqlText += ` ORDER BY r.started_at ASC, s.start_ms ASC, s.track ASC, s.id ASC`
	case OrderRecent:
		sqlText += ` ORDER BY r.started_at DESC, s.start_ms DESC, s.track ASC, s.id DESC`
	default:
		sqlText += ` ORDER BY bm25(transcript_fts) ASC, r.started_at DESC, s.start_ms ASC, s.id ASC`
	}
	sqlText += ` LIMIT ? OFFSET ?`
	full = append(full, limit, offset)

	rows, err := d.sql.Query(sqlText, full...)
	if err != nil {
		return nil, wrapFTSError(err)
	}
	defer rows.Close()

	out := []TranscriptHit{}
	for rows.Next() {
		var (
			h       TranscriptHit
			started int64
			score   float64
		)
		if err := rows.Scan(&h.SegmentID, &h.RecordingID, &h.Recording, &h.Track, &h.Speaker,
			&h.StartMS, &h.EndMS, &h.Text, &started, &h.SessionID, &h.Snippet, &score); err != nil {
			return nil, err
		}
		h.At = time.Unix(started, 0).Add(time.Duration(h.StartMS) * time.Millisecond)
		h.Score = -score
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFTSError(err)
	}

	ctxN := q.Context
	if ctxN == 0 {
		ctxN = DefaultSearchContext
	}
	if ctxN > MaxSearchContext {
		ctxN = MaxSearchContext
	}
	if ctxN > 0 {
		for i := range out {
			text, err := d.segmentContext(out[i], ctxN)
			if err != nil {
				return nil, err
			}
			out[i].Context = text
		}
	}
	return out, nil
}

// CountTranscriptMatches is the total the search would return without paging,
// for "1–50 of 312".
func (d *DB) CountTranscriptMatches(q TranscriptQuery) (int, error) {
	match, err := q.match()
	if err != nil {
		return 0, err
	}
	where, args := q.filters()
	sqlText := `SELECT COUNT(*)
		FROM transcript_fts
		JOIN transcript_segments s ON s.id = transcript_fts.rowid
		JOIN recordings r ON r.id = s.recording_id
		LEFT JOIN session_recordings sr ON sr.recording_id = s.recording_id
		WHERE transcript_fts MATCH ?`
	full := append([]any{match}, args...)
	if where != "" {
		sqlText += ` AND ` + where
	}
	var n int
	if err := d.sql.QueryRow(sqlText, full...).Scan(&n); err != nil {
		return 0, wrapFTSError(err)
	}
	return n, nil
}

// match turns the query into an FTS5 MATCH expression.
func (q TranscriptQuery) match() (string, error) {
	if q.Raw {
		s := strings.TrimSpace(q.Text)
		if s == "" {
			return "", ErrEmptyQuery
		}
		return s, nil
	}
	return MatchQuery(q.Text, q.Prefix)
}

// filters builds the non-FTS half of the WHERE clause.
func (q TranscriptQuery) filters() (string, []any) {
	var (
		parts []string
		args  []any
	)
	if q.RecordingID > 0 {
		parts = append(parts, `s.recording_id = ?`)
		args = append(args, q.RecordingID)
	}
	if q.SessionID > 0 {
		parts = append(parts, `sr.session_id = ?`)
		args = append(args, q.SessionID)
	}
	if q.Track != nil {
		parts = append(parts, `s.track = ?`)
		args = append(args, *q.Track)
	}
	if s := strings.TrimSpace(q.Speaker); s != "" {
		parts = append(parts, `s.speaker = ? COLLATE NOCASE`)
		args = append(args, s)
	}
	if !q.Since.IsZero() {
		parts = append(parts, `r.started_at >= ?`)
		args = append(args, q.Since.Unix())
	}
	if !q.Until.IsZero() {
		parts = append(parts, `r.started_at <= ?`)
		args = append(args, q.Until.Unix())
	}
	return strings.Join(parts, " AND "), args
}

// segmentContext glues the neighbouring utterances of the same track around a
// hit. Same track, not same recording: the neighbour on another track is a
// different person talking over the top, and splicing it in would fabricate a
// sentence nobody said.
func (d *DB) segmentContext(h TranscriptHit, n int) (string, error) {
	before, err := d.contextSide(h, n, true)
	if err != nil {
		return "", err
	}
	after, err := d.contextSide(h, n, false)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(before)+len(after)+1)
	parts = append(parts, before...)
	parts = append(parts, strings.TrimSpace(h.Text))
	parts = append(parts, after...)

	kept := parts[:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " "), nil
}

func (d *DB) contextSide(h TranscriptHit, n int, before bool) ([]string, error) {
	// Ordering by (start_ms, id) both ways keeps two segments that begin in
	// the same millisecond in insertion order rather than an arbitrary one.
	cmp, dir := `<`, `DESC`
	if !before {
		cmp, dir = `>`, `ASC`
	}
	q := fmt.Sprintf(`SELECT text FROM transcript_segments
		WHERE recording_id = ? AND track = ? AND (start_ms %s ? OR (start_ms = ? AND id %s ?))
		ORDER BY start_ms %s, id %s LIMIT ?`, cmp, cmp, dir, dir)
	rows, err := d.sql.Query(q, h.RecordingID, h.Track, h.StartMS, h.StartMS, h.SegmentID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, strings.TrimSpace(s))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if before {
		// Collected newest-first; the reader wants them in the order spoken.
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

// MatchQuery translates what a human typed into an FTS5 MATCH expression.
//
// It does not pass the string through. FTS5's query language treats a great
// deal of ordinary punctuation as syntax, so a search for "isn't it -- right?"
// or for a filename with a colon in it is a syntax ERROR, not zero results,
// and the user gets a database message instead of an answer. Every bare term
// is therefore re-emitted double-quoted, which in FTS5 means "this literal
// string", and the only operators that survive are the ones the user typed in
// capitals on purpose.
//
// Supported by design:
//   - "quoted phrases" stay phrases
//   - AND, OR, NOT in capitals stay operators
//   - a trailing * on a term, or prefix=true, becomes a prefix match
//
// Everything else is a search term.
func MatchQuery(s string, prefix bool) (string, error) {
	toks := tokenizeQuery(s)
	// Trailing and leading operators are a syntax error in FTS5 and are
	// nearly always a half-typed query rather than an intent.
	for len(toks) > 0 && toks[0].operator {
		toks = toks[1:]
	}
	for len(toks) > 0 && toks[len(toks)-1].operator {
		toks = toks[:len(toks)-1]
	}
	if len(toks) == 0 {
		return "", ErrEmptyQuery
	}

	var parts []string
	for i, t := range toks {
		if t.operator {
			// Two operators in a row is also a syntax error; keep the first.
			if i+1 < len(toks) && toks[i+1].operator {
				continue
			}
			parts = append(parts, t.text)
			continue
		}
		term := `"` + strings.ReplaceAll(t.text, `"`, `""`) + `"`
		// A prefix match on a phrase means "the last word of the phrase is a
		// prefix", which is exactly what FTS5 does with "a b"*.
		if t.prefix || (prefix && i == len(toks)-1) {
			term += "*"
		}
		parts = append(parts, term)
	}
	return strings.Join(parts, " "), nil
}

type queryToken struct {
	text     string
	operator bool
	prefix   bool
}

// tokenizeQuery splits on whitespace, keeps double-quoted runs together, and
// drops anything that is only punctuation.
func tokenizeQuery(s string) []queryToken {
	var (
		out   []queryToken
		cur   strings.Builder
		inStr bool
	)
	flush := func(quoted bool) {
		text := cur.String()
		cur.Reset()
		if !quoted {
			text = strings.TrimFunc(text, func(r rune) bool {
				// A trailing * is the user asking for a prefix and is handled
				// by the caller; everything else non-alphanumeric goes.
				return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '*'
			})
		}
		wantPrefix := strings.HasSuffix(text, "*")
		text = strings.TrimRight(text, "*")
		if strings.TrimSpace(text) == "" {
			return
		}
		if !quoted {
			switch text {
			case "AND", "OR", "NOT":
				out = append(out, queryToken{text: text, operator: true})
				return
			}
		}
		out = append(out, queryToken{text: text, prefix: wantPrefix})
	}

	for _, r := range s {
		switch {
		case r == '"':
			if inStr {
				flush(true)
				inStr = false
			} else {
				flush(false)
				inStr = true
			}
		case !inStr && unicode.IsSpace(r):
			flush(false)
		default:
			cur.WriteRune(r)
		}
	}
	flush(inStr)
	return out
}

// wrapFTSError turns SQLite's FTS5 parse failure into something a caller can
// tell apart from a real database problem, because it is a user typo and the
// API should answer 400, not 500.
func wrapFTSError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "fts5: syntax error") ||
		strings.Contains(err.Error(), "no such column") && strings.Contains(err.Error(), "fts5") {
		return fmt.Errorf("%w: %v", ErrBadQuery, err)
	}
	return err
}

// ErrBadQuery is a malformed raw FTS5 query. Only Raw queries can produce it —
// MatchQuery cannot emit invalid syntax.
var ErrBadQuery = errors.New("invalid search query")

// placeholders builds "?,?,?" for an IN clause.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func int64Args(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}
