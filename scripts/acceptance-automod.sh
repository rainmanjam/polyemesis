#!/usr/bin/env bash
# Automod, against a real endpoint.
#
# internal/automod is 2,454 lines whose whole external surface is one HTTP POST,
# and the three properties model.go puts above the classification itself are all
# properties of what happens when the far end is having a bad day:
#
#   FAIL OPEN. A timeout, a 500, a rate-limit or an expired key means the
#   message passes and is flagged for a human. [...] A moderation outage must
#   not silence a chat.
#
# Those are claims about somebody else's server. A stub returns the failure the
# test author imagined; only a real endpoint returns the failure the endpoint
# produces -- a 401 in their wording, a handshake that stalls, a port a real
# kernel really refuses.
#
# WHAT THE FAR END IS, AND WHY IT IS NOT A HARDCODED HOST. Unlike chat, automod
# has no compiled-in endpoint: it is a settings field, and the deployment
# model.go names first is a LOCAL one, Ollama or vLLM on 127.0.0.1. So the
# default here is a default rather than a dependency -- api.openai.com, because
# DefaultModelConfig ships model "gpt-4o-mini" and the OpenAI chat-completions
# shape is the wire contract every compatible server implements.
# POLY_AUTOMOD_ENDPOINT points the suite at any other, which is how an operator
# running the local deployment tests theirs.
#
# TWO TIERS, ONE SUITE.
#
#   Steps 1-6 need NO credentials and run anywhere. That is not a consolation
#   prize: the entire fail-open contract, the deadline, the spend ceiling and
#   the credential handling are reachable with no key at all, because what they
#   are about is REFUSAL. An unauthenticated request to a real API is a real
#   refusal, and a real refusal is the input every one of those paths exists to
#   handle.
#
#   Step 7 needs a key and SKIPS without one, in the shape acceptance-chat uses.
#   It is the only step that asks a model to classify anything.
#
# NOTHING REAL IS SENT ANYWHERE. The only two strings this suite POSTs are
# written in the driver, marked synthetic, and never sampled from a message
# anybody sent. A suite that fed real chat to a third party to prove it could
# would be doing the thing it exists to hold to account.
#
#   ./scripts/acceptance-automod.sh
#
# Environment:
#   POLY_AUTOMOD_ENDPOINT  the endpoint under test (default: api.openai.com)
#   POLY_AUTOMOD_API_KEY   enables step 7; OPENAI_API_KEY is also accepted
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
DRIVER="$ROOT/scripts/acceptance_automod_driver.go"

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

# val <output> <key> -> the value, or empty. Anchored so `findings` cannot match
# `engineFindings`, which is the class of loose-grep error that has produced two
# false alarms in this repo already.
val() { printf '%s\n' "$1" | sed -n "s/^$2=//p" | head -1; }

ENDPOINT="${POLY_AUTOMOD_ENDPOINT:-https://api.openai.com/v1/chat/completions}"

printf "\033[1mautomod, against a real endpoint\033[0m\n"
note "endpoint: $ENDPOINT"

# ---------------------------------------------------------------------------
step "1. The endpoint is reachable, over TLS we would accept"

OUT="$(drive reach)"

# A plain-http endpoint is SUPPORTED, not an error: model.go says the local
# deployment is what this feature is built for, and Ollama on 127.0.0.1 has no
# certificate. Three skips rather than one, so the tally does not move with the
# operator's choice of endpoint.
if [ "$(val "$OUT" notTLS)" = true ]; then
  sk "$(val "$OUT" host) is not https; there is no certificate to check"
  sk "the certificate was not checked against the host"
  sk "the certificate expiry was not read"
  note "a local endpoint is the deployment model.go names first, not a mistake"
