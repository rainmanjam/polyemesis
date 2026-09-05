// Command polyemesis is a self-hosted restreaming server with per-destination
// multichannel audio routing.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rainmanjam/polyemesis/internal/api"
	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/childcensus"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/diag"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/logtz"
	// Aliased: main.go already has a `hooks` type for the service lifecycle
	// callbacks, and that name is the older claim on it.
	webhooks "github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/secrets"
	"github.com/rainmanjam/polyemesis/internal/tlsx"
)

// version is stamped by the Makefile via -ldflags.
var version = "dev"

func main() {
	// Windows may have been started by the Service Control Manager, which needs
	// its own handshake before any work begins. Everywhere else, and on a
	// Windows console, runService reports "not mine" and we run interactively.
	handled, err := runService()
	if err == nil && !handled {
		err = run(nil)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\npolyemesis: %v\n\n", err)
		os.Exit(1)
	}
}

// hooks lets a platform service manager watch and drive the run loop. The
// Windows SCM is the only caller: it has to hear progress while a slow FFmpeg
// probe runs — silence reads as a hung start and gets the process killed — it
// needs a shutdown trigger besides SIGTERM, and it needs somewhere other than
// stderr to log, because a service has no stderr. Every method is nil-safe, so
// the interactive path passes nil and behaves exactly as it did before.
type hooks struct {
	// NewHandler, when set, replaces the stderr log handler.
	NewHandler func(slog.Level) slog.Handler
	// Progress names the startup phase about to begin.
	Progress func(phase string)
	// Ready fires once the listener is up and serving.
	Ready func()
	// Stopping fires as graceful teardown begins.
	Stopping func()
	// Stop is a shutdown trigger that sits alongside SIGINT/SIGTERM.
	Stop <-chan struct{}
}

func (h *hooks) logger(level string) *slog.Logger {
	l := parseLevel(level)
	if h == nil || h.NewHandler == nil {
		return newLogger(level)
	}
	return slog.New(h.NewHandler(l))
}

func (h *hooks) progress(phase string) {
	if h != nil && h.Progress != nil {
		h.Progress(phase)
	}
}

func (h *hooks) ready() {
	if h != nil && h.Ready != nil {
		h.Ready()
	}
}

func (h *hooks) stopping() {
	if h != nil && h.Stopping != nil {
		h.Stopping()
	}
}

// stopped returns the extra shutdown trigger. A nil channel blocks forever in a
// select, which is exactly what the interactive path wants.
func (h *hooks) stopped() <-chan struct{} {
	if h == nil {
		return nil
	}
	return h.Stop
}

