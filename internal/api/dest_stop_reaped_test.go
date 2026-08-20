package api

// #209 at the boundary it was filed against.
//
// POST /destinations/{id}/stop reads the process state back so that "a success
// response is evidence that something happened". It is: supervisor.stop() sets
// StateStopped on BOTH of its arms, so `state: "stopped"` is evidence that
// something happened and NOT evidence of which thing. The two things have
// different consequences for the caller -- one means the child is gone, the
// other means SIGKILL was issued, nobody waited, and a process that may still be
// publishing is still holding the port this response has just declared free.
//
// So a stop answers `reaped`, and a start does not: `reaped` is an answer to
// "did the thing you asked me to end actually end", and starting something asks
// no such question. Both directions are asserted, because a field emitted
// everywhere says as little as one emitted nowhere.

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestStopAnswersWhetherTheChildWasReapedAndStartDoesNot(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())

	create := jsonRequest(t, http.MethodPost, "/api/v1/destinations", map[string]any{
		"sourceId": onlySourceID(t, h, sign),
		"name":     "youtube", "kind": "rtmp",
		"url": "rtmp://example.invalid/live", "streamKey": "k",
	})
	sign(create)
	w := do(t, h, create)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("create destination: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Destination struct {
			ID int64 `json:"id"`
		} `json:"destination"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Destination.ID == 0 {
		t.Fatalf("create response has no id: %v (%s)", err, w.Body.String())
	}

	post := func(path string) map[string]any {
		t.Helper()
		r := jsonRequest(t, http.MethodPost, path, nil)
		sign(r)
		w := do(t, h, r)
		if w.Code != http.StatusOK {
			t.Fatalf("POST %s: %d %s", path, w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("POST %s: decode %v (%s)", path, err, w.Body.String())
		}
		return body
	}

	base := "/api/v1/destinations/" + itoa(created.Destination.ID)

	stop := post(base + "/stop")
	reaped, present := stop["reaped"]
	if !present {
		t.Fatalf("POST %s answered %v with no \"reaped\" field. `state` cannot carry this: "+
			"supervisor.stop() sets StateStopped on the arm where the child was reaped AND "+
			"on the arm where it was sent SIGKILL and abandoned, so a caller reading this "+
			"response cannot tell whether the port and the subscription it has just been "+
			"told are free are actually free.", base+"/stop", stop)
	}
	if reaped != true {
		t.Errorf("reaped = %v on a destination that was never running, want true: there was "+
			"no child, so there is nothing that failed to be observed. A stop that "+
			"reports a warning it does not have makes the real warning unreadable.", reaped)
	}
	if w, ok := stop["warning"]; ok {
		t.Errorf("a clean stop carried warning = %v; the warning exists to mark the "+
			"exceptional arm and must be absent on the ordinary one", w)
	}

	start := post(base + "/start")
	if _, present := start["reaped"]; present {
		t.Errorf("POST %s answered %v, which includes \"reaped\". Starting something asks "+
			"nothing about whether a child was observed dead; a field that appears on "+
			"every response is not an answer to anything.", base+"/start", start)
	}
}
