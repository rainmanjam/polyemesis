#!/usr/bin/env bash
# OAuth, against the real authorization servers.
#
# internal/oauth is the largest external surface in this repository: 10,693
# lines, nineteen hosts, and until this suite not one test that opened a socket
# to any of them. Of the seventeen acceptance suites here before this one,
# exactly two talked to anything outside the machine -- and on their first live
# runs those two found a credential leak (#310) and a shipped-and-wrong default
# that could not publish (#312).
#
# TWO TIERS, ONE SUITE.
#
#   Steps 1-7 need NO credentials and run anywhere, every time. That is not a
#   consolation prize, it is where most of the value is: every provider's OAuth
#   surface is public. The discovery documents, the authorization and token
#   endpoints, the advertised grant types and PKCE methods, and the Graph API
#   version Facebook actually serves can all be fetched by anybody and compared
#   against what internal/oauth hardcodes. An endpoint that moved, a grant that
#   stopped being advertised, or a pinned API version that was quietly retired
#   are silent breaks that nothing in this repository currently detects, and
#   none of them needs a token.
#
#   Step 8 needs a credential and SKIPS without one, in the shape
#   acceptance-chat.sh uses for dry versus live. It is the only step that can
#   say anything about whether a refresh actually SUCCEEDS, which is the
#   hour-four failure this package's whole risk profile turns on.
#
# WHAT A REFUSAL PROVES, AND WHAT IT DOES NOT. Most of the credential-free tier
# consists of asking a platform something it must say no to. A 401 from a token
# endpoint proves the endpoint exists and is asking for credentials. It does
# NOT prove the grant works. Every check below says which of the two it is in
# its own message, because a suite that blurred them would be reporting a green
# tally for a package whose refresh path had stopped working.
#
# NOTHING HERE MUTATES ANY ACCOUNT. Every credential-free check is a GET or a
# deliberately-doomed token request. The credentialed step refreshes a token,
# reads an identity and reads an ingest endpoint; it starts no broadcast and
# writes no metadata.
#
# EVERY CHECK BELOW WAS PROVEN ABLE TO FAIL against the committed tree, and the
# exact change that did it is recorded beside each step. One of them earned its
# keep immediately: step 2's Twitch PKCE check is an assertion about an ABSENCE,
# and the first version of it reported "Twitch still documents no PKCE support"
# while the discovery document was a 404 -- a green check standing on having
# read nothing. It now requires the document to have parsed. That is the failure
# mode this suite is most exposed to, because a check on something a platform
# does NOT say is satisfied by a platform that says nothing.
#
#   ./scripts/acceptance-oauth.sh
#
# Environment (step 8 only; every variable is read from the environment and
# never from argv, which is world-readable in ps):
#   POLY_OAUTH_<PLATFORM>_CLIENT_ID
#   POLY_OAUTH_<PLATFORM>_CLIENT_SECRET
#   POLY_OAUTH_<PLATFORM>_REFRESH_TOKEN
# for PLATFORM in YOUTUBE, TWITCH, FACEBOOK, KICK. A platform missing any of
# the three skips; a platform with all three runs.
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
DRIVER="$ROOT/scripts/acceptance_oauth_driver.go"

cd "$ROOT" || exit 1

pass=0; fail=0; skip=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
sk()   { printf "  \033[33mSKIP\033[0m  %s\n" "$1"; skip=$((skip+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }
note() { printf "        %s\n" "$1"; }

# drive runs one driver command and captures its key=value lines.
drive() { go run "$DRIVER" "$@" 2>&1; }

# val <output> <key> -> the value, or empty. Anchored so `status` cannot match
# `realStatus`, which is the class of loose-grep error that has produced two
# false alarms in this repo already.
val() { printf '%s\n' "$1" | sed -n "s/^$2=//p" | head -1; }

# has <space-delimited-list> <item> -> true when the list contains item as a
# whole word. Written with case rather than grep because a scope name like
# `chat:read` is a regex nobody wants to think about, and because a substring
# match would report `refresh_token_v2` as `refresh_token`.
has() {
  case " $1 " in
    *" $2 "*) return 0 ;;
    *)        return 1 ;;
  esac
}

