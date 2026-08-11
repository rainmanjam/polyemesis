// Package playout is the viewer-facing origin: it packages the relay into
// public HLS (and optionally DASH) and counts who is watching.
//
// polyemesis relays to other platforms, and its HLS has until now been an
// admin-only preview of that relay. Playout is the other half — the thing a
// self-hosted Restreamer is expected to do — serving the stream to viewers
// directly.
//
// Three rules shape the whole package:
//
//   - A variant PACKAGES, it does not encode video. Each variant reads the
//     rendition it names and copies that video bit-for-bit, so the ladder costs
//     one encoder per distinct rendition and playout adds none of its own. The
//     rendition tier is the encoder tier; this is a consumer of it.
//   - Playout has its own relay subscription and its own supervised process,
//     exactly as the preview does. It cannot touch a destination, and a muxer
//     that falls over takes nothing with it. Per-destination audio routing is
//     unaffected by anything in this package.
//   - The segment directory is bounded. A muxer prunes its own window, but a
//     restart orphans the previous run's segments, so the sweeper here is what
//     stands between a long-running origin and a full disk.
package playout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

const (
	// stopTimeout bounds how long one muxer gets to exit.
	stopTimeout = 12 * time.Second
	// sweepInterval is how often the disk cap is enforced. Well under the time
	// it takes any realistic bitrate to overshoot a cap measured in gigabytes.
	sweepInterval = 15 * time.Second
	// bytesPerMB is the binary megabyte MaxDiskMB is expressed in.
	bytesPerMB = 1024 * 1024
	// fallbackVariantKbps is the BANDWIDTH advertised for a variant whose
	// upstream bitrate is unknown — a passthrough variant on an ingest nobody
	// has probed. Guessing high is the safe direction: BANDWIDTH is a ceiling a
	// player uses to rule a rung OUT, so an under-declared rung gets chosen on
	// a link that cannot carry it.
	fallbackVariantKbps = 6000
)

// DirName is the playout directory under the data dir. Named here rather than
// in internal/config so the segment root, the sweeper's scope and the URL the
// handler serves all come from one place.
const DirName = "playout"

// DirIn resolves the playout root inside a data directory.
func DirIn(dataDir string) string { return filepath.Join(dataDir, DirName) }

// Hub is the slice of relay.Hub playout needs: somewhere to subscribe and
// somewhere to stop. Narrow because a variant may read the ingest hub or a
// rendition's, and the manager must not care which.
type Hub interface {
	Subscribe(name string, port int) string
	Unsubscribe(name string)
}

// Ports is relay.PortAllocator's contract.
type Ports interface {
	Allocate() (int, error)
	Release(port int)
}

// Runner is one supervised child. supervisor.Process satisfies it.
//
// Stop returns an error when it had to kill the child rather than watch it
// exit -- see supervisor.ErrStopDeadline. Playout does not act on it (a stopped
// item is being replaced by the next one, and there is no hub for a straggler
// to corrupt), but the signature has to carry it or supervisor.Process no
// longer satisfies this interface.
type Runner interface {
	Start()
	Stop(ctx context.Context) error
}

// Spawner builds a supervised child from a command line. The engine supplies
// one that wires the process into the same log sink, state callbacks and
// restart policy every other polyemesis child uses, which is why this package
// never touches internal/supervisor itself.
type Spawner func(name string, args []string) Runner

// Upstream is what a variant reads, resolved by the engine because only the
// engine knows which renditions are actually running.
//
// The dimensions and bitrate are not used to encode anything — video is copied.
// They are advertised in the master playlist, which is how a player picks a
// rung, so a wrong number here costs a viewer the right quality and nothing
// more.
type Upstream struct {
	Hub Hub
	// Label names the source in logs: "source" for the ingest, the rendition's
	// name otherwise.
	Label  string
	Width  int
	Height int
	// VideoKbps is the rendition's target bitrate, 0 when unknown.
	VideoKbps int
}

// Resolver maps a variant's rendition id — nil meaning the ingest itself — onto
// the hub it should read. An error is a reason this variant cannot run yet,
// reported to the operator and retried on the next reconcile; it is never a
// reason to fail the whole reconcile.
type Resolver func(renditionID *int64) (Upstream, error)

