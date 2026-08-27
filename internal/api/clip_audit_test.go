package api

import (
	"strings"
	"testing"
)

// A CLIP EVENT NAMES THE SHOW IT WAS CUT FROM.
//
// The capture is scoped, so Studio B no longer gets Main's buffer. What was
// left was traceability: the event carried the clip name and the operator's
// address and not the programme, so an install running two shows produced a
// stream of "Clip captured" events that could not be told apart -- including
// two cut a second apart from different programmes.
//
// Mutation: drop the fieldProgramme WithField. Observed to fail with "the clip
// audit event does not name the programme".
func TestAClipAuditEventNamesItsProgramme(t *testing.T) {
	ev := auditClipCaptured("clip-1.ts", "10.0.0.4", "Studio B")
	var found string
	for _, f := range ev.Fields {
		if f.Name == fieldProgramme {
			found = f.Value
		}
	}
	if found != "Studio B" {
		t.Errorf("the clip audit event does not name the programme (got %q); two clips "+
			"cut a second apart from different shows are indistinguishable", found)
	}
	// THE CONTROL: a zero-source install adds no field rather than an empty one.
	bare := auditClipCaptured("clip-2.ts", "10.0.0.4", "")
	for _, f := range bare.Fields {
		if f.Name == fieldProgramme {
			t.Errorf("an install with no programme still emitted a %q field: %q",
				fieldProgramme, f.Value)
		}
	}
	if !strings.Contains(ev.Title, "Clip") {
		t.Errorf("title changed unexpectedly: %q", ev.Title)
	}
}
