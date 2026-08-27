// Package recording owns the recordings directory: indexing the segments the
// recorder writes, and enforcing the retention policy.
//
// The recorder process itself is owned by the engine. This package is the
// bookkeeping around it, kept separate because retention deletes files and
// that deserves its own small, auditable surface.
package recording

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/media"
)

// Manager scans and prunes the recordings directory.
type Manager struct {
	log   *slog.Logger
	store *db.DB
	dir   string
	// ffprobe measures finished segments. Empty means "not wired up": the
	// index still works, it just carries no duration or track count.
	ffprobe string
	// onChange is called after any scan or sweep that altered the index, so
	// the UI can refresh without polling.
	onChange func()
	// onStorage is called when the free-space floor halts or resumes
	// recording, so the owner of the recorder process can act on it.
	onStorage func(StorageState)
	// freeSpace is diskFree in production; tests substitute a volume they can
	// fill on demand, which no real temp directory lets them do.
	freeSpace func(string) (uint64, uint64, error)
	// sourceID is the programme these segments came from, stamped onto every
	// row this manager indexes. Nil on a manager with no programme.
	sourceID *int64

	storageMu sync.Mutex
	storage   StorageState
}

// StorageState is the free-space guard's verdict on whether the volume can
// take more recording.
type StorageState struct {
	Halted bool `json:"halted"`
	// Reason is written for a human reading an error banner, not parsed.
	Reason string `json:"reason,omitempty"`
}

// Option configures a Manager at construction.
type Option func(*Manager)

// WithFFprobe supplies the ffprobe binary used to measure finished segments.
func WithFFprobe(bin string) Option {
	return func(m *Manager) { m.ffprobe = bin }
}

// WithStorageGuard registers the callback fired when the free-space floor
// halts recording, and again when recovered space lets it resume.
// WithSourceID names the programme whose segments this manager indexes.
//
// Without it every recording row was written with a NULL source_id, and the
// clip editor then labelled every clip with the DEFAULT programme's track
// names -- including clips cut from somebody else's show. Unset stays nil,
// which is the honest answer for a manager that is not attached to a
// programme at all (see engine.New's storeless construction in tests).
func WithSourceID(id int64) Option {
	return func(m *Manager) {
		if id > 0 {
			m.sourceID = &id
		}
	}
}

func WithStorageGuard(fn func(StorageState)) Option {
	return func(m *Manager) { m.onStorage = fn }
}

// New creates a Manager.
func New(log *slog.Logger, store *db.DB, dir string, onChange func(), opts ...Option) *Manager {
	m := &Manager{log: log, store: store, dir: dir, onChange: onChange, freeSpace: diskFree}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Dir is the recordings directory.
func (m *Manager) Dir() string { return m.dir }

// Run scans and sweeps on an interval until ctx is cancelled.
func (m *Manager) Run(ctx context.Context, settings func() db.RecordingSettings) {
	// Once at startup so a restart immediately reflects whatever is on disk,
	// including segments written by a previous run that crashed.
	m.ScanAndSweep(settings())

	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.ScanAndSweep(settings())
		}
	}
}

// ScanAndSweep reconciles the index with the filesystem, then applies the
// retention policy.
func (m *Manager) ScanAndSweep(s db.RecordingSettings) {
	changed, err := m.Scan()
	if err != nil {
		m.log.Warn("recording scan failed", "err", err)
	}
	swept, err := m.Sweep(s)
	if err != nil {
		m.log.Warn("recording retention sweep failed", "err", err)
	}
	// Stems follow their masters, so this runs after the sweep that decided
	// which masters survived. Without it they accumulate forever: the index
	// only ever holds video containers, so a stem is never scanned and would
	// never be a candidate for any retention rule.
	stemsSwept, err := m.SweepStems()
	if err != nil {
		m.log.Warn("stem retention sweep failed", "err", err)
	}
	// Proxies and thumbnails follow their masters for exactly the reason stems
	// do: they live under a subdirectory the index never scans, so nothing else
	// would ever consider them for retention.
	derivedSwept := m.sweepDerived()
	// Sessions are derived from the index, so they are refreshed after
	// everything that could have changed it — a first measurement or a swept
	// member both move a session's span.
	grouped := m.groupSessions(s)
	// After the sweep, not before: retention may have just freed enough room
	// to lift a halt, and waiting another tick to notice would cost a whole
	// segment of footage.
	guarded := m.CheckFreeSpace(s)
	if (changed || swept || stemsSwept || derivedSwept || grouped || guarded) && m.onChange != nil {
		m.onChange()
	}
}

