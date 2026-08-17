package oauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The guard D5 is really about: a provider pointed at a stub must send
// EVERYTHING there.
//
// Before this existed, &Facebook{graphBase: srv.URL} redirected exactly one
// endpoint out of thirteen -- the credential check -- while IngestFor,
// PushMetadata, RescheduleBroadcast and the rest read a package var and reached
// the real graph.facebook.com. The field looked like the provider's test seam,
// and credcheck_providers_test.go used it that way. YouTube and Twitch had the
// same split in a different shape: their package vars covered the metadata and
// compliance paths while Account and Ingest wrote the production hostname
// inline.
//
// A sampled guard would not have caught any of that: whichever single call the
// guard happened to make would have been the one that worked. So this
// enumerates the entry points, and a transport that refuses every host but the
// stub turns an escape into a named failure instead of a silent real request.

// hostGuard fails any request that is not addressed to the stub, and remembers
// where it was headed. Returning an error rather than allowing the call is what
// keeps this test off the network entirely -- an escaped request must not become
// a real one, even a failing real one.
type hostGuard struct {
	allow   string
	inner   http.RoundTripper
	mu      sync.Mutex
	escaped []string
}

func (h *hostGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != h.allow {
		h.mu.Lock()
		h.escaped = append(h.escaped, r.Method+" "+r.URL.String())
		h.mu.Unlock()
		return nil, fmt.Errorf("blocked: %s is not the stub", r.URL.Host)
	}
	return h.inner.RoundTrip(r)
}

func (h *hostGuard) escapes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.escaped...)
}

// stubbedWorld returns a base URL every provider should be talking to, and the
// guard watching for the ones that are not.
//
// It swaps the package's shared httpClient, the same seam checkFixture uses.
// That makes this test unsafe to run in parallel with others -- no test in this
// package calls t.Parallel, and the Cleanup restores it -- but note that this is
// the LAST such swap in the package: the provider bases themselves are now
// per-instance, which is the whole point of the change this guards.
func stubbedWorld(t *testing.T) (string, *hostGuard) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Deliberately generic. This guard asserts WHERE the calls went, never
		// what came back, so every provider gets a body it can at least decode.
		_, _ = w.Write([]byte(`{"id":"1","access_token":"t","expires_in":3600,
			"data":[{"id":"1","broadcaster_user_id":1,"slug":"s"}],
			"items":[{"id":"1","snippet":{"title":"t"},"status":{},"contentDetails":{}}]}`))
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse stub URL: %v", err)
	}
	g := &hostGuard{allow: u.Host, inner: http.DefaultTransport}

	prev := httpClient
	httpClient = &http.Client{Transport: g, Timeout: 5 * time.Second}
	t.Cleanup(func() { httpClient = prev })

	return srv.URL, g
}

