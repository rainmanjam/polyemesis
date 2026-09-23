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
#
# The exception is the upgrade guard in sections 7 and 8, and it is not really
# one: the script install.sh WRITES is run, against a temporary directory, and
# the installer itself still never runs. See the note above section 7.
set -uo pipefail
SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
INSTALL="$SCRIPTS/install.sh"
# This suite shells out to nothing and so has never needed the preflight
# helpers; it is sourced for poly_verdict_trap alone.
. "$SCRIPTS/lib-preflight.sh"
# poka-yoke: the run's own verdict, armed before the first check so no exit
# path can skip it. Held as a trap rather than printed at the foot of the
# script, because the foot is one exit path out of many -- and the `exit 1` at
# the end of this file is another. See the verdict section of lib-preflight.sh
# for the failure -- a red run reported as exit 0 -- that is why.
trap 'poly_verdict_trap $?' EXIT

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

# ------------------------------------------------- acme's two pre-commit facts
#
# BOTH OF THESE ARE ABOUT THE MOMENT BEFORE THE OPERATOR COMMITS, which is when
# an acme mistake is cheapest to fix and most expensive to discover: a failed
# validation leaves NO certificate -- the self-signed one is not a fallback --
# and Let's Encrypt allows five failures per hostname per hour.

step "7. The DNS check compares against this host, not just 'does it resolve'"
# internal/api's acme-preflight answers this in three parts: doesn't resolve,
# resolves HERE, resolves ELSEWHERE. The installer used to answer only the
# first, which is the one that catches the rarest mistake -- a record pointing
# at an old host or a CDN looks identical to no record at all from a check that
# only asks whether the name resolves.
# THE CALL, NOT THE WORD. The first version of this grepped for the bare name
# `local_addresses_include`, which also appears in the helper's own doc comment
# -- so a mutation that renamed the function and stubbed the branch to `false`
# left the comment behind and this assertion passed over a script that no longer
# compared anything. Matching the invocation is what makes it a check.
grep -qE 'if +local_addresses_include +"\$acme_resolved"' "$INSTALL" \
  && ok "the resolved address is compared against this machine's own" \
  || bad "the installer is back to asking only whether the name resolves; a record pointing at the wrong host would install silently"
grep -q 'not an address this machine holds' "$INSTALL" \
  && ok "a name resolving elsewhere is reported" \
  || bad "nothing tells the operator the name went somewhere else"
# UNKNOWN, NOT FAIL. Behind NAT, a floating IP or a load balancer, a resolved
# address this box does not hold is exactly correct, so this must never harden
# into a refusal.
if grep -qE 'local_addresses_include.*\|\| *die' "$INSTALL"; then
  bad "a name resolving elsewhere aborts the install; behind NAT that is the correct configuration"
else
  ok "resolving elsewhere warns rather than refusing"
fi

step "8. The TLS menu offers the most secure mode first"
# EVERY OTHER ORDERING IN THE PRODUCT ALREADY RUNS THIS WAY: tls.mode: auto
# resolves acme -> selfsigned, TLS.md's worked configurations lead with the
# public-DNS deployment, and the install-mode menu above leads with the
# recommendation. This menu led with plain HTTP and buried acme at option 3.
acme_pos="$(grep -n '1) acme' "$INSTALL" | head -1 | cut -d: -f1)"
off_pos="$(grep -n '3) off' "$INSTALL" | head -1 | cut -d: -f1)"
if [ -n "$acme_pos" ] && [ -n "$off_pos" ] && [ "$acme_pos" -lt "$off_pos" ]; then
  ok "acme is offered before off"
else
  bad "the TLS menu no longer leads with the most secure mode"
fi
# The numeric aliases have to follow the menu, or 1 and 3 select the opposite
# of what is printed beside them -- which is worse than either order.
grep -q '1|acme) TLS_MODE="acme"' "$INSTALL" \
  && ok "choice 1 selects acme" || bad "choice 1 no longer matches the menu it is printed under"
grep -q '3|off)  TLS_MODE="off"' "$INSTALL" \
  && ok "choice 3 selects off" || bad "choice 3 no longer matches the menu it is printed under"

step "9. The interactive default is DECIDED, and only acme when the name already points here"
# BY INVOCATION, NOT BY GREP. The whole value of suggested_tls_mode is what it
# returns, and a check that reads the script's text cannot tell a working
# decision from a comment describing one -- which is exactly how the DNS
# assertion in step 7 first passed over a script that compared nothing.
tls_dec="$(mktemp)"
sed -n '/^local_addresses() {/,/^}/p;/^local_addresses_include() {/,/^}/p;/^resolved_addresses() {/,/^}/p;/^public_ip() {/,/^}/p;/^suggested_tls_mode() {/,/^}/p' "$INSTALL" > "$tls_dec"
# shellcheck disable=SC1090
CHECK_PUBLIC_IP=false . "$tls_dec"

expect_mode() { # want, hostname, email, why
  got="$(CHECK_PUBLIC_IP=false suggested_tls_mode "$2" "$3")"
  if [ "$got" = "$1" ]; then ok "$4 -> $got"; else bad "$4 -> $got (wanted $1)"; fi
}
expect_mode selfsigned ""                      "a@b.com" "no hostname"
expect_mode selfsigned "stream.example.com"    ""        "no contact address"
expect_mode selfsigned "polyemesis.local"      "a@b.com" "a .local name Let's Encrypt cannot issue for"
expect_mode selfsigned "nodots"                "a@b.com" "a name with no dot"
expect_mode selfsigned "no-such-host.invalid"  "a@b.com" "a name that does not resolve"

# THE TWO THAT DECIDE IT, AND THEY ARE STUBBED ON PURPOSE.
#
# The first version of these asked the real resolver about example.com, and on a
# machine where that does not resolve the function returned selfsigned from the
# "does not resolve" branch -- one step BEFORE the comparison under test. A
# mutation that suggested acme unconditionally passed. The case was green for a
# reason that had nothing to do with what it claimed to check, which is the same
# masking the VOD fixture in internal/oauth hit and the same lesson: a fixture
# that can exit early is a fixture that can pass early.
#
# Stubbing the lookup is what internal/api's own preflight test does with
# s.resolver(). The comparison is the thing under test; DNS is not.
resolved_addresses() { printf '%s' "$STUB_RESOLVES"; }

STUB_RESOLVES="203.0.113.9"
expect_mode selfsigned "stream.example.com" "a@b.com" "a name resolving somewhere this box does not hold"

# THE POSITIVE CONTROL. Without it every case above passes against a function
# that always answers selfsigned, and the whole step asserts nothing.
STUB_RESOLVES="$(local_addresses | head -1)"
if [ -n "$STUB_RESOLVES" ]; then
  expect_mode acme "stream.example.com" "a@b.com" "a name resolving to an address this box holds"
else
  bad "local_addresses returned nothing, so acme can never be suggested and every case above is vacuous"
fi

# And the opt-in path: with the flag off, a NAT'd box stays on selfsigned even
# when the world would see the match.
STUB_RESOLVES="203.0.113.9"
public_ip() { printf '203.0.113.9\n'; }
expect_mode selfsigned "stream.example.com" "a@b.com" "NAT match available but --check-public-ip not passed"
if [ "$(CHECK_PUBLIC_IP=true suggested_tls_mode "stream.example.com" "a@b.com")" = acme ]; then
  ok "with --check-public-ip, a NAT'd box whose public address matches is offered acme"
else
  bad "the opt-in lookup does not change the answer, so the flag does nothing"
fi
rm -f "$tls_dec"

step "10. The public-IP lookup is opt-in and never runs by itself"
# It is the only check in this installer that tells a third party the install
# exists. That is the operator's decision, so it must never fire from a default.
grep -q 'CHECK_PUBLIC_IP=false' "$INSTALL" \
  && ok "it defaults to off" || bad "the third-party lookup is no longer off by default"
grep -q -- '--check-public-ip) CHECK_PUBLIC_IP=true' "$INSTALL" \
  && ok "--check-public-ip is the only way to turn it on" || bad "the opt-in flag is gone"
if grep -nE '^\s*(pub|[A-Za-z_]+)="?\$\(public_ip' "$INSTALL" | grep -qv 'CHECK_PUBLIC_IP'; then
  : # checked below against the guard rather than here
fi
# The call sits behind the flag, in the same branch, not merely near it.
awk '/if \[ "\$CHECK_PUBLIC_IP" = true \]/{f=1} f&&/public_ip/{found=1} END{exit !found}' "$INSTALL" \
  && ok "public_ip is called only inside the opt-in branch" \
  || bad "public_ip is reachable without the flag that exists to gate it"

step "8. ACME does not turn HSTS on before issuance has ever worked"
# TLS.md's reasoning, and the UI panel that prints the same snippet already
# follows it: HSTS has no server-side undo, and install.sh --tls acme is handed
# to somebody whose FIRST acme restart has not happened yet. If issuance later
# breaks and they fall back to selfsigned, a browser that received the header
# refuses the click-through and setting hsts: false does not help.
# THE COMMENTED FORM CONTAINS THE SAME WORDS, which is how the first version of
# this check failed against the fixed script: `# hsts: true` matches any pattern
# looking for `hsts: true`. What separates them is the two spaces of YAML indent
# immediately before it, with nothing in between.
if grep -qE "printf 'tls:.*mode: .acme.*\\\\n  hsts: true" "$INSTALL"; then
  bad "acme writes hsts: true; a first-time issuance failure then locks the browser out of the fallback"
else
  ok "acme leaves hsts off until issuance is known to work"
fi
grep -q 'no server-side undo' "$INSTALL" \
  && ok "the config says why hsts is left off" \
  || bad "hsts is off with no reason recorded, so the next reader will turn it on"

# ------------------------------------------------------------ upgrade guard
#
# WHY THIS IS IN THIS FILE. The update.sh install.sh writes is a DECISION the
# installer makes, which is what every case above pins, and driving it needs
# nothing this suite does not already have: a temporary directory, four files
# and the script install.sh just wrote. No container, no download, no root. A
# separate suite would be a second copy of this harness testing the same file.
#
# WHY IT IS WORTH THE LINES. #348 gave the binary install an upgrade script
# with five refusals in it and a test for none of them. The refusal that
# matters is secret.key. Since 0.7.0 seals destination stream keys at rest, a
# database restored WITHOUT secret.key comes back with every destination
# DISABLED -- correctly, because a key that will not open disables its
# destination rather than failing open. Nothing about that restore looks wrong:
# the server starts, the database loads, every destination is still listed. The
# operator learns what the backup was missing when they go live and nothing
# publishes. If this guard regresses, the upgrade that ate the key still exits
# 0 and still prints "backup verified".
#
# The script under test is GENERATED by sourcing install.sh, never transcribed.
# A test carrying its own copy of update.sh would go on passing for years after
# install.sh stopped writing the check.

work="$(mktemp -d)"
# Split from the EXIT case, which now has to carry the verdict as well: bash
# keeps ONE EXIT handler, so a line that installs only the rm silently disarms
# the verdict armed at the top of this file and a truncated log goes back to
# reading a failed run as a pass. INT and TERM keep the plain rm they had --
# they never exited this script, and this is not the change that should make
# them start.
trap 'rm -rf "$work"' INT TERM
trap 'poly_verdict_trap $? rm -rf "$work"' EXIT

# install.sh ends in `main "$@"`, so sourcing it as-is would attempt an install
# on whoever ran this suite. Replace that one line, and PROVE the replacement
# matched before eval rather than assuming it: the failure mode of a bad
# assumption here is a developer's laptop growing a polyemesis user, a unit
# file and a /var/lib directory.
load_install_defs() {
  local body
  body="$(sed 's/^main "$@"$/: # main invocation stripped by acceptance-install.sh/' "$INSTALL")"
  if printf '%s\n' "$body" | grep -q '^main "$@"$'; then
    echo "acceptance-install: install.sh's main invocation did not strip; refusing to source it" >&2
    return 1
  fi
  eval "$body"
  # install.sh arms this at top level to undo a partial install. Nothing here
  # installs anything, and leaving it armed makes the subshell's own exit run a
  # rollback against whatever INSTALL_DIR happens to be set to.
  trap - EXIT INT TERM
}