// bytesPerGB is the binary gigabyte the size settings are expressed in.
const bytesPerGB = 1024 * 1024 * 1024

// CheckFreeSpace applies the free-space floor and reports whether the halt
// state changed. One statfs, so it is cheap enough for the sweep tick.
//
// A recorder left running until writes fail does not fail alone: it takes the
// database, the HLS preview and anything else on the volume with it. Stopping
// early is the only outcome where the operator still has a working box.
func (m *Manager) CheckFreeSpace(s db.RecordingSettings) bool {
	if s.MinFreeGB <= 0 {
		return m.setStorage(StorageState{})
	}
	free, total, err := m.freeSpace(m.dir)
	if err != nil {
		m.log.Warn("free-space check failed", "dir", m.dir, "err", err)
		return false
	}
	// Platforms without a statfs report zeroes rather than an error; halting
	// on that would disable recording everywhere it is unimplemented.
	if total == 0 {
		return false
	}

	floor := s.MinFreeGB * bytesPerGB
	// Resume with headroom. Resuming exactly at the floor would halt again on
	// the next tick, cycling the recorder every 30 seconds and shredding the
	// recording into unusable fragments.
	resume := floor * 1.25

	if m.Storage().Halted {
		if float64(free) < resume {
			return false
		}
		m.log.Info("free space recovered; recording may resume",
			"freeGb", float64(free)/bytesPerGB, "floorGb", s.MinFreeGB)
		return m.setStorage(StorageState{})
	}

	if float64(free) >= floor {
		return false
	}
	reason := fmt.Sprintf("recording halted: %.1f GB free on %s is below the %.1f GB floor",
		float64(free)/bytesPerGB, m.dir, s.MinFreeGB)
	m.log.Error("free space below floor; halting recording",
		"dir", m.dir, "freeGb", float64(free)/bytesPerGB, "floorGb", s.MinFreeGB)
	return m.setStorage(StorageState{Halted: true, Reason: reason})
}

// Storage reports the current verdict of the free-space guard.
func (m *Manager) Storage() StorageState {
	m.storageMu.Lock()
	defer m.storageMu.Unlock()
	return m.storage
}

// RecordingAllowed is what the owner of the recorder process consults before
// starting or keeping it alive.
func (m *Manager) RecordingAllowed() bool { return !m.Storage().Halted }

// StorageGuarded reports whether a storage guard is registered.
//
// Exported for one caller and that caller is a test, which is worth these
// words. The guard is a callback whose ABSENCE IS SILENT: drop
// WithStorageGuard from a construction site and nothing fails, nothing logs,
// and the only symptom arrives weeks later as a volume filled to the last byte
// by a recorder nobody stopped. The engine's own manager must carry one and
// the shared read-only instance must not, and the test that guards that
// pairing had no way to say so -- comparing the two pointers stays green with
// the option deleted.
func (m *Manager) StorageGuarded() bool { return m.onStorage != nil }

func (m *Manager) setStorage(st StorageState) bool {
	m.storageMu.Lock()
	if m.storage == st {
		m.storageMu.Unlock()
		return false
	}
	m.storage = st
	m.storageMu.Unlock()

	if m.onStorage != nil {
		m.onStorage(st)
	}
	return true
}

