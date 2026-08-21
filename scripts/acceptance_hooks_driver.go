//go:build ignore

// Driver for acceptance-hooks.sh.
//
// EVERY COMMAND HERE PUTS A REAL SOCKET UNDER THE DISPATCHER. That is the
// entire difference between this and internal/hooks' own tests, which are good
// and which nothing below duplicates for its own sake. Those tests hand the
// Dispatcher a WithDoer that returns a hand-built *http.Response, a WithSleep
// that returns instantly and, where they need one, a WithClock. Every option is
// deliberately absent here: the Dispatcher gets its default &http.Client{}, its
// default sleepCtx and time.Now, and the far end is an http.Server listening on
// a loopback port.
//
// WHAT THAT BUYS, AND IT IS SPECIFIC.
//
//	A FAKE DOER CANNOT PRODUCE A *url.Error. It returns whatever error the test
//	wrote, so the one string that has ever leaked a webhook credential in this
//	codebase -- net/http's `Post "https://host/PATH-IS-THE-SECRET": dial tcp
//	...` -- is a string no unit test in this package has ever constructed. The
//	whole of dispatch.go's three-pass scrub (ClientErrorText, then the hook's
//	own SecretSet, then Redact as the residual) exists for that string, and
//	until now nothing had ever run it against one. Steps 5-6 do, with a control
//	that first proves the raw error really does carry the secret -- because a
//	redaction check whose input was already clean is the exact vacuous guard
//	this repo keeps finding.
//
//	A FAKE SLEEP CANNOT MEASURE BACKOFF. TestA503IsRetried counts three
//	attempts with the wait stubbed to return true immediately, so backoffBase
//	could be a nanosecond and it would still pass. Step 3 measures the gaps on
//	a wall clock.
//
//	A HAND-BUILT RESPONSE CANNOT PROVE THE BYTES SURVIVED THE WIRE. The
//	signature is an HMAC over the exact body, and step 2 recomputes it from the
//	bytes an independent HTTP server received, with the header names written out
//	as literals rather than taken from the package's own constants.
//
// NOTHING SECRET IS EVER PRINTED. The two credentials each command plants -- a
// path segment standing in for a Slack or Discord webhook's tail, and a signing
// key -- are minted from crypto/rand at run time and leave this process only as
// booleans and lengths.
//
//	go run scripts/acceptance_hooks_driver.go <cmd>
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: acceptance_hooks_driver.go <cmd>")
	}
	switch os.Args[1] {
	case "deliver":
		deliver()
	case "retry":
		retry()
	case "permanent":
		permanent()
	case "transport":
		transportTimeout()
		transportRefused()
	case "echo":
		echoBack()
	case "testbutton":
		testButton()
	case "remote":
		remote()
	default:
		fail("unknown command %q", os.Args[1])
	}
}

func fail(f string, a ...any) {
	fmt.Printf("ERR "+f+"\n", a...)
	os.Exit(1)
}

// emit prints one key=value result line. A command that cannot answer prints
// nothing rather than a plausible zero, and the suite treats a missing key as a
// failure rather than as a false.
func emit(k string, v any) { fmt.Printf("%s=%v\n", k, v) }

// The delivery headers, as LITERALS rather than as hooks.SignatureHeader and
// friends. A receiver is written against the strings, so taking them from the
// package under test would make the contract self-consistent by construction
// and a rename would pass in silence.
const (
	hSig      = "X-Polyemesis-Signature"
	hTS       = "X-Polyemesis-Timestamp"
	hTrigger  = "X-Polyemesis-Trigger"
	hDelivery = "X-Polyemesis-Delivery"
	hSequence = "X-Polyemesis-Sequence"
)

// ------------------------------------------------------------- the far end

// hit is one request as the endpoint saw it, which is the only place in this
// driver where the wire is observed. Recorded before the reply is written, so a
// handler that then sleeps still counts as having received it.
type hit struct {
	at     time.Time
	method string
	path   string
	header http.Header
	body   []byte
}

// farEnd is a real HTTP server on a loopback port.
type farEnd struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []hit
}

