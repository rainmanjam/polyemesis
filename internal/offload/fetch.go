package offload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
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
		t, err := s.store.GetStorageTarget(s.box, it.TargetID)
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
		// The rename gave the file a new mtime, which the next pass would
		// read as "changed since it was verified" and send the whole file
		// again. These are the verified bytes, so the mtime goes back to
		// before the earliest verification.
		settled := it.VerifiedAt
		for _, other := range its {
			if !other.VerifiedAt.IsZero() && other.VerifiedAt.Before(settled) {
				settled = other.VerifiedAt
			}
		}
		if !settled.IsZero() {
			at := settled.Add(-time.Second)
			_ = os.Chtimes(final, at, at)
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

// fetchOne brings one object into final: into a temporary file beside it,
// with up to fetchTries attempts that RESUME from the bytes already received
// (HTTP Range) rather than starting over, a running SHA-256 over the whole
// file, and a rename only once the size and digest are the uploaded ones.
//
// The size cap holds on every attempt: an object declaring any other total is
// refused before a byte is written, and no attempt reads more than one byte
// past what remains.
func (s *Service) fetchOne(ctx context.Context, c Client, it db.OffloadItem, final string) error {
	tmp, err := os.CreateTemp(filepath.Dir(final), ".offload-fetch-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	h := sha256.New()
	var got int64
	wait := s.fetchBackoff
	for try := 0; ; try++ {
		err = s.fetchAttempt(ctx, c, it, tmp, h, &got)
		if err == nil {
			break
		}
		// A declared length or digest that is wrong will not become right by
		// asking again, and a cancelled context means stop.
		if errors.Is(err, ErrChecksumMismatch) || ctx.Err() != nil || try+1 >= s.fetchTries || !retryable(err) {
			return err
		}
		s.log.Warn("offload: fetch-back attempt failed; resuming", "file", it.RelPath, "have", got, "err", clipErr(err))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
	}
	if got != it.Bytes || hex.EncodeToString(h.Sum(nil)) != it.SHA256 {
		return fmt.Errorf("%w: fetched copy is not the uploaded bytes", ErrChecksumMismatch)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
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

// fetchAttempt appends what the service sends from *got on to tmp and h.
func (s *Service) fetchAttempt(ctx context.Context, c Client, it db.OffloadItem, tmp *os.File, h hash.Hash, got *int64) error {
	body, info, partial, err := c.GetFrom(ctx, it.Key, *got)
	if err != nil {
		return err
	}
	defer body.Close()
	if info.Size != it.Bytes {
		return fmt.Errorf("%w: object declares %d bytes, %d were uploaded", ErrChecksumMismatch, info.Size, it.Bytes)
	}
	if *got > 0 && !partial {
		// The service ignored the Range and sent everything: start over.
		if err := tmp.Truncate(0); err != nil {
			return err
		}
		h.Reset()
		*got = 0
	}
	if _, err := tmp.Seek(*got, io.SeekStart); err != nil {
		return err
	}
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(body, it.Bytes-*got+1))
	*got += n
	if *got > it.Bytes {
		return fmt.Errorf("%w: object is longer than the %d bytes uploaded", ErrChecksumMismatch, it.Bytes)
	}
	if err != nil {
		return err
	}
	if *got < it.Bytes {
		return fmt.Errorf("fetch ended after %d of %d bytes", *got, it.Bytes)
	}
	return nil
}
