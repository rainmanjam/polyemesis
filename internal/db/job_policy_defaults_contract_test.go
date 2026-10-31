package db

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// The Jobs Policy tab's idea of "the default" must be the server's.
//
// THE TAB HIDES WHAT IS AT ITS DEFAULT (A63). Twelve of its settings sit behind
// an Advanced disclosure that stays closed only while every one of them holds
// its factory value, and opens on its own the moment one does not. That rule is
// only as honest as the table it compares against: if the UI believed the
// battery floor defaulted to 20 while DefaultSettings() says 40, a box running
// on the factory policy would render the disclosure open for no reason, and --
// the dangerous direction -- a box whose operator had set the floor to exactly
// the UI's stale number would have that change folded away out of sight.
//
// No endpoint reports the defaults (GET /jobs/policy returns the stored policy,
// the kinds and the modes), so the UI carries them in
// ui/src/lib/contract/job-policy-defaults.json. This test is what stops that
// file being a copy that was right once: it marshals the real
// DefaultSettings().PostProd through the real JSON tags and requires the two
// documents to be equal, key for key. A field added to PostProdSettings with a
// default fails here until the UI has been told about it.
func TestTheUIsJobPolicyDefaultsAreTheServersDefaults(t *testing.T) {
	raw, err := json.Marshal(DefaultSettings().PostProd)
	if err != nil {
		t.Fatalf("marshal the default post-production policy: %v", err)
	}
	var server map[string]any
	if err := json.Unmarshal(raw, &server); err != nil {
		t.Fatalf("unmarshal the default post-production policy: %v", err)
	}
	// Per-kind rows and the whisper model are not settings with a single
	// default the tab compares against: a kind with no row inherits the default
	// mode, and an empty model means "the hardware's choice". Both are
	// omitempty, so they are absent here anyway; deleting them says so.
	delete(server, "kinds")
	delete(server, "whisperModel")

	var ui map[string]any
	if err := json.Unmarshal([]byte(testenv.ReadUI(t, "lib", "contract", "job-policy-defaults.json")), &ui); err != nil {
		t.Fatalf("ui/src/lib/contract/job-policy-defaults.json is not JSON: %v", err)
	}

	if !reflect.DeepEqual(server, ui) {
		t.Errorf("ui/src/lib/contract/job-policy-defaults.json has drifted from DefaultSettings().PostProd.\n"+
			"server: %s\nui:     %v\n"+
			"Rewrite the JSON file from the server value above; the Jobs Policy tab decides "+
			"which settings it may fold away by comparing against it.", raw, ui)
	}
}