printf "\033[1moauth, against the real authorization servers\033[0m\n"

# ---------------------------------------------------------------------------
step "1. The published discovery documents still point where internal/oauth points"

# THE COMPARISON IS AGAINST WHAT THE PACKAGE BUILDS, not against a second copy
# of the same URL written into this file. The driver reads Provider.AuthURL --
# the method production calls -- so ytConsentBase and twitchIDBase are on trial
# here. A suite that retyped the endpoints would keep passing after one of them
# was changed, which is the whole class of bug this exists to catch.
#
# Proven able to fail against the committed tree by pointing ytConsentBase at
# accounts.moved.example.com (one failure, the YouTube consent comparison), by
# spelling twitch.go's authorize path "/oauth2/authorise" (one failure, the
# Twitch consent comparison), and by pointing each discovery URL at a path that
# 404s -- which failed all seven Google-derived checks and all four
# Twitch-derived ones, none of them passing on a document that was never read.
GDISC="$(drive discovery google)"
YAUTH="$(drive authurl youtube)"

if [ "$(val "$GDISC" published)" = true ] && [ "$(val "$GDISC" issuer)" = "https://accounts.google.com" ]; then
  ok "Google publishes its discovery document (issuer $(val "$GDISC" issuer))"
else
  bad "Google's discovery document did not fetch or parse: status=$(val "$GDISC" status) $(val "$GDISC" error)"
fi

GA="$(val "$GDISC" authorizationEndpoint)"; YA="$(val "$YAUTH" endpoint)"
if [ -n "$GA" ] && [ "$GA" = "$YA" ]; then
  ok "YouTube's consent URL is where Google says it is ($YA)"
else
  bad "polyemesis sends YouTube consent to '$YA'; Google's document says '$GA'"
fi

# postForm puts client_secret in the request body, so client_secret_post is the
# authentication method polyemesis relies on. A server that stopped accepting
# it would refuse every exchange and every refresh at once.
if has "$(val "$GDISC" tokenAuthMethods)" client_secret_post; then
  ok "Google still accepts client_secret_post, which is how postForm authenticates"
else
  bad "Google no longer advertises client_secret_post: '$(val "$GDISC" tokenAuthMethods)'"
fi

TDISC="$(drive discovery twitch)"
TAUTH="$(drive authurl twitch)"

if [ "$(val "$TDISC" published)" = true ] && [ "$(val "$TDISC" issuer)" = "https://id.twitch.tv/oauth2" ]; then
  ok "Twitch publishes its discovery document (issuer $(val "$TDISC" issuer))"
else
  bad "Twitch's discovery document did not fetch or parse: status=$(val "$TDISC" status) $(val "$TDISC" error)"
fi

TA="$(val "$TDISC" authorizationEndpoint)"; TB="$(val "$TAUTH" endpoint)"
if [ -n "$TA" ] && [ "$TA" = "$TB" ]; then
  ok "Twitch's consent URL is where Twitch says it is ($TB)"
else
  bad "polyemesis sends Twitch consent to '$TB'; Twitch's document says '$TA'"
fi

if has "$(val "$TDISC" tokenAuthMethods)" client_secret_post; then
  ok "Twitch still accepts client_secret_post, which is how postForm authenticates"
else
  bad "Twitch no longer advertises client_secret_post: '$(val "$TDISC" tokenAuthMethods)'"
fi

# ---------------------------------------------------------------------------
step "2. The grants and PKCE methods polyemesis depends on are still advertised"

