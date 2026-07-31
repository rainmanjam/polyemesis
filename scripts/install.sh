#!/usr/bin/env bash
#
# polyemesis interactive installer for Linux.
#
#   curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL \
#     https://raw.githubusercontent.com/rainmanjam/polyemesis/main/scripts/install.sh | sudo bash
#
# or, having read it first, which is the better habit:
#
#   curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSLO \
#     https://raw.githubusercontent.com/rainmanjam/polyemesis/main/scripts/install.sh
#   less install.sh && sudo bash install.sh
#
# Two install modes:
#
#   docker  — the image bundles a known-good FFmpeg with libsrt, so nothing on
#             the host matters except Docker. Recommended.
#   binary  — one static binary plus a systemd unit. No Docker, but YOUR FFmpeg
#             has to clear 6.0 and carry libsrt, and on several current LTS
#             distributions it does not.
#
# This script never asks for, stores, or writes an admin password. polyemesis
# has no admin account until you open the UI and create one on the first-run
# screen, so there is no credential for an installer to mishandle.

set -euo pipefail

# ------------------------------------------------------------------ constants

REPO="rainmanjam/polyemesis"
IMAGE="rainmanjam/polyemesis"          # Docker Hub. The project publishes nowhere else.
SERVICE_NAME="polyemesis"
INSTALL_DIR="/opt/polyemesis"
DATA_DIR="/var/lib/polyemesis"
CONFIG_DIR="/etc/polyemesis"
BIN_PATH="/usr/local/bin/polyemesis"
RUN_USER="polyemesis"

# The floor polyemesis enforces at startup. Below this it refuses to start
# rather than failing later in a way that looks like a bug.
FFMPEG_MIN_MAJOR=6

HTTP_PORT=8080
SRT_PORT=6000
RTMP_PORT=1935

MODE=""            # docker | binary
TLS_MODE="off"     # off | selfsigned | acme
DOMAIN_NAME=""
ACME_EMAIL=""
ENABLE_RTMP="no"
CONFIGURE_FIREWALL="no"
COMPOSE_CMD=""

# Rollback bookkeeping. Each is flipped as the step that owns it succeeds, and
# read back by the trap so a half-finished install can be undone rather than
# left for the operator to reverse-engineer.
DIRS_CREATED=false
USER_CREATED=false
UNIT_CREATED=false
CONTAINER_STARTED=false
BINARY_INSTALLED=false
INSTALL_COMPLETE=false

# Every network fetch below goes through these flags.
#
# --proto '=https' pins the initial request and --proto-redir '=https' pins
# every redirect, so a hijacked or misconfigured redirect cannot walk this down
# to plaintext HTTP. That matters more here than in most scripts: this runs as
# root and installs a binary that will be started as a service, so a downgrade
# is a code-execution path rather than a privacy problem. `curl -L` alone will
# happily follow https -> http.
#
# Written out at each call site rather than held in an array. It is repetitive,
# and it means the reader of a script they are about to pipe into a shell can
# see the scheme being pinned on the line doing the download -- and so can
# static analysis, which cannot resolve "${ARRAY[@]}" and reported four
# downloads as unpinned when every one of them was pinned.

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'
BLUE=$'\033[0;34m'; BOLD=$'\033[1m'; NC=$'\033[0m'

# Colour is noise in a log file or a CI capture. Drop it when stdout is not a
# terminal rather than emitting escape sequences nobody will render.
if [ ! -t 1 ]; then
  RED=""; GREEN=""; YELLOW=""; BLUE=""; BOLD=""; NC=""
fi

info()    { echo "${BLUE}[info]${NC} $*"; }
ok()      { echo "${GREEN}[ ok ]${NC} $*"; }
warn()    { echo "${YELLOW}[warn]${NC} $*"; }
err()     { echo "${RED}[fail]${NC} $*" >&2; }
header()  { echo; echo "${BOLD}$*${NC}"; }

