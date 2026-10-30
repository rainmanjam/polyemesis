package obsbridge

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rainmanjam/polyemesis/internal/eventstream"
)

// Via says which way a fact reached the bridge.
type Via string

const (
	// ViaWebSocket: polyemesis's own obs-websocket connection.
	ViaWebSocket Via = "websocket"
	// ViaScript: the OBS script, through POST /api/v1/obs/events.
	ViaScript Via = "script"
)

// Kind is one of the three facts the bridge acts on. The strings are the
// script's wire vocabulary (POST /api/v1/obs/events {"event": ...}), so they
// are as stable as an event type: do not rename them.
type Kind string

const (
	KindStreamStarted Kind = "stream.started"
	KindStreamStopped Kind = "stream.stopped"
	KindSceneChanged  Kind = "scene.changed"
)

// Event is one fact about OBS.
type Event struct {
	Kind  Kind
	Scene string
	Via   Via
	// Sync marks a state READ after (re)connecting, rather than a change OBS
	// announced. It updates what the bridge knows and acts on nothing: a
	// reconnect is not OBS starting, and it is not OBS stopping either. If OBS
	// stopped while the link was down, the operator was not watching this
	// bridge do it, and a stop they did not see coming is the one stop-all
	// exists to make deliberate.
	Sync bool
}

// Errors Handle returns. The API maps each to a status.
var (
	// ErrDisabled: the bridge is switched off. A 409 to the script, with a
	// sentence that says where to switch it on.
	ErrDisabled = errors.New("the OBS bridge is switched off; turn it on in Settings, OBS bridge")
	// ErrBadEvent: not one of the three facts, or a scene name nobody could
	// have typed. A 400.
	ErrBadEvent = errors.New("unknown OBS event")
)

// normaliseScene trims and bounds a scene name arriving from the network. It
// returns "" for one that cannot be a scene: empty, too long, or carrying
// control characters, which no OBS scene name does and which would otherwise
// reach the status page and every SSE consumer verbatim.
func normaliseScene(s string) string {
	if s == "" || len(s) > MaxSceneNameLength {
		return ""
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return ""
		}
	}
	return s
}

func (ev Event) validate() (Event, error) {
	switch ev.Kind {
	case KindStreamStarted, KindStreamStopped:
		ev.Scene = ""
	case KindSceneChanged:
		ev.Scene = normaliseScene(ev.Scene)
		if ev.Scene == "" {
			return ev, fmt.Errorf("%w: scene.changed needs a scene name of 1 to %d bytes with no control characters",
				ErrBadEvent, MaxSceneNameLength)
		}
	default:
		return ev, fmt.Errorf("%w: use %q, %q or %q", ErrBadEvent, KindStreamStarted, KindStreamStopped, KindSceneChanged)
	}
	if ev.Via != ViaWebSocket && ev.Via != ViaScript {
		return ev, fmt.Errorf("%w: unknown origin", ErrBadEvent)
	}
	return ev, nil
}

// Outcome is what one action did to one destination. Same words as the bulk
// controls' bulkDestResult (started, stopped, warned, failed, skipped) plus
// "applied" for a scene's levels, so the status page renders one vocabulary.
type Outcome struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	Message string `json:"message,omitempty"`
}

// Actions is what the bridge asks of the rest of polyemesis. internal/api
// implements it on the same code paths the per-destination controls use, so
// OBS can never do more than the operator's own buttons do.
//
// Each method is called on the bridge's single worker goroutine, one at a
// time, and must honour ctx: a stop cancels a paced start that is still
// walking its list.
type Actions interface {
	Start(ctx context.Context, cfg Config) []Outcome
	Stop(ctx context.Context, cfg Config) []Outcome
	ApplyScene(ctx context.Context, cfg Config, rule SceneRule) []Outcome
}

// Publisher is the slice of *eventstream.Hub the bridge uses.
type Publisher interface {
	Publish(eventstream.Envelope)
}

// ConnState is the obs-websocket link's state, for the status page.
type ConnState string

