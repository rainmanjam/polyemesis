package playout

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// ---------------------------------------------------------------- test doubles

type fakeHub struct {
	mu   sync.Mutex
	name string
	subs map[string]int
}

func newHub(name string) *fakeHub { return &fakeHub{name: name, subs: map[string]int{}} }

// Refuses an occupied name, like the real one. #711. A fake that accepts a
// collision the production hub refuses is a fake that hides the bug.
func (h *fakeHub) Subscribe(name string, port int) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, taken := h.subs[name]; taken {
		return "", fmt.Errorf("%q: %w", name, relay.ErrSubscriberExists)
	}
	h.subs[name] = port
	return fmt.Sprintf("udp://127.0.0.1:%d", port), nil
}

func (h *fakeHub) Unsubscribe(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, name)
}

func (h *fakeHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

type fakePorts struct {
	mu   sync.Mutex
	next int
	held map[int]bool
	err  error
}

func newPorts() *fakePorts { return &fakePorts{next: 21000, held: map[int]bool{}} }

func (p *fakePorts) Allocate() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return 0, p.err
	}
	p.next++
	p.held[p.next] = true
	return p.next, nil
}

func (p *fakePorts) Release(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.held, port)
}

func (p *fakePorts) leaked() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.held)
}

type fakeProc struct {
	name string
	args []string
	mu   sync.Mutex
	runs int
	dead bool
}

func (p *fakeProc) Start() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runs++
}

func (p *fakeProc) Stop(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dead = true
	// Always a clean stop. The deadline path is supervisor.Process's, and
	// playout has no behaviour that depends on which one happened.
	return nil
}

func (p *fakeProc) stopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dead
}

type spawnLog struct {
	mu    sync.Mutex
	procs []*fakeProc
}

func (s *spawnLog) spawner() Spawner {
	return func(name string, args []string) Runner {
		p := &fakeProc{name: name, args: args}
		s.mu.Lock()
		s.procs = append(s.procs, p)
		s.mu.Unlock()
		return p
	}
}

func (s *spawnLog) all() []*fakeProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*fakeProc(nil), s.procs...)
}

func (s *spawnLog) named(name string) *fakeProc {
	for _, p := range s.all() {
		if p.name == name {
			return p
		}
	}
	return nil
}

// harness is a manager wired to doubles, with the collaborators kept to hand.
type harness struct {
	*Manager
	dir    string
	hub    *fakeHub
	rend   *fakeHub
	ports  *fakePorts
	spawns *spawnLog
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		dir:    t.TempDir(),
		hub:    newHub("ingest"),
		rend:   newHub("rendition"),
		ports:  newPorts(),
		spawns: &spawnLog{},
	}
	h.Manager = New(Deps{
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Dir:   h.dir,
		Ports: h.ports,
		Spawn: h.spawns.spawner(),
		Now:   func() time.Time { return epoch },
	})
	return h
}

// resolve routes nil to the ingest hub and rendition 7 to the rendition hub,
// which is the shape the engine's real resolver has.
func (h *harness) resolve(id *int64) (Upstream, error) {
	if id == nil {
		return Upstream{Hub: h.hub, Label: "source"}, nil
	}
	if *id == 7 {
		return Upstream{Hub: h.rend, Label: "720p", Width: 1280, Height: 720, VideoKbps: 3000}, nil
	}
	return Upstream{}, fmt.Errorf("rendition %d is not running", *id)
}

func rid(v int64) *int64 { return &v }

func baseSettings(variants ...db.PlayoutVariant) db.PlayoutSettings {
	s := db.DefaultSettings().Playout
	s.Enabled = true
	if len(variants) > 0 {
		s.Variants = variants
	}
	return s
}

func readMaster(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, MasterPlaylist))
	if err != nil {
		t.Fatalf("read master playlist: %v", err)
	}
	return string(b)
}

// ------------------------------------------------------------------ reconcile

