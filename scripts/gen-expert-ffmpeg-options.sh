#!/usr/bin/env bash
# Regenerates internal/ffmpeg/expert_options_gen.go: the option table expert
# mode's argument walk classifies every token against (poka-yoke audit
# 2026-11-04, finding 2).
#
# THE TABLE IS MEASURED, NOT TYPED. Each of ffmpeg's own options is run alone
# (`ffmpeg -nostdin -<opt>`); "Missing argument" means it takes a value, and
# anything else means it is a boolean. Every AVOption (codec, format, protocol,
# filter-less) takes a value on the command line, so those are read from
# `-h full` and `-h protocol=<p>`. `-h full` hides some legacy aliases (-s,
# -ab, -acodec, ...), so a fixed list of those is probed the same way.
#
# libsrt's options are appended by hand because a build without libsrt (the
# Homebrew one, for instance) does not list them; they all take a value.
#
# An option this table does not know is REFUSED by expert mode, never guessed
# at, so a build that lacks something only makes expert mode stricter.
#
# Usage: scripts/gen-expert-ffmpeg-options.sh [path-to-ffmpeg]
set -euo pipefail

FFMPEG="${1:-ffmpeg}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/internal/ffmpeg/expert_options_gen.go"
WORK="$(mktemp -d)"
trap 'command rm -rf "$WORK"' EXIT
# Probes run in an empty directory: -report and -vstats write files into the
# working directory when they are run, and this must not litter the repo.
cd "$WORK"

VERSION="$("$FFMPEG" -hide_banner -version | head -1)"

# Options that print something and exit. They are neither safe nor useful in
# an argv that streams, so they are left out of both lists and refused.
INFO=" -L -h -help -? --help -version -buildconf -formats -muxers -demuxers -devices -decoders -encoders -filters -pix_fmts -layouts -sample_fmts -bsfs -protocols -dispositions -colors -sources -sinks -hwaccels -codecs "

HIDDEN="-i -s -ab -aq -ac -ar -acodec -scodec -dcodec -apad -vtag -atag -stag -isync -frames -aframes -tag
-ch_layout -channel_layout -guess_layout_max -sample_fmt -bsf -vpre -apre -spre -fps_mode -fpsmax
-force_key_frames -hwaccel -hwaccel_device -hwaccel_output_format -copytb -enc_time_base -autorotate
-filter_threads -filter_complex_threads -filter_buffered_frames -shortest_buf_duration
-thread_queue_size -recast_media -vstats_version -max_alloc -cpuflags -cpucount -loglevel -v
-timelimit -stats_period -dts_delta_threshold -dts_error_threshold -abort_on -xerror -dframes
-vframes -target -timecode -streamid -map_metadata -map_chapters -discard -disposition
-bits_per_raw_sample -time_base -apply_cropping -metadata -program -stream_group
-max_muxing_queue_size -muxing_queue_data_threshold -rc_override -display_rotation
-mastering_display -content_light -reinit_filter -drop_changed -canvas_size -fix_sub_duration
-itsscale -readrate -readrate_initial_burst -readrate_catchup -sseof -seek_timestamp -accurate_seek"

LIBSRT="-timeout -listen_timeout -send_buffer_size -recv_buffer_size -pkt_size -payload_size
-latency -rcvlatency -peerlatency -maxbw -pbkeylen -passphrase -enforced_encryption
-kmrefreshrate -kmpreannounce -snddropdelay -mss -ffs -ipttl -iptos -inputbw -oheadbw
-tlpktdrop -nakreport -connect_timeout -mode -sndbuf -rcvbuf -lossmaxttl -minversion
-streamid -srt_streamid -smoother -messageapi -transtype -linger -tsbpd"

# Hardware encoders' private options, by hand for the same reason as libsrt:
# the build that generates this table (a macOS one, say) has no NVENC, VAAPI or
# QSV, but the install that reads it may. All take a value; none names a file
# or a device -- device selection (-vaapi_device, -init_hw_device, ...) comes
# from the server's own encoder configuration, never from expert arguments.
HWENC="-rc -cq -multipass -rc-lookahead -spatial-aq -temporal-aq
-zerolatency -b_ref_mode -nonref_p -strict_gop -aq-strength -bluray-compat -surfaces
-delay -no-scenecut -forced-idr -tier -weighted_pred -init_qpP -init_qpB -init_qpI
-qp -qp_cb_offset -qp_cr_offset -ldkfs -intra-refresh -single-slice-intra-refresh
-max_slice_size -constrained-encoding -split_encode_mode -lookahead_level -unidir_b
-highbitdepth -tf_level -b_adapt -rc_mode -low_power -idr_interval -b_depth
-async_depth -quality -sei -aud -coder -max_frame_size -blbrc -look_ahead
-look_ahead_depth -extbrc -adaptive_i -adaptive_b -scenario -preset -tune -profile -level"

"$FFMPEG" -hide_banner -h full 2>/dev/null >help.txt
{
	grep -E '^-[A-Za-z]' help.txt | awk '{print $1}' | sed 's/\[.*//'
	for o in $HIDDEN; do echo "$o"; done
} | sort -u >main.txt

