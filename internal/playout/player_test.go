package playout

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func TestMasterPlaylistAdvertisesCodecsOnlyWhenKnown(t *testing.T) {
	h := newHarness(t)
	resolve := func(id *int64) (Upstream, error) {
		if id == nil {
			// The source rung before its first probe: codec unknown.
			return Upstream{Hub: h.hub, Label: "source"}, nil
		}
		switch *id {
		case 7:
			return Upstream{Hub: h.rend, Label: "720p", Width: 1280, Height: 720, VideoKbps: 3000, Codec: "h264"}, nil
		case 8:
			return Upstream{Hub: newHub("hevc"), Label: "4k", Width: 3840, Height: 2160, VideoKbps: 12000, Codec: "hevc"}, nil
		}
		return Upstream{Hub: newHub("av1"), Label: "av1", VideoKbps: 2000, Codec: "AV1"}, nil
	}
	s := baseSettings(
		db.PlayoutVariant{Name: "source", Enabled: true},
		db.PlayoutVariant{Name: "hd", Enabled: true, RenditionID: rid(7)},
		db.PlayoutVariant{Name: "uhd", Enabled: true, RenditionID: rid(8)},
		db.PlayoutVariant{Name: "av", Enabled: true, RenditionID: rid(9)},
	)
	if err := h.Reconcile(s, resolve); err != nil {
		t.Fatal(err)
	}
	lines := map[string]string{}
	for _, l := range strings.Split(readMaster(t, h.dir), "\n") {
		if i := strings.Index(l, `NAME="`); i >= 0 {
			name := strings.TrimSuffix(l[i+len(`NAME="`):], `"`)
			lines[name] = l
		}
	}
	want := map[string]string{
		"hd":  `CODECS="avc1.640028,mp4a.40.2"`,
		"uhd": `CODECS="hvc1.1.6.L120.90,mp4a.40.2"`,
		"av":  `CODECS="av01.0.08M.08,mp4a.40.2"`,
	}
	for name, codecs := range want {
		if !strings.Contains(lines[name], codecs) {
			t.Errorf("rung %s: want %s in %q", name, codecs, lines[name])
		}
	}
	if strings.Contains(lines["source"], "CODECS") {
		t.Errorf("guessed a CODECS value for a rung whose codec is unknown: %q", lines["source"])
	}

	st := h.Status()
	got := map[string]string{}
	for _, v := range st.Variants {
		got[v.Name] = v.Codec
	}
	if got["hd"] != "h264" || got["uhd"] != "hevc" || got["av"] != "av1" || got["source"] != "" {
		t.Fatalf("status codecs = %v", got)
	}
}