die() { err "$*"; exit 1; }

# ------------------------------------------------------------------- rollback

cleanup_on_failure() {
  local code=$?
  [ "$INSTALL_COMPLETE" = true ] && exit 0
  [ "$code" -eq 0 ] && exit 0

  echo
  warn "install failed (exit $code) — undoing what it had already done"

  if [ "$CONTAINER_STARTED" = true ] && [ -n "$COMPOSE_CMD" ]; then
    (cd "$INSTALL_DIR" && $COMPOSE_CMD down --remove-orphans) >/dev/null 2>&1 || true
    info "stopped and removed the container"
  fi
  if [ "$UNIT_CREATED" = true ]; then
    systemctl disable --now "$SERVICE_NAME" >/dev/null 2>&1 || true
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
    systemctl daemon-reload >/dev/null 2>&1 || true
    info "removed the systemd unit"
  fi
  [ "$BINARY_INSTALLED" = true ] && rm -f "$BIN_PATH" && info "removed $BIN_PATH"
  if [ "$DIRS_CREATED" = true ]; then
    rm -rf "$INSTALL_DIR"
    # NOT $DATA_DIR. It holds the database, secret.key and any recording made
    # between the failing step and now. An installer that deletes a data
    # directory on the way out is a worse problem than the one it is recovering
    # from, so it is left in place and named.
    info "removed $INSTALL_DIR"
    [ -d "$DATA_DIR" ] && warn "left $DATA_DIR alone — it may hold data. Remove it yourself if this was a first install."
  fi
  if [ "$USER_CREATED" = true ]; then
    userdel "$RUN_USER" >/dev/null 2>&1 || true
    info "removed the $RUN_USER account"
  fi

  echo
  err "nothing was left running. Fix the cause above and run this again."
  exit "$code"
}
trap cleanup_on_failure EXIT INT TERM

# ---------------------------------------------------------------------- input
#
# read from the controlling terminal, not stdin. Piping this script into bash
# makes stdin the SCRIPT, so a plain `read` swallows the next line of source
# and the prompt appears to answer itself.

ask() {
  local prompt="$1" default="${2:-}" varname="$3" reply=""
  local shown="$prompt"
  [ -n "$default" ] && shown="$prompt [$default]"

  if [ -r /dev/tty ]; then
    printf '%s: ' "$shown" > /dev/tty
    IFS= read -r reply < /dev/tty || reply=""
  elif [ -t 0 ]; then
    printf '%s: ' "$shown"
    IFS= read -r reply || reply=""
  else
    # No terminal at all (a provisioning run, a CI job). Take every default and
    # say so, rather than blocking forever on a prompt nobody can answer.
    reply=""
  fi

  [ -z "$reply" ] && reply="$default"
  printf -v "$varname" '%s' "$reply"
}

ask_yn() {
  local prompt="$1" default="$2" varname="$3" reply=""
  ask "$prompt (y/n)" "$default" reply
  case "${reply,,}" in
    y|yes) printf -v "$varname" '%s' "yes" ;;
    *)     printf -v "$varname" '%s' "no" ;;
  esac
}

# ------------------------------------------------------------------- preflight

require_root() {
  [ "$(id -u)" -eq 0 ] || die "run this with sudo: sudo bash $0"
}

detect_os() {
  [ -r /etc/os-release ] || die "no /etc/os-release — this installer targets Linux"
  # shellcheck disable=SC1091
  . /etc/os-release
  DISTRO="${ID:-unknown}"
  DISTRO_VERSION="${VERSION_ID:-unknown}"
  case "$(uname -m)" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) die "unsupported architecture $(uname -m) — polyemesis publishes amd64 and arm64" ;;
  esac
  ok "detected ${DISTRO} ${DISTRO_VERSION} (${ARCH})"
}

