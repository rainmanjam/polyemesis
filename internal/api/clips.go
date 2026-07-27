package api

// The clip editor's server side: choose in and out points in a recording that
// is already on disk, see where the keyframes are and how far the cut will
// move, then queue the export.
//
// NOT the rolling clip buffer. That is internal/clips, it lives under
// /api/v1/clips, and it answers "give me the last thirty seconds of what is
// going out right now" from memory. Everything in this file is the other
// feature — internal/clipper, under /api/v1/clipper — which reads files hours
// later and cuts them keyframe-accurately. The two never share a path, a job
// kind or a directory, because a user who confuses them loses work.
//
// Nothing here runs FFmpeg on the request. Planning runs ffprobe, which only
// demuxes a bounded window, and the cut itself is a queued job so the resource
// policy can hold it back while a broadcast is going out.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/media"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

const (
	// clipExportSubdir is where finished exports land, under the recordings
	// directory and beside — never inside — clips.Subdir. The rolling buffer
	// prunes its own directory by count and by size, and an export a human
	// deliberately made must not be swept away to make room for an instant
	// replay nobody kept.
	clipExportSubdir = "exports"

	// maxKeyframeWindow bounds one keyframe request. The probe is a demux, not
	// a decode, but a scrubber asking for four hours at once would still read
	// every byte of every segment; the editor asks for the visible span and
	// asks again when it zooms.
	maxKeyframeWindow = 5 * time.Minute

	// maxClipTitleLen bounds the operator's name for a clip before it becomes
	// a filename.
	maxClipTitleLen = 80
)

// ------------------------------------------------------------------ the source

// clipSourceView is everything the editor needs to draw a timeline before the
// user has touched anything.
type clipSourceView struct {
	RecordingID int64  `json:"recordingId"`
	Recording   string `json:"recording"`
	SessionID   int64  `json:"sessionId,omitempty"`
	SessionName string `json:"sessionName,omitempty"`

	StartedAt  time.Time `json:"startedAt"`
	DurationMS int64     `json:"durationMs"`
	// Segments are the recording files this timeline is stitched from, in
	// order. A cut may span several of them; the editor plays whichever one
	// the playhead is over.
	Segments []clipSegmentView `json:"segments"`
	Tracks   []clipTrackView   `json:"tracks"`

	HasTranscript bool `json:"hasTranscript"`
	// MaxClipSeconds mirrors clipper.MaxClipDuration so the editor can refuse
	// an impossible range with the same number the server would.
	MaxClipSeconds float64 `json:"maxClipSeconds"`
	// Modes is the catalogue, in the order to offer it.
	Modes []string `json:"modes"`
}

// clipSegmentView is one file on the timeline plus the derived media the
// scrubber can use for it.
type clipSegmentView struct {
	RecordingID int64  `json:"recordingId"`
	Name        string `json:"name"`
	StartMS     int64  `json:"startMs"`
	DurationMS  int64  `json:"durationMs"`

	// Proxy, Poster and SpriteVTT are URLs, empty when that file has not been
	// generated yet. An absent proxy is not an error: the editor falls back to
	// a timeline with no picture, which still cuts.
	Proxy     string `json:"proxy,omitempty"`
	Poster    string `json:"poster,omitempty"`
	SpriteVTT string `json:"spriteVtt,omitempty"`
	// MediaBase is the URL prefix the VTT's own cue payloads resolve against,
	// because those name the sheets relative to the VTT.
	MediaBase string `json:"mediaBase"`
	// Missing marks a segment whose master file is not on disk. Shown rather
	// than dropped: a hole in the timeline the user can see beats a timeline
	// that silently got shorter.
	Missing bool `json:"missing,omitempty"`
}

// clipTrackView is one audio track of the recording, described well enough to
// pick from. Roles and labels come from the ingest annotations, which is the
// same fact every destination's routing resolves against.
type clipTrackView struct {
	Index    int    `json:"index"`
	Label    string `json:"label,omitempty"`
	Role     string `json:"role,omitempty"`
	Language string `json:"language,omitempty"`
	// Speaker is the name transcription attributed to this track, which is
	// what makes "clip just what Sam said" a track selection.
	Speaker string `json:"speaker,omitempty"`
}