func TestReconcileStartsOneMuxerPerEnabledVariant(t *testing.T) {
	tests := []struct {
		name     string
		variants []db.PlayoutVariant
		want     []string
	}{
		{"a single passthrough rung",
			[]db.PlayoutVariant{{Name: "source", Enabled: true}},
			[]string{"playout:source"}},
		{"a rung on a rendition and one on the ingest",
			[]db.PlayoutVariant{
				{Name: "source", Enabled: true},
				{Name: "hd", Enabled: true, RenditionID: rid(7)},
			},
			[]string{"playout:hd", "playout:source"}},
		{"a disabled rung starts nothing",
			[]db.PlayoutVariant{
				{Name: "source", Enabled: true},
				{Name: "hd", Enabled: false, RenditionID: rid(7)},
			},
			[]string{"playout:source"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			if err := h.Reconcile(baseSettings(tc.variants...), h.resolve); err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if h.spawns.named(want) == nil {
					t.Fatalf("no process named %q; spawned %v", want, names(h.spawns.all()))
				}
			}
			if got := len(h.spawns.all()); got != len(tc.want) {
				t.Fatalf("spawned %v, want exactly %v", names(h.spawns.all()), tc.want)
			}
		})
	}
}

func TestAVariantSubscribesToItsOwnUpstreamHub(t *testing.T) {
	h := newHarness(t)
	s := baseSettings(
		db.PlayoutVariant{Name: "source", Enabled: true},
		db.PlayoutVariant{Name: "hd", Enabled: true, RenditionID: rid(7)},
	)
	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatal(err)
	}

	// One on the ingest hub, one on the rendition's: a playout variant is a
	// rendition consumer and must read the encode, not re-do it.
	if h.hub.count() != 1 || h.rend.count() != 1 {
		t.Fatalf("ingest subs = %d, rendition subs = %d, want 1 each", h.hub.count(), h.rend.count())
	}
}

func TestAnUnchangedSettingsSaveDoesNotCycleALiveMuxer(t *testing.T) {
	h := newHarness(t)
	s := baseSettings(db.PlayoutVariant{Name: "source", Enabled: true})
	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatal(err)
	}
	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatal(err)
	}

	if n := len(h.spawns.all()); n != 1 {
		t.Fatalf("%d processes spawned across two identical reconciles, want 1", n)
	}
	if h.spawns.all()[0].stopped() {
		t.Fatal("the live muxer was stopped by a no-op reconcile")
	}
}

func TestChangingWhatTheCommandLineDependsOnRestartsTheVariant(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*db.PlayoutSettings)
		restart bool
	}{
		{"segment length", func(s *db.PlayoutSettings) { s.SegmentSeconds = 2 }, true},
		{"playlist window", func(s *db.PlayoutSettings) { s.PlaylistSegments = 12 }, true},
		{"dvr window", func(s *db.PlayoutSettings) { s.DVRWindowSeconds = 300 }, true},
		{"audio bitrate", func(s *db.PlayoutSettings) { s.AudioKbps = 96 }, true},
		{"output format", func(s *db.PlayoutSettings) { s.Format = db.PlayoutHLSDASH }, true},
		{"audio track", func(s *db.PlayoutSettings) { s.Variants[0].AudioTrack = 2 }, true},
		// These reach the master playlist and the handler, never the muxer.
		{"public flag", func(s *db.PlayoutSettings) { s.Public = true }, false},
		{"cross-origin flag", func(s *db.PlayoutSettings) { s.AllowCrossOrigin = true }, false},
		{"disk cap", func(s *db.PlayoutSettings) { s.MaxDiskMB = 4096 }, false},
		{"session idle timeout", func(s *db.PlayoutSettings) { s.SessionIdleSeconds = 45 }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			s := baseSettings(db.PlayoutVariant{Name: "source", Enabled: true})
			if err := h.Reconcile(s, h.resolve); err != nil {
				t.Fatal(err)
			}
			first := h.spawns.all()[0]

			next := baseSettings(db.PlayoutVariant{Name: "source", Enabled: true})
			tc.mutate(&next)
			if err := h.Reconcile(next, h.resolve); err != nil {
				t.Fatal(err)
			}

			restarted := len(h.spawns.all()) > 1
			if restarted != tc.restart {
				t.Fatalf("restarted = %v, want %v", restarted, tc.restart)
			}
			if tc.restart && !first.stopped() {
				t.Fatal("the replaced muxer was never stopped")
			}
		})
	}
}