# check_ffmpeg enforces the two separate things that go wrong, because they
# fail differently and only one of them is fatal.
check_ffmpeg() {
  command -v ffmpeg >/dev/null 2>&1 || {
    err "no ffmpeg on PATH."
    echo "     polyemesis shells out to FFmpeg for everything. Install ${FFMPEG_MIN_MAJOR}.0 or newer,"
    echo "     or re-run and choose the docker mode, which bundles one."
    return 1
  }

  local raw major
  raw="$(ffmpeg -version 2>/dev/null | head -1 | sed -n 's/^ffmpeg version \([0-9][0-9]*\)\..*/\1/p')"
  if [ -z "$raw" ]; then
    # A git build, or a distro that rewrites the version banner. Unknown is not
    # the same as too old, so this warns rather than refusing.
    warn "could not parse the FFmpeg version from its banner — check it yourself:"
    ffmpeg -version | head -1
  else
    major="$raw"
    if [ "$major" -lt "$FFMPEG_MIN_MAJOR" ]; then
      err "FFmpeg $major.x is below the ${FFMPEG_MIN_MAJOR}.0 floor — polyemesis refuses to start against it."
      case "${DISTRO}:${DISTRO_VERSION}" in
        ubuntu:22.04) echo "     Ubuntu 22.04 ships 4.4. \`apt install ffmpeg\` will not fix this." ;;
        debian:12)    echo "     Debian 12 ships 5.1. \`apt install ffmpeg\` will not fix this." ;;
      esac
      echo "     Options: a newer distribution, a static build with libsrt"
      echo "     (https://github.com/BtbN/FFmpeg-Builds/releases), or the docker mode."
      return 1
    fi
    ok "ffmpeg $major.x clears the ${FFMPEG_MIN_MAJOR}.0 floor"
  fi

  # -x, not a bare grep. Every build lists `srtp` — Secure RTP, an unrelated
  # protocol that happens to contain the substring — so `grep srt` reports
  # success on a build with no SRT at all.
  if ffmpeg -hide_banner -protocols 2>/dev/null | tr ' ' '\n' | grep -qx srt; then
    ok "ffmpeg has libsrt — multitrack ingest available"
  else
    warn "this FFmpeg has NO libsrt."
    echo "       polyemesis will start and warn, and you can use RTMP — but RTMP carries"
    echo "       ONE stereo pair, so per-destination audio routing has nothing to route"
    echo "       from. That is the whole reason to run polyemesis."
    echo "       Fix it with a build configured --enable-libsrt, or use the docker mode."
    local proceed
    ask_yn "Continue without SRT anyway?" "n" proceed
    [ "$proceed" = "yes" ] || return 1
  fi
  return 0
}

install_docker() {
  if command -v docker >/dev/null 2>&1; then
    ok "docker present: $(docker --version)"
  else
    info "installing Docker from get.docker.com"
    curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL https://get.docker.com | sh || die "Docker install failed"
    systemctl enable --now docker || die "could not start the Docker daemon"
    ok "docker installed"
  fi

  if docker compose version >/dev/null 2>&1; then
    COMPOSE_CMD="docker compose"
  elif command -v docker-compose >/dev/null 2>&1; then
    COMPOSE_CMD="docker-compose"
  else
    die "Docker Compose not found. Install the compose plugin and re-run."
  fi
  ok "using: $COMPOSE_CMD"
}

# port_in_use reports whether anything already holds a port, so a collision is
# named here rather than discovered as an opaque bind error on first start.
port_in_use() {
  local port="$1" proto="${2:-tcp}"
  if command -v ss >/dev/null 2>&1; then
    if [ "$proto" = udp ]; then ss -lnu 2>/dev/null | grep -qE "[:.]${port}\b"
    else ss -lnt 2>/dev/null | grep -qE "[:.]${port}\b"; fi
  else
    return 1
  fi
}

warn_if_taken() {
  local port="$1" proto="$2" what="$3"
  if port_in_use "$port" "$proto"; then
    warn "${proto}/${port} (${what}) is already in use — expect a bind failure unless you change it"
  fi
}

