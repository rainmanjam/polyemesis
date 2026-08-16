// Package clips keeps the last N seconds of the live stream in memory and cuts
// a playable file out of it on demand.
//
// It is a relay consumer like any other: it binds a loopback UDP port, the hub
// sends it a copy of every datagram, and nothing it does can reach back into
// the streaming path. A capture that is slow, a disk that is full, a buffer
// that is empty — all of them are answered on the HTTP request that asked for
// them, and the destinations never learn anything happened.
package clips

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Subdir is where clips live under the recordings directory. A subdirectory
// rather than the recordings directory itself, because the recording index
// scans that directory flat and would otherwise adopt every clip as a
// recording segment with its own retention.
const Subdir = "clips"

// Prefix and Ext are the clip filename shape, and the only names this package
// will list or delete.
const (
	Prefix = "clip-"
	Ext    = ".ts"
	// stamp is the filename timestamp layout, matching the recorder's so a
	// human sorting the two directories sees the same shape in both.
	stamp = "20060102-150405"
)

// Buffer and retention defaults.
const (
	// DefaultWindowSeconds is a compromise, not a maximum: long enough that
	// "clip that" after something happens still catches it, short enough that
	// the ceiling below is rarely what is binding.
	DefaultWindowSeconds = 60
	MinWindowSeconds     = 5
	MaxWindowSeconds     = 300

	// DefaultMaxRingBytes is the hard ceiling on how much of the stream is
	// held in RAM. 128 MiB is about 60 s of a 17 Mbit/s feed, or 20 s of a 50
	// Mbit/s one — in both cases a number a media server can hold without
	// anybody noticing, which is the property being bought here.
	DefaultMaxRingBytes = 128 << 20
	MinMaxRingBytes     = 8 << 20
	MaxMaxRingBytes     = 2048 << 20

	// Disk retention. Clips are small and deliberate, so these are generous;
	// they exist to stop an unattended install filling a volume, not to ration
	// anything.
	DefaultMaxClips   = 200
	DefaultMaxAgeDays = 30
	DefaultMaxDiskMB  = 4096
)

// Config sizes the buffer and the retention that follows it.
type Config struct {
	// Dir is the clips directory, normally recordings/clips.
	Dir           string
	WindowSeconds int
	MaxRingBytes  int64
	MaxClips      int
	MaxAgeDays    int
	MaxDiskMB     int
}

// Normalized fills the zero values and clamps the rest, so a caller that only
// knows how many seconds it wants gets a working configuration.
func (c Config) Normalized() Config {
	if c.WindowSeconds == 0 {
		c.WindowSeconds = DefaultWindowSeconds
	}
	c.WindowSeconds = clampInt(c.WindowSeconds, MinWindowSeconds, MaxWindowSeconds)
	if c.MaxRingBytes == 0 {
		c.MaxRingBytes = DefaultMaxRingBytes
	}
	c.MaxRingBytes = clampInt64(c.MaxRingBytes, MinMaxRingBytes, MaxMaxRingBytes)
	if c.MaxClips <= 0 {
		c.MaxClips = DefaultMaxClips
	}
	if c.MaxAgeDays <= 0 {
		c.MaxAgeDays = DefaultMaxAgeDays
	}
	if c.MaxDiskMB <= 0 {
		c.MaxDiskMB = DefaultMaxDiskMB
	}
	return c
}

