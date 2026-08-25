#!/usr/bin/env bash
# Outbound webhooks, over a real socket.
#
# internal/hooks is 2,150 lines whose entire job is to make an HTTP request to
# somebody else's server, and until now every test of it replaced the HTTP
# client. Its unit tests are good -- ordering, the drop path, the subscription
# filter and the signature are all pinned -- but they are pinned through a
# WithDoer that returns a struct literal, a WithSleep that returns instantly and
# a WithClock. Three of the four things this suite measures are things those
# options define out of existence:
#
#   A FAKE DOER CANNOT PRODUCE A *url.Error. net/http renders a transport
#   failure as `Post "https://host/PATH": dial tcp ...` -- the FULL URL, and a
#   Slack or Discord webhook keeps its whole credential in that path. The
#   three-pass scrub in dispatch.go (ClientErrorText, then this hook's own
#   SecretSet, then Redact as the residual) exists for that one string, and no
#   test in the package has ever constructed it. Steps 5 and 6 do.
#
#   A FAKE SLEEP CANNOT MEASURE BACKOFF. TestA503IsRetried counts three attempts
#   with the wait stubbed out, so backoffBase could be a nanosecond. Step 3
#   measures the gaps on a wall clock.
#
#   A HAND-BUILT RESPONSE CANNOT PROVE THE BYTES SURVIVED. Step 2 recomputes the
#   HMAC from the bytes an independent HTTP server received, with the header
#   names written out as literals rather than read from the package's own
#   constants -- so a renamed header is a failure here rather than a silent
#   break in somebody's receiver.
#
# TWO TIERS, ONE SUITE.
#
#   Steps 1-8 need NO credentials and contact nothing outside this machine: the
#   far end is an http.Server the driver starts on a loopback port, which is
#   what made hooks the cheapest package on the untested-against-reality list.
#   That is not a lesser tier. A real listener is a real socket, a real
#   net/http transport, real timeouts and real *url.Error text, and every
#   defect above is reachable from it.
#
#   Step 9 needs a real remote endpoint and SKIPS without one, in the shape
#   acceptance-multistream and acceptance-chat both use. It is the only step
#   that proves anything about DNS, TLS or a third party's HTTP stack.
#
# ON SECRETS. Every credential in steps 1-8 is minted from crypto/rand inside
# the driver, exists for one run and is never printed -- it leaves the driver
# only as booleans and lengths. Step 9's endpoint URL is itself a credential,
# which is the whole premise of the suite, so it arrives in the environment and
# NOTHING derived from it is printed: not the URL, not the host, not the
# endpoint's reply, not the error text.
#
#   ./scripts/acceptance-hooks.sh
#
# Environment:
#   POLY_HOOKS_URL   a webhook endpoint you control; enables step 9. Never pass
#                    it on a command line: argv is world-readable via ps(1).
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
DRIVER="$ROOT/scripts/acceptance_hooks_driver.go"

# poka-yoke: this suite's own `drive` is nothing but `go run`. Without this,
# a host with go off PATH gets "go: command not found" parsed as driver
# output and reports an ordinary-looking 0 passed / N failed -- see
# lib-preflight.sh for the incident that found this.
. "$SCRIPTS/lib-preflight.sh"
poly_require_cmd go "needed to run the acceptance driver via 'go run'"

cd "$ROOT" || exit 1

pass=0; fail=0; skip=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
sk()   { printf "  \033[33mSKIP\033[0m  %s\n" "$1"; skip=$((skip+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }
note() { printf "        %s\n" "$1"; }

# drive runs one driver command and captures its key=value lines.
drive() { go run "$DRIVER" "$@" 2>&1; }

# val <output> <key> -> the value, or empty. Anchored, so `sent` cannot match
# `lastSent` -- the class of loose-grep error that has produced two false alarms
# in this repo already.
val() { printf '%s\n' "$1" | sed -n "s/^$2=//p" | head -1; }

# is <output> <key> <want> -> true when the key is present AND equals want. A
# MISSING key is false, never a silent pass: a driver that died before emitting
# would otherwise leave every "must be false" check green.
is() { [ "$(val "$1" "$2")" = "$3" ]; }

# num <output> <key> -> the value if it is a whole number, else the empty string,
# which every arithmetic test below then fails. Guards the shape of the input
# rather than trusting it.
num() { case "$(val "$1" "$2")" in ''|*[!0-9]*) printf '' ;; *) val "$1" "$2" ;; esac; }