// Deps are the collaborators handed to the manager once, at construction.
type Deps struct {
	Log   *slog.Logger
	Dir   string
	Ports Ports
	Spawn Spawner
	// Now is overridden by tests; nil means time.Now.
	Now func() time.Time
	// ClientIP decides what identifies a viewer. nil falls back to the remote
	// address, which is right unless a trusted proxy is in front.
	ClientIP func(*http.Request) string
}

// variant is one running rung.
type variant struct {
	cfg  db.PlayoutVariant
	proc Runner
	hub  Hub
	// port and subName are its subscription on whichever hub it reads. Held so
	// teardown releases exactly what start took.
	port    int
	subName string
	// spec hashes everything the command line depends on, so an unrelated
	// settings change never cycles a live muxer.
	spec string
	// bandwidth and resolution are what the master playlist advertises.
	bandwidth int
	width     int
	height    int
	// err is why this rung is not running, shown rather than swallowed.
	err     string
	startAt time.Time
}

// Manager owns the playout processes, the segment directory and the viewer
// table.
type Manager struct {
	log      *slog.Logger
	dir      string
	ports    Ports
	spawn    Spawner
	now      func() time.Time
	clientIP func(*http.Request) string

	sessions *Sessions

	mu       sync.Mutex
	settings db.PlayoutSettings
	running  map[string]*variant
	usage    Usage
	stopped  bool
}

// New creates a manager. It does not start anything: playout comes up on the
// first Reconcile, like every other part of the pipeline.
func New(d Deps) *Manager {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.ClientIP == nil {
		d.ClientIP = remoteIP
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Manager{
		log:      d.Log,
		dir:      d.Dir,
		ports:    d.Ports,
		spawn:    d.Spawn,
		now:      d.Now,
		clientIP: d.ClientIP,
		sessions: NewSessions(0, 0),
		running:  map[string]*variant{},
	}
}

// Dir is the playout root, the directory the handler serves and the sweeper
// bounds.
func (m *Manager) Dir() string { return m.dir }

// Settings returns the settings the manager last reconciled against.
func (m *Manager) Settings() db.PlayoutSettings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings
}

// AllowAnonymous reports whether the playlists may be served without a session.
// The API consults it per request rather than deciding at route-build time, so
// flipping the setting takes effect without a restart.
func (m *Manager) AllowAnonymous() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.Enabled && m.settings.Public
}

// RenditionRefs is playout's contribution to the rendition ref count: how many
// enabled variants read each rendition.
//
// It reads settings rather than running state on purpose. The engine has to
// know a rendition is wanted BEFORE it decides which encodes to start, and a
// variant cannot be running yet at that point — asking the running state would
// be a deadlock dressed as a ref count, where the rendition waits for the
// variant and the variant waits for the rendition.
func RenditionRefs(s db.PlayoutSettings) map[int64]int {
	refs := map[int64]int{}
	if !s.Enabled {
		return refs
	}
	for _, v := range s.EnabledVariants() {
		if v.RenditionID != nil {
			refs[*v.RenditionID]++
		}
	}
	return refs
}

// Reconcile makes the running muxers match the settings. Safe to call
// repeatedly and from any handler.
//
// It returns an error only for a failure that affects every variant, such as an
// unusable playout directory. A single variant that cannot start records its
// reason and is retried next time, because one broken rung must not stop the
// others from serving.
func (m *Manager) Reconcile(s db.PlayoutSettings, resolve Resolver) error {
	m.sessions.SetLimits(time.Duration(s.SessionIdleSeconds)*time.Second, s.MaxSessions)

	m.mu.Lock()
	stopped := m.stopped
	m.settings = s
	m.mu.Unlock()
	if stopped {
		return nil
	}

	if !s.Enabled {
		m.stopAll()
		// Not Reset: an operator who just switched playout off still wants to
		// see how many people were watching when they did.
		return nil
	}

	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return fmt.Errorf("create playout directory: %w", err)
	}

	want := map[string]*variant{}
	for _, cfg := range s.EnabledVariants() {
		want[cfg.Name] = m.plan(s, cfg, resolve)
	}

	m.mu.Lock()
	var stop []*variant
	for name, cur := range m.running {
		w, keep := want[name]
		// An empty spec is a variant that failed to start, so it never matches
		// and is retried; a changed spec is a real restart.
		if keep && w.err == "" && w.spec != "" && w.spec == cur.spec {
			// Already running the right thing: carry the live process forward
			// and drop the plan.
			cur.bandwidth, cur.width, cur.height = w.bandwidth, w.width, w.height
			delete(want, name)
			continue
		}
		stop = append(stop, cur)
		delete(m.running, name)
	}
	m.mu.Unlock()

	for _, v := range stop {
		m.teardown(v)
	}
	for _, name := range sortedNames(want) {
		m.start(s, want[name])
	}

	if err := m.writeMaster(); err != nil {
		m.log.Warn("playout: write master playlist", "err", err)
	}
	return nil
}