# THE HOUR-FOUR CHECK, in its credential-free form. refresh_token disappearing
# from Google's grant list is precisely the break that looks like nothing for
# four hours and then breaks every connected YouTube account at once. Nothing
# else in this repository would notice it before an operator did.
#
# Proven able to fail against the committed tree by renaming the driver's
# grant_types_supported JSON tag, which failed both grant checks and nothing
# else; by returning false from YouTube.PKCE(), which failed the S256
# conjunction alone; and by returning true from Twitch.PKCE(), which failed the
# Twitch premise alone.
if has "$(val "$GDISC" grantTypes)" refresh_token; then
  ok "Google still advertises the refresh_token grant, which every YouTube account depends on"
else
  bad "Google no longer advertises refresh_token: '$(val "$GDISC" grantTypes)'"
  note "every connected YouTube account would stop working when its access token expires"
fi

if has "$(val "$GDISC" grantTypes)" authorization_code; then
  ok "Google still advertises the authorization_code grant, which is how an account connects"
else
  bad "Google no longer advertises authorization_code: '$(val "$GDISC" grantTypes)'"
fi

# THREE FACTS AT ONCE, and they have to agree. youtube.go returns PKCE() true
# and sends code_challenge_method=S256; if Google stopped supporting S256 the
# consent request would be rejected for every user. Checking only the discovery
# document would pass while the provider had quietly stopped sending the
# parameter, and checking only the provider would pass while Google had dropped
# it, so the check is the conjunction.
if has "$(val "$GDISC" codeChallengeMethods)" S256 \
   && [ "$(val "$YAUTH" pkce)" = true ] \
   && [ "$(val "$YAUTH" codeChallengeMethod)" = S256 ]; then
  ok "Google supports S256, YouTube.PKCE() is on, and the consent URL carries S256"
else
  bad "S256 disagreement: google='$(val "$GDISC" codeChallengeMethods)' pkce=$(val "$YAUTH" pkce) sent='$(val "$YAUTH" codeChallengeMethod)'"
fi

# THE INVERSE, AND IT IS A REAL CHECK. twitch.go keeps PKCE off with a comment
# whose entire argument is that Twitch documents no RFC 7636 support, so
# sending a code_challenge on a hunch could lock every user out of sign-in.
# That argument has a premise, and this is the premise: Twitch's own discovery
# document still advertises no code_challenge_methods_supported. The day it
# does, this fails -- and the failure is an instruction to turn PKCE on, not a
# defect.
#
# THE published=true CLAUSE IS LOAD-BEARING AND WAS ADDED AFTER THIS CHECK WAS
# CAUGHT PASSING VACUOUSLY. This is an assertion about an ABSENCE, and an
# absence is trivially satisfied by a document that was never fetched: pointing
# the driver at a 404 made every other Twitch check fail and left this one
# green, reporting "Twitch still documents no PKCE support" on the strength of
# having read nothing at all. An absence is only evidence when something was
# read.
if [ "$(val "$TDISC" published)" = true ] \
   && [ -z "$(val "$TDISC" codeChallengeMethods)" ] && [ "$(val "$TAUTH" pkce)" = false ]; then
  ok "Twitch's document still advertises no PKCE support, so twitch.go keeping it off still matches"
elif [ "$(val "$TDISC" published)" != true ]; then
  bad "Twitch's PKCE premise could not be checked: the discovery document did not parse"
else
  bad "PKCE disagreement: twitch advertises code_challenge_methods='$(val "$TDISC" codeChallengeMethods)' while Twitch.PKCE()=$(val "$TAUTH" pkce)"
  note "if Twitch has documented S256, turn PKCE on in twitch.go; if PKCE() was turned on"
  note "without Twitch documenting support, turn it back off -- that is the sign-in outage"
  note "twitch.go's comment warns about."
fi

# ---------------------------------------------------------------------------
step "3. The two platforms with no usable discovery document, recorded as such"

