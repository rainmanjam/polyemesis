package playout

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// keepNewestPerVariant is how many of each variant's most recent segments the
// sweeper will never delete, whatever the cap says.
//
// The newest segments are the ones the live playlist points at. Deleting them
// to satisfy a cap would not free meaningful space and would break playback for
// every viewer at once — which is a worse outcome than being briefly over a
// soft limit that the muxer's own window is already pulling back down.
const keepNewestPerVariant = 4

// segmentExts are the files the sweeper considers disposable. Playlists,
// manifests and DASH init segments are deliberately absent: they are tiny, and
// deleting one makes every segment beside it unreachable, so removing them
// costs playback without buying space.
var segmentExts = map[string]bool{
	".ts":  true,
	".m4s": true,
	".mp4": true,
	".m4v": true,
	".m4a": true,
}

// Usage is the playout directory's disk footprint.
type Usage struct {
	// Bytes is what the segments occupy, Files how many there are.
	Bytes int64 `json:"bytes"`
	Files int   `json:"files"`
	// LimitBytes is the configured cap, and OverLimit whether the last sweep
	// left it exceeded — which only happens when the untouchable newest
	// segments alone are larger than the cap, i.e. the cap is set too low for
	// the bitrate.
	LimitBytes int64 `json:"limitBytes"`
	OverLimit  bool  `json:"overLimit"`
	// Deleted is how many segments the sweeper has reclaimed since start.
	Deleted int64 `json:"deleted"`
}

// segmentFile is one candidate for deletion.
type segmentFile struct {
	path    string
	variant string
	mod     time.Time
	size    int64
}

// scanSegments walks the playout root and returns every disposable segment,
// oldest first.
//
// It is a walk rather than an index because the muxer, not polyemesis, creates
// these files, and after a restart the previous run's segments are on disk with
// nothing in memory that remembers them. That is the exact case the cap exists
// for, so the filesystem has to be the source of truth.
func scanSegments(root string) ([]segmentFile, error) {
	var out []segmentFile
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// A file that vanished mid-walk is the muxer pruning its own
			// window, which is the system working. Skip it and carry on rather
			// than abandoning the sweep.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !segmentExts[strings.ToLower(filepath.Ext(name))] {
			return nil
		}
		// DASH init segments carry the codec configuration; without one, every
		// media segment beside it is undecodable.
		if strings.HasPrefix(name, "init-") || strings.HasPrefix(name, "init_") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		variant := ""
		if parts := strings.Split(filepath.ToSlash(rel), "/"); len(parts) > 1 {
			variant = parts[0]
		}
		out = append(out, segmentFile{
			path:    path,
			variant: variant,
			mod:     info.ModTime(),
			size:    info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Oldest first, name-broken so the order is stable when a filesystem gives
	// a whole directory of segments the same coarse timestamp.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].mod.Equal(out[j].mod) {
			return out[i].mod.Before(out[j].mod)
		}
		return out[i].path < out[j].path
	})
	return out, nil
}

// protectedNewest marks the last keepNewestPerVariant segments of each variant,
// given the oldest-first ordering scanSegments produced.
func protectedNewest(files []segmentFile) map[string]bool {
	seen := map[string]int{}
	keep := map[string]bool{}
	for i := len(files) - 1; i >= 0; i-- {
		f := files[i]
		if seen[f.variant] < keepNewestPerVariant {
			keep[f.path] = true
			seen[f.variant]++
		}
	}
	return keep
}

// sweep enforces the total-size cap over the playout root, deleting oldest
// first, and returns the resulting usage.
//
// limitBytes <= 0 measures without deleting. That is not a way to disable the
// cap from settings — Validate refuses a zero cap — it is what lets a caller
// ask "how big is this" without the answer having side effects.
func sweep(root string, limitBytes int64, remove func(string) error) (Usage, error) {
	files, err := scanSegments(root)
	if err != nil {
		return Usage{LimitBytes: limitBytes}, err
	}

	var total int64
	for _, f := range files {
		total += f.size
	}
	u := Usage{Bytes: total, Files: len(files), LimitBytes: limitBytes}
	if limitBytes <= 0 || total <= limitBytes {
		return u, nil
	}

	keep := protectedNewest(files)
	for _, f := range files {
		if total <= limitBytes {
			break
		}
		if keep[f.path] {
			continue
		}
		if err := remove(f.path); err != nil && !os.IsNotExist(err) {
			continue
		}
		total -= f.size
		u.Files--
		u.Deleted++
	}
	u.Bytes = total
	u.OverLimit = total > limitBytes
	return u, nil
}

// clearVariantDir removes a variant's playlists, manifests and segments without
// removing the directory itself.
//
// Called when a variant starts, because the previous run's playlist would
// otherwise be served to the first player through the door, pointing at
// segments the new muxer is about to overwrite with different content — which
// a player renders as a burst of corruption rather than as a missing file.
func clearVariantDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
	return nil
}
