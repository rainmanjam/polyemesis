package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// Follow-ups to recurring templates (#18) from the wave 3 re-judge. Each test names its finding.

// #7: a template that did not choose privacy (stored as public) never widens the destination's own compliance
// privacy, so a private destination's look-ahead events are not public for days.
func TestATemplateNeverWidensTheDestinationsCompliancePrivacy(t *testing.T) {
	f := newRecurringFixture(t)
	f.dest.Compliance.Privacy = db.PrivacyPrivate
	if _, err := f.store.UpdateDestination(f.dest); err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}
	f.attachTemplate(t)
	f.sweep(t)
	if len(f.fake.edits) == 0 {
		t.Fatal("no event was titled")
	}
	for _, e := range f.fake.edits {
		if e.Privacy != "private" {
			t.Errorf("event written with privacy %q; the destination is private", e.Privacy)
		}
	}
}

// #33: the first create in a walk mints the destination's stream, and a later create in the same walk
// must be handed that key, or a destination with its own ingest gets a second stream. The mirrored
// occurrence already exists here (created by an earlier sweep), so no mirror write refreshes the row
// between the two creates: the walk has to carry the key itself.
func TestALaterCreateInOneSweepIsHandedTheKeyTheFirstCreateMinted(t *testing.T) {
	f := newRecurringFixture(t)
	f.dest.StreamKey = ""
	if _, err := f.store.UpdateDestination(f.dest); err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}
	f.attachTemplate(t, func(tpl *scheduler.BroadcastTemplate) { tpl.Lookahead = 3 })
	first := time.Date(2026, 11, 3, 19, 0, 0, 0, time.UTC)
	id, err := f.store.ClaimOccurrence(scheduler.Occurrence{ScheduleID: f.schedule, DestinationID: f.dest.ID, At: first})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.store.ResolveOccurrence(id, "earlier-event", "Earlier"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	f.sweep(t)
	if len(f.fake.creates) != 2 {
		t.Fatalf("creates = %d, want 2 (the 10th and the 17th)", len(f.fake.creates))
	}
	if got := f.fake.creates[1].HeldKey; got != "key-yt-occ-1" {
		t.Errorf("the second create held %q, want the first create's key-yt-occ-1", got)
	}
}

// #39: an event created and then not recorded must not be left behind as a claim that ages into a
// duplicate. Here the row is gone by the time it is written, so the event itself is cancelled.
func TestAnEventWhoseRecordCannotBeWrittenIsNotLeftUnnamed(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t, func(tpl *scheduler.BroadcastTemplate) { tpl.Lookahead = 1 })
	if err := f.store.SetBroadcastTemplateThumbnail(f.schedule, []byte("\x89PNG\r\n\x1a\nx"), "image/png"); err != nil {
		t.Fatalf("thumbnail: %v", err)
	}
	f.fake.onThumb = func() {
		// Runs under the fake's lock, so the store is reached directly.
		rows, _ := f.store.ListOpenOccurrences()
		for _, o := range rows {
			_ = f.store.DeleteOccurrence(o.ID)
		}
	}
	f.sweep(t)
	if got := f.fake.cancelled(); len(got) != 1 || got[0] != "yt-occ-1" {
		t.Errorf("cancelled = %v; the unrecorded event yt-occ-1 must be removed", got)
	}
}

// #56: the rollback cancel after a refused title gets a context of its own, not the create's,
// which may be the one that ran out.
func TestTheRollbackCancelRunsOnAFreshContext(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t, func(tpl *scheduler.BroadcastTemplate) { tpl.Lookahead = 1 })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.fake.editErr = errors.New("refused")
	f.fake.onEdit = cancel
	f.s.recurringOnce(ctx, f.now)
	if len(f.fake.cancelCtxErr) != 1 || f.fake.cancelCtxErr[0] != nil {
		t.Errorf("rollback cancel contexts = %v, want one live context", f.fake.cancelCtxErr)
	}
}

