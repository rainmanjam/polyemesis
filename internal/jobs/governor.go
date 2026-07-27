package jobs

// The resource governor: the part of this subsystem that decides whether the
// machine may spend anything on background work right now.
//
// The queue already knows how to hold work back — Defer parks a job until an
// instant, Pause stops claiming altogether — and it deliberately has no opinion
// about when to do either. This file is that opinion, and it is the reason the
// whole post-production tier is safe to ship on the same box as a live
// broadcast: a dropped frame is unrecoverable, a transcript that arrives an
// hour late costs nothing, so every gate here resolves in favour of the stream.
//
// Two design choices are worth stating up front.
//
// The governor reads the machine through FUNCTION VALUES, never through an
// import. It does not know that internal/engine exists or that internal/stats
// samples CPU; it is handed "is an ingest live" and "what is the CPU at" and
// that is all. That keeps the package free of an import cycle and makes the
// entire policy matrix a table test with a fake clock.
//
// Every gate FAILS OPEN. A sensor that is nil, a reading that is unavailable, a
// time zone that will not parse: none of them block work. This repo has learned
// four times that a check which is wrong in the restrictive direction is worse
// than no check, and a governor that silently stops transcribing forever
// because /sys was not mounted is exactly that failure.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mode is how one kind of work is allowed onto the machine.
type Mode string

const (
	// ModeRealtime runs immediately and accepts the CPU cost. It is for work
	// that is worthless late — live captions on the broadcast that is happening
	// now — and it is exempt from every gate except the machine-safety one.
	ModeRealtime Mode = "realtime"
	// ModeDeferred runs only when the governor permits. It is the default, and
	// it is what "yield to the stream" means in practice.
	ModeDeferred Mode = "deferred"
	// ModeScheduled runs only inside a window the operator chose, typically
	// overnight. Outside the window it is held back exactly like deferred work.
	ModeScheduled Mode = "scheduled"
	// ModeManual runs only when a human releases the job.
	ModeManual Mode = "manual"
)

// DefaultMode is what a kind with no policy of its own gets. Deferred, because
// the safe answer has to be the one nobody has to remember to choose.
const DefaultMode = ModeDeferred

// Valid reports whether m is a mode this package understands.
func (m Mode) Valid() bool {
	switch m {
	case ModeRealtime, ModeDeferred, ModeScheduled, ModeManual:
		return true
	}
	return false
}

// Suspension is what "pause" actually does to a kind that is already running.
type Suspension string

const (
	// SuspendStop means the processor registered a Suspender and its in-flight
	// work genuinely stops — SIGSTOP on the child, or a checkpoint. This is what
	// "pause" is supposed to mean and what a long transcription should provide.
	SuspendStop Suspension = "suspend"
	// SuspendFinish means the processor cannot be halted mid-run, so it is
	// allowed to finish and is simply not given more work. Stated explicitly
	// rather than pretended away: an operator who is told "paused" while an
	// FFmpeg is still chewing a core has been lied to.
	SuspendFinish Suspension = "finish-then-yield"
)

// Suspender is implemented by a processor whose running work can genuinely be
// halted and picked up again — a whisper.cpp child that can take SIGSTOP, a
// transcoder that can checkpoint between segments.
//
// It is per KIND, not per job: the processor already knows which of its own
// children are alive, and a kind-wide "stop what you are doing" is both simpler
// to implement correctly and impossible to leave half-applied.
//
// A kind that registers nothing gets SuspendFinish. That is a supported answer,
// not a failure.
type Suspender interface {
	// Suspend halts in-flight work. It must be idempotent and must not block.
	Suspend() error
	// Resume lets it continue. It must be idempotent.
	Resume() error
}

// Controller is the slice of *Queue the governor drives. An interface so the
// whole policy is testable against a recording fake, and narrow so it is
// obvious the governor can only ever hold work back — it cannot start a job,
// cancel one, or touch what a worker is doing.
type Controller interface {
	List(f Filter) ([]Job, error)
	Defer(id int64, at time.Time, reason string) error
	Pause()
	Resume()
	Paused() bool
}

var _ Controller = (*Queue)(nil)

// Governor defaults. They are deliberately cautious on the "hold work back"
// side and generous on the "let it run again" side, because the cost of the two
// mistakes is not symmetric.
const (
	// DefaultGovernorTick is how often the machine is sampled. Resource gating
	// is inherently periodic — CPU is a rolling average and the hysteresis is
	// measured in elapsed time — so this is a sampling clock, not a scheduler.
	DefaultGovernorTick = 5 * time.Second

	// DefaultDeferFor is how far ahead a blocked job is parked. Short on
	// purpose: the deferral is renewed every tick while the block lasts, so a
	// governor that dies costs at most this much lost work time rather than
	// stranding the queue. It is the same self-healing property StateDeferred
	// was designed around.
	DefaultDeferFor = 30 * time.Second
	// DefaultManualDeferFor parks work nobody has released. Long, because
	// nothing is coming to release it, and still finite for the same
	// self-healing reason.
	DefaultManualDeferFor = 30 * time.Minute

	// DefaultCPUCeilingPercent is the instantaneous ceiling above which no new
	// heavy job starts.
	DefaultCPUCeilingPercent = 85
	// DefaultCPUResumePercent is where the ceiling releases. The gap between
	// the two is the hysteresis that stops the governor oscillating on a load
	// that sits right on the line.
	DefaultCPUResumePercent = 65
	// DefaultCPUSustained is how long the ceiling must be exceeded before
	// RUNNING work is suspended, as opposed to merely not started. A transcode
	// is allowed to cause a two-second spike; it is not allowed to sit on the
	// machine for half a minute while the stream needs it.
	DefaultCPUSustained = 30 * time.Second
	// DefaultCPUSettle is how long it must be calm again before running work is
	// let go.
	DefaultCPUSettle = 20 * time.Second

	// DefaultIngestLinger keeps the ingest gate closed for a moment after the
	// stream stops. An encoder that dropped once usually drops again, and
	// pouncing on the machine in the gap is how a reconnect turns into a
	// failed reconnect.
	DefaultIngestLinger = 30 * time.Second

	// DefaultBatteryFloorPercent holds heavy work back on a laptop that is
	// already running down. Above it, battery is not treated as a reason to
	// refuse: plenty of machines run a whole show on mains-shaped batteries.
	DefaultBatteryFloorPercent = 40
	// DefaultThermalCeilingC is the machine-safety stop. Reaching it means the
	// CPU is about to throttle itself, which degrades the live stream far more
	// than any background job would, so this is the one gate REALTIME work also
	// obeys.
	DefaultThermalCeilingC = 90

	// DefaultNiceLevel is the OS priority every heavy child starts at. Ten is
	// enough that the stream wins every scheduling contest and small enough
	// that an otherwise idle machine still makes progress.
	DefaultNiceLevel = 10
	// MaxNiceLevel is the kernel's own ceiling.
	MaxNiceLevel = 19

	// MinutesPerDay is the exclusive upper bound on a window's start.
	MinutesPerDay = 24 * 60
	// MaxWindows bounds one kind's schedule.
	MaxWindows = 16
)