const (
	// ConnOff: not configured to connect (the script-only deployment, or the
	// bridge switched off).
	ConnOff ConnState = "off"
	// ConnConnecting: dialling, or waiting to dial again.
	ConnConnecting ConnState = "connecting"
	// ConnConnected: identified, subscribed, and receiving events.
	ConnConnected ConnState = "connected"
	// ConnUnreachable: the address did not answer, or the link dropped.
	// Retried with backoff for as long as the configuration stands.
	ConnUnreachable ConnState = "unreachable"
	// ConnRefused: obs-websocket refused the password, or asked for one and
	// none is set. Retried slowly: it will not fix itself, but OBS's own
	// setting may be changed back.
	ConnRefused ConnState = "refused"
	// ConnIncompatible: whatever answered did not speak obs-websocket v5.
	ConnIncompatible ConnState = "incompatible"
)

// Connection is the link's state as the status page and obs.connection carry
// it. Detail is always one of this package's own sentences -- never an error
// string from the network, which can carry an address, and never anything
// derived from the password.
type Connection struct {
	State               ConnState `json:"state"`
	Detail              string    `json:"detail,omitempty"`
	Since               time.Time `json:"since"`
	OBSStudioVersion    string    `json:"obsStudioVersion,omitempty"`
	OBSWebSocketVersion string    `json:"obsWebSocketVersion,omitempty"`
}

// ActionRecord is one thing the bridge did, newest first in Status.
type ActionRecord struct {
	At      time.Time `json:"at"`
	Action  string    `json:"action"` // start | stop | scene
	Via     Via       `json:"via"`
	Scene   string    `json:"scene,omitempty"`
	Preset  string    `json:"preset,omitempty"`
	Results []Outcome `json:"results"`
}

// Status is the bridge's live state: GET /api/v1/obs's "status".
type Status struct {
	Connection Connection `json:"connection"`
	// Streaming is whether OBS is streaming, as far as the bridge knows. Null
	// until something has said.
	Streaming *bool  `json:"streaming"`
	Scene     string `json:"scene,omitempty"`
	// StopPendingUntil is set while a grace period is running: OBS stopped,
	// and the destinations stop at this time unless OBS starts again first.
	StopPendingUntil *time.Time     `json:"stopPendingUntil,omitempty"`
	LastActions      []ActionRecord `json:"lastActions"`
}

// Event payloads, carried in eventstream.Envelope.Data.

// ConnectionData is obs.connection's data.
type ConnectionData struct {
	State  ConnState `json:"state"`
	Detail string    `json:"detail,omitempty"`
}

// StreamData is obs.stream's data.
type StreamData struct {
	State string `json:"state"` // started | stopped
	Via   Via    `json:"via"`
	Sync  bool   `json:"sync,omitempty"`
	// StopInSeconds is set on a stop that will stop destinations: how long the
	// grace period is. Absent when stopping is not armed.
	StopInSeconds *int `json:"stopInSeconds,omitempty"`
}

// SceneData is obs.scene's data.
type SceneData struct {
	Scene string `json:"scene"`
	Via   Via    `json:"via"`
	Sync  bool   `json:"sync,omitempty"`
	// Preset is the routing preset the scene's rule applies, when it has one.
	Preset string `json:"preset,omitempty"`
}

// maxActionRecords bounds Status.LastActions.
const maxActionRecords = 10

