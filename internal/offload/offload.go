// Package offload copies finished recording segments (and optionally their
// stems, transcripts and clips) to S3-compatible storage targets, verifies
// every upload, and in move mode deletes the local copy only after that
// verification -- fetching it back on demand when the clipper needs it.
//
// THE ONE INVARIANT this package exists to keep: a local file is never
// deleted unless every enabled target holds a verified copy of exactly those
// bytes. "Verified" means the service recomputed both digests on receipt (the
// SigV4 payload hash and Content-MD5 are signed into the PUT), a HEAD after
// the upload reported the same size, the same MD5 ETag where the target uses
// one, and the same SHA-256 metadata; with VerifyDownload the object is also
// read back and re-hashed. Eviction then re-checks the remote object and the
// local file one last time before the unlink.
//
// Never the file being written: a recording segment is a candidate only once
// it has been ffprobed (DurationMS > 0), is outside recording.LiveSet (the
// same inference retention protects with), and has not been modified for the
// settle window. Extras follow their master or the same settle rule.
package offload

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/clips"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/netguard"
	"github.com/rainmanjam/polyemesis/internal/objstore"
	"github.com/rainmanjam/polyemesis/internal/recording"
	"github.com/rainmanjam/polyemesis/internal/secrets"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// Client is the slice of objstore.Client offload uses; tests substitute a
// store that lies about what it holds.
type Client interface {
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64, sha256Hex, md5B64 string, meta map[string]string) (string, error)
	Head(ctx context.Context, key string) (objstore.ObjectInfo, error)
	Get(ctx context.Context, key string) (io.ReadCloser, objstore.ObjectInfo, error)
	GetFrom(ctx context.Context, key string, offset int64) (io.ReadCloser, objstore.ObjectInfo, bool, error)
	Delete(ctx context.Context, key string) error
	CreateMultipart(ctx context.Context, key string, meta map[string]string) (string, error)
	UploadPart(ctx context.Context, key, uploadID string, n int, body io.ReadSeeker, size int64, sha256Hex, md5B64 string) (string, error)
	CompleteMultipart(ctx context.Context, key, uploadID string, parts []objstore.Part) (string, error)
	AbortMultipart(ctx context.Context, key, uploadID string) error
}

// Recordings is what offload needs from recording.Manager.
type Recordings interface {
	Dir() string
	Resolve(name string) (string, error)
}

const (
	// SettleWindow is how long a file must sit unmodified before it is a
	// candidate. It is a second guard on top of LiveSet, not a replacement.
	SettleWindow = 2 * time.Minute
	// FetchPin keeps a fetched-back recording local this long after the
	// fetch, so move mode does not evict it out from under the clipper.
	FetchPin = db.OffloadFetchPin
	// backoffBase and backoffCap bound the retry schedule: 30s, 1m, 2m, ...
	// capped at an hour, with jitter so a fleet of failures does not retry in
	// lockstep.
	backoffBase = 30 * time.Second
	backoffCap  = time.Hour
	tickEvery   = 30 * time.Second
	// maxErrLen keeps a stored error short; it is shown in the Library.
	maxErrLen = 300
)

// Service runs offload.
type Service struct {
	log      *slog.Logger
	store    *db.DB
	box      *secrets.Box
	rec      Recordings
	onChange func()

	now       func() time.Time
	newClient func(t db.StorageTarget) (Client, error)
	segSecs   func() int

	passMu sync.Mutex
	again  atomic.Bool

	stuckMu sync.Mutex
	onStuck func(StuckEvent)

	// pins are files something is reading right now (a download, a clip
	// export, a queued or running job): eviction leaves them alone.
	pinMu sync.Mutex
	pins  map[string]int
	busy  func(name string) bool

	// Multipart: files above mpThreshold go up in parts of partSize, each
	// part tried partTries times with backoff starting at partBackoff.
	mpThreshold int64
	partSize    int64
	partTries   int
	partBackoff time.Duration
	// fetchTries and fetchBackoff are fetch-back's attempts, resumed by Range.
	fetchTries   int
	fetchBackoff time.Duration

	fetchMu  sync.Mutex
	fetches  map[string]*FetchState
	fetchSem chan struct{}
	baseCtx  context.Context
}

// Option configures a Service.
type Option func(*Service)

// WithClientFactory replaces the S3 client constructor (tests).
func WithClientFactory(fn func(t db.StorageTarget) (Client, error)) Option {
	return func(s *Service) { s.newClient = fn }
}

// Multipart defaults. 64 MiB is where a single PUT's all-or-nothing retry
// starts to cost real bandwidth; 16 MiB parts keep a resume cheap and stay
// above every service's 5 MiB minimum part size.
const (
	DefaultMultipartThreshold int64 = 64 << 20
	DefaultPartSize           int64 = 16 << 20
)

