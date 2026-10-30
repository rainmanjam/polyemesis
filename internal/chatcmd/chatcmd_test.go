package chatcmd

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// Chat commands (#6): `!bitrate` for anyone, `!switch` and `!fix` for the
// channel's moderators and the broadcaster, answered through the existing chat
// send. They put the broadcast in the hands of whoever moderates the channel,
// so most of what is tested here is who may NOT run them.

type fakeProgramme struct {
	mu       sync.Mutex
	report   Report
	switched []string
	fixed    int
	err      error
}

func (p *fakeProgramme) Report() Report { p.mu.Lock(); defer p.mu.Unlock(); return p.report }
func (p *fakeProgramme) SwitchSource(kind string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.switched = append(p.switched, kind)
	return p.err
}
func (p *fakeProgramme) FixSource() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fixed++
	return p.err
}

type fakeSender struct {
	mu   sync.Mutex
	sent []string
}

func (s *fakeSender) Send(_ context.Context, text string) ([]chat.SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, text)
	return nil, nil
}

func (s *fakeSender) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

type rig struct {
	h      *Handler
	prog   *fakeProgramme
	send   *fakeSender
	now    time.Time
	logBuf *bytes.Buffer
	cfg    db.ChatCommandSettings
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		prog:   &fakeProgramme{report: Report{Live: true, BitrateKbps: 4520, Active: "primary"}},
		send:   &fakeSender{},
		now:    time.Unix(1_700_000_000, 0),
		logBuf: &bytes.Buffer{},
		cfg:    db.ChatCommandSettings{Enabled: true},
	}
	r.h = New(Options{
		Log:      slog.New(slog.NewTextHandler(r.logBuf, nil)),
		Sender:   r.send,
		Settings: func() db.ChatCommandSettings { return r.cfg },
		Resolve: func(id int64) (Programme, bool) {
			if id != 0 && id != 7 {
				return nil, false
			}
			return r.prog, true
		},
		Now: func() time.Time { return r.now },
	})
	return r
}

func msg(text string, mod bool) chat.Message {
	return chat.Message{
		ID: "m1", Platform: db.PlatformTwitch, Text: text,
		Author: chat.Author{ID: "u1", Name: "someone", Moderator: mod},
	}
}

func (r *rig) say(m chat.Message) {
	if m.At.IsZero() {
		m.At = r.now // said just now, unless the test says otherwise
	}
	r.h.Handle(context.Background(), m)
	r.now = r.now.Add(time.Minute) // past every cooldown, unless a test says otherwise
}

func TestBitrateAnswersAnyViewerWithWhatIsOnAir(t *testing.T) {
	r := newRig(t)
	r.say(msg("!bitrate", false))
	got := r.send.all()
	if len(got) != 1 || !strings.Contains(got[0], "4520 kbps") || !strings.Contains(got[0], "primary") {
		t.Fatalf("replies %q, want one naming 4520 kbps and the primary", got)
	}
}

func TestBitrateSaysWhyWhenTheLinkIsDegraded(t *testing.T) {
	r := newRig(t)
	r.prog.report = Report{Live: true, BitrateKbps: 640, Active: "slate", Degraded: "bitrate 640 kbps below 800 kbps for 5s"}
	r.say(msg("!bitrate", false))
	got := r.send.all()
	if len(got) != 1 || !strings.Contains(got[0], "degraded") || !strings.Contains(got[0], "below 800 kbps") || !strings.Contains(got[0], "slate") {
		t.Fatalf("replies %q, want the degraded detail and the slate", got)
	}
}

func TestBitrateWithNothingArrivingSaysSoRatherThanZero(t *testing.T) {
	r := newRig(t)
	r.prog.report = Report{Live: false}
	r.say(msg("!bitrate", false))
	if got := r.send.all(); len(got) != 1 || strings.Contains(got[0], "0 kbps") {
		t.Fatalf("replies %q, want a plain 'nothing is arriving'", got)
	}
}

// The moderator commands, and who may not run them.
func TestOnlyModeratorsAndTheBroadcasterCanSwitch(t *testing.T) {
	r := newRig(t)
	r.say(msg("!switch backup", false))
	if len(r.prog.switched) != 0 {
		t.Fatalf("a viewer switched the source: %v", r.prog.switched)
	}
	if len(r.send.all()) != 0 {
		t.Errorf("a refused viewer was answered %q; refusals are silent so chat cannot be used to spam", r.send.all())
	}

	r.say(msg("!switch backup", true))
	b := msg("!switch auto", false)
	b.Author.Broadcaster = true
	r.say(b)
	if want := []string{"backup", "auto"}; strings.Join(r.prog.switched, ",") != strings.Join(want, ",") {
		t.Fatalf("switched %v, want %v (a moderator, then the broadcaster)", r.prog.switched, want)
	}
}