func run(h *hooks) error {
	var (
		configPath  = flag.String("config", "config.yaml", "path to config.yaml")
		addr        = flag.String("addr", "", "HTTP listen address (overrides config)")
		dataDir     = flag.String("data", "", "data directory (overrides config)")
		ffmpegPath  = flag.String("ffmpeg", "", "path to the ffmpeg binary (overrides config)")
		ffprobePath = flag.String("ffprobe", "", "path to the ffprobe binary (overrides config)")
		logLevel    = flag.String("log", "info", "log level: debug, info, warn, error")
		showVersion = flag.Bool("version", false, "print the version and exit")
		resetPass   = flag.Bool("reset-admin", false, "set a new admin password and sign out every session, then exit")
		// #718. A password change ends SESSIONS; API tokens carry no epoch and
		// survive it. The command now always says which tokens survive, and
		// this is how an operator who has decided they are compromised ends
		// them from a shell they can reach. Opt-in rather than implied: routine
		// rotation is the common case and destroying every integration's
		// credential is the wrong default for it.
		resetRevoke = flag.Bool("revoke-api-tokens", false, "with -reset-admin, also delete every API token")
		verifyBak   = flag.String("verify-backup", "", "check that a backup directory holds a database that opens, then exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("polyemesis", version)
		return nil
	}

	// DEBUG MODE, WIRED HERE BECAUSE THE LOGGER IS BUILT HERE. The switch shares
	// its level with the handler, so changing it at runtime reaches every
	// component that was handed this logger at startup -- the engine, the
	// notifier, the supervisor -- rather than only whoever asks for a new one.
	//
	// The recorder wraps the handler rather than replacing it: the process keeps
	// logging exactly where it did, and the ring is a second consumer that stays
	// empty until an operator turns recording on. That is what makes it safe to
	// leave wired in permanently.
	diagSwitch := diag.NewSwitch(parseLevel(*logLevel))
	diagRecorder := diag.NewRecorder(diag.DefaultCapacity, nil)
	log := h.debugLogger(*logLevel, diagSwitch, diagRecorder)

	h.progress("loading the configuration")
	// An explicit --config that is absent refuses to start; the implicit
	// default name still defaults so a fresh box boots. flag.Visit reports
	// only flags that were actually set, which is the one signal that tells
	// the two apart. #644.
	cfg, err := configLoaderFor(flag.Visit)(*configPath)
	if err != nil {
		return err
	}
	// Flags win over the file, so a systemd unit or a container can override
	// paths without rewriting config.yaml.
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if *ffmpegPath != "" {
		cfg.FFmpeg.Binary = *ffmpegPath
	}
	if *ffprobePath != "" {
		cfg.FFmpeg.Probe = *ffprobePath
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}

	// BEFORE anything is started. A reset touches only the database and then
	// exits, so it must not bind a port, spawn a child or write a log file --
	// this is run on a box where the real server is usually already running, and
	// a second instance racing it for the listener would fail for a reason that
	// has nothing to do with the password.
	// Before anything else that touches state. update.sh calls this on the copy
	// it just took, and the whole point is that it answers about THAT
	// directory: it opens the file, walks it, and reads the schema, without
	// running a migration -- migrating the backup would move the copy forward
	// to the schema the operator is keeping a way back from. #643.
	if *verifyBak != "" {
		return verifyBackup(*verifyBak, os.Stdout)
	}

	if *resetPass {
		return resetAdmin(cfg, os.Stdin, os.Stdout, *resetRevoke)
	}
	// Text overlays need a font FILE, and the image polyemesis ships has no
	// system fonts at all -- fontconfig is installed and finds nothing. The
	// embedded copies are written out here so drawtext has a real path to open.
	//
	// Every startup, not just the first: it keeps the built-ins in step with
	// the binary across an upgrade, and repairs one that was truncated. An
	// operator's own fonts in the same directory are left alone.
	if err := ffmpeg.EnsureFonts(cfg.FontsDir()); err != nil {
		return err
	}

	// FFmpeg is checked before anything else is opened: a missing or too-old
	// binary is a hard, immediately actionable failure and there is no point
	// creating a database first.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h.progress("detecting ffmpeg")
	tools, err := ffmpeg.Detect(ctx, cfg.FFmpeg.Binary, cfg.FFmpeg.Probe)
	if err != nil {
		return err
	}
	log.Info("ffmpeg detected", "version", tools.Version, "path", tools.FFmpeg, "srt", tools.HasLibSRT)

	// The certificate is settled before anything long-lived is opened: an
	// unreadable cert pair or an unwritable tls/ directory is a configuration
	// mistake, and the operator should hear about it in the same breath as a
	// missing ffmpeg rather than after the engine has started.
	h.progress("preparing tls")
	provider, err := newTLSProvider(cfg)
	if err != nil {
		return err
	}
	log.Info("tls", "mode", provider.Mode(), "hostname", cfg.TLS.Hostname)

	// BEFORE the database, which is a move rather than an addition: this used
	// to sit below, because the only things that needed it were the OAuth
	// tables and MQTT, and both are handed it per call. db.Open now takes it
	// too -- destination stream keys are sealed with it, and Open is what
	// backfills the ones an older release left in plaintext -- so the key has
	// to exist before the handle does.
	//
	// A failure here is still fatal, and stays fatal for the same reason it
	// always was: an unreadable key file is not a reason to start up and
	// quietly serve an install whose credentials cannot be decrypted.
	box, err := secrets.LoadOrCreate(cfg.SecretPath())
	if err != nil {
		return err
	}

	h.progress("opening the database")
	store, err := db.Open(cfg.DBPath(), db.WithSecretBox(box))
	if err != nil {
		return err
	}
	defer store.Close()
	// One-shot, here rather than on internal/db's read path -- see
	// migrateLegacyPlaylistFilePath's comment for why.
	if err := migrateLegacyPlaylistFilePath(store, cfg.DataDir, log); err != nil {
		return err
	}
	// Also one-shot, and before anything can accept an upload: nothing else in
	// the product ever removes a staged file a killed process left behind. See
	// sweepUploadLeftovers.
	sweepUploadLeftovers(cfg.DataDir, log)

	bus := events.NewBroker()

	// One engine per source, under a manager that owns the set.
	//
	// The set may be EMPTY, and that is a boot rather than a failure. What
	// stood here said an install always has at least one source because the
	// database refuses to delete the last -- true of deletion, and never true
	// of an install that had not created its first. Start refuses only when
	// sources exist and not one of them came up; with none, the server comes
	// up and serves the Sources page, which is the only screen that can get
	// the operator out of this state.
	eng := engine.NewManager(log, cfg, store, tools, bus)
	h.progress("starting the streaming engine")
	if err := eng.Start(ctx); err != nil {
		return fmt.Errorf("starting the streaming engine: %w", err)
	}

	// After the engine, because the governor's sensors read from it, and before
	// the API, which cannot be given a queue after its handlers are built.
	h.progress("starting the background job queue")
	pp := startPostProd(ctx, log, cfg, store, eng, tools)

	// Retained MQTT telemetry. Started unconditionally and inert until an
	// operator switches it on: the runner watches the stored settings, so
	// enabling it does not need a restart. Nothing in the streaming path can
	// block on or fail because of a broker.
	stopMQTT := startMQTT(ctx, log, store, box, eng, version)
	defer func() {
		// Its own bounded context, because this runs after ctx is cancelled and
		// a clean `offline` is exactly what must still get out. Without it the
		// instance sits on the broker reading `online` until the keep-alive
		// times out.
		shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		stopMQTT(shutdown)
	}()

	// The unified chat fan-in. Built unconditionally: a Hub with nothing
	// attached is the difference between "no platform is connected" and "this
	// build has no chat", and the operator needs to be told which.
	hub := chat.New(
		chat.WithStore(store),
		chat.WithPublisher(bus),
		chat.WithLogger(log),
	)
	defer hub.Close()
	// Retention is an operator setting, and the stored value has to be applied
	// at startup as well as on save. Without this a longer window would work
	// until the first restart and then quietly revert to the built-in two
	// hours, taking the moderator's user-card history with it.
	//
	// A failed read leaves the Hub on its own defaults rather than stopping the
	// server: chat history is the most expendable data here, and refusing to
	// boot over it would be a wildly disproportionate response.
	// ONE SPEND COUNTER FOR THE PROCESS, created before anything that could
	// rebuild a model connector. ApplyAutomod runs here at boot and again on
	// every settings save; the connector is rebuilt each time and the hourly
	// allowance must not be. See internal/automod/budget.go.
	automodBudget := automod.NewBudget()

	if s, err := store.GetSettings(); err == nil {
		api.ApplyChatRetention(hub, s.Chat)
		// Automod, for the same reason: a matrix armed in the UI that quietly
		// reverts on restart is worse than one that never worked, because the
		// operator has already stopped checking it.
		api.ApplyAutomod(hub, store, box, log, s.Automod, automodBudget)
		// Alert delivery, for the third time and the same reason. Safe to call
		// before the engines exist: the Manager remembers the budget and hands
		// it to each engine as it is created, so this does not depend on
		// running after Start.
		api.ApplyAlertSettings(eng, s.Alerts)
	} else {
		log.Warn("chat retention settings unreadable; using the built-in defaults", "err", err)
	}

	// Lifecycle webhooks. One dispatcher for the whole process, handed to every
	// engine: a sequence number and a delivery log belong to the endpoint, and
	// an endpoint is subscribed to by the install rather than by one programme.
	//
	// Started unconditionally and inert until an operator adds a hook. The
	// dispatcher re-reads the table on a ticker, so creating one takes effect
	// without a restart -- the same shape as the alert notifier's rule cache.
	hookd := webhooks.NewDispatcher(log, webhooks.SourceFunc(func() ([]webhooks.Hook, error) {
		return store.EnabledHooks(box)
	}))
	go hookd.Run(ctx)
	eng.SetHooks(hookd)

	srv := api.New(api.Options{
		Diag:      diagRecorder,
		DiagLevel: diagSwitch,
		Log:       log, Config: cfg,
		DB: store, Secrets: box, Engine: eng, Events: bus, Version: version,
		Chat:          hub,
		AutomodBudget: automodBudget,
		Hooks:         hookd,
		// The same provider the listener serves from. Handing the API its own
		// would mean a second selfsigned Provider regenerating the material on
		// disk out from under the running listener.
		TLS: provider,
		// The post-production tier. Optional in the API's eyes; wired here so
		// the jobs page governs a real queue instead of answering 503.
		Jobs: pp.queue, Governor: pp.gov, Whisper: pp.whisper,
	})
	go srv.RefreshLoop(ctx)
	// Pre-announces scheduled Facebook broadcasts, so a show on a schedule has
	// an event page before it starts. Best-effort by construction: it runs
	// ahead of the stream and nothing downstream waits on it, so a failure
	// here never delays or blocks a go-live.
	go srv.PreannounceLoop(ctx)
	// Drives a platform's broadcast lifecycle: puts a YouTube broadcast on air
	// once bytes are arriving, and ends it when the operator turns the
	// destination off or deletes it.
	//
	// TWO STATEMENTS AND BOTH ARE REQUIRED. SetLifecycle gives it the UP/DOWN
	// edges the engine already derives, which is only latency; LifecycleLoop is
	// the sweep that re-derives everything from the destination rows and is what
	// actually makes it work. Wire the loop without the seam and a go-live waits
	// up to fifteen seconds; wire the seam without the loop and NOTHING
	// HAPPENS AT ALL -- a dropped edge is never recovered, a restart never
	// reconciles, and every broadcast on the box sits in "testing" for ever.
	eng.SetLifecycle(srv.Lifecycle())
	go srv.LifecycleLoop(ctx)

	// After the API, because the adapters refresh their tokens through it.
	// Nothing here fails the start: a platform that will not connect is a line
	// in the log and a state in the chat pane, not a reason to refuse to stream.
	if n := srv.StartChat(ctx); n > 0 {
		log.Info("chat connected", "platforms", n)
	}

	httpServer := &http.Server{
		Addr:      cfg.Addr,
		Handler:   srv.Handler(),
		TLSConfig: provider.TLSConfig(),
		// No WriteTimeout: the WebSocket and multi-gigabyte recording
		// downloads are both long-lived by design, and a write timeout would
		// sever them mid-transfer.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Only started when we are the ones terminating TLS; behind a proxy the
	// proxy owns port 80.
	var redirectServer *http.Server
	if provider.Enabled() {
		var bindErr error
		redirectServer, bindErr = startHTTPHelper(log, cfg, provider)
		// Told to the API as well as to the log, because the log is not where
		// anyone is looking. "Nothing is listening on :80" is the single most
		// common reason Let's Encrypt issuance never completes, and until now
		// the only record of it was one line at startup — which the operator
		// reading a certificate warning in a browser three weeks later has no
		// reason to go back and find. See internal/api/acme_preflight.go.
		if bindErr != nil {
			srv.SetHTTPHelperStatus(false, bindErr.Error())
		} else {
			srv.SetHTTPHelperStatus(true, "")
		}
	}

	if err := reportStartup(log, cfg, provider, store, tools); err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() {
		if provider.Enabled() {
			// Empty paths: the certificate comes from TLSConfig, which is
			// either the loaded pair or ACME's GetCertificate callback.
			errCh <- httpServer.ListenAndServeTLS("", "")
			return
		}
		errCh <- httpServer.ListenAndServe()
	}()

	// SIGINT/SIGTERM must tear the FFmpeg children down in order; leaving
	// orphaned encoders holding an RTMP connection is exactly the failure mode
	// process groups exist to prevent.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	h.ready()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			eng.Stop()
			return fmt.Errorf("http server: %w", err)
		}
	case sig := <-sigCh:
		log.Info("shutting down", "signal", sig.String())
	case <-h.stopped():
		// The Windows SCM asked us to stop. It falls through to the identical
		// teardown below, because finalising recordings is not something a
		// service stop gets to skip.
		log.Info("shutting down", "reason", "service stop requested")
	}

	h.stopping()

	// ONE DEADLINE FOR EVERY PHASE BELOW.
	//
	// This was 20 seconds for the HTTP servers, and each phase after it owned
	// a constant of its own: a 5s lifecycle drain, then 30 seconds PER ENGINE
	// stopped one after another. Nothing added them up, and their sum passes
	// TimeoutStopSec=45 on an install with two programmes. systemd then
	// SIGKILLs the cgroup, which truncates whatever was being recorded --
	// silently, at exactly the right file size. See
	// internal/engine/shutdown_budget.go. #645.
	//
	// Every phase now draws from this one context, so a slow HTTP shutdown
	// leaves less for the engines rather than adding to them, and the total
	// cannot exceed what systemd is waiting for.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), engine.ShutdownBudget)
	defer shutdownCancel()
	if redirectServer != nil {
		// The redirect helper holds nothing a client can be mid-transfer on,
		// so it goes first and never delays the listener that does.
		_ = redirectServer.Shutdown(shutdownCtx)
	}
	_ = httpServer.Shutdown(shutdownCtx)

	cancel()
	// AFTER cancel, BEFORE eng.Stop, and both halves of that placement matter.
	// After, because the sweep loop must not be running concurrently with this
	// pass. Before, because eng.Stop tears every destination down, and a clean
	// stop deliberately produces no DOWN edge that means "the operator ended
	// this" -- so this is the last moment at which a broadcast the operator DID
	// ask to end can still be ended.
	//
	// It has its own whole-drain budget (lifecycleDrainBudget) and it never puts
	// a broadcast live. An unclean shutdown -- a kill, a power cut -- is covered
	// instead by the platform's enableAutoStop plus the boot reconciliation the
	// next start performs before its first tick.
	srv.DrainLifecycleWithin(shutdownCtx)
	eng.StopWithin(shutdownCtx)
	warnIfShutdownOverran(shutdownCtx, log)
	reportSurvivingChildren(log, childcensus.Live())
	log.Info("goodbye")
	return nil
}

