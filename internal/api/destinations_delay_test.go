package api

// #11 at the REST boundary: the broadcast delay's kill switch.
//
// A delay exists so an operator can keep something off the air. So a plain
// stop drains (the agreed "flush on stop"), but ?now=true, stop-all and delete
// all CUT -- nothing still buffered reaches the platform -- and a stop that
// left a drain playing says so in its answer.

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/engine"
)

// delayServer is a real engine whose ingest is flowing, with the given
// delayed destinations created, started and fed, so a stop has a buffered
// tail to drain or cut.
//
// THE FEED IS WHAT LIFTS THE DESTINATION HOLD. The fixture's FFprobe does not
// exist, so the ingest is never measured; the engine starts destinations
// provisionally once probing has failed probeGiveUp times WHILE THE RELAY IS
// FLOWING. So datagrams go into the hub for the whole setup, and keep going
// in the background so each line has something buffered.
func delayServer(t *testing.T, n int) (s *Server, h http.Handler, sign func(*http.Request), ids []int64) {
	t.Helper()
	s, h, _, sign = engineServer(t, defaultTools(), Options{})
	hub := s.mgr.Default().Hub()
	stop := make(chan struct{})
	fed := make(chan struct{})
	go func() {
		defer close(fed)
		for {
			select {
			case <-stop:
				return
			default:
			}
			hub.Deliver(make([]byte, 1316))
			time.Sleep(2 * time.Millisecond)
		}
	}()
	t.Cleanup(func() { close(stop); <-fed })
	src := onlySourceID(t, h, sign)
	for i := range n {
		create := jsonRequest(t, http.MethodPost, "/api/v1/destinations", map[string]any{
			"sourceId": src,
			"name":     "delayed " + itoa(int64(i)), "kind": "rtmp",
			"url": "rtmp://example.invalid/live" + itoa(int64(i)), "streamKey": "k",
			"enabled": true, "delaySeconds": 600,
		})
		sign(create)
		w := do(t, h, create)
		if w.Code != http.StatusOK && w.Code != http.StatusCreated {
			t.Fatalf("create destination: %d %s", w.Code, w.Body.String())
		}
		var created struct {
			Destination struct {
				ID           int64 `json:"id"`
				DelaySeconds int   `json:"delaySeconds"`
			} `json:"destination"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Destination.ID == 0 {
			t.Fatalf("create response has no id: %v (%s)", err, w.Body.String())
		}
		if created.Destination.DelaySeconds != 600 {
			t.Fatalf("created with delaySeconds 600, answered %d", created.Destination.DelaySeconds)
		}
		ids = append(ids, created.Destination.ID)
		postJSON(t, h, sign, "/api/v1/destinations/"+itoa(created.Destination.ID)+"/start", nil)
	}
	for _, id := range ids {
		waitPhase(t, s, id, engine.DelayBuffering, 3*time.Minute)
	}
	// Something buffered in each line, so a drain has a tail to play.
	time.Sleep(200 * time.Millisecond)
	return s, h, sign, ids
}

func phaseOf(s *Server, id int64) string {
	for _, d := range s.mgr.DestinationStatuses() {
		if d.ID == id && d.Delay != nil {
			return d.Delay.Phase
		}
	}
	return ""
}

func waitPhase(t *testing.T, s *Server, id int64, want string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for phaseOf(s, id) != want {
		if time.Now().After(deadline) {
			for _, d := range s.mgr.DestinationStatuses() {
				t.Logf("status id=%d enabled=%v err=%q proc=%v warnings=%v", d.ID, d.Enabled, d.Error, d.Process, d.Warnings)
			}
			t.Fatalf("destination %d never reached delay phase %q (now %q)", id, want, phaseOf(s, id))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func postJSON(t *testing.T, h http.Handler, sign func(*http.Request), path string, body any) map[string]any {
	t.Helper()
	r := jsonRequest(t, http.MethodPost, path, body)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("POST %s: %d %s", path, w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out
}

// THE KILL SWITCH, END TO END THROUGH REST. One flowing programme, three
// delayed destinations, one per stop path:
//
//   - a plain stop DRAINS and says so in its answer; Stop now (?now=true) on
//     the draining destination then cuts it within a bounded time;
//   - stop-all CUTS: it is the panic button;
//   - delete CUTS, including a destination already draining from a stop.
//
// Mutations: pass `false` as cut in bulkOne (stop-all then leaves a drain);
// remove the s.mgr.CutDelay(id) after the delete's reconcile (the deleted
// destination then keeps airing); drop the CutDelay in applyDestinationEnabled
// (Stop now on a draining destination then cuts nothing).
func TestTheBroadcastDelayKillSwitchThroughREST(t *testing.T) {
	s, h, sign, ids := delayServer(t, 3)
	plain, deleted, all := ids[0], ids[1], ids[2]

	// Plain stop: drains.
	stop := postJSON(t, h, sign, "/api/v1/destinations/"+itoa(plain)+"/stop", nil)
	if p := phaseOf(s, plain); p != engine.DelayDraining {
		t.Fatalf("a plain stop of a delayed destination left phase %q, want draining", p)
	}
	if left, _ := stop["drainingSeconds"].(float64); left <= 0 {
		t.Fatalf("the stop answer does not say the tail is still airing: %v", stop)
	}
	// ... and Stop now cuts it.
	began := time.Now()
	cut := postJSON(t, h, sign, "/api/v1/destinations/"+itoa(plain)+"/stop?now=true", nil)
	if cut["cut"] != true {
		t.Fatalf("Stop now on a draining destination answered %v, want cut:true", cut)
	}
	if p := phaseOf(s, plain); p != "" {
		t.Fatalf("still in phase %q after Stop now", p)
	}
	if took := time.Since(began); took > 15*time.Second {
		t.Fatalf("Stop now took %s", took)
	}

	// Delete of a destination already draining: cut.
	postJSON(t, h, sign, "/api/v1/destinations/"+itoa(deleted)+"/stop", nil)
	if phaseOf(s, deleted) != engine.DelayDraining {
		t.Fatal("setup: not draining")
	}
	del := jsonRequest(t, http.MethodDelete, "/api/v1/destinations/"+itoa(deleted), nil)
	sign(del)
	if w := do(t, h, del); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if p := phaseOf(s, deleted); p != "" {
		t.Fatalf("a deleted destination is still airing its delayed tail (phase %q)", p)
	}

	// Stop-all: cut.
	r := jsonRequest(t, http.MethodPost, "/api/v1/destinations/stop-all?source="+itoa(onlySourceID(t, h, sign)),
		map[string]any{"confirm": true})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("stop-all: %d %s", w.Code, w.Body.String())
	}
	if p := phaseOf(s, all); p != "" {
		t.Fatalf("stop-all left a delayed destination in phase %q: its buffer is still airing", p)
	}
}

// ?now= is parsed strictly: a value that is not a boolean is refused rather
// than read as a stop that airs what the operator meant to cut.
func TestStopNowRefusesAValueThatIsNotABoolean(t *testing.T) {
	_, h, _, sign := engineServer(t, defaultTools(), Options{})
	r := jsonRequest(t, http.MethodPost, "/api/v1/destinations/1/stop?now=yes", nil)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusBadRequest {
		t.Fatalf("now=yes answered %d, want 400", w.Code)
	}
}

// The delay is validated at the API like every other field.
func TestADelayOverFifteenMinutesIsRefusedAtCreate(t *testing.T) {
	_, h, _, sign := engineServer(t, defaultTools(), Options{})
	r := jsonRequest(t, http.MethodPost, "/api/v1/destinations", map[string]any{
		"sourceId": onlySourceID(t, h, sign),
		"name":     "too long", "kind": "rtmp",
		"url": "rtmp://example.invalid/live", "streamKey": "k", "delaySeconds": 901,
	})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusBadRequest {
		t.Fatalf("a 901 s delay answered %d, want 400", w.Code)
	}
}
