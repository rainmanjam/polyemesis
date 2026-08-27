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
# Whether a polyemesis container was already running when this run began. Read
# only by the rollback, to tell "we started it" from "it was already serving".
CONTAINER_PREEXISTING=false
# The same question for the binary mode's two artefacts. `install -m 0755` and
# `cat >` both succeed over an existing file, so without these the rollback
# could not tell a first install from a re-run and deleted the binary and the
# unit of a working install it had merely replaced.
BIN_PREEXISTING=false
UNIT_PREEXISTING=false
BINARY_INSTALLED=false
CONFIG_DIR_CREATED=false
# Same reasoning as CONFIG_DIR_CREATED, and the reason it needed its own flag is
# recorded beside every `mkdir -p "$INSTALL_DIR"`: rollback used to delete an
# install directory this run had merely opened, taking docker-compose.yml,
# uninstall.sh and every backup-*.tar.gz update.sh had written there with it.
INSTALL_DIR_CREATED=false
INSTALL_COMPLETE=false
# Set by cleanup_on_failure so the EXIT trap cannot re-run it after INT/TERM.
ROLLBACK_DONE=false

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
  # ONCE. The trap is armed for EXIT INT TERM, so a Ctrl-C ran this twice: SIGINT
  # fired the INT handler, which cleaned up and exited, and that exit fired the
  # EXIT handler, which cleaned up again. The operator saw the same failure
  # reported twice and had no way to tell that from "the first rollback failed
  # and it retried".
  #
  # Harmless today only because every action below is idempotent -- rm -f,
  # compose down, systemctl disable. That is a property of the current contents,
  # not of the design, and the next step added here would inherit the bug
  # silently.
  [ "$ROLLBACK_DONE" = true ] && exit "$code"
  ROLLBACK_DONE=true
  [ "$INSTALL_COMPLETE" = true ] && exit 0
  [ "$code" -eq 0 ] && exit 0

  echo
  warn "install failed (exit $code) — undoing what it had already done"

  # Only a container THIS RUN brought up. A re-run over a healthy install -- to
  # change a port, add TLS, upgrade -- used to `compose down` the operator's
  # already-running production container on any later failure, so a 60s verify()
  # timeout took a live broadcast off air. A container that was already up is
  # the operator's; the worst this run did to it was recreate it, and leaving it
  # serving is strictly better than stopping it.
  if [ "$CONTAINER_STARTED" = true ] && [ -n "$COMPOSE_CMD" ]; then
    (cd "$INSTALL_DIR" && $COMPOSE_CMD down --remove-orphans) >/dev/null 2>&1 || true
    info "stopped and removed the container"
  elif [ "$CONTAINER_PREEXISTING" = true ]; then
    warn "left the container running — it predates this run."
    echo "     It may now be running the configuration this run wrote. Your previous"
    echo "     config.yaml is beside it as a .bak- snapshot in $INSTALL_DIR."
  fi
  # THE SAME QUESTION AS THE CONTAINER ABOVE, asked of the other install mode.
  # `cat >` and `install -m 0755` both succeed over an existing file, so a
  # failed re-run over a WORKING systemd install used to disable the operator's
  # service, delete its unit file and delete the binary -- leaving a host with
  # no polyemesis on it at all, recovering from a failure that had broken
  # nothing. The new binary and the new unit are already on disk by then and the
  # service is serving from them; that is imperfect and it is not "nothing
  # installed", which is what deleting them produces.
  if [ "$UNIT_CREATED" = true ] && [ "$UNIT_PREEXISTING" != true ]; then
    systemctl disable --now "$SERVICE_NAME" >/dev/null 2>&1 || true
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
    systemctl daemon-reload >/dev/null 2>&1 || true
    info "removed the systemd unit"
  elif [ "$UNIT_PREEXISTING" = true ]; then
    warn "left the ${SERVICE_NAME} unit in place — it predates this run."
    echo "     It now holds what this run wrote. Your previous copy is beside it as a"
    echo "     .bak- snapshot in /etc/systemd/system/."
  fi
  if [ "$BINARY_INSTALLED" = true ] && [ "$BIN_PREEXISTING" != true ]; then
    rm -f "$BIN_PATH" && info "removed $BIN_PATH"
  elif [ "$BIN_PREEXISTING" = true ]; then
    warn "left $BIN_PATH in place — a binary was already there and this run replaced it."
  fi
  if [ "$DIRS_CREATED" = true ]; then
    # NOT $DATA_DIR. It holds the database, secret.key and any recording made
    # between the failing step and now. An installer that deletes a data
    # directory on the way out is a worse problem than the one it is recovering
    # from, so it is left in place and named.
    #
    # And only if THIS run created $INSTALL_DIR. See the note beside the mkdir:
    # a blanket delete here destroyed the operator's docker-compose.yml, their
    # uninstall.sh and EVERY backup-*.tar.gz update.sh had written into it --
    # announced as `[info] removed /opt/polyemesis`, which is the quietest
    # possible line for the most damaging action in this file.
    if [ "$INSTALL_DIR_CREATED" = true ]; then
      rm -rf "$INSTALL_DIR"
      info "removed $INSTALL_DIR"
    elif [ -d "$INSTALL_DIR" ]; then
      info "left $INSTALL_DIR alone — it predates this run (it holds your compose file and any backups)"
    fi
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

# Installing a release binary whose checksum CANNOT BE CHECKED, as opposed to
# one whose checksum is wrong. Both end with an unverified binary running as a
# service; only this one used to happen without anybody choosing it. Off, so the
# exception is a flag somebody typed.
ALLOW_UNVERIFIED=false

# Set when this run installs the static FFmpeg, and consumed by the config
# writer.
#
# THIS IS THE POKA-YOKE, AND IT REPLACES AN INSTRUCTION. The warning below used
# to read "Put /usr/local/bin ahead of it, or set the ffmpeg path in the config"
# -- asking the operator to perform an edit, to a file THIS SCRIPT IS CREATING,
# in the same run. internal/config.FFmpeg has Binary and Probe (config.go:107),
# and the config.yaml emitted here carried no ffmpeg block at all.
#
# Writing the absolute path means PATH ORDER STOPS MATTERING, which is better
# than getting PATH order right. The failure it removes is silent: the service
# starts against the older FFmpeg and misbehaves later, somewhere else.
FFMPEG_PINNED_BIN=""
FFMPEG_PINNED_PROBE=""

ffmpeg_static_asset() {
  # BtbN publishes per-architecture GPL tarballs. Only these two are built.
  case "$ARCH" in
    amd64) echo "ffmpeg-n8.1-latest-linux64-gpl-8.1.tar.xz" ;;
    arm64) echo "ffmpeg-n8.1-latest-linuxarm64-gpl-8.1.tar.xz" ;;
    *)     echo "" ;;
  esac
}