// Options configures a Bridge.
type Options struct {
	Log *slog.Logger
	// Pub receives obs.* events. Nil publishes nothing.
	Pub Publisher
	// Load reads the stored configuration and the obs-websocket password.
	Load func() (Config, string, error)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

type jobKind int

const (
	jobStart jobKind = iota
	jobStop
	jobScene
)

type job struct {
	kind   jobKind
	via    Via
	ctx    context.Context
	cancel context.CancelFunc
	rule   SceneRule
	scene  string
}

// Bridge is the OBS bridge. One per process.
type Bridge struct {
	log  *slog.Logger
	pub  Publisher
	load func() (Config, string, error)
	now  func() time.Time

	// graceUnit is one second in production. A var on the struct, not a
	// package var, so a test shortens it for its own bridge and nobody else's.
	graceUnit time.Duration
	// retryMin and retryMax bound the reconnect backoff; tests shorten them.
	retryMin, retryMax time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	jobs   chan job
	kick   chan struct{}
	wg     sync.WaitGroup

	mu        sync.Mutex
	cfg       Config
	password  string
	actions   Actions
	conn      Connection
	streaming *bool
	scene     string

	startCancel context.CancelFunc
	stopGen     uint64
	stopTimer   *time.Timer
	stopDue     time.Time
	records     []ActionRecord
}

// New builds a bridge and starts its action worker. Run starts the
// obs-websocket side; Close stops both.
func New(o Options) *Bridge {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &Bridge{
		log: log, pub: o.Pub, load: o.Load, now: now,
		graceUnit: time.Second, retryMin: time.Second, retryMax: 30 * time.Second,
		ctx: ctx, cancel: cancel,
		// Buffered so Handle never blocks the read loop or an HTTP handler on
		// a worker busy with a paced start. Sixteen is far more than three
		// kinds of fact can queue while one action runs.
		jobs: make(chan job, 16),
		kick: make(chan struct{}, 1),
	}
	b.conn = Connection{State: ConnOff, Since: now()}
	b.wg.Add(1)
	go b.worker()
	return b
}

// SetActions installs what the bridge acts through. Until it is called the
// bridge records and publishes, and acts on nothing.
func (b *Bridge) SetActions(a Actions) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.actions = a
}

// Close stops the worker, the obs-websocket client and any pending stop.
func (b *Bridge) Close() {
	b.mu.Lock()
	b.cancelStopLocked()
	b.mu.Unlock()
	b.cancel()
	b.wg.Wait()
}

// Config returns the configuration the bridge is acting on.
func (b *Bridge) Config() Config {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cfg
}

// Reload re-reads the stored configuration NOW, so the PUT that changed it is
// in force when it returns, and asks Run to bring the connection into line.
func (b *Bridge) Reload() error {
	if b.load == nil {
		return nil
	}
	cfg, pw, err := b.load()
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.cfg, b.password = cfg, pw
	if !cfg.Enabled || !cfg.StopArmed() {
		// A stop armed under the old configuration does not survive the
		// operator turning stopping off.
		b.cancelStopLocked()
	}
	b.mu.Unlock()
	select {
	case b.kick <- struct{}{}:
	default:
	}
	return nil
}

// Status returns the live state.
func (b *Bridge) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := Status{Connection: b.conn, Scene: b.scene, LastActions: append([]ActionRecord(nil), b.records...)}
	if b.streaming != nil {
		v := *b.streaming
		st.Streaming = &v
	}
	if !b.stopDue.IsZero() {
		d := b.stopDue
		st.StopPendingUntil = &d
	}
	if st.LastActions == nil {
		st.LastActions = []ActionRecord{}
	}
	return st
}

// Handle takes one fact about OBS and acts on it if it is an edge.
func (b *Bridge) Handle(ev Event) error {
	ev, err := ev.validate()
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.cfg.Enabled {
		return ErrDisabled
	}
	switch ev.Kind {
	case KindStreamStarted:
		b.streamStartedLocked(ev)
	case KindStreamStopped:
		b.streamStoppedLocked(ev)
	case KindSceneChanged:
		b.sceneChangedLocked(ev)
	}
	return nil
}

func (b *Bridge) streamStartedLocked(ev Event) {
	if b.streaming != nil && *b.streaming {
		// The same state reported again -- by the other way in, or by a
		// reconnect's read. Not an edge, so nothing happens and nothing is
		// published twice.
		return
	}
	on := true
	b.streaming = &on
	// OBS is back: a stop waiting out its grace period is cancelled. This is
	// the grace period's whole purpose.
	b.cancelStopLocked()
	b.publishLocked(eventstream.TypeOBSStream, StreamData{State: "started", Via: ev.Via, Sync: ev.Sync})
	if ev.Sync || !b.cfg.StartOnStream || len(b.cfg.Destinations) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(b.ctx)
	if b.startCancel != nil {
		b.startCancel()
	}
	b.startCancel = cancel
	b.enqueueLocked(job{kind: jobStart, via: ev.Via, ctx: ctx, cancel: cancel})
}

