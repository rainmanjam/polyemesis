#!/usr/bin/env bash
# Chat, against the real platforms.
#
# internal/chat's own doc comment states the gap this suite fills:
#
#   the IRC transport cannot be exercised against Twitch offline, and this
#   package does not pretend otherwise -- there is no test here that proves
#   polyemesis can talk to irc.chat.twitch.tv. [...] The parts that need a
#   socket to a real platform are verified by connecting to one.
#
# That last sentence described a practice nothing performed: 10,063 lines of
# adapter, 4,494 lines of tests, and no test that opened a socket. Of the
# seventeen acceptance suites in this repo, exactly one -- acceptance-multistream
# -- talked to anything outside the machine, and on its first live runs it found
# a credential leak and a shipped-and-wrong default that unit tests could not
# reach.
#
# TWO TIERS, ONE SUITE.
#
#   Everything in steps 1-6 needs NO credentials and runs anywhere, every time.
#   That is not a consolation prize: Twitch accepts any nick matching justinfan*
#   as an anonymous reader, so the whole chat READ path -- CAP handshake, JOIN,
#   the IRCv3 line parser, tag parsing, message normalisation -- runs against
#   live traffic from a real channel with nothing secret involved.
#
#   Step 7 needs a token and SKIPS without one, in the shape acceptance-
#   multistream uses for dry versus live. It holds an authenticated session open
#   long enough to exercise the keepalive, which a handshake cannot reach.
#
# NOTHING HERE SENDS A MESSAGE. Every check reads. A suite that posted to a real
# channel to prove it could would be writing to somebody's stream, and a suite
# that did it on every CI run would be doing so repeatedly.
#
#   ./scripts/acceptance-chat.sh
#
# Environment:
#   TWITCH_CHAT_TOKEN     enables step 7; without it the step skips
#   TWITCH_CHAT_NICK      the account the token belongs to
#   TWITCH_CHAT_CHANNEL   channel to join for step 7
#   POLY_CHAT_ANON_CHANNEL  channel for the anonymous read (default: xqc)
#   POLY_CHAT_ANON_HOLD     how long to listen (default: 25s)
#   POLY_CHAT_HOLD          how long step 7 holds the session (default: 30s)
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
DRIVER="$ROOT/scripts/acceptance_chat_driver.go"

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

# val <output> <key> -> the value, or empty. Anchored so `state` cannot match
# `firstState`, which is the class of loose-grep error that has produced two
# false alarms in this repo already.
val() { printf '%s\n' "$1" | sed -n "s/^$2=//p" | head -1; }

printf "\033[1mchat, against the real platforms\033[0m\n"

# ---------------------------------------------------------------------------
step "1. Twitch is reachable, over TLS we would accept"

OUT="$(drive twitch-reach)"
case "$(val "$OUT" reached)" in
  true) ok "irc.chat.twitch.tv:6697 answered in $(val "$OUT" dialMs)ms" ;;
  *)    bad "could not reach Twitch chat: $(val "$OUT" error)" ;;
esac

# The certificate is checked here rather than assumed, because the adapter
# verifies against the system roots and a chain that stopped verifying would
# look exactly like a network fault at the point of use.
CN="$(val "$OUT" certCN)"
if [ "$CN" = "irc.chat.twitch.tv" ]; then
  ok "the certificate is Twitch's own (CN=$CN, chain of $(val "$OUT" chainLen))"
else
  bad "certificate CN is '$CN', expected irc.chat.twitch.tv"
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

# ---------------------------------------------------------------------------
step "2. An anonymous reader joins a real channel"

ANON_CH="${POLY_CHAT_ANON_CHANNEL:-xqc}"
OUT="$(POLY_CHAT_ANON_CHANNEL="$ANON_CH" drive twitch-anon)"

if [ "$(val "$OUT" joined)" = true ]; then
  ok "joined #$(val "$OUT" channel) anonymously, no credentials involved"