else
  case "$(val "$OUT" reached)" in
    true) ok "$(val "$OUT" host) answered over TLS $(val "$OUT" tlsVersion) in $(val "$OUT" dialMs)ms" ;;
    *)    bad "could not reach $(val "$OUT" host): $(val "$OUT" error)" ;;
  esac

  # Checked here rather than assumed, because the connector verifies against the
  # system roots at the point of use and a chain that stopped verifying would
  # look exactly like a network fault there.
  #
  # The driver dials WITHOUT verification and verifies afterwards, so this line
  # can fail while the one above passes. Written the other way round it could
  # not: a verifying handshake refuses the connection first, and the check would
  # have reported a fact its own precondition guaranteed.
  if [ "$(val "$OUT" nameOK)" = true ]; then
    ok "the chain verifies to a system root for $(val "$OUT" host) (chain of $(val "$OUT" chainLen))"
  else
    bad "the certificate does not verify for $(val "$OUT" host): $(val "$OUT" certError)"
  fi

  # An expiry is a scheduled outage that nobody schedules. Reported as a warning
  # well before it bites rather than as a failure on the day.
  DAYS="$(val "$OUT" certDaysLeft)"
  case "$DAYS" in
    ''|*[!0-9-]*) bad "no certificate expiry could be read" ;;
    *) if [ "$DAYS" -gt 14 ]; then
         ok "the certificate has $DAYS days left"
       else
         bad "the certificate expires in $DAYS days"
       fi ;;
  esac
fi

# ---------------------------------------------------------------------------
step "2. A real refusal lets the message through"

# THE CONTRACT THIS PACKAGE IS BUILT AROUND, measured against a refusal that was
# not written by us. The unit test writes w.WriteHeader(401) and proves the
# connector handles the 401 we imagined. This proves it handles the one the
# endpoint sends -- status line, headers, body and all -- and the cost of being
# wrong is stated in model.go: a moderation outage that silenced a chat.
OUT="$(drive refusal)"

if [ "$(val "$OUT" answered)" = true ]; then
  # An endpoint with no authentication answered. Legitimate for a local model,
  # and it cannot settle this check either way. Three skips, matching the three
  # below.
  sk "the endpoint answered an unauthenticated request; there was no refusal"
  sk "no refusal, so the fail-open path was not exercised"
  sk "no refusal, so the engine's verdict was not exercised"
  note "point POLY_AUTOMOD_ENDPOINT at an endpoint that requires a key to run these"
else
  # STATUS, NOT MERELY 'IT FAILED'. A findings count of zero is also what a
  # request that never left the machine produces, and reporting a connection
  # failure as a proven refusal is the exact mistake the multitrack probe made.
  # A status line means the far end was reached and answered.
  STATUS="$(val "$OUT" status)"
  if [ "${STATUS:-0}" -ge 400 ] 2>/dev/null; then
    ok "the endpoint refused an unauthenticated call with $STATUS, in $(val "$OUT" elapsedMs)ms"
  else
    bad "no HTTP status came back, so nothing was refused: $(val "$OUT" error)"
    note "this is a transport failure, not a refusal; the checks below prove nothing"
  fi

  if [ "$(val "$OUT" findings)" = 0 ]; then
    ok "the refusal produced no findings, so the message passes"
  else
    bad "a refused call produced $(val "$OUT" findings) findings; a failure would ACT"
  fi

  # Through the Engine as well, because that is what internal/chat holds. Both
  # halves: an empty verdict alone is also what a connector returning (nil, nil)
  # would produce, and that one leaves the operator believing the model looked
  # and was fine with it.
  if [ "$(val "$OUT" engineErr)" = true ] \
     && [ "$(val "$OUT" engineActs)" = 0 ] \
     && [ "$(val "$OUT" engineFindings)" = 0 ]; then
    ok "the engine returns an empty verdict AND tells the caller it failed"
  else
    bad "engine err=$(val "$OUT" engineErr) acts=$(val "$OUT" engineActs) findings=$(val "$OUT" engineFindings)"
  fi
fi

