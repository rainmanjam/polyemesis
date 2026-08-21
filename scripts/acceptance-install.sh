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
trap 'rm -rf "$work"' EXIT INT TERM

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
printf 'sqlite\n'    > "$root/data/polyemesis.db"
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
happy_path_then_a_second_run() { # <root> -> 2 if the clock crossed a minute
  local root="$1" before after out1 st1 out2 st2 dest
  mkdir -p "$root/data"
  printf 'key\n'    > "$root/data/secret.key"
  printf 'sqlite\n' > "$root/data/polyemesis.db"
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
  case "$out1" in
    *"backup verified: database and secret.key both present"*)
      ok "and it names the two files it verified rather than saying \"done\"" ;;
    *) bad "the success line no longer names what it checked" ;;
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
    # ... -v polyemesis-data:/data -v DIR:/backup alpine tar czf /backup/NAME -C /data .
    archive=""
    for a in "$@"; do case "$a" in /backup/*) archive="${a#/backup/}" ;; esac; done
    [ -n "$archive" ] || { echo "stub docker: no /backup path in: $*" >&2; exit 1; }
    tar czf "$STUB_BACKUP_DIR/$archive" -C "$STUB_VOLUME" . || exit 1
    exit 0 ;;
esac
echo "stub docker: unexpected invocation: $*" >&2
exit 1
STUB
chmod +x "$stub/docker"

gen_docker_update() { # gen_docker_update <install_dir>
  # Read by install.sh's write_helper_scripts, same as above.
  # shellcheck disable=SC2034
  ( load_install_defs || exit 1
    INSTALL_DIR="$1"
    MODE=docker
    COMPOSE_CMD="echo [stub compose]"
    write_helper_scripts >/dev/null )
}

run_docker_update() { # run_docker_update <install_dir> <volume_dir>
  STUB_VOLUME="$2" STUB_BACKUP_DIR="$1" PATH="$stub:$PATH" bash "$1/update.sh" 2>&1
}

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
printf 'sqlite\n' > "$vol/polyemesis.db"
out="$(run_docker_update "$docker_dir" "$vol")"; st=$?
check_refusal "an archive with a database but NO secret.key is refused" "$st" "$out" "no secret.key"
case "$out" in
  *disabled*) ok "and it says what that costs: every destination back disabled" ;;
  *) bad "the docker secret.key refusal no longer explains the consequence" ;;
esac

rm -f "$docker_dir"/backup-*.tar.gz

vol="$work/vol-ok"; mkdir -p "$vol"
printf 'sqlite\n' > "$vol/polyemesis.db"
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

printf "\n\033[1mSummary\033[0m\n  %d passed, %d failed\n" "$pass" "$fail"
[ "$fail" -eq 0 ] || { printf "\n  \033[31mINSTALLER ACCEPTANCE FAILED\033[0m\n"; exit 1; }
printf "\n  \033[32mINSTALLER ACCEPTANCE PASSED\033[0m\n"