// newTLSProvider turns the config into a certificate provider. Resolution is
// delegated to the config package so the listener, the banner and the API all
// describe the same decision instead of each re-deriving it.
func newTLSProvider(cfg config.Config) (*tlsx.Provider, error) {
	mode := cfg.ResolvedTLSMode()
	opts := tlsx.Options{
		Mode:      tlsx.Mode(mode),
		ACMEEmail: cfg.TLS.ACMEEmail,
		CertFile:  cfg.TLS.CertFile,
		KeyFile:   cfg.TLS.KeyFile,
		DataDir:   cfg.DataDir,
	}
	// Only the modes that put a name in a certificate need one, and manual
	// mode takes whatever the operator's file already says.
	switch mode {
	case config.ModeACME, config.ModeSelfSigned:
		host, err := cfg.TLS.EffectiveHostname()
		if err != nil {
			return nil, err
		}
		opts.Hostname = host
	}
	return tlsx.New(opts)
}

// startHTTPHelper brings up the plain-HTTP companion on :80 — the ACME HTTP-01
// responder in acme mode, a permanent redirect to HTTPS in every mode.
//
// It fails soft on purpose. Port 80 is unbindable for an unprivileged process
// and is often already taken; aborting startup over it would leave the operator
// with no UI in which to fix the setting that broke startup. Returns a nil
// server when no helper is running, which the caller treats as "nothing to shut
// down", and the bind error separately — a failure to take :80 is something the
// ACME preflight has to be able to report, and "no server" alone does not
// distinguish it from "the main listener already owns the port".
func startHTTPHelper(log *slog.Logger, cfg config.Config, provider *tlsx.Provider) (*http.Server, error) {
	const addr = ":80"
	if listenPort(cfg.Addr) == "80" {
		return nil, nil // the TLS listener already owns it
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if provider.Mode() == tlsx.ModeACME {
			log.Warn("cannot bind :80 for the acme http-01 challenge; certificate issuance will keep failing until port 80 reaches this host (free the port, grant CAP_NET_BIND_SERVICE, or forward it). Serving https meanwhile",
				"error", err)
		} else {
			log.Warn("cannot bind :80 to redirect plain http to https; serving https only", "error", err)
		}
		return nil, err
	}

	srv := &http.Server{
		Handler:           provider.HTTPChallengeHandler(redirectToHTTPS(cfg)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	log.Info("http redirect listening", "addr", addr, "acmeChallenge", provider.Mode() == tlsx.ModeACME)
	return srv, nil
}

// redirectToHTTPS sends everything that is not an ACME challenge to the TLS
// listener.
//
// The destination is the configured hostname when there is one. When there is
// not, it is the Host header the client sent — which is attacker-controlled,
// and is why this function is more careful than a one-line http.Redirect.
//
// The exposure is narrow but real. An attacker cannot make a victim's browser
// send a forged Host to this box, so they cannot steer a victim directly. What
// they CAN do is send `Host: evil.example` themselves and have a shared cache
// in front of this server store the resulting `301 Location: https://evil.
// example/...` against that URL, then serve it to the next person who asks for
// it. A permanent, cacheable redirect carrying a value a stranger chose is the
// whole of that bug.
//
// So the two cases are treated differently, because they are different:
//
//   - Configured hostname: the destination is fixed and ours. Permanent and
//     cacheable, which is what a redirect to a stable name should be.
//   - Host from the request: validated for shape, then sent as a TEMPORARY
//     redirect with `Cache-Control: no-store` and `Vary: Host`, so no
//     intermediary may store it and hand it to a different client.
//
// Reflecting the request host at all is deliberate: an operator who has not set
// tls.hostname still reaches this box by IP or by some name their own network
// resolves, and refusing to redirect would break that for no gain — the client
// only ever arrives at the authority it already asked for.
func redirectToHTTPS(cfg config.Config) http.HandlerFunc {
	configured := strings.TrimSpace(cfg.TLS.Hostname) != ""

	return func(w http.ResponseWriter, r *http.Request) {
		host := redirectHost(cfg, r.Host)
		if host == "" {
			http.Error(w, "no host to redirect to", http.StatusBadRequest)
			return
		}

		// 301 is what a browser caches for a page load, but it is also allowed
		// to rewrite the request to GET; 308 keeps the method and body for the
		// API clients that would otherwise silently lose their request.
		permanent, temporary := http.StatusMovedPermanently, http.StatusFound
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			permanent, temporary = http.StatusPermanentRedirect, http.StatusTemporaryRedirect
		}

		code := permanent
		if !configured {
			if !plausibleHost(host) {
				http.Error(w, "unusable Host header", http.StatusBadRequest)
				return
			}
			code = temporary
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Add("Vary", "Host")
		}

		// The Location header carries the REQUEST URI verbatim, query string and
		// all, and a playout watch token travels in that query string. With
		// tls.hostname set -- the recommended production configuration -- this is
		// a 301/308, which is permanently cacheable by definition: every
		// intermediary and the browser's own redirect cache is then holding a
		// URL that contains a live watch credential, for as long as it likes.
		//
		// So no-store fires whenever there is a token to leak, in BOTH branches,
		// rather than only in the unconfigured one where it was put for an
		// unrelated reason (a Host header the client chose). Vary: Host goes with
		// it for the same reason it always did.
		//
		// Deliberately over-broad on the query test: ANY query string suppresses
		// caching, not just one whose parameter happens to be spelled "token".
		// The failure direction of over-matching is one uncached redirect; the
		// failure direction of under-matching is a permanently cached credential,
		// and this exact class shipped once already because a name-based rule did
		// not recognise the spelling in front of it.
		if r.URL.RawQuery != "" || carriesWatchPath(r.URL.Path) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Add("Vary", "Host")
		}

		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), code)
	}
}