// deliverOnce is the four steps every delivery check opens with: stand up a far
// end with the given reply, point a dispatcher at it, publish one event, and
// wait for the delivery to be recorded.
//
// A function rather than the fifth copy of the same twenty lines. The copies
// were not merely repetitive, they were misleading: four blocks differing only
// in their reply invite the reader to diff them for a difference in the SETUP,
// and there was never one. What actually differs is what each check measures
// afterwards, which is what the callback is for.
//
// The wait is on the RECORD rather than on Stats.Sent, and that is load-bearing:
// deliver bumps Sent before it appends the record, so waiting on the counter can
// read the log one entry short.
//
// A false return means the delivery never landed. Callers emit what they can and
// stop rather than measuring an event that did not happen.
func deliverOnce(pathSecret, signSecret string,
	reply func(w http.ResponseWriter, r *http.Request, n int),
	measure func(far *farEnd, r *rig)) {
	far := newFarEnd(reply)
	defer far.close()

	rn := start(hooks.Hook{
		ID: 1, Name: "acceptance", Enabled: true,
		URL: far.srv.URL + "/webhook/" + pathSecret, Secret: signSecret,
		TimeoutSeconds: 5, MaxAttempts: 3,
	}.Normalized())
	defer rn.stop()

	rn.d.Publish(hooks.Event{
		Trigger: hooks.TriggerIngestPublished,
		Source:  hooks.SourceRef{ID: 7, Name: "Main"},
	})
	if !waitFor(30*time.Second, func() bool { return len(rn.d.Deliveries(1)) == 1 }) {
		emit("attempts", far.count())
		return
	}
	measure(far, rn)
}

// newFarEnd starts the server. reply is handed the attempt number, one-based,
// so a handler can fail the first two and accept the third without keeping its
// own counter.
func newFarEnd(reply func(w http.ResponseWriter, r *http.Request, n int)) *farEnd {
	f := &farEnd{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.got = append(f.got, hit{
			at: time.Now(), method: r.Method, path: r.URL.Path,
			header: r.Header.Clone(), body: body,
		})
		n := len(f.got)
		f.mu.Unlock()
		reply(w, r, n)
	}))
	return f
}

func (f *farEnd) seen() []hit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]hit(nil), f.got...)
}

func (f *farEnd) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func (f *farEnd) close() { f.srv.Close() }

// ------------------------------------------------------------ the near end

// safeBuf collects the dispatcher's own log. Guarded because deliver() runs on
// a worker goroutine and this is read from main's.
//
// The log is CAPTURED RATHER THAN DISCARDED because #310 was exactly this: a
// refused destination wrote its stream key to server.log on every retry. A
// scrub that covers the API response and not the log file has moved the
// disclosure rather than closed it.
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// rig is a running Dispatcher with its log in reach.
type rig struct {
	d      *hooks.Dispatcher
	logs   *safeBuf
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// start runs a Dispatcher over one hook, with NO option that replaces a moving
// part. Only the reload interval is shortened, because five seconds of waiting
// for the hook to be picked up would be five seconds added to every step and it
// is not the thing under test.
func start(h hooks.Hook) *rig {
	// EVERY hook in this driver points at an httptest server on 127.0.0.1,
	// which the SSRF guard refuses by default (poka-yoke audit #4). Set here
	// rather than on each of the five literals below, because it is one fact
	// about this whole file -- these endpoints are local ON PURPOSE -- and a
	// per-literal flag is one more thing a sixth case can forget, which is how
	// the suite would go quietly green while delivering nothing.
	h.AllowPrivateTarget = true
	logs := &safeBuf{}
	d := hooks.NewDispatcher(
		slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		hooks.SourceFunc(func() ([]hooks.Hook, error) { return []hooks.Hook{h}, nil }),
		hooks.WithReloadInterval(10*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	r := &rig{d: d, logs: logs, cancel: cancel, done: make(chan struct{})}
	go func() { defer close(r.done); d.Run(ctx) }()
	if !waitFor(10*time.Second, d.HasHooks) {
		fail("the dispatcher never started a worker for the hook")
	}
	return r
}

func (r *rig) stop() {
	r.once.Do(func() {
		r.cancel()
		<-r.done
	})
}

// waitFor polls rather than sleeping a fixed interval, so a loaded runner is
// slow rather than red.
func waitFor(limit time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// plant mints a credential for this run only. Never a constant and never an
// argument: a literal in this file is a committed secret whatever it protects,
// and argv is world-readable through ps(1).
func plant() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		fail("cannot mint a test credential: %v", err)
	}
	return hex.EncodeToString(buf)
}

func mustSecret() string {
	s, err := hooks.NewSecret()
	if err != nil {
		fail("cannot mint a signing secret: %v", err)
	}
	return s
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// rawErrorCarries is the CONTROL for every redaction check below, and without
// it none of them proves anything.
//
// It makes the same request with a bare http.Client and reports whether the
// error net/http hands back contains the planted secret. If that is false the
// suite's "the recorded error carried no secret" checks are passing on an input
// that never had one, which is the vacuous guard this driver is written to
// avoid rather than demonstrate.
func rawErrorCarries(rawURL string, timeout time.Duration, secret string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader("{}"))
	if err != nil {
		return false
	}
	resp, err := (&http.Client{}).Do(req)
	if resp != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), secret)
}