// #15: a platform refusal can carry a credential, and the log line naming it is redacted.
func TestARefusedCreateIsLoggedRedacted(t *testing.T) {
	f := newRecurringFixture(t)
	var logs bytes.Buffer
	f.s.log = slog.New(slog.NewTextHandler(&logs, nil))
	f.attachTemplate(t)
	f.fake.ingestErr = errors.New("refused: rtmp://a.rtmp.youtube.com/live2/" + runTokenFixture())
	f.sweep(t)
	if !strings.Contains(logs.String(), "could not create the event") {
		t.Fatalf("the refusal was not logged at all, so this test examined nothing:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), runTokenFixture()) {
		t.Errorf("the log carries the credential:\n%s", logs.String())
	}
}

// planUpcoming records one upcoming occurrence for the HTTP fixture and returns its id.
func planUpcoming(t *testing.T, f *recurringHTTP, at time.Time) int64 {
	t.Helper()
	id, err := f.store.ClaimOccurrence(scheduler.Occurrence{ScheduleID: f.schedule, DestinationID: f.dest.ID,
		At: at, State: scheduler.OccurrencePending})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.store.ResolveOccurrence(id, "yt-occ-1", "old"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return id
}

// #15: the skip's 502 body is redacted.
func TestASkipThePlatformRefusesAnswersARedacted502(t *testing.T) {
	f := newRecurringHTTP(t)
	id := planUpcoming(t, f, time.Now().Add(72*time.Hour).Truncate(time.Hour))
	f.fake.cancelErr = errors.New("refused: rtmp://a.rtmp.youtube.com/live2/" + runTokenFixture())
	body := send(t, f.h, f.sign, http.MethodPost, "/api/v1/recurring/occurrences/"+strconv.FormatInt(id, 10)+"/skip",
		nil, http.StatusBadGateway)
	if strings.Contains(string(body), runTokenFixture()) {
		t.Errorf("the 502 body carries the credential: %s", body)
	}
}

// #15: the retitle's 502 body is redacted.
func TestARetitleThePlatformRefusesAnswersARedacted502(t *testing.T) {
	f := newRecurringHTTP(t)
	send(t, f.h, f.sign, http.MethodPut, "/api/v1/schedules/"+strconv.FormatInt(f.schedule, 10)+"/template",
		templateBody(), http.StatusOK)
	id := planUpcoming(t, f, time.Now().Add(72*time.Hour).Truncate(time.Hour))
	f.fake.editErr = errors.New("refused: rtmp://a.rtmp.youtube.com/live2/" + runTokenFixture())
	body := send(t, f.h, f.sign, http.MethodPost, "/api/v1/recurring/occurrences/"+strconv.FormatInt(id, 10)+"/title",
		map[string]any{"title": "New"}, http.StatusBadGateway)
	if strings.Contains(string(body), runTokenFixture()) {
		t.Errorf("the 502 body carries the credential: %s", body)
	}
}

// #40: a retitle is refused, as a skip is, for a show that has started and for a destination on air.
func TestARetitleIsRefusedForAStartedShowAndForALiveDestination(t *testing.T) {
	f := newRecurringHTTP(t)
	send(t, f.h, f.sign, http.MethodPut, "/api/v1/schedules/"+strconv.FormatInt(f.schedule, 10)+"/template",
		templateBody(), http.StatusOK)
	past := planUpcoming(t, f, time.Now().Add(-time.Hour).Truncate(time.Minute))
	send(t, f.h, f.sign, http.MethodPost, "/api/v1/recurring/occurrences/"+strconv.FormatInt(past, 10)+"/title",
		map[string]any{"title": "Late"}, http.StatusConflict)

	if err := f.store.DeleteOccurrence(past); err != nil {
		t.Fatalf("DeleteOccurrence: %v", err)
	}
	future := planUpcoming(t, f, time.Now().Add(72*time.Hour).Truncate(time.Hour))
	f.dest.Enabled = true
	if _, err := f.store.UpdateDestination(f.dest); err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}
	send(t, f.h, f.sign, http.MethodPost, "/api/v1/recurring/occurrences/"+strconv.FormatInt(future, 10)+"/title",
		map[string]any{"title": "On air"}, http.StatusConflict)
	if len(f.fake.edits) != 0 {
		t.Errorf("a refused retitle still wrote %d edits", len(f.fake.edits))
	}
}

// #49/#50: a picture one byte over the limit is a 413, not the store's 500; one at the limit is
// stored; and a body that breaks off is not reported as too large.
func TestAThumbnailIsRefusedOneByteOverTheLimitAndAcceptedAtIt(t *testing.T) {
	f := newRecurringHTTP(t)
	base := "/api/v1/schedules/" + strconv.FormatInt(f.schedule, 10)
	send(t, f.h, f.sign, http.MethodPut, base+"/template", templateBody(), http.StatusOK)
	png := func(n int) []byte {
		b := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, n-8)...)
		return b
	}
	put := func(body io.Reader) int {
		r := httptest.NewRequest(http.MethodPut, base+"/template/thumbnail", body)
		r.Header.Set("Content-Type", "image/png")
		r.RemoteAddr = "203.0.113.5:44444"
		f.sign(r)
		return do(t, f.h, r).Code
	}
	if got := put(bytes.NewReader(png(scheduler.MaxThumbnailBytes + 1))); got != http.StatusRequestEntityTooLarge {
		t.Errorf("one byte over the limit got %d, want 413", got)
	}
	if got := put(bytes.NewReader(png(scheduler.MaxThumbnailBytes))); got != http.StatusNoContent {
		t.Errorf("exactly the limit got %d, want 204", got)
	}
	if got := put(io.MultiReader(bytes.NewReader(png(64)), errReader{})); got == http.StatusRequestEntityTooLarge {
		t.Errorf("a body that broke off was reported as too large")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }
