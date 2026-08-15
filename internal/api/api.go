// Package api is the HTTP surface: a JSON REST API, a WebSocket for live
// telemetry, and the embedded single-page UI, all on one listener.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/clips"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/recording"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/secrets"
	"github.com/rainmanjam/polyemesis/internal/stats"
	"github.com/rainmanjam/polyemesis/internal/tlsx"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
	"github.com/rainmanjam/polyemesis/internal/upgrade"
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

// engOrNil is eng() for the callers that have to answer for an absent engine,
// and it exists to make that answer ATOMIC.
//
// Two properties, and the second is the one that was got wrong:
//
//	It tolerates a Server with no manager at all -- every server in this
//	package's unit tests -- where eng() would panic inside Manager.Default
//	before there is an engine to test.
//
//	It reads the engine set ONCE. eng() re-derives from Manager.Engines under
//	m.mu on every call, and Manager.reconcile deletes from that map when a
//	source is deleted, as does Manager.Stop when the process drains. A caller
//	that wrote `if s.eng() == nil { ... }; s.eng().Hub()` therefore tested one
//	engine and dereferenced another, and the guard bought nothing at exactly
//	the moment it was needed: the last source going away under a scrape.
//
// Capture the return value and use THAT. See audit.go's eng := s.eng(), which
// is the shape the rest of this package already uses.
func (s *Server) engOrNil() *engine.Engine {
	if s.mgr == nil {
		return nil
	}
	return s.eng()
}

// tools is the FFmpeg this install detected.
//
// OFF THE MANAGER, NOT OFF AN ENGINE, and that is the whole point of it having
// a name of its own. Every engine is handed the same *ffmpeg.Tools -- it
// describes the box, not a programme -- so reading it through s.eng() made
// /system, /encoders, /fonts, the clip planner and the upload probe depend on
// a pipeline being up to answer a question about the machine.
//
// Nil when this build has no manager, which is every server in this package's
// unit tests, and nil is a value each caller has to handle: only Tools'
// METHODS are nil-receiver safe, its FIELDS are not. See ffmpeg.Tools.HasFilter
// versus ffmpeg.Tools.FFmpeg.
func (s *Server) tools() *ffmpeg.Tools {
	if s.mgr == nil {
		return nil
	}
	return s.mgr.Tools()
}

// hostSystem is this box's CPU and memory, from the one sampler the manager
// runs. Not from an engine: see engine.Manager.Host.
//
// A build with no manager reports the zero snapshot rather than panicking.
// That is not a claim that the box is idle -- it is the same "nothing has
// looked yet" the sampler itself answers with before its first tick.
func (s *Server) hostSystem() stats.System {
	if s.mgr == nil {
		return stats.System{}
	}
	return s.mgr.Host().System()
}

// ingestBitrate is the arrival series the dashboard graphs, empty when no
// programme is running.
//
// The check is HERE, in the handler layer, and not a nil-receiver guard on
// stats.Monitor. That is the deliberate half of it. stats.Monitor and
// relay.Hub describe a pipeline that is up; teaching them to answer for one
// that is not spreads "there is nothing running" into two packages that have
// no way to say so, and the next reader of Monitor.Bitrate would have to
// wonder which of its zeroes meant idle and which meant absent. The API is
// where the question "is there a programme at all" is already asked, so it is
// where it is answered.
//
// Empty rather than a single zero sample: the graph draws no line for a series
// it has never had a reading of, which is the truth, where a zero reading
// claims a measured silence.
//
// ONE engOrNil, and the result is what gets dereferenced. See engOrNil.
func (s *Server) ingestBitrate() []stats.Sample {
	e := s.engOrNil()
	if e == nil {
		return []stats.Sample{}
	}
	return e.Monitor().Bitrate()
}

// relayStats is the fan-out hub's throughput, zero when no programme is
// running. Same reasoning as ingestBitrate, and the zero value is honest: no
// hub exists, so nothing has been received, replicated or dropped.
func (s *Server) relayStats() relay.Stats {
	e := s.engOrNil()
	if e == nil {
		return relay.Stats{}
	}
	return e.Hub().Stats()
}

// clipDir is where captured clips live: recordings/clips, one directory for
// the whole install.
//
// Off the CONFIG, not off an engine, for the same reason Server.recordings is:
// engine.New computes this exact path from the same cfg.RecordingsDir(), so
// every engine names the same directory and a clip an operator captured
// yesterday is still on disk after the programme that made it was deleted.
//
// It is a real base directory and must stay one. clips.Resolve confines a
// downloaded name against it, and a nil-safe accessor answering "" would turn
// that confinement into confinement against nothing. See jobs.go's ruling on
// the same question for recordings.
func (s *Server) clipDir() string {
	return filepath.Join(s.cfg.RecordingsDir(), clips.Subdir)
}

// recordings is the shared, read-only view of the recordings directory:
// usage, deletes, and the path confinement the downloads run through.
//
// Off the manager for the same reason tools is, plus one of its own:
// recordings deliberately outlive the source that made them
// (recordings.source_id is ON DELETE SET NULL), so an operator clearing disk
// after deleting a programme is exactly the caller this must still serve.
//
// The pure PATH readers do not come through here at all -- they take
// s.cfg.RecordingsDir() directly, because a directory name needs no manager.
//
// It requires a manager and does not pretend otherwise: a nil one panics here
// exactly as s.eng() did, rather than handing back a Manager whose Dir() is ""
// and turning every confinement below it into confinement against nothing.
func (s *Server) recordings() *recording.Manager { return s.mgr.Recordings() }