# offer_ffmpeg_upgrade takes the major you have (empty when there is no FFmpeg
# at all) and NEED, which is "optional" or "required".
#
# THE TWO CALLERS WANT OPPOSITE THINGS AND THE DIFFERENCE IS NOT COSMETIC.
#
#   optional -- you have 6.x or 7.x. Everything works; what 8.x adds is
#               multitrack FLV. Declining costs you one feature, so the default
#               is "no" and the return value is ignored.
#
#   required -- you have nothing, or something below the floor. polyemesis
#               REFUSES TO START against it, so declining ends the install. The
#               default is "yes", the wording says what is actually at stake,
#               and the return value decides whether check_ffmpeg fails.
#
# Returns 0 only when a usable FFmpeg is on PATH afterwards. That is stricter
# than "the files were written": /usr/local/bin is ahead of /usr/bin on a
# DEFAULT path and not on every host, and an install that put a good binary
# somewhere PATH does not reach is a failure that used to report success.
offer_ffmpeg_upgrade() {
  local have="$1" need="${2:-optional}" asset answer tmp label default_answer
  asset="$(ffmpeg_static_asset)"
  # "6.x" reads wrong when there is no FFmpeg at all.
  if [ -n "$have" ]; then label="$have.x"; else label="no FFmpeg"; fi

  if [ "$need" = "required" ]; then
    echo "     polyemesis shells out to FFmpeg for everything and will not start"
    echo "     against ${label}. A current static build fixes that without touching"
    echo "     your distribution's package."
  else
    echo "     What you have keeps working: SRT carries every audio track on any"
    echo "     version above the floor, and nothing about routing, recording or"
    echo "     destinations depends on this."
    echo "     What ${FFMPEG_RECOMMENDED_MAJOR}.x adds is multitrack FLV. Below 7.1 an Enhanced RTMP"
    echo "     publisher sending several audio tracks arrives as ONE track, with no"
    echo "     error on either end -- which is the part worth knowing, because it"
    echo "     looks like the tracks were never sent."
  fi

  # --check INSTALLS NOTHING, AND THAT PROMISE OUTRANKS THIS OFFER.
  #
  # Its whole contract is that it proves what a host can be asked without
  # changing it -- it is what CI runs across the distro matrix, including the
  # two images that are deliberately below the floor. Without this guard,
  # `--check --ffmpeg force` would download and replace a system binary during
  # a preflight, which is the one thing --check says it will not do. Reported
  # rather than silently skipped, so the operator learns the fix exists.
  if [ "${CHECK_ONLY:-false}" = true ]; then
    echo "     --check makes no changes. Re-run without it (or with --ffmpeg force)"
    echo "     to install FFmpeg ${FFMPEG_RECOMMENDED_MAJOR}.x to /usr/local/bin."
    return 1
  fi

  if [ -z "$asset" ]; then
    warn "no static build is published for $ARCH — staying on ${label}"
    echo "     Use the docker mode, which bundles ${FFMPEG_RECOMMENDED_MAJOR}.x for every architecture."
    return 1
  fi

  case "$FFMPEG_UPGRADE" in
    skip)
      echo "     Skipping the install (--ffmpeg skip). Staying on ${label}."
      return 1 ;;
    force) answer=yes ;;
    *)
      if ! interactive; then
        # Unattended. Installing a system binary nobody asked for is not a
        # default worth taking silently -- and that holds even when it is
        # required, because the honest outcome there is a refusal that names
        # the flag, not a system binary replaced by a script nobody watched.
        warn "unattended: leaving ${label}. Re-run with --ffmpeg force to install ${FFMPEG_RECOMMENDED_MAJOR}.x."
        return 1
      fi
      # Default "n" when the upgrade is OPTIONAL. ask() returns the default
      # whenever nothing can answer -- and interactive() can still be true on a
      # host where /dev/tty is readable but no one is reading it, so a "y"
      # default would replace a system binary on the strength of a question
      # nobody saw. Someone who wants this either types y or passes
      # --ffmpeg force.
      #
      # Default "y" when it is REQUIRED, and the same reasoning is what permits
      # it: the alternative to installing is not "keep what you have", it is
      # "this install stops here". A question nobody saw then defaults to the
      # outcome the operator was asking for by running the installer at all.
      if [ "$need" = "required" ]; then default_answer=y; else default_answer=n; fi
      ask_yn "Install FFmpeg ${FFMPEG_RECOMMENDED_MAJOR}.x to /usr/local/bin now?" "$default_answer" answer ;;
  esac

  if [ "$answer" != "yes" ]; then
    if [ "$need" = "required" ]; then
      echo "     Declined. Nothing was changed, and this install cannot continue"
      echo "     against ${label} — see the options above."
      return 1
    fi
    echo "     Left at ${label}. Enhanced RTMP multitrack will arrive as a single"
    echo "     track; everything else behaves the same. Re-run with --ffmpeg force"
    echo "     to change this later."
    return 1
  fi

  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" RETURN

  echo "     Fetching $asset ..."
  if ! fetch_https "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/$asset" \
        "$tmp/ff.tar.xz"; then
    warn "download failed — staying on ${label}. Nothing was changed."
    warn "(needs one of curl, wget or python3; this host appears to have none)"
    return 1
  fi

  # VERIFY BEFORE EXTRACTING, AND BEFORE RUNNING IT AS ROOT.
  #
  # This installer refuses its OWN binary without a matching SHA256SUMS, and
  # fetched a third-party FFmpeg with no integrity check at all -- then
  # extracted it and EXECUTED it as root to probe for libsrt. Whatever was
  # published at that moment ran on the operator's box. The asymmetry was the
  # finding: strict about us, silent about them.
  #
  # BtbN publishes one checksums.sha256 per release covering every asset, in
  # the `<hash>  <name>` form sha256sum -c reads directly -- the same idiom
  # install_binary_mode uses for our own download.
  if ! fetch_https "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/checksums.sha256" \
        "$tmp/checksums.sha256"; then
    warn "could not fetch BtbN's checksums.sha256 — refusing to install an"
    warn "unverified FFmpeg. Staying on ${label}. Nothing was changed."
    warn "Install FFmpeg 6.0+ with libsrt by hand if you need it sooner."
    return 1
  fi
  mv "$tmp/ff.tar.xz" "$tmp/$asset"
  if ! (cd "$tmp" && grep " ${asset}\$" checksums.sha256 | sha256sum -c --status -); then
    warn "CHECKSUM MISMATCH for $asset — refusing it. Staying on ${label}."
    warn "Nothing was changed, and nothing from that download was run."
    return 1
  fi
  mv "$tmp/$asset" "$tmp/ff.tar.xz"
  echo "     checksum verified"

  mkdir -p "$tmp/x"
  if ! tar xf "$tmp/ff.tar.xz" --strip-components=1 -C "$tmp/x" 2>/dev/null; then
    warn "the archive did not extract — staying on ${label}. Nothing was changed."
    return 1
  fi

  # Verify BEFORE displacing anything: a build without libsrt would be a
  # downgrade in capability dressed as an upgrade in version.
  if ! "$tmp/x/bin/ffmpeg" -hide_banner -protocols 2>/dev/null | tr ' ' '\n' | grep -qx srt; then
    warn "the downloaded build has no libsrt — refusing it, staying on ${label}."
    return 1
  fi

  install -m 0755 "$tmp/x/bin/ffmpeg"  /usr/local/bin/ffmpeg
  install -m 0755 "$tmp/x/bin/ffprobe" /usr/local/bin/ffprobe
  hash -r 2>/dev/null || true
  # Report the binary that was just written, by path. Reading `ffmpeg` through
  # PATH instead reports whatever PATH happens to resolve -- which on a host
  # where /usr/local/bin is not first is the OLD build, so a successful upgrade
  # announces the version it just replaced.
  ok "installed $(/usr/local/bin/ffmpeg -version 2>/dev/null | head -1 | cut -d' ' -f1-3) to /usr/local/bin"
  # Recorded so the generated config can PIN it. See FFMPEG_PINNED_BIN.
  FFMPEG_PINNED_BIN=/usr/local/bin/ffmpeg
  FFMPEG_PINNED_PROBE=/usr/local/bin/ffprobe
  echo "     The distro package is untouched at /usr/bin/ffmpeg. To go back:"
  echo "     rm /usr/local/bin/ffmpeg /usr/local/bin/ffprobe"

  # WROTE THE FILES IS NOT THE SAME AS FIXED THE PROBLEM, and this is where the
  # difference bites. /usr/local/bin is ahead of /usr/bin on a DEFAULT PATH and
  # not on every host -- a trimmed root PATH, or a distro that orders them the
  # other way, leaves the old binary winning. That was already warned about;
  # what is new is that the REQUIRED caller cannot treat a warning as success,
  # because it is about to report a floor as cleared while polyemesis would
  # still shell out to the binary that does not clear it.
  if [ "$(command -v ffmpeg)" != "/usr/local/bin/ffmpeg" ]; then
    warn "PATH still resolves ffmpeg to $(command -v ffmpeg)."
    # NOT "go and fix your PATH" any more. FFMPEG_PINNED_BIN is written into the
    # generated config.yaml as ffmpeg.binary, so polyemesis uses the build this
    # script just verified regardless of what PATH resolves. In binary mode the
    # ambiguity is gone rather than reported.
    echo "     polyemesis will still use ${FFMPEG_PINNED_BIN} — it is pinned in the"
    echo "     generated config, so PATH order does not decide this."
    # Still fatal for the DOCKER-mode preflight and for --check, where no config
    # of ours is written and PATH is genuinely what decides.
    [ "$need" = "required" ] && [ "${MODE:-}" != "binary" ] && return 1
  fi
  return 0
}