// Reasons a Verdict carries. They are compared in tests and shown to the
// operator, so they read as sentences rather than as codes.
const (
	ReasonAllowed       = "the machine has room for it"
	ReasonRealtime      = "this kind runs in realtime and is never held back"
	ReasonManualOnly    = "this kind only runs when a human asks for it"
	ReasonOutsideWindow = "outside the scheduled window"
	ReasonIngestLive    = "an ingest is live and the stream keeps the machine"
	ReasonCPUBusy       = "host cpu is above the ceiling"
	ReasonGPUBusy       = "the gpu is busy with the live stream"
	ReasonOnBattery     = "the machine is on battery"
	ReasonTooHot        = "the machine is too hot to take on more work"
	ReasonGovernorOff   = "the governor is switched off"
	ReasonReleased      = "a human released this job"
)

// ------------------------------------------------------------------- windows

// Window is a local wall-clock range a scheduled kind may run in.
//
// It may wrap midnight, which is the common case — 02:00-06:00 does not, but
// 22:00-06:00 does, and a naive start <= t < end comparison gets it silently
// wrong. A wrapping window belongs to the day its START falls on, so a Saturday
// 22:00-06:00 window runs into Sunday morning.
type Window struct {
	// TZ is the IANA zone the wall-clock fields are read in. Empty means UTC,
	// never the server's local time: "the machine happens to be in Denver" is
	// not a decision anybody made. Same rule as internal/scheduler.
	TZ string `json:"tz,omitempty"`
	// StartMinutes and EndMinutes are minutes past local midnight. End may be
	// MinutesPerDay to mean the following midnight; End before Start wraps.
	StartMinutes int `json:"startMinutes"`
	EndMinutes   int `json:"endMinutes"`
	// Days are the local weekdays the window opens on. Empty means every day.
	Days []time.Weekday `json:"days,omitempty"`
}

// Validate rejects a window that could not be evaluated.
func (w Window) Validate() error {
	if w.StartMinutes < 0 || w.StartMinutes >= MinutesPerDay {
		return fmt.Errorf("window start %d is outside 00:00-23:59", w.StartMinutes)
	}
	if w.EndMinutes < 0 || w.EndMinutes > MinutesPerDay {
		return fmt.Errorf("window end %d is outside 00:00-24:00", w.EndMinutes)
	}
	if w.StartMinutes == w.EndMinutes {
		return errors.New("window starts and ends at the same time, so it never opens")
	}
	for _, d := range w.Days {
		if d < time.Sunday || d > time.Saturday {
			return fmt.Errorf("window has an unknown weekday %d", int(d))
		}
	}
	if strings.TrimSpace(w.TZ) != "" {
		if _, err := time.LoadLocation(w.TZ); err != nil {
			return fmt.Errorf("window has an unknown time zone %q", w.TZ)
		}
	}
	return nil
}

// location resolves TZ, defaulting to UTC.
func (w Window) location() (*time.Location, error) {
	if strings.TrimSpace(w.TZ) == "" {
		return time.UTC, nil
	}
	return time.LoadLocation(w.TZ)
}

// Contains reports whether t falls inside the window.
//
// A zone that will not load reports TRUE. That is the fail-open direction: a
// broken zone string should not be the reason transcripts never appear, and the
// resource gates still apply on top of this one.
func (w Window) Contains(t time.Time) bool {
	loc, err := w.location()
	if err != nil {
		return true
	}
	if w.StartMinutes == w.EndMinutes {
		return false
	}
	local := t.In(loc)
	mins := local.Hour()*60 + local.Minute()

	if w.EndMinutes > w.StartMinutes {
		return w.opensOn(local.Weekday()) && mins >= w.StartMinutes && mins < w.EndMinutes
	}
	// Wrapped. Either we are after the start on a day the window opens, or
	// before the end on the morning after one.
	if w.opensOn(local.Weekday()) && mins >= w.StartMinutes {
		return true
	}
	return w.opensOn(local.AddDate(0, 0, -1).Weekday()) && mins < w.EndMinutes
}

func (w Window) opensOn(d time.Weekday) bool {
	if len(w.Days) == 0 {
		return true
	}
	for _, want := range w.Days {
		if want == d {
			return true
		}
	}
	return false
}

// InAnyWindow reports whether t falls inside any of them. An EMPTY list means
// always — a scheduled kind with no window is a configuration nobody finished,
// and refusing to ever run it would be a silent black hole.
func InAnyWindow(ws []Window, t time.Time) bool {
	if len(ws) == 0 {
		return true
	}
	for _, w := range ws {
		if w.Contains(t) {
			return true
		}
	}
	return false
}

// -------------------------------------------------------------------- policy

// KindPolicy is how one kind of work is governed.
type KindPolicy struct {
	Mode Mode `json:"mode"`
	// Windows applies to ModeScheduled only.
	Windows []Window `json:"windows,omitempty"`
	// UsesGPU marks work that would compete with a GPU-accelerated rendition
	// encoder. Only these kinds see the GPU gate, so a CPU-only transcode is
	// never held back by a busy GPU it was not going to touch.
	UsesGPU bool `json:"usesGpu,omitempty"`
	// IgnoreIngest exempts a kind from the yield-to-the-stream gate without
	// making it realtime. It exists for cheap work — writing a sidecar, hashing
	// a file — that is heavy enough to queue and light enough not to matter.
	IgnoreIngest bool `json:"ignoreIngest,omitempty"`
}

// Normalized fills the default mode.
func (k KindPolicy) Normalized() KindPolicy {
	if !k.Mode.Valid() {
		k.Mode = DefaultMode
	}
	return k
}