func TestMovingAVariantBetweenRenditionsRestartsIt(t *testing.T) {
	h := newHarness(t)
	if err := h.Reconcile(baseSettings(db.PlayoutVariant{Name: "hd", Enabled: true}), h.resolve); err != nil {
		t.Fatal(err)
	}
	first := h.spawns.all()[0]

	moved := baseSettings(db.PlayoutVariant{Name: "hd", Enabled: true, RenditionID: rid(7)})
	if err := h.Reconcile(moved, h.resolve); err != nil {
		t.Fatal(err)
	}

	if len(h.spawns.all()) != 2 {
		t.Fatal("moving a variant onto a rendition did not restart it against the new relay")
	}
	if !first.stopped() {
		t.Fatal("the muxer reading the old relay is still running")
	}
	if h.hub.count() != 0 || h.rend.count() != 1 {
		t.Fatalf("ingest subs = %d, rendition subs = %d, want 0 and 1", h.hub.count(), h.rend.count())
	}
}

func TestOneBrokenRungDoesNotStopTheOthersFromServing(t *testing.T) {
	h := newHarness(t)
	s := baseSettings(
		db.PlayoutVariant{Name: "source", Enabled: true},
		// Rendition 99 is not running, which is a reason for this rung not to
		// start and no reason at all for the other one to stop.
		db.PlayoutVariant{Name: "broken", Enabled: true, RenditionID: rid(99)},
	)
	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatalf("Reconcile returned %v; one bad rung must not fail the reconcile", err)
	}

	if h.spawns.named("playout:source") == nil {
		t.Fatal("the healthy rung did not start")
	}
	if h.spawns.named("playout:broken") != nil {
		t.Fatal("the broken rung was started against nothing")
	}

	var broken VariantStatus
	for _, v := range h.Status().Variants {
		if v.Name == "broken" {
			broken = v
		}
	}
	if broken.Error == "" {
		t.Fatal("the broken rung reports no reason; the operator has nothing to act on")
	}
	if broken.Running {
		t.Fatal("the broken rung reports as running")
	}
}

func TestABrokenRungIsRetriedOnceItsRenditionAppears(t *testing.T) {
	h := newHarness(t)
	s := baseSettings(db.PlayoutVariant{Name: "hd", Enabled: true, RenditionID: rid(7)})

	missing := func(id *int64) (Upstream, error) { return Upstream{}, errors.New("not running yet") }
	if err := h.Reconcile(s, missing); err != nil {
		t.Fatal(err)
	}
	if len(h.spawns.all()) != 0 {
		t.Fatal("started against a missing rendition")
	}

	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatal(err)
	}
	if h.spawns.named("playout:hd") == nil {
		t.Fatal("the rung was not retried once its rendition appeared")
	}
}

func TestDisablingPlayoutStopsEverythingAndReleasesIt(t *testing.T) {
	h := newHarness(t)
	s := baseSettings(
		db.PlayoutVariant{Name: "source", Enabled: true},
		db.PlayoutVariant{Name: "hd", Enabled: true, RenditionID: rid(7)},
	)
	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatal(err)
	}

	off := s
	off.Enabled = false
	if err := h.Reconcile(off, h.resolve); err != nil {
		t.Fatal(err)
	}

	for _, p := range h.spawns.all() {
		if !p.stopped() {
			t.Fatalf("%s is still running", p.name)
		}
	}
	if h.hub.count() != 0 || h.rend.count() != 0 {
		t.Fatal("relay subscriptions survived the shutdown; the hub keeps shouting into dead ports")
	}
	if h.ports.leaked() != 0 {
		t.Fatalf("%d relay ports leaked", h.ports.leaked())
	}
	if _, err := os.Stat(filepath.Join(h.dir, MasterPlaylist)); !os.IsNotExist(err) {
		t.Fatal("the master playlist survived; a player would keep fetching a dead ladder")
	}
}

func TestStopIsFinalAndCannotBeUndoneByALateReconcile(t *testing.T) {
	h := newHarness(t)
	s := baseSettings(db.PlayoutVariant{Name: "source", Enabled: true})
	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatal(err)
	}
	h.Stop()

	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatal(err)
	}
	if n := len(h.spawns.all()); n != 1 {
		t.Fatalf("%d processes spawned, want the post-Stop reconcile to spawn nothing", n)
	}
	if h.ports.leaked() != 0 {
		t.Fatalf("%d relay ports leaked across shutdown", h.ports.leaked())
	}
}

