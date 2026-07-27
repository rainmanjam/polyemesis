// Package api is the HTTP surface: a JSON REST API, a WebSocket for live
// telemetry, and the embedded single-page UI, all on one listener.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/secrets"
	"github.com/rainmanjam/polyemesis/internal/tlsx"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
	"github.com/rainmanjam/polyemesis/internal/web"
)

// eng is the engine an unscoped request operates on: the default source's.
//
// Every endpoint that predates sources goes through here, which is what keeps
// the whole existing API and UI working while multi-source runs underneath.
//
// It cannot be nil in a running server: the database refuses to delete the last
// source, and Manager.Start fails when no engine came up, so the process never
// finishes booting without one. A handler reached before Start would panic,
// which is the correct outcome for a bug that means the server is serving
// before its pipeline exists.
func (s *Server) eng() *engine.Engine { return s.mgr.Default() }

// Server wires the HTTP layer to everything else.
type Server struct {
	log      *slog.Logger
	cfg      config.Config
	store    *db.DB
	box      *secrets.Box
	mgr      *engine.Manager
	bus      *events.Broker
	sessions *auth.Manager
	logins   *auth.Throttle
	// tls is the same Provider the listener is serving from, so the status
	// card describes the certificate actually in use rather than re-deriving
	// one — a second Provider in selfsigned mode would rewrite the material
	// on disk out from under the running listener. Nil is tolerated: the
	// status endpoint then reports the configured intent only.
	tls     *tlsx.Provider
	version string
	// startedAt is the process start, which is what the uptime metric reports.
	startedAt time.Time

	// The post-production tier. Every one of these is optional and every
	// handler that reads one checks first, because a build that has not wired
	// the queue yet must still serve the console — the library's search and
	// session views work perfectly well without a job ever running.
	jobq    *jobs.Queue
	gov     *jobs.Governor
	whisper *transcribe.Tools

	// chat is the unified cross-platform chat fan-in. Optional like everything
	// above it: a build with no chat wired serves the pane read-only from the
	// stored scrollback rather than hiding it.
	chat *chat.Hub
}

// Options configures the server.
type Options struct {
	Log     *slog.Logger
	Config  config.Config
	DB      *db.DB
	Secrets *secrets.Box
	Engine  *engine.Manager
	Events  *events.Broker
	Version string
	// TLS is the provider the HTTP listener was built from. Optional; without
	// it the TLS status endpoint can only report what config.yaml asked for.
	TLS *tlsx.Provider

	// Jobs is the background work queue. Optional: without it the jobs page
	// reports that no queue is running and the library hides its submit
	// buttons rather than offering work nothing will pick up.
	Jobs *jobs.Queue
	// Governor is the resource policy. Optional even when Jobs is set, and the
	// absence is reported as "not governed" rather than as "allowed": an
	// operator who cannot see a gate must not be told there is one.
	Governor *jobs.Governor
	// Whisper is the startup detection of whisper.cpp. A nil pointer is a
	// perfectly ordinary machine without it installed; every method on Tools
	// is nil-receiver safe for exactly this reason.
	Whisper *transcribe.Tools
	// Chat is the chat Hub. Optional: without it the chat page reports that no
	// platform is connected and still shows the stored history.
	Chat *chat.Hub
}

