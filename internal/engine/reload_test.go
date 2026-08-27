package engine

// The classification guard.
//
// Every silent no-op this repo has shipped had the same shape: a field was
// added to db.Settings, validated, stored, returned by the API, and never
// wired to anything that was already running. meters.intervalMs was the most
// recent. The remedy is not vigilance; it is that adding a field to
// db.Settings without saying what happens when it changes must not compile
// green.

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func TestEverySettingsFieldHasAReloadRule(t *testing.T) {
	var missing []string
	walkSettings(reflect.TypeOf(db.Settings{}), "", func(path string) {
		if _, ok := settingsReload[path]; !ok {
			missing = append(missing, path)
		}
	})

	for _, p := range missing {
		t.Errorf("db.Settings.%s has no entry in settingsReload. Say what happens "+
			"when an operator changes it while the stream is up: ClassLive (and name "+
			"the function that pushes it into the running process), ClassRespawn (and "+
			"name the signature function that notices), ClassRebind, or ClassOnDemand. "+
			"A field with no answer is a field that will be stored, reported as saved, "+
			"and ignored.", p)
	}
}

func TestNoReloadRuleNamesAFieldThatNoLongerExists(t *testing.T) {
	known := map[string]bool{}
	walkSettings(reflect.TypeOf(db.Settings{}), "", func(path string) { known[path] = true })

	for path := range settingsReload {
		if !known[path] {
			t.Errorf("settingsReload names %q, which is not a field of db.Settings. "+
				"A stale rule is worse than none: it reads as a decision somebody made "+
				"about behaviour that no longer exists.", path)
		}
	}
}

// A rule's Applies field is a claim about the code, so it has to be checkable.
// A ClassLive entry that names a function nobody wrote is the same lie as no
// entry at all, dressed up as a decision.
func TestEveryReloadRuleNamesAFunctionThatExists(t *testing.T) {
	// The packages a settings value can be delivered by. internal/api carries
	// the out-of-band appliers (chat retention, automod, alert retry, the jobs
	// policy endpoint), and cmd/polyemesis carries the startup-only ones -- the
	// ClassNextStart rules point there, which is precisely what makes them
	// startup-only.
	dirs := []string{
		filepath.Join("."),
		filepath.Join("..", "playout"),
		filepath.Join("..", "recording"),
		filepath.Join("..", "api"),
		filepath.Join("..", "supervisor"),
		filepath.Join("..", "srtserver"),
		filepath.Join("..", "mqtt"),
		filepath.Join("..", "jobs"),
		filepath.Join("..", "chat"),
		// The display time zone is delivered by logtz.Set -- from the settings
		// handler on a save, and from main at boot. It is a delivery package
		// like the others here, so a rule naming it has to be checkable.
		filepath.Join("..", "logtz"),
		filepath.Join("..", "..", "cmd", "polyemesis"),
	}
	src := readGoSources(t, dirs)

	for path, rule := range settingsReload {
		if rule.Applies == "" {
			t.Errorf("settingsReload[%q] names no function. Every rule must say which "+
				"function carries the change, or it is documentation rather than a "+
				"decision anybody can check.", path)
			continue
		}
		if rule.Why == "" {
			t.Errorf("settingsReload[%q] has no reason. The reason is what a reviewer "+
				"reads when they disagree with the class.", path)
		}
		re := regexp.MustCompile(`func (\([^)]*\) )?` + regexp.QuoteMeta(rule.Applies) + `\b`)
		if !re.MatchString(src) {
			t.Errorf("settingsReload[%q] names %s, which is not defined in any package "+
				"a settings value can be delivered by. Either the function was renamed "+
				"and the rule was not, or the delivery was never written.",
				path, rule.Applies)
		}
	}
}

// The two fields this work moved. Pinned by name so a future edit that quietly
// puts either back into a signature has to argue with a test.
func TestTheFieldsThisWorkMadeLiveAreClassifiedLive(t *testing.T) {
	for _, path := range []string{"meters.intervalMs", "destinations.staggerMs"} {
		if got := settingsReload[path].Class; got != ClassLive {
			t.Errorf("settingsReload[%q].Class = %q, want %q", path, got, ClassLive)
		}
	}
}

// walkSettings visits every leaf json path in a struct tree.
//
// Modelled on walk() in internal/db/settings_drift_test.go rather than shared
// with it: that one is a test helper in another package, and exporting it to
// reach across would make a production package depend on a test one.
func walkSettings(rt reflect.Type, prefix string, visit func(path string)) {
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		return
	}
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}

		ft := f.Type
		for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice || ft.Kind() == reflect.Map {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "time" && ft.Name() != "Time" {
			walkSettings(ft, path, visit)
			continue
		}
		visit(path)
	}
}

func readGoSources(t *testing.T, dirs []string) string {
	t.Helper()
	var b strings.Builder
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("cannot read %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("cannot read %s: %v", e.Name(), err)
			}
			b.Write(raw)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// A reconcile that restarts a live output must say so. "Saved" is a statement
// about the database; an operator watching a card go grey needs to know it was
// their edit that did it, and an operator whose card did NOT go grey needs to
// know the edit still landed.
func TestAReconcileRecordsWhatItRestartedAndWhatItAppliedLive(t *testing.T) {
	rec := newReloadRecorder()
	rec.note("destination", "twitch", reloadRestart, "the routing graph changed")
	rec.note("destination", "youtube", reloadLive, "reconnect policy retuned")

	got := rec.snapshot()
	if len(got) != 2 {
		t.Fatalf("notes = %d, want 2", len(got))
	}
	if got[0].Action != reloadRestart || got[0].Name != "twitch" {
		t.Errorf("first note = %+v, want the restart of twitch", got[0])
	}
	if got[1].Action != reloadLive {
		t.Errorf("second note = %+v, want a live apply", got[1])
	}
}

// Notes raised outside a reconcile -- the preview idling out, a storage guard
// halting the recorder -- must not accumulate into the next settings save's
// report. They are not consequences of anything the operator just did.
// Asserting on LastReload alone would be vacuous, and was: noteReload writes to
// reloadRec while LastReload reads lastReload, so with no reconcile ever run the
// second is nil no matter what the first did. A version of this test that only
// checked LastReload passed against a noteReload that recorded everything.
//
// So it checks the mechanism: no recorder may exist outside a reconcile, and a
// reconcile's report may contain only what was raised during it.
func TestNotesRaisedOutsideAReconcileAreDropped(t *testing.T) {
	e := &Engine{log: testLogger()}

	e.noteReload("preview", "preview", reloadRestart, "idle timeout")
	if r := e.reloadRec.Load(); r != nil {
		t.Fatalf("noteReload installed a recorder with no reconcile in flight: %+v",
			r.snapshot())
	}
	if got := e.LastReload().Notes; len(got) != 0 {
		t.Fatalf("notes = %+v, want none: nothing was reconciling, so nothing an "+
			"operator did caused this", got)
	}

	// And the next reconcile must not inherit it. This is the half that matters
	// -- an operator saving a log level must not be told their edit stopped a
	// recording the storage guard halted an hour earlier.
	rec := newReloadRecorder()
	e.reloadRec.Store(rec)
	e.noteReload("destination", "twitch", reloadLive, "policy retuned")
	e.reloadRec.Store(nil)

	notes := rec.snapshot()
	if len(notes) != 1 || notes[0].Name != "twitch" {
		t.Fatalf("the reconcile's report = %+v, want only the note raised during it", notes)
	}
}