func TestAFailedPortAllocationLeavesNoSubscriptionBehind(t *testing.T) {
	h := newHarness(t)
	h.ports.err = errors.New("no free port")

	s := baseSettings(db.PlayoutVariant{Name: "source", Enabled: true})
	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatalf("Reconcile returned %v; a port shortage is one rung's problem", err)
	}
	if h.hub.count() != 0 {
		t.Fatal("subscribed to the hub without a process to read it")
	}
	if len(h.spawns.all()) != 0 {
		t.Fatal("spawned a muxer with no relay port")
	}

	h.ports.err = nil
	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatal(err)
	}
	if h.spawns.named("playout:source") == nil {
		t.Fatal("the rung was not retried once a port was free")
	}
}

// ------------------------------------------------------------ master playlist

func TestMasterPlaylistAdvertisesEveryRunningRungCheapestFirst(t *testing.T) {
	h := newHarness(t)
	s := baseSettings(
		db.PlayoutVariant{Name: "source", Enabled: true},
		db.PlayoutVariant{Name: "hd", Enabled: true, RenditionID: rid(7)},
	)
	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatal(err)
	}

	got := readMaster(t, h.dir)
	if !strings.HasPrefix(got, "#EXTM3U") {
		t.Fatalf("master playlist does not start with #EXTM3U:\n%s", got)
	}
	// The 720p rendition declares 3000 kbps, the passthrough rung falls back to
	// the conservative ceiling, so hd must come first.
	hd := strings.Index(got, "hd/"+MediaPlaylist)
	src := strings.Index(got, "source/"+MediaPlaylist)
	if hd < 0 || src < 0 {
		t.Fatalf("a rung is missing from the ladder:\n%s", got)
	}
	if hd > src {
		t.Fatalf("rungs are not cheapest-first; a player would start on the most expensive one:\n%s", got)
	}
	if !strings.Contains(got, "RESOLUTION=1280x720") {
		t.Fatalf("the rendition's size is not advertised:\n%s", got)
	}
	// The passthrough rung's size is unknown, and a guessed RESOLUTION makes a
	// player choose wrongly, so it must be absent rather than invented.
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, `NAME="source"`) && strings.Contains(line, "RESOLUTION") {
			t.Fatalf("invented a resolution for the passthrough rung: %s", line)
		}
	}
	if !strings.Contains(got, "BANDWIDTH=") {
		t.Fatal("no BANDWIDTH; a player has nothing to choose on")
	}
}

func TestMasterPlaylistOmitsARungThatDidNotStart(t *testing.T) {
	h := newHarness(t)
	s := baseSettings(
		db.PlayoutVariant{Name: "source", Enabled: true},
		db.PlayoutVariant{Name: "broken", Enabled: true, RenditionID: rid(99)},
	)
	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatal(err)
	}

	got := readMaster(t, h.dir)
	if strings.Contains(got, "broken/") {
		t.Fatalf("a rung that never started is in the ladder; every player would retry it forever:\n%s", got)
	}
	if !strings.Contains(got, "source/") {
		t.Fatalf("the working rung is missing:\n%s", got)
	}
}