// storageVerdict is the free-space guard's answer: whether the floor has
// stopped recording, and the sentence that says why.
//
// OFF AN ENGINE, and it is the one reader in this group that stays there. The
// floor itself describes the volume, but the HALT does not -- it is one
// recorder child being stopped, by the guard on THAT engine's own recording
// manager (recording.WithStorageGuard in engine.New). The shared read-only
// manager drives no recorder, so nothing ever calls CheckFreeSpace on it and
// its StorageState is the zero value for the life of the process. Serving that
// would tell the operator everything is fine on the one page whose job is to
// explain a recorder that stopped on its own -- see the field comment on
// recording.DiskUsage.Storage.
//
// The default engine's, because this endpoint is unscoped and that is the
// programme it speaks for everywhere else. On a real install the answer is the
// same whichever engine is asked: one volume, one floor, one install-wide
// Recording block (see engine.effectiveSettings, which overlays the ingest and
// nothing else).
//
// No engine means the zero verdict, and that is the TRUE answer rather than a
// fallback: nothing has been halted because nothing was recording.
func (s *Server) storageVerdict() recording.StorageState {
	if s.mgr == nil {
		return recording.StorageState{}
	}
	e := s.mgr.Default()
	if e == nil {
		return recording.StorageState{}
	}
	return e.Recordings().Storage()
}

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
	// providers is the OAuth provider set every handler resolves through, and
	// it replaced five function-pointer fields on this struct.
	//
	// Those fields -- ingestForFn, pushMetadataFn, pushComplianceFn,
	// pushBroadcastFn and rescheduleFn -- all existed for one reason, which
	// each of their comments stated: internal/oauth resolved a provider through
	// package-level functions against production hosts, so no test in THIS
	// package could aim a provider anywhere it controlled, and the last
	// observable point before a request left the process was a closure wrapped
	// around the call. Four of the five received the pusher as a parameter,
	// which made that closure a stand-in for an interface value the test could
	// have supplied outright if it had been able to build one.
	//
	// oauth.Set is that missing injection point. The zero value resolves to the
	// platforms' real hosts, so a server built with no Providers -- which is
	// every server in production -- behaves exactly as it did; a test hands in
	// oauth.NewSet(oauth.WithBaseURL(srv.URL)) and every call this package makes
	// lands on its own httptest server instead.
	//
	// Resolve EVERY capability through this field, never through the
	// package-level oauth.Get/MetadataFor/ComplianceFor/TargetsFor. A handler
	// that mixes the two holds a stubbed provider and a production one at the
	// same time, which is the partially-redirected provider internal/oauth's
	// endpoints.go opens by warning about.
	providers oauth.Set
	// tls is the same Provider the listener is serving from, so the status
	// card describes the certificate actually in use rather than re-deriving
	// one — a second Provider in selfsigned mode would rewrite the material
	// on disk out from under the running listener. Nil is tolerated: the
	// status endpoint then reports the configured intent only.
	tls     *tlsx.Provider
	version string
	// startedAt is the process start, which is what the uptime metric reports.
	startedAt time.Time

	// upgradeMethod and execPath are how this install was put on the box and
	// what an upgrade would replace. Both are settled ONCE, in New, because
	// both are properties of the process rather than of a request: Detect reads
	// the environment and /proc/self/cgroup, os.Executable reads the same
	// argv-derived path every time, and neither answer can change without this
	// process ending. Deciding them per request would put file stats in a
	// handler and buy nothing.
	//
	// PLAIN FIELDS, not an interface or an Options entry, so a test can build
	// &Server{upgradeMethod: ..., execPath: ...} the way version_check_test.go
	// already builds a server for the version endpoints. Their zero values are
	// safe: an empty Method makes upgrade.PlanFor report "unrecognised install
	// method", which is a refusal, and a refusal is the correct answer from a
	// server that never established how it was installed.
	upgradeMethod upgrade.Method
	execPath      string

	// uiFS overrides the embedded UI filesystem the NotFound terminal serves.
	// Nil -- which is every server this binary builds -- means web.Handler(),
	// the embedded bundle, unchanged. See uiHandler for why the seam exists.
	uiFS fs.FS

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

	// hooks is the shared lifecycle-webhook dispatcher. Optional like
	// everything else in this block: a build with none wired still serves the
	// hooks page, listing stored hooks and reporting that nothing is delivering
	// -- which is the difference an operator needs to see.
	hooks *hooks.Dispatcher

	// kickKeys caches Kick's webhook signing key. One per server rather than
	// one per adapter: the key belongs to Kick, not to an account, so two
	// connected Kick channels share the fetch.
	kickKeys *chat.KickKeyFetcher

	// revokedMu guards revoked and wsPingEvery.
	revokedMu sync.RWMutex
	// revoked is the set of api_tokens.id values this process has deleted.
	//
	// IT EXISTS FOR ONE READER: the /ws ping tick (#159). A socket's principal
	// is captured once, at upgrade, and requireAuth never runs again -- so
	// revoking a token, which is the operator's ONLY lever after a leak, did
	// not reach a socket that was already open. It stayed open, and it stayed
	// at the scope it was opened with, until the client went away. Under a
	// single-administrator product that is defensible; "revoke does not revoke"
	// is not a sentence to leave in the product.
	//
	// IN-PROCESS AND WRITE-ONLY-ON-REVOKE, and the three alternatives were all
	// worse:
	//
	//	Re-looking the token up on each tick (LookupAPIToken) means retaining
	//	the PLAINTEXT bearer for the life of the socket so there is something
	//	to look up with, and firing a last_used_at write per socket per minute
	//	at a single SQLite connection, and adding a database-error path to a
	//	loop where the only safe answer to an error is "do not close the
	//	socket" -- which is a branch that must never be got wrong and would
	//	never be exercised.
	//
	//	A process-global epoch counter bumped on every mutation does not
	//	survive a restart, has to be touched by every future mutation site, and
	//	invites somebody to treat the counter as the authorisation decision.
	//
	//	Broadcasting revocations over a channel couples the socket loop to the
	//	store's lifecycle for a signal that is one map lookup.
	//
	// UNBOUNDED BY DESIGN, and the bound is the process. An entry is ~8 bytes
	// and is added only when an operator revokes a token by hand; an install
	// that revoked ten thousand tokens between restarts would be holding 80 kB.
	// Pruning would need to know that no socket still holds the id, which is
	// the state this map exists to avoid tracking.
	//
	// It is NOT an authorisation source. Absence from this set means "this
	// process has not seen that token deleted", which is not the same as "the
	// token is valid" -- a token deleted by another process, or by an operator
	// editing the database, is absent here. Every REQUEST still goes through
	// requireAuth, which asks the database. This only ever CLOSES a socket
	// early; it never keeps one open.
	revoked map[int64]struct{}
	// wsPingEvery overrides pingPeriod, and is set only by tests.
	//
	// The revocation check rides the existing ping tick, so a test of it has to
	// wait a tick, and the real tick is 25 seconds. The alternative was to
	// export the check and call it directly, which would test the map and not
	// the thing that was broken -- that a socket already open stops receiving.
	wsPingEvery time.Duration

	// settingsMu is the one serialisation boundary between a playlist
	// reference existing and an upload it names being removable.
	// handlePutSettings holds it across validating and storing a settings
	// document; handleDeleteMedia holds it across checking whether a stored
	// playlist item names the upload being deleted and removing that
	// upload's files. Without a shared lock the two can interleave: a save
	// that already passed its own existence check can still commit after a
	// concurrent delete decided the same upload was unreferenced, leaving a
	// freshly saved item pointing at a file that is gone -- exactly the
	// state the delete's in-use guard exists to prevent. Neither handler is
	// hot enough for the coarse scope to matter.
	settingsMu sync.Mutex

	// preannounceMu guards announceFails, which the pre-announce sweep mutates
	// and nothing else reads.
	preannounceMu sync.Mutex
	// announceFails counts CONSECUTIVE failures per SHOW -- creates as well as
	// reschedules, which is why it is no longer named for one of them.
	//
	// Keyed per (destination, schedule) rather than per destination, because a
	// destination now holds one broadcast per schedule: a per-destination count
	// would let one schedule's create fail three times and trip the give-up on
	// ANOTHER schedule's perfectly good broadcast, orphaning its event page.
	//
	// A named struct key rather than the two ids packed into an int64. The
	// packing was safe for any id below 2^31 and it was still the wrong shape:
	// a key that has to be encoded and decoded is one a reader has to verify,
	// and Go compares struct keys natively.
	//
	// In memory on purpose: a restart resetting the count only means the sweep
	// is more patient, which is the safe direction.
	announceFails map[showKey]int

	// auditSink diverts audit events instead of publishing them, and is set
	// only by tests.
	//
	// It exists because the alternative is untestable rather than merely
	// awkward. publishAudit needs a manager and a started engine, and this
	// package's tests have neither, so without a seam every audit publish is a
	// no-op under test and the wiring in the handlers -- which endpoint raises
	// what, on which branch -- cannot be asserted at all. Removing all five
	// call sites left the package green.
	auditSink func(alerts.Event)

	// probeBin supplies the ffprobe path directly, and is set only by tests.
	//
	// Same shape as auditSink. Its original justification was that probeUpload
	// reached ffprobe through s.eng().Tools() and this package's tests have no
	// manager, so probing was skipped and deleting the probe call left the
	// package green -- review found that; the tests did not. probeUpload now
	// reads the detection off the MANAGER (Server.tools), so a fixture that
	// builds one no longer needs a seam at all, and
	// media_probe_wiring_test.go's pair is exactly that fixture.
	//
	// What it still buys is the cheap fixture: the twenty-odd cases in
	// media_probe_test.go and upload_verdict_test.go aim a fake prober at one
	// upload each, and they run on testServer -- no manager, no ports, no
	// engines. Rebuilding each of those around a started manager would trade a
	// one-line override for a second of setup per case, and it is the same
	// binary either way.
	probeBin string

	// encodeBin supplies the ffmpeg path for probeUpload's duration count, and
	// is set only by tests.
	//
	// The sibling of probeBin and it stands or falls with it. The branch it
	// reaches is the one that COUNTS the length of a raw elementary stream
	// (#218), which no other test exercises: a test that sets probeBin and
	// leaves this empty exercises the refusal; one that sets both exercises the
	// count.
	encodeBin string

	// probeTimeout overrides probeUploadTimeout, and is set only by tests.
	//
	// Zero means the constant. It exists because probeUploadTimeout is 30
	// seconds and the behaviour that needs asserting -- a probe that runs out
	// of time is treated as "could not check" and the upload is kept -- would
	// otherwise cost half a minute per run, which is how a check ends up
	// deleted rather than fixed.
	probeTimeout time.Duration
}

