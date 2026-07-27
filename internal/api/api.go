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
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/secrets"
	"github.com/rainmanjam/polyemesis/internal/web"
)

// Server wires the HTTP layer to everything else.
type Server struct {
	log      *slog.Logger
	cfg      config.Config
	store    *db.DB
	box      *secrets.Box
	eng      *engine.Engine
	bus      *events.Broker
	sessions *auth.Manager
	logins   *auth.Throttle
	version  string
	// startedAt is the process start, which is what the uptime metric reports.
	startedAt time.Time
}

// Options configures the server.
type Options struct {
	Log     *slog.Logger
	Config  config.Config
	DB      *db.DB
	Secrets *secrets.Box
	Engine  *engine.Engine
	Events  *events.Broker
	Version string
}

// New creates the server.
func New(o Options) *Server {
	return &Server{
		log:       o.Log,
		cfg:       o.Config,
		store:     o.DB,
		box:       o.Secrets,
		eng:       o.Engine,
		bus:       o.Events,
		version:   o.Version,
		startedAt: time.Now(),
		logins:    auth.NewThrottle(),
		sessions: auth.New(
			o.Secrets.Derive("session-jwt"),
			o.Config.TLS.Enabled,
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

	r.Route("/api/v1", func(r chi.Router) {
		// --- unauthenticated ---
		r.Get("/setup", s.handleSetupStatus)
		r.Post("/setup", s.handleSetup)
		r.Post("/auth/login", s.handleLogin)
		r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		})

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
			r.Get("/status", s.handleStatus)
			r.Get("/source", s.handleSource)
			r.Get("/stats", s.handleStats)
			r.Get("/levels", s.handleLevels)

			r.Get("/settings", s.handleGetSettings)
			r.Put("/settings", s.handlePutSettings)

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

			r.Get("/recordings", s.handleListRecordings)
			r.Get("/recordings/usage", s.handleRecordingUsage)
			r.Delete("/recordings/{id}", s.handleDeleteRecording)

			r.Get("/processes", s.handleListProcesses)
			r.Get("/processes/{name}/logs", s.handleProcessLogs)

			r.Get("/platforms/guides", s.handlePlatformGuides)
			r.Get("/platforms/credentials", s.handleListCreds)
			r.Put("/platforms/credentials/{platform}", s.handlePutCreds)
			r.Delete("/platforms/credentials/{platform}", s.handleDeleteCreds)
			r.Get("/platforms/accounts", s.handleListAccounts)
			r.Delete("/platforms/accounts/{id}", s.handleDeleteAccount)
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
		})
	})

	// Preview playlist and segments. hls.js cannot attach headers, so this
	// relies on the session cookie, same-origin.
	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		r.Handle("/hls/*", s.hlsHandler())
	})

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
			strings.HasPrefix(r.URL.Path, "/hls/") {
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