gen_binary_update() { # gen_binary_update <install_dir> <data_dir>
  # These four are read by install.sh's write_binary_update_script, which
  # arrives through the eval above and is therefore invisible to static
  # analysis.
  # shellcheck disable=SC2034
  ( load_install_defs || exit 1
    INSTALL_DIR="$1"
    DATA_DIR="$2"
    BIN_PATH="$1/polyemesis"
    SERVICE_NAME="polyemesis-acceptance"
    write_binary_update_script )

  # THE BACKUP CHECK IS NOT OPTIONAL, SO THE SUITE HAS TO SUPPLY IT.
  #
  # update.sh asks the installed binary whether the copy it just took actually
  # opens -- `polyemesis -verify-backup <dir>` -- and refuses the upgrade when
  # it cannot ask. That refusal is the point (#643): printing "verified"
  # without opening the copy is the bug it replaced. So this stub stands in for
  # the real binary and answers the same question the shell can answer: are the
  # two files there. The database-level checks it cannot do -- integrity_check,
  # a truncated file, a valid SQLite file that is not ours -- are covered in
  # internal/db/verifybackup_test.go. This suite is testing update.sh's control
  # flow, not SQLite.
  cat > "$1/polyemesis" <<'BINSTUB'
#!/usr/bin/env bash
set -u
if [ "${1:-}" = "-verify-backup" ]; then
  d="${2:-}"
  [ -f "$d/polyemesis.db" ] || { echo "stub: no polyemesis.db in $d" >&2; exit 1; }
  [ -f "$d/secret.key" ]    || { echo "stub: no secret.key in $d" >&2; exit 1; }
  # The stub has to be able to say NO for a reason the EXISTENCE checks in
  # update.sh cannot already reach, or the verify step is never what decides
  # and a suite that removes it still passes. The SQLite magic is the cheapest
  # honest stand-in for "it opens": a copy taken mid-write, a truncated file
  # and a wrong file all fail it. The real checks are in
  # internal/db/verifybackup_test.go.
  case "$(head -c 15 "$d/polyemesis.db" 2>/dev/null)" in
    "SQLite format 3") ;;
    *) echo "stub: $d/polyemesis.db is not a SQLite database" >&2; exit 1 ;;
  esac
  echo "backup at $d opens, passes integrity_check and holds this server's schema"
  exit 0
fi
echo "stub polyemesis: unexpected invocation: $*" >&2
exit 1
BINSTUB
  chmod +x "$1/polyemesis"
}

check_refusal() { # check_refusal <label> <status> <output> <substring the message must name>
  local label="$1" status="$2" out="$3" want="$4"
  if [ "$status" -eq 0 ]; then
    bad "$label: update.sh exited 0 — the upgrade would have gone ahead"
    return
  fi
  case "$out" in
    *"$want"*) ok "$label, and the message names it: \"$want\"" ;;
    *) bad "$label: it refused, but the message never says \"$want\""
       printf '        got: %s\n' "$(printf '%s' "$out" | tr '\n' ' ')" ;;
  esac
}

backups_under() { # backups_under <dir> -> how many data.bak-* it holds
  find "$1" -maxdepth 1 -name 'data.bak-*' 2>/dev/null | wc -l | tr -d ' '
}

step "7. The generated update.sh refuses a backup it cannot be restored from"

main_calls="$(grep -c '^main "$@"$' "$INSTALL")"
if [ "$main_calls" = 1 ]; then
  ok "install.sh still ends in one bare \`main \"\$@\"\`, which is what makes it sourceable"
else
  bad "expected exactly one top-level \`main \"\$@\"\` in install.sh, found $main_calls"
fi

# (1) No data directory at all. cp would fail, but only AFTER the script had
#     told the operator it was backing something up.
root="$work/absent"; mkdir -p "$root"
gen_binary_update "$root/opt" "$root/data"
bash -n "$root/opt/update.sh" \
  && ok "the generated update.sh parses" \
  || bad "install.sh generated an update.sh with a syntax error"
out="$(bash "$root/opt/update.sh" 2>&1)"; st=$?
check_refusal "a data directory that does not exist is refused" "$st" "$out" "does not exist"
[ "$(backups_under "$root")" = 0 ] \
  && ok "and it refused before creating anything" \
  || bad "it created a backup directory for a data directory that does not exist"

# (2) An empty one. This is the shape the docker branch's comment describes:
#     the backup succeeds, archives nothing, exits 0, and the upgrade proceeds
#     with no way back.
root="$work/empty"; mkdir -p "$root/data"
gen_binary_update "$root/opt" "$root/data"
out="$(bash "$root/opt/update.sh" 2>&1)"; st=$?
check_refusal "an empty data directory is refused" "$st" "$out" "is empty"
[ "$(backups_under "$root")" = 0 ] \
  && ok "and again nothing was created" \
  || bad "it backed up an empty directory instead of refusing"

# (3) THE ONE THAT MATTERS. A database with no key beside it. Everything about
#     this backup looks fine to a count of files.
root="$work/nokey"; mkdir -p "$root/data"
printf 'SQLite format 3\000' > "$root/data/polyemesis.db"
printf 'recording\n' > "$root/data/recording.mp4"
gen_binary_update "$root/opt" "$root/data"
out="$(bash "$root/opt/update.sh" 2>&1)"; st=$?
check_refusal "a backup with a database but NO secret.key is refused" "$st" "$out" "secret.key"
case "$out" in
  *disabled*) ok "and it says what the operator would have lost: every destination disabled" ;;
  *) bad "the secret.key refusal no longer explains that the restore comes back disabled"
     printf '        an operator told only that a file is missing restores anyway\n' ;;
esac

# (4) The other half of the pair: a key with nothing to unseal.
root="$work/nodb"; mkdir -p "$root/data"
printf 'key\n' > "$root/data/secret.key"
gen_binary_update "$root/opt" "$root/data"
out="$(bash "$root/opt/update.sh" 2>&1)"; st=$?
check_refusal "a backup with no polyemesis.db is refused" "$st" "$out" "polyemesis.db"

# (5) and (6) The happy path, and then the SAME script run again — which is
#     what an operator does after an upgrade goes wrong, and how the nesting
#     bug was found: `cp -a src dest` puts src INSIDE dest when dest exists, so
#     the second run's checks would have been reading data.bak-STAMP/data and
#     passing against a directory that is not the backup.
#
#     The backup name carries a minute-resolution stamp, so the collision only
#     exists while both runs land in the same minute. If the clock crosses one
#     mid-case the two runs chose different names and the case tested nothing;
#     retry rather than report a pass it did not earn.
# THE CASE THE EXISTENCE CHECKS CANNOT REACH.
#
# Both files are present, so every check update.sh had before #643 passes --
# and the database still does not open, which is exactly what a copy taken
# from a live server can look like. Without this case the verify step never
# decides anything here: measured, stubbing the verify call out entirely left
# the suite green at 89/89.
a_backup_that_exists_but_will_not_open() { # <root>
  local root="$1" out st
  mkdir -p "$root/data"
  printf 'key\n' > "$root/data/secret.key"
  printf 'this is not a database\n' > "$root/data/polyemesis.db"
  gen_binary_update "$root/opt" "$root/data"

  out="$(bash "$root/opt/update.sh" 2>&1)"; st=$?
  if [ "$st" -eq 0 ]; then
    bad "a backup whose database does not open was accepted -- the upgrade would have gone ahead with no way back"
    printf '        %s\n' "$(printf '%s' "$out" | tr '\n' ' ')"
    return
  fi
  case "$out" in
    *"not usable"*) ok "a backup whose database does not open is refused, and the message says it is not usable" ;;
    *) bad "it refused, but the message never says the backup is unusable"
       printf '        got: %s\n' "$(printf '%s' "$out" | tr '\n' ' ')" ;;
  esac
}

happy_path_then_a_second_run() { # <root> -> 2 if the clock crossed a minute
  local root="$1" before after out1 st1 out2 st2 dest
  mkdir -p "$root/data"
  printf 'key\n' > "$root/data/secret.key"
  printf 'SQLite format 3\000' > "$root/data/polyemesis.db"
  gen_binary_update "$root/opt" "$root/data"

  before="$(date +%F-%H%M)"
  out1="$(bash "$root/opt/update.sh" 2>&1)"; st1=$?
  out2="$(bash "$root/opt/update.sh" 2>&1)"; st2=$?
  after="$(date +%F-%H%M)"
  [ "$before" = "$after" ] || return 2

  if [ "$st1" -eq 0 ]; then
    ok "a data directory holding both files is allowed through"
  else
    bad "the guard refused a complete backup (exit $st1)"
    printf '        %s\n' "$(printf '%s' "$out1" | tr '\n' ' ')"
  fi
  # The line changed with #643 and the claim in it got stronger. It used to
  # say the two files were PRESENT; presence is not the property that matters,
  # because a copy taken from a live database exists and may not open. It now
  # reports that the copy was opened. Pinned here for the same reason the old
  # line was: rewording it is a reviewable act, not an accident.
  case "$out1" in
    *"backup verified: it opens"*)
      ok "and the success line claims the copy was OPENED, not merely that two files exist" ;;
    *) bad "the success line no longer says the backup was opened" ;;
  esac

  # The reported path is the operator's only way back. It has to be real.
  dest="$(printf '%s\n' "$out1" | sed -n 's/^backing up .* to //p' | head -1)"
  if [ -n "$dest" ] && [ -f "$dest/secret.key" ] && [ -f "$dest/polyemesis.db" ]; then
    ok "it reports the backup path, and that path holds both files"
  else
    bad "the reported backup path (${dest:-none reported}) does not hold both files"
  fi

  check_refusal "a second run in the same minute is refused" "$st2" "$out2" "already exists"
  if [ -e "$dest/data" ]; then
    bad "the second run nested the copy: $dest/data exists, so the checks read the wrong directory"
  else
    ok "and the first backup was left intact — nothing nested inside it"
  fi
  [ "$(backups_under "$root")" = 1 ] \
    && ok "one run, one backup" \
    || bad "two runs left $(backups_under "$root") backup directories"
}

a_backup_that_exists_but_will_not_open "$work/unopenable"

tries=0
while :; do
  tries=$((tries + 1))
  happy_path_then_a_second_run "$work/happy-$tries"
  [ $? -eq 2 ] || break
  if [ "$tries" -ge 3 ]; then
    bad "the clock crossed a minute on all three attempts; the repeat-run case never ran"
    break
  fi
done