func (s *Server) handleClipSource(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	tl, err := s.clipTimeline(id)
	if err != nil {
		s.writeClipError(w, err)
		return
	}

	view := clipSourceView{
		RecordingID:    tl.anchor.ID,
		Recording:      tl.anchor.Filename,
		StartedAt:      tl.parts[0].rec.StartedAt,
		DurationMS:     tl.durationMS(),
		Segments:       make([]clipSegmentView, 0, len(tl.parts)),
		Tracks:         s.clipTracks(tl),
		MaxClipSeconds: clipper.MaxClipDuration.Seconds(),
		Modes:          clipModes(),
	}
	if tl.session != nil {
		view.SessionID = tl.session.ID
		view.SessionName = tl.session.DisplayTitle()
	}
	for _, p := range tl.parts {
		view.Segments = append(view.Segments, s.clipSegmentView(p))
	}
	if has, err := s.anyTranscript(tl); err == nil {
		view.HasTranscript = has
	}
	writeJSON(w, http.StatusOK, view)
}

// clipSegmentView describes one part's derived media, probing the filesystem
// for what exists rather than trusting the index.
func (s *Server) clipSegmentView(p clipPart) clipSegmentView {
	out := clipSegmentView{
		RecordingID: p.rec.ID,
		Name:        p.rec.Filename,
		StartMS:     p.startMS,
		DurationMS:  p.rec.DurationMS,
		MediaBase:   fmt.Sprintf("/api/v1/library/recordings/%d/media/", p.rec.ID),
		Missing:     p.path == "",
	}
	layout := media.LayoutFor(s.eng().Recordings().Dir(), p.rec.Filename)
	if fileExists(layout.Proxy) {
		out.Proxy = out.MediaBase + media.ProxyName
	}
	if fileExists(layout.Poster) {
		out.Poster = out.MediaBase + media.PosterName
	}
	if fileExists(layout.SpriteVTT) {
		out.SpriteVTT = out.MediaBase + media.SpriteVTTName
	}
	return out
}

// clipTracks describes the recording's audio tracks.
//
// The count comes from the index, which the recorder measured off the file; the
// descriptions come from the ingest annotations, which are the operator's own
// words for the same tracks. A recording whose track count was never measured
// falls back to the live ingest's layout rather than offering nothing: an empty
// picker would make "clip just the mic" impossible on exactly the recordings
// that most need it.
func (s *Server) clipTracks(tl clipTimeline) []clipTrackView {
	n := tl.anchor.Tracks
	if n <= 0 {
		n = len(s.eng().Source().Tracks)
	}
	if n <= 0 {
		return []clipTrackView{}
	}

	byIndex := map[int]routing.TrackAnnotation{}
	if settings, err := s.store.GetSettings(); err == nil {
		for _, a := range settings.Ingest.Annotations {
			byIndex[a.Track] = a
		}
	}
	speakers := s.clipSpeakers(tl.anchor.ID)

	out := make([]clipTrackView, 0, n)
	for i := 0; i < n; i++ {
		a := byIndex[i]
		out = append(out, clipTrackView{
			Index:    i,
			Label:    a.Label,
			Role:     string(a.Role),
			Language: a.Language,
			Speaker:  speakers[i],
		})
	}
	return out
}

// clipSpeakers maps track index to the speaker transcription attributed to it.
// A missing transcript is not an error here — it only means the picker shows
// track numbers instead of names.
func (s *Server) clipSpeakers(recordingID int64) map[int]string {
	out := map[int]string{}
	tracks, err := s.store.ListTranscriptTracks(recordingID)
	if err != nil {
		return out
	}
	for _, t := range tracks {
		if t.Speaker != "" {
			out[t.Track] = t.Speaker
		}
	}
	return out
}

func (s *Server) anyTranscript(tl clipTimeline) (bool, error) {
	for _, p := range tl.parts {
		has, err := s.store.HasTranscript(p.rec.ID)
		if err != nil {
			return false, err
		}
		if has {
			return true, nil
		}
	}
	return false, nil
}

