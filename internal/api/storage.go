package api

// Storage targets and offload (Wave 4 #10): S3-compatible buckets finished
// recordings are copied to. Mirror mode only: no local file is moved or
// deleted.
//
// The secret access key goes in once, on create or update, is sealed by the
// database's secret box and is never returned: StorageTarget.SecretAccessKey
// is json:"-" and responses carry hasSecret. A read-scoped token additionally
// gets readSafeStorageTarget, which blanks the access key id. The Test route
// is session-only, like /automod/test, because it unseals the secret and
// sends a signed request out of the process.

import (
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
	OffloadStems       *bool   `json:"offloadStems"`
	OffloadTranscripts *bool   `json:"offloadTranscripts"`
	OffloadClips       *bool   `json:"offloadClips"`
	VerifyDownload     *bool   `json:"verifyDownload"`
	AllowPrivateTarget *bool   `json:"allowPrivateTarget"`
	Enabled            *bool   `json:"enabled"`
	MaxAttempts        *int    `json:"maxAttempts"`
}

func (q storageTargetRequest) applyTo(t db.StorageTarget) db.StorageTarget {
	str := func(dst, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	bl := func(dst, v *bool) {
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
	bl(&t.PathStyle, q.PathStyle)
	bl(&t.OffloadStems, q.OffloadStems)
	bl(&t.OffloadTranscripts, q.OffloadTranscripts)
	bl(&t.OffloadClips, q.OffloadClips)
	bl(&t.VerifyDownload, q.VerifyDownload)
	bl(&t.AllowPrivateTarget, q.AllowPrivateTarget)
	bl(&t.Enabled, q.Enabled)
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
	if err := validateEndpoint(t.Endpoint, t.AllowPrivateTarget); err != nil {
		return err
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
		saved.Bucket != next.Bucket || saved.AllowPrivateTarget != next.AllowPrivateTarget ||
		saved.PathStyle != next.PathStyle
}

// locationChanged reports whether an edit changes WHERE the target's objects
// live -- a different service, bucket, region, account or addressing. Such an
// edit sends every one of the target's files back to be uploaded at the new
// address, and an upload still running against the old one cannot write its
// result (db.RebindStorageTarget advances the binding generation).
func locationChanged(saved, next db.StorageTarget) bool {
	return normalEndpoint(saved.Endpoint) != normalEndpoint(next.Endpoint) || saved.Bucket != next.Bucket ||
		saved.Region != next.Region || saved.PathStyle != next.PathStyle || saved.AccessKeyID != next.AccessKeyID
}

// msgInvalidID is the 400 every storage route gives a path id that is not one.
const msgInvalidID = errInvalidID

// validateEndpoint refuses an endpoint that is not an http(s) URL with a
// host, one that carries credentials, a query or a fragment, and a literal
// private address the target did not opt into.
func validateEndpoint(endpoint string, allowPrivate bool) error {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.New("endpoint must be an http(s) URL, e.g. https://s3.us-east-1.amazonaws.com")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("endpoint must not carry credentials, a query or a fragment; credentials go in the access key fields")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !netguard.IsPublicAddr(ip) && !allowPrivate {
		return errors.New("endpoint is a private address; enable allowPrivateTarget to offload to a self-hosted target on purpose")
	}
	return nil
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
		writeError(w, http.StatusBadRequest, msgInvalidID)
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
	t := req.applyTo(db.StorageTarget{Provider: db.StorageProviderS3, Enabled: true,
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
		writeError(w, http.StatusBadRequest, msgInvalidID)
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
	if locationChanged(existing, t) {
		// Multipart progress made at the old address is read first, so it
		// can still be aborted there after the rebind drops it.
		pending, _ := s.store.ListOffloadMultipart(existing.ID)
		if err := s.store.RebindStorageTarget(s.box, t); err != nil {
			writeStoreError(w, err)
			return
		}
		if s.offload != nil {
			s.offload.AbortUploads(r.Context(), existing, pending)
		}
	} else if err := s.store.UpdateStorageTarget(s.box, t); err != nil {
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
		writeError(w, http.StatusBadRequest, msgInvalidID)
		return
	}
	t, err := s.store.GetStorageTarget(s.box, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Abandon its multipart uploads on the service while its credentials
	// can still be read; after the delete nothing could.
	//
	// The delete goes ahead whatever the abort managed -- a target whose keys
	// were revoked or whose bucket is gone must still be deletable -- and
	// what could not be aborted is said, with the key and upload id, because
	// the rows below are the last record of them and they keep billing until
	// the bucket's lifecycle rule removes them. With no offload service
	// nothing was even tried, so every recorded upload is reported.
	var left []db.OffloadMultipart
	if s.offload != nil {
		left = s.offload.AbortTarget(r.Context(), t)
	} else if rows, err := s.store.ListOffloadMultipart(id); err == nil {
		left = rows
	}
	if err := s.store.DeleteStorageTarget(id); err != nil {
		writeStoreError(w, err)
		return
	}
	resp := map[string]any{"status": "deleted"}
	if len(left) > 0 {
		uploads := make([]map[string]string, 0, len(left))
		for _, mp := range left {
			uploads = append(uploads, map[string]string{"key": mp.Key, "uploadId": mp.UploadID, "relPath": mp.RelPath})
			s.log.Warn("storage target deleted with an unfinished upload that could not be aborted",
				"target", t.Name, "key", mp.Key, "uploadId", mp.UploadID)
		}
		resp["warning"] = fmt.Sprintf("%s could not be cancelled and will keep billing until the bucket's "+
			"lifecycle rule removes %s. Abort %s with your provider (list-multipart-uploads, then "+
			"abort-multipart-upload) using the keys and upload ids below.",
			plural(len(left), "unfinished upload", "unfinished uploads"), wordFor(len(left), "it", "them"),
			wordFor(len(left), "it", "them"))
		resp["unabortedUploads"] = uploads
	}
	writeJSON(w, http.StatusOK, resp)
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
	base := db.StorageTarget{Provider: db.StorageProviderS3, Enabled: true,
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
	if off := s.offload; off != nil {
		off.Kick()
	}
}

// ---- Library integration ----

// recordingOffload is one recording's offload state, per target.
type recordingOffload struct {
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
}

func (s *Server) loadOffloadIndex() offloadIndex {
	idx := offloadIndex{byRel: map[string][]db.OffloadItem{}, names: map[int64]string{}}
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
		v.Targets = append(v.Targets, recordingOffloadOn{TargetID: it.TargetID, TargetName: idx.names[it.TargetID],
			State: it.State, Attempts: it.Attempts, LastError: it.LastError, VerifiedAt: it.VerifiedAt})
	}
	return v
}

// handleRetryStorageTarget is the target card's Retry now: every failed or
// stuck item on the target goes back in the queue with its attempts reset.
func (s *Server) handleRetryStorageTarget(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, msgInvalidID)
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
		writeError(w, http.StatusBadRequest, msgInvalidID)
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
