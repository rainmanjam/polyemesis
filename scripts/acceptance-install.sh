#!/usr/bin/env bash
# Installer acceptance: the decisions install.sh makes, without installing.
#
# install.sh had no tests at all, and the first thing a reading of it turned up
# was a real bug: CAP_NET_BIND_SERVICE was granted only when tls.mode was acme,
# so an operator choosing selfsigned -- the DEFAULT -- and accepting the 443
# offer got a unit that could not bind the port the installer had just written
# into its own ExecStart. `bind: permission denied`, on a fresh install, from
# following the prompts.
#
# This does not run the installer. It extracts the decisions and drives them
# directly, because the alternative is a container per case and the thing worth
# pinning is the LOGIC, not that bash can write a file.
set -uo pipefail
SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
INSTALL="$SCRIPTS/install.sh"

pass=0; fail=0
ok()  { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad() { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step(){ printf "\n\033[1m%s\033[0m\n" "$1"; }

step "1. The file is syntactically valid"
bash -n "$INSTALL" && ok "install.sh parses" || bad "install.sh has a syntax error"

# The capability decision, lifted verbatim. If the case in install.sh changes
# shape this stops matching and the extraction check below fails loudly rather
# than silently testing a stale copy.
caps_for() { # caps_for <tls_mode> <http_port> -> "yes" | "no"
  case "$1:$2" in
    acme:*|*:443|*:80) echo yes ;;
    *)                 echo no  ;;
  esac
}

step "2. CAP_NET_BIND_SERVICE is granted for every privileged port"
# tls_mode http_port want
while read -r mode port want; do
  got=$(caps_for "$mode" "$port")
  if [ "$got" = "$want" ]; then
    ok "$mode on $port -> caps=$got"
  else
    bad "$mode on $port -> caps=$got, want $want"
  fi
done <<'CASES'
off        8080 no
off        443  yes
off        80   yes
selfsigned 8080 no
selfsigned 443  yes
selfsigned 80   yes
acme       8080 yes
acme       443  yes
acme       80   yes
CASES

step "3. install.sh still contains the case this mirrors"
# The mirror above is only worth anything while it matches the original. Pin
# both the branch and the capability name.
if grep -q 'acme:\*|\*:443|\*:80' "$INSTALL"; then
  ok "the capability case is present and unchanged in shape"
else
  bad "the capability case in install.sh no longer matches this test's copy"
  printf "        the test above is now checking a stale mirror; re-read install.sh\n"
fi
grep -q 'AmbientCapabilities=CAP_NET_BIND_SERVICE' "$INSTALL" \
  && ok "AmbientCapabilities=CAP_NET_BIND_SERVICE is still written into the unit" \
  || bad "the unit no longer grants CAP_NET_BIND_SERVICE at all"

step "4. The 443 offer only fires on an untouched default"
# A port given on the command line or typed at the prompt is a DECISION. The
# offer must not overwrite it, or --port would silently not mean what it says.
if grep -q 'HTTP_PORT_SET" != true \] && \[ "\$HTTP_PORT" = "8080"' "$INSTALL"; then
  ok "the offer is gated on both HTTP_PORT_SET and the 8080 default"
else
  bad "the 443 offer is no longer gated on an untouched default; --port could be overridden"
fi

step "5. The firewall opens the CHOSEN port, not 443 unconditionally"
# Opening a port nothing binds looks like working TLS and serves nothing.
if grep -q 'ufw allow "${HTTP_PORT}/tcp"' "$INSTALL"; then
  ok "ufw opens HTTP_PORT"
else
  bad "ufw no longer opens HTTP_PORT"
fi
if grep -qE 'ufw allow 443/tcp' "$INSTALL"; then
  bad "ufw opens 443 unconditionally; a port nothing binds is worse than a closed one"
else
  ok "no unconditional 443 rule"
fi

step "6. ACME still opens 80 for the http-01 challenge"
grep -q '\[ "\$TLS_MODE" = acme \]  && ufw allow 80/tcp' "$INSTALL" \
  && ok "acme opens 80" || bad "acme no longer opens 80; issuance would never complete"

printf "\n\033[1mSummary\033[0m\n  %d passed, %d failed\n" "$pass" "$fail"
[ "$fail" -eq 0 ] || { printf "\n  \033[31mINSTALLER ACCEPTANCE FAILED\033[0m\n"; exit 1; }
printf "\n  \033[32mINSTALLER ACCEPTANCE PASSED\033[0m\n"