else
  bad "never joined #$(val "$OUT" channel); state never reached live"
fi

# NOT AN ASSERTION ON THE COUNT. Whether a channel is talking during a 25-second
# window is not something this suite controls, and a check that required a
# message would fail on a quiet stream and teach everyone to ignore it. A zero
# is reported as a zero.
MSGS="$(val "$OUT" messages)"
if [ "${MSGS:-0}" -gt 0 ]; then
  ok "$MSGS live messages arrived through the ordinary adapter"

  # The count alone would pass for a sink that incremented on any line. These
  # three fields mean the IRCv3 tag parser and the normaliser both ran on real
  # bytes: an author, a body, and an id that only exists as a tag.
  ALEN="$(val "$OUT" firstAuthorLen)"; TLEN="$(val "$OUT" firstTextLen)"
  if [ "$(val "$OUT" firstHasID)" = true ] \
     && [ "${ALEN:-0}" -gt 0 ] && [ "${TLEN:-0}" -gt 0 ]; then
    ok "the first message parsed: author, text, and a tag-derived id"
  else
    bad "a message arrived but did not parse: author=${ALEN:-?} text=${TLEN:-?} id=$(val "$OUT" firstHasID)"
  fi
else
  # TWO SKIPS, not one, so the tally is the same whether the channel was
  # talking or not. A count that moves with the weather cannot be a floor.
  sk "no messages during the window; #$ANON_CH was quiet or offline"
  sk "the parser was not exercised on live bytes"
  note "set POLY_CHAT_ANON_CHANNEL to a live channel to exercise the parser"
fi

if [ "$(val "$OUT" fatal)" = true ]; then
  bad "the anonymous session was refused: $(val "$OUT" error)"
else
  ok "the anonymous session was not refused"
fi

# ---------------------------------------------------------------------------
step "3. A rejected login is understood as fatal"

# THE CHECK THIS SUITE WAS ORIGINALLY BUILT FOR. fatalNotice() classifies a
# login failure by matching Twitch's own wording. A unit test feeding it a
# hand-written NOTICE proves the matcher works on the string we imagined; only
# this proves it works on the string Twitch sends. If Twitch reworded it, the
# notice would be classified retryable and polyemesis would retry a rejected
# password every thirty seconds -- which twitch.go's own comment says is how an
# IP gets banned.
OUT="$(drive twitch-refusal)"

if [ "$(val "$OUT" timedOut)" = true ]; then
  bad "no verdict in 45s; Twitch never refused and never accepted"
  note "written with a justinfan nick this check did exactly that: Twitch"
  note "treats those as anonymous and ignores the password entirely."
elif [ "$(val "$OUT" fatal)" = true ]; then
  ok "a bad token is refused and classified fatal, in $(val "$OUT" elapsedMs)ms"
else
  bad "the refusal was not classified fatal; it would be retried"
  note "state=$(val "$OUT" state) returned=$(val "$OUT" returned)"
fi

if [ "$(val "$OUT" state)" = failed ]; then
  ok "the adapter reports failed, which is what the UI shows the operator"
else
  bad "adapter state is '$(val "$OUT" state)', expected failed"
fi

# ---------------------------------------------------------------------------
step "4. Kick's webhook public key is fetchable and well formed"

# FETCHED AT RUNTIME BY DESIGN. kick_verify.go records that a compiled-in key
# could not be repaired short of a new binary if Kick rotated it. The cost of
# that choice is a live dependency on one URL, and nothing else in the repo
# would notice it going away.
OUT="$(drive kick-key)"

if [ "$(val "$OUT" fetched)" = true ]; then
  ok "fetched from $(val "$OUT" url) in $(val "$OUT" fetchMs)ms"
else
  bad "could not fetch Kick's public key: $(val "$OUT" error)"
fi

