package eventstream

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/failreason"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// fakeClock is a settable clock for the replay window.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func drain(sub *Subscription) []Envelope {
	var out []Envelope
	for {
		select {
		case env, ok := <-sub.C:
			if !ok {
				return out
			}
			out = append(out, env)
		default:
			return out
		}
	}
}

func seqOf(t *testing.T, id string) uint64 {
	t.Helper()
	_, s, ok := strings.Cut(id, "-")
	if !ok {
		t.Fatalf("id %q has no boot prefix", id)
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		t.Fatalf("id %q: %v", id, err)
	}
	return n
}

// TestEventsAreNumberedInOrderUnderOneBootPrefix: ids are what Last-Event-ID
// resumes from, so they must be strictly increasing, and they carry the
// process's boot prefix so an id from before a restart can never be mistaken
// for one from after it.
func TestEventsAreNumberedInOrderUnderOneBootPrefix(t *testing.T) {
	h := NewHub()
	sub, _, _ := h.Subscribe(Filter{}, "")
	defer sub.Close()
	for i := 0; i < 5; i++ {
		h.Publish(Envelope{Type: TypeIngestPublished})
	}
	got := drain(sub)
	if len(got) != 5 {
		t.Fatalf("got %d events, want 5", len(got))
	}
	for i, env := range got {
		if seqOf(t, env.ID) != uint64(i+1) {
			t.Errorf("event %d has id %q", i, env.ID)
		}
		if !strings.HasPrefix(env.ID, h.boot+"-") {
			t.Errorf("event id %q lacks this process's boot prefix %q", env.ID, h.boot)
		}
		if env.SpecVersion != SpecVersion || env.At.IsZero() {
			t.Errorf("event %d was not stamped: %+v", i, env)
		}
	}
}

// TestASlowClientIsDisconnectedRatherThanSilentlySkipped is the bound on
// per-client memory and the reason it is a disconnect.
//
// events.Broker drops a frame for a slow browser and carries on, which is right
// for a level meter. Here it would be wrong: a consumer that silently lost an
// event would believe it had seen them all, because ids are only checked on
// resume. So the hub closes the subscription; the handler ends the response;
// the client reconnects with Last-Event-ID and the ring fills the gap.
//
// The fast subscriber next to it must be untouched -- one slow consumer is not
// allowed to cost anybody else anything.
func TestASlowClientIsDisconnectedRatherThanSilentlySkipped(t *testing.T) {
	h := NewHub(WithClientBuffer(2))
	slow, _, _ := h.Subscribe(Filter{}, "")
	fast, _, _ := h.Subscribe(Filter{}, "")
	defer fast.Close()

	var fastGot []Envelope
	for i := 0; i < 3; i++ {
		h.Publish(Envelope{Type: TypeIngestPublished})
		fastGot = append(fastGot, drain(fast)...)
	}

	// The slow one holds its two buffered events, then the channel is closed.
	// Bounded, so a build that never closes it fails here rather than hanging
	// the package until go test's own timeout.
	buffered := 0
	timeout := time.After(2 * time.Second)
drainSlow:
	for {
		select {
		case _, ok := <-slow.C:
			if !ok {
				break drainSlow
			}
			buffered++
		case <-timeout:
			t.Fatal("the slow client's channel was never closed: a consumer that falls " +
				"behind is skipped silently instead of being disconnected")
		}
	}
	if buffered != 2 {
		t.Errorf("the slow client received %d events before being cut off, want its buffer of 2", buffered)
	}
	if !slow.Overflowed() {
		t.Error("the slow client's subscription does not report that the hub closed it")
	}
	if h.Kicked() != 1 {
		t.Errorf("Kicked = %d, want 1", h.Kicked())
	}
	if len(fastGot) != 3 {
		t.Errorf("the fast client got %d events, want all 3", len(fastGot))
	}
	if h.HasSubscribers() != true {
		t.Error("the fast client is no longer counted")
	}
	// Close after the hub already closed it must not panic.
	slow.Close()

	// And the remedy works: resuming from the last id it saw replays the one
	// it missed.
	r2, replay2, gap2 := h.Subscribe(Filter{}, fastGot[1].ID)
	defer r2.Close()
	if gap2 || len(replay2) != 1 || replay2[0].ID != fastGot[2].ID {
		t.Errorf("resume after %s: gap=%v replay=%v, want exactly %s", fastGot[1].ID, gap2, ids(replay2), fastGot[2].ID)
	}
}