// TestAStubbedProviderReachesNoRealHost drives every entry point that makes an
// HTTP call, on a provider built with WithBaseURL, and fails naming any request
// that went somewhere else.
//
// Errors from the calls are ignored on purpose: the generic stub body does not
// satisfy every provider's parsing, and this guard's claim is about the
// destination of the requests, not their outcome. An escaped request is
// recorded by the transport whether or not the caller surfaced the error.
func TestAStubbedProviderReachesNoRealHost(t *testing.T) {
	const (
		cid    = "client-id"
		secret = "client-secret"
		tok    = "access-token"
		ref    = "user:1000"
	)
	ctx := context.Background()
	at := time.Unix(1893456000, 0)

	// Named so a failure says which call escaped. Every exported method that
	// reaches the network is here; TestEveryProviderCallGoesThroughTheInstanceBase
	// below is what notices when a new one is added and this list is not.
	providers := map[string]func(t *testing.T, base string) map[string]func(){
		"facebook": func(t *testing.T, base string) map[string]func() {
			f := NewFacebook(WithBaseURL(base))
			return map[string]func(){
				"Exchange":               func() { _, _ = f.Exchange(ctx, cid, secret, "https://r.test/cb", "code", "") },
				"Refresh":                func() { _, _ = f.Refresh(ctx, cid, secret, "refresh") },
				"Account":                func() { _, _ = f.Account(ctx, cid, tok) },
				"AccountFor":             func() { _, _ = f.AccountFor(ctx, cid, tok, ref) },
				"Targets":                func() { _, _ = f.Targets(ctx, cid, tok) },
				"Ingest":                 func() { _, _ = f.Ingest(ctx, cid, tok) },
				"IngestFor":              func() { _, _ = f.IngestFor(ctx, cid, tok, ref, IngestOptions{}) },
				"PushMetadata":           func() { _, _ = f.PushMetadata(ctx, cid, tok, ref, Metadata{Title: "x"}) },
				"UpdateLiveVideo":        func() { _, _ = f.UpdateLiveVideo(ctx, cid, tok, ref, "9", Metadata{Title: "x"}) },
				"UpdateLiveVideoPrivacy": func() { _, _ = f.UpdateLiveVideoPrivacy(ctx, cid, tok, ref, "9", db.FBPrivacySelf) },
				"RescheduleBroadcast":    func() { _ = f.RescheduleBroadcast(ctx, tok, "9", at) },
				"PushCompliance": func() {
					_, _ = f.PushCompliance(ctx, cid, tok,
						ComplianceTarget{AccountRef: ref, StreamKey: facebookKeyForLiveVideo("9")},
						db.Compliance{FacebookPrivacy: db.FBPrivacySelf})
				},
				"CheckCredentials": func() { _ = f.CheckCredentials(ctx, cid, secret) },
			}
		},
		"youtube": func(t *testing.T, base string) map[string]func() {
			y := NewYouTube(WithBaseURL(base))
			return map[string]func(){
				"Exchange":   func() { _, _ = y.Exchange(ctx, cid, secret, "https://r.test/cb", "code", "v") },
				"Refresh":    func() { _, _ = y.Refresh(ctx, cid, secret, "refresh") },
				"Account":    func() { _, _ = y.Account(ctx, cid, tok) },
				"AccountFor": func() { _, _ = y.AccountFor(ctx, cid, tok, "") },
				"Targets":    func() { _, _ = y.Targets(ctx, cid, tok) },
				"Ingest":     func() { _, _ = y.Ingest(ctx, cid, tok) },
				"IngestFor":  func() { _, _ = y.IngestFor(ctx, cid, tok, "", IngestOptions{}) },
				// Scheduled as well as live-now: the two take different paths
				// through IngestFor and only the scheduled one reaches
				// liveBroadcasts.insert and liveBroadcasts.bind, so a stub that
				// only ever sees the live-now path proves nothing about them.
				"IngestForScheduled":  func() { _, _ = y.IngestFor(ctx, cid, tok, "", IngestOptions{ScheduledFor: at}) },
				"RescheduleBroadcast": func() { _ = y.RescheduleBroadcast(ctx, tok, "9", at) },
				"Stats":               func() { _, _ = y.Stats(ctx, cid, tok) },
				"PushMetadata":        func() { _, _ = y.PushMetadata(ctx, cid, tok, "4242", Metadata{Title: "x", Category: "Gaming"}) },
				"PushCompliance": func() {
					_, _ = y.PushCompliance(ctx, cid, tok, ComplianceTarget{}, db.Compliance{Privacy: db.PrivacyUnlisted})
				},
				"BroadcastWindow": func() { _, _ = y.BroadcastWindow(ctx, tok) },
				"PushBroadcastSettings": func() {
					_, _ = y.PushBroadcastSettings(ctx, cid, tok, BroadcastSettings{EnableDvr: ptrBool(true)})
				},
			}
		},
		"twitch": func(t *testing.T, base string) map[string]func() {
			tw := NewTwitch(WithBaseURL(base))
			return map[string]func(){
				"Exchange":     func() { _, _ = tw.Exchange(ctx, cid, secret, "https://r.test/cb", "code", "") },
				"Refresh":      func() { _, _ = tw.Refresh(ctx, cid, secret, "refresh") },
				"Account":      func() { _, _ = tw.Account(ctx, cid, tok) },
				"Ingest":       func() { _, _ = tw.Ingest(ctx, cid, tok) },
				"Stats":        func() { _, _ = tw.Stats(ctx, cid, tok) },
				"PushMetadata": func() { _, _ = tw.PushMetadata(ctx, cid, tok, "4242", Metadata{Title: "x", Category: "Chess"}) },
				"PushCompliance": func() {
					_, _ = tw.PushCompliance(ctx, cid, tok, ComplianceTarget{AccountRef: "4242"}, db.Compliance{Labels: map[string]bool{"x": true}})
				},
				"CheckCredentials": func() { _ = tw.CheckCredentials(ctx, cid, secret) },
			}
		},
		"kick": func(t *testing.T, base string) map[string]func() {
			k := NewKick(WithBaseURL(base))
			return map[string]func(){
				"Exchange":         func() { _, _ = k.Exchange(ctx, cid, secret, "https://r.test/cb", "code", "v") },
				"Refresh":          func() { _, _ = k.Refresh(ctx, cid, secret, "refresh") },
				"Account":          func() { _, _ = k.Account(ctx, cid, tok) },
				"Ingest":           func() { _, _ = k.Ingest(ctx, cid, tok) },
				"PushMetadata":     func() { _, _ = k.PushMetadata(ctx, cid, tok, "4242", Metadata{Title: "x", Category: "Chess"}) },
				"SearchCategories": func() { _, _ = k.SearchCategories(ctx, cid, tok, "chess") },
				"UpdateChannel":    func() { _ = k.UpdateChannel(ctx, tok, KickChannelUpdate{StreamTitle: "x"}) },
				"Stats":            func() { _, _ = k.Stats(ctx, cid, tok) },
				"PushBroadcastSettings": func() {
					_, _ = k.PushBroadcastSettings(ctx, cid, tok, BroadcastSettings{EnableDvr: ptrBool(true)})
				},
				"CheckCredentials": func() { _ = k.CheckCredentials(ctx, cid, secret) },
			}
		},
	}

	for platform, build := range providers {
		t.Run(platform, func(t *testing.T) {
			for name, call := range build(t, "") {
				t.Run(name, func(t *testing.T) {
					base, guard := stubbedWorld(t)
					// Rebuilt against this subtest's stub; the outer build("")
					// exists only to enumerate the names.
					build(t, base)[name]()
					if got := guard.escapes(); len(got) > 0 {
						t.Fatalf("%s.%s went to a real platform host despite WithBaseURL:\n  %s\n\n"+
							"Every HTTP call must be built from the provider's own base -- see "+
							"endpoints.go. A call site that writes a production constant directly, "+
							"or reads one through anything but apiBase/authBase, produces exactly "+
							"this.", platform, name, strings.Join(got, "\n  "))
					}
					_ = call
				})
			}
		})
	}
}

