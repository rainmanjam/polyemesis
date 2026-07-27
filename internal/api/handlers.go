package api

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/metrics"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// -------------------------------------------------------------- setup & auth

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	has, err := s.store.HasUser()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"needsSetup":       !has,
		"minPasswordChars": db.MinPasswordLength,
	})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Username) == "" {
		req.Username = "admin"
	}

	// CreateUser refuses to run twice, so this endpoint cannot be used to take
	// over an existing install even though it is unauthenticated.
	user, err := s.store.CreateUser(req.Username, req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	token, err := s.sessions.Issue(user.ID, user.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.sessions.SetSession(w, r, token); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"username": user.Username})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Checked before the body is read and long before bcrypt runs: the point
	// of the throttle is that a guess must not cost us a password hash.
	ip := auth.ClientIP(r, s.cfg.TrustProxyHeaders)
	if wait := s.logins.Retry(ip); wait > 0 {
		s.log.Warn("throttled login", "remote", ip, "retryAfter", wait)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	user, err := s.store.GetUser()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// One message for both wrong-user and wrong-password: no username oracle.
	if user.Username != req.Username || !user.CheckPassword(req.Password) {
		wait := s.logins.Fail(ip)
		s.log.Warn("failed login", "username", req.Username, "remote", ip, "penalty", wait)
		writeError(w, http.StatusUnauthorized, "incorrect username or password")
		return
	}
	s.logins.Succeed(ip)

	token, err := s.sessions.Issue(user.ID, user.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.sessions.SetSession(w, r, token); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": user.Username})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.sessions.ClearSession(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed out"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if p.token == nil {
		writeJSON(w, http.StatusOK, map[string]any{"username": p.username, "auth": "session"})
		return
	}
	// A token acts as the admin, so report the admin's name rather than
	// leaving a scripted caller guessing whose install it is talking to.
	username := ""
	if u, err := s.store.GetUser(); err == nil {
		username = u.Username
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username": username, "auth": "token", "tokenName": p.token.Name,
	})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	user, err := s.store.GetUser()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !user.CheckPassword(req.Current) {
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	if err := s.store.SetPassword(user.ID, req.New); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Re-issue: a password change should refresh the session, not end it.
	token, err := s.sessions.Issue(user.ID, user.Username)
	if err == nil {
		_ = s.sessions.SetSession(w, r, token)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "password changed"})
}

// ------------------------------------------------------------------- system

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	settings := s.eng.Settings()
	spec := ffmpeg.IngestSpec{
		Kind:          ffmpeg.IngestKind(settings.Ingest.Mode),
		SRTPort:       settings.Ingest.SRT.Port,
		SRTPassphrase: settings.Ingest.SRT.Passphrase,
		SRTLatencyMS:  settings.Ingest.SRT.LatencyMS,
		RTMPPort:      settings.Ingest.RTMP.Port,
		RTMPApp:       settings.Ingest.RTMP.App,
		RTMPStreamKey: settings.Ingest.RTMP.StreamKey,
	}
	host := r.Host
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"version":    s.version,
		"ffmpeg":     s.eng.Tools(),
		"ingestUrl":  spec.PublicIngestURL(host),
		"ingestMode": settings.Ingest.Mode,
		"maxTracks":  routing.MaxTracks,
		"tlsEnabled": s.cfg.ServesTLS(),
		"dataDir":    s.cfg.DataDir,
		"uiBuilt":    UIBuilt(),
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.eng.Status())
}

func (s *Server) handleSource(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.eng.SourceInfo())
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"system":  s.eng.Monitor().System(),
		"bitrate": s.eng.Monitor().Bitrate(),
		"relay":   s.eng.Hub().Stats(),
	})
}

func (s *Server) handleLevels(w http.ResponseWriter, r *http.Request) {
	levels, at := s.eng.Levels()
	writeJSON(w, http.StatusOK, map[string]any{"levels": levels, "at": at})
}

// handleMetrics renders the Prometheus exposition.
//
// The route is authenticated; it is not open from loopback. Loopback is the
// wrong gate for the way this server is actually deployed: Prometheus usually
// runs in a neighbouring container, so its scrape never arrives from
// 127.0.0.1, and with TrustProxyHeaders on, every request arrives from a proxy
// that does. The check would be too strict and too lax at once. An API token
// is neither — it is issued and revoked from the tokens page like any other,
// and Prometheus sends one natively with `authorization` or `bearer_token_file`
// in the scrape config. A session cookie is accepted as well, so an admin who
// is already signed in can just open the URL.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	st := s.eng.Status()
	mon := s.eng.Monitor()

	snap := metrics.Snapshot{
		Version:      s.version,
		Uptime:       time.Since(s.startedAt),
		Destinations: make([]metrics.Destination, 0, len(st.Destinations)),
		Relay: metrics.Relay{
			Subscribers: len(st.Relay.Subscribers),
			RxPackets:   st.Relay.RxPackets,
			RxBytes:     st.Relay.RxBytes,
			TxPackets:   st.Relay.TxPackets,
			Dropped:     st.Relay.Dropped,
		},
	}

	// The relay's own rate, not the ingest process's -progress line: this is
	// the series the dashboard graphs, so the metric cannot disagree with what
	// an operator is looking at while they read it.
	if b := mon.Bitrate(); len(b) > 0 {
		snap.Ingest.BitrateKbps = b[len(b)-1].Kbps
	}
	snap.Ingest.State = string(supervisor.StateStopped)
	if st.Ingest != nil {
		snap.Ingest.State = string(st.Ingest.State)
		snap.Ingest.Restarts = st.Ingest.Restarts
	}

	for _, d := range st.Destinations {
		md := metrics.Destination{
			// A destination that is not running has no supervised process at
			// all; reporting it as stopped keeps its state series meaningful
			// instead of leaving every state at zero.
			Process:  metrics.Process{State: string(supervisor.StateStopped)},
			ID:       d.ID,
			Name:     d.Name,
			Kind:     string(d.Kind),
			Platform: string(d.Platform),
			Enabled:  d.Enabled,
		}
		if d.Process != nil {
			md.Process = metrics.Process{
				State:       string(d.Process.State),
				Restarts:    d.Process.Restarts,
				BitrateKbps: d.Process.Progress.BitrateKbps,
				DropFrames:  d.Process.Progress.DropFrames,
			}
		}
		snap.Destinations = append(snap.Destinations, md)
	}

	// A scrape reports what it can. Failing the whole endpoint because the
	// recordings volume is momentarily unreadable would also lose the ingest
	// and destination series, which are the ones an alert is watching.
	if u, err := s.eng.Recordings().Usage(); err == nil {
		snap.Recordings = metrics.Recordings{
			Files:      u.Count,
			UsedBytes:  u.UsedBytes,
			FreeBytes:  u.FreeBytes,
			TotalBytes: u.TotalBytes,
		}
	} else {
		s.log.Warn("metrics: recordings usage unavailable", "err", err)
	}

	sys := mon.System()
	snap.Host = metrics.Host{
		CPUPercent:     sys.CPUPercent,
		MemUsedBytes:   sys.MemUsedBytes,
		MemTotalBytes:  sys.MemTotalBytes,
		ProcCPUPercent: sys.ProcCPUPercent,
		ProcMemBytes:   sys.ProcMemBytes,
		NumCPU:         sys.NumCPU,
	}

	w.Header().Set("Content-Type", metrics.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, metrics.Render(snap))
}

