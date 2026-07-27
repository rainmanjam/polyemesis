package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/media"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// The library replaces the flat recordings list with the unit a human actually
// thinks in: a session, which is a run of segments plus the transcript over it.
//
// The headline is the search. Because each microphone was recorded on its own
// track, a transcript here carries speaker attribution with no diarization
// model behind it, and "find where I said X" is answerable across every
// broadcast this box has ever recorded. Everything else on this page exists to
// get the user from a search hit to the moment it names.

// ---------------------------------------------------------------- the views

// recordingAssets is which derived files exist on disk for one recording.
//
// Presence is stat'ed rather than inferred from a finished job, because a job
// row and a file are two different facts: the file survives the history being
// purged, and a half-written proxy from a killed encoder does not become a
// playable one because a row says "done".
type recordingAssets struct {
	Proxy        bool `json:"proxy"`
	Poster       bool `json:"poster"`
	ContactSheet bool `json:"contactSheet"`
	Sprites      bool `json:"sprites"`
	Archive      bool `json:"archive"`
}

// libraryRecording is one segment as the library shows it.
type libraryRecording struct {
	db.Recording
	SessionID int64 `json:"sessionId,omitempty"`

	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`

	HasTranscript bool            `json:"hasTranscript"`
	Assets        recordingAssets `json:"assets"`
	// ActiveJobs are the queued or running jobs about this recording, so the
	// row can show "transcribing, 40%" instead of an inert button the user
	// presses a second time.
	ActiveJobs []jobView `json:"activeJobs,omitempty"`
}

// librarySession is one broadcast, as the primary unit of the page.
type librarySession struct {
	db.Session
	// DisplayTitle is resolved server-side so an untitled session reads the
	// same everywhere rather than each caller inventing its own fallback.
	DisplayTitle string `json:"displayTitle"`
	// PosterRecordingID and PosterFile name the derived still that stands in
	// for the session, zero and empty when none has been generated. The id
	// rather than the filename, because that is what the media URL is keyed on
	// and resolving a name back to a row in the browser would mean shipping the
	// whole segment list to render a thumbnail.
	PosterRecordingID int64  `json:"posterRecordingId,omitempty"`
	PosterFile        string `json:"posterFile,omitempty"`
	// Transcribed is how many of this session's segments have a transcript,
	// which is what makes "this session is searchable" legible at a glance.
	Transcribed int `json:"transcribed"`
}

// libraryView is the whole page's first load.
type libraryView struct {
	Sessions  []librarySession   `json:"sessions"`
	Ungrouped []libraryRecording `json:"ungrouped"`
	Tags      []string           `json:"tags"`
	Speakers  []string           `json:"speakers"`

	// Jobs reports whether work can be submitted at all. False hides the
	// action buttons rather than offering work nothing would pick up.
	Jobs bool `json:"jobsAvailable"`
	// Transcribe and TranscribeNote are the same answer for whisper.cpp
	// specifically, so the transcribe button can explain itself.
	Transcribe     bool   `json:"transcribeAvailable"`
	TranscribeNote string `json:"transcribeNote,omitempty"`

	// Markers are the sentinels SearchTranscripts wraps matched terms in. Sent
	// rather than hard-coded on the client, because two copies of a private-use
	// codepoint is one copy too many.
	Markers [2]string `json:"markers"`
}

// ---------------------------------------------------------------- the loads

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.store.ListSessions()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	ungrouped, err := s.store.UngroupedRecordings()
	if err != nil {
		writeStoreError(w, err)
		return
	}

	out := libraryView{
		Sessions:       make([]librarySession, 0, len(sessions)),
		Ungrouped:      make([]libraryRecording, 0, len(ungrouped)),
		Tags:           []string{},
		Speakers:       []string{},
		Jobs:           s.jobq != nil,
		Transcribe:     s.whisper.Available(),
		TranscribeNote: s.whisper.Unavailable(),
		Markers:        [2]string{db.HighlightOpen, db.HighlightClose},
	}
	if tags, err := s.store.SessionTags(); err == nil {
		out.Tags = tags
	}
	if speakers, err := s.store.TranscriptSpeakers(); err == nil {
		out.Speakers = speakers
	}

	// One pass over every recording rather than a query per session: a year of
	// segments is thousands of rows and this page is opened constantly.
	all, err := s.store.ListRecordings()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	ids := make([]int64, 0, len(all))
	for _, rec := range all {
		ids = append(ids, rec.ID)
	}
	transcribed, err := s.store.TranscribedRecordings(ids)
	if err != nil {
		s.log.Warn("library: transcript index unavailable", "err", err)
		transcribed = map[int64]bool{}
	}
	sessionOf, err := s.store.SessionIDsForRecordings(ids)
	if err != nil {
		s.log.Warn("library: session index unavailable", "err", err)
		sessionOf = map[int64]int64{}
	}

	// Members, oldest first, so the representative frame comes from the start
	// of the broadcast rather than from whatever segment happened to sort last.
	members := map[int64][]db.Recording{}
	for i := len(all) - 1; i >= 0; i-- {
		rec := all[i]
		if sid := sessionOf[rec.ID]; sid != 0 {
			members[sid] = append(members[sid], rec)
		}
	}

	dir := s.eng().Recordings().Dir()
	for _, sess := range sessions {
		view := librarySession{Session: sess, DisplayTitle: sess.DisplayTitle()}
		for _, rec := range members[sess.ID] {
			if transcribed[rec.ID] {
				view.Transcribed++
			}
			if view.PosterRecordingID == 0 {
				if file := representativeFrame(dir, rec.Filename); file != "" {
					view.PosterRecordingID, view.PosterFile = rec.ID, file
				}
			}
		}
		out.Sessions = append(out.Sessions, view)
	}

	meta, err := s.store.ListRecordingMeta(ids)
	if err != nil {
		meta = map[int64]db.RecordingMeta{}
	}
	for _, rec := range ungrouped {
		out.Ungrouped = append(out.Ungrouped, s.recordingView(rec, dir, meta, transcribed, sessionOf, nil))
	}
	writeJSON(w, http.StatusOK, out)
}

// representativeFrame picks the derived still that best stands in for a
// recording: the contact sheet, which shows the shape of the whole segment,
// falling back to the single poster frame.
func representativeFrame(recordingsDir, name string) string {
	layout := media.LayoutFor(recordingsDir, name)
	if fileExists(layout.ContactSheet) {
		return media.ContactSheetName
	}
	if fileExists(layout.Poster) {
		return media.PosterName
	}
	return ""
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() > 0
}

func assetsFor(recordingsDir, name string) recordingAssets {
	layout := media.LayoutFor(recordingsDir, name)
	a := recordingAssets{
		Proxy:        fileExists(layout.Proxy),
		Poster:       fileExists(layout.Poster),
		ContactSheet: fileExists(layout.ContactSheet),
		Archive:      fileExists(layout.Archive),
	}
	// The sprite sheets are numbered, so the VTT is the one file whose presence
	// means the whole set was written.
	a.Sprites = fileExists(layout.SpriteVTT)
	return a
}

// recordingView decorates one row. activeJobs may be nil, which is the listing
// case: enumerating the queue per row would be a query per recording.
func (s *Server) recordingView(rec db.Recording, dir string, meta map[int64]db.RecordingMeta,
	transcribed map[int64]bool, sessionOf map[int64]int64, activeJobs []jobView) libraryRecording {
	v := libraryRecording{
		Recording:     rec,
		SessionID:     sessionOf[rec.ID],
		HasTranscript: transcribed[rec.ID],
		Assets:        assetsFor(dir, rec.Filename),
		ActiveJobs:    activeJobs,
	}
	if m, ok := meta[rec.ID]; ok {
		v.Title, v.Description, v.Tags = m.Title, m.Description, m.Tags
	}
	return v
}

// activeJobsFor lists the queued or running work about one recording. A nil
// queue simply means no jobs, not an error: the library works without one.
func (s *Server) activeJobsFor(id int64, names map[int64]string) []jobView {
	if s.jobq == nil {
		return nil
	}
	f := jobs.Active()
	f.Target = jobs.RecordingTarget(id)
	list, err := s.jobq.List(f)
	if err != nil {
		s.log.Warn("library: job listing unavailable", "recording", id, "err", err)
		return nil
	}
	stats := s.jobq.Stats()
	snap := s.snapshot()
	now := time.Now()
	free := stats.Running < s.concurrency()
	out := make([]jobView, 0, len(list))
	for _, j := range list {
		out = append(out, s.view(j, snap, stats.Paused, free, names, now))
	}
	return out
}

func (s *Server) handleGetLibrarySession(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	sess, err := s.store.GetSession(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	recs, err := s.store.SessionRecordings(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session": librarySession{
			Session:      *sess,
			DisplayTitle: sess.DisplayTitle(),
		},
		"recordings": s.expand(recs),
	})
}

// expand decorates a set of recordings with everything the segment list shows.
func (s *Server) expand(recs []db.Recording) []libraryRecording {
	dir := s.eng().Recordings().Dir()
	ids := make([]int64, 0, len(recs))
	for _, rec := range recs {
		ids = append(ids, rec.ID)
	}
	meta, err := s.store.ListRecordingMeta(ids)
	if err != nil {
		meta = map[int64]db.RecordingMeta{}
	}
	transcribed, err := s.store.TranscribedRecordings(ids)
	if err != nil {
		transcribed = map[int64]bool{}
	}
	sessionOf, err := s.store.SessionIDsForRecordings(ids)
	if err != nil {
		sessionOf = map[int64]int64{}
	}
	names := make(map[int64]string, len(recs))
	for _, rec := range recs {
		names[rec.ID] = rec.Filename
	}

	out := make([]libraryRecording, 0, len(recs))
	for _, rec := range recs {
		out = append(out, s.recordingView(rec, dir, meta, transcribed, sessionOf,
			s.activeJobsFor(rec.ID, names)))
	}
	return out
}

func (s *Server) handleGetLibraryRecording(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	rec, err := s.store.GetRecording(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	views := s.expand([]db.Recording{*rec})
	tracks, err := s.store.ListTranscriptTracks(id)
	if err != nil {
		s.log.Warn("library: transcript tracks unavailable", "recording", id, "err", err)
		tracks = nil
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recording":        views[0],
		"transcriptTracks": tracks,
	})
}

// ------------------------------------------------------------- session edits

func (s *Server) handleCreateLibrarySession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		db.Metadata
		Recordings []int64 `json:"recordings,omitempty"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	// auto=false: a session a human asked for is a decision, and the backfill
	// must never rewrite it.
	sess, err := s.store.CreateSession(req.Metadata, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Recordings) > 0 {
		if err := s.store.SetSessionRecordings(sess.ID, req.Recordings); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if sess, err = s.store.GetSession(sess.ID); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, librarySession{Session: *sess, DisplayTitle: sess.DisplayTitle()})
}