// plan works out what one variant should look like, without starting anything.
func (m *Manager) plan(s db.PlayoutSettings, cfg db.PlayoutVariant, resolve Resolver) *variant {
	v := &variant{cfg: cfg}
	if resolve == nil {
		v.err = "no upstream resolver"
		return v
	}
	up, err := resolve(cfg.RenditionID)
	if err != nil {
		v.err = err.Error()
		return v
	}
	if up.Hub == nil {
		v.err = "upstream relay is not available"
		return v
	}
	v.hub = up.Hub
	v.width, v.height = up.Width, up.Height

	kbps := up.VideoKbps
	if kbps <= 0 {
		kbps = fallbackVariantKbps
	}
	v.bandwidth = (kbps + s.AudioKbps) * 1000
	v.spec = variantSig(s, cfg, up)
	return v
}

// variantSig hashes everything the muxer's command line depends on.
//
// The upstream label is folded in so moving a variant between rendition tiers
// restarts it, and the advertised bitrate is not, so re-tuning a rendition's
// bitrate does not restart a variant twice — once for the rendition and once
// for the master playlist it is only mentioned in.
func variantSig(s db.PlayoutSettings, cfg db.PlayoutVariant, up Upstream) string {
	parts := []string{
		cfg.Name,
		up.Label,
		strconv.Itoa(cfg.AudioTrack),
		strconv.Itoa(s.SegmentSeconds),
		strconv.Itoa(s.PlaylistSegments),
		strconv.Itoa(s.DVRWindowSeconds),
		strconv.Itoa(s.AudioKbps),
		string(s.Format),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// start brings one variant up: its own directory, its own subscription on
// whichever hub it reads, and a supervised muxer between them.
func (m *Manager) start(s db.PlayoutSettings, v *variant) {
	record := func() {
		m.mu.Lock()
		m.running[v.cfg.Name] = v
		m.mu.Unlock()
	}
	if v.err != "" {
		record()
		m.log.Error("playout variant unavailable", "variant", v.cfg.Name, "err", v.err)
		return
	}

	dir := filepath.Join(m.dir, v.cfg.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		v.spec, v.err = "", err.Error()
		record()
		return
	}
	// The previous run's playlist would otherwise be handed to the first player
	// through the door, naming segments this run is about to overwrite.
	_ = clearVariantDir(dir)

	port, err := m.ports.Allocate()
	if err != nil {
		v.spec, v.err = "", err.Error()
		record()
		m.log.Error("playout: no relay port", "variant", v.cfg.Name, "err", err)
		return
	}

	v.subName = "playout:" + v.cfg.Name
	url := v.hub.Subscribe(v.subName, port)
	args := VariantArgs(VariantSpec{
		Name:             v.cfg.Name,
		RelayURL:         url,
		Dir:              dir,
		SegmentSeconds:   s.SegmentSeconds,
		PlaylistSegments: s.PlaylistSegments,
		DVRSegments:      segmentsFor(s.DVRWindowSeconds, s.SegmentSeconds),
		AudioTrack:       v.cfg.AudioTrack,
		AudioKbps:        s.AudioKbps,
		DASH:             s.Format == db.PlayoutHLSDASH,
	})

	proc := m.spawn(v.subName, args)
	v.proc = proc
	v.port = port
	v.startAt = m.now()

	m.mu.Lock()
	// Stop may have run since this reconcile began. Publishing under the same
	// lock Stop collects variants with is what keeps a late start from becoming
	// an orphan holding a UDP socket.
	if m.stopped {
		m.mu.Unlock()
		v.hub.Unsubscribe(v.subName)
		m.ports.Release(port)
		return
	}
	m.running[v.cfg.Name] = v
	m.mu.Unlock()

	proc.Start()
	m.log.Info("playout variant started", "variant", v.cfg.Name, "upstream", v.subName,
		"segment", s.SegmentSeconds, "dvr", s.DVRWindowSeconds, "format", s.Format)
}

func (m *Manager) teardown(v *variant) {
	if v == nil {
		return
	}
	if v.proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		_ = v.proc.Stop(ctx)
		cancel()
	}
	// After the process, so the muxer is never writing into a closed socket.
	if v.subName != "" && v.hub != nil {
		v.hub.Unsubscribe(v.subName)
	}
	if v.port != 0 {
		m.ports.Release(v.port)
	}
	// The stale playlist left behind would be served to the next viewer,
	// pointing at segments the next start is about to replace.
	_ = clearVariantDir(filepath.Join(m.dir, v.cfg.Name))
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	vs := make([]*variant, 0, len(m.running))
	for _, v := range m.running {
		vs = append(vs, v)
	}
	m.running = map[string]*variant{}
	m.mu.Unlock()

	for _, v := range vs {
		m.teardown(v)
	}
	_ = os.Remove(filepath.Join(m.dir, MasterPlaylist))
}

// Stop tears playout down for good. Further Reconcile calls are no-ops, so a
// request racing shutdown cannot resurrect a muxer.
func (m *Manager) Stop() {
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()
	m.stopAll()
}

// Run enforces the disk cap on an interval until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	// Once immediately: a restart inherits the previous run's orphaned
	// segments, and waiting a full interval to collect them is exactly the
	// window in which a box that was already near its cap fills up.
	m.Sweep()

	tick := time.NewTicker(sweepInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.Sweep()
		}
	}
}

