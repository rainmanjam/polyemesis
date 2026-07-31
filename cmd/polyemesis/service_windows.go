//go:build windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
)

// serviceName must match the name the installer registers with the SCM;
// deploy/windows/install.ps1 uses the same string for both the service and the
// event log source.
const serviceName = "polyemesis"

const (
	// startWaitHint is what we promise the SCM each time we check in during
	// startup. FFmpeg detection shells out to the binary and parses its banner,
	// which on a cold filesystem is measured in seconds, not milliseconds.
	startWaitHint = 30 * time.Second
	// stopWaitHint has to cover the 20s HTTP drain plus the engine teardown
	// that finalises recordings. Undersell it and the SCM kills us mid-flush,
	// which is the exact truncated-recording failure the graceful path exists
	// to prevent.
	stopWaitHint = 45 * time.Second
	// checkInEvery keeps the checkpoint moving while a phase is taking a while.
	// The SCM only tolerates silence up to the wait hint; a rising checkpoint
	// restarts its clock.
	checkInEvery = 3 * time.Second
)

// Event log identifiers. These are not registered in a message file, so the
// Event Viewer renders them with a "description cannot be found" preamble and
// the message body verbatim underneath. That is a tolerable trade for not
// shipping a compiled .mc resource.
const (
	eventIDLifecycle uint32 = 1
	eventIDLog       uint32 = 2
	// Its own ID so an operator can filter for it, and so it is not lost among
	// the lifecycle chatter it sits next to.
	eventIDRecordingTruncation uint32 = 3
)

// recordingTruncationWarning is emitted on EVERY service start, deliberately.
//
// Running under the SCM IS the failing condition -- there is no separate check
// to make. GenerateConsoleCtrlEvent is delivered through the caller's console
// and a service has none, so the graceful CTRL_BREAK in
// internal/supervisor/proc_windows.go fails immediately and the supervisor
// terminates the child instead. FFmpeg never writes the container index for the
// segment it was filling.
//
// Said at start rather than at stop because by the time it happens the operator
// has already lost the segment, and a service stop is frequently a reboot
// nobody is watching. Said every time rather than once because the audience is
// whoever reads the Event Viewer after losing a recording, not whoever
// installed the service.
//
// The loss is bounded to the CURRENT segment: the recorder writes segmented
// MKV, so everything already rolled over is intact and playable. That number is
// the operator's lever, and naming it here is the difference between a warning
// and an apology.
const recordingTruncationWarning = "KNOWN DEFECT: stopping this service truncates an in-progress recording.\r\n" +
	"\r\n" +
	"Why: the graceful stop is a CTRL_BREAK console event, and a Windows service has no " +
	"console for it to travel through. The recorder is terminated instead of being asked " +
	"to finish, so it never writes the index for the segment it was filling.\r\n" +
	"\r\n" +
	"What is lost: only the segment in progress. Earlier segments have already been " +
	"finalised and are playable. Shorter recording.segmentSeconds means less at risk.\r\n" +
	"\r\n" +
	"To avoid it: stop recording from the UI, and wait for the recording to appear in the " +
	"Recordings list, before stopping the service.\r\n" +
	"\r\n" +
	"Tracked in CHANGELOG.md under Known limitations. Running polyemesis from a console " +
	"instead of as a service is unaffected."

// runService hands control to the SCM when it was the SCM that started us.
// A console launch reports false, leaving main() on the interactive path.
func runService() (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return false, fmt.Errorf("determining whether polyemesis is running as a windows service: %w", err)
	}
	if !isService {
		return false, nil
	}

	h := &serviceHandler{}
	// svc.Run returns only after Execute has returned, so by here the run
	// loop's own error (if any) is already parked on the handler.
	if err := svc.Run(serviceName, h); err != nil {
		return true, fmt.Errorf("windows service dispatcher: %w", err)
	}
	return true, h.runErr
}

// phase is one status transition the run loop asked for. Everything funnels
// through a channel rather than writing to the SCM status channel directly:
// the run goroutine and the Execute loop would otherwise both be senders, and
// a Running racing a StopPending is a state the SCM has no way to reconcile.
type phase struct {
	state  svc.State
	detail string
}

type serviceHandler struct {
	elog   *eventlog.Log
	runErr error
}