# A FINDING, NOT A GAP. These two checks assert an absence, and an absence that
# ends is worth a failure: it means a platform started publishing something
# this suite could be comparing against and is not. That is the same reasoning
# the drift guards in internal/oauth already use -- a claim in one place that
# stops matching another should be loud rather than quietly stale.
#
# Proven able to fail against the committed tree by pointing the driver's kick
# and facebook discovery URLs at Google's document -- which made Kick appear to
# publish metadata and Facebook appear to advertise the code response type, one
# failure each and nothing else disturbed.
KDISC="$(drive discovery kick)"
KOIDC="$(drive discovery kick-oidc)"

if [ "$(val "$KDISC" published)" = false ] && [ "$(val "$KOIDC" published)" = false ] \
   && [ "$(val "$KDISC" reached)" = true ] && [ "$(val "$KOIDC" reached)" = true ]; then
  ok "Kick publishes no metadata at either well-known path, so Kick is checked by probe below"
else
  bad "Kick's well-known paths changed: rfc8414=$(val "$KDISC" status) oidc=$(val "$KOIDC" status)"
  note "if Kick now publishes one, compare its endpoints here the way Google's are compared"
fi

FDISC="$(drive discovery facebook)"

# Facebook DOES publish a document, and it describes a different product. It
# lists no token endpoint and no plain `code` response type, because it
# describes Limited Login -- an id_token flow -- and not the versioned Login
# dialog with response_type=code that facebook.go uses. Comparing our endpoints
# against it would be comparing against the wrong thing, so the reason is pinned
# instead. If `code` ever appears there, the document becomes authoritative for
# our flow and this step should become a comparison.
case "$(val "$FDISC" responseTypes)" in
  *,code,*) bad "Facebook's discovery document now lists the code response type; compare endpoints against it" ;;
  *) if [ "$(val "$FDISC" published)" = true ] && [ -z "$(val "$FDISC" tokenEndpoint)" ]; then
       ok "Facebook's document describes Limited Login (no token endpoint, no code response type), not our flow"
     else
       bad "Facebook's document changed shape: published=$(val "$FDISC" published) tokenEndpoint='$(val "$FDISC" tokenEndpoint)'"
     fi ;;
esac

# ---------------------------------------------------------------------------
step "4. Every token endpoint is served, and refuses, through the real Refresh"

# THE CHECK THAT FINDS A MOVED TOKEN ENDPOINT. Each of these calls
# Provider.Refresh -- the production code path, building the production URL --
# against the real authorization server with credentials that cannot work.
#
# A 4xx carrying the platform's own OAuth error vocabulary means the URL
# internal/oauth built is being served by an authorization server that parsed
# our grant and rejected our credentials. A 404 means the path is gone. A
# transport error means the host is gone. Three different repairs, reported as
# three different things.
#
# WHAT THIS DOES NOT PROVE: that a real refresh works. Step 8 is the only thing
# that can say that, and it skips without a credential.
#
# Proven able to fail against the committed tree by appending XX to each token
# path in turn -- ytTokenBase's "/token", twitch.go's tokenEndpoint(), and
# kick.go's Refresh path -- each of which produced exactly the two failures for
# that platform (404, then the missing OAuth vocabulary) and left the other
# three platforms green. Facebook was proven separately by pointing fbGraphBase
# at graph-moved.facebook.com, which produced the transport-error branch.
refusal() {
  local plat="$1" want_not_404="$2" marker="$3" out st
  out="$(drive token-refusal "$plat")"

  if [ "$(val "$out" refused)" != true ]; then
    bad "$plat: the token endpoint MINTED A TOKEN for junk credentials (minted=$(val "$out" mintedAToken))"
    bad "$plat: no refusal to inspect"
    return
  fi
  st="$(val "$out" status)"
  if [ "$(val "$out" transportError)" = true ]; then
    bad "$plat: the authorization server was never reached: $(val "$out" error)"
  elif [ "$want_not_404" = yes ] && [ "$st" = 404 ]; then
    bad "$plat: the token endpoint answered 404; the path polyemesis builds has moved"
  elif [ "${st:-0}" -ge 400 ] && [ "${st:-0}" -lt 500 ]; then
    ok "$plat: the token endpoint answered $st in $(val "$out" elapsedMs)ms -- it exists and is asking for credentials"
  else
    bad "$plat: unexpected token endpoint status '$st': $(val "$out" error)"
  fi

  # The second half, and it is not decoration. A status alone is satisfied by a
  # CDN error page or a login redirect; the platform's own OAuth error
  # vocabulary is what says an authorization server read the request.
  case "$(val "$out" error)" in
    *"$marker"*) ok "$plat: it refused in its own OAuth vocabulary ('$marker'), so an authorization server read the request" ;;
    *)           bad "$plat: the refusal did not contain '$marker': $(val "$out" error)" ;;
  esac
}

