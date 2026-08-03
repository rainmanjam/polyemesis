package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/media"
	"github.com/rainmanjam/polyemesis/internal/playlistmedia"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// The jobs API is the operator's view of the CPU tradeoff this whole tier is
// built around: heavy work is queued, the governor decides when it may have the
// machine, and the live stream wins every argument.
//
// The thing that makes this page usable rather than mystifying is the reason
// string. A queued job that is not running looks broken unless the UI can say
// WHY — "an ingest is live", "outside the scheduled window", "host cpu is above
// the ceiling (94%)" — so every listing carries one, assembled here from the
// queue's own state and the governor's last verdict rather than guessed at in
// TypeScript.

// jobsUnavailable is what every handler answers when no queue was wired. It is
// a 503 rather than a 404 because the route exists and the capability might
// come back on the next start; a client that sees 404 concludes the server is
// too old and stops asking.
const jobsUnavailable = "the background job queue is not running on this server"

// requireJobs guards a handler that cannot work without a queue.
func (s *Server) requireJobs(w http.ResponseWriter) bool {
	if s.jobq == nil {
		writeError(w, http.StatusServiceUnavailable, jobsUnavailable)
		return false
	}
	return true
}

// ------------------------------------------------------------- the job view

// jobView is a stored job plus the two things the queue does not persist:
// why it is not running, and roughly how long it has left.
type jobView struct {
	jobs.Job
	// RecordingID and Recording resolve the opaque Target for the UI, so a job
	// row can link to the segment it is about. Zero and empty for a target
	// that names something else.
	RecordingID int64  `json:"recordingId,omitempty"`
	Recording   string `json:"recording,omitempty"`

	// Blocked distinguishes "held back by the policy" from "just waiting its
	// turn". Both are queued; only one is worth explaining.
	Blocked bool `json:"blocked"`
	// Reason is the operator-facing sentence. Empty means nothing is holding
	// this job back beyond the ordinary queue.
	Reason string `json:"reason,omitempty"`
	// ETASeconds is a linear extrapolation from the progress reported so far.
	// Omitted until there is enough progress for the estimate to be worth
	// anything, because a wildly wrong ETA is worse than none.
	ETASeconds float64 `json:"etaSeconds,omitempty"`
	// Label is the human name of the kind, so the UI does not carry a second
	// copy of the catalogue.
	Label string `json:"label,omitempty"`
}

// jobKindInfo describes one registered kind and how it is currently governed.
// It is what the per-kind mode control on the jobs page is built from, and it
// is listed from the catalogue rather than from jobs that happen to exist:
// an operator must be able to set transcription to run at 02:00 before the
// first transcription has ever been queued.
type jobKindInfo struct {
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	Description string `json:"description"`
	// Mode, Windows, UsesGPU and IgnoreIngest are the EFFECTIVE policy — the
	// kind's own row where it has one, the default where it does not — so the
	// editor opens showing what is actually in force.
	Mode         string        `json:"mode"`
	Windows      []jobs.Window `json:"windows,omitempty"`
	UsesGPU      bool          `json:"usesGpu"`
	IgnoreIngest bool          `json:"ignoreIngest"`
	// Overridden reports whether this kind has a row of its own, so the UI can
	// offer "back to the default" without diffing the two.
	Overridden bool `json:"overridden"`
	// Available is whether the tool this kind needs was found on this machine.
	// It fails OPEN: a kind we cannot make a judgement about is available.
	Available bool `json:"available"`
	// Unavailable is the operator-facing reason, empty when available.
	Unavailable string `json:"unavailable,omitempty"`
}

