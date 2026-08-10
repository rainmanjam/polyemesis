#!/usr/bin/env bash
# TLS acceptance test — the backwards-compatibility one.
#
# tls.mode replaced tls.enabled. Every existing install carries the old key, so
# the thing worth proving by measurement is not that the new modes work but that
# the old configs kept working: an upgrade that silently stopped serving HTTPS,
# or that swapped an operator's real certificate for a generated one, is a
# serious regression and it is invisible from the server's own logs.
#
# Each case runs the real binary against a real config.yaml and inspects the
# handshake from outside the process, with openssl and curl, because that is the
# only vantage point from which "which certificate did it actually present" is a
# question and not an assumption.
#
# Usage:  ./scripts/acceptance-tls.sh [workdir]
set -uo pipefail

WORK="${1:-/tmp/polyemesis-acceptance-tls}"
SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
# Shared teardown. See lib-cleanup.sh: killing the server alone orphans its
# FFmpeg children, and they corrupt the NEXT run's relay ports.
. "$SCRIPTS/lib-cleanup.sh"
# A deadline of our own. See lib-watchdog.sh: the job ceiling cancels a hung
# suite and prints nothing, so the suite has to give up first and say what it
# was waiting for.
. "$SCRIPTS/lib-watchdog.sh"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
BIN="$ROOT/polyemesis"

# One port per case so a lingering process from a previous run cannot be
# mistaken for this one's.
PORT_OFF=8101      # legacy tls.enabled: false
PORT_MANUAL=8102   # legacy tls.enabled: true + certFile/keyFile
PORT_SELF=8103     # mode: selfsigned, started twice
PORT_PROXY=8104    # mode: auto behind a trusted proxy

pass=0; fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; poly_step_record "$1"; }

cleanup() { poly_cleanup "$PORT_OFF $PORT_MANUAL $PORT_SELF $PORT_PROXY" "${WORK:-}"; }
trap 'poly_watchdog_disarm; cleanup' EXIT

[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }
command -v openssl >/dev/null || { echo "openssl is required"; exit 1; }

rm -rf "$WORK"; mkdir -p "$WORK"; cd "$WORK"
# Armed here rather than earlier: the watchdog is a separate process and
# inherits this directory, which is where server.log will be written and where
# its report goes looking for it.
poly_watchdog_arm

# start <dir> <port>  -- boots the binary in <dir> against its config.yaml.
# Returns non-zero if the banner never appeared, so a case can report the
# failure itself rather than hanging.
start() {
  local dir="$1" port="$2"
  ( cd "$dir" && "$BIN" -addr ":$port" -log warn > server.log 2>&1 & )
  for _ in $(seq 1 60); do
    sleep 0.3
    grep -q "web ui" "$dir/server.log" 2>/dev/null && { sleep 0.5; return 0; }
  done
  return 1
}

# stop asks, then OBSERVES. `pkill; sleep 0.6` asked and assumed, and this
# script rebinds: PORT_SELF is stopped at :296 and started again at :299 to
# prove a second run reuses the same leaf certificate. A fixed sleep that is
# 0.6s too short there makes the restart fail to bind and the suite report a
# certificate problem it has no evidence of.
#
# poly_free_port is the exemplar already in the tree (lib-cleanup.sh): it waits
# for the port to be released, escalates only if it is not, and says so loudly
# when it had to.
stop() { pkill -f "polyemesis -addr :$1" 2>/dev/null; poly_free_port "$1"; }

# mode_line <dir> -> whatever the startup banner said tls resolved to.
mode_line() { awk '/^  tls /{print $2; exit}' "$1/server.log"; }

# perms <file> -> octal mode, on both BSD and GNU stat.
#
# The obvious spelling -- `stat -f '%Lp' || stat -c '%a'` -- looks portable and
# is not. On GNU stat, -f is a VALID flag meaning "file system status", so it
# SUCCEEDS and prints something like `File: "data/tls/ca.key"`. The || never
# fires, and the caller compares that string against 0600 and reports a
# permissions failure on a file whose permissions are correct.
#
# That is what this suite did on Linux while passing on macOS for two months.
# GNU first, because its failure on BSD is a real failure.
perms() {
  stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1" 2>/dev/null
}

