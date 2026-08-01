package hooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

const plantedKey = "live_9999_PLANTEDSTREAMKEY"

// The single most important test in this package. Snapshot.IngestError and
// DestState.Error come from supervisor.runOnce, which returns the last three
// FFmpeg STDERR lines -- and an FFmpeg failing to publish prints the whole
// rtmps:// URL, stream key included. Anything that reaches an envelope has to
// go through the same redaction the alert path applies centrally.
func TestNoStreamKeyReachesAnEnvelope(t *testing.T) {
	dirty := "rtmps://live.twitch.tv/app/" + plantedKey + ": Connection refused"

	for _, ev := range []Event{
		{Trigger: TriggerIngestDisconnected, Source: SourceRef{ID: 1, Name: "Main"}, Error: dirty},
		{Trigger: TriggerIngestDisconnected, Source: SourceRef{ID: 1, Name: "Main"}, Reason: dirty},
		{
			Trigger:     TriggerDestinationDown,
			Source:      SourceRef{ID: 1, Name: "Main"},
			Destination: &DestinationRef{ID: 3, Name: "Twitch", Platform: "twitch"},
			Error:       dirty,
		},
	} {
		env := Envelope{
			SpecVersion: SpecVersion, ID: "d1", Sequence: 1,
			Trigger: ev.Trigger, At: time.Unix(0, 0).UTC(),
			Source: ev.Source, Destination: ev.Destination,
			Reason: ev.redacted().Reason, Error: ev.redacted().Error,
		}
		body, err := Encode(env)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), plantedKey) {
			t.Fatalf("%s carried a stream key to the wire:\n%s", ev.Trigger, body)
		}
		if !strings.Contains(string(body), alerts.Mask) {
			t.Errorf("%s dropped the error entirely instead of masking it; the "+
				"receiver needs to know something went wrong:\n%s", ev.Trigger, body)
		}
	}
}

// A structural guard, so a field added later cannot smuggle a credential out by
// being named after one. Walks the marshalled object rather than the Go struct,
// because the JSON tag is what actually ships.
func TestNoEnvelopeFieldIsNamedAfterASecret(t *testing.T) {
	body, err := Encode(Envelope{
		SpecVersion: SpecVersion, ID: "d1", Sequence: 1,
		Trigger: TriggerDestinationDown, At: time.Unix(0, 0).UTC(),
		Source:      SourceRef{ID: 1, Name: "Main"},
		Destination: &DestinationRef{ID: 3, Name: "Twitch", Platform: "twitch"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var tree any
	if err := json.Unmarshal(body, &tree); err != nil {
		t.Fatal(err)
	}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch node := v.(type) {
		case map[string]any:
			for k, child := range node {
				if alerts.SecretName(k) {
					t.Errorf("envelope field %s%s is named after a credential; "+
						"a hook payload must never carry one", path, k)
				}
				walk(path+k+".", child)
			}
		case []any:
			for _, child := range node {
				walk(path, child)
			}
		}
	}
	walk("", tree)
}

// The signature covers the timestamp AND the body. Signing the body alone lets
// a request captured off the wire be replayed an hour later against a receiver
// that only compares digests.
func TestSignCoversTheTimestamp(t *testing.T) {
	const secret = "topsecret"
	body := []byte(`{"a":1}`)

	got := Sign(secret, 1700000000, body)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("1700000000."))
	mac.Write(body)
	want := "v1=" + hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("Sign = %q, want %q", got, want)
	}
	if other := Sign(secret, 1700000001, body); other == got {
		t.Fatal("the timestamp does not change the signature; a captured body " +
			"can be replayed forever")
	}
}

func TestNewSecretIsLongAndDistinct(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSecret()
	if a == b {
		t.Fatal("two generated secrets are identical")
	}
	if len(a) != SecretBytes*2 {
		t.Fatalf("secret is %d hex characters, want %d", len(a), SecretBytes*2)
	}
}

// The envelope is a contract. A receiver written against v1 must keep working,
// so the field names are pinned here and specVersion is bumped if they change.
func TestEnvelopeWireShape(t *testing.T) {
	body, err := Encode(Envelope{
		SpecVersion: SpecVersion, ID: "abc", Sequence: 7, Missed: 2,
		Trigger: TriggerIngestPublished,
		At:      time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
		Source:  SourceRef{ID: 1, Name: "Main"},
		Reason:  "data is arriving on the ingest",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"specVersion":"1"`, `"id":"abc"`, `"sequence":7`, `"missed":2`,
		`"trigger":"ingest.published"`, `"at":"2026-07-31T12:00:00Z"`,
		`"source":{"id":1,"name":"Main"}`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("envelope is missing %s:\n%s", want, body)
		}
	}
	// Absent fields must be absent, not null: a receiver switching on the
	// presence of "destination" should not have to also check for null.
	if strings.Contains(string(body), `"destination"`) {
		t.Errorf("an ingest event carried a destination key:\n%s", body)
	}
	if strings.Contains(string(body), `"test"`) {
		t.Errorf("a real delivery is marked as a test:\n%s", body)
	}
}