// WithMultipart sets the multipart threshold and part size (tests use small
// ones against the fake; the live S3 suite uses 5 MiB parts, the S3 minimum).
func WithMultipart(threshold, partSize int64) Option {
	return func(s *Service) { s.mpThreshold, s.partSize = threshold, partSize }
}

// WithRetryPacing sets part and fetch-back retry backoff (tests: near zero).
func WithRetryPacing(d time.Duration) Option {
	return func(s *Service) { s.partBackoff, s.fetchBackoff = d, d }
}

// WithClock replaces time.Now (tests).
func WithClock(fn func() time.Time) Option { return func(s *Service) { s.now = fn } }

// New builds a Service. onChange is called whenever an item's state moves,
// so the Library refreshes over the recordings event.
func New(log *slog.Logger, store *db.DB, box *secrets.Box, rec Recordings, onChange func(), opts ...Option) *Service {
	s := &Service{log: log, store: store, box: box, rec: rec, onChange: onChange, now: time.Now,
		newClient: NewClient, fetches: map[string]*FetchState{}, baseCtx: context.Background(),
		mpThreshold: DefaultMultipartThreshold, partSize: DefaultPartSize, partTries: 3, partBackoff: time.Second,
		fetchTries: 3, fetchBackoff: time.Second}
	s.segSecs = func() int {
		st, err := store.GetSettings()
		if err != nil {
			return 0
		}
		return st.Recording.SegmentSeconds
	}
	for _, o := range opts {
		o(s)
	}
	if s.onChange == nil {
		s.onChange = func() {}
	}
	return s
}