// Validate rejects a kind policy that could not be evaluated.
func (k KindPolicy) Validate() error {
	if !k.Mode.Valid() {
		return fmt.Errorf("unknown job mode %q (realtime, deferred, scheduled, manual)", k.Mode)
	}
	if len(k.Windows) > MaxWindows {
		return fmt.Errorf("a kind has %d windows (maximum %d)", len(k.Windows), MaxWindows)
	}
	for _, w := range k.Windows {
		if err := w.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// CPUPolicy is the host-CPU gate.
type CPUPolicy struct {
	// CeilingPercent is the instantaneous level above which nothing new starts.
	// Zero disables the gate entirely.
	CeilingPercent float64 `json:"ceilingPercent"`
	// ResumePercent is where it releases; it must be below the ceiling.
	ResumePercent float64 `json:"resumePercent"`
	// Sustained is how long the ceiling must be exceeded before running work is
	// suspended too.
	Sustained time.Duration `json:"sustained"`
	// Settle is how long it must be calm before running work is released.
	Settle time.Duration `json:"settle"`
}

// GPUPolicy is the GPU gate. Detection is thin on every platform, so the honest
// answer is a manual switch beside whatever we can measure.
type GPUPolicy struct {
	// AvoidWhenStreaming applies the gate to kinds marked UsesGPU whenever the
	// GPU is reported busy.
	AvoidWhenStreaming bool `json:"avoidWhenStreaming"`
	// Busy is the operator's own "the GPU is in use by streaming" switch. It is
	// OR-ed with whatever the sensor reports, because on most machines there is
	// no reliable way to ask, and guessing "free" would be the restrictive
	// mistake in the one direction that hurts the broadcast.
	Busy bool `json:"busy"`
}

// PowerPolicy is the best-effort laptop gate.
type PowerPolicy struct {
	// BatteryFloorPercent holds deferred work back on battery below this level.
	// Zero disables it.
	BatteryFloorPercent float64 `json:"batteryFloorPercent"`
	// ThermalCeilingC stops EVERYTHING, realtime included, because a machine
	// that is thermally throttling has already started degrading the stream.
	// Zero disables it.
	ThermalCeilingC float64 `json:"thermalCeilingC"`
}

// Policy is the whole resource policy.
type Policy struct {
	// Enabled false makes the governor inert: it releases anything it was
	// holding and then decides nothing. The gates are a safety feature, and a
	// safety feature you cannot switch off is one people work around.
	Enabled bool `json:"enabled"`
	// YieldToStream is the default and most important gate. With it on, an
	// ingest that is delivering holds back every deferred kind.
	YieldToStream bool `json:"yieldToStream"`

	// Default governs a kind nothing was configured for.
	Default KindPolicy `json:"default"`
	// Kinds are the per-kind overrides.
	Kinds map[Kind]KindPolicy `json:"kinds,omitempty"`

	CPU   CPUPolicy   `json:"cpu"`
	GPU   GPUPolicy   `json:"gpu"`
	Power PowerPolicy `json:"power"`

	// NiceLevel is the OS priority heavy children start at, 0..19. This one
	// applies regardless of every other policy: it is cheap insurance that even
	// an unpaused job loses the CPU to the stream.
	NiceLevel int `json:"niceLevel"`
	// IdleIO additionally drops the child to the idle IO class where ionice
	// exists. A transcode reading a recording off the same spindle the recorder
	// is writing to is the case this exists for.
	IdleIO bool `json:"idleIo"`

	// DeferFor and ManualDeferFor are how far ahead blocked work is parked.
	DeferFor       time.Duration `json:"deferFor"`
	ManualDeferFor time.Duration `json:"manualDeferFor"`
	// IngestLinger keeps the ingest gate closed after the stream stops.
	IngestLinger time.Duration `json:"ingestLinger"`
}

// DefaultPolicy is what a fresh install governs with: on, yielding to the
// stream, everything deferred, every heavy child niced.
func DefaultPolicy() Policy {
	return Policy{
		Enabled:       true,
		YieldToStream: true,
		Default:       KindPolicy{Mode: ModeDeferred},
		Kinds:         map[Kind]KindPolicy{},
		CPU: CPUPolicy{
			CeilingPercent: DefaultCPUCeilingPercent,
			ResumePercent:  DefaultCPUResumePercent,
			Sustained:      DefaultCPUSustained,
			Settle:         DefaultCPUSettle,
		},
		GPU: GPUPolicy{AvoidWhenStreaming: true},
		Power: PowerPolicy{
			BatteryFloorPercent: DefaultBatteryFloorPercent,
			ThermalCeilingC:     DefaultThermalCeilingC,
		},
		NiceLevel:      DefaultNiceLevel,
		IdleIO:         true,
		DeferFor:       DefaultDeferFor,
		ManualDeferFor: DefaultManualDeferFor,
		IngestLinger:   DefaultIngestLinger,
	}
}

// Normalized fills the defaults and clamps the rest, so a policy assembled from
// a half-filled settings form is one the governor can evaluate.
func (p Policy) Normalized() Policy {
	p.Default = p.Default.Normalized()
	if len(p.Kinds) > 0 {
		out := make(map[Kind]KindPolicy, len(p.Kinds))
		for k, kp := range p.Kinds {
			out[k] = kp.Normalized()
		}
		p.Kinds = out
	}
	if p.CPU.CeilingPercent < 0 {
		p.CPU.CeilingPercent = 0
	}
	if p.CPU.CeilingPercent > 100 {
		p.CPU.CeilingPercent = 100
	}
	// A resume level at or above the ceiling would leave no hysteresis at all
	// and the gate would chatter on every sample.
	if p.CPU.ResumePercent <= 0 || p.CPU.ResumePercent >= p.CPU.CeilingPercent {
		p.CPU.ResumePercent = p.CPU.CeilingPercent * 0.75
	}
	if p.CPU.Sustained <= 0 {
		p.CPU.Sustained = DefaultCPUSustained
	}
	if p.CPU.Settle <= 0 {
		p.CPU.Settle = DefaultCPUSettle
	}
	if p.NiceLevel < 0 {
		p.NiceLevel = 0
	}
	if p.NiceLevel > MaxNiceLevel {
		p.NiceLevel = MaxNiceLevel
	}
	if p.DeferFor <= 0 {
		p.DeferFor = DefaultDeferFor
	}
	if p.ManualDeferFor <= 0 {
		p.ManualDeferFor = DefaultManualDeferFor
	}
	if p.IngestLinger < 0 {
		p.IngestLinger = 0
	}
	return p
}

// Validate rejects a policy that could not be evaluated.
func (p Policy) Validate() error {
	if err := p.Default.Validate(); err != nil {
		return err
	}
	for k, kp := range p.Kinds {
		if err := kp.Validate(); err != nil {
			return fmt.Errorf("job kind %q: %w", k, err)
		}
	}
	if p.CPU.CeilingPercent < 0 || p.CPU.CeilingPercent > 100 {
		return fmt.Errorf("cpu ceiling %.0f%% out of range (0-100, 0 to disable)", p.CPU.CeilingPercent)
	}
	if p.CPU.ResumePercent < 0 || p.CPU.ResumePercent > 100 {
		return fmt.Errorf("cpu resume level %.0f%% out of range (0-100)", p.CPU.ResumePercent)
	}
	if p.Power.BatteryFloorPercent < 0 || p.Power.BatteryFloorPercent > 100 {
		return fmt.Errorf("battery floor %.0f%% out of range (0-100, 0 to disable)", p.Power.BatteryFloorPercent)
	}
	if p.Power.ThermalCeilingC < 0 || p.Power.ThermalCeilingC > 150 {
		return fmt.Errorf("thermal ceiling %.0f°C out of range (0-150, 0 to disable)", p.Power.ThermalCeilingC)
	}
	if p.NiceLevel < 0 || p.NiceLevel > MaxNiceLevel {
		return fmt.Errorf("nice level %d out of range (0-%d)", p.NiceLevel, MaxNiceLevel)
	}
	return nil
}

// For returns the policy governing one kind.
func (p Policy) For(kind Kind) KindPolicy {
	if kp, ok := p.Kinds[kind]; ok {
		return kp.Normalized()
	}
	return p.Default.Normalized()
}

// ------------------------------------------------------------------- sensors

// PowerState is what the machine's power and thermal sensors say.
//
// Known false means the platform told us nothing, which is the normal answer on
// most of them. It is not an error and it must never gate anything.
type PowerState struct {
	Known bool `json:"known"`
	// OnBattery is false when running on mains or when there is no battery.
	OnBattery bool `json:"onBattery"`
	// Percent is the charge level, or -1 when unknown.
	Percent float64 `json:"percent"`
	// TempC is the hottest sensor seen, or -1 when unknown.
	TempC float64 `json:"tempC"`
}

// UnknownPower is the reading that gates nothing.
func UnknownPower() PowerState { return PowerState{Percent: -1, TempC: -1} }

// Sensors is how the governor reads the machine. Every field is optional: a nil
// func means the gate it feeds never fires.
type Sensors struct {
	// IngestLive reports whether an ingest is delivering right now. It is a
	// predicate rather than an engine handle so this package never imports the
	// thing it is protecting.
	IngestLive func() bool
	// CPUPercent reports host CPU utilisation 0..100. Negative or NaN means the
	// reading is unavailable, which disables the gate rather than closing it.
	// internal/stats.Monitor.System().CPUPercent is the intended source.
	CPUPercent func() float64
	// GPUBusy reports whether the GPU is being used by the live path. Most
	// platforms cannot answer honestly, which is why Policy.GPU.Busy exists
	// beside it.
	GPUBusy func() bool
	// Power reports battery and thermal state. ReadPowerState is the built-in
	// best-effort implementation.
	Power func() PowerState
}

// ---------------------------------------------------------------- power probe

// powerRoots is every tree ReadPowerState reads. Fields rather than constants
// so the tests can point the whole probe at a fake /sys in a TempDir — none of
// this exists on a developer's macOS box.
type powerRoots struct {
	// supply is /sys/class/power_supply.
	supply string
	// thermal is /sys/class/thermal.
	thermal string
}

var systemPowerRoots = powerRoots{
	supply:  "/sys/class/power_supply",
	thermal: "/sys/class/thermal",
}

// ReadPowerState is the built-in best-effort battery and thermal probe.
//
// It reads sysfs and nothing else: no helper binary, no root, no polling
// daemon. On a platform that has no sysfs it reports Known false and every
// power gate stays open, which is the correct answer — "skip gracefully where
// the platform gives you nothing" is the requirement, and a wrong guess about
// heat would stop work forever on a machine that is perfectly cool.
func ReadPowerState() PowerState { return readPowerState(systemPowerRoots) }

func readPowerState(r powerRoots) PowerState {
	st := UnknownPower()

	if entries, err := os.ReadDir(r.supply); err == nil {
		for _, e := range entries {
			dir := filepath.Join(r.supply, e.Name())
			switch strings.TrimSpace(readFileString(filepath.Join(dir, "type"))) {
			case "Battery":
				st.Known = true
				// "Discharging" is the only status that means the wall is gone.
				// "Not charging" is a full battery on mains, and treating it as
				// battery would hold work back on a docked laptop forever.
				if strings.EqualFold(strings.TrimSpace(readFileString(filepath.Join(dir, "status"))), "discharging") {
					st.OnBattery = true
				}
				if n, ok := readFileInt(filepath.Join(dir, "capacity")); ok {
					st.Percent = float64(n)
				}
			case "Mains", "USB", "USB_PD", "USB_PD_DRP":
				if n, ok := readFileInt(filepath.Join(dir, "online")); ok && n == 1 {
					st.Known = true
					// A live mains supply overrides a battery that has not yet
					// updated its status; the wall is the ground truth.
					st.OnBattery = false
				}
			}
		}
	}

	if entries, err := os.ReadDir(r.thermal); err == nil {
		hottest := -1.0
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), "thermal_zone") {
				continue
			}
			n, ok := readFileInt(filepath.Join(r.thermal, e.Name(), "temp"))
			if !ok {
				continue
			}
			// Millidegrees, by the sysfs contract. A zone reporting an absurd
			// value is dropped rather than believed: a bogus 200°C would stop
			// every job on the box.
			c := float64(n) / 1000
			if c <= 0 || c > 150 {
				continue
			}
			if c > hottest {
				hottest = c
			}
		}
		if hottest > 0 {
			st.Known = true
			st.TempC = hottest
		}
	}
	return st
}

