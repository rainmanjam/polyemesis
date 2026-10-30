package polls

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
)

// Timings.
const (
	// tickEvery drives the clock: closing at time, expiring holds, and the
	// coalesced events below. Its granularity is the most a poll overruns.
	tickEvery = 250 * time.Millisecond
	// voteEvery coalesces poll.vote. A raid voting at once is one event a
	// second, not one per vote: the replay ring holds five minutes of events
	// for every subscriber, and a vote storm would push everything else out.
	voteEvery = time.Second
	// displayEvery republishes poll.display while a poll is open, so an
	// overlay that connects mid-poll -- or after the replay window -- draws it
	// without waiting for the next vote.
	displayEvery = 15 * time.Second
	// mirrorEvery is how often a native mirror's running count is read.
	// Twitch allows 800 requests a minute; this is twelve.
	mirrorEvery = 5 * time.Second
	// mirrorGrace bounds how long a closing poll waits for the native
	// mirror's final count before it is published without it.
	mirrorGrace = 10 * time.Second
	// sendTimeout bounds one announcement and one mirror call.
	sendTimeout = 15 * time.Second
)

// Errors the API maps to statuses.
var (
	// ErrRunning: one poll at a time, because "type 1" can only mean one
	// thing in a chat at once.
	ErrRunning = errors.New("a poll is already running; end it first")
	// ErrNoPoll: there is no running poll with that id.
	ErrNoPoll = errors.New("no poll with that id is running")
)

// Store is where polls are kept. *db.DB implements it.
type Store interface {
	CreatePoll(db.Poll) (int64, error)
	UpdatePoll(db.Poll) error
}

// Publisher is the events hub.
type Publisher interface {
	Publish(eventstream.Envelope)
}

// Sender posts into every connected chat. *chat.Hub implements it.
type Sender interface {
	Send(ctx context.Context, text string) ([]chat.SendResult, error)
}

// Mirror runs a poll as a platform's own native poll too.
type Mirror interface {
	// Start creates the native polls it can, one Mirrored per account it
	// considered, including the ones it skipped and why.
	Start(ctx context.Context, s Spec) []Mirrored
	// Read returns a native poll's running count, one per option. With end,
	// it closes the native poll first and returns the final count.
	Read(ctx context.Context, m Mirrored, end bool) ([]int, error)
}

// Mirrored is one native poll, or the reason there is none. Ref is the
// mirror's own handle and is never serialised.
type Mirrored struct {
	Result NativeResult
	Ref    any
}

// Option configures an Engine.
type Option func(*Engine)

// WithClock replaces time.Now.
func WithClock(now func() time.Time) Option { return func(e *Engine) { e.now = now } }

// WithSender announces polls in chat.
func WithSender(s Sender) Option { return func(e *Engine) { e.send = s } }

// WithMirror mirrors polls to native platform polls.
func WithMirror(m Mirror) Option { return func(e *Engine) { e.mirror = m } }

// WithLogger replaces the logger.
func WithLogger(l *slog.Logger) Option {
	return func(e *Engine) {
		if l != nil {
			e.log = l
		}
	}
}

// voter is one viewer on one platform. The platform's user id, never a name:
// a display name is not an identity.
type voter struct {
	platform db.Platform
	id       string
}

// msgRef is one chat message.
type msgRef struct {
	platform db.Platform
	account  string
	id       string
}

type vote struct {
	option int
	seq    uint64
	msg    msgRef
}

type held struct {
	voter    voter
	option   int
	seq      uint64
	deadline time.Time
}

// active is the running poll.
type active struct {
	spec Spec
	res  Result
	seq  uint64

	votes   map[voter]vote
	byMsg   map[msgRef]voter
	pending map[msgRef]held
	banned  map[voter]bool

	closeBy time.Time

	// mirror is the engine's mirror as it was when this poll started.
	mirror        Mirror
	mirrors       []Mirrored
	mirrorVotes   [][]int
	mirrorReading bool
	mirrorFinal   bool
	mirrorBy      time.Time
	nextMirror    time.Time

	dirty       bool
	lastVote    time.Time
	lastDisplay time.Time
}

