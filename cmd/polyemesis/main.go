// Command polyemesis is a self-hosted restreaming server with per-destination
// multichannel audio routing.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
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
)

// version is stamped by the Makefile via -ldflags.
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\npolyemesis: %v\n\n", err)
		os.Exit(1)
	}
}

func run() error {
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

	log := newLogger(*logLevel)

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

	tools, err := ffmpeg.Detect(ctx, cfg.FFmpeg.Binary, cfg.FFmpeg.Probe)
	if err != nil {
		return err
	}
	log.Info("ffmpeg detected", "version", tools.Version, "path", tools.FFmpeg, "srt", tools.HasLibSRT)

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
	if err := eng.Start(ctx); err != nil {
		return fmt.Errorf("starting the streaming engine: %w", err)
	}

	srv := api.New(api.Options{
		Log: log, Config: cfg, ConfigPath: *configPath,
		DB: store, Secrets: box, Engine: eng, Events: bus, Version: version,
	})
	go srv.RefreshLoop(ctx)

	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: srv.Handler(),
		// No WriteTimeout: the WebSocket and multi-gigabyte recording
		// downloads are both long-lived by design, and a write timeout would
		// sever them mid-transfer.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	if err := reportStartup(log, cfg, store, tools); err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() {
		if cfg.TLS.Enabled {
			errCh <- httpServer.ListenAndServeTLS(cfg.TLS.CertFile, cfg.TLS.KeyFile)
			return
		}
		errCh <- httpServer.ListenAndServe()
	}()

	// SIGINT/SIGTERM must tear the FFmpeg children down in order; leaving
	// orphaned encoders holding an RTMP connection is exactly the failure mode
	// process groups exist to prevent.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			eng.Stop()
			return fmt.Errorf("http server: %w", err)
		}
	case sig := <-sigCh:
		log.Info("shutting down", "signal", sig.String())
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)

	cancel()
	eng.Stop()
	log.Info("goodbye")
	return nil
}

func reportStartup(log *slog.Logger, cfg config.Config, store *db.DB, tools *ffmpeg.Tools) error {
	hasUser, err := store.HasUser()
	if err != nil {
		return err
	}
	settings, err := store.GetSettings()
	if err != nil {
		return err
	}

	scheme := "http"
	if cfg.TLS.Enabled {
		scheme = "https"
	}
	shown := cfg.Addr
	if strings.HasPrefix(shown, ":") {
		shown = "localhost" + shown
	}

	fmt.Printf("\n  polyemesis %s\n", version)
	fmt.Printf("  web ui      %s://%s\n", scheme, shown)
	fmt.Printf("  ingest      %s (port %d)\n", settings.Ingest.Mode, ingestPort(settings))
	fmt.Printf("  data dir    %s\n", cfg.DataDir)
	fmt.Printf("  ffmpeg      %s\n", tools.Version)
	if warn := tools.SRTWarning(); warn != "" {
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

func ingestPort(s db.Settings) int {
	if s.Ingest.Mode == db.IngestRTMP {
		return s.Ingest.RTMP.Port
	}
	return s.Ingest.SRT.Port
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}
