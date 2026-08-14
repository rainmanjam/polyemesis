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

# What the Docker image ships, and what CI now tests against. Between the floor
# and this the software runs correctly but not identically, so clearing the
# floor is reported differently from being current -- an operator on 6.1.1 has
# a working install that quietly cannot do things a 8.x install can, and the
# old check told them only that they had passed.
FFMPEG_RECOMMENDED_MAJOR=8

HTTP_PORT=8080
SRT_PORT=6000
RTMP_PORT=1935

MODE=""            # docker | binary
TLS_MODE="off"     # off | selfsigned | acme
DOMAIN_NAME=""
ACME_EMAIL=""
# Both ingest ports are published by default, matching what the server now
# binds and what this project's own docs have always told people to run
# (`-p 6000:6000/udp -p 1935:1935`). Set --rtmp-port 0 to decline it.
ENABLE_RTMP="yes"
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
CONFIG_DIR_CREATED=false
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

# fetch_https downloads one URL to one path, over https, refusing to be walked
# down to plaintext by a redirect. Returns non-zero if no downloader is present.
#
# THREE IMPLEMENTATIONS, because the honest answer to "which of these is always
# installed" is "none of them". A bare ubuntu:24.04 image has curl, wget AND
# python3 all absent; Debian minimal has python3 and no curl; and the documented
# way to run this script is `curl ... | sh`, which obviously implies curl. Any
# single choice is wrong on some host somebody actually has.
#
# This used to be python3 alone, on the reasoning that "neither curl nor wget is
# guaranteed present" — true, but it left urlretrieve following redirects with no
# restriction on scheme, which made this the ONE fetch in the installer that
# could be downgraded to http. It writes a binary into /usr/local/bin with
# install -m 0755, to be run by a root service, so that is a code-execution path
# and not a privacy question. All three branches below pin the scheme.
#
# Used only for this optional FFmpeg upgrade. The polyemesis binary and its
# checksums keep their inline curl, for the reason given at the top of this file:
# on the fetch that matters most, the reader piping this into a shell should see
# the flags on the line doing the download.
fetch_https() {
  local url="$1" out="$2"
  case "$url" in https://*) ;; *) return 1 ;; esac

  if command -v curl >/dev/null 2>&1; then
    curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL -o "$out" "$url" 2>/dev/null
    return $?
  fi
  if command -v wget >/dev/null 2>&1; then
    # --https-only is wget's --proto-redir: it refuses to follow a redirect to
    # any other scheme rather than silently downgrading.
    wget --https-only --secure-protocol=TLSv1_2 -q -O "$out" "$url" 2>/dev/null
    return $?
  fi
  if command -v python3 >/dev/null 2>&1; then
    # urlopen rather than urlretrieve, so the FINAL url after redirects can be
    # checked. urlretrieve reports only the last response, never the chain, and
    # will happily land on http.
    python3 - "$url" "$out" <<'PYFETCH' 2>/dev/null
import shutil, sys, urllib.request
url, out = sys.argv[1], sys.argv[2]
with urllib.request.urlopen(url) as r:
    if not r.geturl().startswith("https://"):
        sys.exit("redirected off https")
    with open(out, "wb") as f:
        shutil.copyfileobj(r, f)
PYFETCH
    return $?
  fi
  return 1
}

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
    # Config is not data: it is regenerated from the answers given, and leaving
    # a stale one behind makes a re-run behave differently from a first run for
    # reasons the operator cannot see.
    # Only if THIS run created it. See the note beside the mkdir.
    if [ "$CONFIG_DIR_CREATED" = true ]; then
      rm -rf "$CONFIG_DIR"
      info "removed $CONFIG_DIR"
    elif [ -d "$CONFIG_DIR" ]; then
      info "left $CONFIG_DIR alone — it predates this run"
    fi
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

  if [ "${ASSUME_YES:-false}" = true ]; then
    # Unattended. Take the default without printing a prompt nobody will read.
    reply=""
  elif [ -r /dev/tty ]; then
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
  # NOT named `reply`.
  #
  # ask() declares `local ... reply=""` of its own, and bash is dynamically
  # scoped: passing "reply" as the output name made ask's printf -v write into
  # ask's OWN local, which vanished on return. This function therefore read an
  # empty string every time and answered "no" to everything -- including
  # confirm_plan's "Proceed?", so the installer printed the plan and exited
  # with "nothing done." no matter what was typed. It could not install.
  #
  # The double underscore is the point: it must not collide with any local in
  # any function this one calls.
  local prompt="$1" default="$2" varname="$3" __answer=""
  ask "$prompt (y/n)" "$default" __answer
  case "${__answer,,}" in
    y|yes) printf -v "$varname" '%s' "yes" ;;
    *)     printf -v "$varname" '%s' "no" ;;
  esac
}