step "8. The docker branch's update.sh refuses the same missing key"
# The archive version of the same guard, driven with a stub `docker` because
# what is under test is the script's reaction to an archive, not docker. The
# stub understands exactly the two invocations the generated script makes.
stub="$work/stub-bin"; mkdir -p "$stub"
cat > "$stub/docker" <<'STUB'
#!/usr/bin/env bash
set -u
case "${1:-}" in
  volume)
    case "${2:-}" in
      inspect) [ -d "$STUB_VOLUME" ] ; exit $? ;;
      ls)      echo "DRIVER  VOLUME NAME"; echo "local   some-other-volume"; exit 0 ;;
    esac ;;
  run)
    # THE BACKUP CHECK (#643). The stub models the one property of the real
    # image that decided whether it worked: a database handed in on a
    # READ-ONLY bind mount does not open. SQLite has to create the -shm beside
    # a WAL database, the mount forbids it, and the real server answers
    # `unable to open database file (14)`. This stub used to accept that mount,
    # and so the suite was green over a verify step that refused every real
    # docker upgrade -- measured on a real install with the 0.10.0 image.
    # Any host bind mount is refused here, not just :ro: its owner and mode
    # are the host's, which the image's uid 10001 does not share.
    for a in "$@"; do
      if [ "$a" = "-verify-backup" ] || [ "$a" = "--entrypoint" ]; then
        for m in "$@"; do
          case "$m" in
            *:/backup:ro|*:/backup)
              echo "polyemesis: backup at /backup is not usable: backup's polyemesis.db could not be read: unable to open database file (14)" >&2
              exit 1 ;;
          esac
        done
        # The supported shape: the archive on stdin, unpacked by the container.
        vdir="$(mktemp -d)"
        tar xzf - -C "$vdir" || { echo "stub: the archive on stdin will not unpack" >&2; exit 1; }
        [ -f "$vdir/polyemesis.db" ] || { echo "stub: no polyemesis.db in the archive" >&2; exit 1; }
        [ -f "$vdir/secret.key" ]    || { echo "stub: no secret.key in the archive" >&2; exit 1; }
        case "$(head -c 15 "$vdir/polyemesis.db" 2>/dev/null)" in
          "SQLite format 3") ;;
          *) echo "stub: the archived polyemesis.db is not a SQLite database" >&2; exit 1 ;;
        esac
        rm -rf "$vdir"
        echo "backup opens, passes integrity_check and holds this server's schema"
        exit 0
      fi
    done
    # The archive: `alpine tar czf - -C /data .` to stdout, which the script
    # redirects into the file it created. The older shape wrote
    # /backup/NAME through a bind mount; still understood, so a revert of the
    # script is caught by the assertions below rather than by this stub.
    archive=""
    for a in "$@"; do case "$a" in /backup/*) archive="${a#/backup/}" ;; esac; done
    if [ -n "$archive" ]; then
      tar czf "$STUB_BACKUP_DIR/$archive" -C "$STUB_VOLUME" . || exit 1
    else
      tar czf - -C "$STUB_VOLUME" . || exit 1
    fi
    exit 0 ;;
esac
echo "stub docker: unexpected invocation: $*" >&2
exit 1
STUB
chmod +x "$stub/docker"

gen_docker_update() { # gen_docker_update <install_dir> [compose_cmd]
  # Read by install.sh's write_helper_scripts, same as above.
  # shellcheck disable=SC2034
  ( load_install_defs || exit 1
    INSTALL_DIR="$1"
    MODE=docker
    COMPOSE_CMD="${2-echo [stub compose]}"
    write_helper_scripts >/dev/null )
}

run_docker_update() { # run_docker_update <install_dir> <volume_dir> [args...]
  local dir="$1" vol="$2"; shift 2
  # umask 022, the common default, so an archive mode that only came out right
  # because the suite's own umask was strict cannot pass.
  ( umask 022
    STUB_VOLUME="$vol" STUB_BACKUP_DIR="$dir" PATH="$stub:$PATH" \
      bash "$dir/update.sh" "$@" 2>&1 )
}

# A compose stub that can answer `top` two ways, so the on-air guard has
# something to refuse. Everything else it echoes, exactly as the plain
# `echo [stub compose]` command used elsewhere in this section does, so the
# assertions about reaching `pull` read the same.
cat > "$stub/compose" <<'COMPOSESTUB'
#!/usr/bin/env bash
set -u
if [ "${1:-}" = "top" ]; then
  if [ "${STUB_ON_AIR:-false}" = true ]; then
    echo "polyemesis"
    echo "UID   PID   CMD"
    echo "root  4211  ffmpeg -re -i srt://127.0.0.1:9000 -f flv rtmp://live.example.net/app/streamkey"
  fi
  exit 0
fi
echo "[stub compose] $*"
COMPOSESTUB
chmod +x "$stub/compose"

docker_dir="$work/docker"; mkdir -p "$docker_dir"
gen_docker_update "$docker_dir"
bash -n "$docker_dir/update.sh" \
  && ok "the generated docker update.sh parses" \
  || bad "install.sh generated a docker update.sh with a syntax error"

out="$(run_docker_update "$docker_dir" "$work/no-such-volume")"; st=$?
check_refusal "a missing volume is refused" "$st" "$out" "no docker volume named"

# Each case below is a FRESH scenario, not a rerun -- clear whatever the
# previous case left behind so this test drives the volume/archive checks in
# isolation from the same-minute collision guard, which gets its own case.
rm -f "$docker_dir"/backup-*.tar.gz

vol="$work/vol-empty"; mkdir -p "$vol"
out="$(run_docker_update "$docker_dir" "$vol")"; st=$?
check_refusal "an empty volume is refused" "$st" "$out" "archive is empty"

rm -f "$docker_dir"/backup-*.tar.gz

vol="$work/vol-nokey"; mkdir -p "$vol"
printf 'SQLite format 3\000' > "$vol/polyemesis.db"
out="$(run_docker_update "$docker_dir" "$vol")"; st=$?
check_refusal "an archive with a database but NO secret.key is refused" "$st" "$out" "no secret.key"
case "$out" in
  *disabled*) ok "and it says what that costs: every destination back disabled" ;;
  *) bad "the docker secret.key refusal no longer explains the consequence" ;;
esac
# That refusal came AFTER `compose stop`. It used to exit there under set -e,
# leaving a broadcast host off the air with one line of explanation at best.
case "$out" in
  *"[stub compose] start"*) ok "and it starts the container it stopped, rather than leaving the host off the air" ;;
  *) bad "the refusal left the container stopped: update.sh never ran \`compose start\`" ;;
esac
if [ -z "$(find "$docker_dir" -maxdepth 1 -name 'backup-*.tar.gz')" ]; then
  ok "and the unverified archive it wrote is removed, not left holding the disk"
else
  bad "a refused run left its unverified archive behind: $(ls "$docker_dir"/backup-*.tar.gz)"
fi

rm -f "$docker_dir"/backup-*.tar.gz

vol="$work/vol-ok"; mkdir -p "$vol"
printf 'SQLite format 3\000' > "$vol/polyemesis.db"
printf 'key\n'    > "$vol/secret.key"
out="$(run_docker_update "$docker_dir" "$vol")"; st=$?
if [ "$st" -eq 0 ]; then
  ok "a volume holding both files is allowed through"
else
  bad "the docker guard refused a complete archive (exit $st)"
  printf '        %s\n' "$(printf '%s' "$out" | tr '\n' ' ')"
fi
case "$out" in
  *"[stub compose] pull"*) ok "and only then does it reach the pull" ;;
  *) bad "the happy path never reached \`compose pull\`" ;;
esac
# The archive holds secret.key and tls/ca.key. Written by tar inside the
# container it took the container's umask -- 0644, readable by every local
# account. Run under a permissive umask on purpose: the mode must not depend on
# the operator's.
archive="$(find "$docker_dir" -maxdepth 1 -name 'backup-*.tar.gz' | head -1)"
mode="$(ls -l "$archive" 2>/dev/null | cut -c1-10)"
[ "$mode" = "-rw-------" ] \
  && ok "the backup archive is 0600: it holds secret.key, so only its owner may read it" \
  || bad "the backup archive is ${mode:-missing}, so any local account can read secret.key out of it"

step "9. A second docker update in the same minute does not overwrite the backup"
# Same stamp, same dest -- tar czf would otherwise TRUNCATE the archive from
# the happy-path run above, replacing the pre-upgrade backup with whatever the
# half-migrated volume holds now, at the exact moment an operator has least to
# spare. The archive from the "both files" case above is still on disk here.
before="$(find "$docker_dir" -maxdepth 1 -name 'backup-*.tar.gz' | sort)"
before_sum="$(cat "$docker_dir"/backup-*.tar.gz | cksum)"
out2="$(run_docker_update "$docker_dir" "$vol")"; st2=$?
check_refusal "a same-minute rerun is refused" "$st2" "$out2" "already exists"
after="$(find "$docker_dir" -maxdepth 1 -name 'backup-*.tar.gz' | sort)"
after_sum="$(cat "$docker_dir"/backup-*.tar.gz | cksum)"
if [ "$before" = "$after" ] && [ "$before_sum" = "$after_sum" ]; then
  ok "and the existing backup was left byte-for-byte intact"
else
  bad "the existing backup changed even though the rerun was refused"
fi

# ------------------------------------------------------ DATA_DIR validation
#
# validate_data_dir is the guard in front of `chown -R "$RUN_USER:$RUN_USER"
# "$DATA_DIR"` -- the one operator-supplied value that reaches it, via the
# "Data directory" prompt in binary mode. Each case below runs in its own
# subshell so a `die` in validate_data_dir (which exits) only ends that
# subshell, not this whole suite.

step "10. validate_data_dir refuses what would make chown -R a filesystem-wide mistake"

check_data_dir() { # check_data_dir <desc> <value> <accept|reject> [message substring]
  local desc="$1" val="$2" want="$3" substr="${4:-}" out st
  out="$( ( load_install_defs || exit 2
            validate_data_dir "$val" RESULT || exit 1
            printf '%s' "$RESULT" ) 2>&1 )"
  st=$?
  if [ "$want" = accept ]; then
    if [ "$st" -eq 0 ]; then ok "$desc (-> $out)"
    else bad "$desc: expected acceptance, got exit $st: $out"; fi
    return
  fi
  if [ "$st" -eq 0 ]; then
    bad "$desc: expected a refusal, but it was accepted (-> $out)"
    return
  fi
  case "$out" in
    *"$substr"*) ok "$desc" ;;
    *) bad "$desc: refused, but the message doesn't mention \"$substr\": $out" ;;
  esac
}

check_data_dir "an empty value is refused"                 ""                       reject "empty"
check_data_dir "a relative path is refused"                "var/lib/polyemesis"     reject "absolute"
check_data_dir "'/' is refused"                             "/"                      reject "entire filesystem"
check_data_dir "a top-level system directory is refused"    "/usr"                   reject "top-level system directory"
check_data_dir "a '..' component is refused"                "/var/lib/../../etc"     reject "component"
check_data_dir "an ordinary nested path is accepted"         "/srv/polyemesis-data"   accept

if grep -q 'validate_data_dir "\$DATA_DIR" DATA_DIR' "$INSTALL"; then
  ok "gather_configuration actually calls the guard after the Data directory prompt"
else
  bad "the Data directory prompt no longer calls validate_data_dir — the check above is now testing dead code"
fi

# --------------------------------------------------------- config preservation
#
# preserve_existing runs immediately before each `} > file` that would
# otherwise truncate a config.yaml (or docker-compose.yml) a re-run might be
# overwriting. It must never touch the file about to be overwritten, must
# snapshot what was there, and must never let a second snapshot destroy the
# first.

step "11. preserve_existing snapshots instead of silently losing an edited config"

WORK_PRESERVE="$work/preserve"; mkdir -p "$WORK_PRESERVE"
(
  load_install_defs || exit 1
  d="$WORK_PRESERVE"
  f="$d/config.yaml"
  printf 'dataDir: "/first"\n' > "$f"

  preserve_existing "$f" >/dev/null
  preserve_existing "$f" >/dev/null   # same-second rerun, on purpose

  [ -f "$f" ] && grep -q '/first' "$f" && echo ORIGINAL_OK || echo ORIGINAL_CHANGED
  n="$(find "$d" -maxdepth 1 -name 'config.yaml.bak-*' | wc -l | tr -d ' ')"
  echo "BACKUP_COUNT=$n"
  bad_backup=0
  for b in "$d"/config.yaml.bak-*; do
    grep -q '/first' "$b" || bad_backup=1
  done
  [ "$bad_backup" -eq 0 ] && echo BACKUP_CONTENT_OK || echo BACKUP_CONTENT_BAD

  np="$d/does-not-exist.yaml"
  preserve_existing "$np" && echo NOOP_OK || echo NOOP_BAD
  n2="$(find "$d" -maxdepth 1 -name 'does-not-exist.yaml.bak-*' | wc -l | tr -d ' ')"
  echo "NOOP_BACKUPS=$n2"
) > "$WORK_PRESERVE.out" 2>&1

out="$(cat "$WORK_PRESERVE.out")"
case "$out" in
  *ORIGINAL_OK*) ok "the file about to be overwritten is untouched by preserve_existing itself" ;;
  *) bad "preserve_existing modified the file it was supposed to be snapshotting: $out" ;;
esac
n="$(printf '%s\n' "$out" | sed -n 's/^BACKUP_COUNT=//p')"
if [ "${n:-0}" -ge 2 ]; then
  ok "two calls produced two snapshots, not one overwritten by the other ($n found)"
else
  bad "expected at least 2 snapshots after two calls, found ${n:-0}: $out"
fi
case "$out" in
  *BACKUP_CONTENT_OK*) ok "every snapshot holds the pre-overwrite content" ;;
  *) bad "a snapshot's content is wrong: $out" ;;
esac
case "$out" in
  *NOOP_OK*) ok "a file that does not exist yet is left alone (nothing to preserve on a first install)" ;;
  *) bad "preserve_existing failed on a nonexistent path, which install.sh always calls it with once: $out" ;;
esac
n2="$(printf '%s\n' "$out" | sed -n 's/^NOOP_BACKUPS=//p')"
[ "${n2:-1}" = 0 ] \
  && ok "and it created no backup for a file that was never there" \
  || bad "it created a backup for a file that does not exist: $out"

for site in \
  'preserve_existing "$INSTALL_DIR/config.yaml"' \
  'preserve_existing "$INSTALL_DIR/docker-compose.yml"' \
  'preserve_existing "$CONFIG_DIR/config.yaml"'
do
  if grep -qF "$site" "$INSTALL"; then
    ok "call site present: $site"
  else
    bad "missing call site, config could be silently overwritten: $site"
  fi
done

step "12. The generated uninstaller refuses before it can end a broadcast"

# EVERY PATH HERE USES TEMP DIRECTORIES. An earlier hand-test of this script ran
# the generated uninstaller with --force against the real /usr/local/bin and
# /etc/polyemesis; it happened to be a machine with no install, which is luck
# rather than a test design. A suite that can uninstall the host it runs on is
# not a suite.
WORK_UNINST="$work/uninst"; mkdir -p "$WORK_UNINST/out" "$WORK_UNINST/bin"
printf '#!/bin/sh\necho 0\n' > "$WORK_UNINST/bin/id"; chmod +x "$WORK_UNINST/bin/id"

(
  load_install_defs || exit 1
  INSTALL_DIR="$WORK_UNINST/out"
  SERVICE_NAME="polyemesis"
  BIN_PATH="$WORK_UNINST/fake-bin"
  CONFIG_DIR="$WORK_UNINST/fake-cfg"
  DATA_DIR="$WORK_UNINST/fake-data"
  write_binary_uninstall_script >/dev/null 2>&1
) || bad "could not generate the uninstaller"

U="$WORK_UNINST/out/uninstall.sh"
if [ -f "$U" ]; then
  bash -n "$U" && ok "the generated uninstaller is syntactically valid" \
                || bad "the generated uninstaller does not parse"

  # The escaping is the thing that breaks: a runtime variable expanded at
  # GENERATION time bakes one install's paths into every copy.
  if grep -q 'SERVICE_NAME="polyemesis"' "$U" && grep -q '"\$SERVICE_NAME"' "$U"; then
    ok "install-time values are baked in and runtime references survive"
  else
    bad "heredoc escaping is wrong: check \$ vs \\\$ in write_binary_uninstall_script"
  fi

  bash "$U" --wat >/dev/null 2>&1
  [ "$?" = 2 ] && ok "an unknown option is refused" || bad "an unknown option was accepted"

  # id is STUBBED to a non-zero uid rather than relying on who runs this. CI
  # runs as root, so the unstubbed version passed the root check, failed later
  # for an unrelated reason, and reported "a non-root run was not refused" --
  # a test whose answer depended on its environment rather than on the code.
  printf '#!/bin/sh\necho 1000\n' > "$WORK_UNINST/bin/id"; chmod +x "$WORK_UNINST/bin/id"
  out="$(PATH="$WORK_UNINST/bin:$PATH" bash "$U" 2>&1)"; rc=$?
  if [ "$rc" != 0 ] && printf '%s' "$out" | grep -q 'must run as root'; then
    ok "a non-root run is refused before the first mutation"
  else
    bad "a non-root run was not refused (rc=$rc)"
  fi
  printf '#!/bin/sh\necho 0\n' > "$WORK_UNINST/bin/id"; chmod +x "$WORK_UNINST/bin/id"

  # No terminal to confirm on must REFUSE, not assume. An unattended job that
  # inherits this script must not be able to uninstall a broadcast server.
  out="$(PATH="$WORK_UNINST/bin:$PATH" bash "$U" </dev/null 2>&1)"; rc=$?
  if [ "$rc" != 0 ] && printf '%s' "$out" | grep -q 'No terminal to confirm on'; then
    ok "with no terminal to confirm on, it refuses rather than assuming"
  else
    bad "a run with no terminal was not refused (rc=$rc): $out"
  fi

  # --remove-data must refuse a path that would take the system with it, and
  # must still work for a real one -- a guard that refuses everything passes
  # every negative case and is useless.
  for badpath in "" "/" "/usr" "relative/path"; do
    sed "s|^DATA_DIR=.*|DATA_DIR=\"$badpath\"|" "$U" > "$WORK_UNINST/g.sh"
    out="$(PATH="$WORK_UNINST/bin:$PATH" bash "$WORK_UNINST/g.sh" --remove-data --force 2>&1)"; rc=$?
    if [ "$rc" != 0 ] && printf '%s' "$out" | grep -qi refus; then
      ok "--remove-data refuses DATA_DIR='$badpath'"
    else
      bad "--remove-data accepted DATA_DIR='$badpath' (rc=$rc)"
    fi
  done

  # AND IS IT OURS? The three checks above prove the path is safe to TYPE.
  # DATA_DIR is frozen into the uninstaller at generation time and never
  # re-read, so an operator who moved the data directory later and repointed
  # config.yaml by hand has an uninstaller aimed at a stale path that passes all
  # three -- deletes whatever now lives there, and reports "database, secret.key
  # and recordings are gone" whether or not any of that was ever true.
  mkdir -p "$WORK_UNINST/fake-data"; : > "$WORK_UNINST/fake-data/someone-elses-files"
  out="$(PATH="$WORK_UNINST/bin:$PATH" bash "$U" --remove-data --force 2>&1)"; rc=$?
  if [ "$rc" != 0 ] && [ -e "$WORK_UNINST/fake-data/someone-elses-files" ]; then
    case "$out" in
      *"neither polyemesis.db nor"*)
        ok "--remove-data refuses a directory holding neither polyemesis.db nor secret.key" ;;
      *) bad "--remove-data refused, but not for the right reason: $out" ;;
    esac
  else
    bad "--remove-data deleted a directory with no polyemesis.db and no secret.key (rc=$rc)"
  fi

  # A guard that refuses everything passes every negative case and is useless.
  # Either marker is enough: a database with no key file, and a key file with no
  # database, are both this install's data directory.
  for marker in polyemesis.db secret.key; do
    rm -rf "$WORK_UNINST/fake-data"
    mkdir -p "$WORK_UNINST/fake-data"; : > "$WORK_UNINST/fake-data/$marker"
    out="$(PATH="$WORK_UNINST/bin:$PATH" bash "$U" --remove-data --force 2>&1)"; rc=$?
    if [ "$rc" = 0 ] && [ ! -e "$WORK_UNINST/fake-data" ]; then
      ok "--remove-data deletes a legitimate data directory (found by $marker)"
    else
      bad "--remove-data did not delete a data directory holding $marker (rc=$rc)"
    fi
  done
else
  bad "no uninstaller was generated"
fi

# ------------------------------------------------- rollback blast radius (#532)
#
# THE WORST THING THIS SCRIPT CAN DO. A re-run over a healthy install -- to
# change a port, add TLS, upgrade -- that fails at any later step (verify()
# timing out after 60s is the reachable one) used to run `rm -rf "$INSTALL_DIR"`
# on the operator's EXISTING install directory, because `mkdir -p` succeeds on a
# directory that is already there and DIRS_CREATED was set unconditionally right
# after it. That deleted docker-compose.yml, config.yaml, its just-written .bak-
# snapshot, uninstall.sh and every backup-*.tar.gz update.sh had ever written
# there. The docker volume survives; the compose file needed to bring it back
# does not. And it printed `[info] removed /opt/polyemesis`.
#
# The CONFIG_DIR half of this had already been fixed, with the reasoning
# recorded beside it. INSTALL_DIR never got the same treatment. These cases pin
# both halves: that the flag is only set for a directory this run created, and
# that the trap only deletes when the flag says so.

step "13. Rollback deletes only what this run created"

WORK_RB="$work/rollback"

# (1) The flag itself, through a REAL install.sh function rather than a copy of
#     the line. write_binary_update_script is one of the three sites that
#     `mkdir -p "$INSTALL_DIR"`, and it needs nothing but a directory.
rb_flag_for() { # rb_flag_for <install_dir>  -> prints the resulting flag
  # These are read by install.sh's write_binary_update_script, which arrives
  # through the eval in load_install_defs and is invisible to static analysis.
  # shellcheck disable=SC2034
  ( load_install_defs || exit 1
    INSTALL_DIR="$1"
    DATA_DIR="$1/data"
    BIN_PATH="$1/polyemesis"
    SERVICE_NAME="polyemesis-acceptance"
    write_binary_update_script
    printf '%s' "$INSTALL_DIR_CREATED" )
}

mkdir -p "$WORK_RB/pre-existing"
: > "$WORK_RB/pre-existing/backup-2026-01-01.tar.gz"
got="$(rb_flag_for "$WORK_RB/pre-existing")"
if [ "$got" = false ]; then
  ok "a pre-existing install directory does not set INSTALL_DIR_CREATED"
else
  bad "INSTALL_DIR_CREATED=$got for a directory that already existed — rollback would rm -rf the operator's backups"
fi

got="$(rb_flag_for "$WORK_RB/fresh")"
if [ "$got" = true ]; then
  ok "a directory this run created does set INSTALL_DIR_CREATED"
else
  bad "INSTALL_DIR_CREATED=$got for a directory this run created — a failed first install would leave its own mess behind"
fi

# (2) The trap, driven directly. cleanup_on_failure exits, so each case runs in
#     its own subshell.
rb_trap() { # rb_trap <install_dir> <install_dir_created>
  # Read by install.sh's cleanup_on_failure, which arrives through the eval.
  # shellcheck disable=SC2034
  ( load_install_defs || exit 1
    INSTALL_DIR="$1"
    CONFIG_DIR="$WORK_RB/etc"
    DATA_DIR="$WORK_RB/data"
    COMPOSE_CMD=""
    DIRS_CREATED=true
    INSTALL_DIR_CREATED="$2"
    # errexit OFF for the two lines below. install.sh sets -e, so a bare
    # `( exit 1 )` would end this subshell right there and cleanup_on_failure
    # would never run -- which is exactly how the first draft of this case
    # "passed" while asserting nothing. In the real script the handler runs from
    # a trap, after the shell has already decided to exit.
    set +e
    ( exit 1 )
    cleanup_on_failure ) >/dev/null 2>&1
}

mkdir -p "$WORK_RB/keep"; : > "$WORK_RB/keep/backup-2026-01-01.tar.gz"
rb_trap "$WORK_RB/keep" false
if [ -e "$WORK_RB/keep/backup-2026-01-01.tar.gz" ]; then
  ok "rollback leaves an install directory it did not create — backups survive"
else
  bad "rollback deleted a pre-existing install directory and the backups in it"
fi

mkdir -p "$WORK_RB/drop"; : > "$WORK_RB/drop/docker-compose.yml"
rb_trap "$WORK_RB/drop" true
if [ ! -d "$WORK_RB/drop" ]; then
  ok "rollback still removes an install directory it did create"
else
  bad "rollback left behind an install directory this run created"
fi

# (3) The docker path takes the same guard. It cannot be driven here -- it pulls
#     an image and starts a container -- so the shape is pinned instead, which
#     is what stops the fix living on the binary path only.
if grep -q '\[ -d "\$INSTALL_DIR" \] || INSTALL_DIR_CREATED=true' "$INSTALL"; then
  # Anchored, so the prose that explains this at the top of install.sh is not
  # counted as a call site.
  n="$(grep -cE '^[[:space:]]*\[ -d "\$INSTALL_DIR" \] \|\| INSTALL_DIR_CREATED=true' "$INSTALL")"
  m="$(grep -cE '^[[:space:]]*mkdir -p "\$INSTALL_DIR"' "$INSTALL")"
  if [ "$n" = "$m" ]; then
    ok "every one of the $m \`mkdir -p \$INSTALL_DIR\` sites is guarded ($n guards)"
  else
    bad "$m \`mkdir -p \$INSTALL_DIR\` sites but only $n guards — one of them still tells rollback it may delete an existing install"
  fi
else
  bad "no INSTALL_DIR_CREATED guard in install.sh at all"
fi

# (4) And the container half: a container that was already up before this run is
#     the operator's, and `compose down` on it takes a live broadcast off air.
if grep -q 'CONTAINER_PREEXISTING" = true \] || CONTAINER_STARTED=true' "$INSTALL"; then
  ok "CONTAINER_STARTED is not set for a container that was already running"
else
  bad "CONTAINER_STARTED is set unconditionally — a failed re-run would compose down the operator's live container"
fi

# (5) THE SAME MISTAKE ON THE OTHER MODE, found by sweeping for the shape rather
#     than by another report. `install -m 0755` and `cat >` both succeed over an
#     existing file, so a failed re-run over a WORKING systemd install used to
#     disable the service, delete its unit and delete the binary -- a host with
#     no polyemesis on it at all, recovering from a failure that had broken
#     nothing.
#
#     UNIT_CREATED IS FORCED false IN BOTH CASES BELOW. cleanup_on_failure's
#     unit branch spells /etc/systemd/system/<name>.service literally, with no
#     variable to point somewhere harmless -- so driving it here would be a test
#     that reaches into the real /etc, which is what the note above section 12
#     says a suite must never do. Only the BIN_PATH half is exercised; the unit
#     half is pinned by shape, below.
rb_binary() { # rb_binary <root> <bin_preexisting>
  # Read by install.sh's cleanup_on_failure, which arrives through the eval.
  # shellcheck disable=SC2034
  ( load_install_defs || exit 1
    INSTALL_DIR="$1/opt"
    CONFIG_DIR="$1/etc"
    DATA_DIR="$1/data"
    BIN_PATH="$1/bin/polyemesis"
    COMPOSE_CMD=""
    DIRS_CREATED=false
    UNIT_CREATED=false
    BINARY_INSTALLED=true
    BIN_PREEXISTING="$2"
    set +e
    ( exit 1 )
    cleanup_on_failure ) >/dev/null 2>&1
}

rbb="$work/rb-binary"; mkdir -p "$rbb/bin"; : > "$rbb/bin/polyemesis"
rb_binary "$rbb" true
if [ -e "$rbb/bin/polyemesis" ]; then
  ok "rollback leaves a binary that predates this run — a re-run that fails does not uninstall the host"
else
  bad "rollback deleted the binary of an install it had only replaced"
fi

: > "$rbb/bin/polyemesis"
rb_binary "$rbb" false
if [ ! -e "$rbb/bin/polyemesis" ]; then
  ok "and it still removes a binary this run installed for the first time"
else
  bad "rollback left behind a binary this run had installed"
fi

if grep -q 'UNIT_CREATED" = true \] && \[ "\$UNIT_PREEXISTING" != true \]' "$INSTALL" \
   && grep -q 'UNIT_PREEXISTING=true' "$INSTALL"; then
  ok "and the unit file is under the same guard, so a failed re-run cannot disable a running service"
else
  bad "the unit removal is unguarded — a failed re-run would disable and delete a working install's service"
fi

# --------------------------------------------------- the data directory default
#
# The prompt's default used to be the compiled-in constant rather than the
# existing install's dataDir, and under --yes ask() takes the default WITHOUT
# PRINTING A PROMPT. A re-run to change a port therefore created a new data
# directory, minted a fresh secret.key in it, rewrote the unit's --data and
# restarted the service onto an empty database -- every destination, source and
# recording gone from the UI, with the summary printing "create your admin
# password" as though this were a first install.

step "14. A re-run defaults to the data directory the install is already using"

WORK_DD="$work/datadir"; mkdir -p "$WORK_DD/etc"

read_data_dir() { # read_data_dir <config_dir>
  # Read by install.sh's existing_data_dir, which arrives through the eval.
  # shellcheck disable=SC2034
  ( load_install_defs || exit 1
    CONFIG_DIR="$1"
    existing_data_dir )
}

printf 'dataDir: "/srv/polyemesis-moved"\naddr: ":8080"\n' > "$WORK_DD/etc/config.yaml"
got="$(read_data_dir "$WORK_DD/etc")"
[ "$got" = "/srv/polyemesis-moved" ] \
  && ok "a quoted dataDir is read back out of an existing config.yaml" \
  || bad "expected /srv/polyemesis-moved, got '${got:-<empty>}'"

printf 'dataDir: /srv/unquoted\n' > "$WORK_DD/etc/config.yaml"
got="$(read_data_dir "$WORK_DD/etc")"
[ "$got" = "/srv/unquoted" ] \
  && ok "an unquoted dataDir is read too" \
  || bad "expected /srv/unquoted, got '${got:-<empty>}'"

# Anything it cannot parse must read as "no previous install", never as a guess.
printf 'dataDir: relative/path\n' > "$WORK_DD/etc/config.yaml"
got="$(read_data_dir "$WORK_DD/etc")"
[ -z "$got" ] \
  && ok "a non-absolute dataDir is reported as no previous install rather than guessed at" \
  || bad "a relative dataDir was accepted as '$got'"

got="$(read_data_dir "$WORK_DD/nothing-here")"
[ -z "$got" ] \
  && ok "no config.yaml means no previous install, and the constant default stands" \
  || bad "invented a data directory from a config that does not exist: '$got'"

if grep -q 'prior_data_dir="\$(existing_data_dir)"' "$INSTALL" \
   && grep -q 'refusing under --yes' "$INSTALL"; then
  ok "gather_configuration uses it as the default and refuses to move the data under --yes"
else
  bad "the Data directory prompt no longer consults existing_data_dir — the checks above test dead code"
fi

# ------------------------------------------------------------- port validation
#
# RTMP_PORT was the one port that skipped the numeric/range check, so
# `--rtmp-port 70000` reached docker-compose.yml as a port mapping and failed at
# `compose up` -- inside the install, which then ran the rollback above.

step "15. Every port the installer accepts is validated, not just two of them"

# THE MESSAGE, NOT JUST THE EXIT CODE. A non-zero exit proves nothing here:
# install.sh refuses to run as a non-root user a few lines later, so every one
# of these cases "failed" for that reason instead and the first draft of this
# section passed with the validation removed entirely. The refusal has to name
# the variable it refused.
check_port_arg() { # check_port_arg <desc> <expected-substring> <args...>
  local desc="$1" want="$2"; shift 2
  local out
  out="$(bash "$INSTALL" "$@" 2>&1)"
  case "$out" in
    *"$want"*) ok "$desc" ;;
    *) bad "$desc: nothing said \"$want\". install.sh got as far as $(printf '%s' "$out" | head -1)" ;;
  esac
}

check_port_arg "--http-port 70000 is refused" "HTTP_PORT must be between" --http-port 70000 --check
check_port_arg "--srt-port 0 is refused"      "SRT_PORT must be between"  --srt-port 0 --check
check_port_arg "--rtmp-port 70000 is refused" "RTMP_PORT must be between" --rtmp-port 70000 --check
check_port_arg "--rtmp-port abc is refused"   "RTMP_PORT must be a number" --rtmp-port abc --check
check_port_arg "--rtmp-port -1 is refused"    "RTMP_PORT must be a number" --rtmp-port -1 --check

# 0 is not a port here, it is how you decline RTMP -- see the ENABLE_RTMP case,
# which reads the port as the switch. It must survive the range check.
out="$(bash "$INSTALL" --rtmp-port 0 --check 2>&1)"; rc=$?
case "$out" in
  *"RTMP_PORT must be"*) bad "--rtmp-port 0 was rejected by the range check, but 0 is how you decline RTMP" ;;
  *) ok "--rtmp-port 0 still means 'decline RTMP' rather than failing the range check (rc=$rc)" ;;
esac

# ------------------------------------------- generated unit vs the shipped unit
#
# deploy/polyemesis.service is the hand-install this project documents; the unit
# install.sh generates is what the RECOMMENDED path actually creates. They
# drifted -- the generated one carried no UMask and none of the hardening from
# ProtectKernelTunables down -- and neither file looks wrong on its own, which
# is why it lasted. install.sh is fetched standalone with curl and has no
# repository to read, so it cannot be generated from that file; this is the
# guard instead.

step "16. The generated systemd unit is not weaker than the one the docs ship"

SHIPPED="$SCRIPTS/../deploy/polyemesis.service"
if [ ! -f "$SHIPPED" ]; then
  bad "deploy/polyemesis.service is missing — this check cannot run"
else
  missing=""
  while read -r directive; do
    [ -n "$directive" ] || continue
    grep -q "^${directive}=" "$INSTALL" || missing="$missing $directive"
  done <<EOF
$(sed -n '/^\[Service\]/,/^\[Install\]/p' "$SHIPPED" \
   | sed -n 's/^\([A-Za-z][A-Za-z0-9]*\)=.*/\1/p' \
   | grep -vE '^(ExecStart|User|Group|ReadWritePaths)$' \
   | sort -u)