// ------------------------------------------------------------------- timeline

// clipPart is one recording file placed on the clip timeline.
type clipPart struct {
	rec     db.Recording
	startMS int64
	// path is the resolved master on disk, empty when the file is gone.
	path string
}

// clipTimeline is the anchor recording, its session siblings, and where each
// one sits relative to the first.
type clipTimeline struct {
	anchor  db.Recording
	session *db.Session
	parts   []clipPart
}

func (t clipTimeline) durationMS() int64 {
	var end int64
	for _, p := range t.parts {
		if e := p.startMS + p.rec.DurationMS; e > end {
			end = e
		}
	}
	return end
}

// clipTimeline builds the timeline a recording belongs to.
//
// The recorder writes hourly segments, so the thing a human thinks of as "the
// recording" is usually several files, and an in-point forty minutes in may sit
// in the second of them. The session is what already groups those files, so it
// is what the editor scrubs across. A recording with no session — or a session
// read that fails — is a perfectly good one-file timeline, and is treated as
// one rather than refused.
func (s *Server) clipTimeline(id int64) (clipTimeline, error) {
	anchor, err := s.store.GetRecording(id)
	if err != nil {
		return clipTimeline{}, err
	}
	out := clipTimeline{anchor: *anchor}

	recs := []db.Recording{*anchor}
	if sess, err := s.store.SessionForRecording(id); err == nil && sess != nil {
		if members, err := s.store.SessionRecordings(sess.ID); err == nil && len(members) > 0 {
			recs = members
			out.session = sess
		}
	}
	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].StartedAt.Equal(recs[j].StartedAt) {
			return recs[i].ID < recs[j].ID
		}
		return recs[i].StartedAt.Before(recs[j].StartedAt)
	})

	// Wall-clock offsets are the honest layout, because they preserve the gap a
	// recorder restart left. They are only usable when every segment has a
	// start and they run forwards; otherwise the files go end to end, which is
	// what clipper.NewTimeline does with an all-zero set.
	offsets, ok := clipOffsets(recs)
	for i, rec := range recs {
		if rec.DurationMS <= 0 {
			// Not measured yet. A zero-length segment contributes no frames and
			// would only put a hole in the middle of the timeline.
			continue
		}
		p := clipPart{rec: rec}
		if ok {
			p.startMS = offsets[i]
		}
		if path, err := s.eng().Recordings().Resolve(rec.Filename); err == nil && fileExists(path) {
			p.path = path
		}
		out.parts = append(out.parts, p)
	}
	if !ok {
		var at int64
		for i := range out.parts {
			out.parts[i].startMS = at
			at += out.parts[i].rec.DurationMS
		}
	}
	if len(out.parts) == 0 {
		return clipTimeline{}, errClipNotMeasured
	}
	return out, nil
}

// clipOffsets converts segment start times into timeline offsets, reporting
// false when the clock cannot be trusted to produce them.
func clipOffsets(recs []db.Recording) ([]int64, bool) {
	if len(recs) == 0 {
		return nil, false
	}
	base := recs[0].StartedAt
	if base.IsZero() {
		return nil, false
	}
	out := make([]int64, len(recs))
	for i, rec := range recs {
		if rec.StartedAt.IsZero() {
			return nil, false
		}
		ms := rec.StartedAt.Sub(base).Milliseconds()
		if ms < 0 {
			return nil, false
		}
		out[i] = ms
	}
	return out, true
}

// timeline resolves the parts that are actually on disk into a cuttable
// timeline.
func (t clipTimeline) timeline() (clipper.Timeline, error) {
	segs := make([]clipper.Segment, 0, len(t.parts))
	for _, p := range t.parts {
		if p.path == "" {
			continue
		}
		segs = append(segs, clipper.Segment{
			Path:     p.path,
			Start:    time.Duration(p.startMS) * time.Millisecond,
			Duration: time.Duration(p.rec.DurationMS) * time.Millisecond,
		})
	}
	if len(segs) == 0 {
		return clipper.Timeline{}, errClipFilesMissing
	}
	// Every Start is meaningful here, including a single segment's zero, so the
	// end-to-end fallback in NewTimeline is never what runs.
	return clipper.NewTimeline(segs)
}

