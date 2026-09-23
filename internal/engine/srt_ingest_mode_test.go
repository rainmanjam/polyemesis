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