# fingerprint_file <pem> -> the SHA-256 fingerprint of the first certificate.
fingerprint_file() {
  openssl x509 -in "$1" -noout -fingerprint -sha256 2>/dev/null | cut -d= -f2
}

# fingerprint_served <port> [servername] -> fingerprint of the leaf the running
# server presents. This is the measurement the whole script exists for.
fingerprint_served() {
  local port="$1" name="${2:-localhost}"
  openssl s_client -connect "127.0.0.1:$port" -servername "$name" </dev/null 2>/dev/null \
    | openssl x509 -noout -fingerprint -sha256 2>/dev/null | cut -d= -f2
}

# headers <url> [curl args...] -> response headers of a GET, lowercased names.
headers() {
  local url="$1"; shift
  curl -s -o /dev/null -D - "$@" "$url" 2>/dev/null | tr 'A-Z' 'a-z'
}

# ------------------------------------------------- 1. legacy tls.enabled:false
step "1. OLD-STYLE config: tls.enabled: false still serves plain HTTP"

mkdir -p off
cat > off/config.yaml <<YAML
addr: "127.0.0.1:$PORT_OFF"
dataDir: "./data"
tls:
  enabled: false
  hsts: true
YAML

if start off "$PORT_OFF"; then
  ok "server started with a pre-mode config.yaml"

  body=$(curl -s "http://127.0.0.1:$PORT_OFF/api/v1/health")
  [ "$body" = '{"status":"ok"}' ] && ok "plain HTTP serves /api/v1/health" \
    || bad "plain HTTP health returned '$body'"

  m=$(mode_line off)
  [ "$m" = "off" ] && ok "tls.enabled: false resolved to mode off" \
    || bad "tls.enabled: false resolved to '$m', expected off"

  # HSTS was asked for and must still be refused: nothing here is HTTPS.
  h=$(headers "http://127.0.0.1:$PORT_OFF/api/v1/health")
  case "$h" in
    *strict-transport-security*) bad "HSTS sent over plain HTTP" ;;
    *) ok "no Strict-Transport-Security over plain HTTP, even with tls.hsts: true" ;;
  esac
  case "$h" in
    *content-security-policy*) ok "Content-Security-Policy present" ;;
    *) bad "no Content-Security-Policy" ;;
  esac

  # Nothing generated: an install that never asked for TLS gets no key material.
  [ -e off/data/tls ] && bad "a tls/ directory was created in off mode" \
    || ok "no key material generated in off mode"

  code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT_OFF/api/v1/tls/ca")
  [ "$code" = "404" ] && ok "/api/v1/tls/ca 404s when there is no local CA" \
    || bad "/api/v1/tls/ca returned $code in off mode, expected 404"
else
  bad "server did not start with tls.enabled: false"
fi
stop "$PORT_OFF"

# ------------------------------------------- 2. legacy tls.enabled:true + pair
step "2. OLD-STYLE config: tls.enabled: true + cert/key still serves THAT cert"

mkdir -p manual
# A throwaway pair standing in for the operator's real certificate. The subject
# is deliberately distinctive so a generated cert could not be mistaken for it.
openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
  -subj "/CN=legacy-operator-cert" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" \
  -keyout manual/operator.key -out manual/operator.crt >/dev/null 2>&1
chmod 600 manual/operator.key

OPERATOR_FP=$(fingerprint_file manual/operator.crt)
[ -n "$OPERATOR_FP" ] && ok "generated a throwaway operator certificate" \
  || bad "could not generate a throwaway certificate"

cat > manual/config.yaml <<YAML
addr: "127.0.0.1:$PORT_MANUAL"
dataDir: "./data"
tls:
  enabled: true
  certFile: "$WORK/manual/operator.crt"
  keyFile: "$WORK/manual/operator.key"
  hsts: true
YAML