# ---------------------------------------------------------------------------
step "3. The configured timeout is enforced on a real network"

# INVISIBLE OFFLINE. A stub on loopback answers in well under a millisecond, so
# a timeout that was stored but never applied to the transport looks exactly
# like one that was. Across the internet it does not.
OUT="$(drive deadline)"

if [ "$(val "$OUT" beatIt)" = true ]; then
  sk "the endpoint answered inside a 1ms deadline; nothing was timed out"
  sk "the deadline's promptness was not measured"
  note "only a loopback endpoint can do this; the check needs a remote one"
else
  if [ "$(val "$OUT" wasDeadline)" = true ]; then
    ok "the call gave up on our deadline rather than on the endpoint's"
  else
    bad "the call failed for another reason: $(val "$OUT" error)"
  fi

  # The deadline has to be the thing that ended it. A 4-second default applied
  # instead of the 1ms we asked for would still report a timeout.
  MS="$(val "$OUT" elapsedMs)"
  case "$MS" in
    ''|*[!0-9]*) bad "no elapsed time was reported" ;;
    *) if [ "$MS" -lt 2000 ]; then
         ok "it gave up in ${MS}ms, so the configured deadline is the one in force"
       else
         bad "it took ${MS}ms to honour a 1ms deadline"
       fi ;;
  esac
fi

# ---------------------------------------------------------------------------
step "4. An unreachable endpoint does not leak its own credential"

# WHY THE ENDPOINT IS A CREDENTIAL. internal/api/redact.go masks
# automod.model.endpoint out of GET /settings and says why: a self-hosted or
# proxied inference endpoint most often arrives as
# https://host/v1/chat/completions?api_key=sk-..., and a key in a query string is
# still a key. That reasoning was applied to the settings blob and stopped
# there. net/http puts the request URL verbatim into *url.Error, and
# internal/chat writes that error to server.log once per message for as long as
# the endpoint is down -- there is no backoff, because failing open means trying
# again on the next message. #310 was this exact shape.
#
# THIS CHECK FOUND THAT. On the committed tree before this suite, the sentinel
# was in the error, in ModelStats.LastError and in the log line.
OUT="$(drive leak)"

if [ "$(val "$OUT" refused)" = true ] && [ "$(val "$OUT" sentinelConfigured)" = true ]; then
  ok "a real host on a closed port refused, with a sentinel key in the query string"

  [ "$(val "$OUT" sentinelInError)" = false ] \
    && ok "the key is not in the error internal/chat logs" \
    || bad "the key is in the error internal/chat writes to server.log"

  [ "$(val "$OUT" sentinelInStats)" = false ] \
    && ok "the key is not in ModelStats.LastError, which the spend panel shows" \
    || bad "the key is in ModelStats.LastError"

  [ "$(val "$OUT" sentinelInLog)" = false ] \
    && ok "the key survives no slog rendering of that error either" \
    || bad "the key appears once slog renders the error"

  # The masking must not have simply deleted the endpoint. An operator whose
  # moderation stopped needs to know WHICH endpoint stopped answering, and an
  # error reduced to nothing would pass all three checks above while trading one
  # silent failure for another.
  [ "$(val "$OUT" hostNamed)" = true ] \
    && ok "and the error still names the host, so the operator can act on it" \
    || bad "the error no longer names the endpoint that failed"
else
  # The gate failed, so nothing below it can be concluded. Reported as a
  # failure rather than a skip: the four checks it guards are the reason this
  # step exists.
  bad "nothing refused the connection, so no error was produced to inspect"
  sk "the error was not searched for the key"
  sk "ModelStats.LastError was not searched for the key"
  sk "no slog rendering was searched for the key"
  sk "the error's usefulness to an operator was not checked"
fi

# ---------------------------------------------------------------------------
step "5. The spend ceiling stops a call before it leaves"