// New creates the server.
func New(o Options) *Server {
	return &Server{
		log:       o.Log,
		cfg:       o.Config,
		store:     o.DB,
		box:       o.Secrets,
		mgr:       o.Engine,
		bus:       o.Events,
		tls:       o.TLS,
		jobq:      o.Jobs,
		gov:       o.Governor,
		whisper:   o.Whisper,
		chat:      o.Chat,
		version:   o.Version,
		startedAt: time.Now(),
		logins:    auth.NewThrottle(),
		sessions: auth.New(
			o.Secrets.Derive("session-jwt"),
			// ServesTLS, not the legacy tls.enabled: an install that writes
			// tls.mode explicitly leaves Enabled false, and reading it here
			// would drop the Secure flag from the session cookie on a server
			// that is genuinely terminating TLS.
			o.Config.ServesTLS(),
			o.Config.TrustProxyHeaders,
		),
	}
}

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(s.requestLogger)
	// Applies to the embedded UI and the HLS preview as well as the API, which
	// is the point: the CSP only protects the pages the browser renders.
	r.Use(securityHeaders(s.cfg.ResolvedTLSMode(), s.cfg.TLS.HSTS))

	r.Route("/api/v1", func(r chi.Router) {
		// --- unauthenticated ---
		r.Get("/setup", s.handleSetupStatus)
		r.Post("/setup", s.handleSetup)
		r.Post("/auth/login", s.handleLogin)
		r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		})
		// Sessionless on purpose: in selfsigned mode the browser will not let
		// the user reach the login form until this CA is installed. See
		// handleDownloadCA for the full argument.
		r.Get("/tls/ca", s.handleDownloadCA)

		// The public player's two reads. Unauthenticated by necessity — a
		// viewer has no account — but not unguarded: both run the playout
		// access check, which refuses unless playout is enabled, public, and
		// either open or presented with the playback token. See playout.go.
		r.Get("/playout/public", s.handlePlayoutPublic)
		r.Get("/playout/poster.jpg", s.handlePlayoutPoster)

		// --- authenticated ---
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Use(s.requireCSRF)

			r.Post("/auth/logout", s.handleLogout)
			r.Get("/auth/me", s.handleMe)
			r.Post("/auth/password", s.handleChangePassword)

			r.Get("/auth/tokens", s.handleListAPITokens)
			r.Post("/auth/tokens", s.handleCreateAPIToken)
			r.Delete("/auth/tokens/{id}", s.handleRevokeAPIToken)

			r.Get("/system", s.handleSystem)
			// Authenticated on purpose: the build number is a fingerprint, and
			// an unauthenticated scanner should not get to read it. The check
			// is a POST so requireCSRF covers it and no prefetching browser
			// can reach out to GitHub on the operator's behalf.
			r.Get("/version", s.handleVersion)
			r.Post("/version/check", s.handleCheckUpdate)
			r.Get("/status", s.handleStatus)
			r.Get("/source", s.handleSource)
			// What each incoming track is. Per-ingest, not per-destination:
			// the feed is the same feed whoever is listening to it.
			r.Put("/source/annotations", s.handlePutAnnotations)
			// Manual failover. The tier's return mode defaults to manual, so
			// this is how a broadcast leaves a slate.
			r.Post("/failover/source", s.handleSwitchSource)
			r.Get("/stats", s.handleStats)
			r.Get("/levels", s.handleLevels)

			r.Get("/settings", s.handleGetSettings)
			r.Put("/settings", s.handlePutSettings)

			r.Get("/tls", s.handleTLSStatus)

			r.Get("/destinations", s.handleListDestinations)
			r.Post("/destinations", s.handleCreateDestination)
			// chi matches the static segment ahead of {id}, so this does not
			// collide with the destination routes below.
			r.Put("/destinations/order", s.handleReorderDestinations)
			r.Get("/destinations/{id}", s.handleGetDestination)
			r.Put("/destinations/{id}", s.handleUpdateDestination)
			r.Delete("/destinations/{id}", s.handleDeleteDestination)
			r.Post("/destinations/{id}/start", s.handleStartDestination)
			r.Post("/destinations/{id}/stop", s.handleStopDestination)
			r.Post("/destinations/{id}/restart", s.handleRestartDestination)
			r.Post("/destinations/{id}/refresh-key", s.handleRefreshKey)

			// Expert mode: hand-edited FFmpeg arguments for one destination.
			// Preview and dry-run are POSTs because they carry a candidate
			// edit in the body, not because they change anything — neither
			// writes. See expert.go.
			r.Get("/destinations/{id}/expert", s.handleGetExpert)
			r.Put("/destinations/{id}/expert", s.handlePutExpert)
			r.Delete("/destinations/{id}/expert", s.handleDeleteExpert)
			r.Post("/destinations/{id}/expert/preview", s.handlePreviewExpert)
			r.Post("/destinations/{id}/expert/dry-run", s.handleDryRunExpert)

			r.Get("/renditions", s.handleListRenditions)
			r.Post("/renditions", s.handleCreateRendition)
			// Static segment first, same as /destinations/order above.
			r.Get("/renditions/presets", s.handleRenditionPresets)
			r.Get("/renditions/{id}", s.handleGetRendition)
			r.Put("/renditions/{id}", s.handleUpdateRendition)
			r.Delete("/renditions/{id}", s.handleDeleteRendition)
			r.Post("/renditions/{id}/restart", s.handleRestartRendition)

			// Which encoders this FFmpeg actually registers, so the rendition
			// editor cannot offer one that would only fail once a stream is live.
			r.Get("/encoders", s.handleListEncoders)

			r.Post("/routing/compile", s.handleCompileRouting)
			r.Get("/routing/presets", s.handleListPresets)
			r.Post("/routing/presets/{preset}", s.handleApplyPreset)

			// Playout administration. The media itself is served outside this
			// group; only the configuration and the operator's view of it are
			// behind a session.
			r.Get("/playout", s.handleGetPlayout)
			r.Put("/playout/publish", s.handlePutPlayoutPublish)
			r.Post("/playout/token", s.handleRotatePlayoutToken)
			r.Post("/playout/analytics/reset", s.handleResetPlayoutAnalytics)

			r.Get("/recordings", s.handleListRecordings)
			r.Get("/recordings/usage", s.handleRecordingUsage)
			// Stems live on disk beside the masters rather than in the index,
			// so they are listed by name; the static segment beats {id} the
			// same way /destinations/order does.
			r.Get("/recordings/stems", s.handleListStems)
			r.Delete("/recordings/{id}", s.handleDeleteRecording)

			// Background work. Every heavy post-production task is a queued
			// job governed by a resource policy that yields to the stream, so
			// this is where an operator sees the CPU tradeoff and changes it.
			// Static segments precede {id} for the reason /destinations/order
			// does. See jobs.go.
			r.Get("/jobs", s.handleListJobs)
			r.Get("/jobs/overview", s.handleJobsOverview)
			r.Get("/jobs/policy", s.handleGetJobPolicy)
			r.Put("/jobs/policy", s.handlePutJobPolicy)
			r.Post("/jobs/pause", s.handlePauseJobs)
			r.Post("/jobs/resume", s.handleResumeJobs)
			r.Post("/jobs/purge", s.handlePurgeJobs)
			r.Get("/jobs/{id}", s.handleGetJob)
			r.Delete("/jobs/{id}", s.handleDeleteJob)
			r.Post("/jobs/{id}/cancel", s.handleCancelJob)
			r.Post("/jobs/{id}/retry", s.handleRetryJob)
			// "Run it anyway": releases one job from the governor's gates
			// without changing the policy for every other job of its kind.
			r.Post("/jobs/{id}/release", s.handleReleaseJob)

			// The media library: sessions, transcripts and the full-text
			// search over them. See library.go.
			r.Get("/library", s.handleLibrary)
			r.Get("/library/search", s.handleSearchTranscripts)
			r.Post("/library/sessions", s.handleCreateLibrarySession)
			r.Post("/library/sessions/regroup", s.handleRegroupSessions)
			r.Get("/library/sessions/{id}", s.handleGetLibrarySession)
			r.Put("/library/sessions/{id}", s.handleUpdateLibrarySession)
			r.Delete("/library/sessions/{id}", s.handleDeleteLibrarySession)
			r.Get("/library/recordings/{id}", s.handleGetLibraryRecording)
			r.Put("/library/recordings/{id}", s.handleUpdateLibraryRecording)
			r.Get("/library/recordings/{id}/transcript", s.handleGetTranscript)
			r.Delete("/library/recordings/{id}/transcript", s.handleDeleteTranscript)
			r.Put("/library/recordings/{id}/speaker", s.handleSetTranscriptSpeaker)
			// Submitting the work the library page offers. One route per
			// kind's name rather than a body field, so an unknown kind is a
			// 404 from the router instead of a validation error.
			r.Post("/library/recordings/{id}/jobs/{kind}", s.handleSubmitRecordingJob)
			// Derived media: the proxy the inline player uses, the poster, the
			// contact sheet, the scrub sprites. A GET, so requireCSRF passes
			// it through and a plain <video src> can reach it — which it has
			// to, because a media element attaches no headers of its own.
			r.Get("/library/recordings/{id}/media/{file}", s.handleLibraryMedia)

			// The clip editor: keyframe-accurate lossless cuts out of a
			// recording already on disk. Deliberately NOT under /clips, which
			// is the live ring buffer and a different feature entirely; see
			// the file comment in clips.go. Planning is a POST because it
			// carries a candidate cut in the body, not because it writes —
			// nothing here runs FFmpeg, the export is a queued job.
			r.Get("/clipper/recordings/{id}", s.handleClipSource)
			r.Get("/clipper/recordings/{id}/keyframes", s.handleClipKeyframes)
			r.Get("/clipper/recordings/{id}/transcript", s.handleClipTranscript)
			r.Post("/clipper/recordings/{id}/plan", s.handleClipPlan)
			r.Post("/clipper/recordings/{id}/export", s.handleClipExport)

			// Alerting: webhook rules and the test button that is the only
			// reason anybody believes a webhook works.
			r.Get("/alerts/meta", s.handleAlertsMeta)
			r.Get("/alerts/rules", s.handleListAlertRules)
			r.Post("/alerts/rules", s.handleCreateAlertRule)
			r.Get("/alerts/rules/{id}", s.handleGetAlertRule)
			r.Put("/alerts/rules/{id}", s.handleUpdateAlertRule)
			r.Delete("/alerts/rules/{id}", s.handleDeleteAlertRule)
			r.Post("/alerts/rules/{id}/test", s.handleTestAlertRule)

			// Scheduling. /runs is what the last sweep did, which is where a
			// skipped occurrence gets explained.
			r.Get("/schedules", s.handleListSchedules)
			r.Post("/schedules", s.handleCreateSchedule)
			r.Get("/schedules/runs", s.handleScheduleRuns)
			r.Get("/schedules/{id}", s.handleGetSchedule)
			r.Put("/schedules/{id}", s.handleUpdateSchedule)
			r.Delete("/schedules/{id}", s.handleDeleteSchedule)

			// The rolling clip buffer.
			r.Get("/clips", s.handleListClips)
			r.Post("/clips", s.handleCaptureClip)
			r.Put("/clips/buffer", s.handleSetClipBuffer)
			r.Delete("/clips/{name}", s.handleDeleteClip)

			// Loudness compliance, read by the meters page.
			r.Get("/loudness", s.handleLoudness)
			r.Put("/loudness", s.handleSetLoudnessMonitor)

			r.Get("/processes", s.handleListProcesses)
			r.Get("/processes/{name}/logs", s.handleProcessLogs)

			r.Get("/platforms/presets", s.handlePlatformPresets)
			r.Get("/platforms/capabilities", s.handlePlatformCapabilities)
			r.Get("/platforms/guides", s.handlePlatformGuides)
			r.Get("/platforms/credentials", s.handleListCreds)
			r.Put("/platforms/credentials/{platform}", s.handlePutCreds)
			r.Delete("/platforms/credentials/{platform}", s.handleDeleteCreds)
			r.Get("/platforms/accounts", s.handleListAccounts)
			r.Delete("/platforms/accounts/{id}", s.handleDeleteAccount)
			// Live viewer count, for the platforms that publish one. Absent is
			// a 200 saying so rather than a 404; see handleAccountStats.
			r.Get("/platforms/accounts/{id}/stats", s.handleAccountStats)

			// Go-live metadata. The push is a job rather than a request so a
			// slow platform API cannot hold the dashboard open; the composer
			// polls the job for per-platform results. See metadata.go.
			r.Get("/metadata", s.handleMetadataOverview)
			r.Post("/metadata/push", s.handlePushMetadata)
			r.Get("/metadata/push/{id}", s.handleMetadataJob)

			// Unified chat. Live messages arrive on the WebSocket, not here;
			// these four are the scrollback a freshly opened pane needs plus
			// the three things a socket cannot do. Deletion is addressed by
			// query parameters because a platform-issued message id does not
			// survive a path segment. See chat.go.
			r.Get("/chat", s.handleChatOverview)
			r.Get("/chat/messages", s.handleChatMessages)
			r.Delete("/chat/messages", s.handleChatDeleteMessage)
			r.Post("/chat/send", s.handleChatSend)
		})

		// A scraper has no CSRF token to double-submit, and this route is a
		// read-only GET, so requireCSRF is skipped. It is still authenticated;
		// handleMetrics says why.
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/metrics", s.handleMetrics)
		})

		// OAuth redirects are top-level browser navigations, so they carry the
		// session cookie but cannot carry a CSRF header. The OAuth `state`
		// parameter is the CSRF defence for these two routes.
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/oauth/{platform}/start", s.handleOAuthStart)
			r.Get("/oauth/{platform}/callback", s.handleOAuthCallback)
		})

		// Kick's chat webhook. Outside requireAuth because Kick's servers post
		// here with no session and no header polyemesis issued; the
		// unguessable path segment is the whole credential, and the handler
		// compares it before doing anything else. See chat_wiring.go.
		r.HandleFunc("/chat/kick/{secret}", s.handleKickChatWebhook)

		// The WebSocket does its own auth; the browser cannot set headers on
		// the upgrade request, so CSRF middleware would reject it.
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/ws", s.handleWS)
		})

		// Downloads are top-level navigations for the same reason.
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/recordings/{id}/download", s.handleDownloadRecording)
			r.Get("/recordings/stems/{name}/download", s.handleDownloadStem)
			// Addressed by the JOB rather than by a filename: the export's
			// path was written by the server and is never handed to a client
			// to spell back at us.
			r.Get("/clipper/jobs/{id}/download", s.handleDownloadClipExport)
			r.Get("/clips/{name}/download", s.handleDownloadClip)
		})
	})

	// Preview playlist and segments. hls.js cannot attach headers, so this
	// relies on the session cookie, same-origin.
	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		r.Handle("/hls/*", s.hlsHandler())
	})

	// --- the public origin ---
	//
	// Registered OUTSIDE every authenticated group, which is the whole point:
	// a viewer has no session and never will. requireAuth is deliberately
	// absent, and the guard is instead per-request inside the handlers, because
	// "is this stream public" is a setting an operator flips at runtime and a
	// route table is built once at startup. Both handlers refuse unless playout
	// is explicitly enabled; see the access rules at the top of playout.go.
	r.Handle(PlayoutPrefix+"*", s.playoutHandler())
	// The player page. It resolves to the same SPA bundle as the admin UI but
	// through its own route, so the frame-blocking headers can be relaxed for
	// an embed without relaxing them for the console.
	watch := s.watchHandler()
	r.Handle(WatchPath, watch)
	r.Handle(WatchPath+"/*", watch)

	if h, err := web.Handler(); err == nil {
		r.NotFound(h.ServeHTTP)
	} else {
		s.log.Error("embedded UI unavailable", "err", err)
	}
	return r
}