// ----------------------------------------------------------------- settings

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	// Start from the stored settings so a partial payload cannot blank fields
	// the client did not send.
	settings, err := s.store.GetSettings()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !decodeJSON(w, r, &settings) {
		return
	}
	if err := settings.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.PutSettings(settings); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.eng.Reconcile(); err != nil {
		writeError(w, http.StatusInternalServerError, "settings saved but reconcile failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// ------------------------------------------------------------- destinations

func (s *Server) handleListDestinations(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListDestinations()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	src := s.eng.Source()

	// Each row is returned with its compiled routing, so the UI can render the
	// "Tracks 1, 2, 4 → stereo" summary and the generated filter string
	// without a second round trip.
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		item := map[string]any{"destination": row}
		if c, err := routing.Compile(row.Profile, src); err == nil {
			item["routing"] = c
		} else {
			item["routingError"] = err.Error()
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetDestination(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	row, err := s.store.GetDestination(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	resp := map[string]any{"destination": row}
	if c, err := routing.Compile(row.Profile, s.eng.Source()); err == nil {
		resp["routing"] = c
	} else {
		resp["routingError"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleCreateDestination(w http.ResponseWriter, r *http.Request) {
	var row db.Destination
	if !decodeJSON(w, r, &row) {
		return
	}
	row.ID = 0
	created, err := s.store.CreateDestination(&row)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.eng.Reconcile(); err != nil {
		s.log.Warn("reconcile after destination create", "err", err)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"destination": created})
}

func (s *Server) handleUpdateDestination(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := s.store.GetDestination(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Decode over the existing row so a client sending only a routing profile
	// does not wipe the URL.
	if !decodeJSON(w, r, existing) {
		return
	}
	existing.ID = id

	updated, err := s.store.UpdateDestination(existing)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Reconcile restarts only this destination, and only if the change
	// actually affects its command line.
	if err := s.eng.Reconcile(); err != nil {
		s.log.Warn("reconcile after destination update", "err", err)
	}

	resp := map[string]any{"destination": updated}
	if c, err := routing.Compile(updated.Profile, s.eng.Source()); err == nil {
		resp["routing"] = c
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleReorderDestinations persists dashboard order. It deliberately does not
// reconcile: position is display only and is not part of a destination's spec
// hash, so rearranging cards must never interrupt a live output.
func (s *Server) handleReorderDestinations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.store.ReorderDestinations(req.IDs); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := s.store.ListDestinations()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ids": ids})
}

func (s *Server) handleDeleteDestination(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteDestination(id); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.eng.Reconcile(); err != nil {
		s.log.Warn("reconcile after destination delete", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) setDestinationEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.SetDestinationEnabled(id, enabled); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.eng.Reconcile(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": enabled})
}

func (s *Server) handleStartDestination(w http.ResponseWriter, r *http.Request) {
	s.setDestinationEnabled(w, r, true)
}

func (s *Server) handleStopDestination(w http.ResponseWriter, r *http.Request) {
	s.setDestinationEnabled(w, r, false)
}

func (s *Server) handleRestartDestination(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.eng.RestartDestination(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarting"})
}

// -------------------------------------------------------------- routing API

func (s *Server) handleCompileRouting(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile routing.Profile `json:"profile"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Profile.ApplyDefaults()

	// This backs the live filter-string preview in the routing editor: the
	// user sees exactly what their checkboxes compile to, before saving.
	res, err := routing.Compile(req.Profile, s.eng.Source())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   err.Error(),
			"profile": req.Profile,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"routing": res, "profile": req.Profile})
}

func (s *Server) handleListPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"presets":  routing.Presets(),
		"defaults": routing.DefaultPresetOpts(),
	})
}

func (s *Server) handleApplyPreset(w http.ResponseWriter, r *http.Request) {
	var opts routing.PresetOpts
	// A body is optional: without one the OBS-convention defaults apply.
	if r.ContentLength > 0 && !decodeJSON(w, r, &opts) {
		return
	}
	profile, err := routing.ApplyPreset(chi.URLParam(r, "preset"), s.eng.Source(), opts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := routing.Compile(profile, s.eng.Source())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": profile, "routing": res})
}

// ---------------------------------------------------------------- recordings

func (s *Server) handleListRecordings(w http.ResponseWriter, r *http.Request) {
	recs, err := s.store.ListRecordings()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, recs)
}

func (s *Server) handleRecordingUsage(w http.ResponseWriter, r *http.Request) {
	usage, err := s.eng.Recordings().Usage()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func (s *Server) handleDeleteRecording(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.eng.Recordings().Delete(id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleDownloadRecording(w http.ResponseWriter, r *http.Request) {
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
	// Resolve confines the path to the recordings directory; the filename
	// originates from a database row and is never trusted as a path.
	path, err := s.eng.Recordings().Resolve(rec.Filename)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "recording file is missing from disk")
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "video/x-matroska")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", filepath.Base(rec.Filename)))
	// ServeContent gives us range requests, so a partially downloaded
	// multi-gigabyte segment can be resumed.
	http.ServeContent(w, r, rec.Filename, stat.ModTime(), f)
}

// ---------------------------------------------------------------- processes

func (s *Server) handleListProcesses(w http.ResponseWriter, r *http.Request) {
	procs := s.eng.Processes()
	out := make([]map[string]any, 0, len(procs))
	for _, p := range procs {
		out = append(out, map[string]any{
			"status":  p.Status(),
			"command": p.CommandString(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleProcessLogs(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	for _, p := range s.eng.Processes() {
		if p.Name() == name {
			writeJSON(w, http.StatusOK, map[string]any{
				"name":    name,
				"command": p.CommandString(),
				"lines":   p.Logs(),
			})
			return
		}
	}
	writeError(w, http.StatusNotFound, "no such process")
}

// ---------------------------------------------------------------------- HLS

func (s *Server) hlsHandler() http.Handler {
	dir := s.cfg.HLSDir()
	fs := http.FileServer(http.Dir(dir))
	return http.StripPrefix("/hls/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The playlist is rewritten every segment; caching it would freeze the
		// preview a few seconds after it starts.
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			// The encoder is on-demand, and playlist polling is what keeps it
			// alive. Only the playlist counts: a player can keep pulling
			// segments from a manifest it already holds, so segment requests
			// are not evidence anyone is still watching a live stream.
			//
			// A request that starts the encoder is answered 404 below, since
			// ffmpeg has not written the playlist yet. hls.js retries a failed
			// manifest load, so the player recovers within a segment or two.
			s.eng.PreviewRequested()
		}
		fs.ServeHTTP(w, r)
	}))
}
