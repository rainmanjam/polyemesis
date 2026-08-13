//go:build ignore

// Driver for acceptance-chat.sh.
//
// Every command here opens a socket to a real platform. That is the entire
// point, and it is what internal/chat's own doc comment says is missing:
//
//	the IRC transport cannot be exercised against Twitch offline, and this
//	package does not pretend otherwise -- there is no test here that proves
//	polyemesis can talk to irc.chat.twitch.tv. [...] The parts that need a
//	socket to a real platform are verified by connecting to one.
//
// That last sentence described a practice nothing performed. This is it.
//
// WHY A REFUSAL IS WORTH TESTING. fatalNotice() classifies Twitch's login
// failure by matching Twitch's own wording -- "login authentication failed" and
// two variants. A unit test feeding it a hand-written NOTICE proves the matcher
// works on the string we imagined. Only a live connection proves it works on
// the string Twitch actually sends, and the cost of being wrong is specific:
// an unrecognised NOTICE is treated as retryable, so polyemesis would retry a
// rejected password every thirty seconds, which the code comment itself says is
// how an IP gets banned.
//
//	go run scripts/acceptance_chat_driver.go <cmd> [args]
package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: acceptance_chat_driver.go <cmd> [args]")
	}
	switch os.Args[1] {
	case "twitch-reach":
		twitchReach()
	case "twitch-refusal":
		twitchRefusal()
	case "twitch-anon":
		twitchAnon()
	case "kick-key":
		kickKey()
	case "kick-verify":
		kickVerify()
	case "twitch-live":
		twitchLive()
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

// ---------------------------------------------------------------- twitch

// twitchReach proves the transport before anything is sent over it: DNS, TCP,
// TLS with the right server name, and a certificate chain that verifies
// against the system roots.
//
// Separate from the refusal check because they fail for different reasons and
// a combined check would report a expired-root problem as a protocol problem.
func twitchReach() {
	const addr = "irc.chat.twitch.tv:6697"
	start := time.Now()
	d := &tls.Dialer{Config: &tls.Config{
		ServerName: "irc.chat.twitch.tv",
		MinVersion: tls.VersionTLS12,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		emit("reached", false)
		emit("error", err.Error())
		return
	}
	defer conn.Close()

	st := conn.(*tls.Conn).ConnectionState()
	emit("reached", true)
	emit("tlsVersion", tlsName(st.Version))
	emit("cipherOK", st.CipherSuite != 0)
	emit("chainLen", len(st.PeerCertificates))
	if len(st.PeerCertificates) > 0 {
		leaf := st.PeerCertificates[0]
		emit("certCN", leaf.Subject.CommonName)
		// Days remaining, so a suite can warn before an expiry becomes an
		// outage rather than after.
		emit("certDaysLeft", int(time.Until(leaf.NotAfter).Hours()/24))
	}
	emit("dialMs", time.Since(start).Milliseconds())
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

// twitchRefusal drives the real adapter, against the real server, with a token
// that cannot work -- and asserts that polyemesis understands the rejection.
//
// The token is deliberately well-formed nonsense rather than empty: an empty
// one can fail a length check locally and never reach the wire, which would
// make this test pass without Twitch having been consulted at all.
//
// THE NICK IS NOT justinfan*, AND THAT IS THE WHOLE TEST. Written with one,
// this check ran for 45 seconds and reported no refusal at all -- because
// Twitch treats a justinfan nick as an anonymous reader and ignores the PASS
// line entirely, answering "001 Welcome, GLHF!" to a token that is plainly
// rubbish. A refusal test that cannot produce a refusal is not a test, and this
// one was caught only by running it. See twitchAnon for the other half of that
// discovery, which turned out to be worth more than this check.
func twitchRefusal() {
	tok := "invalidtokeninvalidtokeninvalid0"
	a, err := chat.NewTwitch(chat.TwitchConfig{
		Nick:       "polyemesisacceptance",
		Channel:    "twitchpresents",
		AccountRef: "acceptance",
		Token:      tok,
	})
	if err != nil {
		fail("building the adapter: %v", err)
	}

	var got int64
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	start := time.Now()
	runErr := a.Run(ctx, chat.SinkFunc(func(chat.Message) { atomic.AddInt64(&got, 1) }))
	elapsed := time.Since(start)

	h := a.Health()
	emit("elapsedMs", elapsed.Milliseconds())
	emit("returned", runErr != nil)
	emit("fatal", runErr != nil && chat.IsFatal(runErr))
	emit("state", string(h.State))
	emit("messages", atomic.LoadInt64(&got))
	if runErr != nil {
		emit("error", strings.ReplaceAll(runErr.Error(), "\n", " "))
	}
	// TIMED OUT is its own verdict and must not read as a refusal. If the
	// context expired we were never told anything, which is a different
	// failure from being told no.
	emit("timedOut", ctx.Err() != nil)
}

// twitchLive is the authenticated half, and runs only when a token is present.
// It holds the session open long enough to be more than a handshake: the
// keepalive is what a short connect test cannot reach, and a broken PING/PONG
// looks perfect for the first thirty seconds.
func twitchLive() {
	tok := os.Getenv("TWITCH_CHAT_TOKEN")
	nick := os.Getenv("TWITCH_CHAT_NICK")
	ch := os.Getenv("TWITCH_CHAT_CHANNEL")
	if tok == "" || nick == "" || ch == "" {
		emit("skipped", true)
		return
	}
	hold := 30 * time.Second
	if s := os.Getenv("POLY_CHAT_HOLD"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			hold = d
		}
	}

	a, err := chat.NewTwitch(chat.TwitchConfig{
		Nick: nick, Channel: ch, AccountRef: "acceptance", Token: tok,
	})
	if err != nil {
		fail("building the adapter: %v", err)
	}

	var msgs int64
	ctx, cancel := context.WithTimeout(context.Background(), hold)
	defer cancel()

	start := time.Now()
	runErr := a.Run(ctx, chat.SinkFunc(func(chat.Message) { atomic.AddInt64(&msgs, 1) }))
	h := a.Health()

	emit("skipped", false)
	emit("heldMs", time.Since(start).Milliseconds())
	emit("state", string(h.State))
	emit("messages", atomic.LoadInt64(&msgs))
	// The session ending because OUR deadline expired is the success shape.
	// Any other end means Twitch or the transport dropped us.
	emit("endedOnOurDeadline", ctx.Err() != nil)
	emit("fatal", runErr != nil && chat.IsFatal(runErr))
	if runErr != nil {
		emit("error", strings.ReplaceAll(runErr.Error(), "\n", " "))
	}
}

// twitchAnon is the check this suite exists for, and it needs no credentials.
//
// Twitch accepts any nick matching justinfan* as an anonymous read-only client
// and ignores the PASS line, so the ordinary adapter -- unmodified, still
// sending its PASS -- can join a real channel and receive real chat. That
// exercises the whole read path against live traffic: the IRCv3 line parser,
// tag parsing, the CAP handshake, JOIN, PING/PONG, and message normalisation.
//
// It was written after twitchRefusal failed to refuse. The capability had been
// recorded here as impossible on the grounds that the adapter always sends a
// PASS; that was true and irrelevant, because the server discards it.
//
// MESSAGES ARE REPORTED, NOT REQUIRED. Whether a given channel is talking right
// now is not something this suite controls, so a zero is reported as a zero and
// the caller decides. Connecting and joining are the assertions.
func twitchAnon() {
	ch := os.Getenv("POLY_CHAT_ANON_CHANNEL")
	if ch == "" {
		ch = "twitchpresents"
	}
	hold := 25 * time.Second
	if s := os.Getenv("POLY_CHAT_ANON_HOLD"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			hold = d
		}
	}

	// justinfan plus digits. Fixed rather than random: the whole run is
	// reproducible, and Twitch does not care which one.
	a, err := chat.NewTwitch(chat.TwitchConfig{
		Nick:       "justinfan41287",
		Channel:    ch,
		AccountRef: "acceptance-anon",
		Token:      "anonymous",
	})
	if err != nil {
		fail("building the adapter: %v", err)
	}

	var msgs int64
	var first atomic.Value
	ctx, cancel := context.WithTimeout(context.Background(), hold)
	defer cancel()

	// POLLED, because the FINAL state is not the interesting one. Run sets
	// StateStopped as it returns, so reading Health() afterwards reports
	// "stopped" whether the session joined a channel or never got past the
	// handshake -- which is how the first version of this check reported a
	// successful anonymous join as though nothing had happened.
	var everLive atomic.Bool
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if a.Health().State == chat.StateLive {
					everLive.Store(true)
				}
			}
		}
	}()

	start := time.Now()
	runErr := a.Run(ctx, chat.SinkFunc(func(m chat.Message) {
		if atomic.AddInt64(&msgs, 1) == 1 {
			first.Store(m)
		}
	}))
	cancel()
	<-pollDone

	emit("channel", ch)
	emit("heldMs", time.Since(start).Milliseconds())
	// The assertion that does not depend on anyone talking: we joined.
	emit("joined", everLive.Load())
	emit("messages", atomic.LoadInt64(&msgs))
	emit("endedOnOurDeadline", ctx.Err() != nil)
	emit("fatal", runErr != nil && chat.IsFatal(runErr))
	// A parsed message proves more than a counter does: an author and a body
	// mean the tag parser and the normaliser both ran on real bytes.
	if v := first.Load(); v != nil {
		m := v.(chat.Message)
		emit("firstAuthorLen", len(m.Author.Name))
		emit("firstTextLen", len(m.Text))
		emit("firstHasID", m.ID != "")
	}
	if runErr != nil {
		emit("error", strings.ReplaceAll(runErr.Error(), "\n", " "))
	}
}