func readFileString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func readFileInt(path string) (int, bool) {
	s := strings.TrimSpace(readFileString(path))
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// ------------------------------------------------------------------ niceness

// NiceTools are the optional binaries that drop a child's OS priority.
//
// Wrapping the argv rather than setting SysProcAttr is a deliberate choice.
// setpriority is not portable — this product ships on Windows — and this file
// cannot grow per-GOOS siblings. More importantly nice(1) and ionice(1) EXEC
// their target rather than forking it, so the child's PID is still FFmpeg's:
// the supervisor's process-group kill keeps working exactly as before, which a
// wrapper that forked would have quietly broken.
//
// Both are optional, detected the way internal/ffmpeg detects FFmpeg, and
// absent means the command runs unwrapped rather than not at all.
type NiceTools struct {
	// Nice is the path to nice(1), empty when it is not installed.
	Nice string `json:"nice,omitempty"`
	// IONice is the path to ionice(1), empty when it is not installed. It is a
	// util-linux tool: macOS and Windows will not have it, and that is fine.
	IONice string `json:"ionice,omitempty"`
}

// DetectNiceTools looks for the priority tools on PATH. It never fails.
//
// Note the exact-name lookup. internal/ffmpeg/detect.go carries a hard-won
// lesson about substring matching — "h264" matching "h264_qsv" — and the same
// trap is here in a different costume: a fuzzy match on "nice" would happily
// find a binary called "notnice" and hand the stream's CPU budget to it.
func DetectNiceTools() NiceTools {
	var t NiceTools
	if p, err := exec.LookPath("nice"); err == nil {
		t.Nice = p
	}
	if p, err := exec.LookPath("ionice"); err == nil {
		t.IONice = p
	}
	return t
}

// Available reports whether anything at all can be applied.
func (t NiceTools) Available() bool { return t.Nice != "" || t.IONice != "" }

// Wrap rewrites a command so the child starts at low CPU and, optionally, low
// IO priority. A missing tool is skipped; with neither installed the command
// comes back untouched, because running the job un-niced is enormously better
// than not running it.
func (t NiceTools) Wrap(level int, idleIO bool, name string, args []string) (string, []string) {
	if level < 0 {
		level = 0
	}
	if level > MaxNiceLevel {
		level = MaxNiceLevel
	}
	out := append([]string{name}, args...)
	if t.Nice != "" && level > 0 {
		out = append([]string{t.Nice, "-n", strconv.Itoa(level)}, out...)
	}
	if idleIO && t.IONice != "" {
		// Class 3 is "idle": this process gets disk time only when nothing else
		// wants any, which is precisely the relationship a transcode should
		// have with the recorder writing segments to the same volume.
		out = append([]string{t.IONice, "-c", "3"}, out...)
	}
	return out[0], out[1:]
}

// ------------------------------------------------------------------ verdicts

// Gates is the machine-wide half of the decision, sampled once per tick and
// then applied to every kind.
type Gates struct {
	At time.Time `json:"at"`
	// IngestLive includes the linger period after the stream stopped.
	IngestLive bool `json:"ingestLive"`
	// CPUPercent is the reading, or -1 when unavailable.
	CPUPercent float64 `json:"cpuPercent"`
	// CPUOverCeiling is the instantaneous ceiling: nothing new starts.
	CPUOverCeiling bool `json:"cpuOverCeiling"`
	// CPUSustained is the ceiling held long enough that running work yields too.
	CPUSustained bool `json:"cpuSustained"`
	GPUBusy      bool `json:"gpuBusy"`
	// OnBattery is the battery gate, already compared against the floor.
	OnBattery bool `json:"onBattery"`
	// TooHot is the machine-safety stop that even realtime obeys.
	TooHot bool       `json:"tooHot"`
	Power  PowerState `json:"power"`
}

// Verdict is what the governor decided about one kind at one instant.
type Verdict struct {
	Kind Kind `json:"kind"`
	Mode Mode `json:"mode"`
	// Start is whether a queued job of this kind may be claimed now.
	Start bool `json:"start"`
	// Continue is whether a job of this kind that is ALREADY RUNNING may keep
	// the machine. It is a separate answer on purpose: most gates should stop
	// new work without throwing away an hour of a transcode that is nearly
	// done, and only the ones that mean "the stream needs this core back right
	// now" clear it.
	Continue bool `json:"continue"`
	// Suspension is what clearing Continue actually does to this kind, which
	// depends on whether its processor registered a Suspender.
	Suspension Suspension `json:"suspension"`
	Reason     string     `json:"reason"`
}

// decide is the whole policy matrix, as a pure function of the kind policy and
// the sampled gates. Everything else in this file is plumbing around it.
//
// The order of the checks is the order of the explanations: the reason an
// operator is shown is the FIRST thing that would have stopped the work, which
// is the one worth fixing.
func decide(kind Kind, kp KindPolicy, p Policy, g Gates, now time.Time) Verdict {
	v := Verdict{Kind: kind, Mode: kp.Mode, Start: true, Continue: true, Reason: ReasonAllowed}
	block := func(start, cont bool, reason string) Verdict {
		v.Start, v.Continue, v.Reason = start, cont, reason
		return v
	}

	// Machine safety first, and it is the only gate realtime obeys. A CPU that
	// is thermally throttling has already started degrading the broadcast, so
	// "accept the CPU cost" stops being a defensible trade.
	if g.TooHot {
		return block(false, false, ReasonTooHot)
	}
	if kp.Mode == ModeRealtime {
		return block(true, true, ReasonRealtime)
	}
	if kp.Mode == ModeManual {
		// Not Continue: a released job that is already running must not be
		// stopped by the mode it was released from.
		return block(false, true, ReasonManualOnly)
	}
	if kp.Mode == ModeScheduled && !InAnyWindow(kp.Windows, now) {
		// A job that started at 05:59 inside the window is allowed to finish
		// after 06:00. Killing it on the boundary would throw away the whole
		// night's work and it would still be there tomorrow.
		return block(false, true, ReasonOutsideWindow)
	}
	// The default and most important gate.
	if p.YieldToStream && g.IngestLive && !kp.IgnoreIngest {
		return block(false, false, ReasonIngestLive)
	}
	if g.CPUOverCeiling {
		return block(false, !g.CPUSustained, ReasonCPUBusy)
	}
	if kp.UsesGPU && g.GPUBusy && p.GPU.AvoidWhenStreaming {
		return block(false, false, ReasonGPUBusy)
	}
	if g.OnBattery {
		// Soft: finish what is running, start nothing more. Draining the last
		// of a laptop's battery on a transcript is rude; abandoning one that is
		// 90% done to save four minutes of charge is worse.
		return block(false, true, ReasonOnBattery)
	}
	return v
}

// ------------------------------------------------------------------ governor

// Snapshot is one whole decision, for a status endpoint and for tests.
type Snapshot struct {
	At      time.Time `json:"at"`
	Enabled bool      `json:"enabled"`
	Gates   Gates     `json:"gates"`
	// Verdicts covers every kind that has a policy or active work, sorted.
	Verdicts []Verdict `json:"verdicts"`
	// Deferred is the jobs this tick pushed back.
	Deferred []int64 `json:"deferred,omitempty"`
	// Suspended is the kinds whose running work is currently halted.
	Suspended []Kind `json:"suspended,omitempty"`
	// Yielding is the kinds that should be paused but cannot be, and are
	// finishing instead. Surfaced rather than hidden: an operator told
	// "paused" while an encoder still holds a core has been misled.
	Yielding []Kind `json:"yielding,omitempty"`
	Paused   bool   `json:"paused"`
}

// GovernorOption configures a Governor.
type GovernorOption func(*Governor)

// WithPolicy sets the resource policy.
func WithPolicy(p Policy) GovernorOption {
	return func(g *Governor) { g.policy = p.Normalized() }
}

// WithSensors sets how the machine is read.
func WithSensors(s Sensors) GovernorOption { return func(g *Governor) { g.sensors = s } }

// WithGovernorClock replaces time.Now, for tests.
func WithGovernorClock(fn func() time.Time) GovernorOption {
	return func(g *Governor) {
		if fn != nil {
			g.now = fn
		}
	}
}

// WithGovernorTick sets the sampling interval.
func WithGovernorTick(d time.Duration) GovernorOption {
	return func(g *Governor) {
		if d > 0 {
			g.tick = d
		}
	}
}

// WithNiceTools overrides the detected priority tools, for tests.
func WithNiceTools(t NiceTools) GovernorOption { return func(g *Governor) { g.nice = t } }

// WithGovernorOnChange registers a callback fired when the decision changes. It
// must not block.
func WithGovernorOnChange(fn func(Snapshot)) GovernorOption {
	return func(g *Governor) { g.onChange = fn }
}

// Governor holds background work back so the live stream keeps the machine.
//
// It actuates through exactly two of the queue's primitives. Blocked jobs are
// DEFERRED a short way ahead and re-deferred each tick, which is per-kind and
// self-healing — a governor that dies leaves work that comes back on its own
// rather than a queue that is stopped forever. The queue's global Pause is used
// only for the machine-safety stop, where "everything, immediately" is exactly
// what is meant and the bluntness is the point.
type Governor struct {
	log      *slog.Logger
	ctrl     Controller
	now      func() time.Time
	tick     time.Duration
	onChange func(Snapshot)
	nice     NiceTools

	mu         sync.Mutex
	policy     Policy
	sensors    Sensors
	suspenders map[Kind]Suspender
	suspended  map[Kind]bool
	// released is the jobs a human has let past the manual and window gates.
	// In memory on purpose: it does not survive a restart, and the direction of
	// that failure is a job running later than a human wanted rather than one
	// stranded forever.
	released map[int64]bool
	// releasedKinds is the same set folded up by kind, because admission is
	// decided per KIND — the store claims by kind, so that is the only
	// granularity the queue can ask at. Rebuilt from each tick's own listing,
	// which costs nothing extra.
	releasedKinds map[Kind]bool

	// Gate hysteresis. Zero times mean "not in that condition".
	lastLiveAt   time.Time
	cpuOverSince time.Time
	cpuLowSince  time.Time
	cpuSustained bool
	pausedByUs   bool

	last Snapshot
}

// NewGovernor creates a governor. It touches neither the queue nor a goroutine
// until Tick or Run is called.
func NewGovernor(log *slog.Logger, ctrl Controller, opts ...GovernorOption) *Governor {
	if log == nil {
		log = slog.Default()
	}
	g := &Governor{
		log:        log,
		ctrl:       ctrl,
		now:        time.Now,
		tick:       DefaultGovernorTick,
		policy:     DefaultPolicy(),
		nice:       DetectNiceTools(),
		suspenders: map[Kind]Suspender{},
		suspended:  map[Kind]bool{},
		released:   map[int64]bool{},

		releasedKinds: map[Kind]bool{},
	}
	for _, o := range opts {
		o(g)
	}
	return g
}

// RegisterSuspender records that this kind's running work can genuinely be
// halted. A kind without one gets SuspendFinish, which is a supported answer.
//
// Registering twice is an error rather than a silent replacement, for the same
// reason Register is: two processors fighting over a kind is a wiring bug.
func (g *Governor) RegisterSuspender(kind Kind, s Suspender) error {
	if kind == "" {
		return errors.New("job kind is required")
	}
	if s == nil {
		return fmt.Errorf("suspender for kind %q is nil", kind)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, dup := g.suspenders[kind]; dup {
		return fmt.Errorf("a suspender is already registered for kind %q", kind)
	}
	g.suspenders[kind] = s
	return nil
}

// SuspensionFor reports what pausing this kind actually does, so the UI can say
// so instead of implying every kind stops on a sixpence.
func (g *Governor) SuspensionFor(kind Kind) Suspension {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.suspensionLocked(kind)
}

func (g *Governor) suspensionLocked(kind Kind) Suspension {
	if _, ok := g.suspenders[kind]; ok {
		return SuspendStop
	}
	return SuspendFinish
}

// SetPolicy replaces the policy under a running governor.
func (g *Governor) SetPolicy(p Policy) {
	g.mu.Lock()
	g.policy = p.Normalized()
	g.mu.Unlock()
}

// Policy returns the policy in force.
func (g *Governor) Policy() Policy {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.policy
}

// SetSensors replaces how the machine is read.
func (g *Governor) SetSensors(s Sensors) {
	g.mu.Lock()
	g.sensors = s
	g.mu.Unlock()
}

// SetGPUBusy is the manual "the GPU is in use by streaming" switch. It exists
// because GPU contention is close to undetectable on most platforms, and an
// operator who knows their rendition ladder is on NVENC can say so rather than
// have us guess.
func (g *Governor) SetGPUBusy(busy bool) {
	g.mu.Lock()
	g.policy.GPU.Busy = busy
	g.mu.Unlock()
}

// Release lets one job past the MODE gates — manual, or outside its window. It
// deliberately does not override the resource gates: a human clicking run does
// not change the fact that a broadcast is live, and the whole point of this
// subsystem is that the stream wins that argument.
func (g *Governor) Release(id int64) {
	g.mu.Lock()
	g.released[id] = true
	g.mu.Unlock()
	g.pullForward(id)
}

// pullForward brings a released job's deferral back to now.
//
// Without this, "run it anyway" is decorative in exactly the case it exists
// for. A job blocked by a MODE gate is parked ManualDeferFor — half an hour —
// on the sound reasoning that nothing was coming to change a mode decision;
// then a human changes it. park() deliberately never brings a job forward,
// because a queued job whose AvailableAt is in the future may be serving a
// retry backoff and shortening that would quietly undo it. So the release path
// has to do it, and it only touches StateDeferred rows: a backoff is a QUEUED
// row with a future AvailableAt, and it is left exactly where it was.
func (g *Governor) pullForward(id int64) {
	active, err := g.ctrl.List(Active())
	if err != nil {
		// Nothing is lost that the next tick cannot redo, and refusing to
		// record the release over a transient listing error would be worse.
		g.log.Debug("governor cannot look up a released job", "job", id, "err", err)
		return
	}
	now := g.now()
	for _, j := range active {
		if j.ID != id {
			continue
		}
		// Admission is decided per kind, so the kind has to be admissible from
		// this instant rather than from the next tick — otherwise the queue
		// wakes on the deferral we are about to write and refuses the very job
		// the operator just released.
		g.mu.Lock()
		g.releasedKinds[j.Kind] = true
		g.mu.Unlock()

		if j.State != StateDeferred || !j.AvailableAt.After(now) {
			return
		}
		if err := g.ctrl.Defer(id, now, ReasonReleased); err != nil {
			g.log.Debug("governor cannot bring a released job forward", "job", id, "err", err)
		}
		return
	}
}

// Released reports whether a job has been released.
func (g *Governor) Released(id int64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.released[id]
}

// MayStart reports whether the queue may CLAIM a job of this kind right now.
// It is the callback handed to Queue.SetAdmit.
//
// Deferral alone cannot enforce a gate, and this is the correction to that.
// Tick pushes blocked work back, but the queue wakes on Submit and again the
// instant a job finishes — both of which land between two ticks — so a job
// could be claimed and started before the governor had ever looked at it. That
// is not a small window: a proxy encode of a short segment begins and ends
// inside it. Measured against a live ingest, it was the whole feature failing.
//
// Three things make this safe to call on the claim path:
//
//   - It fails open at every step. A nil governor, a disabled policy or a
//     sensor that cannot answer all admit the work.
//   - It reuses the last sample for everything with hysteresis — CPU, thermal,
//     battery — because those readings are deliberately smoothed over time and
//     re-sampling them per claim would both cost more and mean less.
//   - It re-reads the INGEST sensor live, because that one is a cheap predicate
//     and it is the gate this entire tier exists for. Waiting up to a full
//     sampling interval to notice that a broadcast has started is exactly the
//     window in which a transcode would take the machine.
func (g *Governor) MayStart(kind Kind) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	policy := g.policy
	if !policy.Enabled {
		g.mu.Unlock()
		return true
	}
	gates := g.last.Gates
	sensors := g.sensors
	lastLive := g.lastLiveAt
	released := g.releasedKinds[kind]
	g.mu.Unlock()

	now := g.now()
	if sensors.IngestLive != nil {
		if sensors.IngestLive() {
			gates.IngestLive = true
			g.mu.Lock()
			g.lastLiveAt = now
			g.mu.Unlock()
		} else {
			// The linger, applied here too: an encoder that dropped is often
			// about to reconnect, and grabbing the machine in the gap is how a
			// recoverable blip becomes a failed reconnect.
			gates.IngestLive = !lastLive.IsZero() && now.Sub(lastLive) < policy.IngestLinger
		}
	}

	kp := policy.For(kind)
	v := decide(kind, kp, policy, gates, now)
	if v.Start {
		return true
	}
	// A human clicked "run it anyway" on a job of this kind. That answers the
	// MODE question and nothing else, so the kind is re-decided as ordinary
	// deferred work — it still loses to a live ingest. Kind-granular because
	// the store claims by kind; the worst case is that a second job of the same
	// kind gets in behind the released one, which park() then pushes back.
	if released && isModeReason(v.Reason) {
		kp.Mode = ModeDeferred
		return decide(kind, kp, policy, gates, now).Start
	}
	return false
}

// Last returns the most recent decision.
func (g *Governor) Last() Snapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last
}