// kindCatalogue names every kind this build can submit. It lives here rather
// than in each processor because it is a UI concern: the queue itself never
// interprets a kind, and a processor has no business owning the sentence shown
// beside its switch.
var kindCatalogue = []struct {
	Kind        jobs.Kind
	Label       string
	Description string
}{
	{transcribe.KindTranscribe, "Transcription",
		"Speech to text, one audio track at a time. Because each microphone is on its own track, this gives speaker attribution without a diarization model."},
	{media.KindProxy, "Proxy encode",
		"A small H.264/MP4 copy a browser can actually play. The MKV masters cannot be played in a browser; this is what the library's inline player uses."},
	{media.KindThumbnails, "Thumbnails",
		"Poster frame, contact sheet and the scrub sprites the timeline shows on hover."},
	{media.KindArchive, "Archive compression",
		"Re-encodes an old recording to HEVC to reclaim disk. Lossy, verified after the fact, and never applied to a recording younger than the configured age."},
	{clipper.JobKind, "Clip export",
		"A keyframe-accurate cut out of a finished recording, copied losslessly wherever the cut lands on a keyframe."},
	{playlistmedia.KindNormalise, "Playlist normalise",
		"Transcodes one uploaded playlist item to the single fixed profile every item must share. A playlist will not go on air until every one of its items has been through this."},
}

func kindLabel(k jobs.Kind) string {
	for _, c := range kindCatalogue {
		if c.Kind == k {
			return c.Label
		}
	}
	return string(k)
}

// ------------------------------------------------------------------ reasons

// blockReason explains why j is not running, in the order an operator would
// want to hear it: the first thing that would have to change.
//
// It returns blocked=false with an empty reason for anything that is running,
// finished, or simply next in line — saying "waiting" about a job that is about
// to start is noise, and noise here is what teaches people to ignore the column.
func blockReason(j jobs.Job, snap jobs.Snapshot, paused bool, slotsFree bool, now time.Time) (bool, string) {
	if j.State == jobs.StateRunning || j.State.Terminal() {
		return false, ""
	}

	// The queue-wide switch beats everything: nothing of any kind is starting.
	if paused {
		return true, "the queue is paused"
	}

	// A verdict against the kind is the interesting answer, and it is checked
	// before the timestamp because the timestamp is usually a CONSEQUENCE of
	// it: the governor parks a blocked job a few seconds ahead and renews the
	// parking every tick, so "available in 24s" is the symptom and "an ingest
	// is live" is the cause.
	if !snap.At.IsZero() && snap.Enabled {
		for _, v := range snap.Verdicts {
			if v.Kind != j.Kind || v.Start {
				continue
			}
			return true, decorateReason(v, snap.Gates)
		}
	}

	if j.AvailableAt.After(now) {
		wait := j.AvailableAt.Sub(now).Round(time.Second)
		// Attempts counts STARTS, so a job that has started before and is
		// waiting is waiting on retry backoff, not on the policy.
		if j.Attempts > 0 && j.State == jobs.StateQueued {
			return true, fmt.Sprintf("retrying in %s after %s", wait, attemptPhrase(j))
		}
		return true, "held back for another " + wait.String()
	}

	if !slotsFree {
		return true, "waiting for a free slot"
	}
	return false, ""
}

// decorateReason puts the measurement beside the verdict. "host cpu is above
// the ceiling" is an assertion; "host cpu is above the ceiling (94%)" is
// evidence, and evidence is what stops an operator disbelieving the gate.
func decorateReason(v jobs.Verdict, g jobs.Gates) string {
	switch v.Reason {
	case jobs.ReasonCPUBusy:
		if g.CPUPercent >= 0 {
			return fmt.Sprintf("%s (%.0f%%)", v.Reason, g.CPUPercent)
		}
	case jobs.ReasonOnBattery:
		if g.Power.Known && g.Power.Percent >= 0 {
			return fmt.Sprintf("%s (%.0f%%)", v.Reason, g.Power.Percent)
		}
	case jobs.ReasonTooHot:
		if g.Power.Known && g.Power.TempC > 0 {
			return fmt.Sprintf("%s (%.0f°C)", v.Reason, g.Power.TempC)
		}
	}
	return v.Reason
}

func attemptPhrase(j jobs.Job) string {
	if j.MaxAttempts > 0 {
		return fmt.Sprintf("attempt %d of %d", j.Attempts, j.MaxAttempts)
	}
	return fmt.Sprintf("attempt %d", j.Attempts)
}

