package api

import (
	"net/http"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
)

// ROUTE POLICY IS DECLARED WHERE THE ROUTE IS REGISTERED.
//
// What a route means beyond its handler -- whether a read-scoped token may
// reach it, whether it needs a running programme, whether the request log
// skips it, whether its path carries a credential -- used to live in parallel
// string tables beside the router, each kept in step with registerRoutes by
// hand (and a drift test for each). It is now a flag on the registration, and
// the tables the middleware and the tests consult are DERIVED from what was
// registered, so a route cannot be moved without its policy moving with it.
//
// Only a route with a non-default policy goes through a routeGroup; the rest
// are plain chi registrations. The default is the safe one: a route that
// carries no flag is denied to read tokens for every method but GET.

// --- the read-scope half: why each route carries the flag it does ---
//
// The readWrite flag marks the POST routes a read-scoped token may still
// reach: the ones that answer a question rather than change anything.
//
// They are POSTs because they carry a body or make an outbound call, not
// because they write. Each was read before it was listed, and one candidate did
// not survive that reading: POST /destinations/{id}/expert/dry-run SPAWNS
// FFMPEG with an argument list the caller supplied, which is the opposite of
// writing nothing whatever its route name suggests. POST
// /platforms/credentials/{platform}/check is absent for the same kind of
// reason -- it handles stored credentials.
//
// Keyed by chi's route PATTERN rather than the request path, so an id in the
// URL cannot smuggle a request onto the list and no string matching has to be
// invented for the {id} segment.
//
// The list is additive and that is its safety property: a route missing from it
// is denied to read tokens, so the failure mode of forgetting to maintain it is
// a monitoring script getting a 403 that somebody notices, never a write
// getting through unannounced.
//
// The expert preview route USED TO BE ON THIS LIST and is deliberately not any
// more. Its response is the resolved FFmpeg argv, which contains the
// destination's stream key in the output target -- so a read token reached the
// credential through a POST this list was hand-built to permit. The comment
// above reasons entirely about whether each candidate WRITES; it never asks
// what the route RETURNS, which is the method-shaped rule's blind spot
// reproduced inside its own exception list. The argv is not masked, because
// expert mode's contract is that the command shown is the command that runs;
// the route is denied to read tokens instead. See the readDenied flag.

// The readDenied flag marks the GETs a read-scoped token may NOT make.
//
// The method rule says a GET is a read. For these five it is false, in one of
// two ways, and both were found by enumerating the route table rather than by
// sampling it:
//
//	THE RESPONSE IS A CREDENTIAL. GET /destinations/{id}/expert returns the
//	resolved argv, whose output target is rtmps://host/app/<streamKey>. Every
//	other egress of an argv in this process is masked by redact.Redact inside
//	supervisor; this one is deliberately raw because expert mode's whole
//	promise is that the command an operator approves is the command that runs.
//	Masking it would break that approval, so the route is denied instead.
//
//	THE GET IS NOT A READ. /clipper/.../keyframes spawns one ffprobe per
//	overlapping timeline part, with no cap on requests.
//	/platforms/accounts/{id}/stats and /metadata/broadcast-window each call a
//	third party and can REFRESH AND PERSIST an OAuth token -- GETs that write
//	the database and burn somebody else's quota, one call per connected account
//	in the broadcast-window case.
//
// Keyed by chi's route PATTERN for the same reason the allowlist is: an id in
// the URL must not be able to smuggle a request past the check.
//
// The readDenied flag is SUBTRACTIVE, which is the opposite safety property to readWrite, and that asymmetry is deliberate rather than sloppy:
// forgetting to add a route here leaves a read token reaching something it
// perhaps should not, so the recurrence guard for this half is a TEST that
// drives each of these through the real router and asserts the 403 -- paired
// with the same route reaching an admin, so a change that merely broke the
// route for everybody cannot pass. See
// TestReadTokenIsDeniedTheRoutesThatAreNotReads.
//
// The three playout routes are ABSENT from this list and the reason is not
// that they were judged harmless. It is that the flag CANNOT REACH THEM. It is
// consulted in exactly one place, inside requireScope, and requireScope is
// middleware on the authenticated group; /playout/*, /playout/public and
// /playout/poster.jpg are all registered outside it, because a viewer has no
// session. A flag on any of them would be a rule that reads as
// enforcement and enforces nothing -- which is what this whole round of work
// has been about deleting.
//
// What actually gates them is playoutOperator, inside authorizePlayout, which
// each of the three handlers calls per request. See TestPlayoutGateMatrix and
// TestPlayoutPosterVerdict.
//
// The poster's throttling was the old argument for leaving it alone and it is
// still true -- a 10-second cache under a mutex and an 8-second timeout, so ~6
// execs a minute however many callers ask -- but it was an answer to "is this
// GET really a read", and the question that mattered was who gets the frame.
// THE RESPONSE IS CONTENT, NOT METADATA is the third reason a GET is denied,
// added for #154, and it is a different claim from the two above: these routes
// are reads, they are cheap, and they leak no credential. They are denied
// because of what `read` was decided to MEAN -- a monitoring script, a
// dashboard, a status page. None of those need the bytes.
//
// The line is drawn at content, not at the archive: a read token still lists
// recordings, clips, stems and sessions, still sees durations, sizes and
// status, and still sees WHETHER a recording has a transcript. What it no
// longer gets is the media itself or the words.
//
// /library/search is flagged and it is the reason the set is eight
// routes rather than seven. The two /transcript routes are the obvious ones;
// search is not, because it reads as a metadata query. It returns
// db.TranscriptHit, which carries Text -- the full segment text -- plus
// Context (the neighbouring segments joined) and Speaker. A read token that
// iterated common words would reconstruct whole transcripts without ever
// naming a /transcript route. Denying only the endpoints whose PATH says
// transcript would have been a fix shaped by the URL rather than by the bytes,
// which is the same mistake as gating on the HTTP verb.
//
// CHAT IS CONTENT FOR THE SAME REASON, and the four chat GETs joined the list
// when staging-readiness row 32 found them missing: the scrollback, a search
// over it, one viewer's history, and the overview that embeds the recent
// messages. /chat/search is the /library/search argument again -- iterate
// common words and the scrollback comes back. The same messages arrive live on
// /ws, which a read token may open, so events.TypeChat is wsDrop there.
//
// GET /library still returns Speakers, the bare list of labels in the archive.
// That is left reachable deliberately: it is who appears, not what was said,
// and a dashboard that groups by speaker needs it. If that judgement is wrong
// it is one more line here, not a redesign.