var (
	errClipNotMeasured  = errors.New("this recording has not been measured yet, so there is no timeline to cut from")
	errClipFilesMissing = errors.New("none of this recording's files are on disk")
)

// writeClipError maps the editor's own refusals onto statuses. Everything the
// clipper calls invalid is the request's fault, not the server's.
func (s *Server) writeClipError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, errClipNotMeasured), errors.Is(err, errClipFilesMissing):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, clipper.ErrInvalidRequest),
		errors.Is(err, clipper.ErrEmptyRange),
		errors.Is(err, clipper.ErrOutOfRange),
		errors.Is(err, clipper.ErrNoSegments):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeStoreError(w, err)
	}
}

// ------------------------------------------------------------------ keyframes

// clipKeyframeView is where a fast cut can start, over the span that was asked
// for.
type clipKeyframeView struct {
	FromMS int64 `json:"fromMs"`
	ToMS   int64 `json:"toMs"`
	// Known is false when nothing could be read. The editor must then stop
	// promising an exact in-point rather than drawing an empty timeline and
	// implying there are no keyframes in it.
	Known    bool     `json:"known"`
	TimesMS  []int64  `json:"timesMs"`
	Warnings []string `json:"warnings,omitempty"`
}

// handleClipKeyframes reports the random-access points across a span.
//
// It never fails the request over a probe that did not work. An ffprobe that
// timed out means "we cannot say", and the editor says exactly that; refusing
// the whole span would take the timeline away over a fact that only affects
// how confidently it can be drawn.
func (s *Server) handleClipKeyframes(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	tl, err := s.clipTimeline(id)
	if err != nil {
		s.writeClipError(w, err)
		return
	}

	from := time.Duration(clipQueryMS(r, "fromMs", 0)) * time.Millisecond
	to := time.Duration(clipQueryMS(r, "toMs", tl.durationMS())) * time.Millisecond
	if from < 0 {
		from = 0
	}
	if to <= from {
		to = from + time.Second
	}
	if to-from > maxKeyframeWindow {
		to = from + maxKeyframeWindow
	}

	view := clipKeyframeView{FromMS: from.Milliseconds(), ToMS: to.Milliseconds(), TimesMS: []int64{}}
	prober := s.clipProber()
	ctx := r.Context()
	var found clipper.Keyframes
	for _, p := range tl.parts {
		if p.path == "" {
			continue
		}
		start := time.Duration(p.startMS) * time.Millisecond
		end := start + time.Duration(p.rec.DurationMS)*time.Millisecond
		if end <= from || start >= to {
			continue
		}
		// The probe window is relative to the file, so the span has to leave
		// timeline coordinates first.
		at := from - start
		if at < 0 {
			at = 0
		}
		one, err := prober.Keyframes(ctx, p.path, at, to-from)
		if err != nil {
			view.Warnings = append(view.Warnings,
				fmt.Sprintf("could not read the keyframes of %s: %v", p.rec.Filename, err))
			continue
		}
		found = found.Merge(one.Shift(start))
	}
	for _, t := range found.Times() {
		if t < from || t > to {
			continue
		}
		view.TimesMS = append(view.TimesMS, t.Milliseconds())
	}
	view.Known = len(view.TimesMS) > 0
	writeJSON(w, http.StatusOK, view)
}

// clipProber is the ffprobe the planner and the keyframe view share.
func (s *Server) clipProber() clipper.Prober {
	bin := s.cfg.FFmpeg.Probe
	if tools := s.eng().Tools(); tools != nil && tools.FFprobe != "" {
		bin = tools.FFprobe
	}
	// An empty Bin means "ffprobe, off PATH", which is the right answer on a
	// machine where nothing was pinned.
	return clipper.FFprobe{Bin: bin}
}

// ------------------------------------------------------------- plan and export