# AN UNBOUNDED PER-MESSAGE API CALL IS A SURPRISE INVOICE, which is model.go's
# own phrasing. The unit test counts handler invocations against a stub; here
# the observable is stronger, because a ceiling applied AFTER the request was
# sent would look identical on a handler count and identical on the invoice.
OUT="$(drive ceiling)"

if [ "$(val "$OUT" firstLeft)" = true ]; then
  ok "the first call reached the far end in $(val "$OUT" firstMs)ms"
else
  bad "the first call never reached the endpoint, so there was no budget to spend"
fi

# Refused, and refused without a network round trip. Both, because "refused" on
# its own is also what an endpoint that had gone away would produce.
SECOND_MS="$(val "$OUT" secondMs)"
if [ "$(val "$OUT" secondBlocked)" = true ] \
   && [ "$(val "$OUT" callsThisHour)" = 1 ] \
   && [ "${SECOND_MS:-9999}" -lt 20 ] 2>/dev/null; then
  ok "the second was refused by the ceiling in ${SECOND_MS}ms, without a request"
else
  bad "second blocked=$(val "$OUT" secondBlocked) calls=$(val "$OUT" callsThisHour) took=${SECOND_MS}ms"
fi

# ---------------------------------------------------------------------------
step "6. When the model cannot be reached, nobody gets moderated"

# THE COMPOSITION, not the connector. Steps 2 and 3 say CheckModel returns no
# findings; this says no message is deleted and no author is banned -- which
# involves the Hub, the worker, the generation counter and the matrix as well,
# and is the sentence an operator would actually check.
#
# Every layer here is production code except the adapter, which has to be ours
# because the alternative is deleting a stranger's message to find out.
#
# THE MATRIX IS DELIBERATELY PERMISSIVE. Against the shipped flag-only default,
# "nothing was deleted" would be true whatever the model returned, because the
# matrix would have blocked the delete regardless. Here the only thing between a
# verdict and a deletion is the verdict.
OUT="$(drive hub)"

if [ "$(val "$OUT" modelAsked)" = true ] && [ "$(val "$OUT" wouldPermitDelete)" = true ]; then
  ok "the message reached the model checker, on a matrix that permits deletion"

  if [ "$(val "$OUT" deletes)" = 0 ] && [ "$(val "$OUT" bans)" = 0 ]; then
    ok "the model could not be reached and nobody was deleted or banned"
  else
    bad "a failed model check still acted: $(val "$OUT" deletes) deletes, $(val "$OUT" bans) bans"
  fi

  [ "$(val "$OUT" historyHas)" = true ] \
    && ok "and the message is still in the Hub's history" \
    || bad "the message disappeared from history"

  [ "$(val "$OUT" sentinelInLog)" = false ] \
    && ok "nothing the Hub logged carried the endpoint's key" \
    || bad "the endpoint's key reached the Hub's log"
else
  # Without both of these the three checks below are vacuous: a Hub that never
  # received the message reports zero deletions too.
  bad "the model was never asked (asked=$(val "$OUT" modelAsked)) or the matrix would have blocked the action anyway (permits=$(val "$OUT" wouldPermitDelete))"
  sk "no moderation actions were counted"
  sk "the Hub's history was not checked"
  sk "the Hub's log was not searched for the key"
fi

# ---------------------------------------------------------------------------
step "7. A real model classifies, in both directions"

# THE ONLY STEP THAT NEEDS A CREDENTIAL, and it skips rather than fails without
# one -- the six steps above are the suite's floor and they run everywhere.
#
# BOTH DIRECTIONS OR NEITHER. A connector that produced a finding for everything
# would pass the abusive case alone, and one that produced nothing ever -- which
# is precisely what a fail-open outage looks like -- would pass the benign case
# alone. Only the pair says the classification round trip works.
if [ -z "${POLY_AUTOMOD_API_KEY:-}${OPENAI_API_KEY:-}" ]; then
  sk "no POLY_AUTOMOD_API_KEY; no message was classified"
  sk "the abusive/benign pair was not exercised"
  note "set POLY_AUTOMOD_API_KEY (or OPENAI_API_KEY) to run it"
