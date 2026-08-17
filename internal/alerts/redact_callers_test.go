package alerts

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// redactAllowlist is every file in the tree permitted to call alerts.Redact,
// with the reason it is allowed.
//
// THIS IS THE ENFORCEMENT MECHANISM FOR #162, in place of a type change.
//
// The alternatives were considered and rejected. A newtype (`Redact(Message)`)
// is castable, so it enforces nothing against the caller who is in a hurry --
// which is the only caller it would need to stop. Unexporting Redact and
// re-exporting a narrower name churns the sentinel-pinned supervisor argv path
// for no gain, because the residual pass still has to exist and still has the
// same limits. Deleting it is not available: the outbound payload paths and the
// supervisor residual both need it.
//
// What actually goes wrong is a NEW caller, added by somebody who read the name
// and not the doc, looping it over tokens -- the exact bug #150 removed. A list
// somebody has to edit, with a test that says why, puts a human in that loop.
//
// Adding an entry is FINE. It is not a wall, it is a doorbell. Read the doc on
// Redact first, satisfy yourself that the declared secrets are already gone via
// a SecretSet, and then add the file with a sentence saying so.
var redactAllowlist = map[string]string{
	// --- the definition and its own tests ---
	"internal/alerts/redact.go": "Redact is defined here; Event.Redacted is the outbound " +
		"payload pass, applied by Publish so a careless event builder cannot skip it.",
	"internal/alerts/redact_test.go":          "tests of Redact itself.",
	"internal/alerts/redact_residual_test.go": "pins the documented residuals; see the comments there.",
	"internal/alerts/webhook_disclosure_test.go": "asserts that Redact is a NO-OP on an https " +
		"webhook path, so nobody records \"also run Redact on LastError\" as the #160 fix.",

	// --- the supervisor residual, #150 ---
	"internal/supervisor/supervisor.go": "the RESIDUAL half of Process.scrub and CommandString. " +
		"The boundary in both is Spec.Secrets (exact literals), applied FIRST and PER " +
		"ELEMENT; Redact then runs once over the JOINED string, which is the only " +
		"application direction that is not strictly worse. Do not move it inside the " +
		"per-element loop.",

	// --- the debug-mode recorder ---
	"internal/diag/diag.go": "residual only, and applied AFTER the recorder's own " +
		"alerts.SecretSet -- Recorder.scrub is Redact(secrets.Scrub(s)), never Redact " +
		"alone. The set is the mechanism and is rebuilt whenever a destination's key " +
		"changes, because a rotated key is a literal the old set never held. It is a " +
		"WHOLE-STRING pass over a log message and over attribute keys and values, " +
		"never over argv elements -- the #150 shape -- and argv reaching a log line " +
		"has already been through supervisor.Spec.Secrets before it gets here. It runs " +
		"on the way IN so the ring never holds a plaintext credential: this buffer is " +
		"exported to somebody who does not have the box, so anything in it has left " +
		"the operator's control.",

	// --- outbound alert / hook payloads ---
	"internal/hooks/payload.go": "the outbound hook payload pass, the hooks-side twin of " +
		"alerts.Event.Redacted. Whole strings, never argv elements.",
	"internal/hooks/dispatch.go": "residual only. The boundary on this path is the per-worker " +
		"alerts.SecretSet seeded from the hook's URL path+query and signing secret (#160), " +
		"and the transport error goes through alerts.ClientErrorText first because Redact " +
		"is a NO-OP on an https path secret.",

	// --- the read-scoped WebSocket view ---
	"internal/api/ws_policy.go": "redactJSONTree, the residual pass over a read-scoped " +
		"socket frame. Note the []any arm: it joins an all-string array and redacts the " +
		"JOIN rather than the elements, precisely so a payload carrying an argv is not " +
		"redacted per element. See TestRedactJSONTreeDoesNotLeakAnArgvHeader.",

	// --- the retained MQTT sink ---
	"internal/engine/status.go": "the residual half of ScrubDestinationText, which masks a " +
		"destination's own declared literals before its error string reaches the RETAINED " +
		"MQTT topic. Same two-pass shape and order as supervisor.Process.scrub: the exact " +
		"SecretSet built from destSecrets is the boundary, Redact is the residual, whole " +
		"string, never per element.",

	// --- tests that call it to PROVE it is insufficient ---
	"internal/api/argv_leak_test.go": "calls Redact per argv element ON PURPOSE, as the " +
		"discriminator that shows the exact set is doing the work. It asserts the LEAK.",
	"internal/api/ws_policy_array_test.go": "asserts the soundness argument the []any arm " +
		"rests on -- that Redact over the space-JOIN only ever matches more than Redact " +
		"over the elements. It calls Redact on both sides to measure the difference.",
	"internal/engine/secrets_wire_spelling_test.go": "calls Redact ON THE FIXTURE, not on " +
		"the thing under test, and asserts it does NOT remove the key. Process.scrub is " +
		"secrets.Scrub then Redact, so a line the residual can mask on its own would come " +
		"back clean whatever destSecrets emitted and the test would measure nothing while " +
		"looking green. This is that guard: it fails the day Redact grows to cover the " +
		"shape, and says the test stopped being a test.",
	"internal/engine/multitrack_wiring_test.go": "the same shape as the entry directly " +
		"above, for the Twitch minted key. It calls Redact ON THE FIXTURE in a negative " +
		"control that asserts the residual pass does NOT remove the minted key's signature " +
		"from a bare token -- and, in the same test, that it DOES remove it from a line " +
		"still shaped like an RTMP URL. That second measurement is why the real guard " +
		"spawns a child and reads Process.Logs instead of asserting on CommandString: on " +
		"a URL-shaped line the residual masks the segment whatever destSecrets emitted, " +
		"so the test would have looked green while measuring nothing.",
}