// routeFlag is one policy bit.
type routeFlag uint8

const (
	// needsSource: the route acts on a running programme and is behind
	// requireSource (the 503 a request with no programme gets).
	needsSource routeFlag = 1 << iota
	// readDenied: a read-scoped token is refused this pattern whatever its
	// method. The method rule calls every GET a read; for these it is false --
	// the response is a credential, content a read token does not get, or the
	// route makes an outbound call on the operator's token.
	readDenied
	// readWrite: a read-scoped token may make this non-GET request, because it
	// answers a question rather than changing anything.
	readWrite
	// obsScope: an "obs"-scoped token may reach it, and nothing else.
	obsScope
	// logSkip: the request log does not record it (polled, or high volume).
	logSkip
	// logSkipUnless5xx: logSkip, except a server error is still logged.
	logSkipUnless5xx
	// secretInPath: the matched wildcard path carries a credential that the log
	// must not record; the line carries the route pattern, with the wildcard
	// segment in place of the secret.
	secretInPath
)

// routePolicy is what registerRoutes recorded. Immutable once published.
type routePolicy struct {
	byRoute   map[string]routeFlag // METHOD + " " + full pattern
	byPattern map[string]routeFlag // full pattern, every method OR-ed
}

func (p *routePolicy) pattern(pattern string, f routeFlag) bool {
	return p != nil && p.byPattern[pattern]&f != 0
}

func (p *routePolicy) route(method, pattern string, f routeFlag) bool {
	return p != nil && p.byRoute[method+" "+pattern]&f != 0
}

// patterns is the set of full patterns carrying f, for the tests and docs that
// reconcile against it.
func (p *routePolicy) patterns(f routeFlag) map[string]bool {
	out := map[string]bool{}
	if p == nil {
		return out
	}
	for pat, fl := range p.byPattern {
		if fl&f != 0 {
			out[pat] = true
		}
	}
	return out
}

// routeRecorder collects policy during one registerRoutes call.
type routeRecorder struct {
	s *Server
	p *routePolicy
}

func newRouteRecorder(s *Server) *routeRecorder {
	return &routeRecorder{s: s, p: &routePolicy{
		byRoute:   map[string]routeFlag{},
		byPattern: map[string]routeFlag{},
	}}
}

// publish makes what was recorded the policy the middleware consults.
func (rr *routeRecorder) publish(dst *atomic.Pointer[routePolicy]) { dst.Store(rr.p) }

// under returns a registrar for routes registered below base on whatever
// router it is handed: base is the prefix of the enclosing r.Route, "" at the
// top level, so the recorded pattern is the full one chi reports.
func (rr *routeRecorder) under(base string) routeGroup { return routeGroup{rr: rr, base: base} }

type routeGroup struct {
	rr   *routeRecorder
	base string
}

func (g routeGroup) record(method, pattern string, f routeFlag) {
	full := g.base + pattern
	g.rr.p.byRoute[method+" "+full] |= f
	g.rr.p.byPattern[full] |= f
}

func (g routeGroup) router(r chi.Router, f routeFlag) chi.Router {
	if f&needsSource != 0 {
		return r.With(g.rr.s.requireSource)
	}
	return r
}

func (g routeGroup) Get(r chi.Router, pattern string, h http.HandlerFunc, f routeFlag) {
	g.record(http.MethodGet, pattern, f)
	g.router(r, f).Get(pattern, h)
}

func (g routeGroup) Head(r chi.Router, pattern string, h http.HandlerFunc, f routeFlag) {
	g.record(http.MethodHead, pattern, f)
	g.router(r, f).Head(pattern, h)
}

func (g routeGroup) Post(r chi.Router, pattern string, h http.HandlerFunc, f routeFlag) {
	g.record(http.MethodPost, pattern, f)
	g.router(r, f).Post(pattern, h)
}

func (g routeGroup) Put(r chi.Router, pattern string, h http.HandlerFunc, f routeFlag) {
	g.record(http.MethodPut, pattern, f)
	g.router(r, f).Put(pattern, h)
}

func (g routeGroup) Delete(r chi.Router, pattern string, h http.HandlerFunc, f routeFlag) {
	g.record(http.MethodDelete, pattern, f)
	g.router(r, f).Delete(pattern, h)
}

// Handle registers every method, as chi's Handle does.
func (g routeGroup) Handle(r chi.Router, pattern string, h http.Handler, f routeFlag) {
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace} {
		g.record(m, pattern, f)
	}
	g.router(r, f).Handle(pattern, h)
}

// currentPolicy is the policy the running router registered; nil before
// registerRoutes has run, when every lookup answers "no flag".
func (s *Server) currentPolicy() *routePolicy { return s.policy.Load() }