refusal youtube yes invalid_client
refusal twitch  yes "invalid client"
refusal kick    yes invalid_grant

# FACEBOOK IS THE EXCEPTION AND THE ASYMMETRY IS DELIBERATE. Graph does not 404
# a wrong path under a valid version: a POST to /v24.0/oauth/access_tokenXX is
# answered 400 with "client_secret should not be passed to
# /oauth/access_tokenXX". So a 4xx alone says less here than it does for the
# other three, and the discriminator has to be the message: "Invalid Client ID"
# is what the real token endpoint says, and it is not what a wrong path says.
refusal facebook no "Invalid Client ID"

# ---------------------------------------------------------------------------
step "5. The refresh grant is recognised as a grant, not merely refused"

# WITHOUT A CONTROL, STEP 4 MEANS LESS THAN IT LOOKS. "The token endpoint
# refused our refresh_token" is satisfied by a server that refuses everything,
# including one that has quietly stopped serving refresh_token at all. It is
# only when a grant type that CANNOT exist is refused DIFFERENTLY that the
# first refusal becomes evidence the grant was recognised, routed, and rejected
# on its merits.
#
# This works on two of the four. Twitch and Facebook validate the client before
# they look at the grant, so both questions come back identically and the
# differential is unreachable without a credential. They skip by name rather
# than reporting a comparison this suite did not make.
#
# Proven able to fail against the committed tree by having the driver send
# refresh_token as BOTH questions, which collapsed the differential and failed
# YouTube and Kick together while nothing else moved.
differential() {
  local plat="$1" out
  out="$(drive grant-differential "$plat")"
  if [ "$(val "$out" probed)" != true ]; then
    bad "$plat: the grant differential could not be probed"
  elif [ "$(val "$out" differs)" = true ]; then
    ok "$plat: refresh_token answers $(val "$out" realStatus)/$(val "$out" realError), a nonsense grant answers $(val "$out" bogusStatus)/$(val "$out" bogusError) -- the grant is recognised"
  else
    bad "$plat: refresh_token and a nonsense grant get the same answer ($(val "$out" realStatus)/$(val "$out" realError)); the endpoint may no longer serve it"
  fi
}

differential youtube
differential kick

sk "twitch: Twitch validates the client before the grant, so the differential needs a credential"
note "step 8 covers Twitch's refresh grant when POLY_OAUTH_TWITCH_* are set"
sk "facebook: Facebook has no refresh_token grant at all -- Refresh uses fb_exchange_token"
note "and Graph validates the client first, so the same differential is unreachable"

# ---------------------------------------------------------------------------
step "6. Facebook's pinned Graph version is still the version Facebook serves"