if start manual "$PORT_MANUAL"; then
  ok "server started with a pre-mode HTTPS config.yaml"

  m=$(mode_line manual)
  [ "$m" = "manual" ] && ok "tls.enabled: true + cert/key resolved to mode manual" \
    || bad "tls.enabled: true resolved to '$m', expected manual"

  body=$(curl -sk "https://127.0.0.1:$PORT_MANUAL/api/v1/health")
  [ "$body" = '{"status":"ok"}' ] && ok "HTTPS serves /api/v1/health" \
    || bad "HTTPS health returned '$body'"

  # The regression this case exists to catch: serving a generated certificate
  # where the operator configured their own.
  served=$(fingerprint_served "$PORT_MANUAL")
  if [ -n "$served" ] && [ "$served" = "$OPERATOR_FP" ]; then
    ok "the served leaf is the operator's own certificate (sha-256 matches)"
  else
    bad "served fingerprint '$served' != operator's '$OPERATOR_FP'"
  fi

  subj=$(openssl s_client -connect "127.0.0.1:$PORT_MANUAL" -servername localhost </dev/null 2>/dev/null \
         | openssl x509 -noout -subject 2>/dev/null)
  case "$subj" in
    *legacy-operator-cert*) ok "served subject is CN=legacy-operator-cert" ;;
    *) bad "served subject is '$subj'" ;;
  esac

  [ -e manual/data/tls/server.crt ] && bad "manual mode generated its own leaf" \
    || ok "manual mode generated no certificate of its own"

  # Positive control for the HSTS logic: a browser-validatable mode with the
  # opt-in set is the one case where the header is allowed out.
  h=$(headers "https://127.0.0.1:$PORT_MANUAL/api/v1/health" -k)
  case "$h" in
    *strict-transport-security*)
      ok "HSTS is sent in manual mode when tls.hsts is opted into"
      case "$h" in
        *includesubdomains*|*preload*) bad "HSTS carries includeSubDomains or preload" ;;
        *) ok "HSTS carries neither includeSubDomains nor preload" ;;
      esac
      ;;
    *) bad "no HSTS in manual mode despite tls.hsts: true" ;;
  esac
else
  bad "server did not start with tls.enabled: true"
fi
stop "$PORT_MANUAL"

# ------------------------------------------------------- 3. mode: selfsigned
step "3. mode: selfsigned generates a CA + leaf, then reuses them"

mkdir -p self
cat > self/config.yaml <<YAML
addr: "127.0.0.1:$PORT_SELF"
dataDir: "./data"
tls:
  mode: selfsigned
  hostname: "localhost"
  hsts: true
YAML

FIRST_CA_FP=""
FIRST_LEAF_FP=""
if start self "$PORT_SELF"; then
  ok "first run started in selfsigned mode"

  m=$(mode_line self)
  [ "$m" = "selfsigned" ] && ok "banner reports mode selfsigned" \
    || bad "banner reports '$m', expected selfsigned"

  missing=""
  for f in ca.crt ca.key server.crt server.key; do
    [ -s "self/data/tls/$f" ] || missing="$missing $f"
  done
  [ -z "$missing" ] && ok "first run wrote ca.crt, ca.key, server.crt, server.key" \
    || bad "first run did not write:$missing"

  # Private keys are 0600. The two public certificates deliberately are not.
  for k in ca.key server.key; do
    p=$(perms "self/data/tls/$k")
    [ "$p" = "600" ] && ok "$k is 0600" || bad "$k is $p, expected 600"
  done
  p=$(perms self/data/tls)
  [ "$p" = "700" ] && ok "tls/ directory is 0700" || bad "tls/ is $p, expected 700"

  # No private key may be readable over HTTP, whatever else changes.
  ca_body=$(curl -sk "https://127.0.0.1:$PORT_SELF/api/v1/tls/ca")
  case "$ca_body" in
    *"BEGIN CERTIFICATE"*) ok "/api/v1/tls/ca serves a certificate PEM without a session" ;;
    *) bad "/api/v1/tls/ca did not serve a certificate" ;;
  esac
  case "$ca_body" in
    *"PRIVATE KEY"*) bad "/api/v1/tls/ca leaked private key material" ;;
    *) ok "/api/v1/tls/ca contains no private key material" ;;
  esac

  # The strongest available statement that the generated chain is coherent:
  # curl validating the leaf against the CA it was told to trust, no -k.
  body=$(curl -s --cacert self/data/tls/ca.crt "https://localhost:$PORT_SELF/api/v1/health")
  [ "$body" = '{"status":"ok"}' ] \
    && ok "the leaf validates against the generated CA (no -k needed)" \
    || bad "validating against the generated CA failed: '$body'"

  # The footgun. Self-signed plus HSTS is how a homelab user loses access to
  # their own box, so the opt-in is overridden here and only here.
  h=$(headers "https://127.0.0.1:$PORT_SELF/api/v1/health" -k)
  case "$h" in
    *strict-transport-security*) bad "HSTS sent in selfsigned mode" ;;
    *) ok "no Strict-Transport-Security in selfsigned mode, despite tls.hsts: true" ;;
  esac
  grep -q "hsts" self/server.log 2>/dev/null \
    && ok "startup warned that tls.hsts was suppressed" \
    || bad "tls.hsts was suppressed without a warning"

  FIRST_CA_FP=$(fingerprint_file self/data/tls/ca.crt)
  FIRST_LEAF_FP=$(fingerprint_served "$PORT_SELF")