func (s *Server) handleUpdateLibrarySession(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		db.Metadata
		// Recordings, when present, replaces the membership. Omitted leaves it
		// alone, so renaming a session cannot silently empty it.
		Recordings []int64 `json:"recordings,omitempty"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	sess, err := s.store.UpdateSessionMeta(id, req.Metadata)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if req.Recordings != nil {
		if err := s.store.SetSessionRecordings(id, req.Recordings); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if sess, err = s.store.GetSession(id); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, librarySession{Session: *sess, DisplayTitle: sess.DisplayTitle()})
}

func (s *Server) handleDeleteLibrarySession(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteSession(id); err != nil {
		writeStoreError(w, err)
		return
	}
	// Said explicitly because the button is next to a delete that DOES remove
	// media, and an operator has to be sure which one they pressed.
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "deleted",
		"note":   "the recordings were ungrouped, not deleted",
	})
}

// handleRegroupSessions runs the backfill: additive, idempotent, and it never
// merges a grouping a human has already split.
func (s *Server) handleRegroupSessions(w http.ResponseWriter, r *http.Request) {
	res, err := s.store.BackfillSessions(db.DefaultSessionRules())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	pruned, err := s.store.PruneEmptySessions()
	if err != nil {
		s.log.Warn("library: pruning empty sessions failed", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created":  res.Created,
		"assigned": res.Assigned,
		"extended": res.Extended,
		"groups":   res.Groups,
		"pruned":   pruned,
	})
}

func (s *Server) handleUpdateLibraryRecording(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req db.Metadata
	if !decodeJSON(w, r, &req) {
		return
	}
	meta, err := s.store.SetRecordingMeta(id, req)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

// ------------------------------------------------------------- transcripts

func (s *Server) handleGetTranscript(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	t, err := s.store.GetTranscript(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Merged is the free-diarization view: every track's segments in time
	// order, each already attributed to the microphone it came from. Sent
	// alongside the per-track breakdown because it is what a reader wants and
	// recomputing it in the browser would mean a second sort of the same data.
	writeJSON(w, http.StatusOK, map[string]any{
		"transcript": t,
		"merged":     t.Merged(),
		"speakers":   t.Speakers(),
		"segments":   t.SegmentCount(),
	})
}

func (s *Server) handleDeleteTranscript(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	// track is optional: absent deletes the whole transcript, present deletes
	// one track so a single bad microphone can be re-run without discarding
	// the others.
	if raw := r.URL.Query().Get("track"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid track")
			return
		}
		if err := s.store.DeleteTranscriptTrack(id, n); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		return
	}
	if err := s.store.DeleteTranscript(id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleSetTranscriptSpeaker relabels one track's speaker.
//
// This is the manual half of the free-diarization story: the tracks already
// separate the voices, and this is where "track 2" becomes "Ana".
func (s *Server) handleSetTranscriptSpeaker(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		Track   int    `json:"track"`
		Speaker string `json:"speaker"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Track < 0 {
		writeError(w, http.StatusBadRequest, "invalid track")
		return
	}
	if err := s.store.SetTranscriptSpeaker(id, req.Track, req.Speaker); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"track": req.Track, "speaker": req.Speaker})
}

