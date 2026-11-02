package offload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Fetch states.
const (
	FetchFetching = "fetching"
	FetchDone     = "done"
	FetchFailed   = "failed"
)

// FetchState is the progress of bringing one evicted recording back.
type FetchState struct {
	State string    `json:"state"`
	Error string    `json:"error,omitempty"`
	Since time.Time `json:"since"`
}

// ErrNotEvicted answers a fetch for a file that is already local or was never
// offloaded.
var ErrNotEvicted = errors.New("this recording is not offloaded")

// Evicted reports whether name's local copy was moved to a target.
func (s *Service) Evicted(name string) bool {
	its, err := s.store.OffloadItemsFor(name)
	if err != nil {
		return false
	}
	for _, it := range its {
		if it.Evicted {
			return true
		}
	}
	return false
}

// FetchStatus returns the fetch in flight or last finished for name.
func (s *Service) FetchStatus(name string) (FetchState, bool) {
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()
	st, ok := s.fetches[name]
	if !ok {
		return FetchState{}, false
	}
	return *st, true
}

// Fetch starts bringing an evicted recording back to local disk and returns
// immediately. A fetch already in flight is joined, not restarted. The
// clipper and the job endpoints call this when a source is missing locally,
// answer 409 "fetching", and the UI retries once the recordings event says
// the file is local again.
func (s *Service) Fetch(name string) (FetchState, error) {
	if !s.Evicted(name) {
		return FetchState{}, ErrNotEvicted
	}
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()
	if st, ok := s.fetches[name]; ok && st.State == FetchFetching {
		return *st, nil
	}
	st := &FetchState{State: FetchFetching, Since: s.now()}
	s.fetches[name] = st
	ctx := s.baseCtx
	go func() {
		err := s.FetchNow(ctx, name)
		s.fetchMu.Lock()
		if err != nil {
			st.State, st.Error = FetchFailed, clipErr(err)
		} else {
			st.State, st.Error = FetchDone, ""
		}
		st.Since = s.now()
		s.fetchMu.Unlock()
		s.onChange()
	}()
	return *st, nil
}

// FetchNow fetches synchronously: GET from the first target holding a
// verified copy, stream into a temporary file in the recordings directory,
// check size and SHA-256 against what was uploaded, then rename into place.
// A copy that hashes differently is discarded and the next target tried.
func (s *Service) FetchNow(ctx context.Context, name string) error {
	final, err := s.rec.Resolve(name)
	if err != nil {
		return err
	}
	its, err := s.store.OffloadItemsFor(name)
	if err != nil {
		return err
	}
	var lastErr error = ErrNotEvicted
	for _, it := range its {
		if it.State != db.OffloadVerified || it.SHA256 == "" {
			continue
		}
		t, err := s.store.GetStorageTarget(it.TargetID)
		if err != nil || !t.HasSecret || t.SecretUnreadable {
			lastErr = fmt.Errorf("target %d unavailable", it.TargetID)
			continue
		}
		c, err := s.newClient(t)
		if err != nil {
			lastErr = err
			continue
		}
		if err := s.fetchOne(ctx, c, it, final); err != nil {
			lastErr = fmt.Errorf("%s: %w", t.Name, err)
			s.log.Warn("offload fetch-back failed", "target", t.Name, "file", name, "err", err)
			continue
		}
		now := s.now()
		for _, all := range its {
			all.Evicted = false
			all.FetchedAt = now
			_ = s.store.PutOffloadItem(all)
		}
		s.log.Info("offload: fetched back", "target", t.Name, "file", name, "bytes", it.Bytes)
		return nil
	}
	return lastErr
}

func (s *Service) fetchOne(ctx context.Context, c Client, it db.OffloadItem, final string) error {
	body, info, err := c.Get(ctx, it.Key)
	if err != nil {
		return err
	}
	defer body.Close()
	// Refuse before writing a byte: the object's declared length must be the
	// length that was uploaded. A broken or hostile endpoint must not be able
	// to fill the recordings volume ahead of the digest check below.
	if info.Size != it.Bytes {
		return fmt.Errorf("%w: object declares %d bytes, %d were uploaded", ErrChecksumMismatch, info.Size, it.Bytes)
	}
	tmp, err := os.CreateTemp(filepath.Dir(final), ".offload-fetch-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	h := sha256.New()
	// And never trust the declaration either: at most one byte past the
	// uploaded length is ever read, which is enough to tell "longer" apart.
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(body, it.Bytes+1))
	if err == nil && n > it.Bytes {
		err = fmt.Errorf("%w: object is longer than the %d bytes uploaded", ErrChecksumMismatch, it.Bytes)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != it.Bytes || hex.EncodeToString(h.Sum(nil)) != it.SHA256 {
		return fmt.Errorf("%w: fetched copy is not the uploaded bytes", ErrChecksumMismatch)
	}
	if _, err := os.Stat(final); err == nil {
		return errors.New("a local file already exists under this name")
	}
	if err := os.Rename(tmpName, final); err != nil {
		return err
	}
	keep = true
	return nil
}
