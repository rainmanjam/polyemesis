package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// termFake is a Helix stub with state: a blocked-term list that grows when a
// term is added, and answers a duplicate with the existing term, as Twitch does.
type termFake struct {
	mu       sync.Mutex
	terms    map[string]BlockedTerm // by lower-cased text
	hidden   map[string]BlockedTerm // private: never listed, but a duplicate add returns it
	deleted  []string
	puts     []map[string]any
	failAdd  int // answer this status to adds once armed
	failFrom int // arm failAdd after this many adds
	adds     int
	// clock is Twitch's clock for a new term's created_at and the Date header;
	// nil means the local clock. whole stamps created_at in whole seconds, as
	// Helix does.
	clock  func() time.Time
	whole  bool
	auto   map[string]any
	nextID int
}

func newTermFake(t *testing.T) (*termFake, *TwitchAdapter) {
	t.Helper()
	f := &termFake{terms: map[string]BlockedTerm{}, hidden: map[string]BlockedTerm{}, auto: map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /moderation/blocked_terms":
			var data []BlockedTerm
			for _, b := range f.terms {
				data = append(data, b)
			}
			out := make([]map[string]string, 0, len(data))
			for _, b := range data {
				out = append(out, map[string]string{"id": b.ID, "text": b.Text, "created_at": b.CreatedAt})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": out, "pagination": map[string]string{}})
		case "POST /moderation/blocked_terms":
			f.adds++
			if f.failAdd != 0 && f.adds > f.failFrom {
				w.WriteHeader(f.failAdd)
				_, _ = w.Write([]byte(`{"error":"x","message":"twitch said no"}`))
				return
			}
			text, _ := body["text"].(string)
			key := strings.ToLower(text)
			b, ok := f.terms[key]
			if !ok {
				b, ok = f.hidden[key]
			}
			if !ok {
				f.nextID++
				b = BlockedTerm{ID: fmt.Sprintf("t%03d", f.nextID), Text: text, CreatedAt: f.stamp()}
				f.terms[key] = b
			}
			w.Header().Set("Date", f.now().UTC().Format(http.TimeFormat))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": b.ID, "text": b.Text, "created_at": b.CreatedAt}}})
		case "DELETE /moderation/blocked_terms":
			id := r.URL.Query().Get("id")
			f.deleted = append(f.deleted, id)
			for k, b := range f.terms {
				if b.ID == id {
					delete(f.terms, k)
				}
			}
			w.WriteHeader(http.StatusNoContent)
		case "GET /moderation/automod/settings":
			_, _ = w.Write([]byte(`{"data":[{"overall_level":null,"aggression":1,"bullying":3,"disability":0,"misogyny":0,"race_ethnicity_or_religion":0,"sex_based_terms":0,"sexuality_sex_or_gender":0,"swearing":4}]}`))
		case "PUT /moderation/automod/settings":
			f.puts = append(f.puts, body)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{body}})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	return f, twitchForDelete(t, srv.URL)
}

func (f *termFake) now() time.Time {
	if f.clock != nil {
		return f.clock()
	}
	return time.Now()
}

func (f *termFake) stamp() string {
	n := f.now().UTC()
	if f.whole {
		return n.Truncate(time.Second).Format(time.RFC3339)
	}
	return n.Format(time.RFC3339Nano)
}

func (f *termFake) seed(id, text, at string) {
	f.terms[strings.ToLower(text)] = BlockedTerm{ID: id, Text: text, CreatedAt: at}
}

func TestOursNeedsAnIDAndACreationTimeCloseToTwitchsAnswer(t *testing.T) {
	ref := time.Date(2026, 11, 7, 12, 0, 10, 0, time.UTC)
	cases := []struct {
		name string
		b    BlockedTerm
		want bool
	}{
		{"created just now", BlockedTerm{ID: "1", CreatedAt: "2026-11-07T12:00:10Z"}, true},
		{"created a second ago (whole-second stamp)", BlockedTerm{ID: "1", CreatedAt: "2026-11-07T12:00:09Z"}, true},
		{"created at the edge", BlockedTerm{ID: "1", CreatedAt: "2026-11-07T12:00:05Z"}, true},
		{"created long before", BlockedTerm{ID: "1", CreatedAt: "2026-11-07T11:59:00Z"}, false},
		{"no id", BlockedTerm{CreatedAt: "2026-11-07T12:00:10Z"}, false},
		{"no time", BlockedTerm{ID: "1"}, false},
		{"unreadable time", BlockedTerm{ID: "1", CreatedAt: "yesterday"}, false},
	}
	for _, c := range cases {
		if got := ours(c.b, ref); got != c.want {
			t.Errorf("%s: ours = %v, want %v", c.name, got, c.want)
		}
	}
}

