package obsbridge

import (
	"fmt"
	"testing"
)

// decodeFrame reads the few facts the bridge acts on and ignores everything
// else, a malformed frame included, without guessing.
func TestDecodeFrameReadsOnlyTheFactsTheBridgeActsOn(t *testing.T) {
	event := func(typ, data string) string {
		return fmt.Sprintf(`{"op":%d,"d":{"eventType":%q,"eventData":%s}}`, OpEvent, typ, data)
	}
	response := func(typ string, ok bool, data string) string {
		return fmt.Sprintf(`{"op":%d,"d":{"requestType":%q,"requestStatus":{"result":%t},"responseData":%s}}`,
			OpRequestResponse, typ, ok, data)
	}
	cases := []struct {
		name string
		raw  string
		want Event
		ok   bool
	}{
		{"not JSON", `{`, Event{}, false},
		{"another op", `{"op":0,"d":{}}`, Event{}, false},
		{"event frame unreadable", fmt.Sprintf(`{"op":%d,"d":"x"}`, OpEvent), Event{}, false},
		{"stream started", event(EventStreamStateChanged, `{"outputState":"`+OutputStarted+`"}`),
			Event{Kind: KindStreamStarted, Via: ViaWebSocket}, true},
		{"stream stopped", event(EventStreamStateChanged, `{"outputState":"`+OutputStopped+`"}`),
			Event{Kind: KindStreamStopped, Via: ViaWebSocket}, true},
		{"stream state in between", event(EventStreamStateChanged, `{"outputState":"OBS_WEBSOCKET_OUTPUT_STARTING"}`), Event{}, false},
		{"stream state unreadable", event(EventStreamStateChanged, `"x"`), Event{}, false},
		{"scene changed", event(EventCurrentProgramSceneChanged, `{"sceneName":"Live"}`),
			Event{Kind: KindSceneChanged, Scene: "Live", Via: ViaWebSocket}, true},
		{"scene unnamed", event(EventCurrentProgramSceneChanged, `{}`), Event{}, false},
		{"unsubscribed event", event("InputMuteStateChanged", `{}`), Event{}, false},
		{"response unreadable", fmt.Sprintf(`{"op":%d,"d":"x"}`, OpRequestResponse), Event{}, false},
		{"request refused", response(ReqGetStreamStatus, false, `{"outputActive":true}`), Event{}, false},
		{"status active", response(ReqGetStreamStatus, true, `{"outputActive":true}`),
			Event{Kind: KindStreamStarted, Via: ViaWebSocket, Sync: true}, true},
		{"status idle", response(ReqGetStreamStatus, true, `{"outputActive":false}`),
			Event{Kind: KindStreamStopped, Via: ViaWebSocket, Sync: true}, true},
		{"status unreadable", response(ReqGetStreamStatus, true, `"x"`), Event{}, false},
		{"current scene", response(ReqGetCurrentProgramScene, true, `{"sceneName":"Live"}`),
			Event{Kind: KindSceneChanged, Scene: "Live", Via: ViaWebSocket, Sync: true}, true},
		{"current scene unnamed", response(ReqGetCurrentProgramScene, true, `{}`), Event{}, false},
		{"another request", response("GetVersion", true, `{}`), Event{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := decodeFrame([]byte(c.raw))
			if ok != c.ok || got != c.want {
				t.Errorf("decodeFrame = %+v, %v; want %+v, %v", got, ok, c.want, c.ok)
			}
		})
	}
}
