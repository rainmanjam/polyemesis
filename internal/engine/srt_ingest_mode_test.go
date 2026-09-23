package engine

import (
	"context"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The one-port SRT listener must admit a publisher only into a source that is
// set to SRT ingest.
//
// The RTMP listener already asks the mode (lookupStreamKey's Ready); the SRT
// one did not. Every source with a running engine got a Sink, so the shared
// SRT port accepted a publish into an RTMP source -- whose hub the RTMP ingest
// child is already writing, so two muxers interleave into one stream -- into a
// pull source, and into a source whose ingest was never chosen, which is what
// the console's create form makes. Meanwhile the API reported those same
// sources tokenEnforced:false with no publish URL, and the Sources page told the
// operator the token protected nothing, while it was in fact the credential for
// an ingest the operator had never turned on.
//
// The standby follows the same rule on failover.backup.mode: an RTMP standby's
// "<token>.backup" must not be reachable over SRT.
//
// Mutation: give every target eng.Hub() regardless of mode again. Observed to
// fail with "admitted over SRT".
func TestTheSRTListenerAdmitsOnlySRTModeSources(t *testing.T) {
	m, store := managerFixture(t)

	mk := func(name string, mode db.IngestMode) *db.Source {
		t.Helper()
		ing := db.DefaultSettings().Ingest
		ing.Mode = mode
		switch mode {
		case db.IngestRTMP:
			ing.RTMP.App = "live"
		case db.IngestPull:
			ing.Pull.URL = "rtmp://camera.invalid/live/cam"
		}
		s := &db.Source{Name: name, Enabled: true, Ingest: ing}
		if err := store.CreateSource(s); err != nil {
			t.Fatalf("CreateSource(%s): %v", name, err)
		}
		return s
	}
	srtSrc := mk("SRT", db.IngestSRT)
	others := []*db.Source{
		mk("RTMP", db.IngestRTMP),
		mk("Pull", db.IngestPull),
		mk("Unchosen", db.IngestUnset),
	}

	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Listeners.SRTPort = freeUDPPort(t)
	st.Listeners.RTMPPort = freeTCPPort(t)
	st.Failover.Enabled = true
	st.Failover.Backup.Enabled = true
	st.Failover.Backup.Mode = db.IngestRTMP
	st.Failover.Backup.RTMP.App = "live"
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Positive control: without it every assertion below passes on a lookup
	// that admits nobody.
	got, ok := m.lookupToken(srtSrc.Token)
	if !ok || got.SourceID != srtSrc.ID || got.Sink == nil {
		t.Fatalf("control: the SRT source's token resolved to %+v, %v; an SRT encoder "+
			"has nowhere to publish", got, ok)
	}

	for _, s := range others {
		if m.Engine(s.ID) == nil {
			t.Fatalf("control: source %q has no engine, so a nil Sink below proves nothing", s.Name)
		}
		if got, ok := m.lookupToken(s.Token); ok && got.Sink != nil {
			t.Errorf("a publisher holding %q's token was admitted over SRT into a source "+
				"whose ingest mode is %q", s.Name, s.Ingest.Mode)
		}
	}

	// The standby is configured for RTMP on every source, the SRT one included.
	for _, s := range append(others, srtSrc) {
		if got, ok := m.lookupToken(s.Token + backupTokenSuffix); ok && got.Sink != nil {
			t.Errorf("%q's RTMP standby was admitted over SRT: its hub already has "+
				"the RTMP backup child writing into it", s.Name)
		}
	}
}

// The standby's positive control, which the test above only has for the
// primary: a source whose failover.backup.mode IS srt must still be reachable
// at "<token>.backup" on the shared port. Without it, a standby gate that
// admits nobody -- `&& false` on the condition in lookupToken -- passes every
// refusal above while leaving every SRT failover encoder with nowhere to
// publish.
//
// Mutation: append `&& false` to the standby condition in lookupToken.
// Observed to fail with "SRT standby is unreachable".
func TestTheSRTListenerAdmitsAnSRTModeStandby(t *testing.T) {
	m, store := managerFixture(t)

	ing := db.DefaultSettings().Ingest
	ing.Mode = db.IngestSRT
	src := &db.Source{Name: "SRT", Enabled: true, Ingest: ing}
	if err := store.CreateSource(src); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Listeners.SRTPort = freeUDPPort(t)
	st.Listeners.RTMPPort = freeTCPPort(t)
	st.Failover.Enabled = true
	st.Failover.Backup.Enabled = true
	st.Failover.Backup.Mode = db.IngestSRT
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	eng := m.Engine(src.ID)
	if eng == nil {
		t.Fatal("control: the SRT source has no engine")
	}
	// The standby hub is what the gate hands out, so its absence would make the
	// lookup below fail for a reason that has nothing to do with the mode gate.
	waitUntil(t, func() bool { return eng.BackupHub() != nil }, "the standby's hub")

	got, ok := m.lookupToken(src.Token + backupTokenSuffix)
	if !ok || got.Sink == nil {
		t.Fatalf("the SRT standby is unreachable (%+v, %v): failover.backup.mode is srt, "+
			"so a backup encoder publishing to <token>.backup has nowhere to go", got, ok)
	}
	if !got.Backup || got.SourceID != src.ID {
		t.Errorf("standby target = %+v, want the backup slot of source %d", got, src.ID)
	}
}