EOF
  if [ -z "$missing" ]; then
    ok "every [Service] directive in deploy/polyemesis.service appears in the generated unit"
  else
    bad "the generated unit is missing:$missing — add them to install.sh's heredoc, or the installer keeps producing a weaker service than the copy-paste instructions"
  fi

  grep -q 'chmod 0750 "\$DATA_DIR"' "$INSTALL" \
    && ok "the installer chmods the data directory 0750, as the shipped unit's header calls for" \
    || bad "the data directory is left at mkdir's 0755 — it holds secret.key and the recordings (#297)"
fi

step "17. The docker update.sh refuses to upgrade a broadcast that is on air"
#
# uninstall.sh has asked this question since it was written; update.sh did not,
# and update.sh stops the container TWICE -- once to take a consistent archive,
# once to swap the image. So the operation that sounds safe was the one that
# would cut a live show mid-sentence, while the one that sounds dangerous
# asked first. A completed broadcast cannot be returned to.

onair_dir="$work/onair"; mkdir -p "$onair_dir"
gen_docker_update "$onair_dir" "$stub/compose"
onair_vol="$work/vol-onair"; mkdir -p "$onair_vol"
printf 'SQLite format 3\000' > "$onair_vol/polyemesis.db"
printf 'key\n'               > "$onair_vol/secret.key"

