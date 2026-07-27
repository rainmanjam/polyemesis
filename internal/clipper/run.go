package clipper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Running a cut is deliberately the smallest part of this package.
//
// Everything that decides anything — where the keyframes are, which segments a
// span touches, how far the in-point moved, what FFmpeg is going to be asked —
// happens in PlanCut and Commands, both pure. What is left here is process
// plumbing: write the sidecars, run the steps in order, publish the result
// atomically, and get out of the way when the context is cancelled.
//
// NOTHING HERE MAY COMPETE WITH THE LIVE STREAM. A precise cut re-encodes a
// fraction of a second, but a fast cut of a forty-minute segment still reads
// and writes gigabytes, and either can arrive while a broadcast is going out.
// So a Cutter is meant to be driven from the job queue under the resource
// policy, never straight from an HTTP handler: Request.HeadThreads is how the
// governor caps the encode, and a cancelled context kills the child process
// rather than waiting for it.

// ProgressFunc is how a running cut reports back. Both arguments are advisory;
// a nil ProgressFunc is fine.
type ProgressFunc func(fraction float64, message string)

// Result is a finished clip.
type Result struct {
	Plan    Plan          `json:"plan"`
	Path    string        `json:"path"`
	Bytes   int64         `json:"bytes"`
	Elapsed time.Duration `json:"elapsed"`
}

// Cutter executes plans.
type Cutter struct {
	log     *slog.Logger
	ffmpeg  string
	ffprobe string
	prober  Prober
	run     func(ctx context.Context, name string, args []string) ([]byte, error)
}

// Option customises a Cutter, chiefly for tests.
type Option func(*Cutter)

// WithRunner replaces the process runner, so the command lines can be asserted
// on without spawning FFmpeg.
func WithRunner(fn func(ctx context.Context, name string, args []string) ([]byte, error)) Option {
	return func(c *Cutter) { c.run = fn }
}

// WithProber replaces the keyframe prober, so planning can be driven from a
// known GOP structure.
func WithProber(p Prober) Option {
	return func(c *Cutter) { c.prober = p }
}

// New builds a Cutter around the detected binaries. Either path may be empty,
// in which case the binary is looked up on PATH at run time.
func New(log *slog.Logger, ffmpegBin, ffprobeBin string, opts ...Option) *Cutter {
	if log == nil {
		log = slog.Default()
	}
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	c := &Cutter{
		log:     log,
		ffmpeg:  ffmpegBin,
		ffprobe: ffprobeBin,
		run:     runCommand,
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.prober == nil {
		c.prober = FFprobe{Bin: ffprobeBin, Run: c.run}
	}
	return c
}

// Plan resolves a request without cutting anything, so a UI can show a human
// what the cut will do — including how far their in-point is about to move —
// before they commit to it.
func (c *Cutter) Plan(ctx context.Context, tl Timeline, req Request) (Plan, error) {
	return PlanWith(ctx, c.prober, tl, req)
}

// Cut plans and then executes in one call.
func (c *Cutter) Cut(ctx context.Context, tl Timeline, req Request, progress ProgressFunc) (Result, error) {
	p, err := c.Plan(ctx, tl, req)
	if err != nil {
		return Result{}, err
	}
	return c.Execute(ctx, p, progress)
}

// Execute carries out a plan.
//
// The clip only appears at its final path once every step has succeeded. The
// intermediates live in a temporary directory NEXT TO the output rather than in
// the system temp: a rename within one filesystem is atomic and free, while a
// rename across two is a copy of the whole clip and can half-succeed.
func (c *Cutter) Execute(ctx context.Context, p Plan, progress ProgressFunc) (Result, error) {
	if len(p.Sources) == 0 {
		return Result{}, ErrNoSegments
	}
	started := time.Now()

	dir := filepath.Dir(p.OutPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("clipper: create %s: %w", dir, err)
	}
	workDir, err := os.MkdirTemp(dir, ".clip-")
	if err != nil {
		return Result{}, fmt.Errorf("clipper: work directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(workDir); err != nil {
			c.log.Warn("clip work directory left behind", "dir", workDir, "err", err)
		}
	}()

	// The plan the caller keeps still names the real output; only the copy that
	// builds the commands is redirected, so Result.Plan reads the way a caller
	// expects.
	staged := p
	staged.OutPath = filepath.Join(workDir, "clip"+filepath.Ext(p.OutPath))

	cmds, err := staged.Commands(workDir)
	if err != nil {
		return Result{}, err
	}
	for i, cmd := range cmds {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		report(progress, float64(i)/float64(len(cmds)), cmd.Name)
		if err := c.runStep(ctx, cmd); err != nil {
			return Result{}, err
		}
	}

	info, err := os.Stat(staged.OutPath)
	if err != nil {
		return Result{}, fmt.Errorf("clipper: FFmpeg reported success but wrote no clip: %w", err)
	}
	if err := os.Rename(staged.OutPath, p.OutPath); err != nil {
		return Result{}, fmt.Errorf("clipper: publish %s: %w", p.OutPath, err)
	}
	report(progress, 1, "done")

	res := Result{Plan: p, Path: p.OutPath, Bytes: info.Size(), Elapsed: time.Since(started)}
	c.log.Info("clip cut",
		"path", p.OutPath,
		"mode", p.Mode,
		"seconds", fmt.Sprintf("%.3f", p.Duration().Seconds()),
		"inDriftMs", p.InDrift.Milliseconds(),
		"driftKnown", p.DriftKnown,
		"reencodedMs", p.HeadDuration.Milliseconds(),
		"sources", len(p.Sources),
		"bytes", res.Bytes,
		"elapsed", res.Elapsed.Round(time.Millisecond))
	return res, nil
}

// runStep writes a command's sidecars and runs it.
func (c *Cutter) runStep(ctx context.Context, cmd Command) error {
	for _, f := range cmd.Files {
		if err := os.WriteFile(f.Path, []byte(f.Content), 0o644); err != nil {
			return fmt.Errorf("clipper: write %s: %w", f.Path, err)
		}
	}
	run := c.run
	if run == nil {
		run = runCommand
	}
	if _, err := run(ctx, c.ffmpeg, cmd.Args); err != nil {
		// Cancellation is not a failure worth wrapping in FFmpeg's own words:
		// the caller asked for it and wants to see its own error back.
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, context.Canceled) {
			return ctxErr
		}
		return fmt.Errorf("clipper: %s step: %w", cmd.Name, err)
	}
	return nil
}

func report(fn ProgressFunc, fraction float64, msg string) {
	if fn == nil {
		return
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	fn(fraction, msg)
}
