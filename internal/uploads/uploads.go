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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
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
	var buf [nameSuffixBytes]byte
	// crypto/rand rather than math/rand: this suffix is what makes the name
	// unguessable, so it should not come from a predictable sequence.
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("%s-%s%s", stem, hex.EncodeToString(buf[:]), ext)
}

// nameSuffixBytes is the random part of a stored name, and it is a COLLISION
// budget rather than a secrecy one.
//
// It was four bytes. Thirty-two bits with a caller-controlled stem is inside
// birthday range for a directory an operator fills over a year: 200,000 draws
// of SafeName("show.ts") produced four colliding names when it was measured.
// A collision is not a cosmetic problem here, because Commit renames onto the
// name -- so the loser's bytes become the winner's file while the winner's
// recorded verdict still describes the bytes that are gone, which is exactly
// the "tell two similar files apart" property this feature exists to provide,
// inverted.
//
// Eight bytes puts the same 200,000 draws at roughly one collision in ten
// billion runs. Commit ALSO refuses to overwrite (see its O_EXCL claim), so
// this is the first of two defences rather than the only one: entropy makes a
// collision not happen, and the claim makes one that happens anyway a loud
// failure rather than silent content substitution.
const nameSuffixBytes = 8

// allowedExt is what may be preserved from a client filename: containers
// polyemesis can plausibly read. Anything else is stored as ".bin".
//
// This used to end "and still works, because ffmpeg probes content rather than
// trusting the extension". Both halves of that were wrong by the time it was
// read, and the second half is what this whole feature exists to refute.
//
// "Still works" is no longer true in either direction. An unrecognised
// extension used to mean the file was stored, listed, and left for the operator
// to discover at air; POST /api/v1/media now probes the bytes and refuses what
// is not media, so a ".bin" that is a PDF gets a 400 and is never stored, and a
// ".bin" that IS media is accepted exactly as before. The extension decides the
// stored NAME and nothing about admission.
//
// "ffmpeg probes content rather than trusting the extension" is the sentence
// that let a PDF into the Library, and it is only mostly true even as a
// statement about ffmpeg. It was measured on this repo's FFmpeg that content
// probing alone decided the format for every shape tried (mp4, mkv, mpegts,
// mxf, nut, wtv, mpeg, and raw h264/mpegvideo/ac3/eac3), with and without the
// extension present -- but "mostly true about ffmpeg" is not a licence for this
// package to make a claim about admission, which it does not control. What
// admits an upload is internal/ffmpeg.ProbeFile's format allowlist and the
// handler's stream check. Not this map.
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
	//
	// IT IS PRESENT FOR AN UNVERIFIED FILE TOO, and that is issue #201. The
	// engine's own FFmpeg is not ffmpeg.ProbeFile and carries neither the format
	// allowlist nor -protocol_whitelist, so this is the one consumer of an
	// upload that still assumes stored implies checked. It takes a deliberate
	// copy-paste out of a Library row that says "Not checked", which is why it
	// was not the path the review's chain ran through -- but it is a path.
	PullURL string `json:"pullUrl"`
	// Media is what ffprobe said about this file when it was accepted, or nil
	// when there is nothing to say -- which now means the file was not
	// inspected, never "it was inspected and found to be nothing". The Library
	// shows it so an operator can tell two similar-looking files apart before
	// scheduling one.
	Media *MediaInfo `json:"media,omitempty"`
	// Verified reports whether these bytes were inspected and accepted as media.
	//
	// DELIBERATELY NOT omitempty, and that is the whole point of the field. A
	// stored upload is in one of three states -- inspected and accepted,
	// refused (in which case it is not here at all), and STORED WITHOUT BEING
	// INSPECTED. The third state is reachable on demand by a remote client: send
	// a complete body and drop the connection, and the probe is interrupted, and
	// an interrupted probe is not a verdict about the file, so the bytes are
	// kept. That is correct -- deleting a completed transfer because the
	// inspection of it was cut short is the data loss this path was reshaped to
	// stop -- but before this field the result was INDISTINGUISHABLE from a file
	// that had passed: `media` was simply absent, which is also how every upload
	// stored before probing existed looks.
	//
	// An absent boolean would have re-created that exactly, so it is always
	// present and always answered, and `false` is a statement rather than a gap.
	Verified bool `json:"verified"`
	// UnverifiedReason says WHY, in the operator's words, when Verified is
	// false. Empty for a file with no recorded verdict at all -- one stored
	// before verdicts existed, or placed in the directory by hand.
	UnverifiedReason string `json:"unverifiedReason,omitempty"`
}