// NewClient builds the production client for a target: SigV4 over a
// transport whose dialer is the netguard SSRF guard, so an operator-typed
// endpoint cannot reach a private address unless the target opted in.
func NewClient(t db.StorageTarget) (Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = netguard.DialContext
	tr.Proxy = nil
	c, err := objstore.New(objstore.Config{
		Endpoint: t.Endpoint, Bucket: t.Bucket, Region: t.Region, PathStyle: t.PathStyle,
		AccessKeyID: t.AccessKeyID, SecretAccessKey: t.SecretAccessKey,
		HTTPClient: &http.Client{Transport: tr, Timeout: 30 * time.Minute,
			// A redirect would re-sign nothing and could point anywhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	})
	if err != nil {
		return nil, err
	}
	return guarded{c: c, allow: t.AllowPrivateTarget}, nil
}

// guarded threads the per-target private-address opt-in onto every request's
// context, which is the only thing netguard.DialContext can see.
type guarded struct {
	c     *objstore.Client
	allow bool
}

func (g guarded) ctx(ctx context.Context) context.Context {
	return netguard.WithAllowPrivateTarget(ctx, g.allow)
}
func (g guarded) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, sh, mb string, meta map[string]string) (string, error) {
	return g.c.Put(g.ctx(ctx), key, body, size, sh, mb, meta)
}
func (g guarded) Head(ctx context.Context, key string) (objstore.ObjectInfo, error) {
	return g.c.Head(g.ctx(ctx), key)
}
func (g guarded) Get(ctx context.Context, key string) (io.ReadCloser, objstore.ObjectInfo, error) {
	return g.c.Get(g.ctx(ctx), key)
}
func (g guarded) Delete(ctx context.Context, key string) error { return g.c.Delete(g.ctx(ctx), key) }
func (g guarded) GetFrom(ctx context.Context, key string, off int64) (io.ReadCloser, objstore.ObjectInfo, bool, error) {
	return g.c.GetFrom(g.ctx(ctx), key, off)
}
func (g guarded) CreateMultipart(ctx context.Context, key string, meta map[string]string) (string, error) {
	return g.c.CreateMultipart(g.ctx(ctx), key, meta)
}
func (g guarded) UploadPart(ctx context.Context, key, id string, n int, body io.ReadSeeker, size int64, sh, mb string) (string, error) {
	return g.c.UploadPart(g.ctx(ctx), key, id, n, body, size, sh, mb)
}
func (g guarded) CompleteMultipart(ctx context.Context, key, id string, parts []objstore.Part) (string, error) {
	return g.c.CompleteMultipart(g.ctx(ctx), key, id, parts)
}
func (g guarded) AbortMultipart(ctx context.Context, key, id string) error {
	return g.c.AbortMultipart(g.ctx(ctx), key, id)
}

// Run ticks until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.fetchMu.Lock()
	s.baseCtx = ctx
	s.fetchMu.Unlock()
	s.Recover(ctx)
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		s.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// candidate is one finished file eligible for upload.
type candidate struct {
	rel    string
	kind   string
	at     time.Time
	source *int64
	path   string
	size   int64
	mtime  time.Time
	// rec is the index row for a recording master.
	rec *db.Recording
}

// usable reports whether a target can upload right now.
func usable(t db.StorageTarget) bool {
	return t.Enabled && t.HasSecret && !t.SecretUnreadable
}

// Pass runs one discover/upload/evict/prune cycle. Exported for tests and for
// the API to kick after a target is saved.
func (s *Service) Pass(ctx context.Context) {
	if !s.passMu.TryLock() {
		// A pass is already running. The ask is not dropped: the running
		// pass goes round once more when it finishes, so a target saved or a
		// Retry pressed mid-pass is acted on without waiting for the tick.
		s.again.Store(true)
		return
	}
	defer s.passMu.Unlock()
	for {
		s.again.Store(false)
		s.passOnce(ctx)
		if ctx.Err() != nil || !s.again.Load() {
			return
		}
	}
}

// Kick asks for a pass soon, on the service's own context, without blocking.
func (s *Service) Kick() {
	s.fetchMu.Lock()
	ctx := s.baseCtx
	s.fetchMu.Unlock()
	go s.Pass(ctx)
}

func (s *Service) passOnce(ctx context.Context) {
	targets, err := s.store.ListStorageTargets(s.box)
	if err != nil {
		s.log.Warn("offload: list targets", "err", err)
		return
	}
	var active []db.StorageTarget
	for _, t := range targets {
		if usable(t) {
			active = append(active, t)
		}
	}
	items, err := s.store.ListOffloadItems()
	if err != nil {
		s.log.Warn("offload: list items", "err", err)
		return
	}
	byKey := map[string]db.OffloadItem{}
	for _, it := range items {
		byKey[itemKey(it.TargetID, it.RelPath)] = it
	}
	cands, err := s.candidates()
	if err != nil {
		s.log.Warn("offload: scan candidates", "err", err)
		return
	}
	changed := s.prune(items, cands)
	// A file that is on disk and marked evicted is an eviction interrupted
	// between the mark and the unlink: the file is still here, so the mark
	// is wrong. Clear it; the next eviction goes through the gate again.
	for _, cand := range cands {
		if cand.kind != db.OffloadKindRecording {
			continue
		}
		for _, t := range targets {
			if it, ok := byKey[itemKey(t.ID, cand.rel)]; ok && it.Evicted {
				if s.store.UndoEvict(cand.rel) == nil {
					for _, tt := range targets {
						if x, ok := byKey[itemKey(tt.ID, cand.rel)]; ok {
							x.Evicted = false
							byKey[itemKey(tt.ID, cand.rel)] = x
						}
					}
					changed = true
				}
				break
			}
		}
	}

	clients := map[int64]Client{}
	for _, t := range active {
		c, err := s.newClient(t)
		if err != nil {
			s.log.Warn("offload: target unusable", "target", t.Name, "err", err)
			continue
		}
		clients[t.ID] = c
	}
	for _, t := range active {
		c := clients[t.ID]
		if c == nil {
			continue
		}
		// A bad template is the target's fault and stops only that target;
		// a key one file cannot have is that file's fault and stops only it.
		if err := ValidatePrefixTemplate(t.PrefixTemplate); err != nil {
			s.log.Warn("offload: prefix template; skipping target", "target", t.Name, "err", err)
			continue
		}
		for _, cand := range cands {
			if ctx.Err() != nil {
				return
			}
			if !wants(t, cand.kind) {
				continue
			}
			it, ok := byKey[itemKey(t.ID, cand.rel)]
			if ok && it.Evicted {
				continue
			}
			if ok && it.State == db.OffloadVerified {
				if it.Bytes == cand.size && !cand.mtime.After(it.VerifiedAt) {
					continue
				}
				// The file changed after it was verified: the remote copy is
				// no longer these bytes, so it is uploaded again.
				it.State = db.OffloadPending
				it.Attempts = 0
				it.NextAttemptAt = time.Time{}
			}
			if ok && it.State == db.OffloadStuck {
				// Stuck stays stuck until Retry now: retrying a refused
				// credential every hour is noise, not progress.
				continue
			}
			if ok && it.State == db.OffloadFailed && s.now().Before(it.NextAttemptAt) {
				continue
			}
			if !ok || strings.HasPrefix(it.Key, unkeyedPrefix) {
				if !ok {
					it = db.OffloadItem{TargetID: t.ID, RelPath: cand.rel, Kind: cand.kind, State: db.OffloadPending}
				}
				key, kerr := ObjectKey(t.PrefixTemplate, cand.kind, cand.source, cand.at, cand.rel)
				if kerr != nil {
					byKey[itemKey(t.ID, cand.rel)] = s.unkeyed(t, it, kerr)
					changed = true
					continue
				}
				it.Key = key
				// One object, one file. A key another file already owns on
				// this target is never written: in move mode that object may
				// be the only copy of the other file.
				if holder, herr := s.store.OffloadKeyHolder(t.ID, key); herr != nil || (holder != "" && holder != cand.rel) {
					byKey[itemKey(t.ID, cand.rel)] = s.keyConflict(t, it, holder, herr)
					changed = true
					continue
				}
			}
			byKey[itemKey(t.ID, cand.rel)] = s.upload(ctx, t, c, it, cand)
			changed = true
		}
	}
	if s.evict(ctx, targets, clients, cands, byKey) {
		changed = true
	}
	if changed {
		s.onChange()
	}
}

// unkeyedPrefix marks the stored key of an item that has no usable object
// key: its rendered key belongs to another file, or the file's path cannot be
// a key at all. It is unique per file (the rel_path follows it), so the
// refusal can be stored and shown without taking anyone's key; the key is
// rendered and checked again on every attempt.
const unkeyedPrefix = "unkeyed:"

func (s *Service) keyConflict(t db.StorageTarget, it db.OffloadItem, holder string, herr error) db.OffloadItem {
	contested := it.Key
	if herr == nil {
		herr = fmt.Errorf("object key %s is already held by %s on this target; change the prefix template", contested, holder)
	}
	s.log.Warn("offload: object key conflict; not uploading", "target", t.Name, "file", it.RelPath, "holder", holder)
	return s.unkeyed(t, it, herr)
}

// unkeyed records an item that cannot be uploaded under any key right now.
func (s *Service) unkeyed(t db.StorageTarget, it db.OffloadItem, why error) db.OffloadItem {
	it.Key = unkeyedPrefix + it.RelPath
	it.Attempts++
	s.log.Warn("offload: file has no usable object key", "target", t.Name, "file", it.RelPath, "err", clipErr(why))
	// No key is a configuration fault: waiting will not give the file one.
	it = s.fail(t, it, why, true)
	if err := s.store.PutOffloadItem(it); err != nil {
		s.log.Warn("offload: persist item", "file", it.RelPath, "err", err)
	}
	return it
}

func itemKey(target int64, rel string) string { return strconv.FormatInt(target, 10) + "\x00" + rel }

func wants(t db.StorageTarget, kind string) bool {
	switch kind {
	case db.OffloadKindRecording:
		return true
	case db.OffloadKindStem:
		return t.OffloadStems
	case db.OffloadKindTranscript:
		return t.OffloadTranscripts
	case db.OffloadKindClip:
		return t.OffloadClips
	}
	return false
}

// candidates lists every finished file.
func (s *Service) candidates() ([]candidate, error) {
	recs, err := s.store.ListRecordings()
	if err != nil {
		return nil, err
	}
	now := s.now()
	live := recording.LiveSet(recs, s.segSecs(), now)
	settled := now.Add(-SettleWindow)
	var out []candidate
	finishedMasters := map[string]*db.Recording{}
	for i := range recs {
		r := &recs[i]
		if live[r.Filename] || r.DurationMS <= 0 {
			continue
		}
		path, err := s.rec.Resolve(r.Filename)
		if err != nil {
			continue
		}
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() || fi.ModTime().After(settled) {
			continue
		}
		finishedMasters[strings.TrimSuffix(r.Filename, filepath.Ext(r.Filename))] = r
		out = append(out, candidate{rel: r.Filename, kind: db.OffloadKindRecording, at: r.StartedAt,
			source: r.SourceID, path: path, size: fi.Size(), mtime: fi.ModTime(), rec: r})
	}
	// A master move mode took off the disk is still a finished master: its
	// stems are uploaded too (to a target added since, or after a retry).
	if evicted, err := s.store.EvictedRecordings(); err == nil {
		for i := range recs {
			if evicted[recs[i].Filename] {
				finishedMasters[strings.TrimSuffix(recs[i].Filename, filepath.Ext(recs[i].Filename))] = &recs[i]
			}
		}
	}
	dir := s.rec.Dir()
	add := func(sub, kind string, keep func(name string) (*db.Recording, bool)) {
		entries, err := os.ReadDir(filepath.Join(dir, sub))
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			master, ok := keep(e.Name())
			if !ok {
				continue
			}
			fi, err := e.Info()
			if err != nil || fi.ModTime().After(settled) {
				continue
			}
			c := candidate{rel: sub + "/" + e.Name(), kind: kind, at: fi.ModTime(),
				path: filepath.Join(dir, sub, e.Name()), size: fi.Size(), mtime: fi.ModTime()}
			if master != nil {
				c.at, c.source = master.StartedAt, master.SourceID
			}
			out = append(out, c)
		}
	}
	add(recording.StemsSubdir, db.OffloadKindStem, func(name string) (*db.Recording, bool) {
		base, ok := recording.StemMasterBase(name)
		if !ok {
			return nil, false
		}
		m, ok := finishedMasters[base]
		return m, ok
	})
	anySettled := func(string) (*db.Recording, bool) { return nil, true }
	add(transcribe.TranscriptsSubdir, db.OffloadKindTranscript, anySettled)
	add(clips.Subdir, db.OffloadKindClip, anySettled)
	add(clipper.ExportSubdir, db.OffloadKindClip, anySettled)
	sort.Slice(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out, nil
}

// prune drops item rows for files that exist nowhere any more: not on disk
// and not evicted. The remote object is left alone -- mirror mode keeps local
// retention unchanged, and the bucket's own lifecycle rule owns the copy.
func (s *Service) prune(items []db.OffloadItem, cands []candidate) bool {
	seen := map[string]bool{}
	changed := false
	for _, it := range items {
		if it.Evicted || seen[it.RelPath] {
			continue
		}
		seen[it.RelPath] = true
		if _, err := os.Stat(filepath.Join(s.rec.Dir(), filepath.FromSlash(it.RelPath))); errors.Is(err, os.ErrNotExist) {
			if err := s.store.DeleteOffloadItemsFor(it.RelPath); err == nil {
				changed = true
			}
		}
	}
	return changed
}

// hashFile returns the hex SHA-256 and base64 MD5 of f's first size bytes.
func hashFile(f *os.File) (string, string, int64, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", "", 0, err
	}
	sh, mh := sha256.New(), md5.New()
	n, err := io.Copy(io.MultiWriter(sh, mh), f)
	if err != nil {
		return "", "", 0, err
	}
	return hex.EncodeToString(sh.Sum(nil)), base64.StdEncoding.EncodeToString(mh.Sum(nil)), n, nil
}

// ErrChecksumMismatch is a verification that found different bytes than were
// sent. It is never treated as success, and an item carrying it is never
// evicted.
var ErrChecksumMismatch = errors.New("checksum mismatch after upload")

var hexMD5 = regexp.MustCompile(`^[0-9a-f]{32}$`)

// upload sends one file and verifies it. It returns the item as persisted.
func (s *Service) upload(ctx context.Context, t db.StorageTarget, c Client, it db.OffloadItem, cand candidate) db.OffloadItem {
	err := func() error {
		f, err := os.Open(cand.path)
		if err != nil {
			return err
		}
		defer f.Close()
		before, err := f.Stat()
		if err != nil {
			return err
		}
		sh, mb, n, err := hashFile(f)
		if err != nil {
			return err
		}
		if n != before.Size() {
			return errors.New("file changed while it was being hashed")
		}
		it.Bytes, it.SHA256 = n, sh
		it.State = db.OffloadUploading
		// The attempt count is the row's as it is NOW: a Retry now pressed
		// after this pass read the items reset it, and this pass's copy is stale.
		if cur, cerr := s.store.OffloadItemsFor(it.RelPath); cerr == nil {
			for _, row := range cur {
				if row.TargetID == it.TargetID && row.Attempts < it.Attempts {
					it.Attempts = row.Attempts
				}
			}
		}
		// Claimed in the database BEFORE the PUT: the UNIQUE(target_id,
		// object_key) index refuses a key another file holds, so the bucket
		// write below can never overwrite that file's object.
		if err := s.store.PutOffloadItem(it); err != nil {
			return fmt.Errorf("claim object key %s: %w", it.Key, err)
		}
		var wantETag string
		if n > s.mpThreshold {
			composite, err := s.putMultipart(ctx, t, c, f, it, n, sh)
			if err != nil {
				return err
			}
			wantETag = composite
		} else {
			body := io.NewSectionReader(f, 0, n)
			if _, err := c.Put(ctx, it.Key, body, n, sh, mb, map[string]string{"sha256": sh}); err != nil {
				return err
			}
			raw, _ := base64.StdEncoding.DecodeString(mb)
			wantETag = hex.EncodeToString(raw)
		}
		if err := verify(ctx, c, t, it.Key, n, sh, wantETag); err != nil {
			return err
		}
		after, err := os.Stat(cand.path)
		if err != nil {
			return err
		}
		if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
			return errors.New("file changed during upload; it will be sent again")
		}
		return nil
	}()
	switch {
	case err != nil && ctx.Err() != nil:
		// Shutdown, not a fault of the file or the target: failed and due
		// again at once, without spending an attempt.
		it.State = db.OffloadFailed
		it.LastError = clipErr(err)
		it.NextAttemptAt = time.Time{}
	case err != nil:
		// Counted from the row as it is NOW, not as this pass read it: an
		// operator who pressed Retry now while the attempt was in flight reset
		// the count, and that reset is not overwritten by a stale copy.
		if cur, cerr := s.store.OffloadItemsFor(it.RelPath); cerr == nil {
			for _, row := range cur {
				if row.TargetID == it.TargetID && row.Attempts < it.Attempts {
					it.Attempts = row.Attempts
				}
			}
		}
		it.Attempts++
		s.log.Warn("offload upload failed", "target", t.Name, "file", it.RelPath, "attempt", it.Attempts, "err", clipErr(err))
		it = s.fail(t, it, err, !retryable(err))
		if it.State == db.OffloadStuck {
			// Giving up: the parts already sent are abandoned on the service
			// too, not left to bill until a lifecycle rule notices.
			s.abortUpload(ctx, c, t.ID, it.RelPath)
		}
	default:
		it.State = db.OffloadVerified
		it.LastError = ""
		it.NextAttemptAt = time.Time{}
		it.VerifiedAt = s.now()
		s.log.Info("offload upload verified", "target", t.Name, "file", it.RelPath, "bytes", it.Bytes)
	}
	if perr := s.store.PutOffloadItem(it); perr != nil {
		s.log.Warn("offload: persist item", "file", it.RelPath, "err", perr)
	}
	return it
}