// Scan indexes every .mkv in the recordings directory and drops index rows
// whose file has disappeared. Filesystem is the source of truth: a user who
// deletes a file by hand should not be left with a phantom row.
//
// Finished segments are also measured once, so the index can report how long
// each one runs and how many audio tracks it kept.
func (m *Manager) Scan() (bool, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	indexed, err := m.store.ListRecordings()
	if err != nil {
		return false, err
	}
	measured := map[string]bool{}
	for _, r := range indexed {
		measured[r.Filename] = r.DurationMS > 0
	}

	changed := false
	onDisk := map[string]bool{}
	segments := make([]*db.Recording, 0, len(entries))

	for _, e := range entries {
		if e.IsDir() || !isRecording(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		onDisk[e.Name()] = true

		// The recorder is still appending to the newest segment, so its size
		// changes on every scan; that is expected, not a reason to skip it.
		segments = append(segments, &db.Recording{
			Filename:  e.Name(),
			StartedAt: startTimeFromName(e.Name(), info.ModTime()),
			Bytes:     info.Size(),
		})
	}

	live := newestSegment(segments)
	for _, rec := range segments {
		// Probing costs an ffprobe per file, so it happens once per segment,
		// and never on the one the recorder is still writing: its duration
		// would be wrong the moment it was recorded.
		if m.ffprobe != "" && rec.Filename != live && !measured[rec.Filename] {
			if err := m.measure(rec); err != nil {
				m.log.Warn("probe recording", "file", rec.Filename, "err", err)
			}
		}
		// The programme this manager belongs to, stamped at index time. It is
		// the only moment anything knows it: the filename does not carry it and
		// a later reader cannot work it out.
		if rec.SourceID == nil {
			rec.SourceID = m.sourceID
		}
		if err := m.store.UpsertRecording(rec); err != nil {
			m.log.Warn("index recording", "file", rec.Filename, "err", err)
			continue
		}
		changed = true
	}

	for _, r := range indexed {
		if !onDisk[r.Filename] {
			if err := m.store.DeleteRecordingByFilename(r.Filename); err != nil {
				m.log.Warn("drop missing recording from index", "file", r.Filename, "err", err)
				continue
			}
			m.log.Info("recording disappeared from disk; removed from index", "file", r.Filename)
			changed = true
		}
	}
	return changed, nil
}

// newestSegment names the segment the recorder is presumably still appending
// to. Start time, not mtime: mtime on an older segment can be bumped by a
// filesystem touch, while the encoded start time cannot move.
func newestSegment(segments []*db.Recording) string {
	newest := ""
	var at time.Time
	for _, s := range segments {
		// Equal start times are broken by name so the choice is stable across
		// scans; without that, two same-second segments would take turns being
		// "live" and neither would ever be measured.
		if newest == "" || s.StartedAt.After(at) || (s.StartedAt.Equal(at) && s.Filename > newest) {
			newest, at = s.Filename, s.StartedAt
		}
	}
	return newest
}

// segmentProbe is the slice of ffprobe's JSON we need: overall duration and
// enough of each stream to count the audio ones.
type segmentProbe struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// measure fills in DurationMS and Tracks by running ffprobe over the segment.
func (m *Manager) measure(rec *db.Recording) error {
	path, err := m.Resolve(rec.Filename)
	if err != nil {
		return err
	}
	// A segment is a local file, so ffprobe returns almost immediately; the
	// timeout only exists so a corrupt file cannot stall the scan loop.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, m.ffprobe,
		"-hide_banner",
		"-loglevel", "error",
		"-print_format", "json",
		"-show_entries", "format=duration:stream=codec_type",
		path,
	).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return fmt.Errorf("ffprobe: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return err
	}

	var p segmentProbe
	if err := json.Unmarshal(out, &p); err != nil {
		return fmt.Errorf("parse ffprobe output: %w", err)
	}
	secs, err := strconv.ParseFloat(p.Format.Duration, 64)
	if err != nil || secs <= 0 {
		return fmt.Errorf("ffprobe reported no duration for %s", rec.Filename)
	}
	tracks := 0
	for _, s := range p.Streams {
		if s.CodecType == "audio" {
			tracks++
		}
	}
	rec.DurationMS = int64(secs * 1000)
	rec.Tracks = tracks
	return nil
}

