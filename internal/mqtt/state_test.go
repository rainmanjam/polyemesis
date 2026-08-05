package mqtt

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// approvedFields is every field allowed to reach an MQTT topic, by payload
// type.
//
// This is the security property of this package, held by construction rather
// than by review. polyemesis holds stream keys, OAuth tokens and refresh
// tokens, SRT passphrases and webhook URLs with secrets in their paths. An MQTT
// topic has no equivalent of the URL masking the alerts payloads already apply,
// and a retained message survives the process that sent it -- so a leak here is
// published to every subscriber on the broker and stays there.
//
// Adding a field to one of these structs fails this test. That is the point:
// the decision to expose something must be made deliberately, here, rather than
// inherited by a struct that grew.
var approvedFields = map[string][]string{
	"HostState": {
		"version", "startedAt", "uptimeSec",
		"sources", "sourcesLive", "destinations", "destinationsUp", "at",
	},
	"SourceState": {
		"id", "name", "slug", "live", "ingestMode", "ingestError",
		"bitrateKbps", "uptimeSec", "restarts", "lossPercent", "recording",
		"destinations", "destinationsUp", "failover", "at",
	},
	"DestState": {
		"id", "name", "slug", "platform", "kind", "enabled", "running",
		"error", "bitrateKbps", "uptimeSec", "restarts", "rendition", "at",
	},
	"RenditionState": {
		"id", "name", "slug", "consumers", "running", "width", "height",
		"fps", "codec", "encoder", "bitrateKbps", "error", "at",
	},
}

func TestPayloadsCarryOnlyApprovedFields(t *testing.T) {
	types := []any{HostState{}, SourceState{}, DestState{}, RenditionState{}}
	for _, v := range types {
		rt := reflect.TypeOf(v)
		want := map[string]bool{}
		for _, f := range approvedFields[rt.Name()] {
			want[f] = true
		}
		if len(want) == 0 {
			t.Errorf("%s has no entry in approvedFields; a new payload type must be reviewed before it can publish", rt.Name())
			continue
		}
		got := map[string]bool{}
		for i := range rt.NumField() {
			name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			got[name] = true
			if !want[name] {
				t.Errorf("%s.%s is published to MQTT but is not in approvedFields. "+
					"If it is safe to expose, add it there; if it carries a URL, key, token or passphrase, remove it from the struct.",
					rt.Name(), name)
			}
		}
		for name := range want {
			if !got[name] {
				t.Errorf("approvedFields lists %s.%s but the struct does not have it; the list has drifted from the payload",
					rt.Name(), name)
			}
		}
	}
}

// The census above is a whitelist over field *names*. This is the complementary
// check: no field name anywhere in the payloads is one of the shapes a
// credential arrives in. It catches an approved-looking name that is not.
func TestNoPayloadFieldIsNamedLikeACredential(t *testing.T) {
	banned := []string{
		"url", "key", "token", "secret", "password", "passphrase",
		"credential", "auth", "streamkey", "ingest",
	}
	for _, v := range []any{HostState{}, SourceState{}, DestState{}, RenditionState{}} {
		rt := reflect.TypeOf(v)
		for i := range rt.NumField() {
			name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
			lower := strings.ToLower(name)
			for _, b := range banned {
				if !strings.Contains(lower, b) {
					continue
				}
				// ingestMode and ingestError are the two deliberate exceptions.
				// A mode is `srt`/`rtmp`/`pull` and carries nothing.
				//
				// ingestError is exempt on different grounds, and the grounds
				// this comment used to give were wrong: it said an FFmpeg error
				// is not a URL. It routinely is -- what FFmpeg prints when a
				// publish endpoint refuses it is the output URL, key and all,
				// and supervisor.Status builds LastError from exactly those
				// lines. The field is safe because that value is masked at
				// source in supervisor.Status, not because of what FFmpeg
				// happens to print.
				//
				// Named explicitly so a future `ingestURL` does not inherit
				// the exemption.
				if name == "ingestMode" || name == "ingestError" {
					continue
				}
				t.Errorf("%s.%s is named like a credential (%q). Publishing it would put it on a retained topic for every subscriber on the broker.",
					rt.Name(), name, b)
			}
		}
	}
}

// A publisher that emitted nothing would satisfy the two checks above. This is
// the positive case: prove the payload really does carry the fields an operator
// needs, and that the test can see payload content at all.
func TestPayloadsActuallyCarryTheStateAnOperatorNeeds(t *testing.T) {
	tel, f := newTelemetry(t)
	if err := tel.Publish(context.Background(), sampleSnapshot()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	var found bool
	for _, m := range f.msgs {
		if !strings.Contains(m.topic, "/dest/") {
			continue
		}
		found = true
		var got map[string]any
		if err := json.Unmarshal(m.payload, &got); err != nil {
			t.Fatalf("destination payload is not JSON: %v", err)
		}
		for _, k := range []string{"id", "name", "platform", "enabled", "running"} {
			if _, ok := got[k]; !ok {
				t.Errorf("destination payload has no %q; a consumer cannot tell what this entity is", k)
			}
		}
		if got["name"] != "Twitch (main)" {
			t.Errorf("destination name = %v, want the operator's original text, not the slug", got["name"])
		}
	}
	if !found {
		t.Fatal("no destination topic was published")
	}
}
