// Package recording owns the recordings directory: indexing the segments the
// recorder writes, and enforcing the retention policy.
//
// The recorder process itself is owned by the engine. This package is the
// bookkeeping around it, kept separate because retention deletes files and
// that deserves its own small, auditable surface.
package recording

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Manager scans and prunes the recordings directory.
type Manager struct {
	log   *slog.Logger
	store *db.DB
	dir   string
	// onChange is called after any scan or sweep that altered the index, so
	// the UI can refresh without polling.
	onChange func()
}

// New creates a Manager.
func New(log *slog.Logger, store *db.DB, dir string, onChange func()) *Manager {
	return &Manager{log: log, store: store, dir: dir, onChange: onChange}
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
	if (changed || swept) && m.onChange != nil {
		m.onChange()
	}
}

// Scan indexes every .mkv in the recordings directory and drops index rows
// whose file has disappeared. Filesystem is the source of truth: a user who
// deletes a file by hand should not be left with a phantom row.
func (m *Manager) Scan() (bool, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	changed := false
	onDisk := map[string]bool{}

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
		rec := &db.Recording{
			Filename:  e.Name(),
			StartedAt: startTimeFromName(e.Name(), info.ModTime()),
			Bytes:     info.Size(),
		}
		if err := m.store.UpsertRecording(rec); err != nil {
			m.log.Warn("index recording", "file", e.Name(), "err", err)
			continue
		}
		changed = true
	}

	indexed, err := m.store.ListRecordings()
	if err != nil {
		return changed, err
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

	if s.MaxAgeHours > 0 {
		cutoff := time.Now().Add(-time.Duration(s.MaxAgeHours) * time.Hour)
		remaining := recs[:0]
		for _, r := range recs {
			if r.StartedAt.Before(cutoff) {
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
		// Never delete the last remaining segment: it is almost certainly the
		// one being written right now, and deleting it would fight the
		// recorder rather than free space.
		for i := 0; total > limit && i < len(recs)-1; i++ {
			if m.delete(recs[i], fmt.Sprintf("size cap %.1f GB exceeded", s.MaxGB)) {
				total -= recs[i].Bytes
				deleted = true
			}
		}
	}

	return deleted, nil
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
	if name == "" || strings.ContainsRune(name, os.PathSeparator) || name == "." || name == ".." {
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

// DiskUsage reports total indexed bytes and the free space on the volume.
type DiskUsage struct {
	UsedBytes  int64  `json:"usedBytes"`
	FreeBytes  uint64 `json:"freeBytes"`
	TotalBytes uint64 `json:"totalBytes"`
	Count      int    `json:"count"`
}

// Usage reports recordings disk usage.
func (m *Manager) Usage() (DiskUsage, error) {
	var u DiskUsage
	recs, err := m.store.ListRecordings()
	if err != nil {
		return u, err
	}
	u.Count = len(recs)
	for _, r := range recs {
		u.UsedBytes += r.Bytes
	}
	free, total, err := diskFree(m.dir)
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