// Sweep enforces the retention policy: age first, then total size, deleting
// oldest-first. Returns whether anything was deleted.
func (m *Manager) Sweep(s db.RecordingSettings) (bool, error) {
	recs, err := m.store.ListRecordings()
	if err != nil {
		return false, err
	}
	if len(recs) == 0 {
		return false, nil
	}

	// Oldest first: retention always sacrifices the oldest material.
	sort.Slice(recs, func(i, j int) bool { return recs[i].StartedAt.Before(recs[j].StartedAt) })

	deleted := false

	// #504: the age branch used to carry no protection for the segment the
	// recorder is still appending to, while the size branch four lines below
	// protected it only by array position ("the last element of recs"), a
	// heuristic that happened to work but never named what it was protecting.
	// Neither branch actually asks the recorder which file is open -- that
	// process is owned by another package and does not report it here -- so
	// both now derive the same identity from the index (latest start time)
	// through one shared helper, instead of one branch remembering a rule the
	// other never got.
	live := liveSegment(recs)

	if s.MaxAgeHours > 0 {
		cutoff := time.Now().Add(-time.Duration(s.MaxAgeHours) * time.Hour)
		remaining := recs[:0]
		for _, r := range recs {
			if r.Filename != live && r.StartedAt.Before(cutoff) {
				if m.delete(r, fmt.Sprintf("older than %dh", s.MaxAgeHours)) {
					deleted = true
					continue
				}
			}
			remaining = append(remaining, r)
		}
		recs = remaining
	}

	if s.MaxGB > 0 {
		limit := int64(s.MaxGB * 1024 * 1024 * 1024)
		var total int64
		for _, r := range recs {
			total += r.Bytes
		}
		// Stems are on the same volume and are not in the index, so a cap that
		// ignored them would be several times looser than the number the
		// operator typed. They are charged against the cap here and credited
		// back as their masters go, so the loop never deletes more footage than
		// the cap actually requires.
		stems, serr := m.stemSizes()
		if serr != nil {
			m.log.Warn("stem sizes unreadable; size cap covers masters only", "err", serr)
		}
		for _, st := range stems {
			total += st.bytes
		}
		// Never delete the live segment: see the #504 note above liveSegment.
		for i := 0; total > limit && i < len(recs); i++ {
			if recs[i].Filename == live {
				continue
			}
			if !m.delete(recs[i], fmt.Sprintf("size cap %.1f GB exceeded", s.MaxGB)) {
				continue
			}
			total -= recs[i].Bytes
			// SweepStems runs straight after this and orphans everything
			// starting before the oldest surviving master, so those bytes are
			// as good as freed already.
			if i+1 < len(recs) {
				total -= creditStems(stems, recs[i+1].StartedAt.Add(-stemSweepSlack))
			}
			deleted = true
		}
	}

	return deleted, nil
}

// liveSegment names the one recording retention must never delete: the
// segment with the latest start time, which is the one the recorder is
// presumably still appending to. This is the same heuristic Scan uses (see
// newestSegment) -- start time, not file position -- applied here so the age
// and size branches of Sweep share one notion of "the open file" instead of
// each keeping (or forgetting) their own. It is still an inference from the
// index, not a query of the recorder's actual open file handle: the recorder
// process belongs to another package and this one has no channel to ask it.
func liveSegment(recs []db.Recording) string {
	live := ""
	var at time.Time
	for _, r := range recs {
		if live == "" || r.StartedAt.After(at) || (r.StartedAt.Equal(at) && r.Filename > live) {
			live, at = r.Filename, r.StartedAt
		}
	}
	return live
}

// stemSize is one stem file as the size cap sees it: when its master started,
// and what it costs.
type stemSize struct {
	start time.Time
	bytes int64
}