func (b *Bridge) streamStoppedLocked(ev Event) {
	if b.streaming != nil && !*b.streaming {
		return
	}
	off := false
	b.streaming = &off
	// A paced start still walking its list stops where it is. Whatever it had
	// not reached is reported as skipped by the Actions implementation.
	if b.startCancel != nil {
		b.startCancel()
		b.startCancel = nil
	}
	data := StreamData{State: "stopped", Via: ev.Via, Sync: ev.Sync}
	if !ev.Sync && b.cfg.StopArmed() && len(b.cfg.Destinations) > 0 {
		grace := b.cfg.StopGraceSeconds
		data.StopInSeconds = &grace
		b.armStopLocked(ev.Via, grace)
	}
	b.publishLocked(eventstream.TypeOBSStream, data)
}

func (b *Bridge) sceneChangedLocked(ev Event) {
	if ev.Scene == b.scene {
		return
	}
	b.scene = ev.Scene
	rule, ok := b.cfg.Rule(ev.Scene)
	data := SceneData{Scene: ev.Scene, Via: ev.Via, Sync: ev.Sync}
	if ok {
		data.Preset = rule.Preset
	}
	b.publishLocked(eventstream.TypeOBSScene, data)
	if ev.Sync || !ok {
		return
	}
	b.enqueueLocked(job{kind: jobScene, via: ev.Via, rule: rule, scene: ev.Scene, ctx: b.ctx})
}

// armStopLocked starts the grace period. A generation counter, not just
// Timer.Stop, decides whether a firing timer still counts: Stop cannot recall
// a timer whose function has already started and is waiting for the lock.
func (b *Bridge) armStopLocked(via Via, graceSeconds int) {
	b.cancelStopLocked()
	gen := b.stopGen
	if graceSeconds <= 0 {
		b.enqueueLocked(job{kind: jobStop, via: via, ctx: b.ctx})
		return
	}
	grace := time.Duration(graceSeconds) * b.graceUnit
	b.stopDue = b.now().Add(grace)
	b.stopTimer = time.AfterFunc(grace, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.stopGen != gen {
			return
		}
		b.stopDue = time.Time{}
		b.stopTimer = nil
		b.enqueueLocked(job{kind: jobStop, via: via, ctx: b.ctx})
	})
}

func (b *Bridge) cancelStopLocked() {
	b.stopGen++
	if b.stopTimer != nil {
		b.stopTimer.Stop()
		b.stopTimer = nil
	}
	b.stopDue = time.Time{}
}

func (b *Bridge) enqueueLocked(j job) {
	select {
	case b.jobs <- j:
	default:
		// Sixteen actions queued behind one that is still running means
		// something upstream is flapping. Dropping the newest is the safe
		// direction for a start or a scene; a dropped stop leaves destinations
		// running, which is the direction stop-all's rules prefer to err in.
		b.log.Warn("OBS bridge: action queue is full; dropping an action", "action", jobName(j.kind))
		if j.cancel != nil {
			j.cancel()
		}
	}
}

func jobName(k jobKind) string {
	switch k {
	case jobStart:
		return "start"
	case jobStop:
		return "stop"
	default:
		return "scene"
	}
}

// worker runs actions one at a time, in the order the facts arrived.
func (b *Bridge) worker() {
	defer b.wg.Done()
	for {
		select {
		case <-b.ctx.Done():
			return
		case j := <-b.jobs:
			b.runJob(j)
		}
	}
}