// carriesWatchPath reports whether a path is under the public playout origin or
// the player page, which are the two places a watch token can arrive WITHOUT a
// query string -- a cookie handoff, or a path a future release moves it into.
//
// Belt and braces beside the query test above. Neither is a boundary; together
// they are the answer to "could this Location possibly hold a credential", and
// the honest answer for these prefixes is yes.
func carriesWatchPath(path string) bool {
	return strings.HasPrefix(path, api.PlayoutPrefix) ||
		path == api.WatchPath || strings.HasPrefix(path, api.WatchPath+"/")
}

// plausibleHost reports whether a host:port taken from a request header is
// shaped like an authority and nothing else.
//
// This is not a security boundary on its own — the no-store temporary redirect
// above is what contains the actual risk — but a Host header is raw client
// input, and letting anything at all through into a Location value invites the
// next reader to assume it was checked somewhere. Letters, digits, dots,
// hyphens, and the colon and brackets an IPv6 authority needs. Nothing else,
// and nothing empty.
func plausibleHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, c := range host {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '-' || c == ':' || c == '[' || c == ']':
		default:
			return false
		}
	}
	return true
}

// redirectHost builds the authority to redirect to: the configured hostname
// when there is one (the certificate is only valid for that name anyway),
// otherwise whatever the client asked for, carrying the port the TLS listener
// is actually on.
func redirectHost(cfg config.Config, requestHost string) string {
	host := strings.TrimSpace(cfg.TLS.Hostname)
	if host == "" {
		host = requestHost
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
	}
	if host == "" {
		return ""
	}
	if port := listenPort(cfg.Addr); port != "" && port != "443" {
		return net.JoinHostPort(host, port)
	}
	return host
}

