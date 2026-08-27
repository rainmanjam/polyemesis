package api

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// The drift guard for zero timestamps on the wire.
//
// `json:",omitempty"` DOES NOTHING ON A time.Time. encoding/json's emptiness
// test has no case for a struct, so the option is silently inert and an unset
// instant marshals as "0001-01-01T00:00:00Z". That is a NON-EMPTY string which
// parses cleanly, so every client guard of the form `x.someAt && render(x)`
// passes -- and through a local-time offset the browser prints 12/31/1,
// 16:07:02.
//
// This cost the same bug SIX TIMES over: hooks.Stats.LastSent, alerts.Stats
// .LastSent, jobs.Job's availableAt/startedAt/finishedAt (three at once on
// every queued row), db.Source.PrevTokenUntil, db.FacebookSettings
// .ScheduledFor and expertArgs.UpdatedAt. Every one of them carried a tag
// claiming to prevent exactly what was happening, which is why nobody looked
// again -- the field's own declaration was the alibi.
//
// So the tag is now checked mechanically. Go 1.24's `omitzero` calls IsZero and
// drops the key, which is what `omitempty` was always assumed to do.
//
// WHY THE TAG AND NOT THE OUTPUT: the obvious alternative is to marshal a
// zero-valued root and fail on any "0001-01-01" in the bytes. It cannot work.
// Fields like createdAt and updatedAt are ALWAYS assigned before anything is
// served, so a zero root reports them as violations, and suppressing that needs
// a hand-maintained table of "which timestamps are always set" -- exactly the
// judgement call this guard exists to avoid having to make. Whether a field can
// legitimately be unset is not decidable from the type. Whether its tag tells
// the truth is. This checks the second, which is the whole of the defect: no
// time.Time may claim `omitempty`.
//
// It does NOT catch a time.Time with no option at all that nothing ever
// assigns -- db.Recording.FinishedAt was that shape. Nothing static can, since
// it turns on what the writers do. That one was found by reading the writers
// and is fixed; this guard holds the line that a NEW field cannot re-enter the
// class through the tag that made the other six invisible.

// timeTagExemptions are the (type.field) pairs allowed to keep `omitempty` on a
// time.Time, each with the reason it is harmless. Empty is the goal state; an
// entry here is a promise that something else suppresses the zero.
//
// Listed rather than skipped implicitly: a type that hand-writes MarshalJSON
// makes its struct tags inert, so the guard would pass it either way -- and a
// silent pass is how this class hid in the first place.
var timeTagExemptions = map[string]string{
	"hooks.Stats.LastSent": "Stats has a hand-written MarshalJSON (internal/hooks/dispatch.go) " +
		"that shadows the field with a *time.Time and only sets it when non-zero. " +
		"A pointer on the Go type would break automation.go, which folds per-hook " +
		"stats together with .After().",
}

// timeRoots is every type this server marshals to a browser that is worth
// walking. The walk is recursive, so a nested struct does not need its own
// entry -- only a type reachable from no other root does.
//
// db.TranscriptQuery IS DELIBERATELY ABSENT, and it does carry `omitempty` on
// two time.Time fields (Since, Until). It is a REQUEST type: searchQuery in
// library.go builds it from an *http.Request and nothing ever marshals one
// back. A zero there is a filter that was not supplied, it never reaches a
// browser, and the tag is inert rather than harmful. Listing it would force a
// wire-shape change on a struct that has no wire. If it ever becomes a
// response, it belongs here and needs `omitzero` first.
func timeRoots() []any {
	return []any{
		db.Settings{},
		db.Destination{},
		db.Source{},
		db.Recording{},
		db.Session{},
		db.User{},
		db.APIToken{},
		db.Transcript{},
		db.TranscriptHit{},
		db.AnnouncementSet{},
		db.BroadcastControl{},
		db.Rendition{},
		jobs.Job{},
		jobs.Snapshot{},
		jobs.Stats{},
		hooks.Hook{},
		hooks.Stats{},
		hooks.DeliveryRecord{},
		hooks.Envelope{},
		alerts.Rule{},
		alerts.Stats{},
		alerts.Snapshot{},
		alerts.Delivery{},
		expertArgs{},
		expertResponse{},
	}
}