# between <value> <lo> <hi> -- inclusive, and false for anything non-numeric.
between() {
  case "$1" in ''|*[!0-9]*) return 1 ;; esac
  [ "$1" -ge "$2" ] && [ "$1" -le "$3" ]
}

printf "\033[1moutbound webhooks, over a real socket\033[0m\n"

# ---------------------------------------------------------------------------
step "1. A delivery reaches a listening endpoint, addressed as documented"

D="$(drive deliver)"

# Proven able to fail against the committed tree by changing dispatch.go's
# fanOut `wants := w.hook.Wants(ev.Trigger)` to `... && false`.
if [ "$(num "$D" got)" = 1 ]; then
  ok "one POST arrived at an http.Server on a loopback port"
else
  bad "the endpoint received $(val "$D" got) deliveries, expected 1"
  note "everything below reads the request that did not arrive"
fi

# Proven able to fail against the committed tree by changing dispatch.go's
# `req.Header.Set("Content-Type", "application/json")` to "text/plain".
if is "$D" method POST && is "$D" pathMatches true \
   && is "$D" contentType application/json && is "$D" userAgent polyemesis; then
  ok "it is a POST to the hook's own path, application/json, User-Agent polyemesis"
else
  bad "the request line or headers are wrong: method=$(val "$D" method) path=$(val "$D" pathMatches) type=$(val "$D" contentType) agent=$(val "$D" userAgent)"
fi

# FIVE, and the driver looks them up by literal name. A receiver switches on
# these strings, so a constant renamed in payload.go is a broken receiver and
# the package's own tests -- which read the same constants -- cannot see it.
#
# Proven able to fail against the committed tree by deleting dispatch.go's
# `req.Header.Set(SequenceHeader, strconv.FormatUint(env.Sequence, 10))`.
if [ "$(num "$D" polyHeaders)" = 5 ]; then
  ok "all five X-Polyemesis-* headers arrived under their documented names"
else
  bad "$(val "$D" polyHeaders) of 5 X-Polyemesis-* headers arrived"
fi

# Decoded into a struct the DRIVER declares, not into hooks.Envelope. Decoding
# into the package's own type follows a renamed JSON tag wherever it goes and
# reports a broken contract as intact.
#
# Proven able to fail against the committed tree by changing payload.go's
# `const SpecVersion = "1"` to "2".
if is "$D" decoded true && is "$D" specVersion 1 \
   && is "$D" trigger ingest.published && is "$D" sequence 1 \
   && is "$D" sourceID 7 && is "$D" sourceName Main \
   && is "$D" idLen 32 && is "$D" idIsHex true; then
  ok "the envelope decoded into a receiver's own struct: specVersion 1, trigger, sequence 1, source, a 128-bit hex id"
else
  bad "the envelope is not the documented shape: decoded=$(val "$D" decoded) spec=$(val "$D" specVersion) trigger=$(val "$D" trigger) seq=$(val "$D" sequence) src=$(val "$D" sourceID)/$(val "$D" sourceName) id=$(val "$D" idLen)/$(val "$D" idIsHex)"
fi

# One minute, because `at` is the moment of the transition and this suite
# publishes it immediately. A wide window still catches the failures worth
# catching -- a zero time, a local time labelled UTC, a clock in the wrong unit.
#
# Proven able to fail against the committed tree by changing dispatch.go's
# `At: ev.At.UTC()` to `At: time.Time{}`, which still parses and still ends in
# a Z -- so the skew bound is the whole of this check's teeth.
if is "$D" atParses true && is "$D" atIsUTC true \
   && between "$(num "$D" atSaneMs)" 0 60000; then
  ok "its \`at\` is RFC3339 with a Z, and within a minute of now"