BITS="$(val "$OUT" bits)"
case "$BITS" in
  ''|*[!0-9]*) bad "the key did not parse as RSA" ;;
  *) if [ "$BITS" -ge 2048 ]; then
       ok "it parses as a $BITS-bit RSA key"
     else
       bad "the key is only $BITS bits"
     fi ;;
esac

# ---------------------------------------------------------------------------
step "5. Signature verification actually verifies"

# BOTH DIRECTIONS, because neither alone says anything. A verifier that accepted
# everything would pass a positive-only test and a verifier that rejected
# everything would pass a negative-only one.
#
# We cannot mint a valid Kick signature -- that needs their private key -- so
# the positive case uses a keypair generated here, and the decisive negative
# case is that same signature presented against Kick's real fetched key.
OUT="$(drive kick-verify)"

if [ "$(val "$OUT" fetched)" != true ]; then
  bad "could not fetch the key to verify against: $(val "$OUT" error)"
else
  [ "$(val "$OUT" ourSigOurKey)" = true ] \
    && ok "a genuine signature is accepted" \
    || bad "a genuine signature was REJECTED; real Kick traffic would be dropped"

  [ "$(val "$OUT" ourSigKickKey)" = false ] \
    && ok "a signature from the wrong key is rejected, against Kick's real key" \
    || bad "a foreign signature was accepted; verification is not happening"

  [ "$(val "$OUT" tamperedBody)" = false ] \
    && ok "a tampered body is rejected, so the body is inside the signed material" \
    || bad "the body can be changed without invalidating the signature"

  [ "$(val "$OUT" garbageSig)" = false ] \
    && ok "a malformed signature header is rejected without panicking" \
    || bad "garbage in the signature header was accepted"
fi

# ---------------------------------------------------------------------------
step "6. An authenticated session, held"

# THE ONLY STEP THAT NEEDS A CREDENTIAL, and it skips rather than fails without
# one -- the six steps above are the suite's floor and they run everywhere.
#
# It is held for 30 seconds rather than connected and dropped because a broken
# keepalive looks perfect for the first few seconds. Twitch PINGs and expects a
# PONG; a session that ends before the first PING has tested nothing about
# staying connected.
if [ -z "${TWITCH_CHAT_TOKEN:-}" ]; then
  sk "no TWITCH_CHAT_TOKEN; the authenticated session was not attempted"
  note "set TWITCH_CHAT_TOKEN, TWITCH_CHAT_NICK and TWITCH_CHAT_CHANNEL to run it"
  sk "the held session's ending was not observed"
else
  OUT="$(drive twitch-live)"
  if [ "$(val "$OUT" fatal)" = true ]; then
    bad "the authenticated session was refused: $(val "$OUT" error)"
    note "if the token expired, reconnect the account and run again"
  elif [ "$(val "$OUT" state)" = live ] || [ "$(val "$OUT" messages)" -gt 0 ] 2>/dev/null; then
    ok "authenticated, joined and held for $(val "$OUT" heldMs)ms"
  else
    bad "connected but never reached live; state=$(val "$OUT" state)"
  fi

  # THE SHAPE OF SUCCESS IS OUR DEADLINE ENDING IT. A session that ended any
  # other way was dropped, and a dropped session that produced a message or two
  # first would otherwise read as a pass.
  if [ "$(val "$OUT" endedOnOurDeadline)" = true ]; then
    ok "the session ended on our deadline, not on a disconnect"
  else
    bad "the session ended early: $(val "$OUT" error)"
  fi
fi

# ---------------------------------------------------------------------------
printf "\n"
printf "  \033[1m%d passed, %d failed, %d skipped\033[0m\n\n" "$pass" "$fail" "$skip"

# The floor, so a run that dies halfway cannot report a green tally over three
# checks. FIXED, not a range: every branch in this suite contributes the same
# number either way -- step 6 counts two as a skip or as a pass, and step 2's
# message pair counts two whether the channel was talking or silent. A floor
# that moved with a stranger's stream would be no floor at all.
EXPECTED_CHECKS=17
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