func ids(envs []Envelope) []string {
	out := make([]string, len(envs))
	for i, e := range envs {
		out[i] = e.ID
	}
	return out
}

// TestResumingReplaysExactlyWhatWasMissedAndNothingTwice: the replay and the
// registration are one atomic step, so an event published between them is
// delivered once, live, and not also replayed.
func TestResumingReplaysExactlyWhatWasMissedAndNothingTwice(t *testing.T) {
	h := NewHub()
	var all []Envelope
	first, _, _ := h.Subscribe(Filter{}, "")
	for i := 0; i < 4; i++ {
		h.Publish(Envelope{Type: TypeRecordingStarted})
	}
	all = drain(first)
	first.Close()

	sub, replay, gap := h.Subscribe(Filter{}, all[1].ID)
	defer sub.Close()
	if gap {
		t.Fatal("a resume from an id the ring still holds reported a gap")
	}
	if got := ids(replay); len(got) != 2 || got[0] != all[2].ID || got[1] != all[3].ID {
		t.Fatalf("replay = %v, want %s and %s", got, all[2].ID, all[3].ID)
	}
	h.Publish(Envelope{Type: TypeRecordingStopped})
	live := drain(sub)
	if len(live) != 1 || seqOf(t, live[0].ID) != 5 {
		t.Fatalf("live after resume = %v, want only event 5", ids(live))
	}

	// Resuming from the newest id replays nothing and is not a gap.
	s2, replay2, gap2 := h.Subscribe(Filter{}, live[0].ID)
	defer s2.Close()
	if gap2 || len(replay2) != 0 {
		t.Errorf("resume from the newest id: gap=%v replay=%v", gap2, ids(replay2))
	}
}

// TestAResumeTheRingCannotCoverIsAGap: an id from another process, an id that
// has aged out, an id from the future and one that does not parse all get
// everything still held AND a stream.gap -- more than was asked for, never
// silently less.
func TestAResumeTheRingCannotCoverIsAGap(t *testing.T) {
	clock := newClock()
	h := NewHub(WithClock(clock.now), WithReplayWindow(time.Minute))
	sub, _, _ := h.Subscribe(Filter{}, "")
	h.Publish(Envelope{Type: TypeIngestPublished})
	h.Publish(Envelope{Type: TypeIngestDisconnected}) // the one the client missed
	old := drain(sub)[0]
	clock.add(2 * time.Minute)
	// Both earlier events age out as these arrive, so a client whose last id
	// was the first needs the second, which is gone.
	h.Publish(Envelope{Type: TypeIngestDisconnected})
	h.Publish(Envelope{Type: TypeIngestPublished})
	sub.Close()

	for name, last := range map[string]string{
		"aged out":         old.ID,
		"another process":  "zzzz-1",
		"from the future":  h.boot + "-99",
		"does not parse":   "garbage",
		"no sequence part": h.boot,
	} {
		s, replay, gap := h.Subscribe(Filter{}, last)
		s.Close()
		if !gap {
			t.Errorf("%s (%q): no gap reported", name, last)
		}
		if len(replay) != 2 {
			t.Errorf("%s: replayed %d, want the 2 still held", name, len(replay))
		}
	}
	if g := h.Gap("x"); g.Type != TypeStreamGap || g.ID != "" {
		t.Errorf("Gap() = %+v: a gap is about this connection and carries no id", g)
	}
}