out="$(STUB_ON_AIR=true run_docker_update "$onair_dir" "$onair_vol")"; st=$?
check_refusal "an upgrade while ffmpeg is publishing is refused" "$st" "$out" "publishing right now"
case "$out" in
  *"[stub compose] stop"*)
    bad "it refused AFTER stopping the container — the broadcast was already over" ;;
  *) ok "and it refused BEFORE the stop, so the stream was still running when it gave up" ;;
esac
if [ "$(find "$onair_dir" -maxdepth 1 -name 'backup-*.tar.gz' | wc -l | tr -d ' ')" = 0 ]; then
  ok "and it wrote no archive, so a later same-minute retry is not blocked by its own leftovers"
else
  bad "the refused run still left a backup archive behind"
fi

out="$(STUB_ON_AIR=true run_docker_update "$onair_dir" "$onair_vol" --force)"; st=$?
if [ "$st" -eq 0 ]; then
  ok "--force goes ahead anyway, so the refusal is a guard and not a wall"
else
  bad "--force was still refused (exit $st)"
  printf '        %s\n' "$(printf '%s' "$out" | tr '\n' ' ')"
fi
case "$out" in
  *"[stub compose] pull"*) ok "and the forced run reaches the pull" ;;
  *) bad "--force never reached \`compose pull\`" ;;
esac

# Nothing on air: the guard must be invisible.
out="$(run_docker_update "$onair_dir" "$onair_vol" 2>&1)"; st=$?
case "$out" in
  *"publishing right now"*) bad "the guard refused an idle install — every upgrade now needs --force" ;;
  *) ok "an idle install is not refused (the guard is not a blanket stop)" ;;
esac

step "18. The generated helpers carry the compose command as DATA, not spliced into command position"
#
# The heredocs are unquoted, so \$COMPOSE_CMD written bare inside them was
# expanded while the file was being WRITTEN. With an empty value the generated
# update.sh contained the bare lines `pull` and `up -d`, and uninstall.sh
# contained `down --remove-orphans`: `pull: command not found`, on the upgrade
# path, after the container had already been stopped. #658.

empty_dir="$work/nocompose"; mkdir -p "$empty_dir"
gen_docker_update "$empty_dir" ""
for f in update.sh uninstall.sh; do
  if grep -qE '^COMPOSE_CMD="' "$empty_dir/$f"; then
    ok "$f assigns COMPOSE_CMD once, at the top, as a value"
  else
    bad "$f has no COMPOSE_CMD assignment — the value is being spliced in at generation time again"
  fi
done
if grep -qE '^[[:space:]]*(pull|up -d|down --remove-orphans|stop|start)([[:space:]]|$)' "$empty_dir/update.sh" "$empty_dir/uninstall.sh"; then
  bad "a generated helper holds a bare compose subcommand in command position:"
  grep -nE '^[[:space:]]*(pull|up -d|down --remove-orphans|stop|start)([[:space:]]|$)' "$empty_dir/update.sh" "$empty_dir/uninstall.sh" \
    | sed 's/^/        /'