# Reports whether a question can actually be answered.
#
# ask() returns the default and moves on whenever nothing can answer it: under
# --yes by design, and with no controlling terminal because there is nothing
# else it could do. That is fine for a question with a usable default, and a
# trap for a REQUIRED value validated by re-prompting -- the loop re-asks, gets
# the same empty default, fails the same check, and never terminates.
#
# `--tls acme --yes` with no --hostname did exactly that: 302,994 copies of
# "Let's Encrypt cannot validate '<empty>'" in 25 seconds, no exit, on a host
# where the whole point of --yes was that nobody was watching. Callers use this
# to fail with the flag name instead of looping.
interactive() {
  [ "${ASSUME_YES:-false}" = true ] && return 1
  [ -r /dev/tty ] || [ -t 0 ]
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

# require_systemd fails EARLY, because the alternative is failing late.
#
# Both modes end at `systemctl enable --now`. Without this the binary mode
# downloads ~20 MB, verifies it, installs it, creates a service account and
# writes a unit file before systemd says no -- and what it says is
# "System has not been booted with systemd as init system (PID 1)", which
# names neither polyemesis nor the way out.
#
# Testing for the systemctl BINARY is not enough and was the first thing tried.
# Installing FFmpeg on Ubuntu pulls systemctl in as a dependency, so
# `command -v systemctl` succeeds inside a container that has no init at all.
# /run/systemd/system is the documented "booted with systemd" test -- what
# sd_booted(3) itself uses. It is a strong signal rather than a proof: a bind
# mount or a chroot can carry the path in with no init behind it. PID 1 is
# checked as well, and the two together are as close as a shell script gets.
require_systemd() {
  local pid1=""
  if [ -r /proc/1/comm ]; then read -r pid1 < /proc/1/comm || true; fi
  if [ -d /run/systemd/system ] && [ "${pid1:-systemd}" = systemd ]; then
    ok "systemd is running"
    return 0
  fi
  err "systemd is not running as PID 1 on this host."
  if command -v systemctl >/dev/null 2>&1; then
    echo "     (systemctl is installed, which is not the same thing -- it arrives"
    echo "      as a dependency of other packages. The init is what matters.)"
  fi
  echo "     polyemesis is installed here as a systemd service, in both modes."
  echo "     On a container, WSL without systemd, or an OpenRC/runit distribution,"
  echo "     run the image directly instead:"
  echo "       docker run -d --name polyemesis -p ${HTTP_PORT}:8080 \\"
  echo "         -p ${SRT_PORT}:${SRT_PORT}/udp -v polyemesis-data:/data ${IMAGE}"
  return 1
}

# offer_ffmpeg_upgrade installs a current static FFmpeg, with consent.
#
# The distro package is a dead end on the systems where this matters -- Ubuntu
# 24.04 pins 6.1.1 and `apt upgrade` never moves off it -- so the only way
# forward is a static build. That makes this a system-wide change to a binary
# the operator may be using for other things, which is why it asks, states what
# either answer costs, and never acts on its own under --yes.
#
# Installs to /usr/local/bin, ahead of /usr/bin on a default PATH, leaving the
# distro copy in place. Nothing is deleted, so the way back is one rm.
FFMPEG_UPGRADE=ask   # ask | skip | force

ffmpeg_static_asset() {
  # BtbN publishes per-architecture GPL tarballs. Only these two are built.
  case "$ARCH" in
    amd64) echo "ffmpeg-n8.1-latest-linux64-gpl-8.1.tar.xz" ;;
    arm64) echo "ffmpeg-n8.1-latest-linuxarm64-gpl-8.1.tar.xz" ;;
    *)     echo "" ;;
  esac
}