// TestTheReplayRingIsBoundedByAgeAndByCount: five minutes of a raid's chat
// must not be five minutes of unbounded memory.
func TestTheReplayRingIsBoundedByAgeAndByCount(t *testing.T) {
	clock := newClock()
	h := NewHub(WithClock(clock.now), WithReplayWindow(time.Minute), WithReplayMax(3))
	for i := 0; i < 10; i++ {
		h.Publish(Envelope{Type: TypeIngestPublished})
	}
	h.mu.Lock()
	n := len(h.ring)
	h.mu.Unlock()
	if n != 3 {
		t.Errorf("ring holds %d after 10 publishes with a cap of 3", n)
	}
	clock.add(2 * time.Minute)
	h.Publish(Envelope{Type: TypeIngestPublished})
	h.mu.Lock()
	n = len(h.ring)
	h.mu.Unlock()
	if n != 1 {
		t.Errorf("ring holds %d after the window passed, want only the newest", n)
	}
}

// TestAReadScopedSubscriberIsNeverSentAWithheldType, live or replayed.
func TestAReadScopedSubscriberIsNeverSentAWithheldType(t *testing.T) {
	h := NewHub()
	h.Publish(Envelope{Type: TypeChatMessage, Data: map[string]string{"text": "hi"}})
	h.Publish(Envelope{Type: TypeIngestPublished})

	read, replay, _ := h.Subscribe(Filter{ReadScoped: true}, h.boot+"-0")
	defer read.Close()
	for _, env := range replay {
		if env.Type == TypeChatMessage {
			t.Error("a withheld type was REPLAYED to a read-scoped subscriber")
		}
	}
	h.Publish(Envelope{Type: TypeChatModeration})
	h.Publish(Envelope{Type: TypeChatRetract})
	h.Publish(Envelope{Type: TypeDestinationDown})
	for _, env := range drain(read) {
		if spec, _ := Lookup(env.Type); spec.ReadScope != Granted {
			t.Errorf("read-scoped subscriber was sent %s", env.Type)
		}
	}
}

// TestATopicFilterAdmitsOnlyItsTopics.
func TestATopicFilterAdmitsOnlyItsTopics(t *testing.T) {
	h := NewHub()
	sub, _, _ := h.Subscribe(Filter{Topics: []Topic{TopicRecording}}, "")
	defer sub.Close()
	h.Publish(Envelope{Type: TypeIngestPublished})
	h.Publish(Envelope{Type: TypeRecordingStarted})
	got := drain(sub)
	if len(got) != 1 || got[0].Type != TypeRecordingStarted {
		t.Errorf("got %v, want only recording.started", got)
	}
}

// TestAnUncataloguedTypeReachesNobody: fail closed at the door. A producer that
// invents a type cannot reach a client until somebody catalogues it -- with its
// read-scope decision and its emission test.
func TestAnUncataloguedTypeReachesNobody(t *testing.T) {
	h := NewHub()
	sub, _, _ := h.Subscribe(Filter{}, "")
	defer sub.Close()
	h.Publish(Envelope{Type: "poll.started"})
	h.Publish(Envelope{Type: TypeStreamGap}) // per-connection, never broadcast
	if got := drain(sub); len(got) != 0 {
		t.Errorf("uncatalogued or per-connection types were broadcast: %v", got)
	}
	var nilHub *Hub
	nilHub.Publish(Envelope{Type: TypeAlert}) // nil-safe
	if nilHub.HasSubscribers() {
		t.Error("a nil hub claims subscribers")
	}
}

// streamKey is the credential every leak case below tries to smuggle out. Same
// shape as a real YouTube key.
const streamKey = "abcd-efgh-ijkl-mnop-qrst"