else
  bad "\`at\` is not a usable UTC timestamp: parses=$(val "$D" atParses) z=$(val "$D" atIsUTC) skew=$(val "$D" atSaneMs)ms"
fi

# ---------------------------------------------------------------------------
step "2. The signature verifies when a receiver recomputes it off the wire"

# NOTHING FROM internal/hooks IS INVOLVED IN THE RECOMPUTATION. The driver runs
# crypto/hmac over the timestamp header, a dot, and the body bytes it received,
# which is what the doc comment on Sign tells a receiver to do. Calling
# hooks.Sign here would prove only that the function agrees with itself.
#
# Proven able to fail against the committed tree by changing payload.go's
# `mac.Write([]byte("."))` to a colon.
if is "$D" sigVerifies true; then
  ok "an independent HMAC-SHA256 over <timestamp>.<body> matches X-Polyemesis-Signature"
else
  bad "the signature does not verify; every receiver would reject every delivery"
fi

# A signature verifies over whatever timestamp was signed, including one from
# 1970. Two minutes is the tolerance a receiver would plausibly set, and the
# reason the timestamp is inside the MAC at all is so it can.
#
# Proven able to fail against the committed tree by changing attempt's
# `ts := d.now().Unix()` to `ts := int64(0)`. Note that the signature check
# above stays GREEN under that mutation -- it recomputes over whatever
# timestamp was sent -- which is precisely why this is a separate check.
if is "$D" tsParses true && between "$(num "$D" tsSkewMs)" 0 120000; then
  ok "the signed timestamp is within two minutes of now, so a replay window accepts it"
else
  bad "the timestamp header is unusable: parses=$(val "$D" tsParses) skew=$(val "$D" tsSkewMs)ms"
fi

# NARROW BY DESIGN, and worth stating: this proves the signing key is not
# carried in a header VALUE or in the body of this delivery. It says nothing
# about a header the endpoint might see under some other configuration.
#
# Proven able to fail against the committed tree by adding
# `req.Header.Set("X-Debug-Secret", h.Secret)` to attempt.
if is "$D" signSecretInHeader false && is "$D" signSecretInBody false; then
  ok "the signing key is proven by the signature and appears in no header value or body"
else
  bad "the raw signing key was sent: header=$(val "$D" signSecretInHeader) body=$(val "$D" signSecretInBody)"
fi

# The path credential is in the request TARGET by construction -- it is the
# endpoint -- so the question is only whether it is also somewhere it has no
# business being, where a proxy or a log would copy it separately.
#
# Proven able to fail against the committed tree by adding
# `req.Header.Set("X-Debug-Url", h.URL)` to attempt.
if is "$D" pathSecretInHeader false && is "$D" pathSecretInBody false; then
  ok "the endpoint's path credential appears in no header value and not in the body"
else
  bad "the URL path credential was duplicated: header=$(val "$D" pathSecretInHeader) body=$(val "$D" pathSecretInBody)"
fi

# ---------------------------------------------------------------------------
step "3. A retryable failure is retried, on the real clock"

R="$(drive retry)"

# Proven able to fail against the committed tree by changing attempt's
# retryable case `resp.StatusCode >= 500` to `>= 600`.
if [ "$(num "$R" attempts)" = 3 ]; then
  ok "two 503s were retried and the third attempt was accepted"
else
  bad "the endpoint saw $(val "$R" attempts) attempts, expected 3"
fi

# A receiver deduplicating by delivery id needs every attempt at one transition
# to carry the same id and sequence. Three ids for one event looks to a script
# like three events, which is the failure retries are supposed to avoid.
#
# blankIDOrSeq IS THE NON-VACUITY HALF, and it is here because the check without
# it was green under a mutation that DELETED the sequence header: three empty
# strings are one distinct value, so "they all agree" was true and meant nothing.
#
# Proven able to fail against the committed tree twice: by adding
# `env.ID, _ = deliveryID()` inside deliver's attempt loop (ids=3), and by
# deleting the SequenceHeader line in attempt (blank=3).
if is "$R" distinctIDs 1 && is "$R" distinctSeqs 1 && is "$R" blankIDOrSeq 0; then
  ok "all three attempts carried one delivery id and one sequence number, and neither was blank"