offer_ffmpeg_upgrade() {
  local have="$1" asset answer tmp
  asset="$(ffmpeg_static_asset)"

  echo "     What you have keeps working: SRT carries every audio track on any"
  echo "     version above the floor, and nothing about routing, recording or"
  echo "     destinations depends on this."
  echo "     What ${FFMPEG_RECOMMENDED_MAJOR}.x adds is multitrack FLV. Below 7.1 an Enhanced RTMP"
  echo "     publisher sending several audio tracks arrives as ONE track, with no"
  echo "     error on either end -- which is the part worth knowing, because it"
  echo "     looks like the tracks were never sent."

  if [ -z "$asset" ]; then
    warn "no static build is published for $ARCH — staying on $have.x"
    echo "     Use the docker mode, which bundles ${FFMPEG_RECOMMENDED_MAJOR}.x for every architecture."
    return 0
  fi

  case "$FFMPEG_UPGRADE" in
    skip)
      echo "     Skipping the upgrade (--ffmpeg skip). Staying on $have.x."
      return 0 ;;
    force) answer=yes ;;
    *)
      if ! interactive; then
        # Unattended. Installing a system binary nobody asked for is not a
        # default worth taking silently.
        warn "unattended: leaving FFmpeg at $have.x. Re-run with --ffmpeg force to upgrade."
        return 0
      fi
      # Default "n", not "y". ask() returns the default whenever nothing can
      # answer -- and interactive() can still be true on a host where /dev/tty
      # is readable but no one is reading it, so a "y" default would replace a
      # system binary on the strength of a question nobody saw. Someone who
      # wants this either types y or passes --ffmpeg force.
      ask_yn "Install FFmpeg ${FFMPEG_RECOMMENDED_MAJOR}.x to /usr/local/bin now?" "n" answer ;;
  esac

  if [ "$answer" != "yes" ]; then
    echo "     Left at $have.x. Enhanced RTMP multitrack will arrive as a single"
    echo "     track; everything else behaves the same. Re-run with --ffmpeg force"
    echo "     to change this later."
    return 0
  fi

  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" RETURN

  echo "     Fetching $asset ..."
  if ! fetch_https "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/$asset" \
        "$tmp/ff.tar.xz"; then
    warn "download failed — staying on $have.x. Nothing was changed."
    warn "(needs one of curl, wget or python3; this host appears to have none)"
    return 0
  fi

  mkdir -p "$tmp/x"
  if ! tar xf "$tmp/ff.tar.xz" --strip-components=1 -C "$tmp/x" 2>/dev/null; then
    warn "the archive did not extract — staying on $have.x. Nothing was changed."
    return 0
  fi

  # Verify BEFORE displacing anything: a build without libsrt would be a
  # downgrade in capability dressed as an upgrade in version.
  if ! "$tmp/x/bin/ffmpeg" -hide_banner -protocols 2>/dev/null | tr ' ' '\n' | grep -qx srt; then
    warn "the downloaded build has no libsrt — refusing it, staying on $have.x."
    return 0
  fi

  install -m 0755 "$tmp/x/bin/ffmpeg"  /usr/local/bin/ffmpeg
  install -m 0755 "$tmp/x/bin/ffprobe" /usr/local/bin/ffprobe
  hash -r 2>/dev/null || true
  # Report the binary that was just written, by path. Reading `ffmpeg` through
  # PATH instead reports whatever PATH happens to resolve -- which on a host
  # where /usr/local/bin is not first is the OLD build, so a successful upgrade
  # announces the version it just replaced.
  ok "installed $(/usr/local/bin/ffmpeg -version 2>/dev/null | head -1 | cut -d' ' -f1-3) to /usr/local/bin"
  if [ "$(command -v ffmpeg)" != "/usr/local/bin/ffmpeg" ]; then
    warn "PATH still resolves ffmpeg to $(command -v ffmpeg) — polyemesis will use that one."
    echo "     Put /usr/local/bin ahead of it, or set the ffmpeg path in the config."
  fi
  echo "     The distro package is untouched at /usr/bin/ffmpeg. To go back:"
  echo "     rm /usr/local/bin/ffmpeg /usr/local/bin/ffprobe"
}