// Window is the configured history as a duration.
func (c Config) Window() time.Duration {
	return time.Duration(c.WindowSeconds) * time.Second
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampInt64(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Clip is one captured file.
type Clip struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	// Seconds is what the clip spans, from the datagram timestamps rather than
	// from a probe: the file is TS and the numbers are already known.
	Seconds float64 `json:"seconds"`
	// StartedAt is when the first datagram in the clip arrived, CreatedAt when
	// the operator asked for it. They differ by the clip's own length, and the
	// distinction is what makes "the clip from 20:31" mean anything.
	StartedAt       time.Time `json:"startedAt"`
	CreatedAt       time.Time `json:"createdAt"`
	KeyframeAligned bool      `json:"keyframeAligned"`
	Note            string    `json:"note,omitempty"`
}

// datagramSize matches the relay's: 1316 bytes of TS payload plus slack.
// Reading into a smaller buffer silently truncates a datagram, which in this
// package would mean a clip with a hole in it.
const datagramSize = 2048

// Capturer owns the UDP socket, the ring and the clips directory.
type Capturer struct {
	log      *slog.Logger
	cfg      Config
	buf      *Buffer
	conn     *net.UDPConn
	onChange func()
	now      func() time.Time

	// writeMu serialises captures so two simultaneous requests cannot pick the
	// same filename, and so a sweep never runs against a half-written clip.
	writeMu sync.Mutex

	closeOnce sync.Once
	done      chan struct{}
}

// Option customises a Capturer for tests.
type Option func(*Capturer)

// WithClock replaces the clock, so a test can produce a deterministic filename.
func WithClock(fn func() time.Time) Option {
	return func(c *Capturer) { c.now = fn }
}

// Open binds the capture socket and starts reading.
//
// relayURL is what Hub.Subscribe returned, i.e. udp://host:port. The address is
// taken from it rather than assumed to be loopback, because the hub does not
// always advertise loopback.
func Open(log *slog.Logger, cfg Config, relayURL string, onChange func(), opts ...Option) (*Capturer, error) {
	cfg = cfg.Normalized()
	addr, err := relayAddr(relayURL)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("clips: create %s: %w", cfg.Dir, err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("clips: bind %s: %w", addr, err)
	}
	// The same generous receive buffer the relay uses. It is what absorbs the
	// pause while a large cut is copied out of the ring.
	_ = conn.SetReadBuffer(8 << 20)

	c := &Capturer{
		log:      log,
		cfg:      cfg,
		buf:      NewBuffer(cfg.Window(), cfg.MaxRingBytes),
		conn:     conn,
		onChange: onChange,
		now:      time.Now,
		done:     make(chan struct{}),
	}
	for _, opt := range opts {
		opt(c)
	}
	go c.run()
	return c, nil
}

// relayAddr turns udp://host:port[?opts] into an address to bind.
func relayAddr(u string) (*net.UDPAddr, error) {
	s := strings.TrimPrefix(u, "udp://")
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	addr, err := net.ResolveUDPAddr("udp", s)
	if err != nil {
		return nil, fmt.Errorf("clips: relay url %q: %w", u, err)
	}
	return addr, nil
}

func (c *Capturer) run() {
	buf := make([]byte, datagramSize)
	for {
		select {
		case <-c.done:
			return
		default:
		}
		n, _, err := c.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-c.done:
				return
			default:
			}
			c.log.Debug("clip buffer read error", "err", err)
			continue
		}
		c.buf.Write(buf[:n], c.now())
	}
}

// Close stops reading and releases the socket. The buffer's memory goes with
// it, which is the point: turning the feature off must actually give the RAM
// back.
func (c *Capturer) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.done)
		err = c.conn.Close()
	})
	return err
}

// Dir is where clips are written.
func (c *Capturer) Dir() string { return c.cfg.Dir }

// Addr is the socket the hub sends to. Chiefly for logging, and for a caller
// that asked for port 0 and now needs to know what it got.
func (c *Capturer) Addr() *net.UDPAddr {
	a, _ := c.conn.LocalAddr().(*net.UDPAddr)
	return a
}

// Config is the capturer's resolved configuration.
func (c *Capturer) Config() Config { return c.cfg }

// Stats reports how much history is available to clip.
func (c *Capturer) Stats() Stats { return c.buf.Stats() }