else
  ok 'no bare `pull` / `up -d` / `down --remove-orphans` line in either helper'
fi
bash -n "$empty_dir/update.sh" && bash -n "$empty_dir/uninstall.sh" \
  && ok "both helpers still parse when the compose command is empty" \
  || bad "an empty compose command produces a helper with a syntax error"
out="$(bash "$empty_dir/update.sh" 2>&1)"; st=$?
check_refusal "an update.sh generated without a compose command refuses to run" "$st" "$out" "without a compose command"
out="$(bash "$empty_dir/uninstall.sh" --force 2>&1)"; st=$?
check_refusal "and so does the uninstall.sh" "$st" "$out" "without a compose command"

step "19. The generated compose file caps the container's logs"
#
# Docker's default json-file driver keeps every line forever. polyemesis logs
# per request and per encoder tick, and the install that never restarts is the
# 24/7 broadcast box, so the log only grows until the disk is full -- at which
# point SQLite stops writing and recordings stop finalising, and none of it
# looks like a logging problem. journald caps the systemd path already; only
# docker was unbounded. Checked against install.sh's source because writing the
# compose file means running install_docker_mode, which pulls and starts.
compose_block="$(sed -n "/printf 'services:/,/} > \"\$INSTALL_DIR\/docker-compose.yml\"/p" "$INSTALL")"
if printf '%s' "$compose_block" | grep -q "logging:"; then
  ok "the compose service declares a logging section"
else
  bad "the generated compose file sets no logging options — container logs grow until the disk fills"
fi
for opt in 'max-size' 'max-file'; do
  printf '%s' "$compose_block" | grep -q "$opt" \
    && ok "and it bounds $opt" \
    || bad "the logging section does not set $opt, so it still has no ceiling"
done

step "20. A re-run does not read the install's own ports as taken"
#
# Re-running install.sh over a working install is the documented way to change
# TLS or a port, and every port it asked about was already held -- by the very
# service it was about to restart. warn_if_taken saw a listener, called it a
# collision, and under --yes accepted its own offer: the web UI moved 8080 ->
# 8081 and the summary advertised an SRT port nothing listened on. Measured on a
# hand install adopted with `install.sh --mode binary --yes`.
#
# `ss` and `docker` are stubbed with the shapes the real tools print, so what is
# under test is the installer's reading of them. The real `ss -p` output was
# checked against a process named polyemesis in an ubuntu container.
ports_stub="$work/ports-bin"; mkdir -p "$ports_stub"
cat > "$ports_stub/ss" <<'SSSTUB'
#!/usr/bin/env bash
# Answers from fixture files. Without -p, ss prints no Process column, so the
# users:(...) tail is stripped exactly as the real tool would omit it.
flags="${1:-}"
case "$flags" in *u*) f="${STUB_SS_UDP:-/dev/null}" ;; *) f="${STUB_SS_TCP:-/dev/null}" ;; esac
case "$flags" in *p*) cat "$f" ;; *) sed 's/ *users:.*$//' "$f" ;; esac
SSSTUB
cat > "$ports_stub/docker" <<'DOCKERSTUB'
#!/usr/bin/env bash
# Only `docker port polyemesis`, which is how the installer asks whether a
# published port is its own container's.
if [ "${1:-}" = port ] && [ "${2:-}" = polyemesis ]; then
  [ -n "${STUB_DOCKER_PORTS:-}" ] || { echo "Error: No such container: polyemesis" >&2; exit 1; }
  printf '%s\n' "$STUB_DOCKER_PORTS"
  exit 0
fi
exit 1
DOCKERSTUB
chmod +x "$ports_stub/ss" "$ports_stub/docker"

ss_hdr='State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process'
printf '%s\n%s\n%s\n' "$ss_hdr" \
  'LISTEN 0      4096   *:8080            *:*    users:(("polyemesis",pid=4242,fd=9))' \
  'LISTEN 0      4096   *:1935            *:*    users:(("polyemesis",pid=4242,fd=11))' > "$work/ss-tcp-ours"
printf '%s\n%s\n' "$ss_hdr" \
  'UNCONN 0      0      *:6000            *:*    users:(("polyemesis",pid=4242,fd=10))' > "$work/ss-udp-ours"
printf '%s\n%s\n' "$ss_hdr" \
  'LISTEN 0      511    0.0.0.0:8080      0.0.0.0:*    users:(("nginx",pid=77,fd=6))' > "$work/ss-tcp-nginx"
printf '%s\n%s\n' "$ss_hdr" \
  'LISTEN 0      4096   0.0.0.0:8080      0.0.0.0:*    users:(("docker-proxy",pid=90,fd=4))' > "$work/ss-tcp-proxy"
# A listener on some OTHER port whose address happens to end in .8080-ish
# digits must not count: 10.0.0.80 is an address, not port 80.
printf '%s\n%s\n' "$ss_hdr" \
  'LISTEN 0      4096   10.0.0.80:5000    0.0.0.0:*    users:(("sshd",pid=5,fd=3))' > "$work/ss-tcp-addr80"

port_after() { # port_after <mode> <port> <proto> -> the port warn_if_taken leaves behind
  # Read by install.sh's warn_if_taken, which arrives through the eval.
  # shellcheck disable=SC2034
  ( load_install_defs || exit 1
    MODE="$1"
    ASSUME_YES=true
    p="$2"
    warn_if_taken "$2" "$3" "probe" p >/dev/null 2>&1
    printf '%s' "$p" )
}

got="$(STUB_SS_TCP="$work/ss-tcp-ours" PATH="$ports_stub:$PATH" port_after binary 8080 tcp)"
[ "$got" = 8080 ] \
  && ok "binary re-run: tcp/8080 held by the running polyemesis stays 8080" \
  || bad "binary re-run: the web UI was moved to $got because polyemesis itself holds 8080"
got="$(STUB_SS_UDP="$work/ss-udp-ours" PATH="$ports_stub:$PATH" port_after binary 6000 udp)"
[ "$got" = 6000 ] \
  && ok "binary re-run: udp/6000 held by the running polyemesis stays 6000" \
  || bad "binary re-run: SRT was moved to $got because polyemesis itself holds 6000"
got="$(STUB_SS_TCP="$work/ss-tcp-proxy" STUB_DOCKER_PORTS=$'8080/tcp -> 0.0.0.0:8080\n6000/udp -> 0.0.0.0:6000' \
  PATH="$ports_stub:$PATH" port_after docker 8080 tcp)"
[ "$got" = 8080 ] \
  && ok "docker re-run: tcp/8080 published by the polyemesis container stays 8080" \
  || bad "docker re-run: the web UI was moved to $got because the polyemesis container publishes 8080"

# The guard still has to guard. Somebody else's listener is a real collision.
got="$(STUB_SS_TCP="$work/ss-tcp-nginx" PATH="$ports_stub:$PATH" port_after binary 8080 tcp)"
[ "$got" = 8081 ] \
  && ok "a port held by another program (nginx) is still reported and moved" \
  || bad "a port held by nginx was left at $got -- the collision check stopped working"
got="$(STUB_SS_TCP="$work/ss-tcp-proxy" STUB_DOCKER_PORTS='' PATH="$ports_stub:$PATH" port_after docker 8080 tcp)"
[ "$got" = 8081 ] \
  && ok "a docker-proxy that is NOT the polyemesis container is still a collision" \
  || bad "any docker-proxy on the port was taken for ours (left at $got)"
got="$(STUB_SS_TCP="$work/ss-tcp-ours" PATH="$ports_stub:$PATH" port_after docker 8080 tcp)"
[ "$got" = 8081 ] \
  && ok "in docker mode a bare polyemesis PROCESS still collides -- the container cannot bind over it" \
  || bad "docker mode treated a host polyemesis process as its own container (left at $got)"
got="$(STUB_SS_TCP="$work/ss-tcp-addr80" PATH="$ports_stub:$PATH" port_after binary 80 tcp)"
[ "$got" = 80 ] \
  && ok "an address ending in .80 is not read as port 80" \
  || bad "10.0.0.80:5000 was read as a listener on port 80 (moved to $got)"

step "21. SRT and RTMP ports only go where the server will actually listen"
#
# The server's SRT and RTMP listeners are runtime settings (Settings ->
# Listeners, stored in the database, 6000 and 1935 on a new install). Nothing
# install.sh writes reaches them. In binary mode --srt-port/--rtmp-port changed
# only the firewall rule and the printed address; in docker mode they published
# host N to container N, where nothing listens either.
out="$(bash "$INSTALL" --mode binary --srt-port 6001 --check 2>&1)"
case "$out" in
  *"--srt-port"*"Listeners"*) ok "binary mode refuses --srt-port 6001 and says where the port is really set" ;;
  *) bad "binary mode accepted --srt-port 6001, which the server never reads"
     printf '        got: %s\n' "$(printf '%s' "$out" | head -3 | tr '\n' ' ')" ;;
esac
out="$(bash "$INSTALL" --mode binary --rtmp-port 1936 --check 2>&1)"
case "$out" in
  *"--rtmp-port"*"Listeners"*) ok "binary mode refuses --rtmp-port 1936 the same way" ;;
  *) bad "binary mode accepted --rtmp-port 1936, which the server never reads" ;;
esac
out="$(bash "$INSTALL" --mode binary --srt-port 6000 --rtmp-port 0 --check 2>&1)"
case "$out" in
  *"Listeners"*) bad "binary mode refused the defaults (or --rtmp-port 0), which ARE what the server binds" ;;
  *) ok "binary mode still accepts the default ports and --rtmp-port 0" ;;
esac
out="$(bash "$INSTALL" --mode docker --srt-port 6001 --check 2>&1)"
case "$out" in
  *"Listeners"*) bad "docker mode refused --srt-port, which it can honour by mapping the host port" ;;
  *) ok "docker mode accepts --srt-port 6001" ;;
esac

# And docker mode honours it by publishing the chosen HOST port onto the port
# the server binds INSIDE the container. Generated for real, with compose and
# docker stubbed so nothing is pulled or started.
compose_dir="$work/compose-ports"
# Read by install.sh's install_docker_mode, which arrives through the eval.
# shellcheck disable=SC2034
( load_install_defs || exit 1
  INSTALL_DIR="$compose_dir"; MODE=docker; TLS_MODE=off
  SRT_PORT=6001; RTMP_PORT=1936; ENABLE_RTMP=yes; COMPOSE_CMD=true
  PATH="$ports_stub:$PATH" install_docker_mode >/dev/null 2>&1 )
if grep -q '"6001:6000/udp"' "$compose_dir/docker-compose.yml" 2>/dev/null; then
  ok "docker: --srt-port 6001 publishes host udp/6001 onto the server's udp/6000"
else
  bad "docker: --srt-port 6001 is not mapped onto the server's udp/6000: $(grep -h '/udp' "$compose_dir/docker-compose.yml" 2>/dev/null | tr -d ' ')"
fi
if grep -q '"1936:1935"' "$compose_dir/docker-compose.yml" 2>/dev/null; then
  ok "docker: --rtmp-port 1936 publishes host tcp/1936 onto the server's tcp/1935"
else
  bad "docker: --rtmp-port 1936 is not mapped onto the server's tcp/1935"
fi

# The two numbers install.sh maps onto are the server's defaults. Pinned against
# the Go source so the day the default moves, this fails instead of the ingest.
srv_srt="$(sed -n 's/^SERVER_SRT_PORT=\([0-9]*\).*/\1/p' "$INSTALL")"
srv_rtmp="$(sed -n 's/^SERVER_RTMP_PORT=\([0-9]*\).*/\1/p' "$INSTALL")"
if grep -q "ListenerSettings{SRTPort: ${srv_srt:-x}, RTMPPort: ${srv_rtmp:-x}}" "$SCRIPTS/../internal/db/settings.go"; then
  ok "install.sh's SERVER_SRT_PORT/SERVER_RTMP_PORT ($srv_srt/$srv_rtmp) match the server's listener defaults"
else
  bad "install.sh's server listener ports (${srv_srt:-unset}/${srv_rtmp:-unset}) do not match internal/db/settings.go's defaults"
fi

