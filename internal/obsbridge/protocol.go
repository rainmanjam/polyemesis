package obsbridge

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
)

// The obs-websocket v5 wire protocol, as much of it as the bridge speaks.
// Reference: obs-websocket docs/generated/protocol.md (v5, RPC version 1).
//
// Every message is {"op": <OpCode>, "d": {...}}. The bridge sends Identify and
// two Requests, and reads Hello, Identified, Event and RequestResponse.

// Subprotocol is the JSON encoding's websocket subprotocol. obs-websocket also
// offers msgpack; JSON is what every client library defaults to and what a
// tcpdump of a misbehaving link can be read in.
const Subprotocol = "obswebsocket.json"

// RPCVersion is the obs-websocket RPC version this client is written against.
const RPCVersion = 1

// OpCodes used here.
const (
	OpHello           = 0
	OpIdentify        = 1
	OpIdentified      = 2
	OpEvent           = 5
	OpRequest         = 6
	OpRequestResponse = 7
)

// Event subscription bits: EventSubscription::Scenes (1<<2) carries
// CurrentProgramSceneChanged, EventSubscription::Outputs (1<<6) carries
// StreamStateChanged. Nothing else is subscribed, so OBS is not asked to send
// the input-volume and source chatter this bridge would only throw away.
const (
	SubScenes  = 1 << 2
	SubOutputs = 1 << 6
	// Subscriptions is what Identify asks for.
	Subscriptions = SubScenes | SubOutputs
)

// CloseAuthenticationFailed is WebSocketCloseCode::AuthenticationFailed: the
// password was wrong (or missing). Polyemesis tells it apart from every other
// close because it is the one an operator has to fix rather than wait out.
const CloseAuthenticationFailed = 4009

// Output states (ObsOutputState) that move the bridge. STARTING, STOPPING,
// RECONNECTING and RECONNECTED do not: RECONNECTING in particular is OBS
// fighting to stay live, and arming a stop on it would end a broadcast over a
// network blip.
const (
	OutputStarted = "OBS_WEBSOCKET_OUTPUT_STARTED"
	OutputStopped = "OBS_WEBSOCKET_OUTPUT_STOPPED"
)

// Event types the bridge reads.
const (
	EventStreamStateChanged         = "StreamStateChanged"
	EventCurrentProgramSceneChanged = "CurrentProgramSceneChanged"
)

// Requests the bridge sends, both reads, both only after (re)connecting.
const (
	ReqGetStreamStatus        = "GetStreamStatus"
	ReqGetCurrentProgramScene = "GetCurrentProgramScene"
)

// Message is one frame.
type Message struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
}

// Hello is OpCode 0.
type Hello struct {
	OBSStudioVersion    string `json:"obsStudioVersion"`
	OBSWebSocketVersion string `json:"obsWebSocketVersion"`
	RPCVersion          int    `json:"rpcVersion"`
	Authentication      *struct {
		Challenge string `json:"challenge"`
		Salt      string `json:"salt"`
	} `json:"authentication,omitempty"`
}

// Identify is OpCode 1.
type Identify struct {
	RPCVersion         int    `json:"rpcVersion"`
	Authentication     string `json:"authentication,omitempty"`
	EventSubscriptions int    `json:"eventSubscriptions"`
}

// Identified is OpCode 2.
type Identified struct {
	NegotiatedRPCVersion int `json:"negotiatedRpcVersion"`
}

// EventFrame is OpCode 5.
type EventFrame struct {
	EventType string          `json:"eventType"`
	EventData json.RawMessage `json:"eventData"`
}

// Request is OpCode 6.
type Request struct {
	RequestType string `json:"requestType"`
	RequestID   string `json:"requestId"`
}

// RequestResponse is OpCode 7.
type RequestResponse struct {
	RequestType   string `json:"requestType"`
	RequestID     string `json:"requestId"`
	RequestStatus struct {
		Result bool `json:"result"`
		Code   int  `json:"code"`
	} `json:"requestStatus"`
	ResponseData json.RawMessage `json:"responseData"`
}

// StreamState is StreamStateChanged's data.
type StreamState struct {
	OutputActive bool   `json:"outputActive"`
	OutputState  string `json:"outputState"`
}

// StreamStatus is GetStreamStatus's response data, the part read here.
type StreamStatus struct {
	OutputActive bool `json:"outputActive"`
}

// ProgramScene is CurrentProgramSceneChanged's data and, through its
// deprecated twin, GetCurrentProgramScene's response.
type ProgramScene struct {
	SceneName string `json:"sceneName"`
	// CurrentProgramSceneName is the pre-5.x spelling of the response field,
	// slated for removal; read as a fallback so an older plugin still answers.
	CurrentProgramSceneName string `json:"currentProgramSceneName"`
}

// Name is the scene's name, from whichever field carried it.
func (p ProgramScene) Name() string {
	if p.SceneName != "" {
		return p.SceneName
	}
	return p.CurrentProgramSceneName
}

// AuthString answers obs-websocket's challenge:
//
//	secret = base64(sha256(password + salt))
//	auth   = base64(sha256(secret + challenge))
//
// The password never leaves this function in any form OBS could not already
// derive, and the result is useless against any other challenge.
func AuthString(password, salt, challenge string) string {
	s := sha256.Sum256([]byte(password + salt))
	secret := base64.StdEncoding.EncodeToString(s[:])
	a := sha256.Sum256([]byte(secret + challenge))
	return base64.StdEncoding.EncodeToString(a[:])
}

// frame builds one outgoing message.
func frame(op int, d any) ([]byte, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Message{Op: op, D: raw})
}