# ----------------------------------------------------------------- interview

gather_configuration() {
  header "=== How to install ==="
  echo "  1) docker  — bundles a known-good FFmpeg with SRT. Recommended."
  echo "  2) binary  — static binary + systemd. Needs FFmpeg ${FFMPEG_MIN_MAJOR}.0+ with libsrt on this host."
  echo
  local choice
  ask "Choose 1 or 2" "1" choice
  case "$choice" in
    2|binary) MODE="binary" ;;
    *)        MODE="docker" ;;
  esac
  ok "mode: $MODE"

  header "=== Ports ==="
  ask "Web UI port (tcp)" "$HTTP_PORT" HTTP_PORT
  ask "SRT ingest port (UDP — this is the one people forget)" "$SRT_PORT" SRT_PORT
  ask_yn "Also expose RTMP on ${RTMP_PORT}/tcp? Only needed for encoders that cannot do SRT" "n" ENABLE_RTMP

  warn_if_taken "$HTTP_PORT" tcp "web UI"
  warn_if_taken "$SRT_PORT"  udp "SRT ingest"
  [ "$ENABLE_RTMP" = yes ] && warn_if_taken "$RTMP_PORT" tcp "RTMP ingest"

  header "=== TLS ==="
  echo "  Plain HTTP sends the login form and session cookie in clear text."
  echo "  On anything but loopback that is the biggest exposure this product has."
  echo
  echo "  1) off        — plain HTTP. Fine behind a reverse proxy, or over an SSH tunnel."
  echo "  2) selfsigned — encrypted now, browser warning until you install the CA."
  echo "  3) acme       — Let's Encrypt. Needs a public DNS name and inbound port 80."
  echo
  local tls_choice
  ask "Choose 1, 2 or 3" "2" tls_choice
  case "$tls_choice" in
    1|off)  TLS_MODE="off" ;;
    3|acme) TLS_MODE="acme" ;;
    *)      TLS_MODE="selfsigned" ;;
  esac

  if [ "$TLS_MODE" = "acme" ]; then
    while [ -z "$DOMAIN_NAME" ]; do
      ask "Public DNS name pointing at this box (e.g. stream.example.com)" "" DOMAIN_NAME
      case "$DOMAIN_NAME" in
        *.local|*.internal|*.lan|*.home|*.arpa|localhost|"")
          err "Let's Encrypt cannot validate '${DOMAIN_NAME:-<empty>}' — it needs a public name."
          DOMAIN_NAME="" ;;
        *.*) : ;;
        *)  err "that has no dot in it, so it is not a public FQDN."
            DOMAIN_NAME="" ;;
      esac
    done
    while [ -z "$ACME_EMAIL" ]; do
      ask "Contact email for Let's Encrypt" "" ACME_EMAIL
      case "$ACME_EMAIL" in
        *@*.*) : ;;
        *) err "that does not look like an email address."; ACME_EMAIL="" ;;
      esac
    done
    echo
    warn "acme needs inbound tcp/80 from the public internet for HTTP-01 validation."
    warn "A CNAME to a CDN, a captive portal, or an ISP blocking 80 all break issuance."
  elif [ "$TLS_MODE" = "selfsigned" ]; then
    ask "Hostname or LAN IP you will browse to (goes in the certificate)" "$(hostname -f 2>/dev/null || hostname)" DOMAIN_NAME
  fi
  ok "tls: $TLS_MODE${DOMAIN_NAME:+ ($DOMAIN_NAME)}"

  header "=== Data ==="
  echo "  Holds the database, secret.key (which decrypts your stored platform"
  echo "  tokens), recordings and TLS material. Back it up; treat it as secret."
  [ "$MODE" = "binary" ] && ask "Data directory" "$DATA_DIR" DATA_DIR

  header "=== Firewall ==="
  if command -v ufw >/dev/null 2>&1 || command -v firewall-cmd >/dev/null 2>&1; then
    ask_yn "Open the ports above in the local firewall?" "y" CONFIGURE_FIREWALL
  else
    info "no ufw or firewalld found — skipping"
  fi
}

