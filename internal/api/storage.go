package api

// Storage targets and offload (Wave 4 #10): S3-compatible buckets finished
// recordings are copied to, and in move mode moved to.
//
// The secret access key goes in once, on create or update, is sealed by the
// database's secret box and is never returned: StorageTarget.SecretAccessKey
// is json:"-" and responses carry hasSecret. A read-scoped token additionally
// gets readSafeStorageTarget, which blanks the access key id. The Test route
// is session-only, like /automod/test, because it unseals the secret and
// sends a signed request out of the process.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/netguard"
	"github.com/rainmanjam/polyemesis/internal/offload"
)

// storageTargetView is a target as the settings page lists it: the row and
// how many files sit in each offload state on it.
type storageTargetView struct {
	db.StorageTarget
	Counts map[string]int `json:"counts"`
	// Evicted counts recordings whose only copy is this target's.
	Evicted int `json:"evicted"`
}

// storageTargetRequest is PATCH-shaped: a nil field keeps the stored value.
type storageTargetRequest struct {
	// ID, on the Test route only, names a saved target whose stored secret
	// the probe may use when the form left the secret blank.
	ID                 *int64  `json:"id,omitempty"`
	Name               *string `json:"name"`
	Provider           *string `json:"provider"`
	Endpoint           *string `json:"endpoint"`
	Bucket             *string `json:"bucket"`
	Region             *string `json:"region"`
	PrefixTemplate     *string `json:"prefixTemplate"`
	PathStyle          *bool   `json:"pathStyle"`
	AccessKeyID        *string `json:"accessKeyId"`
	SecretAccessKey    *string `json:"secretAccessKey"`
	Mode               *string `json:"mode"`
	KeepLocalHours     *int    `json:"keepLocalHours"`
	OffloadStems       *bool   `json:"offloadStems"`
	OffloadTranscripts *bool   `json:"offloadTranscripts"`
	OffloadClips       *bool   `json:"offloadClips"`
	VerifyDownload     *bool   `json:"verifyDownload"`
	AllowPrivateTarget *bool   `json:"allowPrivateTarget"`
	Enabled            *bool   `json:"enabled"`
	MaxAttempts        *int    `json:"maxAttempts"`
}

func (q storageTargetRequest) applyTo(t db.StorageTarget) db.StorageTarget {
	str := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	bl := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	str(&t.Name, q.Name)
	str(&t.Provider, q.Provider)
	str(&t.Endpoint, q.Endpoint)
	str(&t.Bucket, q.Bucket)
	str(&t.Region, q.Region)
	str(&t.PrefixTemplate, q.PrefixTemplate)
	str(&t.AccessKeyID, q.AccessKeyID)
	str(&t.Mode, q.Mode)
	bl(&t.PathStyle, q.PathStyle)
	bl(&t.OffloadStems, q.OffloadStems)
	bl(&t.OffloadTranscripts, q.OffloadTranscripts)
	bl(&t.OffloadClips, q.OffloadClips)
	bl(&t.VerifyDownload, q.VerifyDownload)
	bl(&t.AllowPrivateTarget, q.AllowPrivateTarget)
	bl(&t.Enabled, q.Enabled)
	if q.KeepLocalHours != nil {
		t.KeepLocalHours = *q.KeepLocalHours
	}
	if q.MaxAttempts != nil {
		t.MaxAttempts = *q.MaxAttempts
	}
	t.Endpoint = strings.TrimSuffix(t.Endpoint, "/")
	return t
}

var (
	bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionName = regexp.MustCompile(`^[a-z0-9-]{0,40}$`)
)