// MediaInfo is the probe result as the Library needs it.
//
// Plain data, and deliberately not internal/ffmpeg's ProbeResult: this package
// stores bytes and knows nothing about encoders, and importing the ffmpeg
// package here to describe a file on disk would invert that. The caller probes
// and hands the answer down.
type MediaInfo struct {
	DurationSeconds float64 `json:"durationSeconds"`
	VideoCodec      string  `json:"videoCodec"`
	Width           int     `json:"width"`
	Height          int     `json:"height"`
	FrameRate       float64 `json:"frameRate"`
	// AudioTracks is the count, and it is the field this whole feature is for.
	// Per-destination audio routing is the product, so "does this file have the
	// three tracks I am about to route" is the question the Library could not
	// answer before.
	AudioTracks   int    `json:"audioTracks"`
	AudioCodec    string `json:"audioCodec"`
	AudioChannels int    `json:"audioChannels"`
	AudioLayout   string `json:"audioLayout"`
	// ProbedAt dates the reading. A file cannot change under us -- the stored
	// name is unguessable and nothing rewrites it -- but a probe from an older
	// FFmpeg may have seen less, and a reader deserves to know how old the
	// answer is.
	ProbedAt time.Time `json:"probedAt"`
}

// sidecarPrefix marks the JSON file holding one upload's MediaInfo.
//
// A prefix rather than a suffix, and the same shape as ".partial-", because
// List already skips that prefix and a reader of this file only has to learn
// the convention once. Storing it beside the upload rather than in the database
// keeps the two impossible to separate: deleting the directory takes both, and
// there is no schema to migrate for something that is a cache of a fact about
// a file.
const sidecarPrefix = ".probe-"

func sidecarName(stored string) string { return sidecarPrefix + stored + ".json" }

// Reasons a stored upload carries no inspection. Spelled once, because they are
// shown to an operator and matched in tests, and a second copy of a sentence is
// a second place for it to drift from the behaviour it describes.
const (
	// ReasonNoProber is the install that has nothing to inspect with.
	ReasonNoProber = "this server had no ffprobe available when the file arrived"
	// ReasonInterrupted is the client that went away, or a probe that ran out
	// of time. Both leave the same state and neither is a verdict about the
	// bytes.
	ReasonInterrupted = "the inspection was cut short before it finished"
	// ReasonProbeUnusable is a probe that could not be STARTED or whose output
	// could not be read -- a missing binary, a permission error, a fork that
	// failed, output that is not JSON. Every one of those is a fact about this
	// server, not about the operator's file.
	ReasonProbeUnusable = "this server could not run its media inspection"
	// ReasonNotInspected is the bare Store.Save path, which publishes without
	// looking. It has no production caller; the sentence exists so that a file
	// it wrote is still ANSWERED rather than silent.
	ReasonNotInspected = "this file was stored without being inspected"
)

// Verdict is the record that sits beside every upload this server publishes.
//
// IT IS A VERDICT, NOT A METADATA CACHE, and the difference is the entire fix.
// The sidecar used to hold a MediaInfo and nothing else, so "no sidecar" and
// "an upload nobody checked" were the same bytes on disk as "an upload from
// before sidecars existed". Three states were stored as two, and the one that
// went missing is the one a remote client can reach on demand.
//
// So the record always says which it is. Verified true carries Info; verified
// false carries Reason and no Info, because a file nobody read has no duration
// and no track count, and inventing zeroes for it is how a Library column comes
// to state a falsehood about a file.
//
// A record that cannot be read AT ALL -- absent, truncated, not JSON -- reads
// as unverified with an empty Reason. That is fail-closed in the only direction
// that is safe, and it is distinguishable from a recorded refusal to inspect:
// see Store.Verdict's second return.
type Verdict struct {
	Verified bool       `json:"verified"`
	Reason   string     `json:"reason,omitempty"`
	Info     *MediaInfo `json:"info,omitempty"`
}

// VerifiedVerdict is the record for bytes that were inspected and accepted.
func VerifiedVerdict(info MediaInfo) Verdict { return Verdict{Verified: true, Info: &info} }

// UnverifiedVerdict is the record for bytes that were stored without a verdict
// being reachable, and why.
func UnverifiedVerdict(reason string) Verdict { return Verdict{Reason: reason} }