confirm_plan() {
  header "=== About to do this ==="
  echo "  mode          ${MODE}"
  echo "  web UI        tcp/${HTTP_PORT}"
  echo "  SRT ingest    udp/${SRT_PORT}"
  [ "$ENABLE_RTMP" = yes ] && echo "  RTMP ingest   tcp/${RTMP_PORT}"
  echo "  tls           ${TLS_MODE}${DOMAIN_NAME:+  (${DOMAIN_NAME})}"
  [ "$TLS_MODE" = acme ] && echo "  acme email    ${ACME_EMAIL}"
  echo "  data          ${DATA_DIR}"
  echo "  install dir   ${INSTALL_DIR}"
  [ "$CONFIGURE_FIREWALL" = yes ] && echo "  firewall      will open the ports above"
  echo
  local go
  ask_yn "Proceed?" "y" go
  [ "$go" = yes ] || { INSTALL_COMPLETE=true; info "nothing done."; exit 0; }
}

# ------------------------------------------------------------------ tls block
#
# Emitted into config.yaml for both modes. `auto` is not used here on purpose:
# the operator has just answered the question auto exists to guess at, and a
# resolved mode that disagrees with what they chose would be worse than either.

tls_yaml() {
  case "$TLS_MODE" in
    acme)
      printf 'tls:\n  mode: "acme"\n  hostname: "%s"\n  acmeEmail: "%s"\n  hsts: true\n' \
        "$DOMAIN_NAME" "$ACME_EMAIL" ;;
    selfsigned)
      # hsts stays off, and polyemesis would refuse to send it here anyway: an
      # HSTS header from a self-signed host pins the browser to HTTPS for that
      # name and removes the click-through, with no way for the server to undo it.
      printf 'tls:\n  mode: "selfsigned"\n  hostname: "%s"\n' "$DOMAIN_NAME" ;;
    *)
      printf 'tls:\n  mode: "off"\n' ;;
  esac
}

# --------------------------------------------------------------- docker mode

install_docker_mode() {
  mkdir -p "$INSTALL_DIR"
  DIRS_CREATED=true

  {
    printf '# Written by scripts/install.sh. Edit and `docker compose up -d` to apply.\n'
    printf 'dataDir: "/data"\n'
    printf 'addr: ":%s"\n' "$HTTP_PORT"
    tls_yaml
  } > "$INSTALL_DIR/config.yaml"

  {
    printf 'services:\n'
    printf '  polyemesis:\n'
    printf '    image: %s:latest\n' "$IMAGE"
    printf '    container_name: polyemesis\n'
    printf '    restart: unless-stopped\n'
    printf '    ports:\n'
    printf '      - "%s:%s"\n' "$HTTP_PORT" "$HTTP_PORT"
    # /udp is not optional. SRT is UDP, and omitting the suffix publishes a TCP
    # port of the same number instead: the container starts, the UI works, and
    # the ingest silently receives nothing. It is the single most common
    # first-run failure.
    printf '      - "%s:%s/udp"\n' "$SRT_PORT" "$SRT_PORT"
    [ "$ENABLE_RTMP" = yes ] && printf '      - "%s:%s"\n' "$RTMP_PORT" "$RTMP_PORT"
    [ "$TLS_MODE" = acme ] && printf '      - "80:80"\n'
    printf '    volumes:\n'
    printf '      - polyemesis-data:/data\n'
    printf '      - ./config.yaml:/config.yaml:ro\n'
    printf '    command: ["-config", "/config.yaml"]\n'
    # Recordings are finalised on the way down. A shorter grace period truncates
    # whatever was being written, so this matches the project's own compose file.
    printf '    stop_grace_period: 30s\n'
    printf 'volumes:\n'
    printf '  polyemesis-data:\n'
  } > "$INSTALL_DIR/docker-compose.yml"

  info "pulling ${IMAGE}:latest"
  (cd "$INSTALL_DIR" && $COMPOSE_CMD pull) || die "could not pull the image"

  info "starting"
  (cd "$INSTALL_DIR" && $COMPOSE_CMD up -d) || die "could not start the container"
  CONTAINER_STARTED=true

  DATA_DIR="docker volume: polyemesis-data"
}

