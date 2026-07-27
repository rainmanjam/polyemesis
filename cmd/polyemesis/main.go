// Command polyemesis is a self-hosted restreaming server with per-destination
// multichannel audio routing.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rainmanjam/polyemesis/internal/api"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
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
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("polyemesis", version)
		return nil
	}

	log := h.logger(*logLevel)

	h.progress("loading the configuration")
	cfg, err := config.Load(*configPath)
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

	h.progress("opening the database")
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer store.Close()

	box, err := secrets.LoadOrCreate(cfg.SecretPath())
	if err != nil {
		return err
	}

	bus := events.NewBroker()

	eng, err := engine.New(log, cfg, store, tools, bus)
	if err != nil {
		return err
	}
	h.progress("starting the streaming engine")
	if err := eng.Start(ctx); err != nil {
		return fmt.Errorf("starting the streaming engine: %w", err)
	}

	srv := api.New(api.Options{
		Log: log, Config: cfg,
		DB: store, Secrets: box, Engine: eng, Events: bus, Version: version,
		// The same provider the listener serves from. Handing the API its own
		// would mean a second selfsigned Provider regenerating the material on
		// disk out from under the running listener.
		TLS: provider,
	})
	go srv.RefreshLoop(ctx)

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
		redirectServer = startHTTPHelper(log, cfg, provider)
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

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutdownCancel()
	if redirectServer != nil {
		// The redirect helper holds nothing a client can be mid-transfer on,
		// so it goes first and never delays the listener that does.
		_ = redirectServer.Shutdown(shutdownCtx)
	}
	_ = httpServer.Shutdown(shutdownCtx)

	cancel()
	eng.Stop()
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
// with no UI in which to fix the setting that broke startup. Returns nil when
// no helper is running, which the caller treats as "nothing to shut down".
func startHTTPHelper(log *slog.Logger, cfg config.Config, provider *tlsx.Provider) *http.Server {
	const addr = ":80"
	if listenPort(cfg.Addr) == "80" {
		return nil // the TLS listener already owns it
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if provider.Mode() == tlsx.ModeACME {
			log.Warn("cannot bind :80 for the acme http-01 challenge; certificate issuance will keep failing until port 80 reaches this host (free the port, grant CAP_NET_BIND_SERVICE, or forward it). Serving https meanwhile",
				"error", err)
		} else {
			log.Warn("cannot bind :80 to redirect plain http to https; serving https only", "error", err)
		}
		return nil
	}

	srv := &http.Server{
		Handler:           provider.HTTPChallengeHandler(redirectToHTTPS(cfg)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	log.Info("http redirect listening", "addr", addr, "acmeChallenge", provider.Mode() == tlsx.ModeACME)
	return srv
}

// redirectToHTTPS sends everything that is not an ACME challenge to the TLS
// listener.
func redirectToHTTPS(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := redirectHost(cfg, r.Host)
		if host == "" {
			http.Error(w, "no host to redirect to", http.StatusBadRequest)
			return
		}
		// 301 is what a browser caches for a page load, but it is also allowed
		// to rewrite the request to GET; 308 keeps the method and body for the
		// API clients that would otherwise silently lose their request.
		code := http.StatusMovedPermanently
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			code = http.StatusPermanentRedirect
		}
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), code)
	}
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

func listenPort(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	return ""
}

func reportStartup(log *slog.Logger, cfg config.Config, provider *tlsx.Provider, store *db.DB, tools *ffmpeg.Tools) error {
	hasUser, err := store.HasUser()
	if err != nil {
		return err
	}
	settings, err := store.GetSettings()
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
	fmt.Printf("  ingest      %s (port %d)\n", settings.Ingest.Mode, ingestPort(settings))
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

func ingestPort(s db.Settings) int {
	if s.Ingest.Mode == db.IngestRTMP {
		return s.Ingest.RTMP.Port
	}
	return s.Ingest.SRT.Port
}

func newLogger(level string) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(level)}))
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
