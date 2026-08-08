package rtmpserver

import (
	"testing"

	"github.com/bluenviron/gortmplib/pkg/amf0"
	"github.com/bluenviron/gortmplib/pkg/message"
)

func amf0Data(payload ...any) *message.DataAMF0 {
	return &message.DataAMF0{Payload: amf0.Data(payload)}
}

// A mid-stream cue point must not evict the cached onMetaData.
//
// Every AMF0 data message shared one "meta" slot, so a cue point -- an ordinary
// thing for an encoder to send -- replaced the metadata in the replay list.
// Every subscriber attaching afterwards then received that cue point where its
// metadata should have been, including the engine's own FFmpeg, whose first act
// on connecting is to identify the streams.
func TestACuePointDoesNotEvictTheCachedMetadata(t *testing.T) {
	st := &stream{slots: map[string]int{}}

	meta := amf0Data("@setDataFrame", "onMetaData", amf0.Object{})
	cue := amf0Data("onCuePoint", amf0.Object{})

	st.cacheSetup(meta)
	st.cacheSetup(cue)

	if len(st.setup) != 2 {
		t.Fatalf("setup holds %d messages, want 2: the cue point replaced the metadata "+
			"instead of taking a slot of its own", len(st.setup))
	}
	if st.setup[0] != message.Message(meta) {
		t.Error("the first replayed message is no longer onMetaData; a late joiner " +
			"gets a cue point where its metadata should be")
	}
}

// Republished metadata must still REPLACE, not accumulate: that is what the
// slot mechanism is for, and it has to keep working per name.
func TestRepublishedMetadataStillReplacesItself(t *testing.T) {
	st := &stream{slots: map[string]int{}}

	first := amf0Data("@setDataFrame", "onMetaData", amf0.Object{{Key: "w", Value: float64(1280)}})
	second := amf0Data("@setDataFrame", "onMetaData", amf0.Object{{Key: "w", Value: float64(1920)}})

	st.cacheSetup(first)
	st.cacheSetup(second)

	if len(st.setup) != 1 {
		t.Fatalf("setup holds %d messages, want 1: republished metadata is accumulating "+
			"and every new subscriber gets a longer prologue ending in a stale one", len(st.setup))
	}
	if st.setup[0] != message.Message(second) {
		t.Error("the replayed metadata is the older one")
	}
}

// Two different cue points are two different events and must not evict each
// other either.
func TestDistinctDataEventsGetDistinctSlots(t *testing.T) {
	st := &stream{slots: map[string]int{}}
	st.cacheSetup(amf0Data("@setDataFrame", "onMetaData", amf0.Object{}))
	st.cacheSetup(amf0Data("onCuePoint", amf0.Object{}))
	st.cacheSetup(amf0Data("onTextData", amf0.Object{}))
	if len(st.setup) != 3 {
		t.Errorf("setup holds %d messages, want 3", len(st.setup))
	}
}

func TestDataAMF0NameSkipsTheDeliveryWrapper(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []any
		want    string
	}{
		{"obs style", []any{"@setDataFrame", "onMetaData", amf0.Object{}}, "onMetaData"},
		{"bare", []any{"onMetaData", amf0.Object{}}, "onMetaData"},
		{"cue point", []any{"onCuePoint", amf0.Object{}}, "onCuePoint"},
		{"wrapper only", []any{"@setDataFrame"}, "unnamed"},
		{"empty", nil, "unnamed"},
		{"no string", []any{amf0.Object{}}, "unnamed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dataAMF0Name(amf0Data(tc.payload...)); got != tc.want {
				t.Errorf("dataAMF0Name = %q, want %q", got, tc.want)
			}
		})
	}
}