// clipRequestBody is what the editor sends for both planning and exporting.
// The same shape for both is deliberate: a preview that does not describe the
// export it previews is worse than no preview.
type clipRequestBody struct {
	InMS  int64 `json:"inMs"`
	OutMS int64 `json:"outMs"`
	// Mode is clipper.ModeFast or clipper.ModePrecise; empty means the default.
	Mode string `json:"mode,omitempty"`
	// AudioMode is "all" or "tracks". A mix is deliberately not offered here:
	// mixing is the routing editor's job, and a clip that quietly re-encoded
	// its audio would break the bit-exact promise this feature is sold on.
	AudioMode string `json:"audioMode,omitempty"`
	Tracks    []int  `json:"tracks,omitempty"`
	Title     string `json:"title,omitempty"`
	// Container is "mkv" or "mp4". MKV keeps every audio track; MP4 is what a
	// social platform will accept.
	Container string `json:"container,omitempty"`
}

// clipPlanView is a resolved cut in the units a browser thinks in.
//
// clipper.Plan is not returned directly because its durations marshal as
// nanoseconds, and a timeline that has to divide every number it receives by a
// million is a timeline with a rounding bug waiting in it.
type clipPlanView struct {
	Mode          string `json:"mode"`
	RequestedMode string `json:"requestedMode"`

	RequestedInMS  int64 `json:"requestedInMs"`
	RequestedOutMS int64 `json:"requestedOutMs"`
	InMS           int64 `json:"inMs"`
	OutMS          int64 `json:"outMs"`
	DurationMS     int64 `json:"durationMs"`

	// InDriftMS is how far the delivered start landed from the requested one.
	// Negative means earlier, which is the only direction a fast cut moves.
	InDriftMS int64 `json:"inDriftMs"`
	// DriftKnown false means nobody could read the keyframes. Saying "no
	// drift" in that case would be a lie.
	DriftKnown  bool  `json:"driftKnown"`
	KeyframeMS  int64 `json:"keyframeMs"`
	ReEncodedMS int64 `json:"reEncodedMs"`

	LosslessFraction float64 `json:"losslessFraction"`
	Segments         int     `json:"segments"`
	Concat           bool    `json:"concat"`
	VideoEncoder     string  `json:"videoEncoder,omitempty"`

	OutName  string   `json:"outName"`
	Describe string   `json:"describe"`
	Warnings []string `json:"warnings,omitempty"`
}

func (s *Server) handleClipPlan(w http.ResponseWriter, r *http.Request) {
	plan, _, ok := s.planClip(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, clipPlanViewOf(plan))
}

// planClip is the shared half of preview and export: decode, validate, probe,
// plan. It writes its own errors, and reports false when it did.
func (s *Server) planClip(w http.ResponseWriter, r *http.Request) (clipper.Plan, clipTimeline, bool) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return clipper.Plan{}, clipTimeline{}, false
	}
	var body clipRequestBody
	if !decodeJSON(w, r, &body) {
		return clipper.Plan{}, clipTimeline{}, false
	}
	tl, err := s.clipTimeline(id)
	if err != nil {
		s.writeClipError(w, err)
		return clipper.Plan{}, clipTimeline{}, false
	}
	cut, err := tl.timeline()
	if err != nil {
		s.writeClipError(w, err)
		return clipper.Plan{}, clipTimeline{}, false
	}
	req, err := s.clipRequest(tl, body)
	if err != nil {
		s.writeClipError(w, err)
		return clipper.Plan{}, clipTimeline{}, false
	}

	// Bounded so a wedged ffprobe cannot hold the editor open. A plan that
	// times out comes back unsnapped and says so, which is the same graceful
	// degradation PlanWith already does for a probe that failed.
	ctx, cancel := context.WithTimeout(r.Context(), 2*clipper.ProbeTimeout)
	defer cancel()

	plan, err := clipper.PlanWith(ctx, s.clipProber(), cut, req)
	if err != nil {
		s.writeClipError(w, err)
		return clipper.Plan{}, clipTimeline{}, false
	}
	return plan, tl, true
}