# check_ffmpeg enforces the two separate things that go wrong, because they
# fail differently and only one of them is fatal.
check_ffmpeg() {
  command -v ffmpeg >/dev/null 2>&1 || {
    err "no ffmpeg on PATH."
    echo "     polyemesis shells out to FFmpeg for everything. Install ${FFMPEG_MIN_MAJOR}.0 or newer,"
    echo "     or re-run and choose the docker mode, which bundles one."
    case "${DISTRO}" in
      # Fedora has no `ffmpeg` package. ffmpeg-free is 7.1.5 and DOES carry
      # libsrt -- checked, because recommending a build without it would be
      # worse than saying nothing.
      fedora)  echo "     On Fedora the package is \`ffmpeg-free\`: dnf install -y ffmpeg-free" ;;
      rocky|almalinux|rhel|centos)
               echo "     On this family the default repos have neither. EPEL's ffmpeg-free is"
               echo "     5.1.x, below the floor. Use RPM Fusion, a static build, or docker." ;;
    esac
    return 1
  }

  local raw major
  # [^0-9]* before the digits, because a release tarball from the very place
  # this script recommends announces itself as "ffmpeg version n8.1.2-34-g...".
  # The old pattern demanded a digit immediately after "version", so it failed
  # to parse exactly the builds recommended below and reported them as unknown.
  # A master build ("ffmpeg version N-119534-g...") still parses to nothing,
  # which is correct -- it has no major to compare.
  raw="$(ffmpeg -version 2>/dev/null | head -1 | sed -n 's/^ffmpeg version [^0-9]*\([0-9][0-9]*\)\..*/\1/p')"
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
        # Measured, not assumed: EPEL on Rocky 9 carries ffmpeg-free 5.1.9, and
        # there is no `ffmpeg` package at all. Both miss the floor.
        rocky:*|almalinux:*|rhel:*|centos:*)
          echo "     EPEL ships ffmpeg-free 5.1.x here, which is below the floor, and there"
          echo "     is no \`ffmpeg\` package. RPM Fusion or a static build, or use docker." ;;
      esac
      echo "     Options: a newer distribution, a static build with libsrt"
      echo "     (https://github.com/BtbN/FFmpeg-Builds/releases), or the docker mode."
      return 1
    fi
    if [ "$major" -lt "$FFMPEG_RECOMMENDED_MAJOR" ]; then
      ok "ffmpeg $major.x clears the ${FFMPEG_MIN_MAJOR}.0 floor"
      warn "ffmpeg $major.x is older than the ${FFMPEG_RECOMMENDED_MAJOR}.x the container ships."
      offer_ffmpeg_upgrade "$major"
    else
      ok "ffmpeg $major.x matches the ${FFMPEG_RECOMMENDED_MAJOR}.x the container ships"
    fi
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
  if [ "$MODE_SET" = true ]; then
    info "mode given on the command line; not asking"
  else
    local choice
    ask "Choose 1 or 2" "1" choice
    case "$choice" in
      2|binary) MODE="binary" ;;
      *)        MODE="docker" ;;
    esac
  fi
  ok "mode: $MODE"

  header "=== Ports ==="
  [ "$HTTP_PORT_SET" = true ] || ask "Web UI port (tcp)" "$HTTP_PORT" HTTP_PORT
  [ "$SRT_PORT_SET" = true ]  || ask "SRT ingest port (UDP — this is the one people forget)" "$SRT_PORT" SRT_PORT
  [ "$RTMP_SET" = true ]      || ask "RTMP ingest port (tcp — 0 to decline it)" "$RTMP_PORT" RTMP_PORT
  # The port IS the switch, server-side too: internal/engine binds both
  # listeners and treats 0 as off. Asking a yes/no here and a port there meant
  # two different ways to say the same thing.
  case "$RTMP_PORT" in 0|"") ENABLE_RTMP="no" ;; *) ENABLE_RTMP="yes" ;; esac

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
  if [ "$TLS_SET" = true ]; then
    info "TLS mode given on the command line; not asking"
  else
    local tls_choice
    ask "Choose 1, 2 or 3" "2" tls_choice
    case "$tls_choice" in
      1|off)  TLS_MODE="off" ;;
      3|acme) TLS_MODE="acme" ;;
      *)      TLS_MODE="selfsigned" ;;
    esac
  fi

  if [ "$TLS_MODE" = "acme" ]; then
    # Both values below are required and are validated by re-prompting, so
    # neither loop can make progress when there is nothing to prompt. Say which
    # flag is missing and stop, rather than spinning on a question nobody is
    # there to answer.
    if ! interactive; then
      [ -n "$DOMAIN_NAME" ] || die "--tls acme needs a public DNS name: pass --hostname stream.example.com"
      [ -n "$ACME_EMAIL" ]  || die "--tls acme needs a contact address: pass --email you@example.com"
    fi
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
  # 443 IS THE POINT OF TURNING TLS ON, and the port was asked for above --
  # before the operator knew they would be serving HTTPS at all. Left alone,
  # the default 8080 gives a working but unlovely install: the :80 redirect
  # correctly sends browsers to https://host:8080, every link carries the port,
  # and nothing listens on the port people actually try first.
  #
  # Only offered when the port is still the untouched default. A port given on
  # the command line, or typed at the prompt, is a decision and is left alone.
  if [ "$TLS_MODE" != "off" ] && [ "$HTTP_PORT_SET" != true ] && [ "$HTTP_PORT" = "8080" ]; then
    echo
    echo "  HTTPS is normally served on 443, so browsers reach it without a port."
    echo "  The service unit already grants CAP_NET_BIND_SERVICE, so an"
    echo "  unprivileged process can bind it."
    ask_yn "Serve HTTPS on 443 instead of 8080?" "y" USE_443
    if [ "$USE_443" = yes ]; then
      HTTP_PORT=443
      warn_if_taken "$HTTP_PORT" tcp "web UI"
    fi
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

    # The image bakes in `wget -qO- http://127.0.0.1:8080/api/v1/health`, and
    # both halves of that are assumptions this installer is free to break.
    #
    #   --http-port N   moves the listener, so the probe gets ECONNREFUSED
    #   any TLS mode    makes :8080 speak HTTPS, so a plain-HTTP probe gets 400
    #
    # The second one fires on the DEFAULT install: the interactive TLS default
    # is selfsigned, including under --yes. `install.sh --mode docker --yes`
    # therefore produced a container that reported `unhealthy` forever while the
    # server answered every request correctly -- measured at failing streak 5,
    # `wget: server returned error: HTTP/1.0 400 Bad Request`, with the summary
    # printing "server is up" and exiting 0.
    #
    # That is worse than cosmetic. `docker ps` is where an operator looks first,
    # and anything gating on health -- a compose `depends_on: service_healthy`,
    # an orchestrator, a load balancer -- treats a working server as down.
    #
    # --no-check-certificate is correct rather than lax here: selfsigned is the
    # default and acme certificates are issued for the public hostname, so a
    # loopback probe cannot present a name either one validates against.
    if [ "$TLS_MODE" = "off" ]; then
      printf '    healthcheck:\n      test: ["CMD-SHELL", "wget -qO- http://127.0.0.1:%s/api/v1/health || exit 1"]\n' "$HTTP_PORT"
    else
      printf '    healthcheck:\n      test: ["CMD-SHELL", "wget -qO- --no-check-certificate https://127.0.0.1:%s/api/v1/health || exit 1"]\n' "$HTTP_PORT"
    fi
    printf '      interval: 30s\n'
    printf '      timeout: 3s\n'
    printf '      start_period: 10s\n'
    printf 'volumes:\n'
    printf '  polyemesis-data:\n'
    # Compose prefixes volumes with the project name unless the volume pins its
    # own, so this becomes `polyemesis_polyemesis-data` while update.sh,
    # uninstall.sh and the summary below all say `polyemesis-data`. The mismatch
    # is silent in the one place it costs the database: `docker volume rm` errors
    # on a name that does not exist, but `docker run -v` creates it, so the
    # backup in update.sh succeeds and archives an empty directory.
    printf '    name: polyemesis-data\n'
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

  # Record what was actually created. `mkdir -p` succeeds on a directory that
  # already exists, so a blanket DIRS_CREATED=true made rollback delete an
  # EXISTING operator config on a failed re-run -- destroying more than the
  # failure it was recovering from.
  [ -d "$CONFIG_DIR" ] || CONFIG_DIR_CREATED=true
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
  # Ports below 1024 are privileged and this unit runs unprivileged. TWO ports
  # can need it, and gating on acme alone covered only one:
  #
  #   :80  -- the ACME http-01 challenge. Without the capability the bind fails,
  #           polyemesis warns and keeps serving HTTPS, and issuance never
  #           completes.
  #   :443 -- the web UI itself, whenever the operator took the 443 offer. That
  #           is reachable with mode selfsigned, which is the DEFAULT choice, so
  #           gating on acme meant the service could not bind the port the
  #           installer had just written into its own ExecStart.
  case "$TLS_MODE:$HTTP_PORT" in
    acme:*|*:443|*:80)
      caps=$'AmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE'
      ;;
  esac

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

  # `enable --now` starts a stopped unit and does nothing at all to a running
  # one, so re-running the installer over a live install replaced the binary on
  # disk and left the old process serving from the now-deleted inode. The
  # installer printed its success summary, and `polyemesis --version` -- the
  # check UPGRADING.md tells you to run -- reported the NEW version off disk
  # while the OLD one was still answering requests. Restart explicitly.
  #
  # Ordered after enable --now rather than replacing it: on a first install the
  # unit has to be enabled for boot as well as started, and restart alone does
  # not do that.
  systemctl restart "$SERVICE_NAME" || die "the service did not come back after restart — journalctl -u ${SERVICE_NAME} -n 50"
  ok "service enabled and started"
}