else
  bad "the retries were not identifiable as one delivery: ids=$(val "$R" distinctIDs) sequences=$(val "$R" distinctSeqs) blank=$(val "$R" blankIDOrSeq)"
fi

# THE CHECK THIS STEP EXISTS FOR. backoffFor is exponential from backoffBase,
# and with WithSleep stubbed -- which is how the unit test runs it -- the base
# could be a nanosecond and nothing would notice. These are the waits a retried
# endpoint actually gets. Bounded above as well as below, because a base that
# grew would stall everything queued behind it on the same endpoint.
#
# Proven able to fail against the committed tree by changing dispatch.go's
# `backoffBase = time.Second` to time.Millisecond: gap1=1ms gap2=2ms. The
# package's own TestA503IsRetried stays green through that same change.
G1="$(num "$R" gap1Ms)"; G2="$(num "$R" gap2Ms)"
if between "$G1" 900 2500 && between "$G2" 1800 4500 && [ "${G2:-0}" -gt "${G1:-0}" ]; then
  ok "the waits were ${G1}ms then ${G2}ms -- one second, then two, and growing"
else
  bad "the backoff is not the documented 1s then 2s: gap1=${G1:-?}ms gap2=${G2:-?}ms"
fi

# Proven able to fail against the committed tree by deleting deliver's
# `d.bump(func(s *Stats) { s.Retries++ })`: the wire still shows three attempts
# and the counter reports none.
if is "$R" sent 1 && is "$R" retries 2 && is "$R" failed 0 \
   && is "$R" recAttempts 3 && is "$R" recStatus 200; then
  ok "the counters and the operator's delivery record agree: 1 sent, 2 retries, 3 attempts, HTTP 200"
else
  bad "the accounting disagrees with the wire: sent=$(val "$R" sent) retries=$(val "$R" retries) failed=$(val "$R" failed) recAttempts=$(val "$R" recAttempts) recStatus=$(val "$R" recStatus)"
fi

# ---------------------------------------------------------------------------
step "4. A permanent refusal is delivered once"

P="$(drive permanent)"

# The hook allows three attempts; a 404 must still consume one. Retrying a
# deleted endpoint only delays everything queued behind it, and behind it is
# everything for THAT endpoint, because ordering is preserved by never
# overtaking.
#
# Both checks in this step proven able to fail against the committed tree by
# changing attempt's default case to `return resp.StatusCode, snippet, true,
# statusError(resp.StatusCode)`: 3 attempts and 2 retries for one 404.
if [ "$(num "$P" attempts)" = 1 ] && is "$P" recAttempts 1 && is "$P" recStatus 404; then
  ok "a 404 was attempted once out of an allowance of three, and recorded as such"
else
  bad "a 404 was attempted $(val "$P" attempts) times: recAttempts=$(val "$P" recAttempts) recStatus=$(val "$P" recStatus)"
fi

if is "$P" failed 1 && is "$P" retries 0 && is "$P" sent 0; then
  ok "it counts as one failure and no retry"
else
  bad "the counters are wrong for a permanent failure: failed=$(val "$P" failed) retries=$(val "$P" retries) sent=$(val "$P" sent)"
fi

# ---------------------------------------------------------------------------
step "5. A silent endpoint is abandoned on the hook's own timeout"

T="$(drive transport)"

# The endpoint holds the request for six seconds against a one-second hook, so
# the number distinguishes "we gave up" from "they eventually answered". A
# timeout that did not apply would read ~6000 here.
#
# Proven able to fail against the committed tree by changing attempt's
# `time.Duration(h.TimeoutSeconds)*time.Second` to time.Minute: recorded=6001ms,
# i.e. the endpoint's silence rather than our deadline.
TO="$(num "$T" timeoutRecordedMs)"; TOBS="$(num "$T" timeoutObservedMs)"
if is "$T" timeoutRecs 1 && between "$TO" 900 3000 && between "$TOBS" 900 5000; then
  ok "abandoned after ${TO}ms -- the hook's own one-second timeout, not the endpoint's six-second silence"
