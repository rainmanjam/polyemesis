package api

import (
	"encoding/json"
	"reflect"
	"testing"
)

// resetSentCollections must mirror how encoding/json matches a body to a struct,
// or it clears the wrong field or misses the right one. Each case here is one of
// those matching rules.

type rcCell struct {
	N      int  `json:"n"`
	Invert bool `json:"invert,omitempty"`
}

type rcEmbedded struct {
	Flat []rcCell `json:"flat,omitempty"`
}

type rcOwn struct{ Kept []rcCell }

func (o *rcOwn) UnmarshalJSON(b []byte) error { return nil }

type rcDoc struct {
	rcEmbedded
	Cells  []rcCell        `json:"cells"`
	Labels map[string]bool `json:"labels"`
	Fixed  [2]rcCell       `json:"fixed"`
	Inner  *rcDoc          `json:"inner,omitempty"`
	Own    rcOwn           `json:"own"`
	Skip   []rcCell        `json:"-"`
	Plain  []rcCell
}

func stored() *rcDoc {
	cells := func() []rcCell { return []rcCell{{1, true}, {2, false}} }
	return &rcDoc{
		rcEmbedded: rcEmbedded{Flat: cells()},
		Cells:      cells(), Labels: map[string]bool{"a": true},
		Fixed: [2]rcCell{{1, true}}, Inner: &rcDoc{Cells: cells()},
		Own: rcOwn{Kept: cells()}, Skip: cells(), Plain: cells(),
	}
}

// MUTATION: make resetFields a no-op. Observed: FAIL on every "replaced" case.
func TestResetSentCollectionsFollowsEncodingJSONsMatching(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		check      func(d *rcDoc) bool
	}{
		{"a sent slice is replaced", `{"cells":[{"n":2},{"n":1}]}`,
			func(d *rcDoc) bool { return !d.Cells[0].Invert && !d.Cells[1].Invert }},
		{"an absent slice is kept", `{"labels":{}}`,
			func(d *rcDoc) bool { return len(d.Cells) == 2 && d.Cells[0].Invert }},
		{"a sent map is replaced", `{"labels":{"b":true}}`,
			func(d *rcDoc) bool { return !d.Labels["a"] && d.Labels["b"] }},
		{"keys match case-insensitively", `{"CELLS":[{"n":1}]}`,
			func(d *rcDoc) bool { return !d.Cells[0].Invert }},
		{"an untagged field matches its name", `{"Plain":[{"n":1}]}`,
			func(d *rcDoc) bool { return !d.Plain[0].Invert }},
		{"an embedded struct's fields come from the parent object", `{"flat":[{"n":1}]}`,
			func(d *rcDoc) bool { return !d.Flat[0].Invert }},
		{"a fixed array is zeroed", `{"fixed":[{"n":1}]}`,
			func(d *rcDoc) bool { return !d.Fixed[0].Invert }},
		{"a non-nil pointer is walked into", `{"inner":{"cells":[{"n":1}]}}`,
			func(d *rcDoc) bool { return !d.Inner.Cells[0].Invert }},
		{"explicit null clears", `{"cells":null}`,
			func(d *rcDoc) bool { return d.Cells == nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := stored()
			resetSentCollections([]byte(tc.body), d)
			if err := json.Unmarshal([]byte(tc.body), d); err != nil {
				t.Fatal(err)
			}
			if !tc.check(d) {
				t.Errorf("after %s: %+v", tc.body, d)
			}
		})
	}
}

// A type with its own UnmarshalJSON owns its semantics, and a `-` field is not
// the body's to touch.
func TestResetSentCollectionsLeavesWhatTheBodyDoesNotOwn(t *testing.T) {
	d := stored()
	resetSentCollections([]byte(`{"own":{"Kept":[]},"Skip":[]}`), d)
	want := stored()
	if !reflect.DeepEqual(d.Own, want.Own) {
		t.Errorf("a type with its own UnmarshalJSON was reset: %+v", d.Own)
	}
	if !reflect.DeepEqual(d.Skip, want.Skip) {
		t.Errorf(`a json:"-" field was reset: %+v`, d.Skip)
	}
}
