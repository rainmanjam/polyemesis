package offload

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/objstore"
)

// maxParts is the S3 ceiling on parts per upload.
const maxParts = 10000

// putMultipart sends f in parts, resuming an upload a previous attempt (or a
// previous process) left in progress for exactly these bytes, and returns the
// composite ETag the completed object should carry.
//
// Progress is persisted after every part, so a retry or a restart re-sends
// only what never arrived. An upload whose recorded size, digest or key no
// longer match the file is aborted and started over: its parts are of other
// bytes.
func (s *Service) putMultipart(ctx context.Context, t db.StorageTarget, c Client, f *os.File, it db.OffloadItem, n int64, sh string) (string, error) {
	mp, err := s.store.GetOffloadMultipart(t.ID, it.RelPath)
	if err == nil && (mp.Bytes != n || mp.SHA256 != sh || mp.Key != it.Key) {
		_ = c.AbortMultipart(ctx, mp.Key, mp.UploadID)
		_ = s.store.DeleteOffloadMultipart(t.ID, it.RelPath)
		err = db.ErrNotFound
	}
	if errors.Is(err, db.ErrNotFound) {
		partSize := s.partSize
		if floor := (n + maxParts - 1) / maxParts; floor > partSize {
			partSize = floor
		}
		id, cerr := c.CreateMultipart(ctx, it.Key, map[string]string{"sha256": sh})
		if cerr != nil {
			return "", cerr
		}
		mp = db.OffloadMultipart{TargetID: t.ID, RelPath: it.RelPath, Key: it.Key, UploadID: id,
			PartSize: partSize, Bytes: n, SHA256: sh}
		// Persisted before the first part, so a crash from here on leaves an
		// upload the startup sweep knows about and can resume or abort.
		if perr := s.store.PutOffloadMultipart(mp); perr != nil {
			_ = c.AbortMultipart(ctx, it.Key, id)
			return "", perr
		}
	} else if err != nil {
		return "", err
	}

	done := map[int]bool{}
	for _, p := range mp.Parts {
		done[p.Number] = true
	}
	count := int((n + mp.PartSize - 1) / mp.PartSize)
	for i := 1; i <= count; i++ {
		if done[i] {
			continue
		}
		off := int64(i-1) * mp.PartSize
		size := min(mp.PartSize, n-off)
		sec := io.NewSectionReader(f, off, size)
		psh, pmb, err := digestOf(sec)
		if err != nil {
			return "", err
		}
		etag, err := s.uploadPart(ctx, c, mp, i, sec, size, psh, pmb)
		if err != nil {
			return "", err
		}
		mp.Parts = append(mp.Parts, db.OffloadPart{Number: i, ETag: etag, MD5: pmb, Size: size})
		if err := s.store.PutOffloadMultipart(mp); err != nil {
			return "", err
		}
	}
	sort.Slice(mp.Parts, func(a, b int) bool { return mp.Parts[a].Number < mp.Parts[b].Number })
	parts := make([]objstore.Part, len(mp.Parts))
	for i, p := range mp.Parts {
		parts[i] = objstore.Part{Number: p.Number, ETag: p.ETag, MD5: p.MD5, Size: p.Size}
	}
	if _, err := c.CompleteMultipart(ctx, mp.Key, mp.UploadID, parts); err != nil {
		var se *objstore.Error
		if errors.As(err, &se) && se.Code == "NoSuchUpload" {
			// The service forgot it (expired, or a lifecycle rule aborted
			// it): start over next attempt rather than resume nothing.
			_ = s.store.DeleteOffloadMultipart(t.ID, it.RelPath)
		}
		return "", err
	}
	_ = s.store.DeleteOffloadMultipart(t.ID, it.RelPath)
	return objstore.CompositeETag(parts)
}

// uploadPart sends one part, retrying a retryable failure with backoff.
func (s *Service) uploadPart(ctx context.Context, c Client, mp db.OffloadMultipart, n int, body io.ReadSeeker, size int64, sh, mb string) (string, error) {
	wait := s.partBackoff
	var err error
	for try := 0; try < s.partTries; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(wait):
			}
			wait *= 2
		}
		var etag string
		if etag, err = c.UploadPart(ctx, mp.Key, mp.UploadID, n, body, size, sh, mb); err == nil {
			return etag, nil
		}
		if ctx.Err() != nil || !retryable(err) {
			return "", err
		}
	}
	return "", err
}