# ffmpeg_capability_ok ASKS THE BINARY WHAT IT CAN DO, instead of reading its
# name badge.
#
# Everything else in this file decides on a version STRING parsed out of a
# banner, and internal/ffmpeg/detect.go:155 records where that ends up: an
# unrecognised string becomes "assuming >= 6.0", which is a guess written down.
# It is a REASONABLE guess -- an unparseable banner is nearly always a git build,
# which is newer than 6.0 rather than older -- but it is still the installer
# deciding a capability question by reading prose.
#
# So this measures instead. It exercises two of the three things
# MinMajorVersion's comment names as the reason 6.0 is the floor:
#
#   * the modern channel-layout API used by pan/amerge -- the `pan` filter
#     below is exactly the shape internal/routing compiles for a downmix
#   * `-progress` output carrying the fields the engine parses; out_time_ms is
#     one it actually reads
#
# The third, multi-track MPEG-TS mapping, has no probe this cheap and is left
# to the version check.
#
# ~50ms measured, against lavfi rather than a file, so it costs nothing and
# needs nothing on disk.
ffmpeg_capability_ok() {
  ffmpeg -hide_banner -nostdin -loglevel error -progress pipe:1 \
    -f lavfi -i "anullsrc=channel_layout=stereo:sample_rate=48000" \
    -filter_complex "[0:a]pan=mono|c0=0.5*c0+0.5*c1[a]" \
    -map "[a]" -t 0.1 -f null - 2>/dev/null | grep -q "out_time_ms"
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
    # AND THEN OFFER TO DO IT, rather than only saying how.
    #
    # This script already knows how to install a current FFmpeg -- it does it
    # fifty lines up for hosts that are merely OLD. Withholding the same
    # machinery from the hosts that cannot run polyemesis AT ALL was the wrong
    # way round: it handed a working answer to the operator whose install was
    # going to succeed anyway, and a list of homework to the one whose install
    # was about to stop.
    offer_ffmpeg_upgrade "" required || return 1
    return 0
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
    # A git build, or a distro that rewrites the version banner.
    #
    # THIS USED TO WARN AND WALK ON, handing the operator the banner and asking
    # them to judge it. There is no need to guess: the binary is right here and
    # can simply be asked whether it does what polyemesis needs.
    warn "could not parse the FFmpeg version from its banner:"
    ffmpeg -version | head -1
    if ffmpeg_capability_ok; then
      ok "but it does what polyemesis needs — measured, not parsed"
    else
      err "and it cannot do what polyemesis needs."
      echo "     The channel-layout filtering and -progress output that per-destination"
      echo "     audio routing depends on did not work on this build."
      offer_ffmpeg_upgrade "" required || return 1
    fi
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
      # The second of those three is something this script can just do, and the
      # static build it fetches is the same one the sentence above recommends.
      offer_ffmpeg_upgrade "$major" required || return 1
      return 0
    fi
    if [ "$major" -lt "$FFMPEG_RECOMMENDED_MAJOR" ]; then
      ok "ffmpeg $major.x clears the ${FFMPEG_MIN_MAJOR}.0 floor"
      warn "ffmpeg $major.x is older than the ${FFMPEG_RECOMMENDED_MAJOR}.x the container ships."
      # Optional: the return value is deliberately ignored. Declining here costs
      # one feature, not the install.
      offer_ffmpeg_upgrade "$major" optional || true
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

    # AND THEN OFFER TO FIX IT, which this branch spent its whole life telling
    # people to do by hand.
    #
    # The previous line here was "Fix it with a build configured
    # --enable-libsrt, or use the docker mode" -- while offer_ffmpeg_upgrade,
    # fifty lines up, downloads a build and REFUSES IT unless -protocols lists
    # srt. The artefact this message asked the operator to go and compile was
    # already in the script's hand, tested.
    #
    # This is not a rare path and it is not the version gate: an FFmpeg can
    # clear the 6.0 floor and carry no SRT. Homebrew's does, which
    # docs/INSTALL.md states in three places, and several distro builds do too.
    #
    # `required`, because SRT is the reason to run polyemesis at all -- and
    # because a successful return from that function already means the build
    # carries srt AND that PATH resolves to it, which is exactly the condition
    # this branch is testing for.
    if offer_ffmpeg_upgrade "${major:-}" required; then
      ok "installed an FFmpeg with libsrt — multitrack ingest available"
      return 0
    fi

    # Declining is still allowed, because RTMP genuinely works and this is the
    # operator's call to make. It is now a choice made against a fix that was
    # offered, rather than one made against a reading list.
    echo "       You can also use the docker mode, which bundles a build with SRT."
    local proceed
    ask_yn "Continue without SRT anyway?" "n" proceed
    [ "$proceed" = "yes" ] || return 1
  fi

  # And finally, ask a binary that passed every check above to actually DO the
  # thing. A version can be high enough and a protocol list long enough on a
  # build configured without the filters this depends on.
  #
  # A WARNING RATHER THAN A REFUSAL, DELIBERATELY, AND THE REASON IS EVIDENCE
  # RATHER THAN CAUTION. This probe has been run against exactly one build. The
  # version path above already refuses what it knows to be too old; making an
  # under-tested probe able to block an install on four distributions nobody has
  # run it on would be trusting it further than it has earned.
  #
  # installer.yml now asserts it passes on all four matrix images. Once that has
  # a few runs behind it, this can become a refusal -- and the comment should be
  # deleted when it does.
  if ! ffmpeg_capability_ok; then
    warn "this FFmpeg passed every check above but could not run polyemesis's own filter test."
    echo "     Per-destination audio routing may not work. Report this with the output of:"
    echo "     ffmpeg -version | head -1"
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
    # THE OPERATOR JUST ACCEPTED A FULL DOCKER INSTALL AND IS NOW BEING SENT
    # AWAY TO INSTALL PART OF IT BY HAND.
    #
    # get.docker.com -- which install_docker pipes to a shell a few lines up --
    # ships the compose plugin. So the script either already had it, or knows
    # precisely how to get it, at the moment it gave up.
    warn "Docker Compose not found."
    echo "     polyemesis's docker mode is a compose file; without the plugin there is"
    echo "     nothing to run it with."
    local getcompose=""
    case "${DISTRO}" in
      ubuntu|debian) getcompose="apt-get install -y docker-compose-plugin" ;;
      fedora|rocky|almalinux|rhel|centos) getcompose="dnf install -y docker-compose-plugin" ;;
    esac
    if [ -n "$getcompose" ]; then
      local wantcompose
      ask_yn "Install the compose plugin now?" "y" wantcompose
      if [ "$wantcompose" = "yes" ]; then
        # Not `die` on failure: the re-check below is the verdict, and it gives
        # a better message than the package manager's exit code.
        DEBIAN_FRONTEND=noninteractive $getcompose >/dev/null 2>&1 || true
        if docker compose version >/dev/null 2>&1; then
          COMPOSE_CMD="docker compose"
          ok "compose plugin installed"
        fi
      fi
    fi
    if [ -z "${COMPOSE_CMD:-}" ]; then
      die "Docker Compose not found. Install the compose plugin and re-run."
    fi
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