// Engine counts one poll at a time. It implements chat.Tap.
type Engine struct {
	store  Store
	pub    Publisher
	send   Sender
	mirror Mirror
	log    *slog.Logger
	now    func() time.Time

	// mu is held across Publish so a poll's events are on the wire in the
	// order they happened. The events hub never calls out while holding its
	// own lock, so this cannot deadlock.
	mu  sync.Mutex
	cur *active
	wg  sync.WaitGroup
}

var _ chat.Tap = (*Engine)(nil)

// New builds an engine.
func New(store Store, pub Publisher, opts ...Option) *Engine {
	e := &Engine{store: store, pub: pub, log: slog.Default(), now: time.Now}
	for _, o := range opts {
		o(e)
	}
	return e
}

// SetMirror attaches the native-poll mirror, which is built after the engine
// because it reads accounts and tokens through the API server. Nil detaches.
func (e *Engine) SetMirror(m Mirror) {
	e.mu.Lock()
	e.mirror = m
	e.mu.Unlock()
}

// ------------------------------------------------------------- lifecycle

// Start opens a poll. It is announced in every chat, and mirrored where asked,
// in the background: a platform that is slow or refuses does not delay or fail
// the poll, it is reported on the result.
func (e *Engine) Start(s Spec) (Result, error) {
	s, err := s.Normalise()
	if err != nil {
		return Result{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cur != nil {
		return Result{}, ErrRunning
	}
	now := e.now()
	a := &active{
		spec:    s,
		votes:   map[voter]vote{},
		byMsg:   map[msgRef]voter{},
		pending: map[msgRef]held{},
		banned:  map[voter]bool{},
		res: Result{
			Question: s.Question, State: StateOpen, DurationSeconds: s.DurationSeconds,
			StartedAt: now, EndsAt: now.Add(s.Duration()),
		},
		mirrorFinal: true,
	}
	recount(a)
	id, err := e.store.CreatePoll(db.Poll{
		Question: s.Question, Options: s.Options, DurationS: s.DurationSeconds,
		State: string(StateOpen), StartedAt: now, EndsAt: a.res.EndsAt, Result: mustJSON(a.res),
	})
	if err != nil {
		return Result{}, err
	}
	a.res.ID = id
	e.cur = a
	a.lastVote, a.lastDisplay = now, now
	e.publishLocked(eventstream.TypePollStarted, a)
	e.publishDisplayLocked(a)

	if e.send != nil {
		e.goAsync(func(ctx context.Context) { e.announceStart(ctx, a) })
	}
	if s.MirrorTwitch && e.mirror != nil {
		a.mirror = e.mirror
		a.mirrorFinal = false
		e.goAsync(func(ctx context.Context) { e.startMirror(ctx, a) })
	}
	return snapshot(a), nil
}

// End closes the running poll early. Votes automod is still judging, and a
// native mirror's final count, are waited for briefly before the result is
// final; the returned result may therefore still say "closing".
func (e *Engine) End(id int64) (Result, error) {
	e.mu.Lock()
	a := e.cur
	if a == nil || a.res.ID != id {
		e.mu.Unlock()
		return Result{}, ErrNoPoll
	}
	if a.res.State == StateOpen {
		a.res.EndedEarly = true
		e.beginCloseLocked(a, e.now())
	}
	e.mu.Unlock()
	e.Tick()
	e.mu.Lock()
	defer e.mu.Unlock()
	return snapshot(a), nil
}

// Current returns the running poll, if there is one.
func (e *Engine) Current() (Result, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cur == nil {
		return Result{}, false
	}
	recount(e.cur)
	return snapshot(e.cur), true
}

// Run drives the clock until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Tick()
		}
	}
}

// Wait blocks until every background announcement and mirror call has
// returned. For tests and shutdown.
func (e *Engine) Wait() { e.wg.Wait() }

func (e *Engine) goAsync(fn func(ctx context.Context)) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		fn(ctx)
	}()
}

