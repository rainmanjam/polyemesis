package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// retry_destination and apply_routing_preset are marked destructive and were
// dispatched on the first call: retry interrupts a running output (and clears a
// hold), and a preset can restart the destination. Every other destructive
// tool asks first -- stop, stop-all, recording off, a scheduled stop -- so an
// agent that has been told "destructive tools answer Not done until you
// confirm" was wrong about exactly these two. Found by an independent security
// review of the MCP server.
//
// Without confirm:true each now answers "Not done:" with what the call would
// do, dispatches nothing that writes, and is not audited (an mcp.action event
// is the record of a write, so its absence is the proof). With it, each does
// what it did before.
//
// Mutation: drop the !a.Confirm refusal in retry_destination. Observed FAIL
// (the unconfirmed retry dispatched a write). Mutation: drop it in
// runApplyPreset. Observed FAIL (the profile changed). Mutation: let the
// preview's compile count as a write (remove the wrote restore in
// compilePreset). Observed FAIL (an unconfirmed preview was audited).

func TestRetryDestinationAsksFirstAndSaysWhatItWouldInterrupt(t *testing.T) {
	rig := newMCPRig(t)
	cs := mcpClient(t, rig.h, rig.admin)
	text, isErr := callText(t, cs, "retry_destination", map[string]any{"destinationId": 1})
	if !isErr || !strings.HasPrefix(text, "Not done:") {
		t.Fatalf("retry without confirm answered (isError=%v): %s", isErr, text)
	}
	for _, want := range []string{`"running"`, `"held"`, `"live"`, `"destination"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the retry preview does not say %s: %s", want, text)
		}
	}
	if evs := rig.mcpEvents(); len(evs) != 0 {
		t.Fatalf("an unconfirmed retry dispatched a write: %+v", evs)
	}

	text, isErr = callText(t, cs, "retry_destination", map[string]any{"destinationId": 1, "confirm": true})
	if isErr {
		t.Fatalf("a confirmed retry failed: %s", text)
	}
	evs := rig.mcpEvents()
	if len(evs) != 1 || !strings.Contains(fieldOf(evs[0], fieldMCPOutcome), "done") {
		t.Fatalf("a confirmed retry was not dispatched and audited once: %+v", evs)
	}
}

func TestApplyRoutingPresetAsksFirstAndPreviewsTheTracksAndTheRestart(t *testing.T) {
	rig := newMCPRig(t)
	cs := mcpClient(t, rig.h, rig.admin)
	before, err := rig.store.GetDestination(1)
	if err != nil {
		t.Fatal(err)
	}
	preset := routing.Presets()[0].ID
	text, isErr := callText(t, cs, "apply_routing_preset", map[string]any{"destinationId": 1, "preset": preset})
	if !isErr || !strings.HasPrefix(text, "Not done:") {
		t.Fatalf("apply without confirm answered (isError=%v): %s", isErr, text)
	}
	for _, want := range []string{`"destination"`, `"preset"`, `"tracks"`, `"wouldRestart"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the preset preview does not say %s: %s", want, text)
		}
	}
	after, err := rig.store.GetDestination(1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Profile, after.Profile) || !reflect.DeepEqual(before.VODProfile, after.VODProfile) {
		t.Fatal("an unconfirmed preset changed the destination's routing")
	}
	if evs := rig.mcpEvents(); len(evs) != 0 {
		t.Fatalf("an unconfirmed preset was audited as a write: %+v", evs)
	}

	text, isErr = callText(t, cs, "apply_routing_preset", map[string]any{"destinationId": 1, "preset": preset, "confirm": true})
	if isErr {
		t.Fatalf("a confirmed preset failed: %s", text)
	}
	var got struct {
		Applied string `json:"applied"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil || got.Applied != preset {
		t.Fatalf("a confirmed preset answered %s", text)
	}
	if evs := rig.mcpEvents(); len(evs) == 0 {
		t.Fatal("a confirmed preset was not audited")
	}
}

// Every tool marked destructive takes confirm, and says in the catalogue when
// it demands it. A destructive tool that dispatches on the first call has to
// carry that decision in writing (mcpTool.confirms), rather than by omission --
// which is how retry_destination and apply_routing_preset shipped without one.
func TestEveryDestructiveToolAsksFirst(t *testing.T) {
	n := 0
	for _, tool := range mcpCatalogue() {
		if !tool.destructive {
			continue
		}
		n++
		if _, ok := tool.props["confirm"]; !ok {
			t.Errorf("%s is destructive and takes no confirm argument", tool.name)
		}
		if tool.confirms == "" {
			t.Errorf("%s is destructive and does not say when it asks for confirm (mcpTool.confirms)", tool.name)
		}
		if !strings.Contains(tool.description, "confirm:true") {
			t.Errorf("%s's description does not tell the agent it needs confirm:true", tool.name)
		}
	}
	if n < 6 {
		t.Fatalf("only %d destructive tools found; the catalogue did not enumerate", n)
	}
}