# next_free_port walks upwards from $1 until nothing is listening. Bounded, so a
# host with a genuinely saturated range says so instead of spinning.
next_free_port() {
  local port="$1" proto="$2" tries=0
  while [ "$tries" -lt 200 ]; do
    port=$((port + 1))
    [ "$port" -le 65535 ] || return 1
    port_in_use "$port" "$proto" || { echo "$port"; return 0; }
    tries=$((tries + 1))
  done
  return 1
}

# warn_if_taken announced a bind failure and then went and caused it.
#
# It fires DURING THE INTERVIEW: the port was typed seconds ago, the operator is
# still sitting there, and nothing has been written yet. Everything needed to
# avoid the failure is in hand at the moment it is predicted, which is the
# definition of a missed poka-yoke.
#
# The variable name is passed in so the corrected value goes back to the caller,
# following the ask/ask_yn convention already used throughout the interview.
warn_if_taken() {
  local port="$1" proto="$2" what="$3" var="${4:-}" free answer
  port_in_use "$port" "$proto" || return 0

  warn "${proto}/${port} (${what}) is already in use — polyemesis would fail to bind it"
  # Nothing to offer, or nowhere to put the answer: fall back to the old
  # behaviour rather than pretending.
  if [ -z "$var" ] || ! free="$(next_free_port "$port" "$proto")"; then
    echo "     Change it, or stop whatever is holding ${proto}/${port}."
    return 0
  fi
  ask_yn "Use ${proto}/${free} for ${what} instead?" "y" answer
  if [ "$answer" = "yes" ]; then
    printf -v "$var" '%s' "$free"
    ok "${what} moved to ${proto}/${free}"
  else
    echo "     Keeping ${proto}/${port}. Stop whatever is holding it before starting polyemesis."
  fi
}

# --------------------------------------------------------------- validation

# resolve_path canonicalizes a path with GNU realpath -m, which tolerates
# components that do not exist yet (DATA_DIR usually doesn't, before mkdir
# runs) and follows any symlink in the part that does. Falls back to a manual
# walk when -m isn't available -- not this installer's Linux target, but this
# script's own tests must still run wherever they're developed.
resolve_path() {
  local p="$1" resolved
  if resolved="$(realpath -m -- "$p" 2>/dev/null)"; then
    printf '%s' "$resolved"
    return 0
  fi
  local dir="$p" tail=""
  while [ -n "$dir" ] && [ "$dir" != "/" ] && ! [ -d "$dir" ]; do
    tail="/$(basename -- "$dir")$tail"
    dir="$(dirname -- "$dir")"
  done
  local existing
  existing="$(cd "$dir" 2>/dev/null && pwd -P)" || existing="$dir"
  if [ -z "$tail" ]; then
    printf '%s' "$existing"
  elif [ "$existing" = "/" ]; then
    printf '%s' "$tail"
  else
    printf '%s%s' "$existing" "$tail"
  fi
}