// Execute is the SCM's view of the process: report in early and often, accept
// stop and shutdown, and translate either into the same teardown SIGTERM gets.
func (h *serviceHandler) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	state := svc.StartPending
	checkPoint := uint32(1)
	// The very first report has to beat anything slow that follows it,
	// including opening the event log.
	s <- svc.Status{State: state, CheckPoint: checkPoint, WaitHint: millis(startWaitHint)}

	if elog, err := eventlog.Open(serviceName); err == nil {
		h.elog = elog
		defer elog.Close()
	}
	h.report(eventlog.Info, eventIDLifecycle, "polyemesis service starting")
	h.report(eventlog.Warning, eventIDRecordingTruncation, recordingTruncationWarning)

	phases := make(chan phase, 16)
	stop := make(chan struct{})
	done := make(chan error, 1)
	var stopOnce sync.Once

	go func() {
		done <- run(&hooks{
			NewHandler: func(level slog.Level) slog.Handler { return newEventLogHandler(h.elog, level) },
			Progress:   func(p string) { phases <- phase{svc.StartPending, p} },
			Ready:      func() { phases <- phase{svc.Running, "serving"} },
			Stopping:   func() { phases <- phase{svc.StopPending, "shutting down"} },
			Stop:       stop,
		})
	}()

	ticker := time.NewTicker(checkInEvery)
	defer ticker.Stop()

	for {
		select {
		case p := <-phases:
			state = p.state
			checkPoint++
			s <- h.status(state, checkPoint, accepted)
			h.report(eventlog.Info, eventIDLifecycle, "polyemesis "+p.detail)

		case <-ticker.C:
			// Only pending states need a heartbeat; Running has no deadline.
			if state == svc.StartPending || state == svc.StopPending {
				checkPoint++
				s <- h.status(state, checkPoint, accepted)
			}

		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				// The run loop owns the ordering — redirect listener, then the
				// HTTP listener, then the engine — so all we do is ask, then
				// keep the SCM patient until it finishes.
				stopOnce.Do(func() {
					h.report(eventlog.Info, eventIDLifecycle, "polyemesis received "+cmdName(c.Cmd)+"; draining")
					close(stop)
				})
				state = svc.StopPending
				checkPoint++
				s <- h.status(state, checkPoint, accepted)
			default:
				// Unaccepted controls should not arrive; ignoring them is
				// friendlier than crashing a service over one.
			}

		case err := <-done:
			h.runErr = err
			if err != nil {
				h.report(eventlog.Error, eventIDLifecycle, "polyemesis exited: "+err.Error())
				s <- svc.Status{State: svc.Stopped}
				// A non-zero exit is what makes the SCM's restart-on-failure
				// recovery action fire, so an engine that died deserves one.
				return false, 1
			}
			h.report(eventlog.Info, eventIDLifecycle, "polyemesis stopped cleanly")
			s <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

// status builds the report for a state, advertising accepted controls only once
// we are actually able to honour them.
func (h *serviceHandler) status(state svc.State, checkPoint uint32, accepted svc.Accepted) svc.Status {
	st := svc.Status{State: state, CheckPoint: checkPoint}
	switch state {
	case svc.Running:
		st.Accepts = accepted
		st.CheckPoint = 0 // a settled state has no progress to report
	case svc.StartPending:
		st.WaitHint = millis(startWaitHint)
	case svc.StopPending:
		st.Accepts = accepted
		st.WaitHint = millis(stopWaitHint)
	}
	return st
}

func (h *serviceHandler) report(etype uint16, eid uint32, msg string) {
	if h.elog == nil {
		return
	}
	switch etype {
	case eventlog.Error:
		_ = h.elog.Error(eid, msg)
	case eventlog.Warning:
		_ = h.elog.Warning(eid, msg)
	default:
		_ = h.elog.Info(eid, msg)
	}
}

func cmdName(c svc.Cmd) string {
	if c == svc.Shutdown {
		return "shutdown"
	}
	return "stop"
}

// millis converts to the SCM's unit. Wait hints are uint32 milliseconds.
func millis(d time.Duration) uint32 { return uint32(d / time.Millisecond) }

// eventLogHandler routes slog records into the Windows Event Log. A service has
// no stderr — writes to it go to a closed handle — so without this every line
// the engine logs about a failing destination or a full disk is discarded.
type eventLogHandler struct {
	elog   *eventlog.Log
	level  slog.Level
	attrs  []slog.Attr
	groups []string
}

func newEventLogHandler(elog *eventlog.Log, level slog.Level) slog.Handler {
	return &eventLogHandler{elog: elog, level: level}
}

func (h *eventLogHandler) Enabled(_ context.Context, l slog.Level) bool {
	// A nil event log means eventlog.Open failed; there is nowhere to write, so
	// skip the formatting work entirely.
	return h.elog != nil && l >= h.level
}

func (h *eventLogHandler) Handle(_ context.Context, rec slog.Record) error {
	if h.elog == nil {
		return nil
	}
	var b strings.Builder
	b.WriteString(rec.Message)
	for _, a := range h.attrs {
		appendAttr(&b, h.groups, a)
	}
	rec.Attrs(func(a slog.Attr) bool {
		appendAttr(&b, h.groups, a)
		return true
	})
	msg := b.String()

	switch {
	case rec.Level >= slog.LevelError:
		return h.elog.Error(eventIDLog, msg)
	case rec.Level >= slog.LevelWarn:
		return h.elog.Warning(eventIDLog, msg)
	default:
		return h.elog.Info(eventIDLog, msg)
	}
}

func (h *eventLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	c := *h
	c.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &c
}

func (h *eventLogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.groups = append(append([]string(nil), h.groups...), name)
	return &c
}

// appendAttr writes one key=value pair in the same shape slog's text handler
// uses, so an operator reading the Event Viewer sees what they would have seen
// on a console.
func appendAttr(b *strings.Builder, groups []string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		sub := a.Value.Group()
		if len(sub) == 0 {
			return
		}
		nested := groups
		if a.Key != "" {
			nested = append(append([]string(nil), groups...), a.Key)
		}
		for _, s := range sub {
			appendAttr(b, nested, s)
		}
		return
	}
	b.WriteByte(' ')
	for _, g := range groups {
		b.WriteString(g)
		b.WriteByte('.')
	}
	b.WriteString(a.Key)
	b.WriteByte('=')
	b.WriteString(a.Value.String())
}
