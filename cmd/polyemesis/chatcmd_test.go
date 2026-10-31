package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/stats"
)

// The wiring between chat commands and the engines: which programme answers,
// what !bitrate reads, and that unreadable settings mean OFF.

type chatStore struct {
	s      db.Settings
	err    error
	defID  int64
	defErr error
}

func (c chatStore) GetSettings() (db.Settings, error) { return c.s, c.err }
func (c chatStore) DefaultSourceID() (int64, error)   { return c.defID, c.defErr }

type chatFakeEngine struct {
	live     bool
	fo       *engine.FailoverStatus
	switched string
	fixed    bool
}

func (f *chatFakeEngine) IngestLive() bool                 { return f.live }
func (f *chatFakeEngine) Monitor() *stats.Monitor          { return stats.NewMonitor(nil) }
func (f *chatFakeEngine) Failover() *engine.FailoverStatus { return f.fo }
func (f *chatFakeEngine) SwitchSource(k string) error      { f.switched = k; return nil }
func (f *chatFakeEngine) FixSource() error                 { f.fixed = true; return nil }

type nopSender struct{}

func (nopSender) Send(context.Context, string) ([]chat.SendResult, error) { return nil, nil }

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestChatCommandsAreOffWhenTheSettingsCannotBeRead(t *testing.T) {
	s := db.DefaultSettings()
	s.ChatCommands.Enabled = true
	o := chatCommandOptions(quiet(), chatStore{s: s, err: errors.New("disk")}, nil, nopSender{})
	if o.Settings().Enabled {
		t.Fatal("unreadable settings left chat commands ON; a moderator switch must be demonstrably enabled")
	}
	o = chatCommandOptions(quiet(), chatStore{s: s}, nil, nopSender{})
	if !o.Settings().Enabled {
		t.Fatal("readable settings with commands on read as off")
	}
}

func TestProgrammeZeroIsTheDefaultProgramme(t *testing.T) {
	eng := &chatFakeEngine{live: true}
	var asked []int64
	o := chatCommandOptions(quiet(), chatStore{defID: 4}, func(id int64) chatEngine {
		asked = append(asked, id)
		if id == 4 {
			return eng
		}
		return nil
	}, nopSender{})
	if _, ok := o.Resolve(0); !ok {
		t.Fatal("programme 0 did not resolve to the default programme")
	}
	if _, ok := o.Resolve(9); ok {
		t.Fatal("a programme with no engine resolved")
	}
	if len(asked) != 2 || asked[0] != 4 || asked[1] != 9 {
		t.Errorf("engines asked for %v, want [4 9]", asked)
	}
	o = chatCommandOptions(quiet(), chatStore{defErr: errors.New("no default")}, func(int64) chatEngine { return eng }, nopSender{})
	if _, ok := o.Resolve(0); ok {
		t.Error("programme 0 resolved with no default programme to resolve to")
	}
}

func TestTheEngineProgrammeReportsTheSelectorsView(t *testing.T) {
	eng := &chatFakeEngine{live: true, fo: &engine.FailoverStatus{Active: "slate", Degraded: "bitrate 300 kbps below 800 kbps for 5s"}}
	p := engineProgramme{eng}
	r := p.Report()
	if !r.Live || r.Active != "slate" || !strings.Contains(r.Degraded, "below 800") {
		t.Errorf("report %+v does not carry the selector's view", r)
	}
	if err := p.SwitchSource("backup"); err != nil || eng.switched != "backup" {
		t.Errorf("SwitchSource did not reach the engine (%v, %q)", err, eng.switched)
	}
	if err := p.FixSource(); err != nil || !eng.fixed {
		t.Error("FixSource did not reach the engine")
	}
	if r := (engineProgramme{&chatFakeEngine{}}).Report(); r.Live || r.Active != "" {
		t.Errorf("an idle engine with failover off reported %+v", r)
	}
}