// etaSeconds extrapolates linearly from the progress reported so far.
//
// Below a few per cent the extrapolation is nonsense — an encoder's first
// second includes opening the file — so it is withheld rather than guessed.
func etaSeconds(j jobs.Job, now time.Time) float64 {
	if j.State != jobs.StateRunning || j.StartedAt.IsZero() || j.Progress < 0.03 {
		return 0
	}
	elapsed := now.Sub(j.StartedAt).Seconds()
	if elapsed <= 0 {
		return 0
	}
	remaining := elapsed/j.Progress - elapsed
	if remaining <= 0 {
		return 0
	}
	return remaining
}

// view decorates one job. names maps recording IDs to filenames; a nil map
// simply leaves the name blank.
func (s *Server) view(j jobs.Job, snap jobs.Snapshot, paused, slotsFree bool, names map[int64]string, now time.Time) jobView {
	v := jobView{Job: j, Label: kindLabel(j.Kind)}
	if id, ok := jobs.ParseRecordingTarget(j.Target); ok {
		v.RecordingID = id
		v.Recording = names[id]
	}
	v.Blocked, v.Reason = blockReason(j, snap, paused, slotsFree, now)
	v.ETASeconds = etaSeconds(j, now)
	return v
}

// snapshot is the governor's last decision, or a zero Snapshot when there is no
// governor. Zero reads as "nothing was decided", which is the honest answer and
// makes blockReason skip the verdict arm entirely.
func (s *Server) snapshot() jobs.Snapshot {
	if s.gov == nil {
		return jobs.Snapshot{}
	}
	return s.gov.Last()
}

// recordingNames indexes the recordings table once so a listing of a hundred
// jobs does not become a hundred point lookups. A read failure costs the names,
// not the listing.
func (s *Server) recordingNames() map[int64]string {
	recs, err := s.store.ListRecordings()
	if err != nil {
		s.log.Warn("jobs: recording names unavailable", "err", err)
		return nil
	}
	out := make(map[int64]string, len(recs))
	for _, r := range recs {
		out[r.ID] = r.Filename
	}
	return out
}

// ------------------------------------------------------------------ listing

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	f, err := jobFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	list, err := s.jobq.List(f)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	stats := s.jobq.Stats()
	snap := s.snapshot()
	names := s.recordingNames()
	now := time.Now()
	free := stats.Running < s.concurrency()

	out := make([]jobView, 0, len(list))
	for _, j := range list {
		out = append(out, s.view(j, snap, stats.Paused, free, names, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jobs":   out,
		"stats":  stats,
		"paused": stats.Paused,
	})
}

// jobFilter reads the query string. An unknown state or a nonsense limit is a
// 400 rather than a silently empty list: "no jobs" and "your filter is
// misspelt" look identical in a table.
func jobFilter(r *http.Request) (jobs.Filter, error) {
	q := r.URL.Query()
	var f jobs.Filter

	for _, raw := range q["state"] {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			// "active" is the filter the page opens with, and spelling it out
			// on the client would mean two definitions of the same idea.
			if part == "active" {
				f.States = append(f.States, jobs.Active().States...)
				continue
			}
			st := jobs.State(part)
			if !st.Valid() {
				return f, fmt.Errorf("unknown job state %q", part)
			}
			f.States = append(f.States, st)
		}
	}
	for _, raw := range q["kind"] {
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				f.Kinds = append(f.Kinds, jobs.Kind(part))
			}
		}
	}
	if t := strings.TrimSpace(q.Get("target")); t != "" {
		f.Target = t
	}
	// recordingId is the spelling the UI has to hand; it is translated here so
	// there is exactly one place that knows how a recording target is spelt.
	if raw := q.Get("recordingId"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return f, errors.New("invalid recordingId")
		}
		f.Target = jobs.RecordingTarget(id)
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return f, errors.New("invalid limit")
		}
		f.Limit = n
	}
	return f, nil
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	j, err := s.jobq.Get(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	stats := s.jobq.Stats()
	writeJSON(w, http.StatusOK, s.view(*j, s.snapshot(), stats.Paused,
		stats.Running < s.concurrency(), s.recordingNames(), time.Now()))
}

