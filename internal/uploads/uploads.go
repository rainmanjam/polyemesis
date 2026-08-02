// Package uploads stores operator-supplied media files under the data
// directory, so a file can be broadcast without shell access to the box.
//
// Before this, going live from a file meant copying it onto the server
// yourself — fine for a Linux host you already have a session on, and a wall
// for everyone running the container. docs/SCHEDULED-BROADCAST.md said "no
// upload path — you place the file yourself"; this is that path.
//
// The whole package is about one risk. Every other file this product writes is
// named by polyemesis: a recording filename is generated, a clip's path is
// derived. An upload is the first case where a REMOTE CALLER influences both
// the bytes and the name, which is exactly the shape ../../SECURITY.md's path
// confinement section exists to defend. So the client's filename is treated as
// a hint that is thrown away, never as a path.
package uploads

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Dir is the subdirectory of the data directory that holds uploads. Its own
// directory rather than sharing recordings/: retention sweeps recordings, and
// a file an operator uploaded on purpose must not be deleted by a policy
// written about footage the server produced.
const Dir = "uploads"

// MaxNameLength caps the stored filename. Long enough for a descriptive name,
// short enough to stay well inside every filesystem's per-component limit once
// the random suffix is appended.
const MaxNameLength = 96

var (
	// ErrTooLarge is returned when the body exceeds the configured limit. The
	// partial file is removed before it is returned.
	ErrTooLarge = errors.New("upload exceeds the size limit")
	// ErrNoSpace is returned when the volume lacks room, checked BEFORE the
	// write rather than discovered during it.
	ErrNoSpace = errors.New("not enough free disk space for this upload")
	// ErrEmpty is returned for a zero-byte upload, which is always a mistake
	// and would otherwise become a selectable source that cannot play.
	ErrEmpty = errors.New("upload is empty")
)

// Store owns the uploads directory.
type Store struct {
	dir string
	// freeBytes reports free space on the volume. Injected so a test can
	// simulate a full disk without one.
	freeBytes func(path string) (uint64, error)
}

// New returns a Store rooted at <dataDir>/uploads, creating it if needed.
func New(dataDir string) (*Store, error) {
	dir := filepath.Join(dataDir, Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create uploads dir: %w", err)
	}
	return &Store{dir: dir, freeBytes: freeBytes}, nil
}

// Dir returns the absolute uploads directory.
func (s *Store) Dir() string { return s.dir }

// FreeBytes reports free bytes on the volume holding path — the same
// measurement Save's floor is checked against.
//
// Exported for internal/playlistmedia, whose normalised derivatives are
// additional copies of operator media on this same volume and must be measured
// the same way. The alternative was a FOURTH copy of statfs in this repo, and
// the three that exist are already one more than the comment in disk_unix.go is
// comfortable with.
func FreeBytes(path string) (uint64, error) { return freeBytes(path) }

// Resolve turns a stored name into an absolute path, refusing anything that is
// not a bare filename inside the uploads directory.
//
// The separator check tests BOTH separators on every platform, not just the
// local one. internal/recording carried `os.PathSeparator` here once, which is
// a check whose meaning changes with GOOS: on Windows that constant is '\', so
// a forward slash passed validation and Join turned "a/b" into a subdirectory
// path. The prefix check below is the second defence, and this is the first.
func (s *Store) Resolve(name string) (string, error) {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid upload name %q", name)
	}
	base, err := filepath.Abs(s.dir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(base, name)
	if !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("upload %q escapes the uploads directory", name)
	}
	return full, nil
}

// SafeName derives a stored filename from the client's, which is a hint and
// nothing more.
//
// The extension is preserved because ffmpeg's demuxer selection benefits from
// it, but it is re-derived from a whitelist rather than carried across: a
// client-supplied ".php" or ".sh" has no business naming a file on disk even
// in a directory nothing executes from.
//
// A random suffix is always appended. Two operators uploading "show.mp4" must
// not collide, and a name that cannot collide also cannot be used to overwrite
// an existing upload by guessing it.
func SafeName(hint string) string {
	ext := strings.ToLower(filepath.Ext(hint))
	if !allowedExt[ext] {
		ext = ".bin"
	}
	stem := strings.TrimSuffix(filepath.Base(hint), filepath.Ext(hint))
	stem = sanitise(stem)
	if stem == "" {
		stem = "upload"
	}
	if len(stem) > MaxNameLength {
		stem = stem[:MaxNameLength]
	}
	var buf [4]byte
	// crypto/rand rather than math/rand: this suffix is what makes the name
	// unguessable, so it should not come from a predictable sequence.
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("%s-%s%s", stem, hex.EncodeToString(buf[:]), ext)
}

// allowedExt is what may be preserved from a client filename. Containers
// polyemesis can plausibly read; anything else is stored as .bin and still
// works, because ffmpeg probes content rather than trusting the extension.
var allowedExt = map[string]bool{
	".ts": true, ".mp4": true, ".mkv": true, ".mov": true, ".m4v": true,
	".flv": true, ".webm": true, ".mpg": true, ".mpeg": true, ".m2ts": true,
	".wav": true, ".flac": true, ".aac": true, ".mp3": true, ".m4a": true,
}