func digestOf(r io.ReadSeeker) (string, string, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	sh, mh := sha256.New(), md5.New()
	if _, err := io.Copy(io.MultiWriter(sh, mh), r); err != nil {
		return "", "", err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	return hex.EncodeToString(sh.Sum(nil)), base64.StdEncoding.EncodeToString(mh.Sum(nil)), nil
}

// abortUpload abandons a file's multipart upload on the service and forgets it.
func (s *Service) abortUpload(ctx context.Context, c Client, target int64, rel string) {
	mp, err := s.store.GetOffloadMultipart(target, rel)
	if err != nil {
		return
	}
	if err := c.AbortMultipart(context.WithoutCancel(ctx), mp.Key, mp.UploadID); err != nil {
		s.log.Warn("offload: abort multipart upload", "file", rel, "err", clipErr(err))
		return
	}
	_ = s.store.DeleteOffloadMultipart(target, rel)
}

// AbortUploads aborts the given multipart uploads on target t (its OLD
// binding, after a rebind dropped the rows). Best effort, like every abort:
// the bucket lifecycle rule is the backstop.
func (s *Service) AbortUploads(ctx context.Context, t db.StorageTarget, rows []db.OffloadMultipart) {
	if len(rows) == 0 {
		return
	}
	c, err := s.newClient(t)
	if err != nil {
		return
	}
	for _, mp := range rows {
		if err := c.AbortMultipart(context.WithoutCancel(ctx), mp.Key, mp.UploadID); err != nil {
			s.log.Warn("offload: abort multipart upload at the old address", "file", mp.RelPath, "err", clipErr(err))
		}
	}
}

// AbortTarget aborts every multipart upload in progress on a target. Called
// before the target is deleted, while its credentials are still readable.
func (s *Service) AbortTarget(ctx context.Context, t db.StorageTarget) {
	rows, err := s.store.ListOffloadMultipart(t.ID)
	if err != nil || len(rows) == 0 {
		return
	}
	c, err := s.newClient(t)
	if err != nil {
		return
	}
	for _, mp := range rows {
		s.abortUpload(ctx, c, t.ID, mp.RelPath)
	}
}

// Recover is restart safety, run once before the first pass:
//   - an item a previous process left "uploading" is failed and due at once;
//   - a multipart upload that will not be resumed -- its item stuck, verified
//     or gone, or its file gone -- is aborted on the service.
//
// A multipart upload whose item is still pending or failed is kept: the next
// pass resumes it from the parts already sent.
func (s *Service) Recover(ctx context.Context) {
	if n, err := s.store.RecoverInterruptedOffload(); err != nil {
		s.log.Warn("offload: recover interrupted uploads", "err", err)
	} else if n > 0 {
		s.log.Info("offload: uploads interrupted by a restart will be retried", "items", n)
	}
	dir := s.rec.Dir()
	rows, err := s.store.ListOffloadMultipart(0)
	if err != nil {
		return
	}
	clients := map[int64]Client{}
	for _, mp := range rows {
		keep := false
		if its, err := s.store.OffloadItemsFor(mp.RelPath); err == nil {
			for _, it := range its {
				if it.TargetID == mp.TargetID &&
					(it.State == db.OffloadPending || it.State == db.OffloadFailed || it.State == db.OffloadUploading) {
					keep = true
				}
			}
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(mp.RelPath))); err != nil {
			keep = false
		}
		if keep {
			continue
		}
		c, ok := clients[mp.TargetID]
		if !ok {
			if t, err := s.store.GetStorageTarget(s.box, mp.TargetID); err == nil {
				c, _ = s.newClient(t)
			}
			clients[mp.TargetID] = c
		}
		if c == nil {
			// The target is gone or unusable: nothing can abort it from here,
			// which is what the bucket lifecycle rule in STORAGE.md is for.
			_ = s.store.DeleteOffloadMultipart(mp.TargetID, mp.RelPath)
			continue
		}
		s.abortUpload(ctx, c, mp.TargetID, mp.RelPath)
	}
}