# --------------------------------------------------------------- binary mode

latest_release_tag() {
  curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1
}

install_binary_mode() {
  check_ffmpeg || die "FFmpeg preflight failed — fix the above, or re-run and choose docker."

  local tag asset url tmp
  tag="$(latest_release_tag)"
  [ -n "$tag" ] || die "no published release found for ${REPO}. Build from source, or use the docker mode."
  ok "latest release: $tag"

  asset="polyemesis-${tag}-linux-${ARCH}"
  url="https://github.com/${REPO}/releases/download/${tag}/${asset}"
  tmp="$(mktemp -d)"

  info "downloading $asset"
  curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL -o "$tmp/$asset" "$url" || die "download failed: $url"

  # Verify against the release's own SHA256SUMS. A binary that is about to run
  # as a service, installed over the network, is worth one extra request.
  if curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL -o "$tmp/SHA256SUMS" \
       "https://github.com/${REPO}/releases/download/${tag}/SHA256SUMS" 2>/dev/null; then
    if (cd "$tmp" && grep " ${asset}\$" SHA256SUMS | sha256sum -c --status -); then
      ok "checksum verified"
    else
      rm -rf "$tmp"
      die "CHECKSUM MISMATCH for $asset — refusing to install it."
    fi
  else
    warn "no SHA256SUMS published for $tag — installing an unverified download"
  fi

  install -m 0755 "$tmp/$asset" "$BIN_PATH"
  BINARY_INSTALLED=true
  rm -rf "$tmp"
  ok "installed $BIN_PATH"

  if ! id "$RUN_USER" >/dev/null 2>&1; then
    useradd --system --home "$DATA_DIR" --shell /usr/sbin/nologin "$RUN_USER"
    USER_CREATED=true
    ok "created the $RUN_USER service account"
  fi

  mkdir -p "$DATA_DIR" "$CONFIG_DIR"
  DIRS_CREATED=true
  chown -R "$RUN_USER:$RUN_USER" "$DATA_DIR"

  {
    printf '# Written by scripts/install.sh.\n'
    printf '# Only what must be known before the database opens lives here.\n'
    printf '# Destinations, routing and renditions are runtime state, edited in the UI.\n'
    printf 'dataDir: "%s"\n' "$DATA_DIR"
    printf 'addr: ":%s"\n' "$HTTP_PORT"
    tls_yaml
  } > "$CONFIG_DIR/config.yaml"

  local caps=""
  if [ "$TLS_MODE" = "acme" ]; then
    # Ports below 1024 are privileged and this unit runs unprivileged, so
    # without this the :80 bind fails, polyemesis warns and keeps serving
    # HTTPS, and ACME issuance never completes.
    caps=$'AmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE'
  fi

  cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=polyemesis restreaming server
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${RUN_USER}
Group=${RUN_USER}
ExecStart=${BIN_PATH} --config ${CONFIG_DIR}/config.yaml --data ${DATA_DIR} --addr :${HTTP_PORT}
${caps}

# polyemesis tears its FFmpeg children down in order on SIGTERM so a recording
# is finalised rather than truncated. Give it room, and signal only the main
# process — it owns the child process groups itself.
KillSignal=SIGTERM
KillMode=mixed
TimeoutStopSec=45
Restart=on-failure
RestartSec=5

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${DATA_DIR}

[Install]
WantedBy=multi-user.target
EOF
  UNIT_CREATED=true

  systemctl daemon-reload
  systemctl enable --now "$SERVICE_NAME" || die "the service did not start — journalctl -u ${SERVICE_NAME} -n 50"
  ok "service enabled and started"
}

