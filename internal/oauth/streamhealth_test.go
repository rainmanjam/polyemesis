package oauth

import (
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// ytStubHealthStreams is two streams on one channel: one YouTube rates bad, with
// an error and a warning issue, and one it rates good.
const ytStubHealthStreams = `{"items":[
 {"id":"s1","cdn":{"ingestionInfo":{"streamName":"key-bad"}},
  "status":{"streamStatus":"active","healthStatus":{"status":"bad","configurationIssues":[
   {"type":"bitrateLow","severity":"error","reason":"Video bitrate low","description":"The stream's current bitrate is lower than the recommended bitrate."},
   {"type":"gopSizeLong","severity":"warning","reason":"Keyframe frequency","description":"..."}]}}},
 {"id":"s2","cdn":{"ingestionInfo":{"streamName":"key-good"}},
  "status":{"streamStatus":"active","healthStatus":{"status":"good"}}}
]}`

// TestYouTubeStreamHealthReadsEveryStreamOnTheChannelInOneListAndChangesNothing:
// one liveStreams.list answers for every destination on the channel, the
// reading is keyed by a DIGEST of the stream key so the cache holds no second
// plaintext copy, only error-severity issues are kept, and nothing but a GET is
// ever sent.
func TestYouTubeStreamHealthReadsEveryStreamOnTheChannelInOneListAndChangesNothing(t *testing.T) {
	var log []capture
	y := ytScheduleStub(t, &log, map[string]http.HandlerFunc{
		"GET " + ytStreamsPath: func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.URL.RawQuery, "status") || !strings.Contains(r.URL.RawQuery, "mine=true") {
				t.Errorf("query = %q, want part=cdn,status&mine=true", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, ytStubHealthStreams)
		},
	})
	got, err := y.StreamHealths(context.Background(), "cid", "tok")
	if err != nil {
		t.Fatal(err)
	}
	bad := got[sha256.Sum256([]byte("key-bad"))]
	if bad.Status != "bad" || !slices.Equal(bad.Issues, []string{"bitrateLow"}) {
		t.Fatalf("bad stream = %+v, want status bad with only the error-severity issue", bad)
	}
	if good := got[sha256.Sum256([]byte("key-good"))]; good.Status != "good" || len(good.Issues) != 0 {
		t.Fatalf("good stream = %+v", good)
	}
	if len(log) != 1 {
		t.Fatalf("%d requests for one channel's health, want 1", len(log))
	}
	for _, c := range log {
		if c.Method != http.MethodGet {
			t.Fatalf("the health read sent %s %s; it must only ever list", c.Method, c.Path)
		}
	}
}

// TestOnlyYouTubeOffersStreamHealth: Twitch publishes no stream-health API, and
// nor do Facebook or Kick in a form with a verdict. A platform that answered
// would have to make one up.
func TestOnlyYouTubeOffersStreamHealth(t *testing.T) {
	for p := range NewSet().All() {
		_, ok := NewSet().StreamHealthFor(p)
		if ok != (p == db.PlatformYouTube) {
			t.Errorf("%s: StreamHealthFor ok = %v", p, ok)
		}
	}
}