// ------------------------------------------------------------------ steps

// deliver posts one ordinary lifecycle event to a listening endpoint and
// reports what arrived: the request line, the headers, the envelope a receiver
// would decode, and whether the signature recomputed off the wire agrees.
func deliver() {
	pathSecret, signSecret := plant(), mustSecret()
	far := newFarEnd(func(w http.ResponseWriter, r *http.Request, n int) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	defer far.close()

	endpoint := far.srv.URL + "/webhook/" + pathSecret
	r := start(hooks.Hook{
		ID: 1, Name: "acceptance", Enabled: true,
		URL: endpoint, Secret: signSecret,
		TimeoutSeconds: 5, MaxAttempts: 1,
	}.Normalized())
	defer r.stop()

	r.d.Publish(hooks.Event{
		Trigger: hooks.TriggerIngestPublished,
		Source:  hooks.SourceRef{ID: 7, Name: "Main"},
		Reason:  "data is arriving on the ingest",
	})
	if !waitFor(15*time.Second, func() bool { return far.count() >= 1 }) {
		emit("got", 0)
		return
	}
	got := far.seen()[0]
	emit("got", far.count())
	emit("method", got.method)
	emit("pathMatches", got.path == "/webhook/"+pathSecret)
	emit("contentType", got.header.Get("Content-Type"))
	emit("userAgent", got.header.Get("User-Agent"))

	present := 0
	for _, k := range []string{hSig, hTS, hTrigger, hDelivery, hSequence} {
		if got.header.Get(k) != "" {
			present++
		}
	}
	emit("polyHeaders", present)

	// Decoded into a struct declared HERE, not into hooks.Envelope. Decoding
	// into the package's own type would follow a renamed JSON tag wherever it
	// went and report a broken contract as intact.
	var env struct {
		SpecVersion string `json:"specVersion"`
		ID          string `json:"id"`
		Trigger     string `json:"trigger"`
		Sequence    uint64 `json:"sequence"`
		At          string `json:"at"`
		Reason      string `json:"reason"`
		Source      struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"source"`
	}
	if err := json.Unmarshal(got.body, &env); err != nil {
		emit("decoded", false)
		return
	}
	emit("decoded", true)
	emit("specVersion", env.SpecVersion)
	emit("trigger", env.Trigger)
	emit("sequence", env.Sequence)
	emit("sourceID", env.Source.ID)
	emit("sourceName", env.Source.Name)
	emit("idLen", len(env.ID))
	_, hexErr := hex.DecodeString(env.ID)
	emit("idIsHex", hexErr == nil)
	at, atErr := time.Parse(time.RFC3339, env.At)
	emit("atParses", atErr == nil)
	emit("atIsUTC", strings.HasSuffix(env.At, "Z"))
	emit("atSaneMs", absMs(time.Since(at)))

	// Recomputed the way the documentation tells a receiver to: HMAC-SHA256 over
	// the timestamp header, a dot, and the body bytes, keyed by the shared
	// secret. Nothing from internal/hooks is involved.
	mac := hmac.New(sha256.New, []byte(signSecret))
	mac.Write([]byte(got.header.Get(hTS)))
	mac.Write([]byte("."))
	mac.Write(got.body)
	want := "v1=" + hex.EncodeToString(mac.Sum(nil))
	emit("sigVerifies", hmac.Equal([]byte(want), []byte(got.header.Get(hSig))))

	// A signature verifies over whatever timestamp was sent, including one from
	// 1970. The header has to be a plausible now, or a receiver's replay window
	// rejects every delivery.
	ts, tsErr := strconv.ParseInt(got.header.Get(hTS), 10, 64)
	emit("tsParses", tsErr == nil)
	emit("tsSkewMs", absMs(time.Since(time.Unix(ts, 0))))

	// The signing key is the shared secret; it must be PROVEN by the signature,
	// never carried. The path credential is in the request target by
	// construction -- it is the endpoint -- but has no business in a header
	// value or in the body.
	inHeader, pathInHeader := false, false
	for _, vs := range got.header {
		for _, v := range vs {
			if strings.Contains(v, signSecret) {
				inHeader = true
			}
			if strings.Contains(v, pathSecret) {
				pathInHeader = true
			}
		}
	}
	emit("signSecretInHeader", inHeader)
	emit("pathSecretInHeader", pathInHeader)
	emit("signSecretInBody", strings.Contains(string(got.body), signSecret))
	emit("pathSecretInBody", strings.Contains(string(got.body), pathSecret))
}

func absMs(d time.Duration) int64 {
	if d < 0 {
		d = -d
	}
	return d.Milliseconds()
}

// retry drives the retryable branch against an endpoint that answers 503 twice
// and then 200, on a real clock with the real sleep.
//
// The gaps are the point. TestA503IsRetried already counts the attempts, with
// WithSleep stubbed to return true immediately -- so backoffBase could be a
// nanosecond and it would stay green. Here the waits are the ones a retried
// endpoint actually experiences.
func retry() {
	pathSecret, signSecret := plant(), mustSecret()
	far := newFarEnd(func(w http.ResponseWriter, r *http.Request, n int) {
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "try later")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	defer far.close()

	r := start(hooks.Hook{
		ID: 1, Name: "acceptance", Enabled: true,
		URL: far.srv.URL + "/webhook/" + pathSecret, Secret: signSecret,
		TimeoutSeconds: 5, MaxAttempts: 3,
	}.Normalized())
	defer r.stop()

	r.d.Publish(hooks.Event{
		Trigger: hooks.TriggerIngestPublished,
		Source:  hooks.SourceRef{ID: 7, Name: "Main"},
	})
	// Waited on the RECORD rather than on Stats.Sent: deliver bumps Sent before
	// it appends the record, so waiting on the counter can read the log one
	// entry short.
	if !waitFor(30*time.Second, func() bool { return len(r.d.Deliveries(1)) == 1 }) {
		emit("attempts", far.count())
		return
	}
	hits := far.seen()
	emit("attempts", len(hits))
	if len(hits) >= 3 {
		emit("gap1Ms", hits[1].at.Sub(hits[0].at).Milliseconds())
		emit("gap2Ms", hits[2].at.Sub(hits[1].at).Milliseconds())
		// A receiver deduplicating by delivery id needs every attempt at one
		// event to carry the SAME id and the same sequence. Three ids for one
		// transition looks to a script like three transitions.
		//
		// blanks IS NOT DECORATION. Counting distinct values alone made this
		// pass when the header was deleted outright -- three empty strings are
		// one distinct value -- which was caught by running the drop-the-header
		// mutation and watching only step 1 go red.
		ids, seqs, blanks := map[string]bool{}, map[string]bool{}, 0
		for _, x := range hits {
			id, seq := x.header.Get(hDelivery), x.header.Get(hSequence)
			if id == "" || seq == "" {
				blanks++
			}
			ids[id] = true
			seqs[seq] = true
		}
		emit("distinctIDs", len(ids))
		emit("distinctSeqs", len(seqs))
		emit("blankIDOrSeq", blanks)
	}
	rec := r.d.Deliveries(1)[0]
	emit("recAttempts", rec.Attempts)
	emit("recStatus", rec.Status)
	s := r.d.Stats()
	emit("sent", s.Sent)
	emit("retries", s.Retries)
	emit("failed", s.Failed)
}

// permanent is the other half of the classification: a 404 is the endpoint
// saying the request is wrong, and retrying it only delays everything queued
// behind it on the same endpoint.
func permanent() {
	pathSecret, signSecret := plant(), mustSecret()
	deliverOnce(pathSecret, signSecret,
		func(w http.ResponseWriter, r *http.Request, n int) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "no such webhook")
		},
		func(far *farEnd, r *rig) {
			// A moment's grace before counting, so "did not retry" is a
			// measurement rather than a race won. Backoff before a second
			// attempt would be a whole second, so a tenth of one is ample and is
			// not a guess at a delivery time.
			time.Sleep(100 * time.Millisecond)
			emit("attempts", far.count())
			rec := r.d.Deliveries(1)[0]
			emit("recAttempts", rec.Attempts)
			emit("recStatus", rec.Status)
			s := r.d.Stats()
			emit("failed", s.Failed)
			emit("retries", s.Retries)
			emit("sent", s.Sent)
		})
}

// transportTimeout points a one-second hook at an endpoint that never answers.
//
// TWO THINGS AT ONCE, because they cannot be separated: the delivery has to be
// abandoned on OUR deadline rather than on the endpoint's whim, and the error
// that records it is a *url.Error carrying the full endpoint URL -- which is
// the string dispatch.go's three-pass scrub exists for and which no unit test
// in the package has ever produced.
func transportTimeout() {
	pathSecret, signSecret := plant(), mustSecret()
	far := newFarEnd(func(w http.ResponseWriter, r *http.Request, n int) {
		// Six seconds against a one-second hook, released early when the client
		// hangs up so the server does not hold the process open afterwards.
		select {
		case <-time.After(6 * time.Second):
		case <-r.Context().Done():
		}
	})
	defer far.close()

	endpoint := far.srv.URL + "/webhook/" + pathSecret
	r := start(hooks.Hook{
		ID: 1, Name: "acceptance", Enabled: true,
		URL: endpoint, Secret: signSecret,
		TimeoutSeconds: 1, MaxAttempts: 1,
	}.Normalized())
	defer r.stop()

	started := time.Now()
	r.d.Publish(hooks.Event{
		Trigger: hooks.TriggerIngestPublished,
		Source:  hooks.SourceRef{ID: 7, Name: "Main"},
	})
	if !waitFor(30*time.Second, func() bool { return len(r.d.Deliveries(1)) == 1 }) {
		emit("timeoutRecs", 0)
		return
	}
	emit("timeoutRecs", 1)
	rec := r.d.Deliveries(1)[0]
	emit("timeoutObservedMs", time.Since(started).Milliseconds())
	emit("timeoutRecordedMs", rec.DurationMS)
	emit("timeoutErrLen", len(rec.Error))
	emit("timeoutErrHasSecret", strings.Contains(rec.Error, pathSecret))
	// The diagnostic has to SURVIVE the scrub. Blanking the whole error would
	// pass a "no secret" check and turn every support conversation into "it says
	// delivery failed" -- which is the trade alerts.ClientErrorText's own doc
	// comment refuses to make.
	emit("timeoutErrNamesHost", strings.Contains(rec.Error, hostOf(endpoint)))

	st := r.d.Stats()
	emit("timeoutLastErrLen", len(st.LastError))
	emit("timeoutLastErrHasSecret", strings.Contains(st.LastError, pathSecret))

	// The whole log as GET /api/v1/hooks/{id}/deliveries serialises it. BEFORE
	// r.stop(), because stopAll deletes the worker and Deliveries then returns
	// an empty slice -- which is how this was first written, and `[]` contains
	// no credential, so it reported SAFE without ever looking at a record.
	blob, mErr := json.Marshal(r.d.Deliveries(1))
	emit("timeoutDeliveriesJSONLen", len(blob))
	emit("timeoutDeliveriesJSONHasHost", mErr == nil && bytes.Contains(blob, []byte(hostOf(endpoint))))
	emit("timeoutDeliveriesJSONHasSecret", mErr == nil && bytes.Contains(blob, []byte(pathSecret)))

	r.stop() // flush the worker before reading its log
	logs := r.logs.String()
	emit("timeoutLogLen", len(logs))
	emit("timeoutLogSaysFailed", strings.Contains(logs, "hook delivery failed"))
	emit("timeoutLogHasSecret", strings.Contains(logs, pathSecret))

	emit("timeoutRawHasSecret", rawErrorCarries(endpoint, time.Second, pathSecret))
}

// transportRefused is the other transport failure, and a different error inside
// the same wrapper: nothing is listening at all.
//
// The port is one the kernel handed us and we immediately gave back, so it is
// free at the moment of asking. If something else claimed it in between, the
// control below reports false and the suite fails the control rather than
// quietly passing the checks that depend on it.
func transportRefused() {
	pathSecret, signSecret := plant(), mustSecret()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail("cannot reserve a port to close: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	endpoint := "http://" + addr + "/webhook/" + pathSecret
	r := start(hooks.Hook{
		ID: 1, Name: "acceptance", Enabled: true,
		URL: endpoint, Secret: signSecret,
		TimeoutSeconds: 2, MaxAttempts: 1,
	}.Normalized())
	defer r.stop()

	r.d.Publish(hooks.Event{
		Trigger: hooks.TriggerIngestPublished,
		Source:  hooks.SourceRef{ID: 7, Name: "Main"},
	})
	if !waitFor(30*time.Second, func() bool { return len(r.d.Deliveries(1)) == 1 }) {
		emit("refusedRecs", 0)
		return
	}
	emit("refusedRecs", 1)
	rec := r.d.Deliveries(1)[0]
	emit("refusedErrLen", len(rec.Error))
	emit("refusedErrHasSecret", strings.Contains(rec.Error, pathSecret))
	emit("refusedErrNamesHost", strings.Contains(rec.Error, addr))

	st := r.d.Stats()
	emit("refusedLastErrLen", len(st.LastError))
	emit("refusedLastErrHasSecret", strings.Contains(st.LastError, pathSecret))

	r.stop()
	logs := r.logs.String()
	emit("refusedLogLen", len(logs))
	emit("refusedLogSaysFailed", strings.Contains(logs, "hook delivery failed"))
	emit("refusedLogHasSecret", strings.Contains(logs, pathSecret))

	// The hook as the API would serialise it. Hook.MarshalJSON substitutes
	// RedactedURL and a hasSecret boolean; this is the rendering a settings page
	// receives, so a regression here is a credential on somebody's screen.
	blob, err := json.Marshal(hooks.Hook{
		ID: 1, Name: "acceptance", Enabled: true, URL: endpoint, Secret: signSecret,
	}.Normalized())
	if err != nil {
		fail("marshalling a hook: %v", err)
	}
	emit("hookJSONHasPathSecret", bytes.Contains(blob, []byte(pathSecret)))
	emit("hookJSONHasSignSecret", bytes.Contains(blob, []byte(signSecret)))
	emit("hookJSONNamesHost", bytes.Contains(blob, []byte(addr)))

	emit("refusedRawHasSecret", rawErrorCarries(endpoint, 2*time.Second, pathSecret))
}

// echoBack is the credential class the transport scrub cannot reach: one the
// ENDPOINT chose to quote back in its own body.
//
// The body below is JSON, which is the shape alerts.Redact is worst at -- its
// own doc records that it cannot see inside one -- so anything that survives
// here survived because the per-hook SecretSet ran, not because the generic
// redactor got lucky. It quotes both credentials, since an endpoint that
// rejects a signature has both to hand.
func echoBack() {
	pathSecret, signSecret := plant(), mustSecret()
	far := newFarEnd(func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"detail":"endpoint retired","echo":"/webhook/`+
			pathSecret+`","signing":"`+signSecret+`"}`)
	})
	defer far.close()

	r := start(hooks.Hook{
		ID: 1, Name: "acceptance", Enabled: true,
		URL: far.srv.URL + "/webhook/" + pathSecret, Secret: signSecret,
		TimeoutSeconds: 5, MaxAttempts: 1,
	}.Normalized())
	defer r.stop()

	r.d.Publish(hooks.Event{
		Trigger: hooks.TriggerIngestPublished,
		Source:  hooks.SourceRef{ID: 7, Name: "Main"},
	})
	if !waitFor(30*time.Second, func() bool { return len(r.d.Deliveries(1)) == 1 }) {
		emit("recs", 0)
		return
	}
	emit("recs", 1)
	rec := r.d.Deliveries(1)[0]
	emit("respLen", len(rec.Response))
	// Non-vacuity, in one line: the endpoint's own words have to still be there.
	// An empty Response would pass both secret checks below and tell the
	// operator nothing about why their endpoint said no.
	emit("respKeptText", strings.Contains(rec.Response, "endpoint retired"))
	emit("respHasPathSecret", strings.Contains(rec.Response, pathSecret))
	emit("respHasSignSecret", strings.Contains(rec.Response, signSecret))
	emit("recStatus", rec.Status)
}