// Sweep enforces the total-size cap over the playout directory.
func (m *Manager) Sweep() Usage {
	m.mu.Lock()
	limit := int64(m.settings.MaxDiskMB) * bytesPerMB
	m.mu.Unlock()

	u, err := sweep(m.dir, limit, os.Remove)
	if err != nil {
		m.log.Warn("playout: sweep failed", "dir", m.dir, "err", err)
		return u
	}
	if u.Deleted > 0 {
		m.log.Warn("playout: segments deleted to stay under the disk cap",
			"deleted", u.Deleted, "limitMb", limit/bytesPerMB)
	}
	if u.OverLimit {
		// Not an error we can act on: the only files left are the ones every
		// viewer is mid-playback on. Saying so is more useful than deleting
		// them would be.
		m.log.Error("playout: disk cap is below one playlist window; raise it or lower the bitrate",
			"bytes", u.Bytes, "limitMb", limit/bytesPerMB)
	}

	m.mu.Lock()
	prev := m.usage
	u.Deleted += prev.Deleted
	m.usage = u
	m.mu.Unlock()
	return u
}

// Usage reports the last sweep's verdict on the segment directory.
func (m *Manager) Usage() Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usage
}

// Analytics reports the viewer picture.
func (m *Manager) Analytics() Analytics { return m.sessions.Snapshot(m.now()) }

// ResetAnalytics clears the viewer table and its counters, so an operator can
// measure one broadcast rather than the uptime of the process.
func (m *Manager) ResetAnalytics() { m.sessions.Reset() }