// PutVerdict records what this server concluded about a stored upload.
//
// Best-effort at the call site by design where the file is already published:
// the upload is on disk and usable, and failing the request because a record
// could not be written would throw away a file the operator has just spent
// minutes sending. Pending.Commit writes it BEFORE it publishes, which is the
// ordering that makes the best-effort case rare rather than routine -- see
// there.
func (s *Store) PutVerdict(stored string, v Verdict) error {
	if strings.ContainsAny(stored, `/\`) || stored == "" {
		return fmt.Errorf("uploads: refusing a media record for %q", stored)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, sidecarName(stored)), b, 0o600)
}

// PutMedia records a PASSING inspection. Kept as the narrow spelling of the
// common case; PutVerdict is what can also record a failure to inspect.
func (s *Store) PutMedia(stored string, info MediaInfo) error {
	return s.PutVerdict(stored, VerifiedVerdict(info))
}

// Verdict returns the recorded conclusion about a stored upload, and whether
// there was a record at all.
//
// THE SECOND RETURN IS NOT A CONVENIENCE. "This server looked at these bytes,
// could not inspect them, and said so" is a different thing from "nobody ever
// wrote anything about this file", and a caller deciding whether to refuse an
// operator's playlist item has to be able to tell them apart: the first is a
// file this build published unchecked and can tell the operator to send again,
// the second is every upload an install made before verdicts existed, and
// refusing those would strand data the operator has had for a year over a
// record that was never written.
//
// Both are unverified. Only the first is EVIDENCE of being unverified.
func (s *Store) Verdict(stored string) (Verdict, bool) {
	b, err := os.ReadFile(filepath.Join(s.dir, sidecarName(stored)))
	if err != nil {
		return Verdict{}, false
	}
	var v Verdict
	if err := json.Unmarshal(b, &v); err != nil {
		// Unreadable is not "fine". A record this process cannot parse is a
		// record, and the safe reading of one is the unverified one.
		return Verdict{Reason: ReasonProbeUnusable}, true
	}
	if !v.Verified {
		v.Info = nil
	}
	return v, true
}

// removeVerdict drops the sidecar. Called on the paths that remove an upload,
// so a name reused by a later upload cannot inherit the previous file's
// numbers -- or, worse, the previous file's PASS.
func (s *Store) removeVerdict(stored string) {
	_ = os.Remove(filepath.Join(s.dir, sidecarName(stored)))
}

// Pending is an upload whose bytes are on disk and which is NOT yet visible.
//
// It exists so a caller can look at the bytes before deciding. Save used to be
// the whole story -- stream, rename, hand back a File -- and the rename is the
// moment the file becomes real: List returns it, its pullUrl works, and PUT
// /api/v1/settings will accept it as a playlist item. A caller that renames
// first and inspects second has, for the length of the inspection, published a
// file it is about to delete. That gap is not theoretical; it was driven to
// 201-then-deleted with a stored playlist item left naming nothing, which is
// the dangling reference handleDeleteMedia takes settingsMu to prevent and
// answers 409 for.
//
// So the sequence is Stage, look, then Commit or Discard. Exactly one of the
// last two must be called; Discard after Commit is a no-op, which makes
// `defer p.Discard()` safe to write next to the Stage.
type Pending struct {
	store *Store
	tmp   string
	name  string
	bytes int64
	done  bool
}

// Path is the absolute path of the staged bytes, for a caller that wants to
// read them before deciding. It is inside the uploads directory and carries the
// ".partial-" prefix List skips, so nothing lists it and no pullUrl reaches it.
func (p *Pending) Path() string { return p.tmp }

// Name is the filename the upload will have if it is committed.
func (p *Pending) Name() string { return p.name }

// Bytes is what was actually written.
func (p *Pending) Bytes() int64 { return p.bytes }

// renameOverClaim renames the staged file onto the name claimed above it,
// retrying briefly while something else holds a handle to the target.
//
// WHY THIS IS NOT A PLAIN os.Rename. On Unix a rename over an open file is
// fine -- the name is rebound and the old inode lives until its last handle
// closes. On Windows it is not: the target is a real file (the O_EXCL claim a
// few lines above created it), and MoveFileEx fails with "Access is denied"
// while ANY handle to it is open, including a read-only one.
//
// Measured, twice in one hour on CI, from a test whose own observer goroutine
// polled os.Stat on the target in a tight loop:
//
//	finalise upload: rename ...\.partial-790352203.mkv ...\show-c37688205ca09aa2.mkv:
//	Access is denied.
//
// The test made it reproducible; it did not invent it. Production has the same
// window and worse company in it -- Windows Defender scans a newly created file,
// the Search indexer opens it, a backup agent reads it, an operator has the
// folder open in Explorer. Any of them holding the claim file for a few
// milliseconds turned a completed multi-gigabyte upload into a 500 and threw
// the bytes away.
//
// A few tries over a short window, because the holders above are transient by
// nature. It is deliberately not a long loop: a rename that fails because the
// path is wrong must still reach the caller quickly, and only a rename that
// fails because someone else is *reading* deserves patience. There is no way to
// distinguish those two from the error alone on Windows, so the bound is what
// keeps the wrong one cheap.
//
// On Unix the first attempt succeeds and this costs one function call.
func renameOverClaim(from, to string) error {
	const attempts = 20
	var err error
	for i := 0; i < attempts; i++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}

// Commit publishes the staged file under its final name, with the verdict this
// server reached about its bytes.
//
// THE VERDICT IS WRITTEN FIRST, and the order is the fix rather than a tidy-up.
// It used to be published by the caller AFTER the rename, which left a window
// -- short, but a window -- in which a file was listed, had a working pullUrl
// and was a legal playlist item while nothing on disk said whether anyone had
// read it. That window is small enough to be dismissed and it is exactly the
// wrong thing to dismiss, because it means "no record" can never be relied on
// to mean anything: a reader finding one has no way to tell a file nobody
// checked from a file whose record has not landed yet. Writing the record while
// the bytes are still under ".partial-" -- which List skips and
// playlistUploadProblems refuses -- costs one fsync-less write and makes the
// rename the single moment the file becomes real, verdict and all.
//
// The verdict is a REQUIRED argument for the same reason. A caller that could
// forget it would re-create the state this whole change exists to remove.
func (p *Pending) Commit(v Verdict) (File, error) {
	if p.done {
		return File{}, errors.New("uploads: this upload was already committed or discarded")
	}
	final, err := p.store.Resolve(p.name)
	if err != nil {
		return File{}, err
	}
	// CLAIM THE NAME FIRST, THEN WRITE THE VERDICT, THEN RENAME, and the order
	// of the first two is not interchangeable. Writing the verdict first means a
	// Commit that then LOSES the name has already overwritten the winner's
	// record with its own -- so the surviving file would carry a description of
	// the bytes that were refused. Measured: the loser's cleanup removed the
	// winner's sidecar outright.
	//
	// os.Rename replaces silently on every platform this runs on, so two
	// uploads that drew the same stored name would leave one operator's bytes
	// under the other's name -- with the loser's verdict already written and now
	// describing bytes that are gone. SafeName's suffix makes that astronomically
	// unlikely (see nameSuffixBytes) and this makes it impossible rather than
	// unlikely: O_EXCL is the atomic reservation, so the second Commit fails
	// loudly instead of overwriting.
	//
	// The claim is a zero-byte file under the final name, so it is briefly
	// listable. That is one os.Rename wide, and it is strictly the better of the
	// two states to be caught in: an operator who refreshes at exactly the wrong
	// moment sees an obviously-empty row, rather than a full-looking one that is
	// somebody else's file. Closing it properly needs either a second rename
	// through a non-Listable name or renameat2(RENAME_NOREPLACE) with this as
	// the portable fallback; issue #203.
	claim, err := os.OpenFile(final, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return File{}, fmt.Errorf("finalise upload: %q already exists", p.name)
		}
		return File{}, fmt.Errorf("finalise upload: %w", err)
	}
	_ = claim.Close()
	if err := p.store.PutVerdict(p.name, v); err != nil {
		_ = os.Remove(final)
		return File{}, fmt.Errorf("record what is known about this upload: %w", err)
	}
	if err := renameOverClaim(p.tmp, final); err != nil {
		_ = os.Remove(final)
		p.store.removeVerdict(p.name)
		return File{}, fmt.Errorf("finalise upload: %w", err)
	}
	p.done = true
	// 0600, not 0644. Nothing outside this process reads an upload: the server
	// serves it over the API and the FFmpeg children it spawns run as the same
	// user. os.CreateTemp already makes the file 0600, so the earlier 0644 was
	// actively WIDENING permissions on operator media for no reader that exists.
	//
	// A failure here UNDOES the rename rather than leaving the file. It is the
	// one window where the file is published and the caller is about to be told
	// the upload failed, and a listable file nobody was told about is the state
	// this whole path was reshaped to make impossible. Save's old shape leaked
	// exactly this: its cleanup was a Remove of the temp name, which the rename
	// had already emptied.
	if err := os.Chmod(final, 0o600); err != nil {
		_ = os.Remove(final)
		p.store.removeVerdict(p.name)
		return File{}, fmt.Errorf("chmod upload: %w", err)
	}
	return File{
		Name:             p.name,
		Origin:           OriginUploaded,
		Bytes:            p.bytes,
		Modified:         time.Now().UTC(),
		PullURL:          PullURL(p.name),
		Media:            v.Info,
		Verified:         v.Verified,
		UnverifiedReason: v.Reason,
	}, nil
}

// Discard removes the staged bytes. Idempotent, and a no-op after Commit.
//
// There is no sidecar to remove and no reference to check: a Pending was never
// published, so nothing in the product has had the chance to name it. That is
// the entire reason the reject path moved here from store.Delete, which had to
// be trusted not to race a settings save and could not be.
func (p *Pending) Discard() error {
	if p.done {
		return nil
	}
	p.done = true
	if err := os.Remove(p.tmp); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Save streams r into the uploads directory and returns the stored file.
//
// Stage plus Commit with nothing in between, kept because that is genuinely
// all some callers want. A caller that must inspect the bytes before the file
// becomes listable wants Stage.
//
// It records the file as UNINSPECTED, because that is what it is. Save looks at
// nothing, so a Save that recorded a pass would be this package asserting a
// fact it did not establish. There is no production caller today -- the upload
// handler stages, probes and commits its own verdict -- and this is what a
// future one would get by default, which is the safe default to have.
func (s *Store) Save(r io.Reader, hint string, maxBytes int64, minFreeBytes uint64) (File, error) {
	p, err := s.Stage(r, hint, maxBytes, minFreeBytes)
	if err != nil {
		return File{}, err
	}
	defer p.Discard()
	return p.Commit(UnverifiedVerdict(ReasonNotInspected))
}

// Stage streams r into a temp file in the uploads directory without publishing
// it, and returns the Pending the caller must Commit or Discard.
//
// It writes to a temporary file and renames on success, so a cancelled or
// oversized upload never leaves a partial file that looks selectable. That
// matters more here than in most upload paths: a half-written video is not
// obviously broken in a listing, and the operator would discover it when the
// broadcast they scheduled goes to air.
func (s *Store) Stage(r io.Reader, hint string, maxBytes int64, minFreeBytes uint64) (*Pending, error) {
	if minFreeBytes > 0 && s.freeBytes != nil {
		free, err := s.freeBytes(s.dir)
		if err != nil {
			// FAIL CLOSED. An earlier version skipped the guard when the check
			// itself errored, which is the wrong direction for a disk check:
			// the one case where you cannot tell how much room is left is not
			// the case to start writing gigabytes.
			return nil, fmt.Errorf("%w: could not read free space: %v", ErrNoSpace, err)
		}
		// The floor has to survive the upload, not merely precede it. Checking
		// `free < minFreeBytes` alone accepts an 8 GiB upload onto a volume
		// with exactly the 2 GiB reserve free, writes until ENOSPC, and eats
		// the reserve the database and the recorder depend on -- which is the
		// entire thing the floor exists to protect.
		//
		// STILL TOCTOU ACROSS CONCURRENT UPLOADS, and that is issue #200 rather
		// than an oversight here. Every request reads the same number and
		// nothing subtracts what the others have already promised to write:
		// eight concurrent Stages were measured admitted against a volume
		// reported as having room for one. Closing it needs a process-wide
		// reservation, because api.Server.uploadStore builds a NEW Store per
		// request by design, so a lock on this struct would bound nothing. The
		// issue records why it was safe to leave and what would change that.
		needed := minFreeBytes
		if maxBytes > 0 {
			needed += uint64(maxBytes)
		}
		if free < needed {
			return nil, ErrNoSpace
		}
	}

	name := SafeName(hint)
	if _, err := s.Resolve(name); err != nil {
		return nil, err
	}

	// The temp file keeps the final name's extension. Content probing decided
	// the format for every shape this was measured against -- mp4, mkv, mpegts,
	// mxf, nut, wtv, mpeg, and the raw h264/mpegvideo/ac3/eac3 elementary
	// streams all reported the same format_name under ".partial-1234567" as
	// under their real name -- so this is not load-bearing. It is here so that
	// the file the gate inspects and the file the operator gets differ in
	// nothing an ffmpeg looks at, which is a cheaper thing to keep true than to
	// re-measure whenever the allowlist grows.
	tmp, err := os.CreateTemp(s.dir, partialPrefix+"*"+filepath.Ext(name))
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Every early return below leaves nothing behind. Past this function the
	// Pending owns the file and Commit/Discard decide its fate.
	fail := func(err error) (*Pending, error) {
		tmp.Close()
		os.Remove(tmpName)
		return nil, err
	}

	var written int64
	if maxBytes > 0 {
		// +1 so a body of exactly maxBytes+1 is detectable as too large rather
		// than being silently truncated to the limit.
		written, err = io.Copy(tmp, io.LimitReader(r, maxBytes+1))
	} else {
		written, err = io.Copy(tmp, r)
	}
	if err != nil {
		// ENOSPC IS THE SAME ANSWER WHETHER IT IS PREDICTED OR DISCOVERED.
		//
		// The pre-check above returns ErrNoSpace, which the handler turns into
		// 507 Insufficient Storage. Running out DURING the write took the
		// default arm instead -- 500, with the message being the raw os.PathError
		// and therefore the absolute data-directory path and the internal
		// ".partial-" temp name in the response body. Two defects from one
		// missing classification: the wrong status for a condition the code
		// already knows how to describe, and a server path handed to whoever
		// filled the disk. The pre-check is a guard against the common case, not
		// a proof, so this arm is reachable whenever anything else is writing to
		// the volume.
		if errors.Is(err, syscall.ENOSPC) {
			return fail(ErrNoSpace)
		}
		return fail(fmt.Errorf("write upload: %w", err))
	}
	if maxBytes > 0 && written > maxBytes {
		return fail(ErrTooLarge)
	}
	if written == 0 {
		return fail(ErrEmpty)
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("sync upload: %w", err))
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return nil, fmt.Errorf("close upload: %w", err)
	}

	return &Pending{store: s, tmp: tmpName, name: name, bytes: written}, nil
}

// PullURL renders the file:// URL for a stored upload, relative to the data
// directory exactly as a pull source expects. Always forward slashes: this is
// a URL, and a backslash here would be a literal character in a filename on
// the platform that uses it as a separator.
func PullURL(name string) string { return "file://" + Dir + "/" + name }

// partialPrefix marks bytes that are on disk but not published: a Pending, or
// the leavings of a process that died mid-upload.
const partialPrefix = ".partial-"

// Listable reports whether a stored filename is one this package will ever
// offer as an upload.
//
// EXPORTED SO THE RULE HAS ONE HOME. Two things ask this question and they must
// not be allowed to answer it differently: List, which decides what the Library
// shows, and internal/api's playlist validation, which decides what a stored
// playlist item may name. That second one used to ask os.Stat instead, which
// answers a different question -- "are there bytes at this path" -- and every
// name skipped here has bytes at its path. So a settings save naming a
// ".partial-" file staged by an upload in flight, or a ".probe-" sidecar,
// passed validation and became a playlist item pointing at something that was
// about to be deleted or was never media in the first place.
func Listable(name string) bool {
	return name != "" &&
		!strings.HasPrefix(name, partialPrefix) &&
		!strings.HasPrefix(name, sidecarPrefix)
}

// List returns the stored uploads, newest first.
func (s *Store) List() ([]File, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := make([]File, 0, len(entries))
	for _, e := range entries {
		// Skip directories and in-flight temp files: a partial upload is not
		// something to offer as a source. Sidecars go with them -- a probe
		// result is not itself media, and listing one would offer it as a
		// pull source.
		if e.IsDir() || !Listable(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// The recorded verdict, not a metadata lookup. A file with no record
		// reads as unverified with no reason, which is the honest description
		// of an upload stored before verdicts existed or placed here by hand.
		v, _ := s.Verdict(e.Name())
		out = append(out, File{
			Name:     e.Name(),
			Origin:   OriginUploaded,
			Bytes:    info.Size(),
			Modified: info.ModTime().UTC(),
			PullURL:  PullURL(e.Name()),
			// Absent unless the file was inspected AND passed, which is why the
			// field is a pointer and the UI has to cope with nil rather than
			// being handed zeroes it would render as "0x0".
			Media:            v.Info,
			Verified:         v.Verified,
			UnverifiedReason: v.Reason,
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
	// The sidecar goes with it. Stored names carry a random suffix so a reuse
	// is vanishingly unlikely, but a stale probe surviving its file would be a
	// listing that describes something that is gone.
	s.removeVerdict(name)
	return os.Remove(full)
}