// Tick advances the running poll to now: expires holds, closes it at its
// time, finalises it once nothing is left to wait for, and sends the
// coalesced events.
func (e *Engine) Tick() {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.cur
	if a == nil {
		return
	}
	now := e.now()
	for k, h := range a.pending {
		if now.After(h.deadline) {
			delete(a.pending, k)
		}
	}
	if a.res.State == StateOpen && !now.Before(a.res.EndsAt) {
		e.beginCloseLocked(a, now)
	}
	if a.res.State == StateClosing {
		judged := len(a.pending) == 0 || now.After(a.closeBy)
		mirrored := a.mirrorFinal || now.After(a.mirrorBy)
		if judged && mirrored {
			e.finaliseLocked(a, now)
			return
		}
	}
	if a.res.State == StateOpen && len(a.mirrors) > 0 && !a.mirrorReading && !now.Before(a.nextMirror) {
		a.mirrorReading = true
		a.nextMirror = now.Add(mirrorEvery)
		mirrors := append([]Mirrored(nil), a.mirrors...)
		e.goAsync(func(ctx context.Context) { e.readMirrors(ctx, a, mirrors, false) })
	}
	if a.dirty && now.Sub(a.lastVote) >= voteEvery {
		a.dirty = false
		a.lastVote, a.lastDisplay = now, now
		recount(a)
		e.publishLocked(eventstream.TypePollVote, a)
		e.publishDisplayLocked(a)
	} else if now.Sub(a.lastDisplay) >= displayEvery {
		a.lastDisplay = now
		e.publishDisplayLocked(a)
	}
}

func (e *Engine) beginCloseLocked(a *active, now time.Time) {
	a.res.State = StateClosing
	a.closeBy = now.Add(PendingGrace)
	a.mirrorBy = now.Add(mirrorGrace)
	// A mirror still being created is ended by startMirror, which sees the
	// poll is no longer open; one already running is ended here.
	if len(a.mirrors) > 0 && !a.mirrorFinal {
		mirrors := append([]Mirrored(nil), a.mirrors...)
		e.goAsync(func(ctx context.Context) { e.readMirrors(ctx, a, mirrors, true) })
	}
	recount(a)
	e.publishDisplayLocked(a)
}

func (e *Engine) finaliseLocked(a *active, now time.Time) {
	a.pending = map[msgRef]held{}
	a.res.State = StateEnded
	ended := now
	a.res.EndedAt = &ended
	recount(a)
	if err := e.store.UpdatePoll(db.Poll{ID: a.res.ID, State: string(StateEnded), EndedAt: now, Result: mustJSON(a.res)}); err != nil {
		e.log.Error("polls: store the result", "poll", a.res.ID, "err", err)
	}
	e.publishLocked(eventstream.TypePollEnded, a)
	e.publishDisplayLocked(a)
	e.cur = nil
	if e.send != nil {
		res := snapshot(a)
		e.goAsync(func(ctx context.Context) { e.sendText(ctx, EndText(res)) })
	}
}

// ------------------------------------------------------------- publishing

func (e *Engine) publishLocked(t eventstream.Type, a *active) {
	if e.pub == nil {
		return
	}
	e.pub.Publish(eventstream.Envelope{Type: t, At: e.now(), Data: snapshot(a)})
}