// ----------------------------------------------------------------- overview

// jobsOverview is one request for everything the jobs page needs on load. It is
// one endpoint rather than five because the page is a status page: five polls
// would show five instants and the columns would disagree with each other.
type jobsOverview struct {
	// Available false means no queue is wired. The page then explains that
	// rather than rendering an empty table that reads as "no work to do".
	Available bool                `json:"available"`
	Paused    bool                `json:"paused"`
	Stats     jobs.Stats          `json:"stats"`
	Counts    map[string]int      `json:"counts"`
	Governor  *jobs.Snapshot      `json:"governor,omitempty"`
	Policy    db.PostProdSettings `json:"policy"`
	Kinds     []jobKindInfo       `json:"kinds"`
	Active    []jobView           `json:"active"`
	Recent    []jobView           `json:"recent"`
	Whisper   whisperInfo         `json:"whisper"`
}

// whisperInfo is what the transcription controls need to know about this
// machine. It is reported even when whisper.cpp is missing, because "install
// this and the button appears" is a far better answer than a disabled control
// with no explanation.
type whisperInfo struct {
	Available   bool     `json:"available"`
	Unavailable string   `json:"unavailable,omitempty"`
	Binary      string   `json:"binary,omitempty"`
	Version     string   `json:"version,omitempty"`
	Backends    []string `json:"backends,omitempty"`
	Backend     string   `json:"backend,omitempty"`
	// Models are the model files actually present on disk.
	Models []string `json:"models,omitempty"`
	// DefaultModel is what a transcribe job gets when the caller names none.
	// Chosen from the startup encoder probe's evidence about this machine
	// rather than guessed; see transcribe.DefaultModel.
	DefaultModel string `json:"defaultModel,omitempty"`
	// Realtime reports whether live captions are worth offering here. It fails
	// open: a machine we cannot judge is assumed capable.
	Realtime bool `json:"realtime"`
	// RealtimeNote is the honest speed/accuracy sentence for the default model.
	RealtimeNote string `json:"realtimeNote,omitempty"`
}

// recentJobLimit bounds the history the overview carries. Enough to see the
// last hour of work at a glance; the full history is a filtered list away.
const recentJobLimit = 50