else
  OUT="$(drive classify)"

  if [ "$(val "$OUT" bothCallsSucceeded)" = true ]; then
    ok "both synthetic messages completed a round trip to the real model"
  else
    bad "a call failed: abuse=$(val "$OUT" abuseError) benign=$(val "$OUT" benignError)"
  fi

  ABUSE="$(val "$OUT" abuseFindings)"; BENIGN="$(val "$OUT" benignFindings)"
  if [ "${ABUSE:-0}" -gt 0 ] && [ "${BENIGN:-1}" -eq 0 ]; then
    ok "synthetic abuse was flagged at $(val "$OUT" abuseConfidence), synthetic banter was not"
  else
    bad "abuse findings=$ABUSE, banter findings=$BENIGN; the pair did not separate"
    note "both zero is what a fail-open outage looks like; both non-zero is a filter"
    note "that would flag the whole channel"
  fi
fi

# ---------------------------------------------------------------------------
# HOW EVERY CHECK ABOVE WAS PROVEN ABLE TO FAIL.
#
# A green suite is worth nothing until each line in it has been watched going
# red for the reason it names. Every one below was run against the committed
# tree with the stated change in place, and only the named check moved. Two of
# them changed the suite's own design when they would not move: see reach()'s
# comment about the verifying dial, which could not fail, and step 4's gate,
# which was added after a broken driver build reported four silent passes.
#
#   1a reachable          POLY_AUTOMOD_ENDPOINT=https://api.openai.com:81/...
#   1b chain verifies     POLY_AUTOMOD_ENDPOINT=https://expired.badssl.com/...
#   1c days left          the same; it reported -4140
#   2a refused with 401   POLY_AUTOMOD_ENDPOINT at a closed port; status=0
#   2b no findings        model.go Check: return a Finding alongside the error
#   2c engine says failed engine.go CheckModel: `return Verdict{}, nil`
#   3a our deadline       model.go: http.Client{} and a 30s ctx in ask()
#   3b promptly           the same, pinned at 3s, against the closed port
#   4a the gate           driver leak(): set api_key to something else
#   4b key not in error   model.go redactEndpoint: `return err`
#   4c key not in stats   the same change
#   4d key not in a log   the same change
#   4e host still named   redactEndpoint: RedactURL("") instead of the URL
#   5a first call left    POLY_AUTOMOD_ENDPOINT at a closed port
#   5b ceiling blocks     model.go reserve(): never return false
#   6a the gate           chat/automod.go checkAutomod: never queue the model
#   6b nobody moderated   chat/automod.go askModel: queue a delete on error
#   6c still in history   the same change; the delete removed it
#   6d no key in the log  model.go redactEndpoint: `return err`
#   7a both calls made    POLY_AUTOMOD_API_KEY=<not a real key>; both 401
#   7b the pair separates the same; both came back empty
#
# 7a and 7b have been proven able to FAIL and never proven able to PASS: no key
# was available when this was written. They are the honest state of the
# credentialed tier, not a claim about it.
# ---------------------------------------------------------------------------
printf "\n"
printf "  \033[1m%d passed, %d failed, %d skipped\033[0m\n\n" "$pass" "$fail" "$skip"

# The floor, so a run that dies halfway cannot report a green tally over four
# checks. FIXED, not a range: every branch in this suite contributes the same
# number either way -- step 1 counts three whether the endpoint is https or a
# local http one, step 2 counts three whether the endpoint refuses or accepts an
# unauthenticated call, step 4 and step 6 count their gate plus its dependants
# either way, and step 7 counts two as a skip or as a pass. A floor that moved
# with the operator's endpoint would be no floor at all.
EXPECTED_CHECKS=21
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