// Capture writes the last d of the stream to a file and returns it.
func (c *Capturer) Capture(d time.Duration) (Clip, error) {
	if d <= 0 {
		d = c.cfg.Window()
	}
	if max := c.cfg.Window(); d > max {
		// Clamped rather than refused: asking for more than is held is a
		// perfectly reasonable thing for a person in a hurry to do, and the
		// honest answer is "here is everything there was".
		d = max
	}

	cut, err := c.buf.Cut(d, c.now())
	if err != nil {
		return Clip{}, err
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if err := os.MkdirAll(c.cfg.Dir, 0o755); err != nil {
		return Clip{}, fmt.Errorf("clips: create %s: %w", c.cfg.Dir, err)
	}
	name, path, err := c.reserve(cut.Start)
	if err != nil {
		return Clip{}, err
	}

	// Written to a temporary file and renamed, so a clip only ever appears in
	// the listing complete. A half-written .ts that a user downloads while the
	// write is still going is a support ticket about a corrupt file.
	tmp := path + ".partial"
	if err := os.WriteFile(tmp, cut.Data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return Clip{}, fmt.Errorf("clips: write %s: %w", name, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return Clip{}, fmt.Errorf("clips: publish %s: %w", name, err)
	}

	clip := Clip{
		Name:            name,
		Bytes:           int64(len(cut.Data)),
		Seconds:         cut.Seconds,
		StartedAt:       cut.Start,
		CreatedAt:       c.now(),
		KeyframeAligned: cut.KeyframeAligned,
		Note:            cut.Note,
	}
	c.log.Info("clip captured", "name", name, "seconds", fmt.Sprintf("%.1f", clip.Seconds),
		"bytes", clip.Bytes, "keyframeAligned", clip.KeyframeAligned)

	if _, err := c.sweepLocked(); err != nil {
		// Retention failing must not lose the clip the operator just asked for.
		c.log.Warn("clip retention sweep", "err", err)
	}
	c.changed()
	return clip, nil
}

// reserve picks a free filename for a clip starting at t.
func (c *Capturer) reserve(t time.Time) (name, path string, err error) {
	base := Prefix + t.Local().Format(stamp)
	for i := 0; i < 100; i++ {
		n := base + Ext
		if i > 0 {
			n = fmt.Sprintf("%s-%d%s", base, i+1, Ext)
		}
		p := filepath.Join(c.cfg.Dir, n)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return n, p, nil
		}
	}
	return "", "", fmt.Errorf("clips: no free filename for %s", base)
}

// Resolve turns a clip name into an absolute path, refusing anything that
// escapes the clips directory. Every filesystem operation in this package goes
// through it, because the name arrives from an HTTP request.
func (c *Capturer) Resolve(name string) (string, error) {
	return Resolve(c.cfg.Dir, name)
}

// Resolve is the package-level form, for callers that hold the directory but
// not a running capturer — a download handler still has to work after the
// buffer has been switched off.
func Resolve(dir, name string) (string, error) {
	// ContainsAny over BOTH separators, spelled literally. The previous form --
	// os.PathSeparator or '/' -- reads as "both" and is both only on Windows,
	// because on Linux that constant IS '/'. See internal/media for the same
	// note; internal/recording is where this drifted far enough to matter.
	if !IsClip(name) || strings.ContainsAny(name, `/\`) ||
		name == "." || name == ".." {
		return "", fmt.Errorf("invalid clip name %q", name)
	}
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(base, name)
	if !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("clip %q escapes the clips directory", name)
	}
	return full, nil
}

// IsClip reports whether a filename is one this build wrote. Deliberately
// narrow: List and Delete only ever touch names that match, so nothing else
// that ends up in the directory can be swept away by retention.
func IsClip(name string) bool {
	return strings.HasPrefix(name, Prefix) &&
		strings.EqualFold(filepath.Ext(name), Ext) &&
		len(name) > len(Prefix)+len(Ext)
}

// List returns the clips on disk, newest first.
func (c *Capturer) List() ([]Clip, error) { return List(c.cfg.Dir) }

// List is the package-level form. See Resolve for why both exist.
func List(dir string) ([]Clip, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Clip{}, nil
		}
		return nil, err
	}
	out := make([]Clip, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !IsClip(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Clip{
			Name:      e.Name(),
			Bytes:     info.Size(),
			StartedAt: startTimeFromName(e.Name(), info.ModTime()),
			CreatedAt: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].Name > out[j].Name
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out, nil
}

// startTimeFromName recovers the capture start from the filename, falling back
// to mtime. The name is the better source: it records when the CONTENT starts,
// while mtime records when the write finished.
func startTimeFromName(name string, fallback time.Time) time.Time {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	base = strings.TrimPrefix(base, Prefix)
	parts := strings.Split(base, "-")
	if len(parts) >= 2 {
		if t, err := time.ParseInLocation(stamp, parts[0]+"-"+parts[1], time.Local); err == nil {
			return t
		}
	}
	return fallback
}

// Delete removes one clip.
func (c *Capturer) Delete(name string) error {
	path, err := c.Resolve(name)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := os.Remove(path); err != nil {
		return err
	}
	c.log.Info("clip deleted", "name", name)
	c.changed()
	return nil
}

// Sweep applies retention and reports how many clips it removed.
func (c *Capturer) Sweep() (int, error) {
	c.writeMu.Lock()
	n, err := c.sweepLocked()
	c.writeMu.Unlock()
	if n > 0 {
		c.changed()
	}
	return n, err
}

// sweepLocked enforces the three retention limits in the order they matter:
// age first, because an old clip is old whatever else is true, then count, then
// total bytes. Oldest goes first in every case.
func (c *Capturer) sweepLocked() (int, error) {
	list, err := List(c.cfg.Dir)
	if err != nil {
		return 0, err
	}
	// Oldest first, so the removals below can simply walk forward.
	sort.Slice(list, func(i, j int) bool { return list[i].StartedAt.Before(list[j].StartedAt) })

	var total int64
	for _, cl := range list {
		total += cl.Bytes
	}
	cutoff := c.now().Add(-time.Duration(c.cfg.MaxAgeDays) * 24 * time.Hour)
	maxBytes := int64(c.cfg.MaxDiskMB) << 20

	removed := 0
	keep := make([]Clip, 0, len(list))
	for _, cl := range list {
		if cl.StartedAt.Before(cutoff) {
			if c.remove(cl, "older than the retention window") {
				total -= cl.Bytes
				removed++
				continue
			}
		}
		keep = append(keep, cl)
	}

	for len(keep) > 0 && (len(keep) > c.cfg.MaxClips || total > maxBytes) {
		cl := keep[0]
		reason := "over the clip count limit"
		if total > maxBytes {
			reason = "over the clip disk limit"
		}
		if !c.remove(cl, reason) {
			break
		}
		total -= cl.Bytes
		removed++
		keep = keep[1:]
	}
	return removed, nil
}

func (c *Capturer) remove(cl Clip, reason string) bool {
	path, err := Resolve(c.cfg.Dir, cl.Name)
	if err != nil {
		c.log.Warn("clip retention skipped a name it could not resolve", "name", cl.Name, "err", err)
		return false
	}
	if err := os.Remove(path); err != nil {
		c.log.Warn("delete clip", "name", cl.Name, "err", err)
		return false
	}
	c.log.Info("clip removed by retention", "name", cl.Name, "reason", reason)
	return true
}

func (c *Capturer) changed() {
	if c.onChange != nil {
		c.onChange()
	}
}

// Usage reports what the clips directory is holding.
type Usage struct {
	Count     int   `json:"count"`
	UsedBytes int64 `json:"usedBytes"`
	MaxBytes  int64 `json:"maxBytes"`
	MaxClips  int   `json:"maxClips"`
}

// Usage totals the clips on disk.
func (c *Capturer) Usage() (Usage, error) { return UsageOf(c.cfg) }

// UsageOf is Capturer.Usage for a caller that has no capturer.
//
// The directory outlives the buffer, and outlives the programme: clips are
// files beside the recordings, so an install with the buffer switched off — or
// with no engine at all — still has a listing and a retention to report it
// against. One implementation rather than two, because the two used to be the
// same fifteen lines in two packages and the API's copy is the one an operator
// reads on a server with nothing running.
func UsageOf(cfg Config) (Usage, error) {
	list, err := List(cfg.Dir)
	if err != nil {
		return Usage{}, err
	}
	u := Usage{
		Count:    len(list),
		MaxBytes: int64(cfg.MaxDiskMB) << 20,
		MaxClips: cfg.MaxClips,
	}
	for _, cl := range list {
		u.UsedBytes += cl.Bytes
	}
	return u, nil
}