// ------------------------------------------------------------------ kick

// kickKey fetches Kick's published webhook public key over the real network
// and parses it. Nothing here is secret -- that is the point of a public key --
// so this needs no credentials and belongs in the always-run tier.
//
// It matters because the key is fetched at runtime by design: kick_verify.go
// records that a rotation cannot be fixed short of a new binary if it were
// compiled in. A URL that starts 404ing is therefore a silent break in webhook
// verification, and nothing else would notice.
func kickKey() {
	f := &chat.KickKeyFetcher{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	start := time.Now()
	pub, err := f.Key(ctx)
	if err != nil {
		emit("fetched", false)
		emit("error", err.Error())
		return
	}
	emit("fetched", true)
	emit("fetchMs", time.Since(start).Milliseconds())
	emit("bits", pub.N.BitLen())
	emit("exponent", pub.E)
	emit("url", chat.KickPublicKeyURL)
}

// kickVerify checks the verifier against the REAL key, both ways round.
//
// We cannot produce a valid Kick signature -- that would need Kick's private
// key -- so the positive case is built with a keypair of our own, and the
// negative case is that same signature presented against Kick's real key.
//
// Both directions are needed. A verifier that accepted everything would pass a
// positive-only test; a verifier that rejected everything would pass a
// negative-only one. Neither alone says the thing works.
func kickVerify() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	real, err := (&chat.KickKeyFetcher{}).Key(ctx)
	if err != nil {
		emit("fetched", false)
		emit("error", err.Error())
		return
	}
	emit("fetched", true)

	ours, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		fail("generating a test key: %v", err)
	}

	body := []byte(`{"event":"chat.message.sent","data":{"content":"acceptance"}}`)
	msgID, ts := "acceptance-1", time.Now().UTC().Format(time.RFC3339)
	signed := []byte(msgID + "." + ts + "." + string(body))
	sum := sha256.Sum256(signed)
	sig, err := rsa.SignPKCS1v15(rand.Reader, ours, crypto.SHA256, sum[:])
	if err != nil {
		fail("signing: %v", err)
	}

	req := func(sigB64 string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/hooks/kick", strings.NewReader(string(body)))
		r.Header.Set("Kick-Event-Message-Id", msgID)
		r.Header.Set("Kick-Event-Message-Timestamp", ts)
		r.Header.Set("Kick-Event-Signature", sigB64)
		return r
	}
	good := base64.StdEncoding.EncodeToString(sig)

	// 1. Our signature against OUR key: must pass, or the verifier is broken
	//    in a way that would reject genuine Kick traffic too.
	emit("ourSigOurKey", chat.VerifyKickSignature(&ours.PublicKey, req(good), body) == nil)
	// 2. Our signature against KICK'S key: must fail. This is the check that
	//    proves verification is actually happening against the fetched key.
	emit("ourSigKickKey", chat.VerifyKickSignature(real, req(good), body) == nil)
	// 3. A tampered body against our own key: must fail, proving the body is
	//    inside the signed material and not merely alongside it.
	emit("tamperedBody", chat.VerifyKickSignature(&ours.PublicKey, req(good), []byte(`{"event":"x"}`)) == nil)
	// 4. Garbage in the signature header: must fail without panicking.
	emit("garbageSig", chat.VerifyKickSignature(&ours.PublicKey, req("!!!not-base64!!!"), body) == nil)
}
