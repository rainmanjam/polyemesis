package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/auth"
)

// Audit events are the security half of the alert catalogue: somebody signed
// in, somebody changed the password, somebody minted a token, somebody saved
// the settings. They travel the alert path rather than the hook path because
// docs/HOOKS.md draws that line at "a person reading Slack" versus "a script",
// and every one of these is read and acted on by a person. It is also the only
// choice that survives an attack: a hook is promised never to be coalesced and
// to be delivered in order per endpoint, so routing failed sign-ins through
// internal/hooks would turn a brute force into a denial of service against the
// operator's own receiver.
//
// Two rules hold for everything in this file, and both exist because the
// destination is a chat channel that outlives the incident and gets
// screenshotted into tickets.
//
// The first is that nothing here sets Event.Key. Notifier.Publish defaults an
// empty key to the type name, which gives every audit event of one type a
// single coalescing subject for the whole install. That is not laziness: a key
// per source address would create one pending group per attacking address, and
// coalescer.evictIfFull drops a rule's OLDEST pending group once that rule
// holds maxGroupsPerRule -- scanning every group belonging to it regardless of
// event type. A distributed guesser would evict the destination.down sitting in
// its debounce window. Operational alerting has to survive security alerting,
// so each security type gets exactly one subject and the varying detail goes in
// a field.
//
// The second is that a NAME travels and a VALUE never does. alerts.Redact is a
// syntax matcher, and the description that used to stand here -- "it finds URLs,
// key=value pairs and Bearer headers" -- was true only of the spellings its
// table happens to hold. It finds a Bearer header written with a SPACE and not
// one written `Authorization:Bearer\ X`; it finds `passphrase=X` and not
// `-passphrase X` or `-rtmp_conn S:X`. It is a residual best-effort pass over
// free text and is NEVER a boundary; where a boundary is needed the exact
// credential literals are removed first (see supervisor.Spec.Secrets).
//
// It provably cannot see a bare scalar either: Redact("hunter2") returns
// "hunter2". For a
// settings value there would be nothing at all between the SRT passphrase and
// the channel except a hand-maintained allowlist, which is exactly the
// per-call-site enforcement redact.go's Redacted refuses to rely on. Naming the
// section that changed answers the operator's real question -- "was that me?"
// -- and carries nothing to leak.
//
// Nothing here is persisted. These are notifications, not an audit trail: the
// alert path is lossy by design, so under sustained delivery failure a security
// event can be dropped with only Stats.Dropped to show for it, and an attacker
// who deletes the only alert rule leaves no local record. That is stated in
// docs/MONITORING.md in those words rather than glossed over.

// fieldAddress labels the request's source address. Deliberately not "ip" or
// "client": alerts.SecretName normalises a field label and masks the value when
// it names a credential, so every label in this file has to be one that is not
// in that table -- a field called "token" would arrive as "[redacted]" and the
// operator would think something had gone wrong.
const (
	fieldAddress      = "Address"
	fieldFailures     = "Failures"
	fieldFailuresPrev = "Failures before"
	fieldTokenName    = "Token name"
	fieldTokenScope   = "Scope"
	fieldSections     = "Sections"
	fieldClipName     = "Clip"
	// The programme a clip was cut from. On a one-programme install it says
	// what everyone already knows; on a multi-programme one it is the only
	// thing in the event that distinguishes two clips cut a second apart from
	// different shows.
	fieldProgramme = "Programme"
	fieldVersion   = "Version"
	fieldForced    = "Forced past a live broadcast"
)