# validate_data_dir guards the chown -R in install_binary_mode against the
# one place an operator-supplied value reaches it: the "Data directory"
# prompt below. Empty, "/", a ".." component, or a top-level system
# directory (typed directly or reached through a symlink) all die here,
# before confirm_plan even prints a summary -- not after mkdir/chown have
# already run. poka-yoke audit #5.
validate_data_dir() {
  local raw="$1" varname="$2"

  [ -n "$raw" ] || die "data directory is empty — refusing to chown -R an empty/unset path"
  case "$raw" in
    /*) ;;
    *) die "data directory '$raw' is not an absolute path" ;;
  esac
  case "/${raw}/" in
    */../*) die "data directory '$raw' contains a '..' component — refusing" ;;
  esac

  local resolved
  resolved="$(resolve_path "$raw")"

  if [ "$resolved" = "/" ]; then
    die "data directory '$raw' resolves to '/' — refusing to chown -R the entire filesystem"
  fi

  local first="${resolved#/}"
  first="${first%%/*}"
  if [ "$resolved" = "/$first" ]; then
    case "$first" in
      bin|boot|dev|etc|home|lib|lib32|lib64|libx32|media|mnt|opt|proc|root|run|sbin|srv|sys|tmp|usr|var)
        die "data directory '$raw' resolves to /$first — a top-level system directory. Refusing to chown -R it." ;;
    esac
  fi

  if [ "$resolved" != "$raw" ]; then
    warn "data directory '$raw' resolves to '$resolved' — using the resolved path"
  fi
  printf -v "$varname" '%s' "$resolved"
}

# existing_data_dir prints the dataDir an already-installed binary mode is using,
# or nothing when there is no previous install to read.
#
# The point is that a RE-RUN must default to where the data actually is, not to
# the compiled-in constant. See the note at the "Data directory" prompt for what
# that cost. Deliberately conservative: only a quoted or bare `dataDir:` at the
# start of a line, which is what this installer itself writes, and anything it
# cannot parse is reported as "no previous install" rather than guessed at.
existing_data_dir() {
  local f="$CONFIG_DIR/config.yaml" line val
  [ -r "$f" ] || return 0
  line="$(grep -m1 -E '^[[:space:]]*dataDir:' "$f" 2>/dev/null || true)"
  [ -n "$line" ] || return 0
  val="${line#*:}"
  # strip leading/trailing whitespace, then one layer of quotes
  val="${val#"${val%%[![:space:]]*}"}"
  val="${val%"${val##*[![:space:]]}"}"
  case "$val" in
    \"*\") val="${val#\"}"; val="${val%\"}" ;;
    \'*\') val="${val#\'}"; val="${val%\'}" ;;
  esac
  case "$val" in
    /*) printf '%s' "$val" ;;
  esac
}

# preserve_existing snapshots a file this run is about to overwrite, so
# re-running the installer over an existing install cannot silently destroy
# operator edits. Both config.yaml files this installer writes say "Edit and
# `docker compose up -d`/restart to apply" -- an invitation a plain overwrite
# erases without a trace. Never overwrites its own previous snapshot: a
# same-second rerun (as in this script's own tests) gets a numbered suffix
# instead. poka-yoke audit #11.
preserve_existing() { # preserve_existing <path-about-to-be-overwritten>
  local f="$1"
  [ -e "$f" ] || return 0
  local stamp dest suffix="" n=2
  stamp="$(date +%Y%m%dT%H%M%S)"
  dest="${f}.bak-${stamp}"
  while [ -e "${dest}${suffix}" ]; do
    suffix=".${n}"
    n=$((n + 1))
  done
  cp -a "$f" "${dest}${suffix}"
  warn "existing $(basename -- "$f") found — your previous copy was kept at ${dest}${suffix}"
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

  # The variable name goes in so an accepted alternative comes back out.
  warn_if_taken "$HTTP_PORT" tcp "web UI"      HTTP_PORT
  warn_if_taken "$SRT_PORT"  udp "SRT ingest"  SRT_PORT
  [ "$ENABLE_RTMP" = yes ] && warn_if_taken "$RTMP_PORT" tcp "RTMP ingest" RTMP_PORT

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
    # TWO THINGS THAT ARE KNOWABLE FROM HERE, CHECKED RATHER THAN ASSERTED.
    #
    # Whether tcp/80 reaches this box from the internet cannot be settled from
    # inside it, so the warning below stays. But two of the ways HTTP-01 fails
    # ARE local facts, and both were being left to the operator to discover
    # after issuance had already failed:
    #
    #   * the name does not resolve at all -- a typo, or a record never created.
    #     Issuance cannot succeed and nothing about the install will say so.
    #   * something else is already holding tcp/80, so the challenge listener
    #     cannot bind. warn_if_taken covers the ports the operator chose; :80 is
    #     not one of them, it is implied by choosing acme.
    #
    # DELIBERATELY NOT DONE: comparing the resolved address against this host's
    # public IP. That needs an external echo service -- a network dependency and
    # a third party told about this install -- to check something the warning
    # below already covers.
    if command -v getent >/dev/null 2>&1 && ! getent hosts "$DOMAIN_NAME" >/dev/null 2>&1; then
      warn "$DOMAIN_NAME does not resolve from this host."
      echo "     Let's Encrypt resolves it from the outside, so this is not proof it will"
      echo "     fail — but a name with no record anywhere cannot be validated. Check the"
      echo "     DNS before relying on the certificate."
    fi
    if port_in_use 80 tcp; then
      warn "something is already listening on tcp/80."
      echo "     HTTP-01 validation needs to bind it. Stop that service, or use"
      echo "     --tls selfsigned and put your own proxy in front."
    fi
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
      # Deliberately WITHOUT a variable here: 443 was just chosen on purpose,
      # for a reason (an unprivileged bind), and silently moving it to 444 would
      # undo the choice the operator just made. Warn only.
      warn_if_taken "$HTTP_PORT" tcp "web UI"
    fi
  fi

  ok "tls: $TLS_MODE${DOMAIN_NAME:+ ($DOMAIN_NAME)}"

  header "=== Data ==="
  echo "  Holds the database, secret.key (which decrypts your stored platform"
  echo "  tokens), recordings and TLS material. Back it up; treat it as secret."
  if [ "$MODE" = "binary" ]; then
    # THE DEFAULT IS THE EXISTING INSTALL'S, NOT THE CONSTANT. A re-run to
    # change a port or turn TLS on offered /var/lib/polyemesis regardless of
    # where the first install had actually been pointed -- and under --yes,
    # ask() takes the default WITHOUT PRINTING A PROMPT. The new directory was
    # created, secret.key minted fresh in it, the unit's --data rewritten, and
    # the service restarted onto an empty database: every destination, source
    # and recording gone from the UI, with the summary printing "create your
    # admin password" as though this were a first install. The old data was
    # intact on disk the whole time and nothing said so.
    local prior_data_dir
    prior_data_dir="$(existing_data_dir)"
    if [ -n "$prior_data_dir" ]; then
      info "an existing install points at $prior_data_dir — offering that, not the default"
      DATA_DIR="$prior_data_dir"
    fi
    ask "Data directory" "$DATA_DIR" DATA_DIR
    validate_data_dir "$DATA_DIR" DATA_DIR
    # And if something still moved it, refuse rather than silently start a
    # fresh install beside the old one. Interactive runs get to say yes.
    if [ -n "$prior_data_dir" ] && [ "$DATA_DIR" != "$prior_data_dir" ]; then
      warn "this would repoint the service from $prior_data_dir to $DATA_DIR."
      echo "     The existing database, secret.key and recordings stay where they are;"
      echo "     the service would start on an EMPTY directory and look like a first install."
      if [ "${ASSUME_YES:-false}" = true ]; then
        die "refusing under --yes. Re-run interactively, or move the data yourself first."
      fi
      local move_ok
      ask_yn "Start with an empty data directory at $DATA_DIR anyway?" "n" move_ok
      [ "$move_ok" = yes ] || die "stopped. Nothing was changed."
    fi
  fi

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

# ffmpeg_yaml pins the FFmpeg this install actually verified, when it installed
# one itself.
#
# Emitted ONLY when this run wrote /usr/local/bin/ffmpeg. If the host's own
# FFmpeg was accepted, nothing is pinned: that binary is on PATH by the
# operator's arrangement, and freezing an absolute path to it would break the
# next `apt upgrade` in a way nobody would connect to this installer.
#
# Docker mode never calls this -- the image carries its own FFmpeg at its own
# path, and a host path pinned into a container config would name a file that
# does not exist there.
ffmpeg_yaml() {
  [ -n "$FFMPEG_PINNED_BIN" ] || return 0
  printf 'ffmpeg:\n  binary: "%s"\n  probe: "%s"\n' \
    "$FFMPEG_PINNED_BIN" "$FFMPEG_PINNED_PROBE"
}

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
  # Record what was actually created. `mkdir -p` succeeds on a directory that
  # already exists, so a blanket DIRS_CREATED=true made rollback `rm -rf` an
  # EXISTING install directory on a failed re-run -- destroying the compose
  # file, the config, the uninstaller and every backup-*.tar.gz that update.sh
  # had ever written there. The volume survives; the compose file needed to
  # bring it back does not.
  [ -d "$INSTALL_DIR" ] || INSTALL_DIR_CREATED=true
  mkdir -p "$INSTALL_DIR"
  DIRS_CREATED=true

  # Asked BEFORE anything is written, because after `compose up -d` there is no
  # way to tell a container this run started from one it recreated.
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -qx 'polyemesis'; then
    CONTAINER_PREEXISTING=true
  fi

  preserve_existing "$INSTALL_DIR/config.yaml"
  {
    printf '# Written by scripts/install.sh. Edit and `docker compose up -d` to apply.\n'
    printf 'dataDir: "/data"\n'
    printf 'addr: ":%s"\n' "$HTTP_PORT"
    tls_yaml
  } > "$INSTALL_DIR/config.yaml"

  preserve_existing "$INSTALL_DIR/docker-compose.yml"
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
  # Not for a container that was already up. Rollback reads this flag to decide
  # whether it may `compose down`, and downing the operator's live container is
  # the failure this whole re-run path exists to avoid.
  [ "$CONTAINER_PREEXISTING" = true ] || CONTAINER_STARTED=true

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
    # A MISMATCH DIED; A MISSING SUMS FILE USED TO WARN AND INSTALL ANYWAY.
    #
    # Those are the same risk with different evidence. "The bytes are wrong" and
    # "nobody can tell whether the bytes are wrong" both end with an unverified
    # binary at $BIN_PATH, installed as root, and the second was the one that
    # happened automatically. It is also the easier one to arrange: suppressing
    # an asset is less work than forging a hash.
    #
    # docs/INSTALL.md tells readers this installer "verifies the download
    # against the release's published SHA256SUMS and refuses to install on a
    # mismatch" -- true as written, and it reads as a stronger promise than a
    # warning kept.
    #
    # The escape hatch stays, because somebody installing a genuinely unsigned
    # release needs it. It is now a flag they typed, which is auditable, rather
    # than a default nobody chose.
    if [ "$ALLOW_UNVERIFIED" != true ]; then
      rm -rf "$tmp"
      err "no SHA256SUMS published for $tag — refusing to install an unverified download."
      echo "     The release may still be publishing; try again in a few minutes."
      echo "     To install it anyway, re-run with --allow-unverified."
      die "refusing to install an unverified binary"
    fi
    warn "no SHA256SUMS published for $tag — installing UNVERIFIED at your request (--allow-unverified)"
  fi

  # Snapshot before replacing, and record that there WAS something to replace.
  # See the rollback: without this, a failed re-run deleted the binary of a
  # working install.
  if [ -e "$BIN_PATH" ]; then
    BIN_PREEXISTING=true
    preserve_existing "$BIN_PATH"
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
  # `mkdir -p` leaves 0755 under the default umask, so every local account could
  # list the directory holding secret.key and the recordings. The hand-install
  # in deploy/polyemesis.service's header has always said
  # `chmod 0750 /var/lib/polyemesis  # holds stream keys -- see #297`; the
  # installer, which is the recommended path, never did it.
  chmod 0750 "$DATA_DIR"

  preserve_existing "$CONFIG_DIR/config.yaml"
  {
    printf '# Written by scripts/install.sh.\n'
    printf '# Only what must be known before the database opens lives here.\n'
    printf '# Destinations, routing and renditions are runtime state, edited in the UI.\n'
    printf 'dataDir: "%s"\n' "$DATA_DIR"
    printf 'addr: ":%s"\n' "$HTTP_PORT"
    tls_yaml
    ffmpeg_yaml
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

  # TWO FILES THAT ARE ONE FILE. deploy/polyemesis.service is the hand-install
  # this project documents; the unit below is what the recommended path actually
  # creates. They drifted: the generated one carried none of the [Service]
  # hardening from `ProtectKernelTunables` down, and no UMask, so the installer
  # produced a weaker service than the copy-paste instructions did. Neither file
  # looks wrong on its own, which is why the drift lasted.
  #
  # install.sh is fetched standalone with curl and has no repository to read, so
  # it cannot be generated from that file. scripts/acceptance-install.sh instead
  # asserts that every [Service] directive in deploy/polyemesis.service appears
  # here -- add a directive there and this fails until it is added here too.
  if [ -e "/etc/systemd/system/${SERVICE_NAME}.service" ]; then
    UNIT_PREEXISTING=true
    preserve_existing "/etc/systemd/system/${SERVICE_NAME}.service"
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

# 0077, so anything this service creates under ${DATA_DIR} is private to it.
# internal/db chmods the database, WAL and shm itself on open and secrets.go
# does the same for secret.key, so the crown jewels were always covered -- this
# covers everything else the service ever writes there, including files added
# later by someone who does not know to secure them. Issue #297.
UMask=0077

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
# THE STREAM KEY IS IN FFmpeg'S ARGV, AND ARGV IS WORLD-READABLE BY DEFAULT.
# A destination's RTMP target is built as rtmp://host/app/<streamKey> and handed
# to the child as an argument, so on a stock Linux host any local account can
# read a live stream key out of /proc/<pid>/cmdline or plain ps(1). polyemesis
# masks its own renderings of that command line and cannot mask the kernel's.
# ProtectProc=invisible hides other users' processes from this unit's view and,
# with hidepid on /proc, is what keeps the key off a shared box. It is one line
# and it costs nothing on a single-operator VPS.
ProtectProc=invisible
ProtectHome=true
ReadWritePaths=${DATA_DIR}
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictNamespaces=true
RestrictRealtime=true
RestrictSUIDSGID=true
LockPersonality=true
# AF_INET/AF_INET6 for HTTP, RTMP and SRT; AF_UNIX for local sockets.
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM

# Several FFmpeg processes each hold a handful of descriptors; the default
# soft limit is fine but headroom costs nothing.
LimitNOFILE=65535

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
  # See the note in install_docker_mode: rollback may only delete an install
  # directory THIS run created.
  [ -d "$INSTALL_DIR" ] || INSTALL_DIR_CREATED=true
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

# write_binary_uninstall_script gives systemd installs the same thing docker
# installs have had: one script that removes what the installer added, keeps the
# data, and SAYS which of the two it did to each thing.
#
# The summary used to print this instead:
#
#	sudo systemctl disable --now polyemesis && sudo rm /usr/local/bin/polyemesis
#
# which leaves the unit file, the config directory and the data directory --
# measured at 8K, 12K and 820K on a real install. Two of those matter. systemd
# still lists a service whose binary is gone, so a later `systemctl start`
# fails against nothing. And the data directory holds secret.key, which the
# summary three lines above calls secret material and which an operator
# decommissioning a host has just been told they have finished removing.
#
# Data is KEPT, like docker's. An installer that deletes a database on the way
# out is a worse problem than the one it is solving. It is named, with the
# command to remove it, so the choice is the operator's and is visible.
write_binary_uninstall_script() {
	# See the note in install_docker_mode: rollback may only delete an install
	# directory THIS run created.
	[ -d "$INSTALL_DIR" ] || INSTALL_DIR_CREATED=true
	mkdir -p "$INSTALL_DIR"
	cat > "$INSTALL_DIR/uninstall.sh" <<EOF
#!/usr/bin/env bash
set -euo pipefail

SERVICE_NAME="$SERVICE_NAME"
BIN_PATH="$BIN_PATH"
CONFIG_DIR="$CONFIG_DIR"
DATA_DIR="$DATA_DIR"

REMOVE_DATA=false
FORCE=false
for arg in "\$@"; do
	case "\$arg" in
		--force|-f)     FORCE=true ;;
		--remove-data)  REMOVE_DATA=true ;;
		-h|--help)
			echo "usage: uninstall.sh [--force] [--remove-data]"
			echo
			echo "  --force        do not ask, and do not refuse while a broadcast is on air"
			echo "  --remove-data  also delete \$DATA_DIR (database, secret.key, recordings)"
			exit 0 ;;
		*) echo "unknown option: \$arg" >&2; exit 2 ;;
	esac
done

# ROOT BEFORE THE FIRST MUTATION. Without this the systemctl call is swallowed
# by its own \`|| true\` and the rm below dies under set -e, leaving the service
# disabled, the unit file present and the binary present -- a half-uninstalled
# host whose next \`systemctl start\` fails against nothing.
if [ "\$(id -u)" != 0 ]; then
	echo "uninstall.sh must run as root: sudo \$0 \$*" >&2
	exit 1
fi

# IS ANYTHING ON AIR? Stopping this service ends every live broadcast on the
# install, and a completed broadcast cannot be returned to. The installer asks
# before the REVERSIBLE act of installing; this is the irreversible one.
#
# Scoped to the unit's own cgroup rather than every ffmpeg on the box: an
# unrelated ffmpeg, or one left behind by a test run, is not this service's
# broadcast and must not block an uninstall.
publishing_now() {
	local cg="/sys/fs/cgroup/system.slice/\${SERVICE_NAME}.service/cgroup.procs"
	local pid args n=0
	[ -r "\$cg" ] || return 1
	while read -r pid; do
		[ -r "/proc/\$pid/cmdline" ] || continue
		args=\$(tr '\\0' ' ' < "/proc/\$pid/cmdline" 2>/dev/null || true)
		case "\$args" in
			*ffmpeg*rtmp:*|*ffmpeg*srt:*|*ffmpeg*"-f flv"*)
				n=\$((n+1))
				echo "    pid \$pid: \$(echo "\$args" | grep -oE '(rtmp|srt)://[^ ]{0,40}' | tail -1)" >&2 ;;
		esac
	done < "\$cg"
	[ "\$n" -gt 0 ]
}

if [ "\$FORCE" != true ]; then
	if publishing_now; then
		echo >&2
		echo "REFUSING: \$SERVICE_NAME is publishing right now (listed above)." >&2
		echo "Uninstalling stops it, and a live broadcast that ends cannot be resumed." >&2
		echo "Stop the destinations first, or pass --force if you mean to end them." >&2
		exit 1
	fi
	# Not on air, but still destructive. A terminal gets asked; a run with no
	# terminal is REFUSED rather than assumed, so an unattended job cannot
	# uninstall a broadcast server by inheriting this script.
	reply=""
	if { printf 'This removes %s, its unit file, %s and %s.\\nType the service name (%s) to confirm: ' \\
			"\$BIN_PATH" "\$CONFIG_DIR" \\
			"\$([ "\$REMOVE_DATA" = true ] && echo "\$DATA_DIR" || echo "nothing else")" \\
			"\$SERVICE_NAME" > /dev/tty; } 2>/dev/null; then
		IFS= read -r reply < /dev/tty 2>/dev/null || reply=""
	else
		echo "No terminal to confirm on. Pass --force if you mean this." >&2
		exit 1
	fi
	if [ "\$reply" != "\$SERVICE_NAME" ]; then
		echo "Not confirmed; nothing was changed." >&2
		exit 1
	fi
fi

systemctl disable --now "\$SERVICE_NAME" >/dev/null 2>&1 || true
rm -f "/etc/systemd/system/\$SERVICE_NAME.service"
systemctl daemon-reload >/dev/null 2>&1 || true
rm -f "\$BIN_PATH"
rm -rf "\$CONFIG_DIR"

echo
echo "Removed the service, the unit file, \$BIN_PATH and \$CONFIG_DIR."

if [ "\$REMOVE_DATA" = true ]; then
	# GUARDED, because the alternative this replaces was printing
	# \`sudo rm -rf \$DATA_DIR\` for the operator to paste -- an unguarded
	# recursive delete, handed over as a instruction, of the one directory
	# holding unrepeatable recordings.
	case "\$DATA_DIR" in
		""|"/") echo "refusing to delete '\$DATA_DIR'" >&2; exit 1 ;;
		/*) : ;;
		*) echo "refusing to delete a non-absolute data directory '\$DATA_DIR'" >&2; exit 1 ;;
	esac
	case "\$DATA_DIR" in
		/bin|/boot|/dev|/etc|/home|/lib|/lib32|/lib64|/media|/mnt|/opt|/proc|/root|/run|/sbin|/srv|/sys|/tmp|/usr|/var)
			echo "refusing to delete '\$DATA_DIR': that is a system directory" >&2; exit 1 ;;
	esac
	# AND IS IT OURS? The three guards above prove the path is safe to TYPE.
	# None of them proves polyemesis ever wrote there. DATA_DIR is frozen into
	# this script when the installer generates it and is never re-read, so an
	# operator who later moved the data directory and repointed config.yaml by
	# hand has an uninstaller pointing at a stale path -- which passes all three
	# checks, deletes whatever now lives there, and reports "database, secret.key
	# and recordings are gone" whether or not any of that was true. Either an
	# unrelated tree is destroyed, or the real data survives a decommission the
	# operator believes finished.
	if [ ! -e "\$DATA_DIR/polyemesis.db" ] && [ ! -e "\$DATA_DIR/secret.key" ]; then
		echo "refusing to delete '\$DATA_DIR': it holds neither polyemesis.db nor" >&2
		echo "secret.key, so it is not this install's data directory. This path was" >&2
		echo "baked in when the installer ran; if the data was moved since, find the" >&2
		echo "real dataDir (it was in \$CONFIG_DIR/config.yaml) and delete it yourself." >&2
		exit 1
	fi
	rm -rf "\$DATA_DIR"
	echo "Deleted \$DATA_DIR - database, secret.key and recordings are gone."
else
	echo
	echo "\$DATA_DIR was KEPT - it holds your database, secret.key and recordings."
	echo "secret.key is what decrypts your stored platform tokens. To destroy it,"
	echo "re-run with the flag, which checks the path before deleting anything:"
	echo
	echo "    sudo \$0 --remove-data"
fi
echo
EOF
	chmod +x "$INSTALL_DIR/uninstall.sh"
	ok "wrote update.sh and uninstall.sh"
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
    write_binary_uninstall_script
    return 0
  fi

  cat > "$INSTALL_DIR/update.sh" <<EOF
#!/usr/bin/env bash
# Back up the volume BEFORE pulling: migrations run forward only and there is
# no downgrade path. The backup is the only way back.
set -euo pipefail
cd "$INSTALL_DIR"
stamp="\$(date +%F-%H%M)"
dest="$INSTALL_DIR/backup-\${stamp}.tar.gz"
echo "backing up to \$dest"

# Same-minute reruns produce the SAME stamp, and \`tar czf\` overwrites an
# existing archive with no warning. That archive is the only way back from
# an upgrade that just went wrong -- exactly when a rerun is likeliest.
# Refuse rather than silently replace it.
if [ -e "\$dest" ]; then
  echo "ERROR: \$dest already exists. Refusing to overwrite the existing backup." >&2
  echo "Wait a minute and re-run, or move the old archive aside first." >&2
  exit 1
fi

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

# LIST ONCE, THEN TEST THE LISTING. Never pipe tar into a reader that can exit
# early. \`tar tzf … | grep -q\` looks obviously correct and is not: grep -q
# exits on its FIRST match, tar then takes SIGPIPE, and under the \`set -o
# pipefail\` above the pipeline returns 141 -- which \`if !\` inverts into "the
# file is missing". A backup that DOES contain secret.key was refused, and
# whether it happened depended on where the file landed in readdir order, i.e.
# inode order. Reproduced at entry 1 of 4002 (false refusal) and entry 4001
# (pass), same archive contents. Invisible on macOS, where bsdtar exits 0 on
# SIGPIPE, which is why this survived local testing.
#
# Herestrings rather than \`printf | grep\`, because printf takes SIGPIPE too --
# reordering the pipeline would move the bug, not remove it. This also walks the
# archive once instead of twice.
listing="\$(tar tzf "\$dest")"

# A backup that exists but holds nothing is worse than no backup, because it
# reads as success. tar always writes the './' entry, so anything under two
# entries means the volume was empty.
entries="\$(wc -l <<<"\$listing")"
if [ "\$entries" -lt 2 ]; then
  echo "ERROR: backup archive is empty (\${entries} entries). Refusing to upgrade." >&2
  exit 1
fi

# AND THE ONE FILE THE COUNT CANNOT VOUCH FOR. A non-empty archive proves the
# volume held something, not that it held the file that makes the database
# usable. Restoring without secret.key brings every destination back DISABLED
# -- correctly, since a key that will not open disables its destination rather
# than failing open -- and that reads as a successful restore until go-live.
if ! grep -q "secret\.key" <<<"\$listing"; then
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

REMOVE_DATA=false
FORCE=false
for arg in "\$@"; do
	case "\$arg" in
		--force|-f)    FORCE=true ;;
		--remove-data) REMOVE_DATA=true ;;
		-h|--help)
			echo "usage: uninstall.sh [--force] [--remove-data]"
			echo
			echo "  --force        do not ask, and do not refuse while a broadcast is on air"
			echo "  --remove-data  also delete the polyemesis-data volume"
			exit 0 ;;
		*) echo "unknown option: \$arg" >&2; exit 2 ;;
	esac
done

# IS ANYTHING ON AIR? \`compose down\` ends every live broadcast the container is
# carrying, and a completed broadcast cannot be returned to. Asked of the
# container's own process table, so an ffmpeg elsewhere on the host is not
# mistaken for this install's broadcast.
publishing_now() {
	local out
	out=\$($COMPOSE_CMD top 2>/dev/null || true)
	case "\$out" in
		*ffmpeg*rtmp:*|*ffmpeg*srt:*|*ffmpeg*"-f flv"*)
			echo "\$out" | grep -E 'ffmpeg.*(rtmp|srt):' | head -3 >&2
			return 0 ;;
	esac
	return 1
}

if [ "\$FORCE" != true ]; then
	if publishing_now; then
		echo >&2
		echo "REFUSING: this install is publishing right now (listed above)." >&2
		echo "Uninstalling stops it, and a live broadcast that ends cannot be resumed." >&2
		echo "Stop the destinations first, or pass --force if you mean to end them." >&2
		exit 1
	fi
	reply=""
	if { printf 'This stops and removes the container in %s.\\nType "remove" to confirm: ' "$INSTALL_DIR" > /dev/tty; } 2>/dev/null; then
		IFS= read -r reply < /dev/tty 2>/dev/null || reply=""
	else
		echo "No terminal to confirm on. Pass --force if you mean this." >&2
		exit 1
	fi
	[ "\$reply" = "remove" ] || { echo "Not confirmed; nothing was changed." >&2; exit 1; }
fi

$COMPOSE_CMD down --remove-orphans
echo
echo "Stopped and removed the container."

if [ "\$REMOVE_DATA" = true ]; then
	docker volume rm polyemesis-data
	echo "Deleted the polyemesis-data volume - database, secret.key and recordings are gone."
else
	echo "The 'polyemesis-data' volume was KEPT — it holds your database, secret.key"
	echo "and recordings. To destroy it:"
	echo
	echo "    \$0 --remove-data"
fi
echo
echo "The install directory is left in place: $INSTALL_DIR"
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
    echo "  Update      sudo $INSTALL_DIR/update.sh"
    echo "  Uninstall   sudo $INSTALL_DIR/uninstall.sh"
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
  --ffmpeg MODE          ask (default), skip, or force installing FFmpeg 8.x to
                         /usr/local/bin when the host's is older than the
                         container's, below the 6.0 floor, or missing entirely.
                         Never installs under --check.
  --allow-unverified     install a release binary even when the release has no
                         published SHA256SUMS to check it against
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
      --allow-unverified) ALLOW_UNVERIFIED=true; shift ;;
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
  # RTMP_PORT IS IN THE LIST. It was the one port that skipped this, so
  # `--rtmp-port 70000` reached docker-compose.yml as a port mapping and failed
  # at `compose up` -- which is loud, but it fails INSIDE the install, so the
  # rollback trap then runs against a half-configured host. Refuse it here,
  # before anything has been created.
  for pv in HTTP_PORT SRT_PORT RTMP_PORT; do
    # 0 declines RTMP entirely -- see the ENABLE_RTMP case in
    # gather_configuration, which reads the port as the switch. It is not a
    # port, so it does not go through the range check below.
    if [ "$pv" = RTMP_PORT ] && [ "${!pv}" = "0" ]; then continue; fi
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
  else
    install_binary_mode
  fi

  # BOTH modes, because write_helper_scripts splits on MODE itself. Calling it
  # only in the docker branch made its own `if [ "$MODE" != "docker" ]` arm
  # unreachable, so #347 -- which exists to give systemd operators the same
  # backup-before-upgrade guard rail docker operators got -- never shipped. The
  # fix was written and never wired.
  write_helper_scripts

  configure_firewall
  verify

  INSTALL_COMPLETE=true
  print_summary
}

main "$@"