// Pin marks a recording as in use until the returned func is called. A
// pinned recording is never evicted.
func (s *Service) Pin(name string) func() {
	s.pinMu.Lock()
	if s.pins == nil {
		s.pins = map[string]int{}
	}
	s.pins[name]++
	s.pinMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.pinMu.Lock()
			if s.pins[name]--; s.pins[name] <= 0 {
				delete(s.pins, name)
			}
			s.pinMu.Unlock()
		})
	}
}

// SetInUse registers the question "is a job about to read this recording?",
// asked of the job queue, so queued and running post-production and clip
// exports keep their inputs.
func (s *Service) SetInUse(fn func(name string) bool) {
	s.pinMu.Lock()
	s.busy = fn
	s.pinMu.Unlock()
}

func (s *Service) inUse(name string) bool {
	s.pinMu.Lock()
	pinned, busy := s.pins[name] > 0, s.busy
	s.pinMu.Unlock()
	return pinned || (busy != nil && busy(name))
}

// StuckEvent is what the alert, webhook and event-stream paths are told when
// an item stops retrying on its own.
type StuckEvent struct {
	TargetID   int64
	TargetName string
	RelPath    string
	Kind       string
	Attempts   int
	Error      string
}

// WithStuckHook registers the callback raised once, on the transition, when an
// item becomes stuck.
func WithStuckHook(fn func(StuckEvent)) Option { return func(s *Service) { s.onStuck = fn } }