# THE SILENT BREAK IN THIS PACKAGE THAT NOTHING ELSE COULD SEE. Meta retires
# Graph versions on a schedule, and does not answer a retired one with an
# error: a request to /v3.0/me is served, successfully, by v20.0, and the only
# place the substitution is visible is the facebook-api-version response
# header. facebook.go pins v24.0 with a comment saying "a broadcast that starts
# working differently on a Tuesday is not a failure mode worth having" -- and
# that pin stops being a pin, silently, on the day v24.0 is retired.
#
# THE MUTATION FOR THIS ONE IS THE BEST EVIDENCE IN THE SUITE. Repinning
# fbGraphBase to v3.0 -- a version retired years ago -- produced exactly ONE
# failure in forty-six checks. The token endpoint still refused correctly, the
# data API still rejected a junk token correctly, and every other Facebook
# check stayed green, because Graph served the retired request from v20.0
# without complaint. That is what this silent break looks like, and this is the
# only check that can see it.
#
# The control was proven separately by making the driver return the version out
# of the request URL instead of out of the response header: the echo check
# failed immediately, which is what it is for.
FV="$(drive fb-version)"

if [ "$(val "$FV" baseFound)" != true ]; then
  bad "the Graph base could not be read back from the provider: $(val "$FV" error)"
  bad "so the served version could not be compared with the pinned one"
else
  PIN="$(val "$FV" pinnedVersion)"; SERVED="$(val "$FV" servedVersion)"
  if [ "$SERVED" = "$PIN" ] && [ -n "$SERVED" ]; then
    ok "facebook.go pins $PIN and Facebook served $SERVED"
  elif [ -z "$SERVED" ]; then
    # SAID SEPARATELY, because "no header came back" and "a different version
    # came back" are different facts and the second one is an accusation. A
    # host that could not be reached at all reported the first as the second
    # while this was one branch.
    bad "Facebook returned no facebook-api-version header, so the pin could not be checked"
  else
    bad "facebook.go pins $PIN but Facebook served '$SERVED'; the pinned version has been retired"
    note "Graph substitutes a supported version silently -- calls keep working and change behaviour"
  fi

  # THE CONTROL, without which the check above is an echo test. A version that
  # cannot exist must come back as a DIFFERENT one; if Facebook simply repeated
  # whatever was asked for, the header would prove nothing and the check above
  # would pass forever.
  CTL="$(val "$FV" controlVersion)"; REQ="$(val "$FV" controlRequested)"
  if [ -n "$CTL" ] && [ "$CTL" != "$REQ" ]; then
    ok "asking for $REQ was answered by $CTL, so the header reports what was served rather than what was asked"
  else
    bad "asking for $REQ was answered '$CTL'; the header is an echo and proves nothing about the pin"
  fi
fi

# ---------------------------------------------------------------------------
step "7. Every data API answers, and polyemesis reads the refusal correctly"

# THE ASSERTION IS ON POLYEMESIS'S READING, NOT ON THE WIRE. getJSON turns a
# 401 into "the platform rejected the access token (401); reconnect the
# account", and fbAdvice's code-190 branch writes the same verdict at greater
# length. Those sentences are what an operator acts on, and a check that only
# looked at the status code would pass while the branch that produces them was
# broken.
#
# A rejection here is decisive about the base URL because the alternative is
# observably different: api.twitch.tv/helixXX, api.kick.com/publicXX and
# googleapis.com/youtubeXX all answer 404, and getJSON reports a 404 as
# "<endpoint> returned 404" rather than as a rejected token.
#
# Proven able to fail against the committed tree by breaking each data-API path
# in turn -- ytAPIBase to /youtubeXX/v3, twitchHelixBase to /helixXX, Kick's
# channels path to /publicXX/v1/channels -- each producing exactly one failure,
# for that platform, reading "refused with status 404 but polyemesis did not
# classify it as a bad token". Facebook was proven by the graph-moved host.
apirefusal() {
  local plat="$1" out
  out="$(drive api-refusal "$plat")"
  if [ "$(val "$out" refused)" != true ]; then
    bad "$plat: the data API returned an identity for a junk token (account=$(val "$out" returnedAccount))"
  elif [ "$(val "$out" transportError)" = true ]; then
    bad "$plat: the data API host was never reached: $(val "$out" error)"
  elif [ "$(val "$out" classifiedAsBadToken)" = true ]; then
    ok "$plat: the data API refused a junk token and polyemesis classified it as one to reconnect"
  else
    bad "$plat: refused with status $(val "$out" status) but polyemesis did not classify it as a bad token"
    note "$(val "$out" error)"
  fi
}