// skipWalkDir decides which directories the walk below does not descend into.
//
// BY PATH, not by basename, and that distinction is the whole function. The
// first version of this walk matched on d.Name(), so `web` skipped the
// front-end source at the repository root AND internal/web -- a first-party Go
// package (web.go, web_test.go, i18n_drift_test.go) whose 404 body this very
// PR copies a wire contract from. Verified before the change: a file planted at
// internal/web/zz_probe.go containing
//
//	for i, a := range argv { out[i] = alerts.Redact(a) }
//
// -- the exact per-element loop this guard exists to stop -- and
// TestRedactIsCalledOnlyFromTheAllowlist PASSED. A guard with a
// directory-shaped hole in it is worse than none, because the allowlist below
// reads as exhaustive.
//
// So the four that are not Go at all are matched at the ROOT ONLY, where they
// actually live, and any future internal/<same name> is walked like anything
// else. `.git` and `node_modules` stay name-matched because neither ever
// contains a Go file of this module at any depth, and `testdata` because the Go
// tool itself excludes those from a build -- a call site that cannot be
// compiled is not a call site.
func skipWalkDir(rel, name string) bool {
	switch rel {
	case "ui", "web", "deploy", "docs":
		return true
	// .claude holds git worktrees, which are FULL CHECKOUTS OF THIS SAME
	// MODULE. Walking one scans every file of whatever branch is checked out
	// there, under a path the allowlist cannot match -- so
	// `internal/alerts/redact.go`, which is allowlisted and calls Redact by
	// definition, reappears as
	// `.claude/worktrees/agent-<id>/internal/alerts/redact.go` and fails the
	// guard. Measured: with two agent worktrees open this reported four
	// violations, none of them in the tree under test, and every one of them a
	// file already on the allowlist at its real path.
	//
	// Root only, per the rule above: a future internal/.claude would be walked
	// like anything else. This is the same class as the `web` hole that comment
	// describes -- a directory-shaped blind spot -- except it fails LOUD rather
	// than silent, which is why it is a nuisance and not a hazard.
	case ".claude":
		return true
	}
	switch name {
	case ".git", "node_modules", "testdata":
		return true
	}
	return false
}

// TestRedactIsCalledOnlyFromTheAllowlist fails the build when a new file calls
// alerts.Redact.
//
// Checked against the SYNTAX TREE, not the source text (#107). A substring
// search for "alerts.Redact(" would match every sentence in this file and in
// the doc comment on Redact, and would miss an in-package call written as a
// bare `Redact(`.
func TestRedactIsCalledOnlyFromTheAllowlist(t *testing.T) {
	root := repoRoot(t)

	found := map[string]bool{}
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)

		if d.IsDir() {
			if skipWalkDir(rel, d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // a file that does not parse is not a call site
		}
		inAlerts := file.Name.Name == "alerts"

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				// pkg.Redact(...), where pkg is a plain identifier. Redact is a
				// package-level function, so a selector on anything else --
				// a struct field, a method -- is a different symbol.
				if fn.Sel.Name != "Redact" {
					return true
				}
				id, ok := fn.X.(*ast.Ident)
				if !ok || id.Name != "alerts" {
					return true
				}
			case *ast.Ident:
				// A bare Redact(...) is only this function inside this package.
				if !inAlerts || fn.Name != "Redact" {
					return true
				}
			default:
				return true
			}
			found[rel] = true
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	if len(found) == 0 {
		t.Fatal("no call to alerts.Redact was found anywhere in the tree. Either the " +
			"walk is broken -- in which case this guard has been silently passing -- or " +
			"Redact is now dead and should be deleted rather than left exported.")
	}

	for path := range found {
		if _, ok := redactAllowlist[path]; !ok {
			t.Errorf("%s calls alerts.Redact and is not on redactAllowlist.\n\n"+
				"Redact is a RESIDUAL pass, not a boundary. Read its doc comment: it is "+
				"blind to FFmpeg's `-flag value` grammar (`-passphrase KEY` leaks even "+
				"though `passphrase` is in its table), it does not mask an https path "+
				"segment, it does not see JSON, and applying it PER ELEMENT of an argv is "+
				"strictly worse than applying it to the joined text and never better.\n\n"+
				"If what you need is a guarantee, declare the literals: "+
				"supervisor.Spec.Secrets for a process, alerts.NewSecretSet at any other "+
				"sink. If you genuinely want the best-effort outer pass on top of that, "+
				"add this file to redactAllowlist with a sentence saying which exact pass "+
				"runs first.", path)
		}
	}
	for path, why := range redactAllowlist {
		if !found[path] {
			t.Errorf("redactAllowlist names %s (%q) but nothing there calls alerts.Redact "+
				"any more. Remove the entry: a stale allowlist entry is a hole somebody "+
				"else can move into without tripping this test.", path, why)
		}
	}
}

// repoRoot walks up from the test's working directory to the module root, so
// this test does not encode how deep internal/alerts sits.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}