// clipRequest turns the editor's body into a clipper.Request.
func (s *Server) clipRequest(tl clipTimeline, body clipRequestBody) (clipper.Request, error) {
	mode := clipper.Mode(strings.TrimSpace(body.Mode))
	if !clipper.ValidMode(mode) {
		return clipper.Request{}, fmt.Errorf("%w: unknown mode %q", clipper.ErrInvalidRequest, body.Mode)
	}
	audio, err := clipAudio(body)
	if err != nil {
		return clipper.Request{}, err
	}
	req := clipper.Request{
		In:      time.Duration(body.InMS) * time.Millisecond,
		Out:     time.Duration(body.OutMS) * time.Millisecond,
		Mode:    mode,
		Audio:   audio,
		Title:   clipTitle(body.Title),
		OutPath: s.clipOutPath(tl, body),
	}
	if mode == clipper.ModePrecise {
		// Only the leading partial GOP is re-encoded, so this names an encoder
		// for a fraction of a second of video. HeadEncoder prefers x264 and
		// falls back to it whenever detection cannot demonstrate anything,
		// which is the fail-open answer.
		if tools := s.eng().Tools(); tools != nil {
			req.VideoEncoder = clipper.HeadEncoder(tools, tools.HWEncoders)
		}
	}
	return req, nil
}

func clipAudio(body clipRequestBody) (clipper.AudioSelection, error) {
	switch clipper.AudioMode(strings.TrimSpace(body.AudioMode)) {
	case "", clipper.AudioAll:
		return clipper.AudioSelection{Mode: clipper.AudioAll}, nil
	case clipper.AudioTracks:
		return clipper.AudioSelection{Mode: clipper.AudioTracks, Tracks: body.Tracks}, nil
	default:
		return clipper.AudioSelection{}, fmt.Errorf("%w: audio mode %q is not one this editor offers",
			clipper.ErrInvalidRequest, body.AudioMode)
	}
}

// clipOutPath names the file the export will write.
func (s *Server) clipOutPath(tl clipTimeline, body clipRequestBody) string {
	ext := ".mkv"
	if strings.EqualFold(strings.TrimPrefix(body.Container, "."), "mp4") {
		ext = ".mp4"
	}
	base := strings.TrimSuffix(tl.anchor.Filename, filepath.Ext(tl.anchor.Filename))
	name := safeClipName(body.Title)
	if name == "" {
		name = safeClipName(base)
	}
	if name == "" {
		name = "clip"
	}
	// The in-point is part of the name so two clips out of one recording never
	// collide, and so a file found later still says where it came from.
	stamp := fmt.Sprintf("%06d", body.InMS/1000)
	return filepath.Join(s.clipExportDir(), name+"-"+stamp+ext)
}

func (s *Server) clipExportDir() string {
	return clipExportDirIn(s.eng().Recordings().Dir())
}

// clipExportDirIn resolves the exports directory to an ABSOLUTE path.
//
// It has to. config.DataDir defaults to "./data", so the recordings directory
// is relative on a stock install, and clipper.Request.Validate refuses a
// relative output path — deliberately, because the cutter is handed a path and
// has no idea what a legitimate directory is. Without this, planning a clip
// failed on every default deployment with "output path ... is not absolute",
// which is exactly what a browser check found.
//
// A failure to resolve falls back to the relative path rather than to an error:
// that leaves the caller with the message they would have got anyway instead of
// inventing a second way for the same thing to break.
func clipExportDirIn(recordingsDir string) string {
	dir := filepath.Join(recordingsDir, clipExportSubdir)
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// safeClipName reduces operator text to something that is safe as a filename on
// every platform this runs on, Windows included.
func safeClipName(s string) string {
	var b strings.Builder
	prev := false
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			prev = false
		default:
			// One separator per run, so "a   b" does not become "a---b".
			if !prev && b.Len() > 0 {
				b.WriteByte('-')
				prev = true
			}
		}
		if b.Len() >= maxClipTitleLen {
			break
		}
	}
	out := strings.Trim(b.String(), "-.")
	if !media.ValidRecordingName(out) {
		return ""
	}
	return out
}