else
  bad "the timeout did not bound the delivery: recorded=${TO:-?}ms observed=${TOBS:-?}ms recs=$(val "$T" timeoutRecs)"
fi

# NON-VACUITY FOR EVERYTHING IN STEP 6. A blank error would satisfy every "no
# credential" check below while telling the operator nothing, which is the trade
# alerts.ClientErrorText's doc comment explicitly refuses: the three failures
# anyone has to tell apart -- a name that will not resolve, a refused
# connection, a certificate that will not verify -- differ only in wording.
#
# Proven able to fail against the committed tree by changing deliver's
# `rec.Error = alerts.Redact(secrets.Scrub(alerts.ClientErrorText(err)))` to
# `rec.Error = ""`. THAT MUTATION PASSES EVERY "carries no credential" CHECK IN
# STEP 6, which is the entire reason this check exists beside them.
if between "$(num "$T" timeoutErrLen)" 1 100000 && is "$T" timeoutErrNamesHost true; then
  ok "the failure is recorded with the endpoint's host still readable in it"
else
  bad "the recorded error is blank or has lost the host: len=$(val "$T" timeoutErrLen) namesHost=$(val "$T" timeoutErrNamesHost)"
fi

# ---------------------------------------------------------------------------
step "6. A transport failure discloses no part of the endpoint's path"

# WHY THIS IS THE STEP THE SUITE WAS BUILT FOR. A webhook endpoint keeps its
# entire credential in its URL path, and net/http puts the full URL into the
# text of every transport error. That text reaches four places an operator or a
# read-scoped token can read: the delivery record, Stats.LastError (served at
# GET /api/v1/hooks/meta), the deliveries JSON, and server.log. #310 was
# precisely this class one package over -- a refused destination wrote its
# stream key to the log on every retry.

# THE CONTROL, FIRST. Each of the checks below asks whether a credential is
# absent from a string. That question is meaningless unless the string had one
# to begin with, and the fastest way to make this whole step green by accident
# is an endpoint URL whose path never reached the error at all. So the driver
# makes the same two requests with a bare http.Client and reports whether the
# RAW error carries the planted secret.
#
# Proven able to fail against the committed tree by changing the driver's
# rawErrorCarries to look for `secret+"-nope"` -- the check has teeth against a
# control that stopped controlling for anything.
if is "$T" timeoutRawHasSecret true && is "$T" refusedRawHasSecret true; then
  ok "control: the raw net/http errors do carry the path credential, so there is something to remove"
else
  bad "control failed -- the raw errors carried no credential, so every check below proves nothing"
  note "timeoutRaw=$(val "$T" timeoutRawHasSecret) refusedRaw=$(val "$T" refusedRawHasSecret)"
  note "if a port was reused between reserving and closing it, rerun before investigating"
fi

# Both failure modes, because they are different errors inside the same wrapper
# and only one of them was ever likely to be tried by hand. Each is required to
# still name its host, so a scrub that blanked the field cannot pass here.
#
# Proven able to fail against the committed tree in both directions: by changing
# deliver's `rec.Error = alerts.Redact(secrets.Scrub(alerts.ClientErrorText(err)))`
# to `alerts.Redact(err.Error())` (both HasSecret go true), and by setting it to
# "" (both lose the host). BOTH LAYERS HAVE TO GO for the credential to appear:
# ClientErrorText rebuilds the URL and the SecretSet then removes the literal, so
# dropping either one alone leaves this green -- which is defence in depth
# working, and is why the mutation names both.
if is "$T" timeoutErrHasSecret false && is "$T" refusedErrHasSecret false \
   && is "$T" timeoutErrNamesHost true && is "$T" refusedErrNamesHost true \
   && between "$(num "$T" timeoutErrLen)" 1 100000 \
   && between "$(num "$T" refusedErrLen)" 1 100000; then
  ok "neither recorded delivery error carries the path, and both still name the host"
else
  bad "a recorded delivery error is wrong: timeoutHasSecret=$(val "$T" timeoutErrHasSecret) refusedHasSecret=$(val "$T" refusedErrHasSecret) hosts=$(val "$T" timeoutErrNamesHost)/$(val "$T" refusedErrNamesHost)"
