// Package chatcmd answers the chat commands of #6: `!bitrate` for anyone, and
// `!switch` and `!fix` for the channel's moderators and the broadcaster.
//
// The convention is the IRL-streaming one (NOALBS and the BELABOX community
// use the same three words): a streamer on a bonded cellular uplink cannot
// reach a dashboard, but their moderators can type in chat. So a moderator who
// sees the picture breaking up can put the backup or the slate on air, or ask
// for the feed to be refreshed, without anybody opening the console.
//
// IT IS A READER OF THE BUS, NOT A STAGE IN THE CHAT HUB. Every normalised
// message is already published as events.TypeChat; subscribing there keeps
// this package out of the hub's delivery path entirely. The bus drops for a
// slow subscriber, and that is acceptable here: a dropped !bitrate is retyped,
// and a command handler that could slow chat delivery down is not.
//
// WHO MAY NOT is most of the design:
//   - nothing runs unless an operator switched the feature on (it gives every
//     moderator of every connected channel control of the broadcast);
//   - !switch and !fix need Author.Moderator or Author.Broadcaster, which a
//     platform that reports neither leaves false -- fail closed;
//   - a refused command is ignored silently, so chat cannot be used to make
//     polyemesis spam its own channel;
//   - our own sends come back as echoes and are never executed, and neither is
//     a replayed message older than maxAge;
//   - replies share one cooldown per command, because every send is billed
//     against the YouTube quota.
package chatcmd

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
)

// Report is what !bitrate says about the programme.
type Report struct {
	// Live is whether anything is arriving on the programme's ingest.
	Live        bool
	BitrateKbps float64
	// Active is the source on air when failover is on, "" when it is off.
	Active string
	// Degraded is the degraded-link detail, "" while the link is healthy.
	Degraded string
}

// Programme is the one programme the commands act on.
type Programme interface {
	Report() Report
	SwitchSource(kind string) error
	FixSource() error
}

// Sender is the existing chat send: every connected platform, quota-paced.
type Sender interface {
	Send(ctx context.Context, text string) ([]chat.SendResult, error)
}

// Options wires a Handler.
type Options struct {
	Log    *slog.Logger
	Sender Sender
	// Settings is read on every message, so a save takes effect at once.
	Settings func() db.ChatCommandSettings
	// Resolve finds a programme by id; 0 is the default programme.
	Resolve func(sourceID int64) (Programme, bool)
	Now     func() time.Time
}

const (
	// maxAge is how old a message may be and still be obeyed. A platform
	// reconnect can replay scrollback; a !switch from before it is history.
	maxAge = 2 * time.Minute
	// cooldown is per command, across every user and platform.
	cooldown = 10 * time.Second
	// sendTimeout bounds one reply; the hub's own send timeout applies within.
	sendTimeout = 15 * time.Second
)

// Handler interprets chat messages as commands.
type Handler struct {
	opts Options
	mu   sync.Mutex
	last map[string]time.Time
}

// New builds a Handler.
func New(o Options) *Handler {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Handler{opts: o, last: map[string]time.Time{}}
}

// Run reads chat messages off the bus until ctx ends.
func (h *Handler) Run(ctx context.Context, bus *events.Broker) {
	if bus == nil {
		return
	}
	sub := bus.Subscribe(events.TypeChat)
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			if m, ok := ev.Data.(chat.Message); ok {
				h.Handle(ctx, m)
			}
		}
	}
}

// Handle runs one message if it is a command this sender may run.
func (h *Handler) Handle(ctx context.Context, m chat.Message) {
	if m.Echo || h.opts.Settings == nil {
		return
	}
	cfg := h.opts.Settings()
	if !cfg.Enabled {
		return
	}
	cmd, arg, ok := parse(m.Text)
	if !ok {
		return
	}
	now := h.opts.Now()
	if !m.At.IsZero() && now.Sub(m.At) > maxAge {
		return
	}
	mod := m.Author.Moderator || m.Author.Broadcaster
	if cmd != "bitrate" && !mod {
		return
	}
	if !h.take(cmd, now) {
		return
	}

	prog, found := h.opts.Resolve(cfg.SourceID)
	if !found {
		h.reply(ctx, "The programme chat commands control is not running.")
		return
	}
	switch cmd {
	case "bitrate":
		h.reply(ctx, bitrateLine(prog.Report()))
	case "switch":
		if arg == "" {
			h.reply(ctx, "Usage: !switch primary | backup | playlist | slate | auto")
			return
		}
		h.audit(m, cmd, arg)
		if err := prog.SwitchSource(arg); err != nil {
			h.reply(ctx, "Could not switch: "+err.Error())
			return
		}
		if arg == "auto" {
			h.reply(ctx, "Source selection is automatic again.")
			return
		}
		h.reply(ctx, "Switched to "+arg+".")
	case "fix":
		h.audit(m, cmd, "")
		if err := prog.FixSource(); err != nil {
			h.reply(ctx, "Could not refresh the feed: "+err.Error())
			return
		}
		h.reply(ctx, "Refreshing the feed; source selection is automatic again.")
	}
}

// parse recognises exactly the three commands, case-insensitively, as the
// first word of the message.
func parse(text string) (cmd, arg string, ok bool) {
	f := strings.Fields(strings.ToLower(text))
	if len(f) == 0 {
		return "", "", false
	}
	switch f[0] {
	case "!bitrate", "!fix":
		return f[0][1:], "", true
	case "!switch":
		if len(f) > 1 {
			arg = f[1]
		}
		return "switch", arg, true
	}
	return "", "", false
}

// take claims the cooldown for cmd, false while it is still running.
func (h *Handler) take(cmd string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if at, ok := h.last[cmd]; ok && now.Sub(at) < cooldown {
		return false
	}
	h.last[cmd] = now
	return true
}

func bitrateLine(r Report) string {
	if !r.Live {
		return "Nothing is arriving from the encoder right now."
	}
	line := fmt.Sprintf("Bitrate: %.0f kbps", r.BitrateKbps)
	if r.Degraded != "" {
		line += "; primary link degraded (" + r.Degraded + ")"
	}
	if r.Active != "" {
		line += "; " + r.Active + " on air"
	}
	return line + "."
}

// audit writes down who ran a moderator command, from which platform. The
// switch itself is then announced on every failover surface -- status, the
// timeline, webhooks, MQTT -- as an operator's selection.
func (h *Handler) audit(m chat.Message, cmd, arg string) {
	h.opts.Log.Info("chat command",
		"command", cmd, "arg", arg,
		"platform", string(m.Platform), "account", m.Account,
		"author", m.Author.Name, "authorId", m.Author.ID)
}

func (h *Handler) reply(ctx context.Context, text string) {
	if h.opts.Sender == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if _, err := h.opts.Sender.Send(ctx, text); err != nil {
		h.opts.Log.Warn("chat command reply failed", "err", err)
	}
}