// listenPort delegates to config so there is ONE answer to "what port is this
// addr", shared with config.TLSPortWarning. Two copies of this could disagree,
// and the warning would then describe a port the redirect does not use.
func listenPort(addr string) string { return config.ListenPort(addr) }

func reportStartup(log *slog.Logger, cfg config.Config, provider *tlsx.Provider, store *db.DB, tools *ffmpeg.Tools) error {
	hasUser, err := store.HasUser()
	if err != nil {
		return err
	}
	// THE BACKUP THAT SILENTLY STOPPED BEING ONE (#557).
	//
	// If this boot sealed stream keys, it just upgraded an install whose
	// polyemesis.db WAS a complete destination backup and no longer is: the
	// keys live in secret.key from now on. An operator whose routine copies the
	// database alone has, as of this second, a backup that restores their
	// destinations with empty stream keys -- and restoring it is when they find
	// out, which is the worst possible moment.
	//
	// Said HERE because here is the only place that can say it. The helper
	// scripts that would have checked ship inside the version being upgraded
	// TO, so a 0.6.0 install runs 0.6.0's update.sh: the guard arrives with the
	// thing it was meant to guard. The first boot on the new code is the one
	// moment the software is both new enough to know and early enough to matter.
	//
	// Error level, not warn, and once rather than every boot: this is a
	// one-time transition and an operator who misses it loses credentials they
	// cannot recover.
	if n := store.SealedOnOpen(); n > 0 {
		log.Error("your database is no longer a complete backup of your destinations",
			"sealedStreamKeys", n,
			"action", "back up secret.key alongside polyemesis.db from now on",
			"why", "this upgrade moved stream keys out of the database into secret.key; "+
				"a backup of polyemesis.db alone will restore these destinations with empty keys",
			"issue", "https://github.com/rainmanjam/polyemesis/issues/557")
	}

	settings, err := store.GetSettings()
	if err != nil {
		return err
	}
	// The log's zone, as early as the store can answer for it. Lines written
	// before this -- the ones about opening the database -- are UTC, which is
	// what every line was before this setting existed.
	if tz := strings.TrimSpace(settings.Display.TimeZone); tz != "" {
		if loc, lerr := time.LoadLocation(tz); lerr == nil {
			logtz.Set(loc)
		} else {
			log.Warn("display time zone does not resolve; logging in UTC",
				"zone", tz, "err", lerr)
		}
	}
	// How many programmes this install has, which is a different question from
	// what settings.ingest says. Nothing reads settings.ingest any more -- the
	// engine takes its ingest from the source row -- so on an install with no
	// source that block describes a listener nobody is behind. See the ingest
	// line below.
	sources, err := store.ListSources()
	if err != nil {
		return err
	}

	scheme := "http"
	if provider.Enabled() {
		scheme = "https"
	}
	shown := cfg.Addr
	if host := strings.TrimSpace(cfg.TLS.Hostname); host != "" && provider.Enabled() {
		shown = host
		if port := listenPort(cfg.Addr); port != "" && port != "443" {
			shown = net.JoinHostPort(host, port)
		}
	} else if strings.HasPrefix(shown, ":") {
		shown = "localhost" + shown
	}

	fmt.Printf("\n  polyemesis %s\n", version)
	fmt.Printf("  web ui      %s://%s\n", scheme, shown)
	// NO SOURCE, NO INGEST, whatever settings.ingest happens to say -- and when
	// there IS one, the ingest reported is the SOURCE's, not the singleton's.
	//
	// This line is the entire user interface of a headless first run: nobody has
	// opened the web UI yet, and these eight lines are the only thing telling an
	// operator where to point an encoder. `ingest srt (port 6000)` on a box with
	// no source is a false statement of exactly the kind that costs an evening --
	// the port really is bound (both listeners bind unconditionally), so nothing
	// downstream contradicts it, and the encoder is simply refused by a server
	// that has no programme to admit it to.
	//
	// The second half is what ingestLine now fixes, and this comment used to
	// defer it: "deliberately NOT extended to naming the DEFAULT SOURCE's ingest
	// when there is one ... a separate change with its own decision about which
	// programme a banner speaks for". A deployment settled the decision. An
	// upgraded box that had been streaming over SRT for weeks printed "ingest not
	// chosen yet -- pick SRT, RTMP or pull in the web UI" on every boot, because
	// its source carried the SRT block and the settings copy had never been
	// written. The banner told a working install to reconfigure itself.
	fmt.Printf("  ingest      %s\n", ingestLine(sources, settings))
	fmt.Printf("  data dir    %s\n", cfg.DataDir)
	fmt.Printf("  ffmpeg      %s\n", tools.Version)
	reportTLS(cfg, provider, shown)
	if warn := tools.SRTWarning(); warn != "" {
		fmt.Printf("\n  WARNING: %s\n", warn)
	}
	if warn := cfg.InsecureExposureWarning(); warn != "" {
		fmt.Printf("\n  WARNING: %s\n", warn)
		log.Warn("insecure exposure", "detail", warn)
	}
	// TLS on, but not on 443. See config.TLSPortWarning for who this reaches:
	// not the operator who ran install.sh, which asks and defaults to 443, but
	// the one whose unit or compose file was written by hand and works well
	// enough that nobody looks at it again.
	if warn := cfg.TLSPortWarning(); warn != "" {
		fmt.Printf("\n  WARNING: %s\n", warn)
		log.Warn("tls on a non-standard port", "detail", warn)
	}
	if _, warn := cfg.HSTSPolicy(); warn != "" {
		fmt.Printf("\n  WARNING: %s\n", warn)
	}
	if !hasUser {
		fmt.Printf("\n  First run: open the web UI to set an admin password.\n")
	}
	if !api.UIBuilt() {
		fmt.Printf("\n  WARNING: no web UI is embedded in this binary.\n")
		fmt.Printf("           Run `make build` (which runs `npm --prefix ui run build` first).\n")
	}
	fmt.Println()
	return nil
}