func TestFixIsModeratorOnlyAndReportsWhatItDid(t *testing.T) {
	r := newRig(t)
	r.say(msg("!fix", false))
	if r.prog.fixed != 0 {
		t.Fatal("a viewer ran !fix")
	}
	r.say(msg("!fix", true))
	if r.prog.fixed != 1 {
		t.Fatalf("a moderator's !fix ran %d times, want 1", r.prog.fixed)
	}
	if got := r.send.all(); len(got) != 1 {
		t.Fatalf("replies %q, want one acknowledgement", got)
	}
}

func TestSwitchWithoutASourceExplainsTheUsageAndChangesNothing(t *testing.T) {
	r := newRig(t)
	r.say(msg("!switch", true))
	if len(r.prog.switched) != 0 {
		t.Fatalf("a bare !switch switched to %v", r.prog.switched)
	}
	if got := r.send.all(); len(got) != 1 || !strings.Contains(got[0], "primary") || !strings.Contains(got[0], "auto") {
		t.Fatalf("replies %q, want the usage", got)
	}
}

func TestASwitchTheEngineRefusesIsReportedNotHidden(t *testing.T) {
	r := newRig(t)
	r.prog.err = errors.New("failover is not enabled, so there is nothing to switch between")
	r.say(msg("!switch slate", true))
	if got := r.send.all(); len(got) != 1 || !strings.Contains(got[0], "failover is not enabled") {
		t.Fatalf("replies %q, want the engine's refusal", got)
	}
}

// Off by default and off means off: the commands give a channel's moderators
// control of the broadcast, which an operator has to choose.
func TestCommandsDoNothingWhileSwitchedOff(t *testing.T) {
	r := newRig(t)
	r.cfg.Enabled = false
	r.say(msg("!bitrate", true))
	r.say(msg("!switch backup", true))
	if len(r.send.all()) != 0 || len(r.prog.switched) != 0 {
		t.Fatalf("commands ran while switched off: sent %q switched %v", r.send.all(), r.prog.switched)
	}
}

// Our own replies come back through the platform as messages. One that started
// with "!" -- or a moderator quoting a command in the reply -- must never be
// executed.
func TestAnEchoOfOurOwnMessageIsNeverExecuted(t *testing.T) {
	r := newRig(t)
	m := msg("!switch backup", true)
	m.Echo = true
	r.say(m)
	if len(r.prog.switched) != 0 {
		t.Fatal("an echoed message was executed as a command")
	}
}

// Old messages -- a reconnect replaying scrollback -- are history, not
// instructions: a !switch from an hour ago must not fire now.
func TestAReplayedOldCommandIsIgnored(t *testing.T) {
	r := newRig(t)
	m := msg("!switch backup", true)
	m.At = r.now.Add(-10 * time.Minute)
	r.say(m)
	if len(r.prog.switched) != 0 {
		t.Fatal("a command from ten minutes ago was executed")
	}
}

// A chat full of viewers typing !bitrate is a quota problem on YouTube and a
// spam problem everywhere: one answer per cooldown.
func TestBitrateIsAnsweredOncePerCooldown(t *testing.T) {
	r := newRig(t)
	for range 5 {
		r.h.Handle(context.Background(), msg("!bitrate", false))
	}
	if got := r.send.all(); len(got) != 1 {
		t.Fatalf("five !bitrate inside the cooldown produced %d replies, want 1", len(got))
	}
}

func TestOnlyTheConfiguredProgrammeIsControlled(t *testing.T) {
	r := newRig(t)
	r.cfg.SourceID = 99 // a programme the resolver does not know
	r.say(msg("!switch backup", true))
	if len(r.prog.switched) != 0 {
		t.Fatal("a command reached a programme other than the configured one")
	}
	if got := r.send.all(); len(got) != 1 {
		t.Fatalf("replies %q, want one saying the programme is not running", got)
	}
}

func TestNotACommandIsNotACommand(t *testing.T) {
	r := newRig(t)
	for _, text := range []string{"bitrate?", "what's the !bitrate", "!bitratey", "!", "!SWITCHBACKUP"} {
		r.say(msg(text, true))
	}
	if len(r.send.all()) != 0 || len(r.prog.switched) != 0 || r.prog.fixed != 0 {
		t.Fatalf("ordinary chat triggered commands: sent %q switched %v fixed %d", r.send.all(), r.prog.switched, r.prog.fixed)
	}
	r.say(msg("  !BITRATE  ", false))
	if len(r.send.all()) != 1 {
		t.Error("a command in capitals with surrounding spaces was not recognised")
	}
}

// Every moderator action is written down with who did it, from where.
func TestEveryModeratorActionIsLoggedWithWhoRanIt(t *testing.T) {
	r := newRig(t)
	r.say(msg("!switch backup", true))
	out := r.logBuf.String()
	for _, want := range []string{"chat command", "switch", "someone", "twitch"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q does not record %q", out, want)
		}
	}
}