fi

# Stats.LastError is SERVED, to a read-scoped token, at GET /api/v1/hooks/meta.
# A credential here is a read token escalating to "can post into your Slack".
#
# Proven able to fail against the committed tree by the same two mutations as
# the check above -- LastError is a copy of rec.Error, and this check exists
# because it is the copy that is SERVED.
if is "$T" timeoutLastErrHasSecret false && is "$T" refusedLastErrHasSecret false \
   && between "$(num "$T" timeoutLastErrLen)" 1 100000 \
   && between "$(num "$T" refusedLastErrLen)" 1 100000; then
  ok "Stats.LastError, which a read-scoped token may fetch, carries none of it and is not blank"
else
  bad "Stats.LastError is wrong: timeout=$(val "$T" timeoutLastErrHasSecret)/$(val "$T" timeoutLastErrLen) refused=$(val "$T" refusedLastErrHasSecret)/$(val "$T" refusedLastErrLen)"
fi

# THE #310 SHAPE. A scrub that covers the API response and not the log file has
# moved the disclosure rather than closed it, and a log file outlives the
# process, gets tailed into a terminal and gets pasted into bug reports.
#
# Proven able to fail against the committed tree by changing the Warn call's
# `"url", hook.RedactedURL()` to `"url", hook.URL` -- which is #310 exactly, one
# package over, and which every other check in this step stays green through.
if is "$T" timeoutLogHasSecret false && is "$T" refusedLogHasSecret false \
   && is "$T" timeoutLogSaysFailed true && is "$T" refusedLogSaysFailed true; then
  ok "the dispatcher's own log lines report the failure and carry no part of the path"
else
  bad "the log is wrong: timeoutHasSecret=$(val "$T" timeoutLogHasSecret) refusedHasSecret=$(val "$T" refusedLogHasSecret) saysFailed=$(val "$T" timeoutLogSaysFailed)/$(val "$T" refusedLogSaysFailed)"
fi

# The whole delivery log as GET /api/v1/hooks/{id}/deliveries serialises it,
# rather than the one field this suite happened to read.
#
# THE HOST AND THE LENGTH ARE THE POINT OF THE OTHER TWO CONDITIONS. Written as
# "has no secret" alone this reported SAFE against a marshalled EMPTY LIST --
# the driver was reading Deliveries after stopping the dispatcher, which deletes
# the worker. `[]` contains no credential and proves nothing.
#
# Proven able to fail against the committed tree by the `alerts.Redact(err.Error())`
# mutation named two checks above: hasSecret=true, len=277.
if is "$T" timeoutDeliveriesJSONHasSecret false \
   && is "$T" timeoutDeliveriesJSONHasHost true \
   && between "$(num "$T" timeoutDeliveriesJSONLen)" 50 1000000; then
  ok "the marshalled delivery log has the record in it, names the host, and carries no part of the path"
else
  bad "the delivery log's JSON is wrong: hasSecret=$(val "$T" timeoutDeliveriesJSONHasSecret) hasHost=$(val "$T" timeoutDeliveriesJSONHasHost) len=$(val "$T" timeoutDeliveriesJSONLen)"
fi

# Hook.MarshalJSON substitutes RedactedURL and a hasSecret boolean. This is the
# rendering a settings page receives, so a regression is a credential on screen.
#
# Proven able to fail against the committed tree by changing hooks.go's
# MarshalJSON `h.RedactedURL()` to `h.URL`.
if is "$T" hookJSONHasPathSecret false && is "$T" hookJSONHasSignSecret false \
   && is "$T" hookJSONNamesHost true; then
  ok "a marshalled Hook shows the host and neither credential"
else
  bad "a marshalled Hook is wrong: path=$(val "$T" hookJSONHasPathSecret) signing=$(val "$T" hookJSONHasSignSecret) host=$(val "$T" hookJSONNamesHost)"
fi

# ---------------------------------------------------------------------------
step "7. An endpoint that quotes credentials back does not get them stored"