# ------------------------------------------------------------------ firewall

configure_firewall() {
  [ "$CONFIGURE_FIREWALL" = yes ] || return 0
  if command -v ufw >/dev/null 2>&1; then
    # HTTP_PORT is 443 whenever the operator took the offer above, so this is
    # what opens HTTPS. It is deliberately not a separate `ufw allow 443` --
    # opening a port nothing binds would look like working TLS and serve
    # nothing, which is harder to diagnose than a closed port.
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

# The binary install's upgrade script, and the reason it exists is in
# write_helper_scripts above.
#
# THE KEY FILE IS THE POINT. A count of files proves the backup is not empty; it
# does not prove it holds the one file whose absence cannot be recovered from.
# Since 0.7.0 seals destination stream keys at rest, a database restored WITHOUT
# secret.key comes back with every destination disabled -- by design, because a
# key that will not open disables its destination rather than failing open. That
# restore looks completely successful until the moment someone goes live.
write_binary_update_script() {
  mkdir -p "$INSTALL_DIR"
  cat > "$INSTALL_DIR/update.sh" <<EOF
#!/usr/bin/env bash
# Back up the data directory BEFORE replacing the binary: migrations run forward
# only and there is no downgrade path. The backup is the only way back.
set -euo pipefail

DATA_DIR="$DATA_DIR"
BIN_PATH="$BIN_PATH"
SERVICE_NAME="$SERVICE_NAME"

stamp="\$(date +%F-%H%M)"
dest="\${DATA_DIR}.bak-\${stamp}"

# A missing or empty data directory means the backup would archive nothing,
# exit 0, and let the upgrade proceed with no way back. Refuse instead.
if [ ! -d "\$DATA_DIR" ]; then
  echo "ERROR: \$DATA_DIR does not exist. Refusing to upgrade." >&2
  exit 1
fi
if [ -z "\$(ls -A "\$DATA_DIR" 2>/dev/null)" ]; then
  echo "ERROR: \$DATA_DIR is empty. Refusing to upgrade: the backup would be empty" >&2
  echo "and migrations do not roll back." >&2
  exit 1
fi

if [ -e "\$dest" ]; then
  echo "ERROR: \$dest already exists. Refusing to upgrade: cp would nest the copy" >&2
  echo "inside it and the checks below would pass against the wrong directory." >&2
  exit 1
fi

echo "backing up \$DATA_DIR to \$dest"
cp -a "\$DATA_DIR" "\$dest"

# THE CHECK THAT MATTERS. Not "is there a backup" but "does the backup hold the
# file that makes the database usable". Without secret.key every destination
# comes back disabled and the restore reads as successful until go-live.
if [ ! -f "\$dest/secret.key" ]; then
  echo "ERROR: the backup at \$dest has no secret.key." >&2
  echo "Restoring a database without it leaves every destination disabled, because" >&2
  echo "a stream key that will not open disables its destination rather than" >&2
  echo "failing open. Refusing to upgrade." >&2
  exit 1
fi
if [ ! -f "\$dest/polyemesis.db" ]; then
  echo "ERROR: the backup at \$dest has no polyemesis.db. Refusing to upgrade." >&2
  exit 1
fi

echo "backup verified: database and secret.key both present"
echo
echo "Now replace \$BIN_PATH with the new binary and restart:"
echo
echo "    sudo systemctl stop \$SERVICE_NAME"
echo "    sudo install -m 0755 ./polyemesis \$BIN_PATH"
echo "    sudo systemctl start \$SERVICE_NAME"
echo
echo "If the upgrade goes wrong, the way back is:"
echo
echo "    sudo systemctl stop \$SERVICE_NAME"
echo "    sudo rm -rf \$DATA_DIR && sudo cp -a \$dest \$DATA_DIR"
echo "    (then reinstall the previous binary)"
EOF
  chmod +x "$INSTALL_DIR/update.sh"
}

write_helper_scripts() {
  # BINARY MODE GETS ONE TOO. It used to return here unless the mode was docker,
  # which left the install that most needs a guard rail with the least: docker
  # operators got a script that REFUSES to upgrade on an empty backup, and
  # systemd operators got the same procedure written in UPGRADING.md, where
  # nothing checks whether they ran it or whether it worked. Migrations are
  # forward-only in both modes. See #347.
  if [ "$MODE" != "docker" ]; then
    write_binary_update_script
    return 0
  fi

  cat > "$INSTALL_DIR/update.sh" <<EOF
#!/usr/bin/env bash
# Back up the volume BEFORE pulling: migrations run forward only and there is
# no downgrade path. The backup is the only way back.
set -euo pipefail
cd "$INSTALL_DIR"
stamp="\$(date +%F-%H%M)"
echo "backing up to $INSTALL_DIR/backup-\${stamp}.tar.gz"

# \`docker run -v\` CREATES a missing volume rather than failing, so a wrong or
# renamed volume backs up an empty directory, exits 0, and the upgrade below
# proceeds with no way back. Refuse to continue unless the volume already exists.
docker volume inspect polyemesis-data >/dev/null 2>&1 || {
  echo "ERROR: no docker volume named 'polyemesis-data'." >&2
  echo "Refusing to upgrade: the backup would be empty and migrations do not roll back." >&2
  echo "Volumes on this host:" >&2
  docker volume ls >&2
  exit 1
}

docker run --rm -v polyemesis-data:/data -v "$INSTALL_DIR:/backup" alpine \\
  tar czf "/backup/backup-\${stamp}.tar.gz" -C /data .

# A backup that exists but holds nothing is worse than no backup, because it
# reads as success. tar always writes the './' entry, so anything under two
# entries means the volume was empty.
entries="\$(tar tzf "$INSTALL_DIR/backup-\${stamp}.tar.gz" | wc -l)"
if [ "\$entries" -lt 2 ]; then
  echo "ERROR: backup archive is empty (\${entries} entries). Refusing to upgrade." >&2
  exit 1
fi
  # AND THE ONE FILE THE COUNT CANNOT VOUCH FOR. A non-empty archive proves the
  # volume held something, not that it held the file that makes the database
  # usable. Restoring without secret.key brings every destination back DISABLED
  # -- correctly, since a key that will not open disables its destination rather
  # than failing open -- and that reads as a successful restore until go-live.
  if ! tar tzf "$INSTALL_DIR/backup-\${stamp}.tar.gz" | grep -q "secret\.key"; then
    echo "ERROR: the backup contains no secret.key. Restoring without it leaves" >&2
    echo "every destination disabled. Refusing to upgrade." >&2
    exit 1
  fi
echo "backup verified: \${entries} entries"
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

usage() {
  cat <<'USAGE'
polyemesis installer

  sudo bash install.sh [options]

With no options it asks. Every question has a flag, so the same script can run
unattended from cloud-init, Ansible, or a CI job -- which is also the only way
it gets tested.

Options:
  --mode docker|binary   install mode (default: ask, then docker)
  --http-port N          web UI port (default 8080)
  --srt-port N           SRT ingest port, UDP (default 6000)
  --rtmp-port N          RTMP ingest port, tcp (default 1935; 0 declines it)
  --rtmp                 accepted for compatibility; RTMP is published by default
  --tls off|selfsigned|acme
                         TLS mode. Not passing it takes the interactive
                         default, which is SELFSIGNED -- including under --yes.
                         Pass --tls off explicitly if that is what you want.
  --hostname NAME        hostname for acme (sets DOMAIN_NAME)
  --email ADDR           contact address for acme
  --ffmpeg MODE          ask (default), skip, or force an FFmpeg upgrade when
                         the installed one is older than the container's
  --yes, -y              accept defaults; never prompt
  --check                run the preflight checks and exit, changing nothing
  --help, -h             this text

Exit status is 0 only if the install finished. --check exits non-zero when the
host cannot support the chosen mode.
USAGE
}

# ASSUME_YES suppresses every prompt. CHECK_ONLY runs the preflight and stops,
# which is what CI runs: it exercises detection and the gates on a real distro
# without installing anything or needing a daemon.
ASSUME_YES=false
CHECK_ONLY=false

# Which answers came from the command line.
#
# Without these, parse_args set MODE and gather_configuration immediately asked
# the same question and overwrote it -- so `--mode binary --yes` took the
# prompt default and installed DOCKER. A root installer that silently does
# something other than what it was told is the worst bug in this file, and it
# survived local testing because --check exits before gather_configuration
# ever runs.
MODE_SET=false
TLS_SET=false
RTMP_SET=false
HTTP_PORT_SET=false
SRT_PORT_SET=false

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --mode)       [ $# -ge 2 ] || die "missing value for --mode"; MODE="$2"; MODE_SET=true; shift 2 ;;
      --mode=*)     MODE="${1#*=}"; MODE_SET=true; shift ;;
      --http-port)  [ $# -ge 2 ] || die "missing value for --http-port"; HTTP_PORT="$2"; HTTP_PORT_SET=true; shift 2 ;;
      --http-port=*) HTTP_PORT="${1#*=}"; HTTP_PORT_SET=true; shift ;;
      --srt-port)   [ $# -ge 2 ] || die "missing value for --srt-port"; SRT_PORT="$2"; SRT_PORT_SET=true; shift 2 ;;
      --srt-port=*) SRT_PORT="${1#*=}"; SRT_PORT_SET=true; shift ;;
      --rtmp)       ENABLE_RTMP="yes"; RTMP_SET=true; shift ;;
      --rtmp-port)  [ $# -ge 2 ] || die "missing value for --rtmp-port"; RTMP_PORT="$2"; RTMP_SET=true; shift 2 ;;
      --rtmp-port=*) RTMP_PORT="${1#*=}"; RTMP_SET=true; shift ;;
      --tls)        [ $# -ge 2 ] || die "missing value for --tls"; TLS_MODE="$2"; TLS_SET=true; shift 2 ;;
      --tls=*)      TLS_MODE="${1#*=}"; TLS_SET=true; shift ;;
      --hostname)   [ $# -ge 2 ] || die "missing value for --hostname"; DOMAIN_NAME="$2"; shift 2 ;;
      --hostname=*) DOMAIN_NAME="${1#*=}"; shift ;;
      --email)      [ $# -ge 2 ] || die "missing value for --email"; ACME_EMAIL="$2"; shift 2 ;;
      --email=*)    ACME_EMAIL="${1#*=}"; shift ;;
      --ffmpeg)     [ $# -ge 2 ] || die "missing value for --ffmpeg"
                    case "$2" in ask|skip|force) FFMPEG_UPGRADE="$2" ;;
                      *) die "--ffmpeg takes ask, skip or force" ;; esac; shift 2 ;;
      -y|--yes)     ASSUME_YES=true; shift ;;
      --check)      CHECK_ONLY=true; ASSUME_YES=true; shift ;;
      -h|--help)    usage; trap - EXIT INT TERM; exit 0 ;;
      *)            usage; echo; die "unknown option: $1" ;;
    esac
  done

  case "$MODE" in
    ""|docker|binary) ;;
    *) die "--mode must be docker or binary, not $MODE" ;;
  esac
  case "$TLS_MODE" in
    off|selfsigned|acme) ;;
    *) die "--tls must be off, selfsigned or acme, not $TLS_MODE" ;;
  esac
  for pv in HTTP_PORT SRT_PORT; do
    case "${!pv}" in
      ''|*[!0-9]*) die "$pv must be a number, not ${!pv}" ;;
    esac
    # Range too. Port 0 is a number and is not a port: Docker reads it as
    # "allocate me anything", and the summary would then advertise :0.
    if [ "${!pv}" -lt 1 ] || [ "${!pv}" -gt 65535 ]; then
      die "$pv must be between 1 and 65535, not ${!pv}"
    fi
  done
}

main() {
  parse_args "$@"
  header "polyemesis installer"
  require_root
  detect_os

  # --check stops here, having proved only what a host can be asked without
  # changing it. This is what CI runs on every distro in the matrix: it
  # exercises detection, the architecture gate, the init gate and the FFmpeg
  # floor, and installs nothing.
  if [ "$CHECK_ONLY" = true ]; then
    local rc=0
    if [ "${MODE:-docker}" = "binary" ]; then
      check_ffmpeg || rc=1
      require_systemd || rc=1
    else
      info "docker mode: the image carries FFmpeg, so no host FFmpeg is required"
      require_systemd || rc=1
    fi
    [ "$rc" -eq 0 ] && ok "preflight passed" || err "preflight failed"
    # Detach the trap rather than setting INSTALL_COMPLETE. That flag makes
    # cleanup_on_failure `exit 0`, which is right for a finished install and
    # WRONG here: it swallowed the non-zero status and every --check reported
    # success while printing its own failures. Nothing has been created at this
    # point, so there is nothing to undo and no reason to run the trap at all.
    trap - EXIT INT TERM
    exit "$rc"
  fi

  gather_configuration
  confirm_plan

  require_systemd || die "cannot install a service without systemd — see above"

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