// testButton drives Dispatcher.Test, which is the path the operator's "send a
// test" button takes. It skips the queue and the subscription filter, so
// nothing above covers it.
//
// Both halves: what a working endpoint returns, and -- the part that matters --
// what a BROKEN one returns, because handleTestHook puts that error into a 502
// body and the error is a *url.Error carrying the whole endpoint URL.
func testButton() {
	pathSecret, signSecret := plant(), mustSecret()
	far := newFarEnd(func(w http.ResponseWriter, r *http.Request, n int) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "accepted")
	})
	defer far.close()

	logs := &safeBuf{}
	d := hooks.NewDispatcher(
		slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		hooks.SourceFunc(func() ([]hooks.Hook, error) { return nil, nil }),
	)

	good := hooks.Hook{
		ID: 1, Name: "acceptance", Enabled: true,
		URL: far.srv.URL + "/webhook/" + pathSecret, Secret: signSecret,
		TimeoutSeconds: 5, MaxAttempts: 1,
		// Built here rather than through start(), so it needs the local-target
		// opt-in of its own. far.srv is httptest on 127.0.0.1.
		AllowPrivateTarget: true,
	}.Normalized()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := d.Test(ctx, good, hooks.TriggerIngestPublished)
	emit("testFailed", err != nil)
	emit("testStatus", res.Status)
	emit("testGot", far.count())
	if far.count() >= 1 {
		wire := far.seen()[0]
		// The operator is handed Body and Signature to check their verifier
		// against. If Body is not the bytes that were sent, that exercise is a
		// lie and their verifier will disagree with every real delivery.
		emit("testBodyMatchesWire", res.Body == string(wire.body))
		emit("testMarkedTest", strings.Contains(res.Body, `"test":true`))

		// A ONE-SECOND WINDOW, AND THAT IS A FINDING RATHER THAN A CONVENIENCE.
		//
		// Test computes the signature it reports from a SECOND d.now().Unix()
		// call, after attempt has already signed and sent with its own. The two
		// disagree whenever the round trip crosses a second boundary, and
		// TestResult carries no timestamp field, so the operator cannot tell
		// which second the reported signature was over -- or verify it at all
		// without guessing.
		//
		// Asserting exact equality with the wire would therefore be a check that
		// fails roughly once in a thousand runs for a reason nobody changed,
		// which is how a suite teaches people to re-run it. The window keeps the
		// failure modes that matter -- signed the wrong body, signed with the
		// wrong key, signed nothing -- and the exact result is reported
		// separately so the drift is visible rather than absorbed.
		wireTS, _ := strconv.ParseInt(wire.header.Get(hTS), 10, 64)
		within := false
		for _, ts := range []int64{wireTS - 1, wireTS, wireTS + 1} {
			mac := hmac.New(sha256.New, []byte(signSecret))
			mac.Write([]byte(strconv.FormatInt(ts, 10)))
			mac.Write([]byte("."))
			mac.Write([]byte(res.Body))
			if hmac.Equal([]byte("v1="+hex.EncodeToString(mac.Sum(nil))), []byte(res.Signature)) {
				within = true
			}
		}
		emit("testSigOverReportedBody", within)
		emit("testSigMatchesWire", res.Signature == wire.header.Get(hSig))
	}

	// The failing half, against a port nothing is on.
	ln, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		fail("cannot reserve a port to close: %v", lerr)
	}
	deadAddr := ln.Addr().String()
	_ = ln.Close()
	deadSecret := plant()
	deadURL := "http://" + deadAddr + "/webhook/" + deadSecret

	bad := hooks.Hook{
		ID: 2, Name: "acceptance-dead", Enabled: true,
		URL: deadURL, Secret: mustSecret(),
		TimeoutSeconds: 2, MaxAttempts: 1,
	}.Normalized()
	_, badErr := d.Test(ctx, bad, hooks.TriggerIngestPublished)
	emit("testDeadFailed", badErr != nil)
	if badErr != nil {
		emit("testDeadErrLen", len(badErr.Error()))
		emit("testDeadErrHasSecret", strings.Contains(badErr.Error(), deadSecret))
		emit("testDeadErrNamesHost", strings.Contains(badErr.Error(), deadAddr))
	}
	emit("testDeadRawHasSecret", rawErrorCarries(deadURL, 2*time.Second, deadSecret))
}