// showKey identifies one scheduled show: a destination, and the schedule that
// puts a broadcast on it.
type showKey struct {
	DestinationID int64
	ScheduleID    int64
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

	// Hooks is the lifecycle-webhook dispatcher. Optional.
	Hooks *hooks.Dispatcher

	// Providers is the OAuth provider set every handler resolves through.
	//
	// Optional, and the zero value is what production passes: an unset Set
	// answers with the platforms' real hosts. It is here so a test can hand in
	// oauth.NewSet(oauth.WithBaseURL(srv.URL)) and have every platform call
	// this package makes land on its own stub -- which is the whole reason the
	// five function-pointer fields on Server no longer exist. See Server's own
	// providers field.
	Providers oauth.Set
}

// New creates the server.
func New(o Options) *Server {
	s := &Server{
		log:       o.Log,
		cfg:       o.Config,
		providers: o.Providers,
		store:     o.DB,
		box:       o.Secrets,
		mgr:       o.Engine,
		bus:       o.Events,
		tls:       o.TLS,
		jobq:      o.Jobs,
		gov:       o.Governor,
		whisper:   o.Whisper,
		chat:      o.Chat,
		hooks:     o.Hooks,
		version:   o.Version,
		startedAt: time.Now(),
		logins:    auth.NewThrottle(),
		kickKeys:  &chat.KickKeyFetcher{},
		revoked:   map[int64]struct{}{},
		sessions: auth.New(
			o.Secrets.Derive("session-jwt"),
			// ServesTLS, not the legacy tls.enabled: an install that writes
			// tls.mode explicitly leaves Enabled false, and reading it here
			// would drop the Secure flag from the session cookie on a server
			// that is genuinely terminating TLS.
			o.Config.ServesTLS(),
			o.Config.TrustProxyHeaders,
			// Session revocation. Reading it through the store rather than
			// caching it means a password change takes effect on the very next
			// request, with no window in which a revoked token still works.
			o.DB.TokenEpoch,
		),
	}
	// nil for both probes means "ask the real environment and the real
	// filesystem", which is what a running server wants; the parameters exist
	// for internal/upgrade's own tests.
	s.upgradeMethod = upgrade.Detect(nil, nil)
	// An error here is not fatal and must not be. os.Executable fails on a few
	// exotic platforms and on a process whose binary has been unlinked, and
	// neither is a reason to refuse to serve — it is a reason to refuse to
	// UPGRADE, which is exactly what an empty path makes upgrade.PlanFor do.
	if p, err := os.Executable(); err == nil {
		s.execPath = p
	}
	return s
}