// NiceCommand rewrites a command line so the child starts at low OS priority.
// Processors call it for every heavy child they spawn; it applies regardless of
// mode and regardless of every other gate, because a realtime job that loses
// its scheduling contests is still doing its job while a stream that loses one
// has already dropped a frame.
func (g *Governor) NiceCommand(name string, args ...string) (string, []string) {
	g.mu.Lock()
	level, idleIO := g.policy.NiceLevel, g.policy.IdleIO
	tools := g.nice
	g.mu.Unlock()
	return tools.Wrap(level, idleIO, name, args)
}

// NiceTools reports which priority tools were found, for a diagnostics page.
func (g *Governor) NiceTools() NiceTools {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.nice
}

// Run governs until ctx ends.
//
// This is the one timer this package adds, and it is a SAMPLING clock rather
// than a scheduler: CPU is a rolling average, the hysteresis is measured in
// elapsed time, and neither internal/scheduler (which fires at occurrences and
// acts on destinations) nor the queue's own wake-driven loop offers anywhere to
// hang that. A caller that already ticks can drive Tick directly instead and
// never start this goroutine.
func (g *Governor) Run(ctx context.Context) {
	if g == nil {
		return
	}
	t := time.NewTicker(g.tick)
	defer t.Stop()
	g.Tick(g.now())
	for {
		select {
		case <-ctx.Done():
			// Leave the queue as we found it. A governor that exits holding the
			// pause would take the whole post-production tier down with it.
			g.release()
			return
		case <-t.C:
			g.Tick(g.now())
		}
	}
}