// ----------------------------------------------------------------- search

// handleSearchTranscripts is the headline of the whole workstream: full-text
// search across every transcript this box holds.
//
// A GET with query parameters rather than a POST with a body, because a search
// result is a place — it has to survive being bookmarked, shared and reloaded.
func (s *Server) handleSearchTranscripts(w http.ResponseWriter, r *http.Request) {
	q, err := searchQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hits, err := s.store.SearchTranscripts(q)
	if err != nil {
		writeSearchError(w, err)
		return
	}
	total, err := s.store.CountTranscriptMatches(q)
	if err != nil {
		// The hits are already in hand; failing the whole request over a count
		// would be the restrictive mistake. -1 reads as "unknown".
		s.log.Warn("library: match count unavailable", "err", err)
		total = -1
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hits":    hits,
		"total":   total,
		"limit":   q.Limit,
		"offset":  q.Offset,
		"markers": [2]string{db.HighlightOpen, db.HighlightClose},
	})
}

// writeSearchError maps the two search errors onto 400. Both are the user's
// query, not the server's fault, and a 500 here would have an operator
// restarting a perfectly healthy box.
func writeSearchError(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrEmptyQuery) || errors.Is(err, db.ErrBadQuery) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeStoreError(w, err)
}

func searchQuery(r *http.Request) (db.TranscriptQuery, error) {
	v := r.URL.Query()
	q := db.TranscriptQuery{
		Text:    strings.TrimSpace(v.Get("q")),
		Prefix:  boolParam(v.Get("prefix")),
		Raw:     boolParam(v.Get("raw")),
		Speaker: strings.TrimSpace(v.Get("speaker")),
	}
	if q.Text == "" {
		return q, db.ErrEmptyQuery
	}

	var err error
	if q.RecordingID, err = int64Param(v.Get("recordingId")); err != nil {
		return q, errors.New("invalid recordingId")
	}
	if q.SessionID, err = int64Param(v.Get("sessionId")); err != nil {
		return q, errors.New("invalid sessionId")
	}
	if raw := v.Get("track"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return q, errors.New("invalid track")
		}
		q.Track = &n
	}
	if q.Since, err = timeParam(v.Get("since")); err != nil {
		return q, errors.New("invalid since")
	}
	if q.Until, err = timeParam(v.Get("until")); err != nil {
		return q, errors.New("invalid until")
	}
	switch o := db.TranscriptOrder(v.Get("order")); o {
	case "", db.OrderRelevance, db.OrderTime, db.OrderRecent:
		q.Order = o
	default:
		return q, fmt.Errorf("unknown order %q (relevance, time, recent)", o)
	}
	for _, p := range []struct {
		name string
		into *int
	}{
		{"limit", &q.Limit},
		{"offset", &q.Offset},
		{"context", &q.Context},
		{"snippetTokens", &q.SnippetTokens},
	} {
		raw := v.Get(p.name)
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			return q, errors.New("invalid " + p.name)
		}
		*p.into = n
	}
	// The store clamps limit and context itself; offset is the one a negative
	// value would turn into a SQL error rather than a smaller answer.
	if q.Offset < 0 {
		return q, errors.New("invalid offset")
	}
	return q, nil
}