// SetStuckHook is WithStuckHook after construction: the API server that owns
// the alert and webhook paths is built after the service.
func (s *Service) SetStuckHook(fn func(StuckEvent)) {
	s.stuckMu.Lock()
	s.onStuck = fn
	s.stuckMu.Unlock()
}

// fail records a failed attempt: retried later with backoff, or stuck when it
// was the target's last allowed attempt or the error is permanent.
func (s *Service) fail(t db.StorageTarget, it db.OffloadItem, err error, permanent bool) db.OffloadItem {
	it.LastError = clipErr(err)
	limit := t.MaxAttempts
	if limit <= 0 {
		limit = db.DefaultOffloadMaxAttempts
	}
	if permanent || it.Attempts >= limit {
		was := it.State
		it.State = db.OffloadStuck
		it.NextAttemptAt = time.Time{}
		if was != db.OffloadStuck {
			s.raiseStuck(StuckEvent{TargetID: t.ID, TargetName: t.Name, RelPath: it.RelPath, Kind: it.Kind,
				Attempts: it.Attempts, Error: it.LastError})
		}
		return it
	}
	it.State = db.OffloadFailed
	it.NextAttemptAt = s.now().Add(backoff(it.Attempts, true))
	return it
}

func (s *Service) raiseStuck(ev StuckEvent) {
	s.stuckMu.Lock()
	fn := s.onStuck
	s.stuckMu.Unlock()
	s.log.Warn("offload: item stuck; it will not be retried until Retry now", "target", ev.TargetName,
		"file", ev.RelPath, "attempts", ev.Attempts, "err", ev.Error)
	if fn != nil {
		fn(ev)
	}
}