step "22. The binary update.sh restarts what it stopped, and its way back keeps the recordings"
#
# Every refusal after `systemctl stop` used to exit under set -e with the
# service down. And the rollback it printed was
#   sudo rm -rf $DATA_DIR && sudo cp -a $dest $DATA_DIR
# which deleted every recording made since the upgrade and copied the root-owned
# polyemesis.previous into the live directory.
#
# systemctl and sudo are stubbed: systemctl records what it was asked to do,
# and sudo runs its command, so the suite drives the script's reaction and not
# systemd.
sysd_stub="$work/sysd-bin"; mkdir -p "$sysd_stub"
cat > "$sysd_stub/systemctl" <<'SYSTEMCTLSTUB'
#!/usr/bin/env bash
# STUB_DIES_AFTER_START: active until something starts it, then not -- a new
# binary that exits as soon as it has read its config.
case "${1:-}" in
  is-active)
    if [ "${STUB_DIES_AFTER_START:-false}" = true ] && grep -qx start "$STUB_SYSTEMCTL_LOG" 2>/dev/null; then exit 3; fi
    [ "${STUB_ACTIVE:-false}" = true ]; exit $? ;;
  start) echo start >> "$STUB_SYSTEMCTL_LOG"; [ "${STUB_START_FAILS:-false}" != true ]; exit $? ;;
  *) echo "$1" >> "$STUB_SYSTEMCTL_LOG"; exit 0 ;;
esac
SYSTEMCTLSTUB
printf '#!/usr/bin/env bash\nexec "$@"\n' > "$sysd_stub/sudo"
chmod +x "$sysd_stub/systemctl" "$sysd_stub/sudo"

run_sysd() { # run_sysd <log> <cmd...> -- with the service reported active
  local log="$1"; shift
  : > "$log"
  STUB_ACTIVE=true STUB_SYSTEMCTL_LOG="$log" PATH="$sysd_stub:$PATH" "$@" 2>&1
}

# (a) A backup that will not open, refused after the stop.
root="$work/sysd-refused"; mkdir -p "$root/data"
printf 'key\n' > "$root/data/secret.key"
printf 'this is not a database\n' > "$root/data/polyemesis.db"
gen_binary_update "$root/opt" "$root/data"
out="$(run_sysd "$root/log" bash "$root/opt/update.sh")"; st=$?
log="$(tr '\n' ' ' < "$root/log")"
if [ "$st" -ne 0 ] && [ "$log" = "stop start " ]; then
  ok "a refusal after the stop starts the service again (systemctl: $log)"
else
  bad "a refusal after the stop left the service down (exit $st, systemctl: ${log:-nothing})"
fi
case "$out" in
  *"running again"*) ok "and it says the service is running again and nothing was upgraded" ;;
  *) bad "the refusal does not tell the operator what state the service is in" ;;
esac
[ "$(backups_under "$root")" = 0 ] \
  && ok "and the unverified copy it took is removed" \
  || bad "a refused run left its unverified copy holding the disk"

# (b) The same, when the start fails too: the one thing it must do is say so.
root="$work/sysd-nostart"; mkdir -p "$root/data"
printf 'key\n' > "$root/data/secret.key"
printf 'this is not a database\n' > "$root/data/polyemesis.db"
gen_binary_update "$root/opt" "$root/data"
out="$(STUB_START_FAILS=true run_sysd "$root/log" bash "$root/opt/update.sh")"
case "$out" in
  *"IS STOPPED"*"systemctl start"*) ok "if the start fails too, it says STOPPED and prints the command" ;;
  *) bad "a failed restart is not announced: $(printf '%s' "$out" | tail -3 | tr '\n' ' ')" ;;
esac

# (c) The happy path. It leaves the service stopped ON PURPOSE -- the operator
#     replaces the binary next -- and points at rollback.sh, not at rm -rf.
root="$work/sysd-happy"; data="$root/data"; mkdir -p "$data/recordings" "$data/tls"
printf 'SQLite format 3\000old' > "$data/polyemesis.db"
printf 'key\n'    > "$data/secret.key"
printf 'ca\n'     > "$data/tls/ca.key"
printf 'before\n' > "$data/recordings/before.mp4"
touch -t 202001010000 "$data/recordings/before.mp4"
gen_binary_update "$root/opt" "$data"
out="$(run_sysd "$root/log" bash "$root/opt/update.sh")"; st=$?
log="$(tr '\n' ' ' < "$root/log")"
[ "$st" -eq 0 ] && [ "$log" = "stop " ] \
  && ok "a verified backup leaves the service stopped for the binary swap, as it says" \
  || bad "the happy path ended with exit $st and systemctl: ${log:-nothing}"
case "$out" in
  *"rm -rf"*) bad "update.sh still prints an rm -rf of the data directory as the way back" ;;
  *"$root/opt/rollback.sh "*) ok "the printed way back is rollback.sh, not an rm -rf of the data directory" ;;
  *) bad "update.sh no longer prints a way back at all" ;;
esac
dest="$(printf '%s\n' "$out" | sed -n 's/^backing up .* to //p' | head -1)"

# (d) The upgrade "happens": the database changes, a WAL appears, a recording is
#     made, the binary is replaced. Then roll back.
if [ -x "$root/opt/rollback.sh" ] && [ -n "$dest" ]; then
  printf 'SQLite format 3\000new' > "$data/polyemesis.db"
  printf 'newer log\n' > "$data/polyemesis.db-wal"
  printf 'after\n' > "$data/recordings/after.mp4"
  printf 'new binary\n' > "$root/opt/polyemesis"
  out="$(run_sysd "$root/log" bash "$root/opt/rollback.sh" "$dest")"; st=$?
  [ "$st" -eq 0 ] && ok "rollback.sh runs against the backup update.sh took" \
    || bad "rollback.sh failed (exit $st): $(printf '%s' "$out" | tail -3 | tr '\n' ' ')"
  [ -f "$data/recordings/after.mp4" ] \
    && ok "a recording made since the upgrade survives the rollback" \
    || bad "the rollback deleted a recording made since the upgrade"
  [ -f "$data/recordings/before.mp4" ] \
    && ok "and the older recording is still there" \
    || bad "the rollback lost a recording from before the upgrade"
  cmp -s "$data/polyemesis.db" "$dest/polyemesis.db" \
    && ok "the database is the backup's" \
    || bad "the database was not restored from the backup"
  [ ! -e "$data/polyemesis.db-wal" ] \
    && ok "and the newer database's -wal is gone, so it cannot be replayed into the older file" \
    || bad "the newer -wal was left beside the restored database"
  [ ! -e "$data/polyemesis.previous" ] \
    && ok "polyemesis.previous is not copied into the live data directory" \
    || bad "polyemesis.previous landed in the live data directory, and every later backup carries it"
  grep -q 'stub polyemesis' "$root/opt/polyemesis" 2>/dev/null \
    && ok "the previous binary is back at BIN_PATH" \
    || bad "BIN_PATH still holds the upgraded binary"
  case "$(tr '\n' ' ' < "$root/log")" in
    *start*) ok "and the service is started again" ;;
    *) bad "rollback.sh did not start the service" ;;
  esac
  case "$out" in
    *"1 file(s) written since the backup"*) ok "it says how many recordings it kept that the old database does not list" ;;
    *) bad "rollback.sh does not report the media it kept: $(printf '%s' "$out" | tr '\n' ' ')" ;;
  esac
  out="$(run_sysd "$root/log" bash "$root/opt/rollback.sh" "$data")"; st=$?
  case "$st:$out" in
    0:*) bad "rollback.sh restored from the LIVE data directory" ;;
    *"not a backup"*) ok "rollback.sh refuses a directory that is not one of update.sh's backups" ;;
    *) bad "rollback.sh refused the live directory without saying why" ;;
  esac
else
  bad "no rollback.sh was written beside update.sh (or no backup path was reported)"
fi

step "23. A docker re-run keeps the listener the operator moved, and says where it lives"
#
# Section 21 maps host N onto the server's DEFAULT 6000/1935. But an install
# made before that fix published "7000:7000/udp", and the way an operator made
# that work was to move the listener to 7000 under Settings -> Listeners.
# Re-running install.sh with the same --srt-port then rewrote the mapping to
# 7000:6000 -- onto a port the server had stopped listening on -- and the
# ingest went dark. The container side of an existing file is the only record
# this script can read of where the listener is now, so a re-run keeps it.
rerun_compose() { # rerun_compose <dir> <srt> <rtmp> <existing compose body>
  mkdir -p "$1"
  printf '%s\n' "$4" > "$1/docker-compose.yml"
  # Read by install.sh's install_docker_mode, which arrives through the eval.
  # shellcheck disable=SC2034
  ( load_install_defs || exit 1
    INSTALL_DIR="$1"; MODE=docker; TLS_MODE=off
    SRT_PORT="$2"; RTMP_PORT="$3"; ENABLE_RTMP=yes; COMPOSE_CMD=true
    PATH="$ports_stub:$PATH" install_docker_mode >/dev/null 2>&1 )
}
old_compose='services:
  polyemesis:
    image: ghcr.io/rainmanjam/polyemesis:latest
    ports:
      - "8080:8080"
      - "7000:7000/udp"
      - "1936:1936"
      - "80:80"'
rerun_compose "$work/rerun-old" 7000 1936 "$old_compose"
if grep -q '"7000:7000/udp"' "$work/rerun-old/docker-compose.yml" 2>/dev/null; then
  ok "a re-run keeps an existing file's SRT container side (7000:7000/udp stays)"
else
  bad "a re-run rewrote the SRT mapping away from the listener the operator moved: $(grep -h '/udp' "$work/rerun-old/docker-compose.yml" 2>/dev/null | tr -d ' ')"
fi
if grep -q '"1936:1936"' "$work/rerun-old/docker-compose.yml" 2>/dev/null; then
  ok "and its RTMP container side (1936:1936 stays)"
else
  bad "a re-run rewrote the RTMP mapping: $(grep -hE '"[0-9]+:[0-9]+"' "$work/rerun-old/docker-compose.yml" 2>/dev/null | tr -d ' ' | tr '\n' ' ')"
fi
# A changed HOST port still lands on the kept container side.
new_compose='services:
  polyemesis:
    ports:
      - "8080:8080"
      - "6001:6000/udp"
      - "1936:1935"'
rerun_compose "$work/rerun-new" 6002 1937 "$new_compose"
if grep -q '"6002:6000/udp"' "$work/rerun-new/docker-compose.yml" 2>/dev/null \
   && grep -q '"1937:1935"' "$work/rerun-new/docker-compose.yml" 2>/dev/null; then
  ok "a new host port on a re-run lands on the container side the file already had"
else
  bad "a re-run with new host ports did not keep the container side: $(grep -hE '"[0-9]+:[0-9]+(/udp)?"' "$work/rerun-new/docker-compose.yml" 2>/dev/null | tr -d ' ' | tr '\n' ' ')"
fi
# A file this script cannot read a mapping from falls back to the defaults.
rerun_compose "$work/rerun-junk" 6001 1936 'services: {}'
if grep -q '"6001:6000/udp"' "$work/rerun-junk/docker-compose.yml" 2>/dev/null; then
  ok "an existing file with no SRT mapping falls back to the server's default 6000"
else
  bad "an unreadable existing file did not fall back to the default container side"
fi

# And the summary says where the listener lives in docker mode too, and that
# moving it means editing the container side of the mapping. It used to print
# that caveat only in binary mode.
# Read by install.sh's print_summary, which arrives through the eval.
# shellcheck disable=SC2034
out="$( ( load_install_defs || exit 1
  MODE=docker; TLS_MODE=off; INSTALL_DIR=/opt/polyemesis; COMPOSE_CMD="docker compose"
  print_summary ) 2>&1 )"
case "$out" in
  *"Settings -> Listeners"*"docker-compose.yml"*) ok "the docker summary says a moved listener needs the compose mapping's container side changed" ;;
  *) bad "the docker summary does not say that Settings -> Listeners and docker-compose.yml must agree" ;;
esac