// stemSizes lists every stem on disk, oldest first. Anything this build does
// not recognise as a stem is skipped, so a stray file in the directory is never
// charged against the cap and never deleted on its account.
func (m *Manager) stemSizes() ([]stemSize, error) {
	entries, err := os.ReadDir(m.StemsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]stemSize, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		start, _, ok := ParseStemFilename(e.Name())
		if !ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, stemSize{start: start, bytes: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	return out, nil
}

// creditStems returns the bytes of every stem starting before cutoff, zeroing
// each as it goes so a second call with a later cutoff cannot count it twice.
func creditStems(stems []stemSize, cutoff time.Time) int64 {
	var freed int64
	for i := range stems {
		if !stems[i].start.Before(cutoff) {
			break
		}
		freed += stems[i].bytes
		stems[i].bytes = 0
	}
	return freed
}

func (m *Manager) delete(r db.Recording, reason string) bool {
	path, err := m.Resolve(r.Filename)
	if err != nil {
		m.log.Warn("refusing to delete recording", "file", r.Filename, "err", err)
		return false
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		m.log.Warn("delete recording", "file", r.Filename, "err", err)
		return false
	}
	if err := m.store.DeleteRecording(r.ID); err != nil {
		m.log.Warn("de-index recording", "file", r.Filename, "err", err)
	}
	m.removeDerived(r.Filename)
	m.log.Info("recording deleted by retention policy", "file", r.Filename, "reason", reason)
	return true
}

// Delete removes one recording by id, for the UI's delete button.
func (m *Manager) Delete(id int64) error {
	r, err := m.store.GetRecording(id)
	if err != nil {
		return err
	}
	path, err := m.Resolve(r.Filename)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := m.store.DeleteRecording(id); err != nil {
		return err
	}
	m.removeDerived(r.Filename)
	// The session this belonged to may now be empty, and its span is wrong
	// either way. Both are cheap and both are wrong until something says so.
	if err := m.store.RecalcSessions(); err != nil {
		m.log.Warn("cannot recalculate session spans", "err", err)
	}
	if _, err := m.store.PruneEmptySessions(); err != nil {
		m.log.Warn("cannot prune empty sessions", "err", err)
	}
	if m.onChange != nil {
		m.onChange()
	}
	return nil
}

// Resolve turns an index filename into an absolute path, refusing anything
// that escapes the recordings directory. Every filesystem operation in this
// package goes through it, because the filename ultimately originates from a
// database row and must never be trusted as a path.
func (m *Manager) Resolve(name string) (string, error) {
	// BOTH separators, on every platform -- not just the local one.
	//
	// This read `strings.ContainsRune(name, os.PathSeparator)`, which is a
	// check whose MEANING CHANGES WITH GOOS: on Windows that constant is '\',
	// so a forward slash passed validation and filepath.Join happily turned
	// "a/b" into a path pointing into a subdirectory. Resolve("/etc/passwd")
	// returned <recordings>\etc\passwd instead of an error.
	//
	// The final prefix check below still contained it, so this was a breach of
	// the "a recording name is a bare filename" contract rather than a live
	// escape -- but it was the outer of two defences failing silently, and the
	// name it is validating comes from a database row. Note that the sibling
	// checks in internal/clips, internal/media and internal/api all had the
	// two-separator form already; this one copy drifted.
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid recording name %q", name)
	}
	base, err := filepath.Abs(m.dir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(base, name)
	if !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("recording %q escapes the recordings directory", name)
	}
	return full, nil
}