// reportTLS prints what a browser is about to see.
//
// The CA fingerprint is printed here rather than left to the UI on purpose: the
// person reading this line is the one staring at a certificate warning, and the
// fingerprint is what tells them the warning is their own box and not something
// sitting in the middle of the connection.
func reportTLS(cfg config.Config, provider *tlsx.Provider, shown string) {
	line := string(provider.Mode())
	switch {
	case provider.Enabled() && cfg.TLS.Hostname != "":
		line += " (hostname " + cfg.TLS.Hostname + ")"
	case !provider.Enabled() && cfg.TrustProxyHeaders:
		line += " (a reverse proxy is expected to terminate tls)"
	}
	fmt.Printf("  tls         %s\n", line)
	if !provider.Enabled() {
		return
	}

	info, err := provider.CertInfo()
	switch {
	case errors.Is(err, tlsx.ErrNoCertificate):
		fmt.Printf("  certificate not issued yet; acme obtains one on the first https request\n")
	case err != nil:
		fmt.Printf("  certificate unreadable: %v\n", err)
	case info.Expired:
		fmt.Printf("  certificate %s — EXPIRED %s\n", info.Subject, info.NotAfter.Format("2006-01-02"))
	default:
		fmt.Printf("  certificate %s, expires %s (%d days)\n",
			info.Subject, info.NotAfter.Format("2006-01-02"), info.DaysRemaining)
	}

	if fp := provider.CAFingerprint(); fp != "" {
		fmt.Printf("  ca sha-256  %s\n", fp)
		fmt.Printf("              trust https://%s/api/v1/tls/ca (on disk: %s) to clear the browser warning\n",
			shown, cfg.SelfSignedCACertPath())
	}
}

