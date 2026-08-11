#!/usr/bin/env bash
# Does anything in scripts/ ask a process to die and then ACT AS THOUGH IT HAD?
#
# WHY THIS EXISTS
#
# After #179/#180 the termination class had exactly one gate, and its
# jurisdiction was `.github/workflows/*.y{a,}ml`. internal/testenv/
# workflowtimeout_test.go is well built -- red fixtures, count-and-identity
# assertions, a zero-files fatal -- but its predicate is the substring `$!` and
# its requirement is `timeout-minutes != nil`. MEASURED by the #199 class sweep:
# a fixture carrying #179's body VERBATIM -- `kill "$pid"; wait "$pid";
# if [ "$ok" != yes ]` -- was dropped into that test's GREEN directory with a
# `timeout-minutes: 10`, and both subtests passed. The gate obliges bounding the
# blast radius. It does not oblige observing the death.
#
# For shell there was no obligation at all. poly_free_port and poly_stop_server
# are helpers; test-lib-cleanup.sh tests the library and never its callers;
# nothing read scripts/*.sh for kill-then-sleep, kill-then-verdict, or a bare
# wait. And that is not hypothetical either: acceptance-mqtt.sh carried the exact
# shape that had just been rewritten at acceptance-postprod.sh:125,
# acceptance-tls.sh:76 and acceptance.sh:134, and survived the round untouched.
# It was found by a sweep, not by CI. Without a gate it recurs; it already did.
#
# WHAT IT LOOKS FOR
#
#   kill-then-assume  a kill/pkill followed, within a few lines and inside the
#                     same straight-line block, by an assumption -- a sleep, an
#                     ok/bad verdict, or a read of a file the killed process was
#                     writing -- with no re-observation of the death anywhere
#                     between them.
#   bare-wait         `wait` with no pid and no bound. #179's mechanism exactly.
#   pipeline-pid      `$!` captured after a PIPELINE, where it names the last
#                     element rather than the process the author meant. #208 was
#                     the live instance: `obs ... | sed ... &` then `OBS_PID=$!`,
#                     so every kill in that entrypoint went to the log prefixer.
#
# WHAT IT DELIBERATELY DOES NOT LOOK FOR, said out loud so nobody reads a green
# run as a stronger claim: it is line-local and syntactic. A kill in one function
# and the assumption in its caller is invisible to it. So is a wait bounded by
# something it cannot see, and so is a `$!` that is correct because the author
# knows the pipeline has one element. It is a net under the class, not a proof
# about it.
#
# HOW TO KNOW IT WORKS: scripts/test-termination-guard.sh runs it against red
# fixtures it must flag and green fixtures it must not, asserted by count AND by
# identity, in the shape internal/testenv/workflowtimeout_test.go established.
#
# Usage:  ./scripts/termination-guard.sh [dir ...]
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(dirname "$HERE")"

