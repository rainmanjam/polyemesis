//go:build ignore

// Driver for acceptance-automod.sh.
//
// internal/automod is 2,454 lines whose entire external surface is one HTTP
// POST, and every property that matters about it is a property of what happens
// when the far end is having a bad day. model.go states three:
//
//	FAIL OPEN. A timeout, a 500, a rate-limit or an expired key means the
//	message passes and is flagged for a human. [...] A moderation outage must
//	not silence a chat.
//
// That is a claim about somebody else's server, and no httptest.Server can
// settle it. A stub returns the failure the test author imagined; only a real
// endpoint returns the failure the endpoint actually produces -- a 401 body in
// their wording, a TLS handshake that stalls, a connection refused by a real
// kernel on a real port.
//
// WHAT THE FAR END IS. Unlike chat, automod has no hardcoded host: the endpoint
// is a settings field, and the deployment model.go names first is a local one
// (Ollama, vLLM). So the far end here is a DEFAULT rather than a dependency --
// api.openai.com, because DefaultModelConfig ships model "gpt-4o-mini" and the
// OpenAI chat-completions shape is the wire contract every compatible server
// implements. POLY_AUTOMOD_ENDPOINT points the whole suite at any other one,
// which is how an operator running the local deployment tests theirs.
//
// NOTHING REAL IS SENT ANYWHERE. Every string this driver POSTs is written
// here, in this file, and marked as synthetic. A suite that fed a real chat
// message to a third party to prove it could would be doing the thing it exists
// to hold to account.
//
//	go run scripts/acceptance_automod_driver.go <cmd>
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// defaultEndpoint is the one DefaultModelConfig's model name implies. Not
// compiled into internal/automod anywhere -- see the package comment.
const defaultEndpoint = "https://api.openai.com/v1/chat/completions"

// synthAbuse and synthBenign are the only two strings this driver ever sends to
// a third party, and both are written here rather than sampled from anything.
// They are a matched pair on purpose: a classifier that answered "abusive" to
// everything would pass the first check alone, and one that answered "fine" to
// everything would pass the second alone.
const (
	synthAbuse  = "shut up you pathetic worthless waste of space, nobody wants you here, get out"
	synthBenign = "the second half of that set was much tighter than the first, nice work"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: acceptance_automod_driver.go <cmd>")
	}
	switch os.Args[1] {
	case "reach":
		reach()
	case "refusal":
		refusal()
	case "deadline":
		deadline()
	case "leak":
		leak()
	case "ceiling":
		ceiling()
	case "hub":
		hub()
	case "classify":
		classify()
	default:
		fail("unknown command %q", os.Args[1])
	}
}

func fail(f string, a ...any) {
	fmt.Printf("ERR "+f+"\n", a...)
	os.Exit(1)
}

// emit prints one key=value result line. The suite greps these, so a command
// that cannot answer prints nothing rather than a plausible zero.
func emit(k string, v any) { fmt.Printf("%s=%v\n", k, v) }

// endpoint is the URL under test.
func endpoint() string {
	if v := strings.TrimSpace(os.Getenv("POLY_AUTOMOD_ENDPOINT")); v != "" {
		return v
	}
	return defaultEndpoint
}