func (b *Bridge) runJob(j job) {
	b.mu.Lock()
	a, cfg := b.actions, b.cfg
	// Re-checked at run time, not only when queued: the configuration or OBS
	// may have moved while an earlier action ran.
	stillStopped := b.streaming != nil && !*b.streaming
	b.mu.Unlock()
	if j.cancel != nil {
		defer j.cancel()
	}
	if a == nil || !cfg.Enabled {
		return
	}
	rec := ActionRecord{At: b.now(), Action: jobName(j.kind), Via: j.via}
	switch j.kind {
	case jobStart:
		if j.ctx.Err() != nil {
			return
		}
		rec.Results = a.Start(j.ctx, cfg)
	case jobStop:
		if !stillStopped || !cfg.StopArmed() {
			return
		}
		rec.Results = a.Stop(j.ctx, cfg)
	case jobScene:
		rec.Scene, rec.Preset = j.scene, j.rule.Preset
		// The rule as it stands now, if the scene still has one; the queued
		// copy otherwise is stale configuration.
		rule, ok := cfg.Rule(j.scene)
		if !ok {
			return
		}
		rec.Preset = rule.Preset
		rec.Results = a.ApplyScene(j.ctx, cfg, rule)
	}
	if rec.Results == nil {
		rec.Results = []Outcome{}
	}
	b.mu.Lock()
	b.records = append([]ActionRecord{rec}, b.records...)
	if len(b.records) > maxActionRecords {
		b.records = b.records[:maxActionRecords]
	}
	b.mu.Unlock()
	b.log.Info("OBS bridge acted", "action", rec.Action, "via", rec.Via, "destinations", len(rec.Results))
}

func (b *Bridge) publishLocked(t eventstream.Type, data any) {
	if b.pub == nil {
		return
	}
	b.pub.Publish(eventstream.Envelope{Type: t, At: b.now(), Data: data})
}

// setConn records a connection state, publishing obs.connection when the
// state or its sentence changed. A retry loop reporting "connecting" every few
// seconds is one event, not one per attempt.
func (b *Bridge) setConn(state ConnState, detail string, hello *Hello) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn.State == state && b.conn.Detail == detail {
		return
	}
	b.conn = Connection{State: state, Detail: detail, Since: b.now()}
	if hello != nil {
		b.conn.OBSStudioVersion = boundVersion(hello.OBSStudioVersion)
		b.conn.OBSWebSocketVersion = boundVersion(hello.OBSWebSocketVersion)
	}
	b.publishLocked(eventstream.TypeOBSConnection, ConnectionData{State: state, Detail: detail})
}

// boundVersion keeps a version string from the network to something a version
// string could be.
func boundVersion(v string) string {
	if len(v) > 32 {
		return ""
	}
	for _, r := range v {
		if !(unicode.IsDigit(r) || unicode.IsLetter(r) || strings.ContainsRune(".-+", r)) {
			return ""
		}
	}
	return v
}

// clientSig hashes what a live connection was built from, so an unrelated
// configuration save -- a scene rule added -- does not cycle a healthy link.
// The password is HASHED, for the reason mqttSig gives: a rotation to another
// password of the same length must still be seen.
func clientSig(cfg Config, password string) string {
	if !cfg.Enabled || !cfg.Connect {
		return ""
	}
	pw := sha256.Sum256([]byte(password))
	return fmt.Sprintf("%s\x00%x", cfg.URL, pw)
}

// Run keeps the obs-websocket connection matching the configuration until ctx
// ends. It never returns an error: OBS being closed is the normal state of an
// install between shows.
func (b *Bridge) Run(ctx context.Context) {
	if err := b.Reload(); err != nil {
		b.log.Warn("OBS bridge: cannot read its configuration; it stays off until it can", "err", err)
	}
	var (
		sig       string
		clientEnd context.CancelFunc
		clientWG  sync.WaitGroup
	)
	stopClient := func() {
		if clientEnd != nil {
			clientEnd()
			clientWG.Wait()
			clientEnd = nil
		}
	}
	defer stopClient()
	for {
		b.mu.Lock()
		cfg, pw := b.cfg, b.password
		b.mu.Unlock()
		if want := clientSig(cfg, pw); want != sig || (want != "" && clientEnd == nil) {
			stopClient()
			sig = want
			if want == "" {
				b.setConn(ConnOff, "", nil)
			} else {
				cctx, cancel := context.WithCancel(ctx)
				clientEnd = cancel
				clientWG.Add(1)
				go func() {
					defer clientWG.Done()
					b.runClient(cctx, cfg.URL, pw)
				}()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-b.ctx.Done():
			return
		case <-b.kick:
		}
	}
}