// Tick samples the machine, decides about every kind, and applies the decision.
//
// Exported and taking the time so the entire policy matrix — window boundaries,
// mode transitions, pause on ingest start, resume on ingest stop — is a table
// test with no ticker and no sleep in it.
func (g *Governor) Tick(now time.Time) Snapshot {
	if g == nil {
		return Snapshot{}
	}
	g.mu.Lock()
	policy := g.policy
	g.mu.Unlock()

	if !policy.Enabled {
		g.release()
		snap := Snapshot{At: now, Enabled: false, Paused: g.ctrl.Paused()}
		g.record(snap)
		return snap
	}

	gates := g.sample(now, policy)
	snap := Snapshot{At: now, Enabled: true, Gates: gates}

	// The machine-safety stop is the only thing that reaches for the global
	// pause: it means "nothing at all, immediately", which is exactly what a
	// blunt instrument is for.
	g.setPaused(gates.TooHot)

	verdicts := &verdictSet{g: g, policy: policy, gates: gates, now: now, byKind: map[Kind]Verdict{}}
	for k := range policy.Kinds {
		verdicts.of(k)
	}

	active, err := g.ctrl.List(Active())
	if err != nil {
		// Not fatal and not a reason to pause: a listing that failed says
		// nothing about whether the machine is busy, and stopping the queue on
		// a transient database error would be the restrictive mistake.
		g.log.Warn("governor cannot list active jobs", "err", err)
		snap.Verdicts = verdicts.sorted()
		snap.Paused = g.ctrl.Paused()
		g.record(snap)
		return snap
	}

	running := map[Kind]int{}
	for _, j := range active {
		v := verdicts.of(j.Kind)
		if j.State == StateRunning {
			running[j.Kind]++
			continue
		}
		if g.park(j, v, policy, gates, now) {
			snap.Deferred = append(snap.Deferred, j.ID)
		}
	}
	snap.Suspended, snap.Yielding = g.applySuspensions(running, verdicts.byKind)

	snap.Verdicts = verdicts.sorted()
	snap.Paused = g.ctrl.Paused()
	g.forgetFinished(active)
	g.record(snap)
	return snap
}