func (s *Server) handleJobsOverview(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings()
	if err != nil {
		writeStoreError(w, err)
		return
	}

	out := jobsOverview{
		Available: s.jobq != nil,
		Policy:    settings.PostProd,
		Counts:    map[string]int{},
		Active:    []jobView{},
		Recent:    []jobView{},
		Whisper:   s.whisperInfo(),
	}
	out.Kinds = kindInfo(settings.PostProd, s.whisper)

	if s.jobq == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}

	stats := s.jobq.Stats()
	out.Stats = stats
	out.Paused = stats.Paused

	// One snapshot for the whole response. Sampling it twice would let the
	// gate panel and the per-job reasons disagree about the same instant,
	// which is exactly the confusion this page exists to remove.
	snap := s.snapshot()
	if !snap.At.IsZero() {
		out.Governor = &snap
	}
	if counts, err := s.store.JobCounts(); err == nil {
		for st, n := range counts {
			out.Counts[string(st)] = n
		}
	} else {
		s.log.Warn("jobs: counts unavailable", "err", err)
	}

	names := s.recordingNames()
	now := time.Now()
	free := stats.Running < s.concurrency()

	if active, err := s.jobq.List(jobs.Active()); err == nil {
		for _, j := range active {
			out.Active = append(out.Active, s.view(j, snap, stats.Paused, free, names, now))
		}
	} else {
		writeStoreError(w, err)
		return
	}
	recent, err := s.jobq.List(jobs.Filter{
		States: []jobs.State{jobs.StateDone, jobs.StateFailed, jobs.StateCancelled},
		Limit:  recentJobLimit,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	for _, j := range recent {
		out.Recent = append(out.Recent, s.view(j, snap, stats.Paused, free, names, now))
	}
	writeJSON(w, http.StatusOK, out)
}

// kindInfo resolves the effective policy for every catalogued kind.
func kindInfo(p db.PostProdSettings, whisper *transcribe.Tools) []jobKindInfo {
	pol := p.Policy()
	overridden := make(map[jobs.Kind]bool, len(p.Kinds))
	for _, k := range p.Kinds {
		overridden[jobs.Kind(strings.TrimSpace(k.Kind))] = true
	}

	out := make([]jobKindInfo, 0, len(kindCatalogue))
	for _, c := range kindCatalogue {
		kp := pol.For(c.Kind)
		info := jobKindInfo{
			Kind:         string(c.Kind),
			Label:        c.Label,
			Description:  c.Description,
			Mode:         string(kp.Mode),
			Windows:      kp.Windows,
			UsesGPU:      kp.UsesGPU,
			IgnoreIngest: kp.IgnoreIngest,
			Overridden:   overridden[c.Kind],
			// Fails open. Only transcription has an optional external tool we
			// can actually check for; everything else needs FFmpeg, which the
			// server would not have started without.
			Available: true,
		}
		if c.Kind == transcribe.KindTranscribe && !whisper.Available() {
			info.Available = false
			info.Unavailable = whisper.Unavailable()
		}
		out = append(out, info)
	}
	return out
}

func (s *Server) whisperInfo() whisperInfo {
	// Every method on *transcribe.Tools is nil-receiver safe, which is what
	// lets this run unguarded on a machine without whisper.cpp.
	info := whisperInfo{
		Available:   s.whisper.Available(),
		Unavailable: s.whisper.Unavailable(),
	}
	if s.whisper != nil {
		info.Binary = s.whisper.Binary
		info.Version = s.whisper.Version
		for _, b := range s.whisper.AvailableBackends() {
			info.Backends = append(info.Backends, string(b))
		}
		info.Backend = string(s.whisper.BestBackend())
	}

	hint := transcribe.HintFromTools(s.eng().Tools())
	model := transcribe.DefaultModel(hint)
	info.DefaultModel = model.Name
	realtime, note := transcribe.RealtimeCapable(model, hint)
	info.Realtime, info.RealtimeNote = realtime, note

	// A missing models directory is not an error: nothing has been downloaded
	// yet, which is the state every fresh install starts in.
	if installed, err := transcribe.InstalledModels(transcribe.ModelsDir(s.cfg.DataDir)); err == nil {
		for _, m := range installed {
			info.Models = append(info.Models, m.Name)
		}
		sort.Strings(info.Models)
	}
	return info
}

// concurrency is how many jobs may run at once, read from the stored settings
// so the "waiting for a free slot" explanation matches the number the operator
// set. A read failure falls back to the queue's own default rather than to
// zero, which would make every waiting job claim the queue was full.
func (s *Server) concurrency() int {
	settings, err := s.store.GetSettings()
	if err != nil || settings.PostProd.Concurrency < 1 {
		return jobs.DefaultConcurrency
	}
	return settings.PostProd.Concurrency
}

// ------------------------------------------------------------------- policy

func (s *Server) handleGetJobPolicy(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"policy": settings.PostProd,
		"kinds":  kindInfo(settings.PostProd, s.whisper),
		"modes":  []jobs.Mode{jobs.ModeRealtime, jobs.ModeDeferred, jobs.ModeScheduled, jobs.ModeManual},
	})
}