// apiKey is read from the environment and never from argv, which is world
// readable in ps. It is never emitted, and no command prints the config it
// built.
func apiKey() string {
	for _, name := range []string{"POLY_AUTOMOD_API_KEY", "OPENAI_API_KEY"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

// modelFor builds a connector against the endpoint under test.
func modelFor(tweak func(*automod.ModelConfig)) *automod.Model {
	cfg := automod.DefaultModelConfig()
	cfg.Enabled = true
	cfg.Endpoint = endpoint()
	if tweak != nil {
		tweak(&cfg)
	}
	return automod.NewModel(cfg)
}

// ------------------------------------------------------------------ reach

// reach proves the transport before anything is sent over it: DNS, TCP, TLS,
// and a chain that verifies against the system roots -- the same roots
// net/http would use at the point of use.
//
// Separate from the refusal check because they fail for different reasons, and
// a combined one would report an expired chain as a protocol problem.
//
// THE HANDSHAKE DELIBERATELY DOES NOT VERIFY, AND THE VERIFICATION IS DONE
// AFTERWARDS BY HAND. Written the other way round -- a verifying dial, then
// leaf.VerifyHostname on whatever came back -- the second check could never
// fail: the handshake would have refused the connection first, so nameOK was
// true for every connection that existed. It reported a fact that its own
// precondition guaranteed, which is the vacuous-guard shape this suite is
// supposed to be careful about. Verified against expired.badssl.com and
// wrong.host.badssl.com, which now fail the certificate check while still
// reporting a successful dial.
//
// InsecureSkipVerify is safe HERE and nowhere else in this repo: nothing is
// ever sent over this connection. It is opened to read the chain, the chain is
// then verified against the system roots by hand, and the socket is closed. The
// connector's own requests go through net/http with ordinary verification --
// this function does not build, configure or hand off a client.
//
// A plain-http endpoint is the SUPPORTED case rather than an error: model.go
// says the local deployment is what this feature is built for, and Ollama on
// 127.0.0.1 has no certificate to check. It reports notTLS and the suite skips
// the three certificate checks rather than inventing answers for them.
func reach() {
	u, err := url.Parse(endpoint())
	if err != nil || u.Host == "" {
		emit("reached", false)
		emit("error", "endpoint is not a URL")
		return
	}
	emit("host", u.Hostname())
	if u.Scheme != "https" {
		emit("notTLS", true)
		emit("reached", false)
		return
	}
	emit("notTLS", false)

	port := u.Port()
	if port == "" {
		port = "443"
	}
	start := time.Now()
	d := &tls.Dialer{Config: &tls.Config{
		ServerName:         u.Hostname(),
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // verified below, by hand -- see the comment
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := d.DialContext(ctx, "tcp", u.Hostname()+":"+port)
	if err != nil {
		emit("reached", false)
		emit("error", err.Error())
		return
	}
	defer conn.Close()

	st := conn.(*tls.Conn).ConnectionState()
	emit("reached", true)
	emit("tlsVersion", tlsName(st.Version))
	emit("chainLen", len(st.PeerCertificates))
	emit("dialMs", time.Since(start).Milliseconds())
	if len(st.PeerCertificates) == 0 {
		emit("nameOK", false)
		return
	}
	leaf := st.PeerCertificates[0]

	// The verification net/http would have done: chain to a system root, valid
	// for this hostname, valid now. Intermediates the server sent go in as
	// candidates, which is what crypto/tls does internally.
	pool := x509.NewCertPool()
	for _, c := range st.PeerCertificates[1:] {
		pool.AddCert(c)
	}
	_, verr := leaf.Verify(x509.VerifyOptions{
		DNSName:       u.Hostname(),
		Intermediates: pool,
	})
	emit("nameOK", verr == nil)
	if verr != nil {
		emit("certError", verr.Error())
	}
	// Days remaining, so a suite can warn before an expiry becomes an outage
	// rather than after.
	emit("certDaysLeft", int(time.Until(leaf.NotAfter).Hours()/24))
}

func tlsName(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "1.3"
	case tls.VersionTLS12:
		return "1.2"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}

// ---------------------------------------------------------------- refusal

// refusal drives the real connector, against the real endpoint, with no
// credential -- and asserts polyemesis lets the message through.
//
// This is the fail-open contract measured against a real refusal instead of a
// stubbed one. The existing unit test writes `w.WriteHeader(401)` and proves
// the connector handles the 401 we imagined; this proves it handles the one the
// endpoint sends, headers, body, wording and all.
//
// THE VACUITY TRAP IS SPECIFIC AND IS WHY httpStatus IS REPORTED SEPARATELY. A
// findings count of zero is also what a connection that never left the machine
// produces. So the suite is told WHICH failure happened: a status line means
// the request reached the far end and the far end answered, and only then does
// "zero findings" mean the fail-open path ran. A transport error here is
// reported as inconclusive rather than as a pass -- this is the mistake
// scripts/probe_platform_ertmp_multitrack.go made, reporting REJECTED for a
// connection that failed before the thing under test was ever sent.
func refusal() {
	m := modelFor(func(c *automod.ModelConfig) {
		c.APIKey = "" // the whole point
		c.Timeout = 20 * time.Second
	})

	start := time.Now()
	findings, err := m.Check(context.Background(), synthBenign)
	emit("elapsedMs", time.Since(start).Milliseconds())
	emit("findings", len(findings))
	if err == nil {
		// An endpoint that answers an unauthenticated request is a local one
		// with no auth, which is legitimate. It cannot settle this check, and
		// says so rather than passing.
		emit("answered", true)
		emit("refused", false)
		return
	}
	emit("answered", false)
	emit("error", err.Error())
	emit("status", statusFrom(err))

	// Through the Engine as well, because that is what internal/chat holds. A
	// connector that failed open into a caller that treated the error as
	// "block it" would invert the design, and the Verdict is where that would
	// show.
	e := automod.New(permissiveMatrix(), automod.PlatformCaps{}, nil, nil, m2Like(m))
	v, verr := e.CheckModel(context.Background(), db.PlatformTwitch, synthBenign)
	emit("engineErr", verr != nil)
	emit("engineFindings", len(v.Findings))
	emit("engineActs", len(v.Act))
}

// m2Like returns a second connector configured exactly like the first. The
// Engine needs its own because the one above has already spent a call against
// the hourly ceiling, and a ceiling refusal is a different failure that would
// answer the engine question by accident.
func m2Like(*automod.Model) *automod.Model {
	return modelFor(func(c *automod.ModelConfig) {
		c.APIKey = ""
		c.Timeout = 20 * time.Second
	})
}

// statusFrom recovers the HTTP status the connector reported. model.go's
// wording is "model API returned NNN", and matching it here rather than in the
// shell keeps the one place that has to change if that string does.
func statusFrom(err error) int {
	const prefix = "model API returned "
	s := err.Error()
	i := strings.Index(s, prefix)
	if i < 0 {
		return 0
	}
	var code int
	if _, e := fmt.Sscanf(s[i+len(prefix):], "%d", &code); e != nil {
		return 0
	}
	return code
}

// permissiveMatrix switches ON the cell the model checker would need to delete
// a message on Twitch.
//
// Deliberately permissive, and that is what makes the fail-open checks mean
// anything. Against DefaultMatrix -- flag only -- "nothing was deleted" is true
// whatever the model said, because the matrix would have blocked a delete
// regardless. Here the only thing standing between a verdict and a deletion is
// the verdict.
func permissiveMatrix() automod.Matrix {
	m := automod.Matrix{Enabled: true, On: map[string]bool{}}
	m.Set(automod.Key{
		Platform: db.PlatformTwitch,
		Action:   automod.ActionDelete,
		Checker:  automod.CheckerModel,
	}, true)
	return m
}

// --------------------------------------------------------------- deadline

// deadline asserts that ModelConfig.Timeout is enforced end to end against a
// real network, not merely stored.
//
// Worth a live check because the failure is invisible offline: a stub server on
// loopback answers in under a millisecond, so a timeout that was never applied
// to the transport looks identical to one that was. Across the internet it does
// not.
//
// The deadline is short enough that no real endpoint can beat it. If one did --
// a local model on a unix-fast loopback -- the command reports beatIt so the
// suite can say the check was inconclusive rather than green.
func deadline() {
	m := modelFor(func(c *automod.ModelConfig) {
		c.APIKey = apiKey() // if there is one; the request should die first
		c.Timeout = time.Millisecond
	})

	start := time.Now()
	findings, err := m.Check(context.Background(), synthBenign)
	elapsed := time.Since(start)
	emit("elapsedMs", elapsed.Milliseconds())
	emit("findings", len(findings))
	if err == nil {
		emit("beatIt", true)
		return
	}
	emit("beatIt", false)
	// os.IsTimeout covers the net side, DeadlineExceeded the context side; the
	// http client can produce either depending on where it gave up.
	emit("wasDeadline", os.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded))
	emit("error", err.Error())
}

// ------------------------------------------------------------------- leak

// leakSentinel is what a credential in the endpoint looks like. It is not a
// credential -- there is nothing to leak here except this string -- and the
// endpoint it is attached to refuses the connection, so it never travels.
const leakSentinel = "sk-polyemesis-acceptance-sentinel-not-a-real-key"

// leak asks whether the endpoint's own credential can reach an operator-visible
// surface when the endpoint is unreachable.
//
// WHY THE ENDPOINT IS A CREDENTIAL. internal/api/redact.go masks
// automod.model.endpoint out of GET /settings, and says why: a self-hosted or
// proxied inference endpoint most often arrives as
// https://host/v1/chat/completions?api_key=sk-..., and a key in a query string
// is still a key. That reasoning was applied to the settings blob. It had not
// been applied to the error path, where net/http puts the request URL verbatim
// into *url.Error -- and internal/chat writes that error to server.log once per
// message, for as long as the endpoint is down, because the fail-open contract
// means there is no backoff. #310 was this exact shape.
//
// A REAL HOST ON A CLOSED PORT, rather than a hostname that does not resolve. A
// resolver failure is wrapped differently and would not exercise the same path;
// port 81 on the real endpoint's host gives a real TCP failure from a real
// kernel, which is what an operator's unreachable endpoint produces.
func leak() {
	u, err := url.Parse(endpoint())
	if err != nil || u.Host == "" {
		fail("endpoint is not a URL")
	}
	u.Host = u.Hostname() + ":81"
	q := u.Query()
	q.Set("api_key", leakSentinel)
	u.RawQuery = q.Encode()
	configured := u.String()

	// The vacuity guard, emitted so the suite can refuse to score a run where
	// the sentinel was never in play.
	//
	// It earned its place immediately. A mutation that removed the driver's
	// masking left the file failing to compile, `go run` printed a build error,
	// every val() came back empty -- and without this gate four "the key is not
	// in X" checks would have read as passes on a run that never made a
	// request.
	emit("sentinelConfigured", strings.Contains(configured, leakSentinel))

	m := automod.NewModel(func() automod.ModelConfig {
		c := automod.DefaultModelConfig()
		c.Enabled = true
		c.Endpoint = configured
		c.Timeout = 6 * time.Second
		return c
	}())

	findings, cerr := m.Check(context.Background(), synthBenign)
	emit("findings", len(findings))
	if cerr == nil {
		// Something answered on port 81. Nothing below can be concluded.
		emit("refused", false)
		return
	}
	emit("refused", true)
	emit("sentinelInError", strings.Contains(cerr.Error(), leakSentinel))
	emit("sentinelInStats", strings.Contains(m.Stats().LastError, leakSentinel))
	// The host has to survive the masking. An error stripped down to nothing
	// would pass every sentinel check above and leave an operator unable to
	// tell which endpoint stopped answering -- one silent failure traded for
	// another.
	emit("hostNamed", strings.Contains(cerr.Error(), u.Hostname()))

	// And through slog, exactly as internal/chat/automod.go writes it. Not a
	// paraphrase: the same call, the same key, so a handler that rendered the
	// error some other way would show up here.
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	log.Warn("automod model check failed; the message passes",
		"platform", db.PlatformTwitch, "err", cerr)
	emit("sentinelInLog", strings.Contains(buf.String(), leakSentinel))
	emit("logWritten", buf.Len() > 0)
}

// ---------------------------------------------------------------- ceiling

// ceiling asserts the hourly spend ceiling stops a call from LEAVING, rather
// than counting one that already did.
//
// The unit test measures this against a stub that records handler calls. Here
// the observable is different and stronger: with a ceiling of one, the second
// call has to come back faster than the network round trip the first one took.
// A ceiling implemented after the request was sent would be indistinguishable
// from no ceiling on the invoice, and identical on the handler count.
func ceiling() {
	m := modelFor(func(c *automod.ModelConfig) {
		c.APIKey = ""
		c.MaxCallsPerHour = 1
		c.Timeout = 20 * time.Second
	})

	first := time.Now()
	_, err1 := m.Check(context.Background(), synthBenign)
	firstMs := time.Since(first).Milliseconds()
	emit("firstMs", firstMs)
	emit("firstLeft", err1 == nil || statusFrom(err1) != 0)

	second := time.Now()
	_, err2 := m.Check(context.Background(), synthBenign)
	emit("secondMs", time.Since(second).Milliseconds())
	emit("secondBlocked", err2 != nil && strings.Contains(err2.Error(), "hourly ceiling"))
	emit("callsThisHour", m.Stats().CallsThisHour)
}

// -------------------------------------------------------------------- hub

// recordingAdapter is a chat.Adapter that delivers one synthetic message and
// then records every moderation action the Hub performs on it.
//
// It exists so this suite can assert fail-open where fail-open actually
// matters: not "the connector returned no findings", which is a statement about
// one function, but "nobody was deleted or banned", which is the statement the
// operator cares about and which involves the Hub, the worker, the generation
// counter and the matrix as well.
type recordingAdapter struct {
	mu       sync.Mutex
	deletes  int
	bans     int
	sent     chan struct{}
	msgID    string
	authorID string
}

func (a *recordingAdapter) Platform() db.Platform { return db.PlatformTwitch }
func (a *recordingAdapter) Account() string       { return "acceptance" }

func (a *recordingAdapter) Run(ctx context.Context, sink chat.Sink) error {
	sink.Deliver(chat.Message{
		ID:       a.msgID,
		Platform: db.PlatformTwitch,
		Account:  "acceptance",
		Author:   chat.Author{ID: a.authorID, Name: "synthetic-author"},
		Text:     synthAbuse,
		At:       time.Now(),
	})
	close(a.sent)
	<-ctx.Done()
	return nil
}

func (a *recordingAdapter) Delete(ctx context.Context, messageID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.deletes++
	return nil
}

func (a *recordingAdapter) Ban(ctx context.Context, userID string, d time.Duration, reason string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bans++
	return nil
}

func (a *recordingAdapter) counts() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.deletes, a.bans
}

// hub runs the whole composition against a real endpoint that cannot be
// reached, and asks the question the operator would ask: when moderation is
// broken, does anybody get moderated anyway, and does the log stay clean.
//
// EVERY LAYER IS THE REAL ONE except the adapter, which has to be ours because
// the alternative is deleting a stranger's message to find out. The Hub, the
// worker, the generation counter, the matrix, the Engine and the Model are all
// production code, wired the way internal/api's ApplyAutomod wires them.
//
// The matrix is permissive (see permissiveMatrix) so that a wrong verdict WOULD
// delete. Against the shipped flag-only default this check would pass with the
// model returning anything at all.
func hub() {
	u, err := url.Parse(endpoint())
	if err != nil || u.Host == "" {
		fail("endpoint is not a URL")
	}
	// Same unreachable-port trick as leak(): a real host, a real refusal, and a
	// sentinel in the query so the log can be searched for it.
	u.Host = u.Hostname() + ":81"
	q := u.Query()
	q.Set("api_key", leakSentinel)
	u.RawQuery = q.Encode()

	cfg := automod.DefaultModelConfig()
	cfg.Enabled = true
	cfg.Endpoint = u.String()
	cfg.Timeout = 6 * time.Second
	cfg.Action = automod.ActionDelete
	model := automod.NewModel(cfg)

	matrix := permissiveMatrix()
	caps := automod.PlatformCaps{}
	emit("wouldPermitDelete", matrix.Allows(caps, automod.Key{
		Platform: db.PlatformTwitch,
		Action:   automod.ActionDelete,
		Checker:  automod.CheckerModel,
	}))

	var logBuf bytes.Buffer
	// Level Debug so nothing the Hub writes is filtered out before we look at
	// it. A leak that only appears at Info would otherwise be invisible here.
	log := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	h := chat.New(chat.WithLogger(log), chat.WithHistory(64))
	h.SetModerator(automod.New(matrix, caps, nil, automod.NewHistory(automod.DefaultHistoryLimits()), model))

	ad := &recordingAdapter{sent: make(chan struct{}), msgID: "acceptance-1", authorID: "synthetic-1"}
	ctx, cancel := context.WithCancel(context.Background())
	if err := h.Attach(ctx, ad); err != nil {
		cancel()
		fail("attach: %v", err)
	}

	select {
	case <-ad.sent:
	case <-time.After(10 * time.Second):
		cancel()
		fail("the adapter never delivered its message")
	}

	// Long enough for the worker to make the call and for the 6s dial to give
	// up, plus slack. The check below is worthless if it runs before the model
	// has failed, which is why modelAsked is reported rather than assumed.
	time.Sleep(12 * time.Second)

	deletes, bans := ad.counts()
	emit("deletes", deletes)
	emit("bans", bans)
	emit("historyHas", len(h.History(0)) > 0)

	out := logBuf.String()
	// THE PROOF THAT ANYTHING HAPPENED AT ALL. This line is written by
	// Hub.askModel and only on the error path, so it means the worker ran, the
	// message was still in history when it looked, and CheckModel came back
	// with an error. Without it, "deletes=0" is what a Hub that never got the
	// message would also report.
	emit("modelAsked", strings.Contains(out, "automod model check failed"))
	emit("sentinelInLog", strings.Contains(out, leakSentinel))

	cancel()
	h.Close()
}

// --------------------------------------------------------------- classify

// classify is the credentialed tier: a real model, asked about two synthetic
// messages, in both directions.
//
// BOTH DIRECTIONS OR NEITHER. A connector that produced a finding for
// everything would pass the abusive case alone; one that produced nothing ever
// -- which is exactly what a fail-open outage looks like -- would pass the
// benign case alone. Only the pair says the classification round trip works.
//
// The two strings are the constants at the top of this file. Neither came from
// a person.
func classify() {
	key := apiKey()
	if key == "" {
		emit("haveKey", false)
		return
	}
	emit("haveKey", true)

	run := func(text string) (int, float64, error) {
		m := modelFor(func(c *automod.ModelConfig) {
			c.APIKey = key
			c.Timeout = 30 * time.Second
			c.Action = automod.ActionDelete
		})
		f, err := m.Check(context.Background(), text)
		if len(f) > 0 {
			return len(f), f[0].Confidence, err
		}
		return 0, 0, err
	}

	n, conf, err := run(synthAbuse)
	emit("abuseFindings", n)
	emit("abuseConfidence", conf)
	if err != nil {
		emit("abuseError", err.Error())
	}

	n2, _, err2 := run(synthBenign)
	emit("benignFindings", n2)
	if err2 != nil {
		emit("benignError", err2.Error())
	}
	// Reported separately so the suite can tell "the model disagreed with us"
	// -- a judgement call about somebody else's classifier -- from "the round
	// trip did not work", which is ours.
	emit("bothCallsSucceeded", err == nil && err2 == nil)
}