// UIBuilt reports whether a real UI is embedded, so main can warn on startup
// rather than letting the user discover a placeholder page.
func UIBuilt() bool { return web.Built() }

// ---------------------------------------------------------------- middleware

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		// Health checks, metric scrapes and the stats poll would otherwise
		// drown the log.
		if r.URL.Path == "/api/v1/health" || r.URL.Path == "/api/v1/metrics" ||
			strings.HasPrefix(r.URL.Path, "/hls/") ||
			// A playout viewer polls a playlist every segment; a busy origin
			// would fill the log with nothing but segment reads.
			strings.HasPrefix(r.URL.Path, PlayoutPrefix) {
			return
		}
		level := slog.LevelDebug
		if ww.Status() >= 500 {
			level = slog.LevelError
		} else if ww.Status() >= 400 {
			level = slog.LevelWarn
		}
		s.log.Log(r.Context(), level, "http",
			"method", r.Method, "path", r.URL.Path,
			"status", ww.Status(), "dur", time.Since(start).Round(time.Millisecond))
	})
}

// principal is who the current request is acting as. Both routes lead to the
// same single administrator; what differs is how the claim was made, which is
// what the CSRF and token-management rules key off.
type principal struct {
	// username is the signed-in account, empty for Bearer authentication.
	username string
	// token is set only for Bearer authentication.
	token *db.APIToken
}