// Handler builds the router.
//
// IT REGISTERS NOTHING ITSELF, and that is a load-bearing property rather than
// a style choice. Every registration goes through registerRoutes, which takes a
// chi.Router INTERFACE, so the route-coverage ledger can hand it a recorder and
// derive the population it reconciles from the registrations this build makes
// instead of from chi.Walk plus a hand-written list of everything the walk
// cannot see. chi.Walk is complete over the routing TRIE and the trie is not the
// mux: r.NotFound and the method-not-allowed terminal are invisible to it, and
// until now the only record that they exist was a slice of probes somebody
// remembered to write.
//
// TestHandlerRegistersOnlyThroughTheRecordedSeam reads this function's AST and
// fails if a registration call appears here, because a route registered on the
// mux directly is a route the recorder cannot see -- which is the same hole one
// level in.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	s.registerRoutes(r)
	return r
}

// registerRoutes is Handler's body, taking the router as an interface so that
// the enumeration authority can be a recorder rather than a walk. See Handler.
func (s *Server) registerRoutes(r chi.Router) {
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
			// After requireAuth, which is what puts the principal in the
			// context, and before everything else: a read-scoped token is
			// refused here rather than in any handler. See requireScope.
			r.Use(s.requireScope)
			r.Use(s.requireCSRF)

			r.Post("/auth/logout", s.handleLogout)
			r.Get("/auth/me", s.handleMe)

			// The onboarding tour's "have I already been offered this".
			//
			// Read is a plain GET, so a read-scoped token may ask. There is
			// nothing to protect in the answer: it is one timestamp about a
			// popover, and refusing it would be the sort of denial that teaches
			// nobody anything.
			//
			// The WRITE is admin-scoped, and it is admin-scoped BY
			// CONSTRUCTION rather than by a list: requireScope refuses every
			// non-GET to a read token unless the pattern is in
			// readScopeWritePatterns, and this one deliberately is not. A
			// read-only credential must not be able to mutate user state,
			// however small that state is -- "it is only a boolean" is the
			// argument that ends with a read token turning off a warning
			// somebody else needs.
			r.Get("/tour", s.handleTourState)
			r.Post("/tour/complete", s.handleTourComplete)

			// --- session only ---
			//
			// A signed-in browser reaches these; an API token does not, and the
			// group is where that is enforced rather than merely described.
			//
			// #140 is why it exists. The media writes below used to sit in the
			// plain authenticated group under a comment stating that a token
			// could not reach them, which was false the day it was written:
			// requireCSRF passes token principals through on purpose, so the
			// only thing standing between a leaked token and an arbitrary file
			// on the server's disk was a sentence. The three token routes were
			// genuinely enforced, by an `if !requireSession(...)` at the top of
			// each handler -- a pattern that protects the handlers that
			// remember it and nothing else, which is precisely how the media
			// routes came to be added without one.
			//
			// So membership of this group is now the whole statement. Adding a
			// route here makes it session-only; adding one outside does not
			// silently make it token-reachable-but-documented-otherwise,
			// because there is no longer a claim anywhere except this block.
			r.Group(func(r chi.Router) {
				r.Use(s.requireSession)

				// Minting and revoking. A token that can mint tokens makes
				// revocation meaningless -- the operator deletes the one they
				// know about while the holder has quietly issued three more.
				// The list is here too: it is the operator's inventory of
				// credentials, and automation has no reason to enumerate it.
				r.Get("/auth/tokens", s.handleListAPITokens)
				r.Post("/auth/tokens", s.handleCreateAPIToken)
				r.Delete("/auth/tokens/{id}", s.handleRevokeAPIToken)

				// The password. handleChangePassword already demands the
				// CURRENT password, so a token alone could never have changed
				// it, but that is an incidental defence sitting inside a
				// handler rather than a stated rule: the account credential is
				// not something a machine credential gets to touch, and after
				// #140 the difference between "cannot in practice" and "is
				// refused by the router" is one this file takes seriously.
				r.Post("/auth/password", s.handleChangePassword)

				// Media WRITES. The bytes and the filename both come from the
				// caller, which is the shape SECURITY.md's path-confinement
				// section exists for, and a credential built for unattended
				// automation should not be able to fill the volume the database
				// and the recorder live on.
				//
				// GET /media is deliberately NOT here. Listing what is already
				// stored is exactly what automation is for, and it is the read
				// the narrowed wording in SECURITY.md and docs/API.md now
				// promises: a token can list media, and cannot write it.
				r.Post("/media", s.handleUploadMedia)
				r.Delete("/media/{name}", s.handleDeleteMedia)
				// Re-inspecting a stored upload (#202). A WRITE, and here
				// rather than beside GET /media, because it replaces the
				// record that playlistUploadProblems and
				// pullSourceUploadProblems both gate saves on. See
				// handleVerifyMedia.
				r.Post("/media/{name}/verify", s.handleVerifyMedia)

				// Replacing the server's own binary, and putting the previous
				// one back. #149 added these calling requireSession from
				// inside the handler, for the reason the group's opening
				// paragraph gives: requireCSRF passes a token-authenticated
				// request straight through by design, so membership of the
				// merely-authenticated group would have left a leaked token
				// able to overwrite the binary. That reasoning is right and
				// the placement was the pre-#140 one -- a check the handler
				// remembers rather than a rule the router imposes. Here the
				// router imposes it. GET /upgrade/plan stays outside: reading
				// what an upgrade WOULD do is a read.
				r.Post("/upgrade/stage", s.handleUpgradeStage)
				r.Post("/upgrade/rollback", s.handleUpgradeRollback)

				// PUT /settings/automod-key is deliberately NOT here, and the
				// question was asked rather than skipped. It seals a
				// third-party API key, which sounds like credential management
				// until you notice that PUT /platforms/credentials/{platform}
				// does the same thing for every streaming platform and stays
				// token-reachable. Sealing a key an operator pasted is a
				// settings write like the others; what should gate it is the
				// token's scope (#104), not this group, and pulling one of the
				// two in here would leave a rule no reader could restate.
			})

			r.Get("/system", s.handleSystem)
			// Authenticated on purpose: the build number is a fingerprint, and
			// an unauthenticated scanner should not get to read it. The check
			// is a POST so requireCSRF covers it and no prefetching browser
			// can reach out to GitHub on the operator's behalf.
			r.Get("/version", s.handleVersion)
			r.Post("/version/check", s.handleCheckUpdate)
			// Acting on what the check found. Separate from /version on
			// purpose: the plan probes whether the install directory is
			// writable BY CREATING A FILE IN IT, and /version is read by the
			// update banner on every page load. See handleUpgradePlan.
			//
			// The two MUTATING routes are registered in the session-only group
			// above, not here: being inside this group is NOT enough --
			// requireCSRF passes a token-authenticated request straight
			// through, by design, so a leaked API token would otherwise be
			// able to replace the server's own binary. See upgrade.go.
			r.Get("/upgrade/plan", s.handleUpgradePlan)
			r.Get("/status", s.handleStatus)
			r.Get("/source", s.handleSource)
			// What each incoming track is. Per-ingest, not per-destination:
			// the feed is the same feed whoever is listening to it.
			r.Put("/source/annotations", s.handlePutAnnotations)
			// Manual failover. The tier's return mode defaults to manual, so
			// this is how a broadcast leaves a slate.
			r.Post("/failover/source", s.handleSwitchSource)
			// Per-item playlist readiness. Its own GET rather than a field on
			// the settings blob -- see handlePlaylistStatus.
			r.Get("/failover/playlist", s.handlePlaylistStatus)
			r.Get("/stats", s.handleStats)
			r.Get("/levels", s.handleLevels)

			r.Get("/settings", s.handleGetSettings)
			r.Put("/settings", s.handlePutSettings)
			// Its own route because the password must never travel outward in
			// the settings blob -- see handlePutMQTTPassword.
			r.Put("/settings/mqtt-password", s.handlePutMQTTPassword)

			r.Get("/tls", s.handleTLSStatus)

			// The platform registry: ingest servers, encoder ceilings and
			// codecs, so the operator picks "Twitch" instead of typing a URL.
			// Static data, but authenticated like everything else here --
			// there is no reason for an unauthenticated caller to enumerate
			// what this install can publish to.
			r.Get("/services", s.handleListServices)

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

			// Sources: the multi-programme endpoints. Placed before renditions
			// because a rendition belongs to a source, which is the order the
			// UI needs to build its forms in too.
			r.Get("/sources", s.handleListSources)
			r.Post("/sources", s.handleCreateSource)
			r.Get("/sources/{id}", s.handleGetSource)
			r.Put("/sources/{id}", s.handleUpdateSource)
			r.Delete("/sources/{id}", s.handleDeleteSource)
			r.Post("/sources/{id}/token", s.handleRotateSourceToken)

			// Automod. Its CONFIG rides inside Settings, so GET/PUT /settings
			// already carries the matrix, rules and model options; only the
			// derived matrix, model spend and the sealed key need endpoints.
			r.Get("/automod/matrix", s.handleAutomodMatrix)
			r.Get("/automod/stats", s.handleAutomodStats)
			r.Put("/settings/automod-key", s.handlePutAutomodKey)

			// The read half of media. POST /media and DELETE /media/{name} are
			// in the session-only group above, which is the fix for #140; this
			// listing stays reachable by an API token on purpose.
			r.Get("/media", s.handleListMedia)

			r.Get("/renditions", s.handleListRenditions)
			r.Post("/renditions", s.handleCreateRendition)
			// Static segment first, same as /destinations/order above.
			r.Get("/renditions/presets", s.handleRenditionPresets)
			// The font picker for text overlays. A listing rather than a
			// compiled-in list, because operators add their own fonts.
			r.Get("/fonts", s.handleListFonts)
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

			// Lifecycle webhooks. Same authenticated group as the alert rules,
			// and /meta before /{id} so chi cannot read "meta" as an id.
			r.Get("/hooks/meta", s.handleHooksMeta)
			r.Get("/hooks", s.handleListHooks)
			r.Post("/hooks", s.handleCreateHook)
			r.Get("/hooks/{id}", s.handleGetHook)
			r.Put("/hooks/{id}", s.handleUpdateHook)
			r.Delete("/hooks/{id}", s.handleDeleteHook)
			// POST despite reading nothing: it makes an outbound call to a
			// third party, so it is neither safe nor idempotent, and POST puts
			// it behind requireCSRF with the rest of the state-changing group.
			r.Post("/hooks/{id}/test", s.handleTestHook)
			r.Get("/hooks/{id}/deliveries", s.handleHookDeliveries)

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
			// POST rather than GET despite reading nothing: it makes an
			// outbound call to a third party, so it is neither safe nor
			// idempotent, and POST puts it behind requireCSRF with the rest of
			// the state-changing group.
			r.Post("/platforms/credentials/{platform}/check", s.handleCheckCreds)
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
			// What is still editable on each account's current broadcast.
			// Fetched when the composer opens, never polled -- every row is a
			// live platform call.
			r.Get("/metadata/broadcast-window", s.handleBroadcastWindow)

			// Unified chat. Live messages arrive on the WebSocket, not here;
			// these four are the scrollback a freshly opened pane needs plus
			// the three things a socket cannot do. Deletion is addressed by
			// query parameters because a platform-issued message id does not
			// survive a path segment. See chat.go.
			r.Get("/chat", s.handleChatOverview)
			r.Get("/chat/messages", s.handleChatMessages)
			// Finding one comment again. Searches the table rather than the
			// Hub's ring, which only holds this process's lifetime.
			r.Get("/chat/search", s.handleChatSearch)
			// The moderator's user card: what one person has said. Read from
			// our own scrollback, because no platform publishes this.
			r.Get("/chat/users", s.handleChatUser)
			r.Delete("/chat/messages", s.handleChatDeleteMessage)
			// Hiding is POST rather than DELETE because it is reversible, and
			// separate from delete because the two are different decisions:
			// one takes a message off the public feed, the other destroys it.
			r.Post("/chat/messages/hide", s.handleChatHideMessage)
			// Banning addresses a PERSON, not a message, so it is its own
			// route rather than a mode on the message ones.
			// Channel-wide rules act on the ROOM, not a message or a
			// person, so they get their own route too.
			r.Patch("/chat/settings", s.handleChatSettings)
			r.Post("/chat/bans", s.handleChatBan)
			r.Delete("/chat/bans", s.handleChatUnban)
			r.Post("/chat/send", s.handleChatSend)
		})

		// A scraper has no CSRF token to double-submit, and this route is a
		// read-only GET, so requireCSRF is skipped. It is still authenticated;
		// handleMetrics says why.
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Use(s.requireScope)
			r.Get("/metrics", s.handleMetrics)
		})

		// OAuth redirects are top-level browser navigations, so they carry the
		// session cookie but cannot carry a CSRF header. The OAuth `state`
		// parameter is the CSRF defence for these two routes.
		//
		// Session-only, and this is the one place the method-shaped scope rule
		// in requireScope would have been wrong. Its premise is that GET does
		// not change anything, and GET /oauth/{platform}/callback does: it
		// stores a connected platform account. The `state` it demands is held
		// in the DATABASE rather than in a cookie, so a caller holding only a
		// bearer token could walk both halves of the flow and attach an account
		// to somebody else's server -- a write, reached by a credential this
		// release is otherwise promising is read-only.
		//
		// Requiring a session costs nothing real, because completing an OAuth
		// consent screen is something only a browser does, and a browser doing
		// it is signed in by definition.
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Use(s.requireSession)
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
			// A read-scoped token is welcome here, and stays welcome: watching a
			// stream go out is the monitoring use case scopes exist for.
			//
			// The reasoning that USED to stand here -- "the read pump discards
			// whatever the client sends, so this is telemetry out and nothing
			// in" -- was true and beside the point. It argued only about the
			// INBOUND direction, and the socket's problem was outbound: every
			// event was rendered by a bare json.Marshal with no principal
			// anywhere in the call, so a read token received the admin shape of
			// everything, including FFmpeg log lines carrying an argv.
			//
			// What makes the read scope safe here is now a thing rather than a
			// sentence: handleWS captures the principal at upgrade and every
			// frame goes through eventView over a CLOSED policy table, which
			// fails closed on an event type nobody has classified. See
			// ws_policy.go.
			r.Use(s.requireScope)
			r.Get("/ws", s.handleWS)
		})

		// Downloads are top-level navigations for the same reason.
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Use(s.requireScope)
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
	// relies on the session cookie, same-origin -- and now SAYS SO IN THE ROUTE
	// TABLE rather than only in this comment.
	//
	// Two things were wrong with the previous registration, and neither was
	// visible from the handler. It carried requireAuth but NOT requireScope,
	// making it the one authenticated group in the table with no scope check at
	// all -- so a read token reached it, and any request whose path ends
	// .m3u8 calls PreviewRequested, which starts the on-demand preview encoder
	// and keeps it alive for as long as the polling continues. And it was
	// registered with r.Handle, so it answered EVERY method, including the ones
	// the scope rule refuses everywhere else.
	//
	// Session-only closes the encoder-pinning for read AND admin bearers, at no
	// cost to the dashboard: the browser is the only thing that has ever
	// fetched these, and it has the cookie. GET and HEAD only, so the method
	// surface matches what a media element actually issues.
	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		r.Use(s.requireSession)
		hls := s.hlsHandler()
		r.Get("/hls/*", hls.ServeHTTP)
		r.Head("/hls/*", hls.ServeHTTP)
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

	if h, err := s.uiHandler(); err == nil {
		r.NotFound(h.ServeHTTP)
	} else {
		s.log.Error("embedded UI unavailable", "err", err)
	}
}

