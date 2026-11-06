#!/usr/bin/env bash
# The installer's argument validator must refuse bad input. The ONE list of bad
# flags, run by release.yml's installer-gate and installer.yml's flags job so
# the two cannot disagree about what "refuses nonsense" means. Needs no root,
# no network and installs nothing: parse_args refuses before require_root.
set -euo pipefail
cd "$(dirname "$0")/.."

bash -n scripts/install.sh
bash -n scripts/acceptance-install.sh

for bad in "--mode wat" "--mode nonsense" "--tls wat" "--tls bogus" "--http-port abc" "--http-port 70000" "--srt-port 0" "--srt-port x" "--rtmp-port 70000" "--version latest" "--version 0.10.0" "--version v0.10" "--mode docker --version v0.10.0" "--unknown" "--wat"; do
  # shellcheck disable=SC2086
  if bash scripts/install.sh $bad >/dev/null 2>&1; then
    echo "::error::install.sh accepted '${bad}'. The argument validator is what stands between an operator's typo and a half-configured host."
    exit 1
  fi
  echo "refused: ${bad}"
done