// handlePutJobPolicy replaces the post-production block of the settings.
//
// It writes through the settings store rather than only into the live governor,
// because a policy that a restart forgets is a policy the operator will be
// bitten by exactly once. Engine.Reconcile is deliberately NOT called: nothing
// in this block changes an FFmpeg command line, and restarting a live encoder
// to change a nice level would be the precise mistake this tier exists to
// avoid.
func (s *Server) handlePutJobPolicy(w http.ResponseWriter, r *http.Request) {
	// Buffered before the store's settings mutex is taken, for the reason
	// readJSONBody exists: the decode below runs inside that lock.
	body, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	// Read, replace and write in one span inside db.UpdateSettings. This block
	// is a slice of the same JSON document PUT /settings and the scheduler write
	// -- there is no way to store only the post-production fields -- so a
	// read-modify-write of its own would discard whatever either of those had
	// just changed, along with every field it never looked at.
	var before int
	settings, err := s.store.UpdateSettings(func(settings *db.Settings) error {
		before = settings.PostProd.Concurrency

		var req db.PostProdSettings
		if !decodeJSONFrom(w, body, &req) {
			return errResponseWritten
		}
		settings.PostProd = req
		return nil
	})
	var invalid db.InvalidSettingsError
	switch {
	case errors.Is(err, errResponseWritten):
		return
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, invalid.Error())
		return
	case err != nil:
		writeStoreError(w, err)
		return
	}
	if s.gov != nil {
		s.gov.SetPolicy(settings.PostProd.Policy())
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"policy": settings.PostProd,
		"kinds":  kindInfo(settings.PostProd, s.whisper),
		// The queue's concurrency is fixed when it is constructed, so a change
		// to it is saved but not live. Said out loud rather than left for the
		// operator to discover by watching the wrong number of jobs run.
		"restartRequired": settings.PostProd.Concurrency != before,
	})
}

// ----------------------------------------------------------------- controls

func (s *Server) handlePauseJobs(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	s.jobq.Pause()
	writeJSON(w, http.StatusOK, map[string]any{"paused": true})
}

func (s *Server) handleResumeJobs(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	s.jobq.Resume()
	writeJSON(w, http.StatusOK, map[string]any{"paused": false})
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	s.jobAction(w, r, func(id int64) error { return s.jobq.Cancel(id) })
}

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	s.jobAction(w, r, func(id int64) error { return s.jobq.Retry(id) })
}

// handleReleaseJob asks the governor to stop gating ONE job.
//
// Deliberately per-job rather than per-kind: "run this one now, I know what I
// am doing" must not become "and every transcription from now on", which is
// what changing the mode would mean.
func (s *Server) handleReleaseJob(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if s.gov == nil {
		// No governor means nothing is gating it, so the request has already
		// got what it asked for. Answering 503 here would be the restrictive
		// mistake: the job is free to run.
		writeJSON(w, http.StatusOK, map[string]any{"released": true, "governed": false})
		return
	}
	s.gov.Release(id)
	writeJSON(w, http.StatusOK, map[string]any{"released": true, "governed": true})
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	s.jobAction(w, r, func(id int64) error { return s.jobq.Delete(id) })
}

// jobAction is the shared shape of every single-job control: parse, act, hand
// back the job as it now stands so the table does not need a second request.
func (s *Server) jobAction(w http.ResponseWriter, r *http.Request, act func(int64) error) {
	if !s.requireJobs(w) {
		return
	}
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := act(id); err != nil {
		writeStoreError(w, err)
		return
	}
	j, err := s.jobq.Get(id)
	if err != nil {
		// Delete is the case that lands here, and it succeeded.
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	stats := s.jobq.Stats()
	writeJSON(w, http.StatusOK, s.view(*j, s.snapshot(), stats.Paused,
		stats.Running < s.concurrency(), s.recordingNames(), time.Now()))
}

// handlePurgeJobs drops finished history. Defaults come from the stored
// retention settings so the button does what the settings page promised.
func (s *Server) handlePurgeJobs(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	settings, err := s.store.GetSettings()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	req := struct {
		Days *int `json:"days,omitempty"`
		Keep *int `json:"keep,omitempty"`
	}{}
	if !decodeJSON(w, r, &req) {
		return
	}
	days := settings.PostProd.RetainDays
	if req.Days != nil {
		days = *req.Days
	}
	keep := settings.PostProd.RetainJobs
	if req.Keep != nil {
		keep = *req.Keep
	}
	if days < 0 || keep < 0 {
		writeError(w, http.StatusBadRequest, "retention values cannot be negative")
		return
	}

	// Zero days means "keep forever", and the honest way to express that to a
	// cutoff-based purge is a cutoff nothing is older than.
	cutoff := time.Time{}
	if days > 0 {
		cutoff = time.Now().AddDate(0, 0, -days)
	}
	n, err := s.jobq.Purge(cutoff, keep)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"purged": n})
}