// uiHandler is the NotFound terminal, resolved through a field so a test can
// mount the same closure over a populated `dist` and drive the SPA and asset
// branches THROUGH THE REAL MUX rather than beside it.
//
// That distinction is the whole of #167's residual. The branch table was driven
// against web.HandlerFor directly, with no requireAuth, no security headers and
// no request logger in front of it, while the mux this checkout and CI actually
// serve mounts a `dist` holding only .gitkeep -- so the configuration production
// ships was the one configuration no probe had ever entered end to end. A nil
// field is the shipped behaviour verbatim.
func (s *Server) uiHandler() (http.Handler, error) {
	if s.uiFS != nil {
		return web.HandlerFor(s.uiFS), nil
	}
	return web.Handler()
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

// markRevoked records that this process deleted an API token, so any /ws socket
// still holding it can be closed on its next ping tick. See Server.revoked.
//
// Called AFTER the delete succeeds, never before: a failed delete leaves the
// token working, and an entry here would then close a socket whose credential
// is still valid -- a self-inflicted outage in the one code path an operator
// reaches for during an incident.
func (s *Server) markRevoked(id int64) {
	if s == nil || id == 0 {
		return
	}
	s.revokedMu.Lock()
	if s.revoked == nil {
		s.revoked = map[int64]struct{}{}
	}
	s.revoked[id] = struct{}{}
	s.revokedMu.Unlock()
}

// isRevoked reports whether this process has deleted the given token id.
//
// One read-lock and one map lookup, per socket, per ping period. Nothing here
// touches the database, and it must not start to: see Server.revoked.
func (s *Server) isRevoked(id int64) bool {
	if s == nil || id == 0 {
		return false
	}
	s.revokedMu.RLock()
	_, ok := s.revoked[id]
	s.revokedMu.RUnlock()
	return ok
}

// sessionEpochChanged reports whether a session signed at `was` has since been
// revoked, which for a session means the user's password was changed.
//
// Deliberately NOT modelled on the revoked set above. That set is an in-process
// note of something this process did, and it is documented as never being an
// authorisation source; the epoch is the opposite -- the database is the only
// thing that knows it, a bump can arrive from another process or from sqlite3,
// and there is nothing in memory that would hear about it. So this reads the
// store, once per socket per ping period.
//
// Fails CLOSED, exactly as auth.(*Manager).checkEpoch does on the request path:
// a store that cannot answer is not a store that has said yes.
func (s *Server) sessionEpochChanged(userID, was int64) bool {
	if s == nil || userID == 0 {
		return false
	}
	current, err := s.store.TokenEpoch(userID)
	if err != nil {
		s.log.Warn("cannot read the session epoch for an open socket; closing it",
			"user", userID, "err", err)
		return true
	}
	return current != was
}

// pingEvery is how often a socket pings, and therefore how long a revoked
// token's socket can survive. Tests shorten it; nothing else does.
func (s *Server) pingEvery() time.Duration {
	if s == nil {
		return pingPeriod
	}
	s.revokedMu.RLock()
	d := s.wsPingEvery
	s.revokedMu.RUnlock()
	if d > 0 {
		return d
	}
	return pingPeriod
}

// principal is who the current request is acting as. Both routes lead to the
// same single administrator; what differs is how the claim was made, which is
// what the CSRF and token-management rules key off.
type principal struct {
	// username is the signed-in account, empty for Bearer authentication.
	username string
	// token is set only for Bearer authentication.
	token *db.APIToken
	// userID and epoch describe a SESSION principal, and are both zero for
	// Bearer authentication.
	//
	// They are carried here rather than re-read where they are needed because
	// re-reading them means calling s.authenticate again, and
	// TestOnlyTwoFunctionsAuthenticate exists to keep the number of places that
	// resolve a principal at two. The one consumer is handleWS, which needs to
	// ask, on a socket that is already open, whether the epoch this session was
	// signed against is still current -- see the note there (#159).
	userID int64
	epoch  int64
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
	// Subject is the user id, written by Issue. A token that got this far has a
	// valid signature and has already passed checkEpoch, so a subject that will
	// not parse is not a credential problem -- it is a token this build did not
	// mint. Zero then.
	//
	// A zero here makes the socket check in handleWS fail OPEN, not closed:
	// both `sessionUser != 0` there and `if userID == 0 { return false }` in
	// sessionEpochChanged skip the re-check, so such a socket would never be
	// closed by a password change. That is unreachable in this build --
	// auth.checkEpoch (internal/auth/auth.go:141) refuses an unparseable
	// subject before authenticate returns, pinned by
	// TestATokenWithAnUnparseableSubjectIsRefused -- and it is written down
	// this way round because the previous wording claimed the opposite. A
	// comment that says "fails closed" over code that fails open is worth less
	// than no comment: it is the thing a reader trusts instead of checking, and
	// this PR closed five defects of exactly that shape.
	//
	// If checkEpoch ever stops guarding this, the fix belongs there or in an
	// explicit refusal here -- not in restoring the sentence.
	userID, _ := strconv.ParseInt(claims.Subject, 10, 64)
	return &principal{username: claims.Username, userID: userID, epoch: claims.Epoch}, nil
}

// readScopeWritePatterns are the POST routes a read-scoped token may still
// reach: the ones that answer a question rather than change anything.
//
// They are POSTs because they carry a body or make an outbound call, not
// because they write. Each was read before it was listed, and one candidate did
// not survive that reading: POST /destinations/{id}/expert/dry-run SPAWNS
// FFMPEG with an argument list the caller supplied, which is the opposite of
// writing nothing whatever its route name suggests. POST
// /platforms/credentials/{platform}/check is absent for the same kind of
// reason -- it handles stored credentials.
//
// Keyed by chi's route PATTERN rather than the request path, so an id in the
// URL cannot smuggle a request onto the list and no string matching has to be
// invented for the {id} segment.
//
// The list is additive and that is its safety property: a route missing from it
// is denied to read tokens, so the failure mode of forgetting to maintain it is
// a monitoring script getting a 403 that somebody notices, never a write
// getting through unannounced.
//
// The expert preview route USED TO BE ON THIS LIST and is deliberately not any
// more. Its response is the resolved FFmpeg argv, which contains the
// destination's stream key in the output target -- so a read token reached the
// credential through a POST this list was hand-built to permit. The comment
// above reasons entirely about whether each candidate WRITES; it never asks
// what the route RETURNS, which is the method-shaped rule's blind spot
// reproduced inside its own exception list. The argv is not masked, because
// expert mode's contract is that the command shown is the command that runs;
// the route is denied to read tokens instead. See readScopeDeniedPatterns.
var readScopeWritePatterns = map[string]bool{
	"/api/v1/version/check":   true,
	"/api/v1/routing/compile": true,
}

// readScopeDeniedPatterns are the GETs a read-scoped token may NOT make.
//
// The method rule says a GET is a read. For these five it is false, in one of
// two ways, and both were found by enumerating the route table rather than by
// sampling it:
//
//	THE RESPONSE IS A CREDENTIAL. GET /destinations/{id}/expert returns the
//	resolved argv, whose output target is rtmps://host/app/<streamKey>. Every
//	other egress of an argv in this process is masked by alerts.Redact inside
//	supervisor; this one is deliberately raw because expert mode's whole
//	promise is that the command an operator approves is the command that runs.
//	Masking it would break that approval, so the route is denied instead.
//
//	THE GET IS NOT A READ. /clipper/.../keyframes spawns one ffprobe per
//	overlapping timeline part, with no cap on requests.
//	/platforms/accounts/{id}/stats and /metadata/broadcast-window each call a
//	third party and can REFRESH AND PERSIST an OAuth token -- GETs that write
//	the database and burn somebody else's quota, one call per connected account
//	in the broadcast-window case.
//
// Keyed by chi's route PATTERN for the same reason the allowlist is: an id in
// the URL must not be able to smuggle a request past the check.
//
// This list is SUBTRACTIVE, which is the opposite safety property to the
// allowlist above, and that asymmetry is deliberate rather than sloppy:
// forgetting to add a route here leaves a read token reaching something it
// perhaps should not, so the recurrence guard for this half is a TEST that
// drives each of these through the real router and asserts the 403 -- paired
// with the same route reaching an admin, so a change that merely broke the
// route for everybody cannot pass. See
// TestReadTokenIsDeniedTheRoutesThatAreNotReads.
//
// The three playout routes are ABSENT from this list and the reason is not
// that they were judged harmless. It is that this map CANNOT REACH THEM. It is
// consulted in exactly one place, inside requireScope, and requireScope is
// middleware on the authenticated group; /playout/*, /playout/public and
// /playout/poster.jpg are all registered outside it, because a viewer has no
// session. An entry added here for any of them would be a rule that reads as
// enforcement and enforces nothing -- which is what this whole round of work
// has been about deleting.
//
// What actually gates them is playoutOperator, inside authorizePlayout, which
// each of the three handlers calls per request. See TestPlayoutGateMatrix and
// TestPlayoutPosterVerdict.
//
// The poster's throttling was the old argument for leaving it alone and it is
// still true -- a 10-second cache under a mutex and an 8-second timeout, so ~6
// execs a minute however many callers ask -- but it was an answer to "is this
// GET really a read", and the question that mattered was who gets the frame.
// THE RESPONSE IS CONTENT, NOT METADATA is the third reason a GET is denied,
// added for #154, and it is a different claim from the two above: these routes
// are reads, they are cheap, and they leak no credential. They are denied
// because of what `read` was decided to MEAN -- a monitoring script, a
// dashboard, a status page. None of those need the bytes.
//
// The line is drawn at content, not at the archive: a read token still lists
// recordings, clips, stems and sessions, still sees durations, sizes and
// status, and still sees WHETHER a recording has a transcript. What it no
// longer gets is the media itself or the words.
//
// /library/search is on this list and it is the reason the list is eight
// entries rather than seven. The two /transcript routes are the obvious ones;
// search is not, because it reads as a metadata query. It returns
// db.TranscriptHit, which carries Text -- the full segment text -- plus
// Context (the neighbouring segments joined) and Speaker. A read token that
// iterated common words would reconstruct whole transcripts without ever
// naming a /transcript route. Denying only the endpoints whose PATH says
// transcript would have been a fix shaped by the URL rather than by the bytes,
// which is the same mistake as gating on the HTTP verb.
//
// GET /library still returns Speakers, the bare list of labels in the archive.
// That is left reachable deliberately: it is who appears, not what was said,
// and a dashboard that groups by speaker needs it. If that judgement is wrong
// it is one more line here, not a redesign.
var readScopeDeniedPatterns = map[string]bool{
	"/api/v1/destinations/{id}/expert":          true,
	"/api/v1/destinations/{id}/expert/preview":  true,
	"/api/v1/clipper/recordings/{id}/keyframes": true,
	"/api/v1/platforms/accounts/{id}/stats":     true,
	"/api/v1/metadata/broadcast-window":         true,

	// #154: content, not metadata. Media bytes.
	"/api/v1/recordings/{id}/download":             true,
	"/api/v1/recordings/stems/{name}/download":     true,
	"/api/v1/clips/{name}/download":                true,
	"/api/v1/clipper/jobs/{id}/download":           true,
	"/api/v1/library/recordings/{id}/media/{file}": true,
	// #154: content, not metadata. The words.
	"/api/v1/clipper/recordings/{id}/transcript": true,
	"/api/v1/library/recordings/{id}/transcript": true,
	"/api/v1/library/search":                     true,
}

// requireScope enforces what a token is ALLOWED to do, once requireAuth has
// settled who it is. It must run after requireAuth, which is what puts the
// principal in the context.
//
// The rule is shaped by METHOD, not by a table of routes: a read-scoped token
// gets GET and HEAD plus the short allowlist above, and everything else is 403.
// The alternative considered and rejected was classifying every route in the
// API as monitor/admin/session, which is a large diff whose real cost is
// permanent -- every route added afterwards has to be classified correctly by
// whoever adds it, and a route someone forgets to classify would default to
// reachable. Here a route added tomorrow is denied to read tokens by
// construction, with no list to remember to update.
//
// Session principals pass untouched. Scopes describe a token; the operator
// signed in at the console is not one, and the session-only group is a
// different question asked in a different place.
func (s *Server) requireScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principalFrom(r.Context())
		if !ok || p.token == nil || p.token.Scope == db.ScopeAdmin {
			next.ServeHTTP(w, r)
			return
		}
		// Anything that is not admin is treated as read-only, including a
		// scope string this build does not recognise. A value that arrived
		// from a newer schema or a hand-edited row should narrow what a
		// credential can do, never widen it.
		//
		// The denials are checked BEFORE the method, because their whole point
		// is that the method is the wrong question for them.
		if rc := chi.RouteContext(r.Context()); rc != nil && readScopeDeniedPatterns[rc.RoutePattern()] {
			writeError(w, http.StatusForbidden,
				"this API token is read-only; this endpoint returns a credential or "+
					"does real work, so it needs a token with the \"admin\" scope")
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		if rc := chi.RouteContext(r.Context()); rc != nil && readScopeWritePatterns[rc.RoutePattern()] {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusForbidden,
			"this API token is read-only; mint a token with the \"admin\" scope to make changes")
	})
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