func TestNoTimeFieldClaimsOmitemptyOnTheWire(t *testing.T) {
	var found []string
	seen := map[reflect.Type]bool{}
	for _, root := range timeRoots() {
		walkTimeFields(reflect.TypeOf(root), seen, &found)
	}
	sort.Strings(found)

	var bad []string
	for _, f := range found {
		if _, ok := timeTagExemptions[f]; !ok {
			bad = append(bad, f)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("time.Time fields tagged `omitempty`, which does NOTHING and "+
			"serves 0001-01-01T00:00:00Z to the browser -- use `omitzero`:\n  %s",
			strings.Join(bad, "\n  "))
	}

	// The exemption table must not outlive what it exempts, or it becomes a
	// place a real violation can be parked and forgotten.
	inFound := map[string]bool{}
	for _, f := range found {
		inFound[f] = true
	}
	for name := range timeTagExemptions {
		if !inFound[name] {
			t.Errorf("timeTagExemptions still lists %s, which no longer carries "+
				"`omitempty` on a time.Time -- delete the entry", name)
		}
	}
}

// TestTimeRootsAreReallyWalked guards the guard: a typo in a root, or a walk
// that silently bottoms out, turns the test above into one that passes because
// it inspected nothing.
func TestTimeRootsAreReallyWalked(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var found []string
	for _, root := range timeRoots() {
		walkTimeFields(reflect.TypeOf(root), seen, &found)
	}
	// Every root must have been REACHED. A root counted by "did this call add
	// anything to seen" would be wrong: seen is shared, so a root that another
	// root already contains as a nested field adds nothing and is still walked.
	for _, root := range timeRoots() {
		rt := reflect.TypeOf(root)
		if !seen[rt] {
			t.Errorf("root %s was never inspected -- is it a struct?", rt)
		}
	}
	// jobs.Job is the type this class cost the most on. If the walk cannot
	// reach its timestamps, it cannot have checked anything.
	if !seen[reflect.TypeOf(jobs.Job{})] {
		t.Fatal("walk never reached jobs.Job")
	}
}

var timeType = reflect.TypeOf(time.Time{})

// walkTimeFields records "pkg.Type.Field" for every time.Time field whose json
// tag carries the inert `omitempty` option.
func walkTimeFields(t reflect.Type, seen map[reflect.Type]bool, found *[]string) {
	for t != nil && (t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice ||
		t.Kind() == reflect.Array || t.Kind() == reflect.Map) {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct || seen[t] || t == timeType {
		return
	}
	seen[t] = true

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		// encoding/json ignores unexported fields, so they cannot reach a
		// browser and cannot carry this defect.
		if f.PkgPath != "" && !f.Anonymous {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		opts := ""
		if i := strings.Index(tag, ","); i >= 0 {
			opts = tag[i+1:]
		}
		if f.Type == timeType && hasOpt(opts, "omitempty") {
			*found = append(*found, fmt.Sprintf("%s.%s.%s", shortPkg(t), t.Name(), f.Name))
			continue
		}
		walkTimeFields(f.Type, seen, found)
	}
}

func hasOpt(opts, want string) bool {
	for _, o := range strings.Split(opts, ",") {
		if strings.TrimSpace(o) == want {
			return true
		}
	}
	return false
}

func shortPkg(t reflect.Type) string {
	p := t.PkgPath()
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// TestZeroTimestampsNeverReachTheBrowser is the behavioural half: the six
// fields this swept are marshalled from their zero value and the key must be
// absent, not present-and-first-century. The tag check above would pass if
// `omitzero` were ever mis-spelled, since a bad option is silently ignored by
// encoding/json exactly like the bug that started this.
func TestZeroTimestampsNeverReachTheBrowser(t *testing.T) {
	cases := []struct {
		name string
		val  any
		key  string
	}{
		{"jobs.Job availableAt", jobs.Job{}, "availableAt"},
		{"jobs.Job startedAt", jobs.Job{}, "startedAt"},
		{"jobs.Job finishedAt", jobs.Job{}, "finishedAt"},
		{"db.Recording finishedAt", db.Recording{}, "finishedAt"},
		{"db.Source prevTokenUntil", db.Source{}, "prevTokenUntil"},
		{"db.FacebookSettings scheduledFor", db.FacebookSettings{}, "scheduledFor"},
		{"expertArgs updatedAt", expertArgs{}, "updatedAt"},
		{"alerts.Stats lastSent", alerts.Stats{}, "lastSent"},
		{"hooks.Stats lastSent", hooks.Stats{}, "lastSent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.val)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			// ONLY the named key. NOT `strings.Contains(b, "0001-01-01")`:
			// a zero jobs.Job also has a zero createdAt and updatedAt, and
			// those are required fields that are always assigned before
			// anything is served. Asserting on the whole body fails on them
			// and says nothing about the field under test -- which is the
			// same reason the tag check above is the guard and a body scan is
			// not.
			if got, ok := m[tc.key]; ok {
				t.Fatalf("%s is present on a zero value as %v -- the browser renders that as 12/31/1, 16:07:02", tc.key, got)
			}
		})
	}
}