else
  bad "server did not start in selfsigned mode"
fi
stop "$PORT_SELF"

step "3b. The second run reuses the same CA — reinstalling it must not be a chore"
if start self "$PORT_SELF"; then
  ok "second run started against the existing material"

  SECOND_CA_FP=$(fingerprint_file self/data/tls/ca.crt)
  if [ -n "$FIRST_CA_FP" ] && [ "$FIRST_CA_FP" = "$SECOND_CA_FP" ]; then
    ok "the CA fingerprint is unchanged across restarts"
  else
    bad "the CA was regenerated: '$FIRST_CA_FP' -> '$SECOND_CA_FP'"
  fi

  SECOND_LEAF_FP=$(fingerprint_served "$PORT_SELF")
  if [ -n "$FIRST_LEAF_FP" ] && [ "$FIRST_LEAF_FP" = "$SECOND_LEAF_FP" ]; then
    ok "the served leaf is the same certificate, not a fresh one"
  else
    bad "the leaf was regenerated: '$FIRST_LEAF_FP' -> '$SECOND_LEAF_FP'"
  fi

  body=$(curl -s --cacert self/data/tls/ca.crt "https://localhost:$PORT_SELF/api/v1/health")
  [ "$body" = '{"status":"ok"}' ] && ok "HTTPS still serves after the restart" \
    || bad "HTTPS after restart returned '$body'"
else
  bad "server did not restart in selfsigned mode"
fi
stop "$PORT_SELF"

# ------------------------------------------------- 4. mode: auto behind proxy
step "4. mode: auto with trustProxyHeaders: true resolves to off"

mkdir -p proxy
cat > proxy/config.yaml <<YAML
addr: "127.0.0.1:$PORT_PROXY"
dataDir: "./data"
trustProxyHeaders: true
tls:
  mode: auto
  hostname: "stream.example.com"
  acmeEmail: "ops@example.com"
YAML

if start proxy "$PORT_PROXY"; then
  ok "server started with mode auto behind a trusted proxy"

  # Note the config: a public FQDN and an ACME email, which is exactly what
  # would otherwise resolve to acme. trustProxyHeaders has to win, or the box
  # fights its own proxy for port 80 and for the certificate.
  m=$(mode_line proxy)
  [ "$m" = "off" ] && ok "auto resolved to off despite a public FQDN and an acmeEmail" \
    || bad "auto resolved to '$m' behind a proxy, expected off"

  body=$(curl -s "http://127.0.0.1:$PORT_PROXY/api/v1/health")
  [ "$body" = '{"status":"ok"}' ] && ok "plain HTTP serves for the proxy to front" \
    || bad "plain HTTP health returned '$body'"

  [ -e proxy/data/tls/acme ] && bad "an ACME cache was created behind a proxy" \
    || ok "no ACME account or cache created behind a proxy"
else
  bad "server did not start with mode auto behind a proxy"
fi
stop "$PORT_PROXY"

# --------------------------------------------------------------------- done
step "Summary"
printf "  %d passed, %d failed\n" "$pass" "$fail"
if [ "$fail" -eq 0 ]; then
  printf "\n  \033[32mTLS ACCEPTANCE PASSED\033[0m\n\n"; exit 0
fi
printf "\n  \033[31mTLS ACCEPTANCE FAILED\033[0m\n\n"; exit 1