// TestNoStreamKeyReachesAnyEnvelopeField is the leak test, in the shape of
// #20's scrub tests and hooks' TestNoStreamKeyReachesTheWire: every field that
// can carry text is loaded with a credential the way a real one arrives, and
// what comes out -- live AND replayed, because the ring replays to principals
// who were not connected when the event was published -- must not contain it.
func TestNoStreamKeyReachesAnyEnvelopeField(t *testing.T) {
	h := NewHub()
	live, _, _ := h.Subscribe(Filter{}, "")
	defer live.Close()

	url := "rtmp://a.rtmp.youtube.com/live2/" + streamKey
	h.Publish(Envelope{
		Type:        TypeDestinationDown,
		Source:      &hooks.SourceRef{ID: 1, Name: "show " + url},
		Destination: &hooks.DestinationRef{ID: 2, Name: "dest " + url, Platform: "youtube"},
		Reason:      "stopped: " + url,
		Error:       "[rtmp @ 0x1] " + url + ": Input/output error",
		Cause:       failreason.KeyRejected,
		Failure:     &failreason.Reason{Cause: failreason.KeyRejected, Line: url + ": Input/output error"},
	})
	h.Publish(Envelope{
		Type: TypeFailoverSwitched,
		Data: map[string]any{
			"streamKey": streamKey,
			"note":      "pulling from srt://host:9000?streamid=publish:live&passphrase=" + streamKey,
			// An argv, split the way #162 split it.
			"argv": []string{"-headers", "Authorization:", "Bearer", streamKey},
		},
	})
	h.Publish(Envelope{Type: TypeAlert, Data: alerts.Event{
		Type: alerts.TypeDestinationDown, Title: "down", Text: "refused " + url,
		Fields: []alerts.Field{{Name: "key", Value: streamKey}},
	}})

	var bodies []string
	for _, env := range drain(live) {
		b, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(b))
	}
	_, replay, _ := h.Subscribe(Filter{}, h.boot+"-0")
	for _, env := range replay {
		b, _ := json.Marshal(env)
		bodies = append(bodies, string(b))
	}
	if len(bodies) != 6 {
		t.Fatalf("checked %d bodies, want 3 live and 3 replayed; the test proves nothing", len(bodies))
	}
	for _, b := range bodies {
		if strings.Contains(b, streamKey) {
			t.Errorf("a stream key reached the wire:\n%s", b)
		}
	}
}

// TestNoEnvelopeFieldIsNamedAfterASecret: the hooks guard of the same name,
// applied to this envelope's JSON shape.
func TestNoEnvelopeFieldIsNamedAfterASecret(t *testing.T) {
	b, err := json.Marshal(Envelope{
		SpecVersion: SpecVersion, ID: "x-1", Type: TypeDestinationDown, At: time.Now(),
		Source: &hooks.SourceRef{}, Destination: &hooks.DestinationRef{},
		Reason: "r", Error: "e", Cause: failreason.TLS, Held: true, KeyChanged: true,
		Failure: &failreason.Reason{Cause: failreason.TLS, Line: "l"}, Data: map[string]int{"n": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	var walk func(v any)
	walk = func(v any) {
		switch t2 := v.(type) {
		case map[string]any:
			for k, e := range t2 {
				if alerts.SecretName(k) {
					t.Errorf("envelope field %q is named after a secret", k)
				}
				walk(e)
			}
		case []any:
			for _, e := range t2 {
				walk(e)
			}
		}
	}
	var tree any
	_ = json.Unmarshal(b, &tree)
	walk(tree)
}

// TestPublishNeverBlocksAPublisher: the callers hold the engine lock or run the
// sweep. Twenty thousand publishes -- five times the ring's cap -- with a subscriber that never reads must
// finish, not wait.
func TestPublishNeverBlocksAPublisher(t *testing.T) {
	h := NewHub(WithClientBuffer(1))
	sub, _, _ := h.Subscribe(Filter{}, "")
	defer sub.Close()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 20_000; i++ {
			h.Publish(Envelope{Type: TypeIngestPublished})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Publish blocked on a subscriber that does not read")
	}
}

// TestFromHookKeepsEveryWebhookField: one conversion, so the stream and the
// webhook cannot drift apart field by field.
func TestFromHookKeepsEveryWebhookField(t *testing.T) {
	at := time.Unix(5, 0)
	env := FromHook(hooks.Event{
		Trigger: hooks.TriggerDestinationDown, At: at,
		Source:      hooks.SourceRef{ID: 1, Name: "s"},
		Destination: &hooks.DestinationRef{ID: 2, Name: "d", Platform: "twitch"},
		Reason:      "stopped", Error: "e", Cause: failreason.DNS, Held: true, KeyChanged: true,
	})
	if env.Type != TypeDestinationDown || !env.At.Equal(at) || env.Source.Name != "s" ||
		env.Destination.Platform != "twitch" || env.Reason != "stopped" || env.Error != "e" ||
		env.Cause != failreason.DNS || !env.Held || !env.KeyChanged {
		t.Errorf("FromHook lost a field: %+v", env)
	}
}