func clipTitle(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxClipTitleLen {
		s = s[:maxClipTitleLen]
	}
	return s
}

func clipPlanViewOf(p clipper.Plan) clipPlanView {
	view := clipPlanView{
		Mode:             string(p.Mode),
		RequestedMode:    string(p.RequestedMode),
		RequestedInMS:    p.RequestedIn.Milliseconds(),
		RequestedOutMS:   p.RequestedOut.Milliseconds(),
		InMS:             p.In.Milliseconds(),
		OutMS:            p.Out.Milliseconds(),
		DurationMS:       p.Duration().Milliseconds(),
		InDriftMS:        p.InDrift.Milliseconds(),
		DriftKnown:       p.DriftKnown,
		KeyframeMS:       p.Keyframe.Milliseconds(),
		ReEncodedMS:      p.HeadDuration.Milliseconds(),
		LosslessFraction: p.LosslessFraction(),
		Segments:         len(p.Sources),
		Concat:           p.Concat,
		VideoEncoder:     p.VideoEncoder,
		OutName:          filepath.Base(p.OutPath),
		Describe:         p.Describe(),
		Warnings:         p.Warnings,
	}
	if view.Warnings == nil {
		view.Warnings = []string{}
	}
	return view
}

func clipModes() []string {
	modes := clipper.Modes()
	out := make([]string, 0, len(modes))
	for _, m := range modes {
		out = append(out, string(m))
	}
	return out
}