# Findings and allowlist entries have to be spelled the same way, so paths are
# reported relative to the repository root. A fixture directory outside the tree
# falls back to <dir>/<file>, which is what makes the identity assertions in
# scripts/test-termination-guard.sh readable.
rel() {
	case "$1" in
	"$ROOT"/*) printf '%s' "${1#"$ROOT"/}" ;;
	*) printf '%s/%s' "$(basename "$(dirname "$1")")" "$(basename "$1")" ;;
	esac
}

# WINDOW. How far after a kill an assumption still counts as belonging to it.
# Six lines, because every real instance in this tree -- and #179's body -- puts
# the assumption within two. Wider mostly collects unrelated code across blank
# lines and comments; narrower misses a kill with a comment under it.
WINDOW="${TERMINATION_GUARD_WINDOW:-6}"

# ---------------------------------------------------------------- the vocabulary

# RE-OBSERVATION: evidence that the author looked again rather than assumed.
# `lsof` is here because the port being released is how this tree observes a
# death it cannot see in the process table, and a `command -v lsof` branch is
# part of that apparatus rather than an aside.
REOBSERVE='pgrep|kill -0|poly_free_port|poly_stop_server|poly_port_holders|poly_wait_jobs|poly_wait_port_ready|poly_bounded_stop|poly__alive|lsof|jobs -rp|docker wait|pidof|ps -o|ps -p|wait "\$|wait \$'

# ASSUMPTION: acting on a death that has only been requested. A sleep standing in
# for the observation; a verdict printed as though the kill had landed; a read of
# a file or a port the killed process owned.
ASSUME='(^|[^[:alnum:]_])(sleep|ok|bad|note|cat|grep|ffprobe|ffmpeg|jq|awk|curl|wget|stat|head|tail)([^[:alnum:]_]|$)'

# BARE WAIT: `wait` with no pid. Redirections are allowed after it because
# `wait 2>/dev/null` is the spelling that actually appeared in this tree.
BAREWAIT='(^|[;&|(]|then |else |do )[[:space:]]*wait[[:space:]]*(2?>[^[:space:]]+|1?>&2|>&2)*[[:space:]]*(;|&&|\|\||$)'

# KILL: a kill in COMMAND POSITION. Anchoring matters more than it looks: the
# first draft matched the substring, and flagged
# `ok "server restarted after the kill"` and `step "6. The kill is a TERM..."` --
# two English sentences about killing, in files that get this right. A guard whose
# first output is prose it misread is a guard people learn to skim.
KILL='(^|;|&&|\||\{|[[:space:]]then|[[:space:]]else|[[:space:]]do)[[:space:]]*(kill|pkill)[[:space:]]'

# BARRIER: control flow leaving the block. Past one of these the next line is no
# longer the kill's straight-line successor, and pretending otherwise is how a
# guard collects the `ok` from the OTHER branch of an if and calls it a finding.
BARRIER='^[[:space:]]*(\}|fi|done|esac|else|elif|;;|\*\)|then)[[:space:]]*$|^[[:space:]]*(exit|return)([[:space:]]|$)'

# ------------------------------------------------------------------ the allowlist
#
# ONE LINE PER EXCEPTION, WITH A REASON, so an intentional exception is written
# down rather than silently unmatched. Format: <path><TAB><rule><TAB><reason>,
# where <rule> may be `*`.
#
# An entry that suppresses NOTHING is an error, not a tidy no-op: a stale entry is
# a standing licence nobody re-argued for, and the file it names may since have
# grown a real instance.
#
# TERMINATION_GUARD_ALLOWLIST points at a file in the same format and REPLACES
# this. It exists so scripts/test-termination-guard.sh can drive the mechanism
# against fixtures; production callers pass nothing.
allowlist() {
	if [ -n "${TERMINATION_GUARD_ALLOWLIST:-}" ]; then
		cat "$TERMINATION_GUARD_ALLOWLIST"
		return 0
	fi
	cat <<'ALLOW'
scripts/test-termination-guard.sh	*	Its red fixtures are heredocs of the exact shapes the guard must flag. Scanning them in place would report the test suite's own evidence as defects.
ALLOW
}

# ------------------------------------------------------------------------ scanning

fail=0
findings_file="$(mktemp)"
trap 'rm -f "$findings_file"' EXIT

note() { # note <path> <line> <rule> <message>
	printf '%s:%s: %s: %s\n' "$(rel "$1")" "$2" "$3" "$4" >>"$findings_file"
}

# MATCHING IS DONE WITH BASH'S OWN `=~`, not by piping each line to grep. That
# is not micro-optimisation: the first draft forked four greps per line across
# 7,500 lines of scripts/ and took 71 SECONDS. A guard that costs a minute on
# every PR is a guard somebody moves to a nightly job and then stops reading.
re_match() { # re_match <text> <ere>
	[[ $1 =~ $2 ]]
}

scan_file() {
	local path="$1"
	local -a lines
	local n=0 code line i j win hitline hittext

	# Read once. `IFS=` and -r keep leading whitespace, which the barrier and
	# comment tests both depend on.
	while IFS= read -r line || [ -n "$line" ]; do
		n=$((n + 1))
		lines[n]="$line"
	done <"$path"

	for ((i = 1; i <= n; i++)); do
		line="${lines[i]}"
		# Everything after a `#` is prose. This file's own header would
		# otherwise be a hundred findings.
		code="${line%%#*}"
		case "$code" in
		*[!\ \	]*) ;;
		*) continue ;;
		esac

		# ---- rule: pipeline-pid ------------------------------------------
		# `$!` names the last element of a pipeline. `( ... | ... ) &` is NOT
		# an instance: there the backgrounded thing is the subshell and `$!`
		# is the subshell's pid, which is what the author meant.
		case "$code" in
		*'$!'*)
			for ((j = i - 1; j >= 1; j--)); do
				hittext="${lines[j]%%#*}"
				case "$hittext" in
				*[!\ \	]*) ;;
				*) continue ;;
				esac
				case "$hittext" in
				*'&') ;;
				*) break ;;
				esac
				case "$hittext" in
				*'('*'|'*'&') break ;;
				*'|'*'&')
					note "$path" "$j" pipeline-pid \
						"\`\$!\` on line $i is captured from a PIPELINE, so it names the last element rather than the process backgrounded here"
					;;
				esac
				break
			done
			;;
		esac

		# ---- rule: bare-wait ---------------------------------------------
		# `wait` alone blocks on every child this shell has, for as long as
		# the slowest one takes. That is #179's mechanism.
		if re_match "$code" "$BAREWAIT"; then
			note "$path" "$i" bare-wait \
				"a bare \`wait\` blocks on every background job with no pid and no ceiling; wait on a pid, or poll with a bound"
		fi

		# ---- rule: kill-then-assume --------------------------------------
		# `kill -0` is an OBSERVATION, not a kill, so it is not a start point.
		if re_match "$code" "$KILL" && ! re_match "$code" 'kill[[:space:]]+-0'; then
			# The rest of the kill's OWN line counts as re-observation:
			# `stop() { pkill -f ...; poly_free_port "$1"; }` is one line and
			# is the correct shape.
			win="$code"
			hitline=""
			for ((j = i + 1; j <= i + WINDOW && j <= n; j++)); do
				re_match "${lines[j]}" "$BARRIER" && break
				hittext="${lines[j]%%#*}"
				win="$win
$hittext"
				if [ -z "$hitline" ] && re_match "$hittext" "$ASSUME"; then
					hitline="$j"
				fi
			done
			if [ -n "$hitline" ] && ! re_match "$win" "$REOBSERVE"; then
				hittext="${lines[hitline]#"${lines[hitline]%%[![:space:]]*}"}"
				note "$path" "$i" kill-then-assume \
					"the kill here is followed on line $hitline by \`${hittext:0:60}\` with nothing observing the death in between"
			fi
		fi
	done
}

# ------------------------------------------------------------------------- driver

dirs=("$@")
if [ "${#dirs[@]}" -eq 0 ]; then
	dirs=("$HERE" "$HERE/obs")
fi

files=0
for d in "${dirs[@]}"; do
	for f in "$d"/*.sh; do
		[ -f "$f" ] || continue
		files=$((files + 1))
		scan_file "$f"
	done
done

# A DIRECTORY THAT MATCHED NOTHING is the failure mode this whole round is
# about: a renamed directory reading as a clean sweep. Fatal, and distinct from
# a finding, so a CI log can tell "nothing wrong" from "nothing looked at".
if [ "$files" -eq 0 ]; then
	echo "TERMINATION GUARD: FATAL -- no *.sh files under ${dirs[*]}; this guard would pass by examining nothing" >&2
	exit 2
fi

# ------------------------------------------------------------------- the allowlist

allow_tmp="$(mktemp)"
allowlist >"$allow_tmp"

suppressed=0
stale=""
kept="$(mktemp)"
: >"$kept"

# allow_covers <finding-line> <path> <rule> -- does this entry cover this finding?
# Findings are `<path>:<line>: <rule>: <message>`.
allow_covers() {
	case "$1" in
	"$2":*) ;;
	*) return 1 ;;
	esac
	[ "$3" = "*" ] && return 0
	case "$1" in
	*": $3: "*) return 0 ;;
	esac
	return 1
}

while IFS="$(printf '\t')" read -r apath arule areason; do
	case "$apath" in "" | \#*) continue ;; esac
	if [ -z "$areason" ]; then
		echo "TERMINATION GUARD: allowlist entry for $apath ($arule) has no reason. An exception nobody wrote down is one nobody argued for." >&2
		fail=1
		continue
	fi
	hits=0
	while IFS= read -r fline; do
		[ -n "$fline" ] || continue
		allow_covers "$fline" "$apath" "$arule" && hits=$((hits + 1))
	done <"$findings_file"
	if [ "$hits" -eq 0 ]; then
		stale="$stale $apath($arule)"
	else
		suppressed=$((suppressed + hits))
	fi
done <"$allow_tmp"

while IFS= read -r fline; do
	[ -n "$fline" ] || continue
	keep=1
	while IFS="$(printf '\t')" read -r apath arule areason; do
		case "$apath" in "" | \#*) continue ;; esac
		[ -n "$areason" ] || continue
		allow_covers "$fline" "$apath" "$arule" && keep=0
	done <"$allow_tmp"
	[ "$keep" -eq 1 ] && printf '%s\n' "$fline" >>"$kept"
done <"$findings_file"

rm -f "$allow_tmp"

# ---------------------------------------------------------------------- reporting

echo "TERMINATION GUARD: scanned $files file(s) under ${dirs[*]}"
if [ -n "$stale" ]; then
	echo "TERMINATION GUARD: allowlist entries that suppress nothing:$stale" >&2
	echo "        A stale exception is a standing licence nobody re-argued for. Delete it, or find out why the shape it covered went away." >&2
	fail=1
fi

if [ -s "$kept" ]; then
	sort "$kept" >&2
	fail=1
fi

if [ "$fail" -ne 0 ]; then
	echo "TERMINATION GUARD: FAILED. A signal is a request. Between asking a process to die and observing it dead there is an interval, and everything a script does in that interval is measured against a machine in a state nobody looked at." >&2
	rm -f "$kept"
	exit 1
fi

rm -f "$kept"
echo "TERMINATION GUARD: ok ($files file(s), $suppressed allowlisted)"