// VariantStatus is one rung as the API reports it.
type VariantStatus struct {
	Name        string `json:"name"`
	RenditionID *int64 `json:"renditionId,omitempty"`
	AudioTrack  int    `json:"audioTrack"`
	Running     bool   `json:"running"`
	// Error is why the rung is not running, empty when it is.
	Error string `json:"error,omitempty"`
	// Bandwidth, Width and Height are what the master playlist advertises.
	Bandwidth int `json:"bandwidth"`
	Width     int `json:"width,omitempty"`
	Height    int `json:"height,omitempty"`
	// Playlist and Manifest are paths relative to the playout root, so the
	// caller decides the URL prefix.
	Playlist  string     `json:"playlist"`
	Manifest  string     `json:"manifest,omitempty"`
	Viewers   int        `json:"viewers"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
}

// Status is everything the playout page needs in one read.
type Status struct {
	Enabled bool `json:"enabled"`
	Public  bool `json:"public"`
	// Master is the cross-variant playlist, relative to the playout root.
	Master    string           `json:"master"`
	Format    db.PlayoutFormat `json:"format"`
	Variants  []VariantStatus  `json:"variants"`
	Analytics Analytics        `json:"analytics"`
	Usage     Usage            `json:"usage"`
}

// Status snapshots the manager for the API.
func (m *Manager) Status() Status {
	a := m.Analytics()

	m.mu.Lock()
	s := m.settings
	usage := m.usage
	running := make(map[string]*variant, len(m.running))
	for k, v := range m.running {
		running[k] = v
	}
	m.mu.Unlock()

	st := Status{
		Enabled:   s.Enabled,
		Public:    s.Public,
		Master:    MasterPlaylist,
		Format:    s.Format,
		Analytics: a,
		Usage:     usage,
		Variants:  make([]VariantStatus, 0, len(s.Variants)),
	}
	for _, cfg := range s.Variants {
		vs := VariantStatus{
			Name:        cfg.Name,
			RenditionID: cfg.RenditionID,
			AudioTrack:  cfg.AudioTrack,
			Playlist:    path2(cfg.Name, MediaPlaylist),
			Viewers:     a.ByVariant[cfg.Name],
		}
		if s.Format == db.PlayoutHLSDASH {
			vs.Manifest = path2(cfg.Name, DASHManifest)
		}
		if v, ok := running[cfg.Name]; ok {
			vs.Running = v.proc != nil && v.err == ""
			vs.Error = v.err
			vs.Bandwidth, vs.Width, vs.Height = v.bandwidth, v.width, v.height
			if !v.startAt.IsZero() {
				at := v.startAt
				vs.StartedAt = &at
			}
		} else if cfg.Enabled && s.Enabled {
			vs.Error = "not started"
		}
		st.Variants = append(st.Variants, vs)
	}
	return st
}

// writeMaster rewrites the cross-variant playlist from the running variants.
//
// polyemesis writes it rather than FFmpeg because variants on different
// renditions are different processes reading different relays, and no single
// muxer can see them all. Writing it ourselves is also what makes a rung that
// failed to start simply absent from the ladder instead of a broken entry every
// player retries.
func (m *Manager) writeMaster() error {
	m.mu.Lock()
	enabled := m.settings.Enabled
	vs := make([]*variant, 0, len(m.running))
	for _, v := range m.running {
		if v.proc != nil && v.err == "" {
			vs = append(vs, v)
		}
	}
	m.mu.Unlock()

	path := filepath.Join(m.dir, MasterPlaylist)
	if !enabled || len(vs) == 0 {
		_ = os.Remove(path)
		return nil
	}

	// Ascending bandwidth is the conventional order, and it is what makes a
	// player that starts on the first entry start on the cheapest rung rather
	// than the most expensive one.
	sort.Slice(vs, func(i, j int) bool {
		if vs[i].bandwidth != vs[j].bandwidth {
			return vs[i].bandwidth < vs[j].bandwidth
		}
		return vs[i].cfg.Name < vs[j].cfg.Name
	})

	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:6\n")
	for _, v := range vs {
		b.WriteString("#EXT-X-STREAM-INF:BANDWIDTH=" + strconv.Itoa(v.bandwidth))
		// RESOLUTION is omitted rather than guessed when the upstream size is
		// unknown: a wrong resolution makes a player pick the wrong rung, while
		// a missing one only makes it fall back to bandwidth, which is correct.
		if v.width > 0 && v.height > 0 {
			b.WriteString(",RESOLUTION=" + strconv.Itoa(v.width) + "x" + strconv.Itoa(v.height))
		}
		b.WriteString(",NAME=\"" + v.cfg.Name + "\"\n")
		b.WriteString(path2(v.cfg.Name, MediaPlaylist) + "\n")
	}

	// Written whole through a temp file: a player that fetched a half-written
	// master would cache a truncated ladder for the rest of the session.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// path2 joins with a forward slash regardless of platform: these are URLs
// inside a playlist, not filesystem paths, and a backslash on Windows would
// make every segment unreachable.
func path2(a, b string) string { return a + "/" + b }

func sortedNames(m map[string]*variant) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