// park pushes one blocked job back, and reports whether it wrote anything.
func (g *Governor) park(j Job, v Verdict, p Policy, gates Gates, now time.Time) bool {
	if v.Start {
		return false
	}
	if isModeReason(v.Reason) && g.Released(j.ID) {
		// A human said run it. That answers the MODE question and nothing else,
		// so the job is re-decided as ordinary deferred work: it still loses to
		// a live ingest, a hot machine or a busy GPU.
		kp := p.For(j.Kind)
		kp.Mode = ModeDeferred
		v = decide(j.Kind, kp, p, gates, now)
		if v.Start {
			return false
		}
	}

	at := now.Add(p.DeferFor)
	if isModeReason(v.Reason) {
		// Nothing is coming to change a mode decision in the next half minute,
		// so re-asking every tick is pure write amplification.
		at = now.Add(p.ManualDeferFor)
	}
	// Never bring a job forward. A queued job whose AvailableAt is in the future
	// is serving a retry backoff, and parking it at a nearer instant would
	// quietly undo that.
	if j.AvailableAt.After(at) {
		at = j.AvailableAt
	}
	// Nothing to do for a job already parked past the point this tick would have
	// parked it: rewriting the row every few seconds for a queue full of
	// deferred work is a lot of SQLite for no new information.
	if j.State == StateDeferred && j.AvailableAt.After(now.Add(p.DeferFor/2)) {
		return false
	}
	if err := g.ctrl.Defer(j.ID, at, v.Reason); err != nil {
		// Benign in the common case: the queue claimed it between the listing
		// and here, and DeferJob refuses to touch a running job.
		g.log.Debug("governor could not defer a job", "job", j.ID, "kind", string(j.Kind), "err", err)
		return false
	}
	return true
}

// applySuspensions halts the kinds whose running work must yield and lets the
// rest go, reporting which kinds genuinely stopped and which are merely
// finishing because they cannot.
func (g *Governor) applySuspensions(running map[Kind]int, verdicts map[Kind]Verdict) (suspended, yielding []Kind) {
	for k, n := range running {
		v := verdicts[k]
		if v.Continue || n == 0 {
			g.resume(k)
			continue
		}
		if v.Suspension == SuspendStop {
			if g.suspend(k) {
				suspended = append(suspended, k)
			}
			continue
		}
		yielding = append(yielding, k)
	}
	// A kind with nothing running has nothing to hold, so release it: the next
	// job of that kind must not be born into a suspension it never earned.
	g.mu.Lock()
	idle := make([]Kind, 0, len(g.suspended))
	for k := range g.suspended {
		if running[k] == 0 {
			idle = append(idle, k)
		}
	}
	g.mu.Unlock()
	for _, k := range idle {
		g.resume(k)
	}

	sort.Slice(suspended, func(i, j int) bool { return suspended[i] < suspended[j] })
	sort.Slice(yielding, func(i, j int) bool { return yielding[i] < yielding[j] })
	return suspended, yielding
}

// isModeReason reports whether a block came from the mode rather than from the
// machine. Only these can be overridden by a human clicking run.
func isModeReason(reason string) bool {
	return reason == ReasonManualOnly || reason == ReasonOutsideWindow
}

// verdictSet decides about a kind once per tick and remembers the answer, so a
// queue holding forty jobs of one kind evaluates the policy once.
type verdictSet struct {
	g      *Governor
	policy Policy
	gates  Gates
	now    time.Time
	byKind map[Kind]Verdict
}

func (s *verdictSet) of(k Kind) Verdict {
	if v, ok := s.byKind[k]; ok {
		return v
	}
	v := decide(k, s.policy.For(k), s.policy, s.gates, s.now)
	v.Suspension = s.g.SuspensionFor(k)
	s.byKind[k] = v
	return v
}