// handleClipExport queues the cut.
//
// It plans first, on the request, and stores the plan's warnings nowhere: the
// worker re-plans against the same timeline and logs them itself. Planning here
// is for the 400 — an impossible range must be refused while the user is still
// looking at the editor, not an hour later in a job's error field.
func (s *Server) handleClipExport(w http.ResponseWriter, r *http.Request) {
	// Checked before the plan, so a server with no queue says so instead of
	// spending two ffprobes to arrive at the same 503.
	if !s.requireJobs(w) {
		return
	}
	plan, tl, ok := s.planClip(w, r)
	if !ok {
		return
	}
	if err := os.MkdirAll(s.clipExportDir(), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create the exports directory: "+err.Error())
		return
	}

	cut, err := tl.timeline()
	if err != nil {
		s.writeClipError(w, err)
		return
	}
	params, err := json.Marshal(clipper.JobParams{
		Request: clipper.Request{
			In:           plan.RequestedIn,
			Out:          plan.RequestedOut,
			Mode:         plan.RequestedMode,
			Audio:        plan.Audio,
			OutPath:      plan.OutPath,
			Title:        plan.Title,
			VideoEncoder: plan.VideoEncoder,
		},
		// The segments travel with the job so a re-index between submission and
		// execution cannot change what gets cut out from under the user.
		Segments: cut.Segments(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	job, _, err := s.jobq.Submit(jobs.Job{
		Kind:   clipper.JobKind,
		Target: jobs.RecordingTarget(tl.anchor.ID),
		Params: params,
		// A human is watching this one, and it is short. Deliberately NOT
		// Unique: two different in-points out of one recording are two
		// different clips, and folding the second into the first would silently
		// throw away the export somebody just asked for.
		Priority: jobs.PriorityUser,
	})
	if err != nil {
		s.writeClipError(w, err)
		return
	}
	// The same jobView every other page reads, so the editor's progress panel
	// and the jobs table cannot disagree about what a job is.
	stats := s.jobq.Stats()
	writeJSON(w, http.StatusAccepted, s.view(*job, s.snapshot(), stats.Paused,
		stats.Running < s.concurrency(), s.recordingNames(), time.Now()))
}

// ---------------------------------------------------------------- transcript

// clipTranscriptSegment is one spoken line, in TIMELINE milliseconds rather
// than in its own recording's, so clicking it seeks to the right place on a
// timeline stitched from several files.
type clipTranscriptSegment struct {
	RecordingID int64  `json:"recordingId"`
	Track       int    `json:"track"`
	Speaker     string `json:"speaker,omitempty"`
	StartMS     int64  `json:"startMs"`
	EndMS       int64  `json:"endMs"`
	Text        string `json:"text"`
}

type clipTranscriptView struct {
	Segments []clipTranscriptSegment `json:"segments"`
	Speakers []string                `json:"speakers"`
	Tracks   []int                   `json:"tracks"`
}

// handleClipTranscript returns the free-diarization view of the timeline.
//
// Every microphone was recorded on its own track, so each segment already knows
// which track — and therefore which person — it came from, with no diarization
// model in the loop at all. That is what makes "find what I said, clip it" a
// text search rather than a listening exercise.
func (s *Server) handleClipTranscript(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	tl, err := s.clipTimeline(id)
	if err != nil {
		s.writeClipError(w, err)
		return
	}

	view := clipTranscriptView{Segments: []clipTranscriptSegment{}, Speakers: []string{}, Tracks: []int{}}
	seenSpeaker := map[string]bool{}
	seenTrack := map[int]bool{}
	for _, p := range tl.parts {
		tr, err := s.store.GetTranscript(p.rec.ID)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				continue
			}
			writeStoreError(w, err)
			return
		}
		if tr == nil {
			continue
		}
		for _, seg := range tr.Merged() {
			view.Segments = append(view.Segments, clipTranscriptSegment{
				RecordingID: p.rec.ID,
				Track:       seg.Track,
				Speaker:     seg.Speaker,
				StartMS:     p.startMS + seg.StartMS,
				EndMS:       p.startMS + seg.EndMS,
				Text:        seg.Text,
			})
			if seg.Speaker != "" && !seenSpeaker[seg.Speaker] {
				seenSpeaker[seg.Speaker] = true
				view.Speakers = append(view.Speakers, seg.Speaker)
			}
			if !seenTrack[seg.Track] {
				seenTrack[seg.Track] = true
				view.Tracks = append(view.Tracks, seg.Track)
			}
		}
	}
	sort.SliceStable(view.Segments, func(i, j int) bool {
		if view.Segments[i].StartMS == view.Segments[j].StartMS {
			return view.Segments[i].Track < view.Segments[j].Track
		}
		return view.Segments[i].StartMS < view.Segments[j].StartMS
	})
	sort.Ints(view.Tracks)
	writeJSON(w, http.StatusOK, view)
}

// handleDownloadClipExport hands over a finished clip.
//
// Keyed on the JOB rather than on a filename the client made up. The job is the
// only record of what was exported, its result already carries the path, and
// letting the browser name a file would put a second path guard in the product
// for no gain.
func (s *Server) handleDownloadClipExport(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	job, err := s.store.GetJob(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if job.Kind != clipper.JobKind || len(job.Result) == 0 {
		// Not "forbidden": this route only knows about finished clip exports,
		// and anything else is simply not here.
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var res clipper.JobResult
	if err := json.Unmarshal(job.Result, &res); err != nil || res.Path == "" {
		writeError(w, http.StatusNotFound, "that job produced no file")
		return
	}
	path, name, err := s.clipExportPath(res.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "that clip is not on disk")
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	http.ServeContent(w, r, name, stat.ModTime(), f)
}

// clipExportPath confines a stored result path to the exports directory.
//
// The path was written by this server, not by a client, so this is a belt on
// top of braces — but a job's params survive a database somebody edited, and
// the one thing this route must never do is serve an arbitrary file.
func (s *Server) clipExportPath(stored string) (path, name string, err error) {
	return clipPathIn(s.clipExportDir(), stored)
}

// clipPathIn is the guard itself, split out so it can be tested without a
// server or a filesystem.
func clipPathIn(dir, stored string) (path, name string, err error) {
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	full, err := filepath.Abs(stored)
	if err != nil {
		return "", "", err
	}
	if !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", "", errors.New("that clip is not in the exports directory")
	}
	return full, filepath.Base(full), nil
}

// --------------------------------------------------------------------- helpers

func clipQueryMS(r *http.Request, name string, fallback int64) int64 {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fallback
	}
	return v
}