// sanitise reduces a filename stem to characters that are safe in a path, a
// URL and a shell word, since the result travels through all three.
func sanitise(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_':
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		default:
			// Everything else -- spaces, quotes, dots, separators, control
			// characters, anything non-ASCII -- collapses to a single dash.
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// Origin values distinguish where a media file came from. They are DERIVED
// from which store an item was read out of, never stored alongside it.
//
// Storing provenance in a column would let it disagree with reality: a row in
// the recordings table IS something the server captured, by construction, and a
// file under uploads/ IS something an operator supplied. A duplicated fact
// drifts -- which is why this repo carries drift-guard tests elsewhere -- and
// here there is nothing to duplicate.
//
// It also needs no migration, which matters for a field being added days before
// a first release.
const (
	// OriginRecorded is footage the server captured from a live stream.
	OriginRecorded = "recorded"
	// OriginUploaded is a file an operator supplied.
	OriginUploaded = "uploaded"
	// OriginClip is derived from a recording by the clipper.
	OriginClip = "clip"
)

// File describes one stored upload.
type File struct {
	Name string `json:"name"`
	// Origin is always OriginUploaded here, and is present so a caller
	// assembling a mixed listing does not have to remember which endpoint a
	// given item arrived from.
	Origin   string    `json:"origin"`
	Bytes    int64     `json:"bytes"`
	Modified time.Time `json:"modified"`
	// PullURL is what to paste into a pull source: relative to the data
	// directory, which is what ffmpeg's file:// handling resolves against.
	PullURL string `json:"pullUrl"`
}

// Save streams r into the uploads directory and returns the stored file.
//
// It writes to a temporary file and renames on success, so a cancelled or
// oversized upload never leaves a partial file that looks selectable. That
// matters more here than in most upload paths: a half-written video is not
// obviously broken in a listing, and the operator would discover it when the
// broadcast they scheduled goes to air.
func (s *Store) Save(r io.Reader, hint string, maxBytes int64, minFreeBytes uint64) (File, error) {
	if minFreeBytes > 0 && s.freeBytes != nil {
		free, err := s.freeBytes(s.dir)
		if err != nil {
			// FAIL CLOSED. An earlier version skipped the guard when the check
			// itself errored, which is the wrong direction for a disk check:
			// the one case where you cannot tell how much room is left is not
			// the case to start writing gigabytes.
			return File{}, fmt.Errorf("%w: could not read free space: %v", ErrNoSpace, err)
		}
		// The floor has to survive the upload, not merely precede it. Checking
		// `free < minFreeBytes` alone accepts an 8 GiB upload onto a volume
		// with exactly the 2 GiB reserve free, writes until ENOSPC, and eats
		// the reserve the database and the recorder depend on -- which is the
		// entire thing the floor exists to protect.
		needed := minFreeBytes
		if maxBytes > 0 {
			needed += uint64(maxBytes)
		}
		if free < needed {
			return File{}, ErrNoSpace
		}
	}

	name := SafeName(hint)
	final, err := s.Resolve(name)
	if err != nil {
		return File{}, err
	}

	tmp, err := os.CreateTemp(s.dir, ".partial-*")
	if err != nil {
		return File{}, fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Both cleanups are unconditional and idempotent: on the success path the
	// rename has already moved the file, so the Remove is a no-op.
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	var written int64
	if maxBytes > 0 {
		// +1 so a body of exactly maxBytes+1 is detectable as too large rather
		// than being silently truncated to the limit.
		written, err = io.Copy(tmp, io.LimitReader(r, maxBytes+1))
	} else {
		written, err = io.Copy(tmp, r)
	}
	if err != nil {
		return File{}, fmt.Errorf("write upload: %w", err)
	}
	if maxBytes > 0 && written > maxBytes {
		return File{}, ErrTooLarge
	}
	if written == 0 {
		return File{}, ErrEmpty
	}
	if err := tmp.Sync(); err != nil {
		return File{}, fmt.Errorf("sync upload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return File{}, fmt.Errorf("close upload: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return File{}, fmt.Errorf("finalise upload: %w", err)
	}
	// 0600, not 0644. Nothing outside this process reads an upload: the server
	// serves it over the API and the FFmpeg children it spawns run as the same
	// user. os.CreateTemp already makes the file 0600, so the earlier 0644 was
	// actively WIDENING permissions on operator media for no reader that exists.
	if err := os.Chmod(final, 0o600); err != nil {
		return File{}, fmt.Errorf("chmod upload: %w", err)
	}

	return File{
		Name:     name,
		Origin:   OriginUploaded,
		Bytes:    written,
		Modified: time.Now().UTC(),
		PullURL:  PullURL(name),
	}, nil
}

// PullURL renders the file:// URL for a stored upload, relative to the data
// directory exactly as a pull source expects. Always forward slashes: this is
// a URL, and a backslash here would be a literal character in a filename on
// the platform that uses it as a separator.
func PullURL(name string) string { return "file://" + Dir + "/" + name }

// List returns the stored uploads, newest first.
func (s *Store) List() ([]File, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := make([]File, 0, len(entries))
	for _, e := range entries {
		// Skip directories and in-flight temp files: a partial upload is not
		// something to offer as a source.
		if e.IsDir() || strings.HasPrefix(e.Name(), ".partial-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, File{
			Name:     e.Name(),
			Origin:   OriginUploaded,
			Bytes:    info.Size(),
			Modified: info.ModTime().UTC(),
			PullURL:  PullURL(e.Name()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// Delete removes one stored upload.
func (s *Store) Delete(name string) error {
	full, err := s.Resolve(name)
	if err != nil {
		return err
	}
	return os.Remove(full)
}