# A DIFFERENT CLASS FROM STEP 6, and the reason dispatch.go applies two passes
# rather than one. Step 6's credential arrives inside a string net/http built,
# in a shape ClientErrorText knows. This one arrives inside a body a STRANGER
# built, in whatever shape they chose -- here JSON, which alerts.Redact's own
# doc says it cannot see into. Only the per-hook exact SecretSet covers it.
E="$(drive echo)"

# Proven able to fail against the committed tree by changing attempt's
# `snippet = alerts.Redact(string(raw))` to read `raw[:0]`: len=0, keptText=false.
# The two checks below stay GREEN through it, which is exactly what makes this
# one necessary rather than decorative.
if is "$E" recs 1 && is "$E" respKeptText true \
   && between "$(num "$E" respLen)" 1 100000 && is "$E" recStatus 400; then
  ok "the endpoint's own words were kept -- the operator can still read why it said no"
else
  bad "the endpoint's response was not usefully recorded: recs=$(val "$E" recs) len=$(val "$E" respLen) keptText=$(val "$E" respKeptText) status=$(val "$E" recStatus)"
fi

# Both proven able to fail against the committed tree by changing deliver's
# `rec.Status, rec.Response = status, secrets.Scrub(snippet)` to drop the Scrub.
# alerts.Redact alone -- which still runs -- does not catch either of them: the
# body is JSON, and "signing" is not a name in its bareSecret table.
if is "$E" respHasPathSecret false; then
  ok "its echo of the URL path was scrubbed out of the stored response"
else
  bad "the endpoint quoted the URL path back and it was stored verbatim"
fi

if is "$E" respHasSignSecret false; then
  ok "its echo of the signing key was scrubbed out of the stored response"
else
  bad "the endpoint quoted the signing key back and it was stored verbatim"
fi

# ---------------------------------------------------------------------------
step "8. The test button, which skips the queue entirely"

# Dispatcher.Test takes the hook straight from the request body and bypasses
# both the intake queue and the subscription filter, so nothing above covers a
# line of it. It is also the only hook path most operators ever exercise.
B="$(drive testbutton)"

# Proven able to fail against the committed tree by changing Test's
# `Status: status` to `Status: status * 0`: the delivery still lands and the
# operator is told nothing about how it went.
if is "$B" testFailed false && is "$B" testStatus 200 && is "$B" testGot 1; then
  ok "one test delivery reached the endpoint and its status was reported back"
else
  bad "the test delivery did not land: failed=$(val "$B" testFailed) status=$(val "$B" testStatus) got=$(val "$B" testGot)"
fi

# The operator is handed Body and Signature to check their own verifier against.
# If Body is not the bytes that went out, that exercise is a lie.
#
# THE SIGNATURE CHECK IS NARROWER THAN IT LOOKS and the message says so: Test
# computes the signature it reports from a SECOND clock reading, after the send,
# and TestResult carries no timestamp field at all -- so the exact second it was
# signed over is not recoverable by the operator or by this check. A one-second
# window is allowed. What survives is the part that matters: wrong body, wrong
# key or no signature all still fail.
#
# Proven able to fail against the committed tree by changing Test's
# `Body: string(body)` to `Body: "{}"` (all three conditions go false), and the
# signature condition alone by the payload.go separator mutation in step 2.
if is "$B" testBodyMatchesWire true && is "$B" testMarkedTest true \
   && is "$B" testSigOverReportedBody true; then
  ok "the reported body is byte-identical to the wire and marked test; the reported signature is an HMAC over it (within a 1s window -- no timestamp is returned)"
else
  bad "what the test button reports is not what was sent: body=$(val "$B" testBodyMatchesWire) markedTest=$(val "$B" testMarkedTest) signature=$(val "$B" testSigOverReportedBody)"
fi

if is "$B" testSigMatchesWire false; then
  note "the reported signature differed from the one sent -- the second-boundary"
  note "drift described above, not a regression. TestResult has no timestamp field."
fi