func TestNormalizeCodec(t *testing.T) {
	for in, want := range map[string]string{
		"h264": "h264", "AVC1": "h264", " hevc ": "hevc", "h265": "hevc",
		"hev1": "hevc", "av1": "av1", "av01": "av1", "vp9": "", "": "",
	} {
		if got := normalizeCodec(in); got != want {
			t.Errorf("normalizeCodec(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClassifyLive(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	grace := 15 * time.Second
	cases := []struct {
		name string
		last time.Time
		want LiveState
	}{
		{"never live", time.Time{}, LiveStateOffline},
		{"fresh segment", now.Add(-2 * time.Second), LiveStateLive},
		{"inside the grace: a rung restart", now.Add(-grace), LiveStateLive},
		{"just past the grace", now.Add(-grace - time.Second), LiveStateEnded},
		{"ended an hour ago", now.Add(-time.Hour), LiveStateEnded},
		{"ended past the hold", now.Add(-EndedHold - time.Second), LiveStateOffline},
	}
	for _, c := range cases {
		if got := classifyLive(now, c.last, grace); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// The whole path: a segment on disk is live, a torn-down ladder is ended (the
// directory is gone but the time is remembered), and the scan is cached.
func TestLiveStateFollowsSegmentsAndSurvivesTeardown(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_700_000_000, 0)
	clock := now
	m := New(Deps{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Dir: dir, Ports: newPorts(), Spawn: (&spawnLog{}).spawner(),
		Now: func() time.Time { return clock },
	})
	if st, _ := m.LiveState(); st != LiveStateOffline {
		t.Fatalf("empty dir: %s", st)
	}

	vdir := filepath.Join(dir, "hd")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	seg := filepath.Join(vdir, "seg_00001.ts")
	if err := os.WriteFile(seg, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(seg, now, now); err != nil {
		t.Fatal(err)
	}
	// Inside the scan TTL the old answer stands; that is the cache.
	if st, _ := m.LiveState(); st != LiveStateOffline {
		t.Fatalf("rescanned inside the TTL: %s", st)
	}
	clock = now.Add(liveScanTTL)
	st, last := m.LiveState()
	if st != LiveStateLive || !last.Equal(now) {
		t.Fatalf("segment on disk: %s last=%v", st, last)
	}

	m.teardown(&variant{cfg: db.PlayoutVariant{Name: "hd"}})
	if _, err := os.Stat(seg); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("teardown left the segment: %v", err)
	}
	clock = now.Add(10 * time.Minute)
	if st, _ := m.LiveState(); st != LiveStateEnded {
		t.Fatalf("after teardown: %s, want ended", st)
	}
}

func TestManagerRecordBeaconChecksTheRunningLadder(t *testing.T) {
	h := newHarness(t)
	s := baseSettings(
		db.PlayoutVariant{Name: "source", Enabled: true},
		db.PlayoutVariant{Name: "broken", Enabled: true, RenditionID: rid(99)},
	)
	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatal(err)
	}
	b := validBeacon()
	b.Variant = "source"
	if err := h.RecordBeacon(b); err != nil {
		t.Fatalf("a beacon naming a running rung was refused: %v", err)
	}
	b.Seq, b.Variant = 2, "broken"
	if err := h.RecordBeacon(b); !errors.Is(err, ErrBeaconInvalid) {
		t.Fatalf("a beacon naming a rung that is not running: %v", err)
	}
	a := h.Analytics()
	if a.QoE.Beacons != 1 || a.QoE.ByVariant["source"] != 1 {
		t.Fatalf("analytics qoe = %+v", a.QoE)
	}
	h.ResetAnalytics()
	if h.Analytics().QoE.Beacons != 0 {
		t.Fatal("ResetAnalytics left the QoE aggregate")
	}
}

// The passthrough rung is planned before the ingest is probed. When the probe
// lands, the sweep picks up the size and codec and the master is rewritten --
// without restarting the muxer.
func TestRefreshUpstreamsPicksUpALateProbe(t *testing.T) {
	h := newHarness(t)
	probed := Upstream{}
	resolve := func(id *int64) (Upstream, error) {
		up := probed
		up.Hub, up.Label = h.hub, "source"
		return up, nil
	}
	s := baseSettings(db.PlayoutVariant{Name: "source", Enabled: true})
	if err := h.Reconcile(s, resolve); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readMaster(t, h.dir), "CODECS") {
		t.Fatal("advertised a codec before the probe")
	}
	if h.refreshUpstreams() {
		t.Fatal("nothing changed, but the refresh reported a change")
	}
	spawned := len(h.spawns.all())

	probed = Upstream{Width: 1920, Height: 1080, Codec: "hevc"}
	if !h.refreshUpstreams() {
		t.Fatal("the probe's size and codec were not picked up")
	}
	got := readMaster(t, h.dir)
	if !strings.Contains(got, `CODECS="hvc1.1.6.L120.90,mp4a.40.2"`) || !strings.Contains(got, "RESOLUTION=1920x1080") {
		t.Fatalf("master not rewritten with the probe:\n%s", got)
	}
	if n := len(h.spawns.all()); n != spawned {
		t.Fatalf("a metadata refresh restarted the muxer (%d spawns, was %d)", n, spawned)
	}
	if v := h.Status().Variants[0]; v.Codec != "hevc" || v.Width != 1920 {
		t.Fatalf("status = %+v", v)
	}
}

// refreshUpstreams runs on the Run goroutine while Status serves every public
// poll and Reconcile runs on the engine's. None of them may read a rung's
// metadata outside the lock, and two master writers must not tear the file.
// Meaningful under -race.
func TestRefreshUpstreamsIsRaceFreeAgainstStatusAndReconcile(t *testing.T) {
	h := newHarness(t)
	var flip atomic.Bool
	resolve := func(id *int64) (Upstream, error) {
		if flip.Load() {
			return Upstream{Hub: h.hub, Label: "source", Width: 1920, Height: 1080, Codec: "hevc"}, nil
		}
		return Upstream{Hub: h.hub, Label: "source", Width: 1280, Height: 720, Codec: "h264"}, nil
	}
	s := baseSettings(db.PlayoutVariant{Name: "source", Enabled: true})
	if err := h.Reconcile(s, resolve); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				switch g {
				case 0:
					flip.Store(i%2 == 0)
					h.refreshUpstreams()
				case 1:
					_ = h.Status()
				case 2:
					_ = h.Reconcile(s, resolve)
				}
			}
		}(g)
	}
	wg.Wait()
	got := readMaster(t, h.dir)
	if !strings.HasPrefix(got, "#EXTM3U\n") || strings.Count(got, "#EXT-X-STREAM-INF") != 1 {
		t.Fatalf("torn master playlist:\n%s", got)
	}
}