// ingestLine describes the ingest an operator can actually publish to.
//
// IT READS THE SOURCES, NOT settings.Ingest, and that is the whole fix.
//
// Before sources existed the ingest configuration lived in the settings
// singleton and this line read it there. Sources own their ingest now, and
// settings.Ingest has quietly become something else: the DEFAULT a newly
// created source starts from. Those are not the same fact, and on an upgraded
// install they disagree.
//
// Observed on a real deployment, which is why this is a fix and not a tidy-up.
// The box had been streaming over SRT for weeks -- its source carried
// `{"mode":"srt", ...}` with a passphrase -- and the settings copy still held
// `"mode":""`, because nothing had ever written a default there. Every boot the
// banner said "ingest not chosen yet -- pick SRT, RTMP or pull in the web UI"
// to an operator whose ingest was up and receiving. The advice was not merely
// redundant, it was wrong: following it would have meant reconfiguring a
// working programme.
//
// The no-source case keeps the wording it already had. "Create a source" is the
// actual next step on a fresh install since #387, and it is the same language
// the API's own zero-source refusal uses ("this install has no source yet").
func ingestLine(sources []*db.Source, s db.Settings) string {
	if len(sources) == 0 {
		return "no programme yet — create a source in the web UI"
	}
	// Deduplicated in source order rather than through a map, so two sources
	// sharing a mode print it once and the order does not change between boots.
	var (
		seen  = make(map[db.IngestMode]bool, len(sources))
		parts = make([]string, 0, len(sources))
	)
	for _, src := range sources {
		if seen[src.Ingest.Mode] {
			continue
		}
		seen[src.Ingest.Mode] = true
		parts = append(parts, describeIngest(src.Ingest.Mode, s))
	}
	line := strings.Join(parts, ", ")
	// The count only when it changes the meaning. "srt (port 6000)" is complete
	// for one source; with three it invites the reading that one programme is
	// listening, when the port is shared by all of them.
	if len(sources) > 1 {
		line += fmt.Sprintf(" — %d sources", len(sources))
	}
	return line
}

// describeIngest names one mode, and the port a publisher aims at if there is
// one.
func describeIngest(mode db.IngestMode, s db.Settings) string {
	switch mode {
	case db.IngestUnset:
		// A source that exists with no mode chosen. Reachable: a programme can
		// be named before it is decided how the bytes arrive.
		return "not chosen yet — pick SRT, RTMP or pull in the web UI"
	case db.IngestPull:
		// Pull DIALS OUT. It is the one mode with no inbound port, and printing
		// one is worse than printing nothing: `ingest pull (port 6000)` is read
		// as "point the encoder at 6000", which in pull mode is an instruction
		// that can never work and sends the operator to their firewall to debug
		// a port that was never in the path.
		//
		// 6000 genuinely IS bound in pull mode -- both listeners bind
		// unconditionally (engine/manager.go, "BOTH LISTENERS BIND, ALWAYS") --
		// so the port is not wrong, only its attribution to THIS ingest.
		// engine.Manager.ListenerBound(db.IngestPull) already returns false and
		// says why: "pull dials out ... saying yes would tell an operator a
		// token gates an ingest that no publisher ever reaches".
		return "pull (dials out; no inbound port)"
	case db.IngestRTMP:
		return fmt.Sprintf("rtmp (port %d)", s.Listeners.RTMPPort)
	default:
		return fmt.Sprintf("%s (port %d)", mode, s.Listeners.SRTPort)
	}
}