// Helix stamps created_at in whole seconds. A sync that starts at 12:00:00.7
// and adds a term at 12:00:00.9 reads 12:00:00Z; that must still be ours.
func TestSyncTrackedRecordsTermsStampedInWholeSeconds(t *testing.T) {
	f, a := newTermFake(t)
	f.whole = true
	res, err := a.SyncTrackedTerms(context.Background(), []string{"aa", "bb", "cc"}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 3 || len(res.AddedIDs) != 3 || res.Present != 0 {
		t.Fatalf("terms this sync created were not recorded: %+v", res)
	}
}

// Twitch's clock is not ours. Whichever way it is off, the terms the sync
// created are recorded, and a term that really is old is still not.
func TestSyncTrackedIsIndependentOfTheLocalClock(t *testing.T) {
	for _, off := range []time.Duration{-2 * time.Hour, -3 * time.Second, 3 * time.Second, 2 * time.Hour} {
		f, a := newTermFake(t)
		f.whole = true
		f.clock = func() time.Time { return time.Now().Add(off) }
		f.hidden["private one"] = BlockedTerm{ID: "priv", Text: "private one", CreatedAt: f.now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)}
		res, err := a.SyncTrackedTerms(context.Background(), []string{"aa", "bb", "private one"}, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if res.Added != 2 || res.Present != 1 || len(res.AddedIDs) != 2 {
			t.Errorf("offset %v: %+v", off, res)
		}
		if _, mine := res.AddedIDs["priv"]; mine {
			t.Errorf("offset %v: a private term was recorded as ours", off)
		}
	}
}

func TestSyncTrackedRecordsOnlyTermsItCreated(t *testing.T) {
	f, a := newTermFake(t)
	f.seed("op1", "Operators", "2020-01-01T00:00:00Z")
	// A private term: not in the list, but a duplicate add returns it.
	f.hidden["private one"] = BlockedTerm{ID: "priv", Text: "private one", CreatedAt: "2020-01-01T00:00:00Z"}

	res, err := a.SyncTrackedTerms(context.Background(),
		[]string{"fresh", "operators", "private one", "wild*", "x", "fresh"}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Present != 2 || res.Skipped != 2 || !res.Done || res.Remaining != 0 {
		t.Fatalf("res = %+v", res)
	}
	if len(res.AddedIDs) != 1 {
		t.Fatalf("recorded %v: only the term this sync created is ours", res.AddedIDs)
	}
	for id, term := range res.AddedIDs {
		if id == "op1" || id == "priv" || term != TermKey("fresh") {
			t.Fatalf("recorded someone else's term: %q=%q", id, term)
		}
	}
	if res.SkipWhy["wildcard"] != 1 || res.SkipWhy["length"] != 1 {
		t.Fatalf("skipWhy = %v", res.SkipWhy)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("a first sync deleted %v", f.deleted)
	}
}

func TestSyncTrackedIsResumableInBatches(t *testing.T) {
	f, a := newTermFake(t)
	want := []string{"aa", "bb", "cc", "dd", "ee"}
	rec := map[string]string{}
	var runs int
	for {
		runs++
		res, err := a.SyncTrackedTerms(context.Background(), want, rec, 2)
		if err != nil {
			t.Fatal(err)
		}
		for id, key := range res.AddedIDs {
			rec[id] = key
		}
		if res.Done {
			break
		}
		if res.Remaining <= 0 || runs > 5 {
			t.Fatalf("not converging: %+v", res)
		}
	}
	if runs != 3 || len(rec) != 5 || len(f.terms) != 5 {
		t.Fatalf("runs=%d recorded=%d on twitch=%d", runs, len(rec), len(f.terms))
	}
	// Run again: nothing to do, nothing re-recorded.
	res, err := a.SyncTrackedTerms(context.Background(), want, rec, 2)
	if err != nil || !res.Done || res.Added != 0 || res.Present != 5 || len(res.AddedIDs) != 0 {
		t.Fatalf("idempotent re-run = %+v err=%v", res, err)
	}
}

func TestSyncTrackedReconcilesDroppedTermsAndOnlyThose(t *testing.T) {
	f, a := newTermFake(t)
	f.seed("mine1", "keepme", "2026-01-01T00:00:00Z")
	f.seed("mine2", "dropme", "2026-01-01T00:00:00Z")
	f.seed("theirs", "operatorword", "2026-01-01T00:00:00Z")
	rec := map[string]string{"mine1": TermKey("keepme"), "mine2": TermKey("dropme"), "gone": TermKey("vanished")}

	res, err := a.SyncTrackedTerms(context.Background(), []string{"keepme", "operatorword", "vanished"}, rec, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "mine2" {
		t.Fatalf("deleted %v: only the recorded term no longer wanted goes", f.deleted)
	}
	// "vanished" was ours and the operator removed it: forgotten, then re-added
	// under a new id. "operatorword" is wanted, present and not ours.
	forgot := strings.Join(res.RemovedIDs, ",")
	if !strings.Contains(forgot, "gone") || !strings.Contains(forgot, "mine2") || strings.Contains(forgot, "theirs") {
		t.Fatalf("removedIDs = %v", res.RemovedIDs)
	}
	if res.Removed != 1 || res.Added != 1 || res.Restored != 1 || res.Present != 2 || !res.Done {
		t.Fatalf("res = %+v", res)
	}
}

func TestSyncTrackedReportsPartialWorkOnAFailure(t *testing.T) {
	f, a := newTermFake(t)
	f.failAdd, f.failFrom = http.StatusTooManyRequests, 2
	res, err := a.SyncTrackedTerms(context.Background(), []string{"aa", "bb", "cc", "dd"}, nil, 0)
	if err == nil || !strings.Contains(err.Error(), "stopped after adding 2 of 4") {
		t.Fatalf("err = %v", err)
	}
	if len(res.AddedIDs) != 2 || res.Remaining != 2 || res.Done {
		t.Fatalf("the two accepted terms must still be reported: %+v", res)
	}
	// The list read failing is reported before anything is touched.
	_, a2 := newHelixFake(t, map[string]fakeReply{"GET /moderation/blocked_terms": {401, errBody}})
	if _, err := a2.SyncTrackedTerms(context.Background(), []string{"aa"}, nil, 0); err == nil {
		t.Fatal("list failure swallowed")
	}
}

func TestSyncTrackedStopsNearTheDeadlineWithoutAnError(t *testing.T) {
	f, a := newTermFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), twitchSyncMargin/2)
	defer cancel()
	res, err := a.SyncTrackedTerms(ctx, []string{"aa", "bb", "cc"}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Done || res.Remaining != 3 || f.adds != 0 {
		t.Fatalf("res=%+v adds=%d: a call with no time left must add nothing and say how much is left", res, f.adds)
	}
}

func TestRemoveTrackedDeletesExactlyTheRecordedIDs(t *testing.T) {
	f, a := newTermFake(t)
	f.seed("m1", "one", "2026-01-01T00:00:00Z")
	f.seed("m2", "two", "2026-01-01T00:00:00Z")
	f.seed("op", "operatorword", "2026-01-01T00:00:00Z")
	res, err := a.RemoveTrackedTerms(context.Background(), map[string]string{"m1": TermKey("one"), "m2": TermKey("two")})
	if err != nil || !res.Done || res.Removed != 2 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if strings.Join(f.deleted, ",") != "m1,m2" || len(f.terms) != 1 {
		t.Fatalf("deleted %v; left %v", f.deleted, f.terms)
	}

	// Near the deadline it stops and says how many are left.
	ctx, cancel := context.WithTimeout(context.Background(), twitchSyncMargin/2)
	defer cancel()
	res, err = a.RemoveTrackedTerms(ctx, map[string]string{"x1": TermKey("x"), "x2": TermKey("y")})
	if err != nil || res.Done || res.Remaining != 2 {
		t.Fatalf("res=%+v err=%v", res, err)
	}

	// A Twitch refusal is reported with how far it got.
	_, bad := newHelixFake(t, map[string]fakeReply{"DELETE /moderation/blocked_terms": {403, errBody}})
	res, err = bad.RemoveTrackedTerms(context.Background(), map[string]string{"x1": TermKey("x")})
	if err == nil || res.Done || res.Remaining != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestRaiseAutoModLevelRaisesNeverLowersAndKeepsSwearing(t *testing.T) {
	f, a := newTermFake(t)
	before, after, err := a.RaiseAutoModLevel(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if before.Bullying != 3 || before.Swearing != 4 {
		t.Fatalf("before = %+v", before)
	}
	if len(f.puts) != 1 {
		t.Fatalf("puts = %v", f.puts)
	}
	put := f.puts[0]
	if _, has := put["overall_level"]; has {
		t.Fatal("the overall level would reset swearing; the PUT must send the categories")
	}
	want := map[string]float64{"aggression": 2, "bullying": 3, "disability": 2, "misogyny": 2,
		"race_ethnicity_or_religion": 2, "sex_based_terms": 2, "sexuality_sex_or_gender": 2, "swearing": 4}
	for k, v := range want {
		if got, ok := put[k].(float64); !ok || got != v {
			t.Errorf("%s = %v, want %v", k, put[k], v)
		}
	}
	if after.Swearing != 4 || after.Bullying != 3 {
		t.Fatalf("after = %+v", after)
	}
	for _, bad := range []int{0, 3, -1} {
		if _, _, err := a.RaiseAutoModLevel(context.Background(), bad); err == nil {
			t.Errorf("level %d accepted", bad)
		}
	}
	_, bad := newHelixFake(t, map[string]fakeReply{"GET /moderation/automod/settings": {401, errBody}})
	if _, _, err := bad.RaiseAutoModLevel(context.Background(), 1); err == nil {
		t.Fatal("read failure swallowed")
	}
}