type ctxKey int

const principalKey ctxKey = iota

func principalFrom(ctx context.Context) (*principal, bool) {
	p, ok := ctx.Value(principalKey).(*principal)
	return p, ok
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := s.authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
	})
}

// authenticate resolves a Bearer token or a session cookie, in that order: a
// client that bothered to send a token meant to use it, and silently falling
// back to an ambient cookie would make a revoked token look like it still
// works.
func (s *Server) authenticate(r *http.Request) (*principal, error) {
	if raw := auth.BearerToken(r); raw != "" {
		tok, err := s.store.LookupAPIToken(raw)
		if err != nil {
			return nil, auth.ErrUnauthorized
		}
		return &principal{token: tok}, nil
	}
	claims, err := s.sessions.FromRequest(r)
	if err != nil {
		return nil, err
	}
	return &principal{username: claims.Username}, nil
}

func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CSRF exists because the browser attaches the session cookie on its
		// own. Nothing attaches an Authorization header on its own, so a
		// token-authenticated request is not forgeable cross-site and the
		// double-submit token has nothing left to protect.
		if p, ok := principalFrom(r.Context()); ok && p.token != nil {
			next.ServeHTTP(w, r)
			return
		}
		if err := auth.CheckCSRF(r); err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ------------------------------------------------------------------ helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already written, so this can only be logged.
		return
	}
}

// apiError is the single error shape the SPA handles.
type apiError struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

// writeStoreError maps store errors onto sensible statuses so handlers do not
// each reinvent the mapping.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, db.ErrNoUser):
		writeError(w, http.StatusConflict, "setup has not been completed")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	// 1 MB is far more than any polyemesis payload; the cap stops a malformed
	// or malicious body from being buffered wholesale.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}
	return true
}

func idParam(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, name), 10, 64)
}
