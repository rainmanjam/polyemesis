#!/usr/bin/env bash
# Checks the shipped Prometheus side with promtool: the alert rules parse, the
# rule unit tests in alerts_test.yml pass, and every query on the Grafana
# dashboard parses as PromQL.
#
# The same script runs in CI (.github/workflows/ci.yml, job "prometheus rules")
# and locally; it needs only docker and jq. The image is pinned by its
# multi-arch index digest, so CI's amd64 runner and an arm64 laptop run the same
# release. Bump PROM_IMAGE's tag and digest together:
#   docker buildx imagetools inspect prom/prometheus:vX.Y.Z   # the top "Digest:"
#
# internal/metrics/dashboard_drift_test.go already checks that every metric
# NAME these files use exists. This checks what that test cannot: that the
# expressions parse, and that the alerts fire and stay quiet when
# alerts_test.yml says they do.
set -euo pipefail

PROM_IMAGE="${PROM_IMAGE:-prom/prometheus:v3.15.0@sha256:efd719c99d83b060d9daefdcf00360461adf279f45ef5391f8d111892118753e}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DASH="$ROOT/deploy/grafana/polyemesis.json"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cp "$ROOT/deploy/prometheus/alerts.yml" "$ROOT/deploy/prometheus/alerts_test.yml" "$work/"

# The dashboard's queries as recording rules, so `promtool check rules` parses
# each one. Grafana's variables are not PromQL and are substituted first:
# $programme sits inside a =~"..." matcher, the interval variables are range
# durations. label_replace's "$1" is PromQL and is left alone (the patterns
# only match a $ followed by a letter or underscore).
jq -r '[.. | objects | select(has("expr")) | .expr] | .[]
       | gsub("\\$\\{?programme\\}?"; ".*")
       | gsub("\\$\\{?__rate_interval\\}?"; "1m")
       | gsub("\\$\\{?__range\\}?"; "1h")
       | gsub("\\$\\{?__interval\\}?"; "30s")
       | @json' "$DASH" >"$work/exprs.jsonl"
n="$(wc -l <"$work/exprs.jsonl" | tr -d ' ')"
if [ "$n" -eq 0 ]; then
  echo "no queries extracted from $DASH: the extraction is wrong, not the dashboard" >&2
  exit 1
fi
{
  echo "groups:"
  echo "  - name: dashboard"
  echo "    rules:"
  i=0
  while IFS= read -r expr; do
    i=$((i + 1))
    echo "      - record: dashboard:query_${i}"
    # A JSON string is a valid YAML double-quoted scalar.
    echo "        expr: ${expr}"
  done <"$work/exprs.jsonl"
} >"$work/dashboard_rules.yml"
if grep -q '\$[A-Za-z_{]' "$work/dashboard_rules.yml"; then
  echo "a Grafana variable survived substitution; add it to the gsub list above:" >&2
  grep -n '\$[A-Za-z_{]' "$work/dashboard_rules.yml" >&2
  exit 1
fi

promtool() {
  docker run --rm -v "$work:/r:ro" -w /r --entrypoint promtool "$PROM_IMAGE" "$@"
}
promtool --version | head -1
promtool check rules alerts.yml
promtool test rules alerts_test.yml
promtool check rules dashboard_rules.yml
echo "dashboard: $n queries parse"