# ------------------------------------------------------------------ firewall

configure_firewall() {
  [ "$CONFIGURE_FIREWALL" = yes ] || return 0
  if command -v ufw >/dev/null 2>&1; then
    ufw allow "${HTTP_PORT}/tcp"  >/dev/null 2>&1 || true
    ufw allow "${SRT_PORT}/udp"   >/dev/null 2>&1 || true
    [ "$ENABLE_RTMP" = yes ] && ufw allow "${RTMP_PORT}/tcp" >/dev/null 2>&1 || true
    [ "$TLS_MODE" = acme ]  && ufw allow 80/tcp              >/dev/null 2>&1 || true
    ok "ufw rules added"
  elif command -v firewall-cmd >/dev/null 2>&1; then
    firewall-cmd --permanent --add-port="${HTTP_PORT}/tcp" >/dev/null 2>&1 || true
    firewall-cmd --permanent --add-port="${SRT_PORT}/udp"  >/dev/null 2>&1 || true
    [ "$ENABLE_RTMP" = yes ] && firewall-cmd --permanent --add-port="${RTMP_PORT}/tcp" >/dev/null 2>&1 || true
    [ "$TLS_MODE" = acme ]  && firewall-cmd --permanent --add-port=80/tcp              >/dev/null 2>&1 || true
    firewall-cmd --reload >/dev/null 2>&1 || true
    ok "firewalld rules added"
  fi
}

# ------------------------------------------------------- helper scripts

write_helper_scripts() {
  [ "$MODE" = "docker" ] || return 0

  cat > "$INSTALL_DIR/update.sh" <<EOF
#!/usr/bin/env bash
# Back up the volume BEFORE pulling: migrations run forward only and there is
# no downgrade path. The backup is the only way back.
set -euo pipefail
cd "$INSTALL_DIR"
stamp="\$(date +%F-%H%M)"
echo "backing up to $INSTALL_DIR/backup-\${stamp}.tar.gz"
docker run --rm -v polyemesis-data:/data -v "$INSTALL_DIR:/backup" alpine \\
  tar czf "/backup/backup-\${stamp}.tar.gz" -C /data .
$COMPOSE_CMD pull
$COMPOSE_CMD up -d
echo "updated. Watch the first minute: $COMPOSE_CMD logs -f"
EOF
  chmod +x "$INSTALL_DIR/update.sh"

  cat > "$INSTALL_DIR/uninstall.sh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
cd "$INSTALL_DIR"
$COMPOSE_CMD down --remove-orphans
echo
echo "Stopped and removed the container."
echo "The 'polyemesis-data' volume was KEPT — it holds your database, secret.key"
echo "and recordings. To destroy it permanently:"
echo
echo "    docker volume rm polyemesis-data"
echo
echo "Then: rm -rf $INSTALL_DIR"
EOF
  chmod +x "$INSTALL_DIR/uninstall.sh"
  ok "wrote update.sh and uninstall.sh"
}

# --------------------------------------------------------------- verification