func boolParam(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func int64Param(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("invalid")
	}
	return n, nil
}

// timeParam accepts RFC3339 or a bare date, because a date range picker sends
// the second and a permalink carries the first.
func timeParam(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}

// ------------------------------------------------------------ derived media

// handleLibraryMedia serves one derived file: the proxy the inline player
// needs, the poster, the contact sheet, a sprite sheet or its VTT.
//
// media.Resolve is the path guard. The recording NAME comes from a database
// row and the FILE from the URL, and neither is trusted as a path — Resolve
// confines the result to the derived directory or refuses.
func (s *Server) handleLibraryMedia(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	rec, err := s.store.GetRecording(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	path, err := media.Resolve(s.eng().Recordings().Dir(), rec.Filename, chi.URLParam(r, "file"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := os.Open(path)
	if err != nil {
		// A specific message rather than a bare 404: "generate it" is an action
		// the library offers, and the client keys off this to offer it.
		writeError(w, http.StatusNotFound, "that derived file has not been generated yet")
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		writeError(w, http.StatusNotFound, "that derived file has not been generated yet")
		return
	}
	// Derived files are rewritten only by a job the user triggered, so a short
	// cache is safe and is what stops a scrubbing player refetching the sprite
	// sheet on every hover. Private: it is behind a session.
	w.Header().Set("Cache-Control", "private, max-age=300")
	// ServeContent gives range requests, which is what makes seeking in the
	// proxy work at all.
	http.ServeContent(w, r, filepath.Base(path), stat.ModTime(), f)
}

// ------------------------------------------------------------ job submission

// submitRequest is the union of every kind's parameters. One struct rather than
// five, because the client sends only the fields its button knows about and a
// per-kind body would mean five nearly identical decode paths.
type submitRequest struct {
	// Priority is "user", "normal" or "bulk". Empty means user: everything on
	// this page was pressed by somebody who is watching for the result.
	Priority string `json:"priority,omitempty"`

	// --- transcribe ---
	Tracks    []int    `json:"tracks,omitempty"`
	Model     string   `json:"model,omitempty"`
	Backend   string   `json:"backend,omitempty"`
	Language  string   `json:"language,omitempty"`
	Translate bool     `json:"translate,omitempty"`
	Threads   int      `json:"threads,omitempty"`
	Formats   []string `json:"formats,omitempty"`

	// --- proxy ---
	Height     int  `json:"height,omitempty"`
	Width      int  `json:"width,omitempty"`
	CRF        int  `json:"crf,omitempty"`
	AudioTrack *int `json:"audioTrack,omitempty"`

	// --- thumbnails ---
	SkipPoster       bool `json:"skipPoster,omitempty"`
	SkipContactSheet bool `json:"skipContactSheet,omitempty"`
	SkipSprites      bool `json:"skipSprites,omitempty"`

	// --- archive ---
	Codec            string `json:"codec,omitempty"`
	Quality          int    `json:"quality,omitempty"`
	Preset           string `json:"preset,omitempty"`
	AcknowledgeLossy bool   `json:"acknowledgeLossy,omitempty"`
	ReplaceOriginal  bool   `json:"replaceOriginal,omitempty"`

	// --- clip export ---
	InMS  int64  `json:"inMs,omitempty"`
	OutMS int64  `json:"outMs,omitempty"`
	Mode  string `json:"mode,omitempty"`
	Title string `json:"title,omitempty"`
}

func (req submitRequest) priority() jobs.Priority {
	switch strings.ToLower(strings.TrimSpace(req.Priority)) {
	case "bulk":
		return jobs.PriorityBulk
	case "normal":
		return jobs.PriorityNormal
	default:
		return jobs.PriorityUser
	}
}

// handleSubmitRecordingJob queues one piece of post-production about one
// recording. It never runs anything itself: the queue owns when, the governor
// owns whether, and this only owns what.
func (s *Server) handleSubmitRecordingJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	rec, err := s.store.GetRecording(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req submitRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	kind := jobs.Kind(chi.URLParam(r, "kind"))
	job, err := s.buildJob(kind, *rec, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	job.Priority = req.priority()

	out, created, err := s.jobq.Submit(job)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	stats := s.jobq.Stats()
	view := s.view(*out, s.snapshot(), stats.Paused, stats.Running < s.concurrency(),
		map[int64]string{rec.ID: rec.Filename}, time.Now())
	status := http.StatusCreated
	if !created {
		// 200, not 201: Unique folded this into work that was already running,
		// and telling the client it created something would have it counting
		// two jobs where there is one.
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"job": view, "created": created})
}

func (s *Server) buildJob(kind jobs.Kind, rec db.Recording, req submitRequest) (jobs.Job, error) {
	switch kind {
	case transcribe.KindTranscribe:
		return s.transcribeJob(rec, req)
	case media.KindProxy:
		return media.NewProxyJob(rec.ID, media.ProxyParams{
			Recording:  rec.Filename,
			DurationMS: rec.DurationMS,
			AudioTrack: req.AudioTrack,
			Width:      req.Width,
			Height:     req.Height,
			CRF:        req.CRF,
		})
	case media.KindThumbnails:
		return media.NewThumbnailJob(rec.ID, media.ThumbnailParams{
			Recording:        rec.Filename,
			DurationMS:       rec.DurationMS,
			SkipPoster:       req.SkipPoster,
			SkipContactSheet: req.SkipContactSheet,
			SkipSprites:      req.SkipSprites,
		})
	case media.KindArchive:
		return media.NewArchiveJob(rec.ID, media.ArchiveParams{
			Recording:  rec.Filename,
			DurationMS: rec.DurationMS,
			// The age gate is the archive worker's, and it needs to know how
			// old the recording is rather than how old the row is.
			RecordedAtUnix:   rec.StartedAt.Unix(),
			Codec:            media.ArchiveCodec(req.Codec),
			Quality:          req.Quality,
			Preset:           req.Preset,
			AcknowledgeLossy: req.AcknowledgeLossy,
			ReplaceOriginal:  req.ReplaceOriginal,
		})
	case clipper.JobKind:
		// Deliberately not submitted from here. A clip is chosen from a
		// timeline with in and out points, and the clip editor owns that whole
		// conversation — plan, keyframes, drift — in clips.go. Two places
		// building the same JobParams would be two spellings of one job, and
		// the library's Clip action is a link into that editor rather than a
		// second, worse submission path.
		return jobs.Job{}, errors.New("clip exports are submitted from the clip editor")
	default:
		return jobs.Job{}, fmt.Errorf("unknown job kind %q", kind)
	}
}

func (s *Server) transcribeJob(rec db.Recording, req submitRequest) (jobs.Job, error) {
	formats := make([]transcribe.SubtitleFormat, 0, len(req.Formats))
	for _, f := range req.Formats {
		sf := transcribe.SubtitleFormat(strings.TrimSpace(f))
		if !transcribe.ValidFormat(sf) {
			return jobs.Job{}, fmt.Errorf("unknown subtitle format %q", f)
		}
		formats = append(formats, sf)
	}

	params := transcribe.Params{
		Recording:   rec.Filename,
		RecordingID: rec.ID,
		Tracks:      req.Tracks,
		// Copied at submission rather than read when the job runs: a transcript
		// is a record of a session, and re-running it after the roles were
		// rearranged must not relabel the speakers.
		Annotations: s.eng().Settings().Ingest.Annotations,
		Model:       strings.TrimSpace(req.Model),
		Backend:     transcribe.Backend(strings.TrimSpace(req.Backend)),
		Language:    strings.TrimSpace(req.Language),
		Translate:   req.Translate,
		Threads:     req.Threads,
		Formats:     formats,
		DurationMS:  rec.DurationMS,
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return jobs.Job{}, err
	}
	return jobs.Job{
		Kind:   transcribe.KindTranscribe,
		Target: jobs.RecordingTarget(rec.ID),
		Params: raw,
		// Pressing Transcribe twice must not transcribe twice.
		Unique: true,
	}, nil
}