// validateStorageTarget refuses a target that cannot work or that points
// somewhere it must not. A literal private address is refused at save time
// unless the target opted in; a hostname is checked at dial time by the same
// netguard guard webhooks use.
func validateStorageTarget(t db.StorageTarget) error {
	if t.Name == "" || len(t.Name) > 80 {
		return errors.New("name is required, at most 80 characters")
	}
	switch t.Provider {
	case db.StorageProviderS3, db.StorageProviderR2, db.StorageProviderB2, db.StorageProviderMinIO, db.StorageProviderWasabi:
	default:
		return errors.New("provider must be one of s3, r2, b2, minio, wasabi")
	}
	u, err := url.Parse(t.Endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.New("endpoint must be an http(s) URL, e.g. https://s3.us-east-1.amazonaws.com")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("endpoint must not carry credentials, a query or a fragment; credentials go in the access key fields")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !netguard.IsPublicAddr(ip) && !t.AllowPrivateTarget {
		return errors.New("endpoint is a private address; enable allowPrivateTarget to offload to a self-hosted target on purpose")
	}
	if !bucketName.MatchString(t.Bucket) {
		return errors.New("bucket must be 3-63 characters of lowercase letters, digits, dots and hyphens")
	}
	if !regionName.MatchString(t.Region) {
		return errors.New("region must be lowercase letters, digits and hyphens (R2 uses auto)")
	}
	if err := offload.ValidatePrefixTemplate(t.PrefixTemplate); err != nil {
		return err
	}
	if t.Mode != db.OffloadMirror && t.Mode != db.OffloadMove {
		return errors.New("mode must be mirror or move")
	}
	if t.KeepLocalHours < 0 || t.KeepLocalHours > 24*365 {
		return errors.New("keepLocalHours must be between 0 and 8760")
	}
	if t.MaxAttempts < 1 || t.MaxAttempts > 100 {
		return errors.New("maxAttempts must be between 1 and 100")
	}
	if t.AccessKeyID == "" || len(t.AccessKeyID) > 128 {
		return errors.New("access key id is required")
	}
	if len(t.SecretAccessKey) > 256 {
		return errors.New("secret access key is too long")
	}
	return nil
}

// THE SECRET IS BOUND TO WHERE IT WAS SAVED FOR, as the automod key is bound
// to its endpoint (automod_probe.go). A stored secret is used only against the
// endpoint, bucket and private-address opt-in it was saved with; a Test or a
// save that points the target anywhere else must carry the secret itself.
// SigV4 never transmits the secret, but a signature made with it is still the
// credential's authority, and it is not this route's to lend to an address the
// caller chose.
const errSecretRebind = "the endpoint, bucket or private-address setting differs from the saved target, " +
	"so its saved secret access key is not used; enter the secret access key again"

func normalEndpoint(e string) string {
	e = strings.TrimSuffix(strings.TrimSpace(e), "/")
	if u, err := url.Parse(e); err == nil {
		u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
		return u.String()
	}
	return e
}

// secretBindingChanged reports whether next points the saved secret somewhere
// other than saved.
func secretBindingChanged(saved, next db.StorageTarget) bool {
	return normalEndpoint(saved.Endpoint) != normalEndpoint(next.Endpoint) ||
		saved.Bucket != next.Bucket || saved.AllowPrivateTarget != next.AllowPrivateTarget
}

// readSafeStorageTarget is the read-scope view: no secret (never marshalled
// anyway) and no access key id.
func readSafeStorageTarget(t db.StorageTarget) db.StorageTarget {
	t.SecretAccessKey = ""
	t.AccessKeyID = ""
	return t
}

func (s *Server) storageTargetViews(r *http.Request, targets []db.StorageTarget) []storageTargetView {
	items, err := s.store.ListOffloadItems()
	if err != nil {
		s.log.Warn("storage: offload items unavailable", "err", err)
	}
	hide := readScopeCannotSeePublishTokens(r)
	out := make([]storageTargetView, 0, len(targets))
	for _, t := range targets {
		if hide {
			t = readSafeStorageTarget(t)
		}
		v := storageTargetView{StorageTarget: t, Counts: map[string]int{}}
		for _, it := range items {
			if it.TargetID != t.ID {
				continue
			}
			v.Counts[it.State]++
			if it.Evicted {
				v.Evicted++
			}
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) handleListStorageTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := s.store.ListStorageTargets(s.box)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"targets":   s.storageTargetViews(r, targets),
		"available": s.offload != nil,
	})
}