: >bool.txt
: >value.txt
while read -r o; do
	case "$INFO" in *" $o "*) continue ;; esac
	out="$("$FFMPEG" -hide_banner -nostdin "$o" </dev/null 2>&1 | head -3 || true)"
	if grep -q 'Missing argument' <<<"$out"; then
		echo "$o" >>value.txt
	elif grep -qi 'Unrecognized option' <<<"$out"; then
		:
	else
		echo "$o" >>bool.txt
	fi
done <main.txt

# Every AVOption line, with its description, for the vetting below.
grep -E '^  -[A-Za-z0-9_]' help.txt >avlines.txt
for p in $("$FFMPEG" -hide_banner -protocols 2>/dev/null | sed -n '/Input/,$p' | grep -v -E 'Input|Output'); do
	"$FFMPEG" -hide_banner -h "protocol=$p" 2>/dev/null | grep -E '^  -[A-Za-z0-9_]' >>avlines.txt || true
done
awk '{print $1}' avlines.txt >>value.txt
for o in $LIBSRT $HWENC; do echo "$o" >>value.txt; done

# THE ALLOWLIST IS VETTED, NOT JUST MEASURED. An option is allowed only if no
# help line for it -- its name and description, under any of the components
# that define it -- mentions a file, a path, a directory, a URL, a log, a dump
# or stats (two-pass and analysis files are "stats"). Those go to the
# file-ish list instead, which expert mode refuses with a sentence saying so.
# Preset options read a preset FILE although their help says "preset name",
# so they are listed by name. Errs towards refusal: an innocent option that
# mentions a file is refused, never the other way round.
FILEISH_RE='(^|[^A-Za-z_])(file|files|filename|filenames|filepath|path|paths|dir|directory|url|urls|uri|log|logs|dump|dumps|stats|statistics)([^A-Za-z_]|$)'
# -max_size: a demuxer in the CI and Alpine builds (absent from Homebrew's)
# describes it as a "file size"; excluded by name so every build agrees.
BYNAME="-report -pre -vpre -apre -spre -fpre -dumpgraph -graph_file -hwaccel -max_size"
{
	grep -E '^-[A-Za-z]' help.txt | sed -E 's/^(-[^ []+)[^ ]*/\1/'
	cat avlines.txt
} | grep -i -E "$FILEISH_RE" | awk '{print $1}' >fileish.txt
for o in $BYNAME; do echo "$o" >>fileish.txt; done
# Any option that names a DEVICE opens a path too (/dev/dri/..., or any file
# handed to it): -vaapi_device, -init_hw_device, -filter_hw_device,
# -hwaccel_device, the capture-device indexes. All of them, by name.
cat main.txt value.txt | grep -i 'device' >>fileish.txt || true
sort -u -o fileish.txt fileish.txt

# NESTED OPTION DICTIONARIES (-format_opts, -fifo_options, -protocol_opts,
# -hls_segment_options, ...) hand a whole key=value list to another component,
# past every check on this page: -f fifo -format_opts hls_segment_filename=...
# writes files. Every <dictionary> option is refused except the encoder-param
# lists, whose keys the walk itself checks.
CHECKED_DICTS=" -x264-params -x265-params -svtav1-params "
grep '<dictionary>' avlines.txt | awk '{print $1}' | sort -u | while read -r o; do
	case "$CHECKED_DICTS" in *" $o "*) ;; *) echo "$o" ;; esac
done >nested.txt

sort -u -o bool.txt bool.txt
comm -23 bool.txt fileish.txt >bool.ok
# A name that is one of ffmpeg's own booleans is a boolean, whatever an
# AVOption of the same name says.
# The print-and-exit options are left out here too: an AVOption of the same
# name (-sources, from libavdevice) would otherwise put one back.
sort -u value.txt | comm -23 - bool.txt | comm -23 - fileish.txt | comm -23 - nested.txt |
	while read -r o; do case "$INFO" in *" $o "*) ;; *) echo "$o" ;; esac; done >value.sorted

emit() {
	while read -r o; do printf '\t"%s": true,\n' "$o"; done <"$1"
}

{
	echo "// Code generated by scripts/gen-expert-ffmpeg-options.sh; DO NOT EDIT."
	echo "// Measured against: $VERSION"
	echo
	echo "package ffmpeg"
	echo
	echo "// ffmpegBoolOptions are ffmpeg's options that take no value and name no file."
	echo "var ffmpegBoolOptions = map[string]bool{"
	emit bool.ok
	echo "}"
	echo
	echo "// ffmpegValueOptions are the options that consume the next token as their"
	echo "// value and whose help names no file, path, URL, log, dump or stats."
	echo "var ffmpegValueOptions = map[string]bool{"
	emit value.sorted
	echo "}"
	echo
	echo "// ffmpegNestedOptions pass a key=value list to another component."
	echo "var ffmpegNestedOptions = map[string]bool{"
	emit nested.txt
	echo "}"
	echo
	echo "// ffmpegFileishOptions were left out of both lists because their help"
	echo "// mentions a file, path, URL, log, dump or stats, or they read a preset file."
	echo "var ffmpegFileishOptions = map[string]bool{"
	emit fileish.txt
	echo "}"
} >"$OUT"
gofmt -w "$OUT"
echo "wrote $OUT ($(wc -l <bool.ok | tr -d ' ') boolean, $(wc -l <value.sorted | tr -d ' ') value, $(wc -l <fileish.txt | tr -d ' ') file-ish options)"