// writeNoSuchEndpoint answers exactly as an unrouted /api path does.
//
// The bytes are duplicated from internal/web's API-miss branch rather than
// imported, because they are a WIRE CONTRACT between two packages that must not
// depend on each other: web is the embedded SPA and knows nothing about the
// route table, and api must not reach into the UI bundle to write a 404. The
// duplication is held honest by a test that drives BOTH through the real router
// and compares them byte for byte, so the copies cannot drift silently -- see
// TestWrongKickSecretIsIndistinguishableFromAnUnroutedPath.
//
// It exists for the one handler whose ROUTE IS ITS SECRET: a wrong secret on
// /api/v1/chat/kick/{secret} has to be indistinguishable from a path that is
// not mounted, and http.NotFound's text/plain body was not (#158).
func writeNoSuchEndpoint(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"no such endpoint"}` + "\n"))
}

// writeStoreError maps store errors onto sensible statuses so handlers do not
// each reinvent the mapping.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, db.ErrNoUser):
		writeError(w, http.StatusConflict, "setup has not been completed")
	case errors.Is(err, db.ErrStateConflict):
		// The sentence is passed through rather than replaced, because unlike
		// the two cases above this one is per-request: WHICH state blocked the
		// operation is the whole of what the operator needs to know, and it is
		// the store that knows it.
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// badRequestError carries a 400 OUT of a db.UpdateSettings closure, so the
// response is written after the store's settings lock has been released.
//
// The obvious shape -- call writeError inside the closure and tell the handler
// the response is already written -- puts a socket write inside that lock, and
// a client that stops reading its response can then hold it. That is the same
// hazard readJSONBody exists to remove, arriving from the other direction, and
// it would sit in the handler whose comments claim the network is kept out.
//
// So nothing inside a settings closure touches the ResponseWriter. It returns
// this, the handler matches it with errors.As once UpdateSettings has returned,
// and the message reaches the client unchanged.
type badRequestError struct{ msg string }

func (e badRequestError) Error() string { return e.msg }

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body, ok := readJSONBody(w, r)
	if !ok {
		return false
	}
	if err := decodeJSONInto(body, v); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

// readJSONBody buffers the request body, so that a caller holding a lock can
// take the NETWORK out of the locked span.
//
// The two settings handlers need this. They decode over the STORED document --
// that is what makes a partial payload safe -- so the decode has to happen
// inside db.UpdateSettings, which holds the store's settings mutex. Reading the
// body there would hold that mutex for as long as the body took to arrive, and
// the body arrives at the speed of the operator's network. ReadHeaderTimeout
// bounds the headers only (cmd/polyemesis/main.go), so a save from a phone on a
// dying connection would stall every other settings writer in the install --
// including the scheduler's sweep, which is not a request that can be retried
// by a human who noticed.
//
// 1 MB is far more than any polyemesis payload; the cap stops a malformed or
// malicious body from being buffered wholesale. Exceeding it fails here rather
// than at Decode, with the same status and the same message text.
func readJSONBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return nil, false
	}
	return body, true
}

// decodeJSONInto is decodeJSON's second half, over bytes already read. It
// returns badRequestError rather than writing, so a settings closure can use it
// without touching the ResponseWriter under the store's lock.
func decodeJSONInto(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequestError{"invalid request body: " + err.Error()}
	}
	return nil
}

func idParam(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, name), 10, 64)
}