func TestMasterPlaylistUsesForwardSlashesWhateverThePlatform(t *testing.T) {
	h := newHarness(t)
	if err := h.Reconcile(baseSettings(db.PlayoutVariant{Name: "source", Enabled: true}), h.resolve); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readMaster(t, h.dir), `\`) {
		t.Fatal("a backslash in a playlist URI makes every segment unreachable")
	}
}

// ------------------------------------------------------------- rendition refs

func TestRenditionRefsCountEnabledVariantsSoAPlayoutOnlyTierStaysUp(t *testing.T) {
	tests := []struct {
		name string
		s    db.PlayoutSettings
		want map[int64]int
	}{
		{"disabled playout holds nothing up",
			func() db.PlayoutSettings {
				s := baseSettings(db.PlayoutVariant{Name: "hd", Enabled: true, RenditionID: rid(7)})
				s.Enabled = false
				return s
			}(),
			map[int64]int{}},
		{"a passthrough rung refs no rendition",
			baseSettings(db.PlayoutVariant{Name: "source", Enabled: true}),
			map[int64]int{}},
		{"one rung on a rendition is one reference",
			baseSettings(db.PlayoutVariant{Name: "hd", Enabled: true, RenditionID: rid(7)}),
			map[int64]int{7: 1}},
		{"two rungs on the same rendition still share one encode",
			baseSettings(
				db.PlayoutVariant{Name: "a", Enabled: true, RenditionID: rid(7)},
				db.PlayoutVariant{Name: "b", Enabled: true, RenditionID: rid(7)},
			),
			map[int64]int{7: 2}},
		{"a disabled rung releases its reference",
			baseSettings(
				db.PlayoutVariant{Name: "a", Enabled: true, RenditionID: rid(7)},
				db.PlayoutVariant{Name: "b", Enabled: false, RenditionID: rid(8)},
			),
			map[int64]int{7: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RenditionRefs(tc.s)
			if len(got) != len(tc.want) {
				t.Fatalf("refs = %v, want %v", got, tc.want)
			}
			for id, n := range tc.want {
				if got[id] != n {
					t.Fatalf("refs[%d] = %d, want %d", id, got[id], n)
				}
			}
		})
	}
}

// -------------------------------------------------------------------- storage

func TestSweepEnforcesTheConfiguredCapAndRecordsIt(t *testing.T) {
	h := newHarness(t)
	s := baseSettings(db.PlayoutVariant{Name: "hd", Enabled: true})
	s.MaxDiskMB = MinPlayoutDiskMBForTest
	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatal(err)
	}

	// Two megabytes of segments against a one-megabyte cap, aged so the
	// oldest-first rule has something to bite on.
	for i := 0; i < 20; i++ {
		writeSegment(t, h.dir, fmt.Sprintf("hd/seg_%05d.ts", i), 100*1024, time.Duration(20-i)*time.Minute)
	}

	u := h.Sweep()
	if u.Deleted == 0 {
		t.Fatal("nothing was deleted; the playout directory is unbounded")
	}
	if u.Bytes > int64(s.MaxDiskMB)*bytesPerMB {
		t.Fatalf("still %d bytes against a %d MB cap", u.Bytes, s.MaxDiskMB)
	}
	if h.Usage().Bytes != u.Bytes {
		t.Fatal("the recorded usage does not match the sweep that produced it")
	}
}

// MinPlayoutDiskMBForTest is a one-megabyte cap. Named rather than inline so it
// is obvious this deliberately sits below db.MinPlayoutDiskMB: settings refuse
// a cap this small, and the sweeper still has to behave when handed one.
const MinPlayoutDiskMBForTest = 1

func TestRunSweepsUntilTheContextIsCancelled(t *testing.T) {
	h := newHarness(t)
	s := baseSettings(db.PlayoutVariant{Name: "hd", Enabled: true})
	s.MaxDiskMB = MinPlayoutDiskMBForTest
	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		writeSegment(t, h.dir, fmt.Sprintf("hd/seg_%05d.ts", i), 100*1024, time.Duration(20-i)*time.Minute)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); h.Run(ctx) }()

	// Run sweeps once immediately, which is the behaviour that matters: a
	// restart must collect the previous run's orphans without waiting a full
	// interval.
	deadline := time.After(2 * time.Second)
	for {
		if h.Usage().Deleted > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Run did not sweep on start")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	cancel()
	<-done
}

// --------------------------------------------------------------------- status

func TestStatusReportsEveryConfiguredRungIncludingDisabledOnes(t *testing.T) {
	h := newHarness(t)
	s := baseSettings(
		db.PlayoutVariant{Name: "source", Enabled: true},
		db.PlayoutVariant{Name: "hd", Enabled: false, RenditionID: rid(7)},
	)
	s.Format = db.PlayoutHLSDASH
	if err := h.Reconcile(s, h.resolve); err != nil {
		t.Fatal(err)
	}

	st := h.Status()
	if !st.Enabled || st.Master != MasterPlaylist {
		t.Fatalf("status = %+v", st)
	}
	if len(st.Variants) != 2 {
		t.Fatalf("%d rungs reported, want every configured one", len(st.Variants))
	}
	for _, v := range st.Variants {
		if v.Playlist != v.Name+"/"+MediaPlaylist {
			t.Fatalf("%s playlist = %q", v.Name, v.Playlist)
		}
		if v.Manifest == "" {
			t.Fatalf("%s reports no DASH manifest despite the hls+dash format", v.Name)
		}
	}
	if st.Variants[0].Name != "source" || !st.Variants[0].Running {
		t.Fatalf("the enabled rung is not reported as running: %+v", st.Variants[0])
	}
	if st.Variants[1].Running || st.Variants[1].Error != "" {
		t.Fatalf("a disabled rung is neither running nor broken: %+v", st.Variants[1])
	}
}

func TestStatusOmitsTheDASHManifestWhenOnlyHLSIsMuxed(t *testing.T) {
	h := newHarness(t)
	if err := h.Reconcile(baseSettings(db.PlayoutVariant{Name: "source", Enabled: true}), h.resolve); err != nil {
		t.Fatal(err)
	}
	if got := h.Status().Variants[0].Manifest; got != "" {
		t.Fatalf("manifest = %q, want none: no DASH muxer is running", got)
	}
}

func TestAllowAnonymousNeedsBothEnabledAndPublic(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		public  bool
		want    bool
	}{
		{"off entirely", false, false, false},
		{"public but disabled serves nothing", false, true, false},
		{"enabled but private needs a session", true, false, false},
		{"enabled and public is the origin case", true, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			s := baseSettings(db.PlayoutVariant{Name: "source", Enabled: true})
			s.Enabled, s.Public = tc.enabled, tc.public
			if err := h.Reconcile(s, h.resolve); err != nil {
				t.Fatal(err)
			}
			if got := h.AllowAnonymous(); got != tc.want {
				t.Fatalf("AllowAnonymous = %v, want %v", got, tc.want)
			}
		})
	}
}

// ------------------------------------------------------------------- settings

func TestPlayoutSettingsValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*db.PlayoutSettings)
		want   string
	}{
		{"the defaults validate", func(*db.PlayoutSettings) {}, ""},
		{"a variant name that is not a safe path segment",
			func(s *db.PlayoutSettings) { s.Variants[0].Name = "../etc" }, "variant name"},
		{"an empty variant name",
			func(s *db.PlayoutSettings) { s.Variants[0].Name = "" }, "variant name"},
		{"two rungs sharing a directory",
			func(s *db.PlayoutSettings) {
				s.Variants = []db.PlayoutVariant{{Name: "hd", Enabled: true}, {Name: "HD", Enabled: true}}
			}, "duplicate"},
		{"an unknown output format",
			func(s *db.PlayoutSettings) { s.Format = "rtmp" }, "format"},
		{"a segment length no muxer would take",
			func(s *db.PlayoutSettings) { s.SegmentSeconds = 0 }, "segment length"},
		{"a playlist window below what a player can buffer",
			func(s *db.PlayoutSettings) { s.PlaylistSegments = 1 }, "playlist window"},
		{"a negative dvr window",
			func(s *db.PlayoutSettings) { s.DVRWindowSeconds = -1 }, "dvr window"},
		{"no disk cap at all",
			func(s *db.PlayoutSettings) { s.MaxDiskMB = 0 }, "disk cap"},
		{"an unbounded viewer table",
			func(s *db.PlayoutSettings) { s.MaxSessions = 0 }, "session cap"},
		{"an idle timeout under one segment",
			func(s *db.PlayoutSettings) { s.SessionIdleSeconds = 1 }, "idle timeout"},
		{"a ladder with no rungs at all",
			func(s *db.PlayoutSettings) { s.Enabled = true; s.Variants = nil }, "no variant"},
		{"an audio track outside any conceivable ingest",
			func(s *db.PlayoutSettings) { s.Variants[0].AudioTrack = 99 }, "audio track"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			all := db.DefaultSettings()
			tc.mutate(&all.Playout)
			err := all.Validate()

			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate returned %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate said %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestPlayoutSettingsSurviveASettingsBlobWrittenBeforeItExisted(t *testing.T) {
	// An upgrade must not turn playout on, and must not fail validation.
	s := db.DefaultSettings()
	if s.Playout.Enabled {
		t.Fatal("playout defaults to on; an upgrade would start serving the stream publicly on its own")
	}
	if s.Playout.Public {
		t.Fatal("playout defaults to public")
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("the default settings do not validate: %v", err)
	}
}

// TestTheRealCollaboratorsSatisfyTheNarrowInterfaces is the compile-time proof
// that the engine can wire this package up without an adapter. The interfaces
// here are deliberately narrow so playout stays testable, and a signature drift
// in relay or supervisor would otherwise only be discovered in engine.go, which
// this package cannot see.
func TestTheRealCollaboratorsSatisfyTheNarrowInterfaces(t *testing.T) {
	var (
		_ Hub    = (*relay.Hub)(nil)
		_ Ports  = (*relay.PortAllocator)(nil)
		_ Runner = (*supervisor.Process)(nil)
	)
}

func names(ps []*fakeProc) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.name)
	}
	return out
}