// ResolveForWrite is Resolve for a file a process is about to CREATE: it
// returns a path that FFmpeg will accept, without ever putting existing footage
// at risk.
//
// FFmpeg refuses an existing output file and exits, so a destination writing to
// a fixed name dies permanently the first time it restarts — an ingest blip at
// 3am ends the recording for the night. The obvious repair, passing -y, is
// worse: every respawn would truncate the file, so a destination that flapped
// once would hand back an empty recording instead of a broken one.
//
// So the rule is: never destroy bytes. A name that is free, or that holds a
// zero-byte file left by a process that died before writing anything, is used
// as given — which keeps the ordinary case producing exactly the filename the
// operator configured. A name that holds real footage yields a timestamped
// sibling instead, so the restart lands beside the earlier take rather than on
// top of it.
//
// The timestamp is seconds-resolution and the counter covers the rest: two
// restarts inside one second is a crash loop, and a crash loop must not be able
// to make this function return a path that already has data in it.
func (m *Manager) ResolveForWrite(name string) (string, error) {
	full, err := m.Resolve(name)
	if err != nil {
		return "", err
	}
	if claim(full) {
		return full, nil
	}
	ext := filepath.Ext(full)
	stem := strings.TrimSuffix(full, ext)
	stamp := time.Now().Format("20060102-150405")
	for n := 0; n < 1000; n++ {
		cand := fmt.Sprintf("%s-%s%s", stem, stamp, ext)
		if n > 0 {
			cand = fmt.Sprintf("%s-%s-%d%s", stem, stamp, n, ext)
		}
		if claim(cand) {
			return cand, nil
		}
	}
	return "", fmt.Errorf("recording %q: no free filename", name)
}

// claim reports whether a path is now free for FFmpeg to create, clearing an
// empty leftover if that is all that stands in the way.
//
// The removal is what makes this different from a plain existence check.
// FFmpeg refuses ANY existing output path, so a zero-byte file left by a
// process that died before writing anything wedges the destination exactly as
// thoroughly as a full one — and unlike a full one, there is nothing in it to
// protect.
func claim(path string) bool {
	if !usable(path) {
		return false
	}
	err := os.Remove(path)
	return err == nil || errors.Is(err, os.ErrNotExist)
}

// usable reports whether a path can be written without losing anything: either
// nothing is there, or what is there is an empty file.
//
// A stat error other than "not found" counts as unusable. Guessing that an
// unreadable path is safe to overwrite is the one wrong answer here.
func usable(path string) bool {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() && info.Size() == 0
}

// DiskUsage reports total indexed bytes and the free space on the volume.
type DiskUsage struct {
	UsedBytes  int64  `json:"usedBytes"`
	FreeBytes  uint64 `json:"freeBytes"`
	TotalBytes uint64 `json:"totalBytes"`
	Count      int    `json:"count"`
	// Storage carries the free-space guard's verdict so the recordings page
	// can explain a recorder that stopped on its own.
	Storage StorageState `json:"storage"`
}

// Usage reports recordings disk usage.
func (m *Manager) Usage() (DiskUsage, error) {
	var u DiskUsage
	recs, err := m.store.ListRecordings()
	if err != nil {
		return u, err
	}
	u.Count = len(recs)
	u.Storage = m.Storage()
	for _, r := range recs {
		u.UsedBytes += r.Bytes
	}
	// Stems are not in the index — nothing indexes them — so without this a
	// stems-enabled install under-reports its own footprint by roughly the
	// track count. That is the direction that quietly fills a volume.
	if b, err := m.StemBytes(); err == nil {
		u.UsedBytes += b
	} else {
		m.log.Warn("stem usage unreadable; disk figure excludes stems", "err", err)
	}
	// Proxies and contact sheets are not in the index either, and a library
	// with a proxy per recording is not a small addition to the footprint.
	if b, err := media.Bytes(m.dir); err == nil {
		u.UsedBytes += b
	} else {
		m.log.Warn("derived-media usage unreadable; disk figure excludes proxies", "err", err)
	}
	free, total, err := m.freeSpace(m.dir)
	if err == nil {
		u.FreeBytes, u.TotalBytes = free, total
	}
	return u, nil
}

func isRecording(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".mkv" || ext == ".mp4" || ext == ".ts"
}

// startTimeFromName recovers the segment start from the strftime-formatted
// filename the recorder writes (rec-20240115-143000.mkv), falling back to the
// file's mtime. The name is more accurate: mtime moves as the file is written.
func startTimeFromName(name string, fallback time.Time) time.Time {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	parts := strings.Split(base, "-")
	if len(parts) >= 3 {
		stamp := parts[len(parts)-2] + "-" + parts[len(parts)-1]
		if t, err := time.ParseInLocation("20060102-150405", stamp, time.Local); err == nil {
			return t
		}
	}
	return fallback
}