// Retry puts failed and stuck items back in the queue with attempts reset --
// every item of target when rel is "", one file's items on every target when
// target is 0 -- and starts a pass. It returns how many items moved.
func (s *Service) Retry(target int64, rel string) (int64, error) {
	n, err := s.store.RetryOffloadItems(target, rel)
	if err != nil || n == 0 {
		return n, err
	}
	s.onChange()
	s.Kick()
	return n, nil
}

func retryable(err error) bool {
	if errors.Is(err, ErrChecksumMismatch) {
		return true
	}
	var se *objstore.Error
	if errors.As(err, &se) {
		return objstore.Retryable(err)
	}
	return true
}

// compositeETag is the shape of a multipart object's ETag.
var compositeETag = regexp.MustCompile(`^[0-9a-f]{32}-[0-9]+$`)

// verify confirms the stored object is the bytes that were sent. wantETag is
// the hex MD5 of the body for a single PUT, or the composite
// md5(part MD5s)-N for a multipart upload; an ETag of neither shape (SSE-KMS,
// some gateways) is no evidence either way, and the SHA-256 metadata and the
// optional read-back carry the check.
func verify(ctx context.Context, c Client, t db.StorageTarget, key string, size int64, sh, wantETag string) error {
	info, err := c.Head(ctx, key)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if info.Size != size {
		return fmt.Errorf("%w: stored %d bytes, sent %d", ErrChecksumMismatch, info.Size, size)
	}
	if etag := strings.ToLower(info.ETag); !info.OpaqueETag && (hexMD5.MatchString(etag) || compositeETag.MatchString(etag)) && etag != wantETag {
		return fmt.Errorf("%w: stored ETag %s is not the %s that was sent", ErrChecksumMismatch, etag, wantETag)
	}
	if got, ok := info.Meta["sha256"]; ok && got != sh {
		return fmt.Errorf("%w: stored sha256 metadata differs", ErrChecksumMismatch)
	}
	if t.VerifyDownload {
		body, _, err := c.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("verify download: %w", err)
		}
		defer body.Close()
		h := sha256.New()
		n, err := io.Copy(h, body)
		if err != nil {
			return fmt.Errorf("verify download: %w", err)
		}
		if n != size || hex.EncodeToString(h.Sum(nil)) != sh {
			return fmt.Errorf("%w: downloaded copy hashes differently", ErrChecksumMismatch)
		}
	}
	return nil
}