step "24. A docker update that fails after the pull does not claim nothing was upgraded"
#
# on_exit's "running again on the image it had. Nothing was upgraded." is true
# up to the pull. After it, `up -d` may already have recreated the container on
# the new image before failing, and `compose start` then starts THAT -- which
# migrates the database forward. Saying the opposite is the one message an
# operator must not be handed at that moment.
cat > "$stub/compose-upfails" <<'COMPOSESTUB'
#!/usr/bin/env bash
set -u
[ "${1:-}" = top ] && exit 0
echo "[stub compose] $*"
[ "${1:-}" = up ] && { echo "stub compose: up failed" >&2; exit 1; }
exit 0
COMPOSESTUB
chmod +x "$stub/compose-upfails"
upfail_dir="$work/docker-upfails"; mkdir -p "$upfail_dir"
gen_docker_update "$upfail_dir" "$stub/compose-upfails"
out="$(run_docker_update "$upfail_dir" "$work/vol-ok")"; st=$?
[ "$st" -ne 0 ] && ok "a failed \`up -d\` fails the update" || bad "a failed \`up -d\` exited 0"
case "$out" in
  *"Nothing was upgraded"*) bad "a failure AFTER the pull still says nothing was upgraded" ;;
  *"after pulling"*) ok "it says the failure came after the pull, not that nothing changed" ;;
  *) bad "a failure after the pull says nothing about where it stopped" ;;
esac
case "$out" in
  *"backup-"*".tar.gz"*) ok "and it names the verified archive as the way back" ;;
  *) bad "a failure after the pull does not name the verified archive" ;;
esac

step "25. update.sh --binary installs only the release it was named as, and only on this host"
#
# update.sh used to end by printing `sudo install -m 0755 ./polyemesis ...` and
# nothing checked what ./polyemesis was. A wrong-architecture build, a
# VERSION=dev build or a half download all install cleanly and then crash-loop
# under Restart=on-failure, with the old binary gone. --binary answers three
# questions BEFORE the service is stopped -- sha256 against SHA256SUMS for this
# host's asset, runs here, -version prints the tag -- then installs and starts.
# --sums keeps the suite off the network; the download path is the same awk.
case "$(uname -m)" in
  x86_64|amd64) host_arch=amd64; other_arch=arm64 ;;
  *)            host_arch=arm64; other_arch=amd64 ;;
esac
tag=v9.9.9-rc.1
asset="polyemesis-${tag}-linux-${host_arch}"

bin_setup() { # bin_setup <name> <version line the new binary prints> -> sets root, data, newbin
  root="$work/bin-$1"; data="$root/data"; mkdir -p "$data" "$root/new"
  printf 'SQLite format 3\000db' > "$data/polyemesis.db"
  printf 'key\n' > "$data/secret.key"
  gen_binary_update "$root/opt" "$data"
  newbin="$root/new/polyemesis"
  printf '#!/usr/bin/env bash\n[ "${1:-}" = -version ] && { echo "%s"; exit 0; }\nexit 1\n' "$2" > "$newbin"
  chmod +x "$newbin"
}
sum_of() { sha256sum "$1" | awk '{print $1}'; }
untouched() { # untouched <label> -- the service was never stopped, nothing was copied or replaced
  local log; log="$(tr '\n' ' ' < "$root/log")"
  if [ -z "$log" ] && [ "$(backups_under "$root")" = 0 ] && grep -q 'stub polyemesis' "$root/opt/polyemesis"; then
    ok "$1: refused before the stop -- no systemctl call, no backup, BIN_PATH unchanged"
  else
    bad "$1: the refusal came too late (systemctl: ${log:-nothing}, backups: $(backups_under "$root"))"
  fi
}

# (a) --binary alone: nothing to check the file against.
bin_setup alone "polyemesis $tag"
out="$(SETTLE_SECONDS=0 run_sysd "$root/log" bash "$root/opt/update.sh" --binary "$newbin")"; st=$?
check_refusal "--binary without --version is refused" "$st" "$out" "go together"
untouched "--binary without --version"

# (b) A tag that is not a tag.
out="$(SETTLE_SECONDS=0 run_sysd "$root/log" bash "$root/opt/update.sh" --binary "$newbin" --version latest)"; st=$?
check_refusal "--version latest is refused" "$st" "$out" "must look like"

# (c) The right release for the OTHER architecture: SHA256SUMS vouches for the
#     file, but under the other asset's name.
bin_setup arch "polyemesis $tag"
printf '%s  polyemesis-%s-linux-%s\n' "$(sum_of "$newbin")" "$tag" "$other_arch" > "$root/SHA256SUMS"
out="$(SETTLE_SECONDS=0 run_sysd "$root/log" bash "$root/opt/update.sh" --binary "$newbin" --version "$tag" --sums "$root/SHA256SUMS")"; st=$?
check_refusal "a binary whose hash is published only for $other_arch is refused on $host_arch" "$st" "$out" "lists no $asset"
untouched "the wrong-architecture binary"

# (d) A hash that does not match -- another version, a partial download.
bin_setup hash "polyemesis $tag"
printf '%s  %s\n' "0000000000000000000000000000000000000000000000000000000000000000" "$asset" > "$root/SHA256SUMS"
out="$(SETTLE_SECONDS=0 run_sysd "$root/log" bash "$root/opt/update.sh" --binary "$newbin" --version "$tag" --sums "$root/SHA256SUMS")"; st=$?
check_refusal "a binary whose sha256 is not the release's is refused" "$st" "$out" "is not $asset"
untouched "the mismatched binary"

# (e) The hash matches but the binary says it is something else: the sums file
#     and the tag disagree about what this file is.
bin_setup dev "polyemesis dev"
printf '%s  %s\n' "$(sum_of "$newbin")" "$asset" > "$root/SHA256SUMS"
out="$(SETTLE_SECONDS=0 run_sysd "$root/log" bash "$root/opt/update.sh" --binary "$newbin" --version "$tag" --sums "$root/SHA256SUMS")"; st=$?
check_refusal "a binary whose -version is not the tag is refused" "$st" "$out" "-version says"
untouched "the VERSION=dev binary"

# (f) Everything agrees: back up, install, start, and say so. The file is 0644,
#     as a curl -fLO download arrives; update.sh checks and installs its own
#     0755 copy rather than reporting that the release "does not run".
bin_setup happy "polyemesis $tag"
chmod 0644 "$newbin"
printf '%s  %s\n' "$(sum_of "$newbin")" "$asset" > "$root/SHA256SUMS"
out="$(SETTLE_SECONDS=0 run_sysd "$root/log" bash "$root/opt/update.sh" --binary "$newbin" --version "$tag" --sums "$root/SHA256SUMS")"; st=$?
log="$(tr '\n' ' ' < "$root/log")"
if [ "$st" -eq 0 ] && [ "$log" = "stop start " ] && cmp -s "$newbin" "$root/opt/polyemesis"; then
  ok "a verified binary is installed at BIN_PATH and the service is started (systemctl: $log)"
else
  bad "the happy path: exit $st, systemctl: ${log:-nothing}, BIN_PATH replaced: $(cmp -s "$newbin" "$root/opt/polyemesis" && echo yes || echo no)"
  printf '        got: %s\n' "$(printf '%s' "$out" | tail -4 | tr '\n' ' ')"
fi
[ "$(backups_under "$root")" = 1 ] \
  && ok "and the verified backup is kept as the way back" \
  || bad "the happy path left $(backups_under "$root") backups, want 1"

# (g) Installed, started, and dead a moment later. The backup is now the way
#     back, so it must survive, and the message must not claim nothing changed.
bin_setup dies "polyemesis $tag"
printf '%s  %s\n' "$(sum_of "$newbin")" "$asset" > "$root/SHA256SUMS"
out="$(STUB_DIES_AFTER_START=true SETTLE_SECONDS=0 run_sysd "$root/log" bash "$root/opt/update.sh" --binary "$newbin" --version "$tag" --sums "$root/SHA256SUMS")"; st=$?
log="$(tr '\n' ' ' < "$root/log")"
case "$st:$out" in
  0:*) bad "a binary that did not stay up was reported as a successful upgrade" ;;
  *"Nothing was upgraded"*) bad "after the swap, the failure still says nothing was upgraded" ;;
  *"did not come up"*"rollback.sh"*) ok "a binary that dies after the start is reported, with rollback.sh as the way back" ;;
  *) bad "a binary that died after the start was not explained: $(printf '%s' "$out" | tail -3 | tr '\n' ' ')" ;;
esac
[ "$(backups_under "$root")" = 1 ] \
  && ok "and the verified backup is kept, not removed as unverified" \
  || bad "the backup the way back depends on was removed ($(backups_under "$root") left)"
[ "$log" = "stop start " ] \
  && ok "and the exit trap did not restart the new binary a second time" \
  || bad "systemctl after a post-swap failure: ${log:-nothing}, want 'stop start '"

step "26. install.sh --version asks for that tag by name, and never falls back to latest"
#
# releases/latest skips every prerelease, and release.yml marks each -rc tag
# one, so a staging box could not install the candidate it was built to test.
# curl is stubbed: it answers releases/tags/<tag> for one tag only, and
# releases/latest with an older one.
curl_stub="$work/curl-bin"; mkdir -p "$curl_stub"
cat > "$curl_stub/curl" <<'CURLSTUB'
#!/usr/bin/env bash
url="${*: -1}"
case "$url" in
  */releases/tags/v9.9.9-rc.1) echo '{ "tag_name": "v9.9.9-rc.1", "prerelease": true }' ;;
  */releases/latest)           echo '{ "tag_name": "v9.9.8" }' ;;
  *) echo '{"message": "Not Found"}'; exit 22 ;;
esac
CURLSTUB
chmod +x "$curl_stub/curl"
resolve() { # resolve <VERSION_PIN> -> resolve_release_tag's output and status
  ( load_install_defs || exit 1
    # shellcheck disable=SC2034 # read by resolve_release_tag, via the eval
    VERSION_PIN="$1"
    PATH="$curl_stub:$PATH" resolve_release_tag ) 2>&1
}
got="$(resolve v9.9.9-rc.1)"; st=$?
[ "$st" -eq 0 ] && [ "$got" = "v9.9.9-rc.1" ] \
  && ok "--version v9.9.9-rc.1 resolves the prerelease by name" \
  || bad "--version v9.9.9-rc.1 resolved to '$got' (exit $st)"
got="$(resolve v9.9.7)"; st=$?
case "$st:$got" in
  0:*) bad "a --version tag with no release resolved to '$got' instead of refusing" ;;
  *"no published release v9.9.7"*) ok "a --version tag with no release is refused, not replaced by latest" ;;
  *) bad "a missing --version tag was refused without naming it: $got" ;;
esac
got="$(resolve "")"; st=$?
[ "$st" -eq 0 ] && [ "$got" = "v9.9.8" ] \
  && ok "with no --version, the latest release is still what installs" \
  || bad "with no --version, resolved '$got' (exit $st), want v9.9.8"

# ------------------------------------------------------------- vacuity guard
#
# THE VERDICT ABOVE IS DERIVED FROM COUNTERS, AND COUNTERS CANNOT SEE A STEP
# THAT NEVER RAN.
#
# This suite runs under `set -uo pipefail` with no `-e`, deliberately: several
# steps run commands that are SUPPOSED to fail and read their status. The cost
# is that a step which fails to EXECUTE -- a helper renamed, a function that
# moved into a subshell, a typo in a call -- prints "command not found" to
# stderr, increments neither counter, and the run still ends "0 failed" and
# exits PASS. That is not hypothetical: it is #657, found when an undefined
# helper produced exactly that and the suite reported PASS.
#
# So the run has to state how much it actually checked. Deliberately BELOW the
# current total rather than equal to it: this is a vacuity guard, not a
# ratchet on the number of assertions, which is free to move either way. The
# same device as stopSiteFloor in internal/testenv/stopdiscard_test.go and the
# package-count floor in internal/testenv/docdrift_packages_test.go.
ASSERTION_FLOOR=100

printf "\n\033[1mSummary\033[0m\n  %d passed, %d failed\n" "$pass" "$fail"

total=$((pass + fail))
if [ "$total" -lt "$ASSERTION_FLOOR" ]; then
  printf "\n  \033[31mINSTALLER ACCEPTANCE INCOMPLETE\033[0m\n"
  printf "  only %d assertions ran, and this suite has at least %d.\n" "$total" "$ASSERTION_FLOOR"
  printf "  Steps did not execute — scroll up for \"command not found\" or an\n"
  printf "  unbound variable. A verdict from counters cannot see a step that\n"
  printf "  never ran, so this run is reported as a failure, not a pass.\n"
  exit 1
fi

[ "$fail" -eq 0 ] || { printf "\n  \033[31mINSTALLER ACCEPTANCE FAILED\033[0m\n"; exit 1; }
printf "\n  \033[32mINSTALLER ACCEPTANCE PASSED\033[0m (%d assertions)\n" "$total"