func (e *Engine) publishDisplayLocked(a *active) {
	if e.pub == nil {
		return
	}
	e.pub.Publish(eventstream.Envelope{Type: eventstream.TypePollDisplay, At: e.now(), Data: snapshot(a).Display()})
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

// recount rebuilds the counts from the votes held, plus every native mirror's
// latest count. From scratch every time -- at most once a second -- so a
// moderation action that removes a vote can never leave a count it forgot to
// decrement.
func recount(a *active) {
	n := len(a.spec.Options)
	opts := make([]OptionResult, n)
	for i, l := range a.spec.Options {
		opts[i] = OptionResult{Label: l, ByPlatform: map[string]int{}}
	}
	total := 0
	by := map[string]int{}
	for v, b := range a.votes {
		p := string(v.platform)
		opts[b.option].Votes++
		opts[b.option].ByPlatform[p]++
		by[p]++
		total++
	}
	for i, m := range a.mirrors {
		if m.Result.Status != NativeMirrored || i >= len(a.mirrorVotes) || len(a.mirrorVotes[i]) != n {
			continue
		}
		p := m.Result.Platform
		for k, c := range a.mirrorVotes[i] {
			if c <= 0 {
				continue
			}
			opts[k].Votes += c
			opts[k].ByPlatform[p] += c
			by[p] += c
			total += c
		}
	}
	a.res.Options, a.res.Total, a.res.ByPlatform = opts, total, by
	a.res.Native = nil
	for i, m := range a.mirrors {
		r := m.Result
		if i < len(a.mirrorVotes) {
			r.Votes = append([]int(nil), a.mirrorVotes[i]...)
		}
		a.res.Native = append(a.res.Native, r)
	}
}

// snapshot is a deep copy of the result, safe to hand outside the lock.
func snapshot(a *active) Result {
	r := a.res
	r.Options = make([]OptionResult, len(a.res.Options))
	for i, o := range a.res.Options {
		bp := make(map[string]int, len(o.ByPlatform))
		for k, v := range o.ByPlatform {
			bp[k] = v
		}
		r.Options[i] = OptionResult{Label: o.Label, Votes: o.Votes, ByPlatform: bp}
	}
	r.ByPlatform = make(map[string]int, len(a.res.ByPlatform))
	for k, v := range a.res.ByPlatform {
		r.ByPlatform[k] = v
	}
	r.Announced = append([]Announcement(nil), a.res.Announced...)
	r.Native = nil
	for _, n := range a.res.Native {
		n.Votes = append([]int(nil), n.Votes...)
		r.Native = append(r.Native, n)
	}
	if a.res.EndedAt != nil {
		t := *a.res.EndedAt
		r.EndedAt = &t
	}
	return r
}

// ------------------------------------------------------------- chat side

func (e *Engine) announceStart(ctx context.Context, a *active) {
	results := e.sendText(ctx, StartText(a.spec))
	e.mu.Lock()
	defer e.mu.Unlock()
	a.res.Announced = results
}

func (e *Engine) sendText(ctx context.Context, text string) []Announcement {
	res, err := e.send.Send(ctx, text)
	if err != nil {
		e.log.Warn("polls: announce", "err", err)
		return []Announcement{{OK: false, Detail: alerts.Redact(err.Error())}}
	}
	out := make([]Announcement, 0, len(res))
	for _, r := range res {
		out = append(out, Announcement{Platform: string(r.Platform), OK: r.OK, Skipped: r.Skipped, Detail: alerts.Redact(r.Detail)})
	}
	return out
}

func (e *Engine) startMirror(ctx context.Context, a *active) {
	ms := a.mirror.Start(ctx, a.spec)
	e.mu.Lock()
	open := e.cur == a && a.res.State == StateOpen
	if open {
		a.mirrors = ms
		a.mirrorVotes = make([][]int, len(ms))
		a.mirrorFinal = len(live(ms)) == 0
		a.nextMirror = e.now()
		recount(a)
		a.dirty = true
	} else {
		// Closed while the mirror was being created. Record the outcomes,
		// end what was created, and let the poll finalise.
		a.mirrors = ms
		a.mirrorVotes = make([][]int, len(ms))
	}
	e.mu.Unlock()
	if !open {
		e.readMirrors(ctx, a, ms, true)
	}
}

func live(ms []Mirrored) []Mirrored {
	var out []Mirrored
	for _, m := range ms {
		if m.Result.Status == NativeMirrored {
			out = append(out, m)
		}
	}
	return out
}

// readMirrors reads every live mirror's count, ending it first when end is
// set. A failed read keeps the last count it had.
func (e *Engine) readMirrors(ctx context.Context, a *active, ms []Mirrored, end bool) {
	counts := make([][]int, len(ms))
	for i, m := range ms {
		if m.Result.Status != NativeMirrored {
			continue
		}
		c, err := a.mirror.Read(ctx, m, end)
		if err != nil {
			e.log.Warn("polls: read native poll", "platform", m.Result.Platform, "err", alerts.Redact(err.Error()))
			continue
		}
		counts[i] = c
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, c := range counts {
		if c != nil && i < len(a.mirrorVotes) {
			a.mirrorVotes[i] = c
		}
	}
	a.mirrorReading = false
	if end {
		a.mirrorFinal = true
	}
	if e.cur == a {
		recount(a)
		a.dirty = true
	}
}

// Offer implements chat.Tap.
func (e *Engine) Offer(m chat.Message, d chat.Display) {
	if m.Echo || m.Author.ID == "" {
		// Our own announcement, and a message with no stable author: neither
		// can be one viewer's one vote.
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.cur
	if a == nil || a.res.State != StateOpen {
		return
	}
	now := e.now()
	if !now.Before(a.res.EndsAt) {
		return
	}
	k, ok := ParseVote(m.Text, len(a.spec.Options))
	if !ok {
		return
	}
	v := voter{platform: m.Platform, id: m.Author.ID}
	if a.banned[v] {
		return
	}
	a.seq++
	ref := msgRef{platform: m.Platform, account: m.Account, id: m.ID}
	switch d {
	case chat.DisplayClear:
		e.applyLocked(a, v, vote{option: k, seq: a.seq, msg: ref})
	case chat.DisplayPending:
		a.pending[ref] = held{voter: v, option: k, seq: a.seq, deadline: now.Add(PendingGrace)}
	default:
		// Blocked, and anything this build does not know: not a vote.
	}
}

// applyLocked makes v the voter's vote unless they have already cast a later
// one -- a vote the model held for two seconds must not overwrite the vote the
// same viewer typed after it.
func (e *Engine) applyLocked(a *active, who voter, v vote) {
	if old, ok := a.votes[who]; ok {
		if old.seq > v.seq {
			return
		}
		delete(a.byMsg, old.msg)
	}
	a.votes[who] = v
	a.byMsg[v.msg] = who
	a.dirty = true
}

// Judge implements chat.Tap.
func (e *Engine) Judge(p db.Platform, account, id string, d chat.Display) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.cur
	if a == nil {
		return
	}
	ref := msgRef{platform: p, account: account, id: id}
	h, ok := a.pending[ref]
	if !ok {
		return
	}
	delete(a.pending, ref)
	if d != chat.DisplayClear || e.now().After(h.deadline) || a.banned[h.voter] {
		return
	}
	e.applyLocked(a, h.voter, vote{option: h.option, seq: h.seq, msg: ref})
}

// RetractMessage implements chat.Tap: a deleted or hidden vote message is no
// longer a vote.
func (e *Engine) RetractMessage(p db.Platform, account, id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.cur
	if a == nil {
		return
	}
	ref := msgRef{platform: p, account: account, id: id}
	delete(a.pending, ref)
	if who, ok := a.byMsg[ref]; ok {
		delete(a.byMsg, ref)
		delete(a.votes, who)
		a.dirty = true
	}
}

// RetractAuthor implements chat.Tap: a banned or timed-out viewer's vote is
// void, and they are ignored for the rest of the poll.
func (e *Engine) RetractAuthor(p db.Platform, _ string, authorID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.cur
	if a == nil || authorID == "" {
		return
	}
	who := voter{platform: p, id: authorID}
	a.banned[who] = true
	if v, ok := a.votes[who]; ok {
		delete(a.byMsg, v.msg)
		delete(a.votes, who)
		a.dirty = true
	}
	for ref, h := range a.pending {
		if h.voter == who {
			delete(a.pending, ref)
		}
	}
}

// ClearRoom implements chat.Tap. A cleared room is housekeeping, not a verdict
// on any message in it: every vote stands.
func (e *Engine) ClearRoom(db.Platform, string) {}