func (s *verdictSet) sorted() []Verdict {
	out := make([]Verdict, 0, len(s.byKind))
	for _, v := range s.byKind {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// sample reads every sensor and folds the readings into the gates, applying the
// hysteresis that stops a load sitting on the threshold from oscillating.
func (g *Governor) sample(now time.Time, p Policy) Gates {
	g.mu.Lock()
	s := g.sensors
	g.mu.Unlock()

	gates := Gates{At: now, CPUPercent: -1, Power: UnknownPower()}

	if s.IngestLive != nil && s.IngestLive() {
		g.mu.Lock()
		g.lastLiveAt = now
		g.mu.Unlock()
		gates.IngestLive = true
	} else {
		g.mu.Lock()
		last := g.lastLiveAt
		g.mu.Unlock()
		// The linger: an encoder that dropped is often about to reconnect, and
		// grabbing the machine in the gap is how a recoverable blip becomes a
		// failed reconnect.
		gates.IngestLive = !last.IsZero() && now.Sub(last) < p.IngestLinger
	}

	if s.CPUPercent != nil && p.CPU.CeilingPercent > 0 {
		pct := s.CPUPercent()
		// NaN is caught explicitly: it fails every comparison, so a bare
		// threshold test would read it as "not busy" on one line and "busy" on
		// another depending which way the comparison was written.
		if !math.IsNaN(pct) && pct >= 0 {
			gates.CPUPercent = pct
			gates.CPUOverCeiling, gates.CPUSustained = g.cpuGate(now, p, pct)
		} else {
			g.clearCPUGate()
		}
	} else {
		g.clearCPUGate()
	}

	if p.GPU.Busy {
		gates.GPUBusy = true
	} else if s.GPUBusy != nil {
		gates.GPUBusy = s.GPUBusy()
	}

	if s.Power != nil {
		st := s.Power()
		gates.Power = st
		if st.Known {
			if p.Power.BatteryFloorPercent > 0 && st.OnBattery &&
				st.Percent >= 0 && st.Percent < p.Power.BatteryFloorPercent {
				gates.OnBattery = true
			}
			if p.Power.ThermalCeilingC > 0 && st.TempC > 0 && st.TempC >= p.Power.ThermalCeilingC {
				gates.TooHot = true
			}
		}
	}
	return gates
}

// cpuGate applies the two-level CPU rule: over the ceiling stops new work at
// once, and staying over it for Sustained stops running work too.
func (g *Governor) cpuGate(now time.Time, p Policy, pct float64) (over, sustained bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	switch {
	case pct >= p.CPU.CeilingPercent:
		g.cpuLowSince = time.Time{}
		if g.cpuOverSince.IsZero() {
			g.cpuOverSince = now
		}
		if now.Sub(g.cpuOverSince) >= p.CPU.Sustained {
			g.cpuSustained = true
		}
		return true, g.cpuSustained

	case pct < p.CPU.ResumePercent:
		g.cpuOverSince = time.Time{}
		if g.cpuLowSince.IsZero() {
			g.cpuLowSince = now
		}
		if now.Sub(g.cpuLowSince) >= p.CPU.Settle {
			g.cpuSustained = false
		}
		return false, g.cpuSustained

	default:
		// The dead band between the resume level and the ceiling. Both timers
		// are left alone: this is neither a reason to start counting towards a
		// suspension nor a reason to call the machine calm.
		return !g.cpuOverSince.IsZero(), g.cpuSustained
	}
}

func (g *Governor) clearCPUGate() {
	g.mu.Lock()
	g.cpuOverSince = time.Time{}
	g.cpuLowSince = time.Time{}
	g.cpuSustained = false
	g.mu.Unlock()
}

// suspend halts a kind's running work, reporting whether it is now halted.
func (g *Governor) suspend(k Kind) bool {
	g.mu.Lock()
	if g.suspended[k] {
		g.mu.Unlock()
		return true
	}
	s, ok := g.suspenders[k]
	g.mu.Unlock()
	if !ok {
		return false
	}
	if err := s.Suspend(); err != nil {
		// Not marked suspended, so the next tick tries again rather than
		// believing a stop that did not happen.
		g.log.Warn("governor cannot suspend a kind", "kind", string(k), "err", err)
		return false
	}
	g.mu.Lock()
	g.suspended[k] = true
	g.mu.Unlock()
	g.log.Info("background work suspended so the stream keeps the machine", "kind", string(k))
	return true
}

// resume lets a suspended kind continue.
func (g *Governor) resume(k Kind) {
	g.mu.Lock()
	if !g.suspended[k] {
		g.mu.Unlock()
		return
	}
	s, ok := g.suspenders[k]
	delete(g.suspended, k)
	g.mu.Unlock()
	if !ok {
		return
	}
	if err := s.Resume(); err != nil {
		g.log.Warn("governor cannot resume a kind", "kind", string(k), "err", err)
		return
	}
	g.log.Info("background work resumed", "kind", string(k))
}

// release puts everything back: nothing suspended, nothing paused by us. It is
// what a disabled or exiting governor leaves behind.
func (g *Governor) release() {
	g.mu.Lock()
	kinds := make([]Kind, 0, len(g.suspended))
	for k := range g.suspended {
		kinds = append(kinds, k)
	}
	g.mu.Unlock()
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	for _, k := range kinds {
		g.resume(k)
	}
	g.setPaused(false)
}

// setPaused drives the queue's global pause, and only ever undoes a pause it
// applied itself. An operator who paused the queue by hand must not find it
// running again because the machine cooled down.
func (g *Governor) setPaused(want bool) {
	g.mu.Lock()
	ours := g.pausedByUs
	g.mu.Unlock()

	switch {
	case want && !ours:
		g.ctrl.Pause()
		g.mu.Lock()
		g.pausedByUs = true
		g.mu.Unlock()
		g.log.Warn("background jobs paused: the machine is at its safety limit")
	case !want && ours:
		g.mu.Lock()
		g.pausedByUs = false
		g.mu.Unlock()
		g.ctrl.Resume()
		g.log.Info("background jobs resumed")
	}
}

// forgetFinished drops release flags for jobs that are no longer active, so a
// long-lived governor does not accumulate one entry per job ever released.
func (g *Governor) forgetFinished(active []Job) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.released) == 0 {
		g.releasedKinds = map[Kind]bool{}
		return
	}
	live := make(map[int64]Kind, len(active))
	for _, j := range active {
		live[j.ID] = j.Kind
	}
	kinds := map[Kind]bool{}
	for id := range g.released {
		kind, stillLive := live[id]
		if !stillLive {
			delete(g.released, id)
			continue
		}
		kinds[kind] = true
	}
	g.releasedKinds = kinds
}

func (g *Governor) record(s Snapshot) {
	g.mu.Lock()
	changed := !sameDecision(g.last, s)
	g.last = s
	fn := g.onChange
	g.mu.Unlock()
	if changed && fn != nil {
		fn(s)
	}
}

// sameDecision compares the parts an operator would notice. Timestamps and the
// exact CPU reading move every tick and are not news.
func sameDecision(a, b Snapshot) bool {
	if a.Enabled != b.Enabled || a.Paused != b.Paused {
		return false
	}
	if a.Gates.IngestLive != b.Gates.IngestLive ||
		a.Gates.CPUOverCeiling != b.Gates.CPUOverCeiling ||
		a.Gates.CPUSustained != b.Gates.CPUSustained ||
		a.Gates.GPUBusy != b.Gates.GPUBusy ||
		a.Gates.OnBattery != b.Gates.OnBattery ||
		a.Gates.TooHot != b.Gates.TooHot {
		return false
	}
	if len(a.Verdicts) != len(b.Verdicts) {
		return false
	}
	for i := range a.Verdicts {
		x, y := a.Verdicts[i], b.Verdicts[i]
		if x.Kind != y.Kind || x.Start != y.Start || x.Continue != y.Continue || x.Reason != y.Reason {
			return false
		}
	}
	return true
}