func backoff(attempts int, retry bool) time.Duration {
	if !retry {
		return backoffCap
	}
	d := backoffBase
	for i := 1; i < attempts && d < backoffCap; i++ {
		d *= 2
	}
	if d > backoffCap {
		d = backoffCap
	}
	return d + time.Duration(rand.Int64N(int64(d)/5+1))
}

func clipErr(err error) string {
	s := err.Error()
	if len(s) > maxErrLen {
		s = s[:maxErrLen]
	}
	return s
}

// evict deletes local recording masters move mode no longer needs on disk.
func (s *Service) evict(ctx context.Context, targets []db.StorageTarget, clients map[int64]Client, cands []candidate, byKey map[string]db.OffloadItem) bool {
	// EVERY ENABLED target counts, usable or not. A target whose secret will
	// not open or whose client could not be built cannot vouch for a copy, so
	// it blocks eviction rather than being quietly left out of the check.
	var enabled []db.StorageTarget
	keep := -1
	for _, t := range targets {
		if !t.Enabled {
			continue
		}
		enabled = append(enabled, t)
		if t.Mode == db.OffloadMove && t.KeepLocalHours > keep {
			keep = t.KeepLocalHours
		}
	}
	if keep < 0 {
		return false
	}
	for _, t := range enabled {
		if !usable(t) || clients[t.ID] == nil {
			return false
		}
	}
	now := s.now()
	changed := false
	for _, cand := range cands {
		if cand.kind != db.OffloadKindRecording || ctx.Err() != nil {
			continue
		}
		if now.Sub(cand.at) < time.Duration(keep)*time.Hour || s.inUse(cand.rel) {
			continue
		}
		var its []db.OffloadItem
		ok := true
		for _, t := range enabled {
			it, found := byKey[itemKey(t.ID, cand.rel)]
			if !found || !Evictable(it, cand.size, cand.mtime, now) {
				ok = false
				break
			}
			its = append(its, it)
		}
		if !ok {
			continue
		}
		// Last look before the only irreversible step: every target still
		// holds an object of exactly this size AND this SHA-256. Size alone is
		// not evidence -- a replaced object can have the same length -- so a
		// missing or different digest, or a missing object, sends the file
		// back to be uploaded again rather than leaving a "verified" row that
		// would never be re-checked.
		ids := make([]int64, 0, len(enabled))
		for i, t := range enabled {
			info, err := clients[t.ID].Head(ctx, its[i].Key)
			if err == nil && info.Size == its[i].Bytes && info.Meta["sha256"] == its[i].SHA256 {
				ids = append(ids, t.ID)
				continue
			}
			ok = false
			if err == nil || errors.Is(err, objstore.ErrNotFound) {
				it := its[i]
				it.State, it.Attempts, it.NextAttemptAt = db.OffloadPending, 0, time.Time{}
				it.LastError = "the stored object no longer matches; uploading again"
				_ = s.store.PutOffloadItem(it)
				changed = true
			}
			s.log.Warn("offload: remote copy failed its pre-eviction check; keeping the local file",
				"target", t.Name, "file", cand.rel, "err", err)
			break
		}
		if !ok {
			continue
		}
		if s.evictOne(cand, ids) {
			s.log.Info("offload: local copy removed after verified upload", "file", cand.rel, "targets", len(ids))
			changed = true
		}
	}
	return changed
}