// clientIP is the address the request came from, resolved the same way the
// login throttle resolves it.
//
// Through auth.ClientIP rather than r.RemoteAddr directly, because the two
// disagree behind a reverse proxy: an alert that named the proxy on every
// sign-in would be telling the operator nothing at all. It honours
// X-Forwarded-For only when the operator has declared a proxy, which is the
// same trust decision the throttle makes and must not be made differently here
// -- an event that credited a forged header while the throttle counted the real
// address would name an innocent third party in the operator's channel.
// The result is checked to BE an address before it is allowed into a payload,
// which auth.ClientIP itself does not do and should not.
//
// The throttle only ever uses that string as a map key, so a garbage value
// there costs an attacker a bucket of their own and nothing else. Here the same
// string is rendered into a Slack or Discord message raised from
// POST /api/v1/auth/login, which is UNAUTHENTICATED. With trustProxyHeaders on,
// auth.ClientIP returns the leftmost X-Forwarded-For segment after nothing but
// a TrimSpace -- so without this check anyone who can reach the login endpoint
// can put arbitrary text of their choosing into the operator's channel, from
// off the internet, without credentials.
//
// alerts.Redact is not a backstop for this. It matches syntax -- URLs, k=v
// pairs, Bearer headers -- and a plain sentence passes through it untouched.
//
// A forged header falls back to the peer address, which is the truth about
// where the connection came from even when it is only the proxy. Naming the
// proxy is a small loss; relaying an attacker's prose to the operator as though
// it were a client address is not.
func (s *Server) clientIP(r *http.Request) string {
	if addr := auth.ClientIP(r, s.cfg.TrustProxyHeaders); net.ParseIP(addr) != nil {
		return addr
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil {
		// RemoteAddr is synthesised by net/http and is an address in every
		// real serving path, so reaching here means something has gone wrong
		// enough that naming nothing beats guessing.
		return "unknown"
	}
	return host
}

// publishAudit hands one audit event to the alert notifier.
//
// Both nil checks are load-bearing rather than defensive. s.mgr is nil in every
// test in this package -- testServer says so in as many words -- and
// Manager.Default takes a read lock on the manager, so an unguarded call turns
// POST /api/v1/auth/login into a panic under `go test ./internal/api`. It is
// reachable on a real install too: Manager.reconcile logs and continues when
// engine.New fails, so an install whose video pipeline will not build has no
// default engine, and a login endpoint that panics because of that is a far
// worse outage than the one it would have been reporting.
//
// One notifier, not all of them. Every engine builds its own but they all read
// the same install-wide alert_rules table, so the default engine's notifier
// already reaches every rule -- and publishing to each engine would deliver the
// same event once per source, because the coalescer is per-notifier. This is
// the same reach handleTestAlertRule already uses.
func (s *Server) publishAudit(ev alerts.Event) {
	// auditSink exists so the HANDLER WIRING can be tested, which it otherwise
	// could not be: s.mgr is nil throughout this package's tests, so every
	// publish returns at the next line and a test can only ever reach the
	// constructors. That gap was not theoretical -- all five publishAudit call
	// sites were deleted and the whole package still passed.
	if s.auditSink != nil {
		s.auditSink(ev)
		return
	}
	if s.mgr == nil {
		return
	}
	eng := s.eng()
	if eng == nil {
		// SAID OUT LOUD, because the events that reach here when no engine is
		// running are the security ones -- auditLoginFailed above all -- and an
		// install whose engines failed to build is exactly when an operator
		// most needs to know that repeated failed sign-ins went unreported.
		// Dropping them was correct; dropping them in silence was the defect
		// (#576): the alert rule is configured, the endpoint is healthy, and
		// nothing anywhere says why nothing arrived.
		//
		// Debug, not Warn: on a fresh install with no source yet this is the
		// normal state and every login would log a warning nobody can act on.
		s.log.Debug("audit event not published: this install has no running programme",
			"type", ev.Type)
		return
	}
	// Notifier.Publish is nil-receiver safe, so an engine that has one but has
	// not started it needs no third check here.
	eng.Alerts().Publish(ev)
}

// auditLoginFailed reports sign-in attempts that have passed the throttle's
// free allowance.
//
// The submitted username is deliberately absent. There is exactly one account
// on an install, so knowing which name was guessed tells the operator nothing
// they cannot already infer, and the field would be attacker-controlled text
// reflected verbatim into their chat channel -- an invitation to write a
// "username" that reads like a message from the software itself.
func auditLoginFailed(address string, failures int) alerts.Event {
	return alerts.Event{
		Type:     alerts.TypeLoginFailed,
		Severity: alerts.SeverityWarning,
		Title:    "Repeated failed sign-ins",
		Text: "Sign-in attempts from one address have been rejected past the free " +
			"allowance and are now being delayed.",
	}.
		WithField(fieldAddress, address).
		WithField(fieldFailures, strconv.Itoa(failures))
}

// auditLoginSucceeded reports an accepted sign-in.
//
// It carries the failure count that preceded it because that is the question a
// reader of auditLoginFailed asks next: a sign-in after nine rejections from
// the same address is the one message in this file that means the guessing
// worked.
func auditLoginSucceeded(address string, failuresBefore int) alerts.Event {
	ev := alerts.Event{
		Type:     alerts.TypeLoginSucceeded,
		Severity: alerts.SeverityInfo,
		Title:    "Signed in",
		Text:     "Somebody signed in to the console with the admin password.",
	}.WithField(fieldAddress, address)
	if failuresBefore > 0 {
		// Only when there were any. WithField already drops an empty value, but
		// "Failures before: 0" is worse than absent: it puts a number in front
		// of a reader on every routine sign-in and trains them to skip the line
		// that matters.
		ev = ev.WithField(fieldFailuresPrev, strconv.Itoa(failuresBefore))
	}
	return ev
}

// auditPasswordChanged reports a replaced admin password.
func auditPasswordChanged(address string) alerts.Event {
	return alerts.Event{
		Type:     alerts.TypePasswordChanged,
		Severity: alerts.SeverityCritical,
		Title:    "Admin password changed",
		Text: "The admin password was replaced. Every session issued before this " +
			"moment has been refused, so if this was not you, you are already " +
			"signed out.",
	}.WithField(fieldAddress, address)
}

// auditAPITokenCreated reports a minted API token.
//
// The token's PREFIX is not here, though the token list in the UI shows it. The
// prefix is a leading fragment of the plaintext, and the argument for showing it
// in an authenticated response -- it identifies a row without helping anyone
// guess the remaining bits -- does not carry over to a message this process
// hands to a third-party chat host and never sees again. The name is what
// answers "was that me?", and the name is enough.
// auditAPITokenRevoked names the token that was destroyed.
//
// The name is the same field the created event carries, so a reader can pair
// the two by eye in a channel. Nothing else about the token travels: the digest
// identifies a credential and the plaintext never existed after minting.
func auditAPITokenRevoked(name, address string) alerts.Event {
	return alerts.Event{
		Type:     alerts.TypeAPITokenRevoked,
		Severity: alerts.SeverityWarning,
		Title:    "API token revoked",
		Text: "An API token was destroyed. Anything still authenticating with it " +
			"stops working now.",
	}.
		WithField(fieldTokenName, name).
		WithField(fieldAddress, address)
}

// auditClipCaptured names the clip, which is a server-generated filename rather
// than anything a viewer or an operator typed, so it is safe to carry whole.
// THE CLIP'S OWN PROGRAMME, and the capture itself is already scoped.
//
// handleCaptureClip resolves the engine through scopedEngine, so the damaging
// half of #545 -- Studio B being handed Main's rolling buffer -- is closed at
// Control. What was left is traceability: the audit event named the clip and
// the operator's address and not the show, so an install running two
// programmes produced a stream of "Clip captured" events that could not be told
// apart. Delivery through the install-wide notifier is deliberate and argued
// (alert rules are install-wide, one table); naming the source is the fix, not
// routing.
func auditClipCaptured(name, address, programme string) alerts.Event {
	return alerts.Event{
		Type:     alerts.TypeClipCaptured,
		Severity: alerts.SeverityInfo,
		Title:    "Clip captured",
		Text:     "A clip was cut from the replay buffer and is available to download.",
	}.
		WithField(fieldClipName, name).
		// WithField already drops an empty value, so a zero-source install adds
		// no field rather than an empty one.
		WithField(fieldProgramme, programme).
		WithField(fieldAddress, address)
}

func auditAPITokenCreated(name, scope, address string) alerts.Event {
	return alerts.Event{
		Type:     alerts.TypeAPITokenCreated,
		Severity: alerts.SeverityCritical,
		Title:    "API token created",
		// The scope travels because it is now the difference between a
		// credential that can read the dashboard and one that can delete a
		// destination, and this alert is read by somebody deciding whether the
		// token they are looking at is the one they expected. The old text
		// asserted every token "acts as the admin, is limited to no part of the
		// API", which stopped being true when scopes shipped -- and an audit
		// trail that describes the wrong power is worse than one that says
		// nothing.
		Text: "A new API token was minted. It does not expire, and it keeps " +
			"working after the password is changed.",
	}.
		WithField(fieldTokenName, name).
		WithField(fieldTokenScope, scope).
		WithField(fieldAddress, address)
}

// auditUpgradeStaged reports a replaced server binary (#148).
//
// The version and the forced flag travel, and nothing else does. Both are the
// same judgement auditAPITokenCreated makes about what is safe to hand a
// third-party chat host: the version is a release tag this server chose from
// its own update feed, never operator text, and the forced flag is a boolean.
//
// The BINARY PATH is deliberately absent, though the log line beside this call
// carries it. A log line stays on the box; this event is delivered to whatever
// webhook or chat channel the install has configured, and the filesystem
// layout of somebody's server is not something to broadcast to answer a
// question -- "which version, and did it interrupt a broadcast" -- that the
// path does not help with.
//
// The forced case is the one an operator will want to find later, so it is a
// named field rather than a sentence: a field can be searched for.
func auditUpgradeStaged(version string, forced bool, address string) alerts.Event {
	ev := alerts.Event{
		Type:     alerts.TypeUpgradeStaged,
		Severity: alerts.SeverityCritical,
		Title:    "Server binary replaced",
		Text: "A new release was staged over this server's own binary. It takes " +
			"effect at the next restart, and it survives a password change and a " +
			"token revocation.",
	}.
		WithField(fieldVersion, version).
		WithField(fieldAddress, address)
	if forced {
		// Only when true, on auditLoginSucceeded's reasoning: "Forced: no" on
		// every routine upgrade trains the reader to skip the line that matters.
		ev = ev.WithField(fieldForced, "yes")
	}
	return ev
}

// auditUpgradeRolledBack reports the previous binary being put back.
//
// No version: a rollback names no release. It restores whatever this box was
// running before the last stage, and inventing a tag for it would be a guess
// printed as a fact.
func auditUpgradeRolledBack(forced bool, address string) alerts.Event {
	ev := alerts.Event{
		Type:     alerts.TypeUpgradeRolledBack,
		Severity: alerts.SeverityCritical,
		Title:    "Server binary rolled back",
		Text: "The previous server binary was restored. It takes effect at the " +
			"next restart.",
	}.WithField(fieldAddress, address)
	if forced {
		ev = ev.WithField(fieldForced, "yes")
	}
	return ev
}

// auditSettingsChanged reports a settings save that altered something, naming
// the sections and never their contents. See changedSections.
func auditSettingsChanged(sections []string, address string) alerts.Event {
	return alerts.Event{
		Type:     alerts.TypeSettingsChanged,
		Severity: alerts.SeverityWarning,
		Title:    "Settings changed",
		Text:     "A settings save altered the stored configuration.",
	}.
		WithField(fieldSections, strings.Join(sections, ", ")).
		WithField(fieldAddress, address)
}

// changedSections names the top-level settings sections whose stored JSON
// differs between two saves. Both arguments are json.Marshal of a db.Settings.
//
// Comparing marshalled JSON rather than Go fields is what makes this survive
// the settings document growing. A hand-written list of comparisons would go
// stale the first time somebody adds a section and forgets this file, and the
// failure would be silent: an alert that quietly stopped covering the block the
// operator most wanted covered. Encoding a struct is deterministic in Go --
// fields in declaration order, map keys sorted -- so equal bytes really do mean
// an unchanged section.
//
// The comparison happens on bytes that never leave this function. What comes
// out is a list of keys, and the keys of db.Settings are a fixed vocabulary
// that no operator input can enter; that is the whole reason this returns names
// rather than a diff.
//
// Unparseable input returns nothing, which suppresses the alert rather than
// inventing one. Both arguments come from marshalling a document the store has
// already serialised successfully, so this is unreachable in practice, and the
// alternative -- guessing that everything changed -- would fire the noisiest
// possible message on the least informed possible basis.
func changedSections(before, after []byte) []string {
	var a, b map[string]json.RawMessage
	if json.Unmarshal(before, &a) != nil || json.Unmarshal(after, &b) != nil {
		return nil
	}
	changed := map[string]bool{}
	for k, v := range b {
		if prev, ok := a[k]; !ok || string(prev) != string(v) {
			changed[k] = true
		}
	}
	// Sections that vanished count too. They cannot happen through the settings
	// endpoint, which decodes over the stored document, but they can happen
	// across an upgrade that removes a block, and "the section you configured
	// is gone" is exactly the kind of thing an operator should hear about.
	for k := range a {
		if _, ok := b[k]; !ok {
			changed[k] = true
		}
	}
	if len(changed) == 0 {
		return nil
	}
	out := make([]string, 0, len(changed))
	for k := range changed {
		out = append(out, k)
	}
	// Sorted because map iteration is not, and an alert whose section list
	// arrives in a different order every time is one a reader cannot compare
	// against the last one.
	sort.Strings(out)
	return out
}

// auditDebugExported records that a debug bundle was downloaded.
//
// CRITICAL rather than Info, and the severity is the point. Nothing has broken
// -- but a copy of this server's own logs has just left the operator's control
// for somebody who does not have the box, and polyemesis deliberately keeps no
// copy of what was sent. This entry is the only durable record that it happened
// at all, which is exactly the question an audit trail exists to answer.
//
// The COUNTS travel, not the contents. "How many lines, and was it truncated"
// is what somebody reviewing the trail later needs in order to know what was
// disclosed; the lines themselves are in the bundle, and putting any of them
// here would put log contents into the alert pipeline -- which reaches webhooks.
func auditDebugExported(records int, truncated bool, address string) alerts.Event {
	text := fmt.Sprintf("A debug bundle of %d log records was downloaded.", records)
	if truncated {
		text += " The capture was truncated: older records had already been dropped."
	}
	return alerts.Event{
		Type:     alerts.TypeDebugExported,
		Severity: alerts.SeverityCritical,
		Title:    "Debug bundle exported",
		Text:     text,
	}.
		WithField(fieldAddress, address)
}