verify() {
  local scheme="http" url deadline
  [ "$TLS_MODE" = "off" ] || scheme="https"
  url="${scheme}://127.0.0.1:${HTTP_PORT}/api/v1/health"

  info "waiting for the server to answer /health"
  deadline=$(( $(date +%s) + 60 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    # -k because in selfsigned mode the certificate is by definition not yet
    # trusted here; this asks whether the server is up, not who it is.
    if curl -fsSk "$url" >/dev/null 2>&1; then
      ok "server is up"
      return 0
    fi
    sleep 2
  done

  err "the server did not answer within 60s."
  if [ "$MODE" = docker ]; then
    echo "     logs: cd $INSTALL_DIR && $COMPOSE_CMD logs --tail 50"
  else
    echo "     logs: journalctl -u $SERVICE_NAME -n 50 --no-pager"
  fi
  return 1
}

print_summary() {
  local scheme="http" hostpart="localhost"
  [ "$TLS_MODE" = "off" ] || scheme="https"
  [ -n "$DOMAIN_NAME" ] && hostpart="$DOMAIN_NAME"

  header "polyemesis is running"
  echo
  echo "  ${BOLD}Open ${scheme}://${hostpart}:${HTTP_PORT}${NC} and create your admin password."
  echo "  There is no account until you do — that first screen is the only way to make one."
  echo

  if [ "$TLS_MODE" = "selfsigned" ]; then
    echo "  ${YELLOW}Your browser will warn about the certificate.${NC} That is expected."
    echo "  Install the CA to make it stop: it is offered at"
    echo "    ${scheme}://${hostpart}:${HTTP_PORT}/api/v1/tls/ca"
    echo "  Check its fingerprint against the 'ca sha-256' line in the startup log first."
    echo "  See docs/TLS.md#trusting-the-self-signed-ca."
    echo
  elif [ "$TLS_MODE" = "acme" ]; then
    echo "  The certificate is issued lazily, on the first HTTPS request for"
    echo "  ${DOMAIN_NAME}. If it does not appear, the cause is almost always"
    echo "  inbound tcp/80 not reaching this host."
    echo
  else
    echo "  ${YELLOW}TLS is off.${NC} The login form and session cookie cross the network in"
    echo "  clear text. Fine on loopback or behind a proxy that terminates TLS;"
    echo "  not fine on an open network. See docs/TLS.md."
    echo
  fi

  echo "  ${BOLD}Point your encoder at${NC}"
  echo "    srt://${hostpart}:${SRT_PORT}?streamid=<token>"
  echo "  The Sources page shows the token. It is the address, so every source"
  echo "  shares this one port — adding another needs no new port and no restart."
  echo
  echo "  In OBS, multitrack SRT does NOT go through the Stream tab. Use"
  echo "  Settings -> Output -> Advanced -> Recording, Type: Custom Output (FFmpeg),"
  echo "  FFmpeg Output Type: Output to URL, Container: mpegts. See docs/OBS.md."
  echo

  echo "${BOLD}Commands${NC}"
  if [ "$MODE" = docker ]; then
    echo "  Logs        cd $INSTALL_DIR && $COMPOSE_CMD logs -f"
    echo "  Restart     cd $INSTALL_DIR && $COMPOSE_CMD restart"
    echo "  Update      sudo $INSTALL_DIR/update.sh"
    echo "  Uninstall   sudo $INSTALL_DIR/uninstall.sh"
    echo "  Config      $INSTALL_DIR/config.yaml"
    echo "  Data        docker volume 'polyemesis-data'  ${YELLOW}(back this up)${NC}"
  else
    echo "  Logs        journalctl -u $SERVICE_NAME -f"
    echo "  Restart     sudo systemctl restart $SERVICE_NAME"
    echo "  Config      $CONFIG_DIR/config.yaml"
    echo "  Data        $DATA_DIR  ${YELLOW}(back this up)${NC}"
    echo "  Uninstall   sudo systemctl disable --now $SERVICE_NAME && sudo rm $BIN_PATH"
  fi
  echo
  echo "  The data directory holds secret.key, which decrypts your stored platform"
  echo "  tokens. Treat a backup of it as secret material."
  echo
  echo "  Docs: https://github.com/${REPO}/tree/main/docs"
}

# ------------------------------------------------------------------- main

main() {
  header "polyemesis installer"
  require_root
  detect_os
  gather_configuration
  confirm_plan

  if [ "$MODE" = "docker" ]; then
    install_docker
    install_docker_mode
    write_helper_scripts
  else
    install_binary_mode
  fi

  configure_firewall
  verify

  INSTALL_COMPLETE=true
  print_summary
}

main "$@"