// TestEveryProviderCallGoesThroughTheInstanceBase reads the source.
//
// The behavioural guard above proves the calls that exist today. This one is
// what catches the call that does not exist yet: a maintainer adding a
// thirteenth Graph endpoint wired straight to fbGraphBase would add no test, so
// nothing would drive it, and the guard above would stay green while the bug
// came back. Every production host constant must be reached only through
// apiBase or authBase, and this is the line that says so.
func TestEveryProviderCallGoesThroughTheInstanceBase(t *testing.T) {
	// The production hosts, and the accessors allowed to read them.
	consts := []string{
		"fbGraphBase", "fbDialogBase",
		"ytAPIBase", "ytConsentBase", "ytTokenBase",
		"twitchHelixBase", "twitchIDBase",
		"kickIDBase", "kickAPIBase",
	}
	// A line may mention a constant if it declares it, comments on it, or wraps
	// it in the per-instance accessor. Anything else is a direct read.
	allowed := regexp.MustCompile(`(?:apiBase|authBase)\(\s*(?:` + strings.Join(consts, "|") + `)\s*\)`)
	mention := regexp.MustCompile(`\b(?:` + strings.Join(consts, "|") + `)\b`)
	declare := regexp.MustCompile(`^\s*(?:const\s+|var\s+)?(?:` + strings.Join(consts, "|") + `)\s*=`)

	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	var offenders []string
	var checked int
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if !mention.MatchString(line) {
				continue
			}
			checked++
			if strings.HasPrefix(trimmed, "//") || declare.MatchString(line) {
				continue
			}
			if allowed.MatchString(line) {
				continue
			}
			offenders = append(offenders, fmt.Sprintf("%s:%d: %s", name, i+1, trimmed))
		}
	}

	// If the constants were renamed and this guard stopped matching anything,
	// it would pass while asserting nothing -- the failure mode the audit's
	// composer-tags guard calls out. Nine constants cannot yield zero lines.
	if checked == 0 {
		t.Fatal("this guard matched no lines at all, so it is no longer looking at the " +
			"production host constants. Update the `consts` list to whatever they are now.")
	}

	if len(offenders) > 0 {
		t.Errorf("these lines read a production host constant directly instead of through "+
			"the provider's apiBase/authBase, so a provider built with WithBaseURL will not "+
			"redirect them:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// TestProvidersWithAimsTheWholeSetAtOneStub is the internal/api-facing half of
// D5: the five function-pointer fields on Server exist because it could not
// build a provider set pointed anywhere but the real platforms.
func TestProvidersWithAimsTheWholeSetAtOneStub(t *testing.T) {
	base, guard := stubbedWorld(t)
	set := NewSet(WithBaseURL(base))

	for _, p := range []db.Platform{
		db.PlatformYouTube, db.PlatformTwitch, db.PlatformFacebook, db.PlatformKick,
	} {
		pr, err := set.Get(p)
		if err != nil {
			t.Fatalf("Set.Get(%q): %v", p, err)
		}
		// Account is the one call every Provider has, so it is the one that
		// proves the set-level option reached each of them.
		_, _ = pr.Account(context.Background(), "cid", "tok")
	}

	if got := guard.escapes(); len(got) > 0 {
		t.Fatalf("a provider set built with WithBaseURL still reached real hosts:\n  %s",
			strings.Join(got, "\n  "))
	}
}

// TestTheZeroSetIsProduction pins the fallback. A Server that forgets to assign
// its Set must talk to the real platforms, not panic on a nil map -- and must
// NOT silently do nothing, which is the failure mode credcheck.go's comment
// describes for nil hooks.
func TestTheZeroSetIsProduction(t *testing.T) {
	var zero Set
	if len(zero.All()) != len(Providers()) {
		t.Fatalf("the zero Set resolves %d providers, want the production %d",
			len(zero.All()), len(Providers()))
	}
	pr, err := zero.Get(db.PlatformFacebook)
	if err != nil {
		t.Fatalf("the zero Set cannot resolve Facebook: %v", err)
	}
	fb, ok := pr.(*Facebook)
	if !ok {
		t.Fatalf("the zero Set resolved Facebook to %T", pr)
	}
	if got := fb.graphEndpoint(); got != fbGraphBase {
		t.Errorf("a provider from the zero Set points at %q, want the production %q", got, fbGraphBase)
	}
}

// TestEveryCapabilityLookupHasASetTwinThatResolves is the guard endpoints.go
// asks for in prose and nothing enforced.
//
// The comment at endpoints.go:153 says "Every capability the package grows needs
// its twin here", and the cost of forgetting is specific rather than cosmetic: a
// caller holding a stubbed Set resolves the capability through the package-level
// lookup instead, which reads the PRODUCTION providers. The test would still
// pass, the stub would still be aimed correctly for every other call, and one
// capability would quietly talk to the real internet.
//
// So this asserts the two things that make a twin real: it exists (the code does
// not compile otherwise), and it resolves against the SET rather than the
// package -- proven by building a Set whose members are stubbed and checking the
// twin hands back a provider aimed at the stub.
func TestEveryCapabilityLookupHasASetTwinThatResolves(t *testing.T) {
	base, guard := stubbedWorld(t)
	set := NewSet(WithBaseURL(base))

	// BOTH ENDS ARE DRIVEN OFF THE MATRIX RATHER THAN OFF A NAMED PLATFORM.
	//
	// This used to hardcode "Kick implements it, YouTube does not", and the
	// hardcoding had to be rewritten twice in one afternoon -- once when Twitch
	// grew a Stats method and once when YouTube did, each time by editing the
	// assertion to point at whichever platform had not been implemented yet.
	// An assertion that has to be re-aimed every time the thing it guards
	// changes is one somebody eventually re-aims wrongly, and the negative half
	// silently stops covering anything the moment every platform implements the
	// capability.
	//
	// The matrix is the right source: TestTheViewerStatsCellAgreesWithWhich-
	// ProvidersActuallyImplementStats already pins it to the code, so reading it
	// here cannot drift away from what the providers do.
	var claimed, denied int
	for _, row := range PlatformCapabilities() {
		ls, ok := set.StatsFor(row.Platform)
		if row.Caps[CapViewerStats] == SupportYes {
			claimed++
			if !ok {
				t.Errorf("Set.StatsFor(%s) did not resolve; it implements Stats, so the twin is not wired",
					row.Platform)
				continue
			}
			// Driven, so a provider that resolves but is aimed at production
			// still trips the escape guard below.
			_, _ = ls.Stats(context.Background(), "cid", "tok")
			continue
		}
		// A platform without the capability must answer false rather than a nil
		// interface that panics on use: internal/api branches on this bool to
		// answer supported:false, and a nil-with-true would crash the handler.
		denied++
		if ok {
			t.Errorf("Set.StatsFor(%s) resolved, but its viewerStats cell is %q — "+
				"the assertion is matching something it should not",
				row.Platform, row.Caps[CapViewerStats])
		}
	}
	// Neither half may quietly cover nothing. A loop that asserts over an empty
	// set passes for the wrong reason, and that is exactly how the negative half
	// would have died unnoticed once every platform implemented Stats.
	if claimed == 0 || denied == 0 {
		t.Fatalf("this test needs both cases to mean anything: %d platforms claim viewer stats, %d deny it",
			claimed, denied)
	}

	if got := guard.escapes(); len(got) > 0 {
		t.Fatalf("a capability resolved through a stubbed Set still reached real hosts:\n  %s",
			strings.Join(got, "\n  "))
	}
}