apirefusal youtube
apirefusal twitch
apirefusal facebook
apirefusal kick

# ---------------------------------------------------------------------------
step "8. A real refresh, and what the refreshed token can do"

# THE ONLY STEP THAT NEEDS A CREDENTIAL, and it skips rather than fails without
# one -- the seven steps above are this suite's floor and they run everywhere.
#
# It is also the only step that can speak to the hour-four failure. Everything
# above establishes that a token endpoint is present and refuses a bad grant.
# None of it establishes that a good grant succeeds, and the gap between those
# two is exactly where a refresh that has quietly stopped working lives.
#
# FOUR CHECKS EITHER WAY, so the tally is the same whether a platform is
# configured or not. A floor that moved with which credentials happened to be
# in the environment would be no floor at all.
#
# NOTHING SECRET IS PRINTED. The stream key is reported as a length and a
# character-class verdict, the ingest URL as its scheme and host with the path
# dropped because some platforms put the key in the path, and the access token
# not at all. The driver's redact() is the backstop underneath all three.
#
# HALF-PROVEN, AND THE HONEST HALF IS WRITTEN DOWN. The failure branch was
# proven against the committed tree by exporting POLY_OAUTH_KICK_* with a
# secret generated at runtime: Kick refused it, this step reported four
# failures instead of four skips, and the tally stayed at forty-six. The PASS
# branch cannot be proven here, because no account is connected and
# platform_creds is still empty.
# It stays because it is the only thing that can ever speak to the hour-four
# failure, and it is marked as unproven rather than described as covered.
#
# Redaction was proven with a control rather than by observation. The driver was
# made to emit the secret directly: with redact() intact it came out as
# "[redacted POLY_OAUTH_KICK_CLIENT_SECRET]", and with redact() neutered the
# same emit printed the value -- so the clean run is a substitution having
# happened, not a platform having declined to echo anything back.
livecheck() {
  local plat="$1" up out EXP MISS
  up="$(printf '%s' "$plat" | tr '[:lower:]' '[:upper:]')"

  # THE SHELL NEVER TOUCHES A CREDENTIAL. It does not read the variables, test
  # them, or pass them anywhere -- the driver inherits the environment and
  # decides for itself whether it has all three, and answers skipped=true when
  # it does not. One process handling secrets is easier to keep honest than
  # two, and nothing a shell holds in a variable is safe from a stray set -x.
  out="$(drive live "$plat")"
  if [ "$(val "$out" skipped)" = true ]; then
    sk "$plat: no complete POLY_OAUTH_${up}_* credential set; the refresh was not attempted"
    note "set POLY_OAUTH_${up}_CLIENT_ID, _CLIENT_SECRET and _REFRESH_TOKEN to run it"
    sk "$plat: the refreshed token's lifetime was not measured"
    sk "$plat: the granted scopes were not compared against what this build asks for"
    sk "$plat: the ingest URL and stream key were not fetched"
    sk "$plat: the live viewer count was not read back"
    return
  fi

  if [ "$(val "$out" refreshed)" != true ]; then
    bad "$plat: the refresh FAILED: $(val "$out" error)"
    bad "$plat: no refreshed token to measure"
    bad "$plat: no granted scopes to compare"
    bad "$plat: no ingest to fetch"
    return
  fi
  ok "$plat: the refresh succeeded and returned an access token of $(val "$out" accessTokenLen) characters"

  # A LIFETIME, NOT A BOOLEAN. A refresh that hands back a token expiring in
  # ninety seconds is technically a success and practically the hour-four
  # failure arriving early. Ten minutes is the floor because the refresher's
  # own margin has to fit inside it.
  EXP="$(val "$out" expiresInSec)"
  case "$EXP" in
    ''|*[!0-9-]*) bad "$plat: the refreshed token carried no expiry" ;;
    *) if [ "$EXP" -gt 600 ]; then
         ok "$plat: the refreshed token is good for $EXP more seconds"
       else
         bad "$plat: the refreshed token expires in $EXP seconds, which is not a usable lifetime"
       fi ;;
  esac

  # The platform's own account of what the refreshed token carries, against
  # what this build asks for. A platform that quietly dropped a scope is
  # AccountNeedsReconnect's case arriving from the other direction, and the
  # operator would otherwise find out from a 401 mid-broadcast.
  MISS="$(val "$out" scopesMissing)"
  if [ -z "$MISS" ]; then
    ok "$plat: the refreshed token reports $(val "$out" scopesReported) scopes covering everything this build asks for"
  else
    bad "$plat: the refreshed token is missing scopes this build asks for: $MISS"
  fi

  # THE ONLY PLACE A REAL STATS BODY IS EVER READ. Every unit test in
  # internal/oauth answers from a stub whose body this repo wrote, so they prove
  # the decoder matches the fixture and never that the fixture matches the
  # platform. Kick's stats fallback is the standing reminder: it shipped against
  # a fixture shaped like the struct rather than like the endpoint, passed for as
  # long as it existed, and could never have worked.
  #
  # LIVENESS IS REPORTED, NOT ASSERTED. Whether this account happens to be
  # streaming is not the suite's business, and a check that failed because nobody
  # was live at 3am would be switched off within a week. The shape IS asserted:
  # the call has to succeed, and a viewer count has to be either a real number or
  # honestly absent -- never the fabricated zero this phase exists to prevent.
  case "$(val "$out" statsOK)" in
    true)
      if [ "$(val "$out" statsLive)" = true ]; then
        ok "$plat: live now, $(val "$out" statsViewers) viewers per $(val "$out" statsSource)"
      else
        ok "$plat: not live, and said so without inventing a viewer count (viewers: $(val "$out" statsViewers))"
      fi ;;
    unsupported)
      sk "$plat: polyemesis reads no viewer count from this platform" ;;
    *)
      bad "$plat: the viewer-count read FAILED: $(val "$out" statsError)" ;;
  esac

  # THE #312 FIELD. This is the destination URL and stream key polyemesis fills
  # in automatically -- the same pair whose hand-typed equivalent shipped a
  # Kick preset that could not publish. Reported redacted: a suite that leaked
  # a key while checking that keys are handled correctly would be its own bug
  # report.
  if [ "$(val "$out" ingestOK)" = true ] && [ "$(val "$out" ingestKeyClean)" = true ]; then
    ok "$plat: ingest resolved to $(val "$out" ingestScheme)://$(val "$out" ingestHost)/… with a clean $(val "$out" ingestKeyLen)-character key"
  elif [ "$(val "$out" ingestOK)" = true ]; then
    bad "$plat: the ingest key is empty or carries whitespace (length $(val "$out" ingestKeyLen))"
  else
    bad "$plat: ingest failed: $(val "$out" ingestError)"
  fi
}

livecheck youtube
livecheck twitch
livecheck facebook
livecheck kick

# ---------------------------------------------------------------------------
printf "\n"
printf "  \033[1m%d passed, %d failed, %d skipped\033[0m\n\n" "$pass" "$fail" "$skip"

# The floor, so a run that dies halfway cannot report a green tally over six
# checks. FIXED, not a range: every branch in this suite contributes the same
# number either way -- step 8 counts four per platform as a skip, as a pass or
# as a failure, and step 4's refusal helper counts two on every path including
# the one where a platform mints a token it should not. A floor that moved with
# which credentials happened to be in the environment would be no floor at all.
EXPECTED_CHECKS=50
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