# handleTestHook puts an error out of the dispatcher straight into a 502 body,
# and its comment used to claim those errors were already redacted. This is the
# check that keeps that claim true.
#
# Proven able to fail against the committed tree by changing Test's
# `err = errors.New(alerts.Redact(secrets.Scrub(alerts.ClientErrorText(err))))`
# to `err = errors.New(err.Error())`: hasSecret goes true. Its own control --
# testDeadRawHasSecret -- proven by the rawErrorCarries mutation in step 6.
if is "$B" testDeadRawHasSecret true && is "$B" testDeadFailed true \
   && is "$B" testDeadErrHasSecret false && is "$B" testDeadErrNamesHost true; then
  ok "a failed test's error -- which the API returns in a 502 body -- names the host and not the path"
else
  bad "the test button's error path is wrong: control=$(val "$B" testDeadRawHasSecret) failed=$(val "$B" testDeadFailed) hasSecret=$(val "$B" testDeadErrHasSecret) namesHost=$(val "$B" testDeadErrNamesHost)"
fi

# ---------------------------------------------------------------------------
step "9. A real remote endpoint"

# THE ONLY STEP THAT LEAVES THIS MACHINE, and it skips rather than fails without
# an endpoint -- the eight steps above are the floor and they run everywhere.
#
# What it adds over the loopback tier is small but is not nothing: DNS, TLS, a
# third party's HTTP stack, and a URL whose path, query and token layout we did
# not choose. The loopback steps all use /webhook/<hex>, which is one shape.
if [ -z "${POLY_HOOKS_URL:-}" ]; then
  sk "no POLY_HOOKS_URL; nothing outside this machine was contacted"
  note "set POLY_HOOKS_URL to a webhook endpoint you control to run it"
  sk "the remote endpoint's reply was not swept for its own URL"
#
# Proven able to fail against the committed tree by pointing POLY_HOOKS_URL at a
# local listener answering 500, which is how both branches of this step and the
# one below were exercised at all -- see the PR body. A suite whose only
# credentialed step has never been RUN is a suite with an untested branch in it.
else
  M="$(drive remote)"
  if ! is "$M" valid true; then
    bad "POLY_HOOKS_URL is not a deliverable hook URL"
    note "it must be http or https with a host; nothing about it is printed here"
  else
    case "$(val "$M" status)" in
      2??) ok "the remote endpoint accepted a signed delivery in $(val "$M" durationMs)ms" ;;
      *)   bad "the remote endpoint answered HTTP $(val "$M" status) after $(val "$M" durationMs)ms"
           note "the URL and the reply are deliberately not printed; check your endpoint" ;;
    esac
  fi

  # Its OWN path, run through the same question steps 6 and 7 ask of ours. A
  # provider that puts its token in a query parameter rather than a path segment
  # is a shape this suite cannot stage for itself.
  #
  # Proven able to fail against the committed tree by changing Test's
  # `Response: secrets.Scrub(snippet)` to `Response: snippet`, against a local
  # listener echoing its own request path.
  if is "$M" discloses false; then
    ok "nothing the remote said or failed with quoted its own URL back"
  else
    bad "the remote endpoint's URL was disclosed in what would be stored or served"
  fi
fi

# ---------------------------------------------------------------------------
printf "\n"
printf "  \033[1m%d passed, %d failed, %d skipped\033[0m\n\n" "$pass" "$fail" "$skip"

# The floor, so a run that dies halfway cannot report a green tally over four
# checks. FIXED, not a range: every branch contributes the same number either
# way -- step 9 counts two as a skip, two as a pass and two as a fail, and the
# note about signature drift in step 8 is a note rather than a check precisely
# so it cannot move this number.
EXPECTED_CHECKS=31
total=$((pass + fail + skip))
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
  printf "  \033[31mINCOMPLETE\033[0m  only %d of %d checks ran; the run stopped early\n\n" \
    "$total" "$EXPECTED_CHECKS"
  exit 1
fi
if [ "$total" -gt "$EXPECTED_CHECKS" ]; then
  printf "  \033[33mNOTE\033[0m  %d checks ran, %d expected. If checks were added,\n" \
    "$total" "$EXPECTED_CHECKS"
  printf "        raise EXPECTED_CHECKS so the guard keeps its teeth.\n"
fi
[ "$fail" -eq 0 ]
