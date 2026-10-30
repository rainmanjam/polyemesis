package main

import (
	"context"
	"log/slog"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/chatcmd"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/stats"
)

// startChatCommands wires #6's chat commands: a reader of the chat messages
// the hub already publishes on the bus, answering through the hub's own send.
//
// Always started, and inert until an operator switches it on: the handler
// reads db.Settings.ChatCommands on every message, so a save takes effect on
// the next line of chat without a restart, and so does switching it off.
func startChatCommands(ctx context.Context, log *slog.Logger, store *db.DB, mgr *engine.Manager, hub *chat.Hub, bus *events.Broker) {
	h := chatcmd.New(chatCommandOptions(log, store, func(id int64) chatEngine {
		if e := mgr.Engine(id); e != nil {
			return e
		}
		return nil
	}, hub))
	go h.Run(ctx, bus)
}

// chatSettingsStore is the slice of the store the commands read.
type chatSettingsStore interface {
	GetSettings() (db.Settings, error)
	DefaultSourceID() (int64, error)
}

// chatEngine is the slice of an engine the commands act on.
type chatEngine interface {
	IngestLive() bool
	Monitor() *stats.Monitor
	Failover() *engine.FailoverStatus
	SwitchSource(kind string) error
	FixSource() error
}

func chatCommandOptions(log *slog.Logger, store chatSettingsStore, engineFor func(int64) chatEngine, sender chatcmd.Sender) chatcmd.Options {
	return chatcmd.Options{
		Log:    log,
		Sender: sender,
		Settings: func() db.ChatCommandSettings {
			s, err := store.GetSettings()
			if err != nil {
				// Unreadable settings disable the commands rather than
				// guessing: a moderator switch must be something the operator
				// demonstrably turned on.
				return db.ChatCommandSettings{}
			}
			return s.ChatCommands
		},
		Resolve: func(id int64) (chatcmd.Programme, bool) {
			if id == 0 {
				def, err := store.DefaultSourceID()
				if err != nil {
					return nil, false
				}
				id = def
			}
			e := engineFor(id)
			if e == nil {
				return nil, false
			}
			return engineProgramme{e}, true
		},
	}
}

// engineProgramme is the one engine chat commands act on.
type engineProgramme struct{ e chatEngine }

func (p engineProgramme) Report() chatcmd.Report {
	r := chatcmd.Report{Live: p.e.IngestLive()}
	if b := p.e.Monitor().Bitrate(); len(b) > 0 {
		r.BitrateKbps = b[len(b)-1].Kbps
	}
	if fo := p.e.Failover(); fo != nil {
		r.Active, r.Degraded = string(fo.Active), fo.Degraded
	}
	return r
}

func (p engineProgramme) SwitchSource(kind string) error { return p.e.SwitchSource(kind) }
func (p engineProgramme) FixSource() error               { return p.e.FixSource() }