// debugLogger builds the process logger with a runtime-switchable level and the
// debug recorder attached.
//
// It falls back to the plain logger when the harness has supplied its own
// handler, for the reason hooks.logger already has that branch: a test driving
// main must be able to capture output, and wrapping somebody else's handler in
// a recorder they did not ask for would change what they see.
func (h *hooks) debugLogger(level string, sw *diag.Switch, rec *diag.Recorder) *slog.Logger {
	if h != nil && h.NewHandler != nil {
		return h.logger(level)
	}
	base := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: sw.Leveler(),
		// Times in the install's display zone -- see internal/logtz. Read per
		// line, so a save takes effect on the next line rather than the next
		// restart.
		ReplaceAttr: logtz.ReplaceAttr,
	})
	return slog.New(diag.NewHandler(base, rec))
}

func newLogger(level string) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level:       parseLevel(level),
		ReplaceAttr: logtz.ReplaceAttr,
	}))
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// verifyBackup answers whether a backup directory can be restored from.
//
// A function rather than a block inside run() so it can be tested: the whole
// point of this flag is that it says no when the copy is unusable, and a check
// nobody has watched say no is a check nobody should trust. #643.
func verifyBackup(dir string, out io.Writer) error {
	if err := db.VerifyBackup(dir); err != nil {
		return fmt.Errorf("backup at %s is not usable: %w", dir, err)
	}
	fmt.Fprintf(out, "backup at %s opens, passes integrity_check and holds this server's schema\n", dir)
	return nil
}

// warnIfShutdownOverran says out loud that the shutdown budget was spent.
//
// The alternative is systemd killing the process and the operator finding a
// truncated recording with nothing in the log connecting the two. If this
// fires, a child did not answer SIGTERM inside engine.ShutdownBudget -- see
// #628 and #631.
//
// A function rather than an inline block so the branch can be tested. The
// whole value of the line is that it appears on the bad day, and a line nobody
// has watched appear is a line nobody should rely on. #645.
// reportSurvivingChildren says so when the teardown left something running.
//
// #631 was found on a production host by somebody running `ps` and noticing an
// ffmpeg whose ppid was the live server -- three weeks and 53 escalations after
// it started happening. Nothing inside the program could see it: a child whose
// handle has been lost is absent from every map, every status page and every
// log line, and present only in the process table.
//
// So this is the line that would have said it on the first occurrence. It is
// DETECTION, not control: the guards in internal/engine are what stop the
// orphan being created, and this is what makes it audible if one ever is again
// by a route nobody has thought of. Deliberately at the very end, after
// StopWithin has finished waiting, so anything still counted here has outlived
// the whole budget rather than merely being slow.
// Takes the census rather than reading it, so the reporting can be tested
// without spawning a process to be reported ON.
func reportSurvivingChildren(log *slog.Logger, live []childcensus.Child) {
	if len(live) == 0 {
		// SAYS SO, RATHER THAN SAYING NOTHING. #717.
		//
		// Silence here was read as an all-clear, and for most of this program's
		// life it could not have been one: the census covered supervisor
		// children only, so a transcode or a whisper child outliving the
		// shutdown produced exactly the silence #631 produced -- while this
		// function said nothing and the operator concluded the teardown was
		// clean.
		//
		// It now covers every spawner that can outlive a call, and
		// TestEverySpawnSiteIsAccountedFor is what keeps that true: a package
		// that spawns without enrolling, and without a stated reason, fails the
		// build. The line below is the claim; that test is what backs it.
		log.Info("no child outlived the shutdown",
			"scope", "every OS child this process spawned and enrolled: supervisor "+
				"children, media transcodes, and the transcription and live-caption workers")
		return
	}
	// Warn rather than Error: on a SIGKILLed child the census clears when the
	// reap lands, and a shutdown that ran out of budget has already said so on
	// the line above. What this adds is WHICH ones, by name.
	for _, c := range live {
		log.Warn("child outlived the shutdown; nothing in polyemesis will signal it again",
			"process", c.Name, "kind", c.Kind, "pid", c.PID, "up", time.Since(c.Since).String())
	}
}

func warnIfShutdownOverran(ctx context.Context, log *slog.Logger) {
	if ctx.Err() == nil {
		return
	}
	log.Warn("shutdown ran out of its budget; some children may have been killed rather than finishing",
		"budget", engine.ShutdownBudget.String())
}

// configLoaderFor decides whether an absent config file is fatal, which is the
// whole of #644.
//
// A --config path that does not exist used to fall back to defaults, and the
// defaults are not a smaller version of the operator's install -- they are a
// DIFFERENT one: a new secret.key, an empty database, plaintext on :8080 and an
// unauthenticated /setup reopened. A single typo therefore booted a working
// server that shared nothing with the one the operator meant to start, and
// nothing about it looked wrong.
//
// The signal that separates a typo from a fresh box is not the path's contents
// but whether the flag was PASSED AT ALL: an explicit --config is a claim that
// the file exists, while the default name is only a place to look. flag.Visit
// reports exactly the flags that were set, which is why the decision hangs on
// it rather than on comparing the path against the default string -- an
// operator who passes the default name explicitly means it just as much.
//
// Taking Visit as a parameter is what makes the decision testable without
// starting a server; run() passes the real flag.Visit. The alternative was
// leaving the one rule that stands between a typo and an empty install
// exercised only by a subprocess test.
func configLoaderFor(visit func(func(*flag.Flag))) func(string) (config.Config, error) {
	explicit := false
	visit(func(f *flag.Flag) {
		if f.Name == "config" {
			explicit = true
		}
	})
	if explicit {
		return config.LoadRequired
	}
	return config.Load
}