func (s *Server) handleGetStorageTarget(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	t, err := s.store.GetStorageTarget(s.box, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.storageTargetViews(r, []db.StorageTarget{t})[0])
}

func (s *Server) handleCreateStorageTarget(w http.ResponseWriter, r *http.Request) {
	var req storageTargetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	t := req.applyTo(db.StorageTarget{Provider: db.StorageProviderS3, Mode: db.OffloadMirror, KeepLocalHours: 24, Enabled: true,
		MaxAttempts: db.DefaultOffloadMaxAttempts})
	if req.SecretAccessKey != nil {
		t.SecretAccessKey = strings.TrimSpace(*req.SecretAccessKey)
	}
	if t.SecretAccessKey == "" {
		writeError(w, http.StatusBadRequest, "secret access key is required")
		return
	}
	if err := validateStorageTarget(t); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.store.CreateStorageTarget(s.box, t)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	saved, err := s.store.GetStorageTarget(s.box, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.kickOffload()
	writeJSON(w, http.StatusCreated, s.storageTargetViews(r, []db.StorageTarget{saved})[0])
}

func (s *Server) handleUpdateStorageTarget(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := s.store.GetStorageTarget(s.box, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req storageTargetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	t := req.applyTo(existing)
	// The stored secret is not carried onto the update: an empty secret
	// means "unchanged" to db.UpdateStorageTarget, so the plaintext never
	// round-trips through this handler.
	t.SecretAccessKey = ""
	if req.SecretAccessKey != nil {
		t.SecretAccessKey = strings.TrimSpace(*req.SecretAccessKey)
	}
	if t.SecretAccessKey == "" && secretBindingChanged(existing, t) {
		writeErrorCode(w, http.StatusBadRequest, "secret_required", errSecretRebind)
		return
	}
	if err := validateStorageTarget(t); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.UpdateStorageTarget(s.box, t); err != nil {
		writeStoreError(w, err)
		return
	}
	saved, err := s.store.GetStorageTarget(s.box, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.kickOffload()
	writeJSON(w, http.StatusOK, s.storageTargetViews(r, []db.StorageTarget{saved})[0])
}

func (s *Server) handleDeleteStorageTarget(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	t, err := s.store.GetStorageTarget(s.box, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Refused before anything is aborted, so a 409 changes nothing.
	if err := s.store.StorageTargetDeletable(id); err != nil {
		writeStoreError(w, err)
		return
	}
	// Abandon its multipart uploads on the service while its credentials
	// can still be read; after the delete nothing could.
	if s.offload != nil {
		s.offload.AbortTarget(r.Context(), t)
	}
	if err := s.store.DeleteStorageTarget(id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleTestStorageTarget runs put, get and delete against the target the
// form describes and reports which step failed. With an id and no secret in
// the body, the saved secret is used, so a saved target can be re-tested
// without re-typing it.
func (s *Server) handleTestStorageTarget(w http.ResponseWriter, r *http.Request) {
	var req storageTargetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	base := db.StorageTarget{Provider: db.StorageProviderS3, Mode: db.OffloadMirror, Enabled: true,
		MaxAttempts: db.DefaultOffloadMaxAttempts}
	var saved *db.StorageTarget
	if req.ID != nil {
		row, err := s.store.GetStorageTarget(s.box, *req.ID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		base = row
		saved = &row
	}
	t := req.applyTo(base)
	if req.SecretAccessKey != nil && strings.TrimSpace(*req.SecretAccessKey) != "" {
		t.SecretAccessKey = strings.TrimSpace(*req.SecretAccessKey)
	} else if saved != nil && secretBindingChanged(*saved, t) {
		writeErrorCode(w, http.StatusBadRequest, "secret_required", errSecretRebind)
		return
	}
	if t.SecretAccessKey == "" {
		if t.SecretUnreadable {
			writeError(w, http.StatusConflict, "the saved secret cannot be opened on this machine; enter it again")
			return
		}
		writeError(w, http.StatusBadRequest, "secret access key is required")
		return
	}
	if t.Name == "" {
		t.Name = "test"
	}
	if err := validateStorageTarget(t); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, offload.Probe(r.Context(), t, nil))
}

// kickOffload runs a pass soon after a target changes, rather than on the
// next 30-second tick.
func (s *Server) kickOffload() {
	off := s.offload
	if off == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		off.Pass(ctx)
	}()
}

// ---- Library integration ----

// recordingOffload is one recording's offload state, per target.
type recordingOffload struct {
	// Evicted means move mode removed the local copy; the clipper and the
	// post-production jobs fetch it back first.
	Evicted bool                 `json:"evicted"`
	Fetch   *offload.FetchState  `json:"fetch,omitempty"`
	Targets []recordingOffloadOn `json:"targets"`
}

type recordingOffloadOn struct {
	TargetID   int64     `json:"targetId"`
	TargetName string    `json:"targetName"`
	State      string    `json:"state"`
	Attempts   int       `json:"attempts"`
	LastError  string    `json:"lastError,omitempty"`
	VerifiedAt time.Time `json:"verifiedAt,omitzero"`
}

// offloadIndex is every item grouped by file, loaded once per listing.
type offloadIndex struct {
	byRel map[string][]db.OffloadItem
	names map[int64]string
	svc   *offload.Service
}

func (s *Server) loadOffloadIndex() offloadIndex {
	idx := offloadIndex{byRel: map[string][]db.OffloadItem{}, names: map[int64]string{}, svc: s.offload}
	items, err := s.store.ListOffloadItems()
	if err != nil {
		s.log.Warn("library: offload state unavailable", "err", err)
		return idx
	}
	for _, it := range items {
		idx.byRel[it.RelPath] = append(idx.byRel[it.RelPath], it)
	}
	if len(items) > 0 {
		if targets, err := s.store.ListStorageTargets(s.box); err == nil {
			for _, t := range targets {
				idx.names[t.ID] = t.Name
			}
		}
	}
	return idx
}

func (idx offloadIndex) view(filename string) *recordingOffload {
	its := idx.byRel[filename]
	if len(its) == 0 {
		return nil
	}
	v := &recordingOffload{Targets: make([]recordingOffloadOn, 0, len(its))}
	for _, it := range its {
		v.Evicted = v.Evicted || it.Evicted
		v.Targets = append(v.Targets, recordingOffloadOn{TargetID: it.TargetID, TargetName: idx.names[it.TargetID],
			State: it.State, Attempts: it.Attempts, LastError: it.LastError, VerifiedAt: it.VerifiedAt})
	}
	if idx.svc != nil {
		if st, ok := idx.svc.FetchStatus(filename); ok {
			v.Fetch = &st
		}
	}
	return v
}

// errRecordingOffloaded is what every path that needs a recording's bytes
// answers when move mode has the only copy in a bucket. The answer is a 409
// with code "offloaded" and the files to fetch; POST
// /library/recordings/{id}/fetch brings them back.
type errRecordingOffloaded struct{ files []string }

func (e errRecordingOffloaded) Error() string {
	return fmt.Sprintf("%d of this recording's files are in offload storage; fetch them back to use them", len(e.files))
}

func (s *Server) writeOffloaded(w http.ResponseWriter, e errRecordingOffloaded) {
	states := map[string]offload.FetchState{}
	if s.offload != nil {
		for _, f := range e.files {
			if st, ok := s.offload.FetchStatus(f); ok {
				states[f] = st
			}
		}
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"error": e.Error(), "code": "offloaded", "offloaded": e.files, "fetch": states,
	})
}

// refuseOffloaded answers 409 "offloaded" for a single recording whose local
// copy was moved, and reports whether it did.
func (s *Server) refuseOffloaded(w http.ResponseWriter, filename string) bool {
	if s.offload == nil || !s.offload.Evicted(filename) {
		return false
	}
	s.writeOffloaded(w, errRecordingOffloaded{files: []string{filename}})
	return true
}

// handleFetchRecording starts bringing a recording -- and every other
// offloaded segment of its session, which is what the clipper scrubs
// across -- back to local disk. It answers 202 immediately; the recordings
// event fires as each fetch finishes, and the clipper retries then.
func (s *Server) handleFetchRecording(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if s.offload == nil {
		writeError(w, http.StatusServiceUnavailable, "offload is not running")
		return
	}
	rec, err := s.store.GetRecording(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	files := []string{rec.Filename}
	if sess, err := s.store.SessionForRecording(id); err == nil && sess != nil {
		if members, err := s.store.SessionRecordings(sess.ID); err == nil {
			files = files[:0]
			for _, m := range members {
				files = append(files, m.Filename)
			}
		}
	}
	started := map[string]offload.FetchState{}
	for _, f := range files {
		st, err := s.offload.Fetch(f)
		if errors.Is(err, offload.ErrNotEvicted) {
			continue
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		started[f] = st
	}
	if len(started) == 0 {
		writeError(w, http.StatusConflict, "this recording is already on local disk")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"fetch": started})
}

// handleRetryStorageTarget is the target card's Retry now: every failed or
// stuck item on the target goes back in the queue with its attempts reset.
func (s *Server) handleRetryStorageTarget(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if _, err := s.store.GetStorageTarget(s.box, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.retryOffload(w, id, "")
}

// handleRetryRecordingOffload is the Library item's Retry now: the
// recording's failed or stuck items, on every target.
func (s *Server) handleRetryRecordingOffload(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	rec, err := s.store.GetRecording(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.retryOffload(w, 0, rec.Filename)
}

func (s *Server) retryOffload(w http.ResponseWriter, target int64, rel string) {
	var n int64
	var err error
	if s.offload != nil {
		n, err = s.offload.Retry(target, rel)
	} else {
		n, err = s.store.RetryOffloadItems(target, rel)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"retried": n})
}

// auditOffloadStuck is the alert. OPERATIONAL, like broadcast.fault, so it
// carries a coalescing Key: keyed by target, a bucket that refuses everything
// is one message, not one per file. Error is service error text and goes
// through the notifier's redaction like every other free-text field.
func auditOffloadStuck(ev offload.StuckEvent) alerts.Event {
	return alerts.Event{
		Type:     alerts.TypeOffloadStuck,
		Severity: alerts.SeverityWarning,
		Key:      fmt.Sprintf("offload.stuck:%d", ev.TargetID),
		Title:    fmt.Sprintf("%s: offload stopped retrying %s", ev.TargetName, ev.RelPath),
		Text:     "Nothing more is uploaded for this file until Retry now. The local copy is untouched. " + ev.Error,
	}.WithField("target", ev.TargetName).WithField("file", ev.RelPath)
}

// OffloadStuck fans one stuck item out to every surface a failure is raised
// on: the alert notifier (and through it the events stream's alert row), the
// lifecycle webhook offload.stuck, and the events stream's offload.stuck.
// MQTT carries telemetry snapshots, not events, so there is no MQTT message.
func (s *Server) OffloadStuck(ev offload.StuckEvent) {
	reason := fmt.Sprintf("%s: %s gave up after %d attempt(s): %s", ev.TargetName, ev.RelPath, ev.Attempts, ev.Error)
	s.publishAudit(auditOffloadStuck(ev))
	hev := hooks.Event{Trigger: hooks.TriggerOffloadStuck, Reason: reason, At: time.Now()}
	if s.hooks != nil {
		s.hooks.Publish(hev)
	}
	if s.stream != nil {
		s.stream.Publish(eventstream.FromHook(hev))
	}
}