// remote is the only step that needs something outside this machine, and it
// skips without it. Everything above is the suite's floor.
//
// POLY_HOOKS_URL IS ITSELF THE CREDENTIAL -- a webhook endpoint keeps its whole
// secret in the path, which is the premise of every check above. So it arrives
// in the environment and NOTHING derived from it is ever printed: not the URL,
// not the host, not the endpoint's response, not the error text. What leaves
// this function is a status code, a duration and two booleans.
func remote() {
	raw := strings.TrimSpace(os.Getenv("POLY_HOOKS_URL"))
	if raw == "" {
		emit("skipped", true)
		return
	}
	emit("skipped", false)

	h := hooks.Hook{
		ID: 1, Name: "acceptance-remote", Enabled: true,
		URL: raw, Secret: mustSecret(),
		TimeoutSeconds: 15, MaxAttempts: 1,
	}.Normalized()
	if err := h.Validate(); err != nil {
		// Validate's own contract is that nothing below the host is ever quoted
		// back, but this is the one string in this driver derived from an
		// operator-supplied credential and it is not worth trusting to that.
		emit("valid", false)
		return
	}
	emit("valid", true)

	d := hooks.NewDispatcher(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		hooks.SourceFunc(func() ([]hooks.Hook, error) { return nil, nil }),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	started := time.Now()
	res, err := d.Test(ctx, h, hooks.TriggerIngestPublished)
	emit("durationMs", time.Since(started).Milliseconds())
	emit("status", res.Status)
	emit("failed", err != nil)
	emit("respLen", len(res.Response))

	// The same question the loopback steps ask, against a URL whose shape we did
	// not choose -- a real provider's path, query and token layout rather than
	// this driver's /webhook/<hex>. Short literals are excluded because
	// SecretSet refuses them at MinSecretLen by design, and counting them here
	// would report a documented residual as a regression.
	text := res.Response
	if err != nil {
		text += " " + err.Error()
	}
	disclosed := false
	for _, lit := range alerts.EndpointSecrets(raw) {
		if len(lit) >= alerts.MinSecretLen && strings.Contains(text, lit) {
			disclosed = true
		}
	}
	emit("discloses", disclosed)
}