// evictOne marks the file evicted -- through db.BeginEvict, the transaction
// that re-reads every enabled target's row and refuses unless the set the
// caller just checked is still exactly the enabled set and every row is still
// verified -- then re-checks the file and unlinks it, undoing the mark when
// anything about the file is not as it was checked.
//
// Mark first, unlink second: a crash between them leaves a file that is
// local AND marked evicted, which Pass repairs by clearing the mark. The other
// order would leave a missing file nothing points at.
func (s *Service) evictOne(cand candidate, checked []int64) bool {
	if err := s.store.BeginEvict(cand.rel, checked); err != nil {
		return false
	}
	path, err := s.rec.Resolve(cand.rel)
	if err == nil {
		var fi os.FileInfo
		if fi, err = os.Stat(path); err == nil && (fi.Size() != cand.size || !fi.ModTime().Equal(cand.mtime)) {
			err = errors.New("file changed since it was checked")
		}
	}
	if err == nil {
		err = os.Remove(path)
	}
	if err != nil {
		// Including ErrNotExist: a file somebody else removed was not evicted
		// by us, and marking it so would keep a deleted recording's row alive.
		_ = s.store.UndoEvict(cand.rel)
		s.log.Warn("offload: evict", "file", cand.rel, "err", err)
		return false
	}
	return true
}

// Evictable is the per-target half of the eviction rule: the item is
// verified, records the same size as the local file, the local file has not
// been touched since verification, and no recent fetch-back pins it.
func Evictable(it db.OffloadItem, localSize int64, localMtime, now time.Time) bool {
	if it.State != db.OffloadVerified || it.SHA256 == "" || it.VerifiedAt.IsZero() {
		return false
	}
	if it.Bytes != localSize || localMtime.After(it.VerifiedAt) {
		return false
	}
	if !it.FetchedAt.IsZero() && now.Sub(it.FetchedAt) < db.OffloadFetchPin {
		return false
	}
	return true
}

// ---- object keys ----

var tokenRE = regexp.MustCompile(`\{[a-z]+\}`)
var safeTemplate = regexp.MustCompile(`^[A-Za-z0-9/_.{}=-]*$`)

// ValidatePrefixTemplate refuses a template with unknown tokens, characters
// outside a conservative set, an absolute path or a parent reference.
func ValidatePrefixTemplate(tpl string) error {
	if len(tpl) > 200 {
		return errors.New("prefix template is longer than 200 characters")
	}
	if !safeTemplate.MatchString(tpl) {
		return errors.New("prefix template may use letters, digits, / _ . = - and {tokens} only")
	}
	if strings.HasPrefix(tpl, "/") {
		return errors.New("prefix template must be relative (no leading /)")
	}
	for _, seg := range strings.Split(tpl, "/") {
		if seg == ".." || seg == "." {
			return errors.New("prefix template must not contain . or .. segments")
		}
	}
	for _, tok := range tokenRE.FindAllString(tpl, -1) {
		switch tok {
		case "{kind}", "{source}", "{yyyy}", "{mm}", "{dd}":
		default:
			return fmt.Errorf("unknown token %s; use {kind} {source} {yyyy} {mm} {dd}", tok)
		}
	}
	if strings.Count(tpl, "{") != len(tokenRE.FindAllString(tpl, -1)) || strings.Count(tpl, "}") != strings.Count(tpl, "{") {
		return errors.New("prefix template has an unbalanced brace")
	}
	return nil
}

var kindDir = map[string]string{
	db.OffloadKindRecording:  "recordings",
	db.OffloadKindStem:       "stems",
	db.OffloadKindTranscript: "transcripts",
	db.OffloadKindClip:       "clips",
	"test":                   "test",
}

// ObjectKey renders a target's prefix template for one file. rel is the
// file's slash-separated path relative to the recordings directory, kept
// whole -- stems/x.flac and exports/x.flac never share a key just because
// they share a basename.
func ObjectKey(tpl, kind string, source *int64, at time.Time, rel string) (string, error) {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, `\`) {
		return "", fmt.Errorf("invalid file path %q", rel)
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("invalid file path %q", rel)
		}
	}
	base := rel
	if tpl == "" {
		tpl = db.DefaultOffloadPrefix
	}
	if err := ValidatePrefixTemplate(tpl); err != nil {
		return "", err
	}
	src := "unattributed"
	if source != nil {
		src = "s" + strconv.FormatInt(*source, 10)
	}
	at = at.UTC()
	r := strings.NewReplacer("{kind}", kindDir[kind], "{source}", src,
		"{yyyy}", at.Format("2006"), "{mm}", at.Format("01"), "{dd}", at.Format("02"))
	prefix := strings.Trim(r.Replace(tpl), "/")
	for strings.Contains(prefix, "//") {
		prefix = strings.ReplaceAll(prefix, "//", "/")
	}
	if prefix == "" {
		return base, nil
	}
	return prefix + "/" + base, nil
}
