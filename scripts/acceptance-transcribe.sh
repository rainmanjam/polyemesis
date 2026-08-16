#!/usr/bin/env bash
# Transcription, against the real model host and the real binaries.
#
# WHAT THIS PACKAGE ACTUALLY IS. An internal coverage review ranked
# internal/transcribe third on "7,661 lines, 2 external hosts", and the host
# count came from grepping for URLs. One of the two is a github.com link inside
# an error message that is never fetched. Transcription itself is LOCAL: a
# whisper.cpp binary the operator installs and an ffmpeg command line. So there
# is no protocol handshake here to go and test, and a network-shaped suite would
# have been the wrong shape.
#
# The real untested surface is different, and it is not smaller:
#
#   1. TEN HARDCODED CLAIMS ABOUT A REMOTE SERVER. models.go names ten models
#      and a byte count for each, composes a Hugging Face URL from the name, and
#      gates downloads on the count. Nothing checked any of it against the host.
#      Both halves fail silently and in opposite directions -- a withdrawn name
#      offers a download that 404s, and a stale byte count makes VerifyModelFile
#      reject a PERFECTLY GOOD model as "most likely an interrupted download".
#   2. TWO ARGUMENT BUILDERS verified only against strings we imagined.
#      args.go's own comment says its exhaustive tests exist so the command
#      lines are "pinned by tests instead of discovered in production" -- but a
#      pinned string is only correct if ffmpeg and whisper.cpp agree with it,
#      and neither had ever been asked.
#
# ON THE FIRST RUN THIS FOUND TWO SHIPPED DEFECTS, both in the class the
# multistream suite found #310 and #312 in -- each half individually correct,
# the composition wrong, and no offline test able to see it:
#
#   * ggmlMagic was byte-reversed. GGML_FILE_MAGIC is the uint32 0x67676d6c and
#     the converter fwrites it, so a real model begins 6c 6d 67 67 ("lmgg");
#     the constant spelled it "ggml". looksLikeGGML therefore rejected every
#     genuine whisper.cpp model, so every download failed claiming the server
#     had returned an error page, and InstalledModels hid hand-copied models.
#     The package's own tests could not see it because they build their fixtures
#     with `copy(buf, ggmlMagic)` -- the same wrong constant.
#   * The SHA-256 check was comparing against the wrong hash. Hugging Face's
#     302 carries the LFS object's SHA-256 in X-Linked-Etag and redirects to a
#     CDN that, since the move to Xet storage, sets a plain Etag holding the
#     xetHash -- a different hash of the same bytes, also bare 64-hex, so it
#     matched the regex and failed the comparison. Every download was rejected.
#
# TIERS. Steps 1-5 need no credentials and no large transfer; they are the
# floor. Steps 6 and 7 fetch a real 74 MB model and run it, and skip without
# POLY_TRANSCRIBE_DOWNLOAD=1. Nothing here needs an account: the Hugging Face
# repo is public and every request is anonymous, so there is no secret in this
# suite to leak, and none in argv.
#
# NO FIXTURE IS COMMITTED. The recordings are built at run time from lavfi tone
# sources, which is what makes step 4 possible at all -- two tracks that can be
# told apart by measurement.
#
# EVERY CHECK WAS PROVEN ABLE TO FAIL against the committed tree, by the exact
# change named beside it. A check nothing could break would be worse than no
# check, because it reports a pass either way:
#
#   1 URLs resolve         models.go: "resolve/main/" -> "resolve/main/nope-"
#   2 ggml magic           driver: ggmlMagicWire -> {0x67,0x67,0x6d,0x6c}
#   3 sha256 advertised    driver: sha256ETagRE {64} -> {65}
#   4 sizes inside band    models.go: tiny Bytes 77_691_713 -> 7_691_713
#   5 sizes exact          models.go: tiny Bytes 77_691_713 -> 77_690_000
#   6 missing is refused   download.go: skip the StatusOK test AND stub
#                          verifyTransfer to ("none", nil)
#   7 nothing left behind  the same change; the 404 body lands on disk
#   8 fixture premise      driver: drop "-map", "0:v" from the fixture
#   9 track 0 is 440 Hz    args.go: "0:a:"+Track -> "0:"+Track
#  10 track 1 is 880 Hz    the same change; track 1 comes out as track 0's tone
#  11 binary detected      detect.go: BinaryNames -> {"whisper-cli-nope"}
#  12 help text parsed     detect.go: parseHelpFlags returns nil
#  13 gated flags present  driver: gatedFlags "output-json-full" ->
#                          "output-json-ultra"
#  14 model fetched        models.go: the URL change from check 1
#  15 sha256 enforced      download.go: verifyTransfer returns "length" where it
#                          returns "sha256"
#  16 verifies at rest     download.go: VerifyModelFile band -> (Bytes-1, Bytes-1)
#  17 arguments accepted   args.go: append "--not-a-flag" to WhisperArgs
#  18 JSON at JSONPath     args.go: JSONPath returns prefix + ".jsonx"
#  19 offsets sane         parse.go: StartMS -> t.Offsets.From + 99999999
#
# Check 17 is the one worth dwelling on, because the first draft of it WAS
# vacuous and the mutation is what exposed it. It read the exit status alone,
# and whisper-cli answers an argument it does not recognise by printing "error:
# unknown argument", printing its usage, and exiting ZERO -- so the check passed
# while the thing it named was broken. It now reads the output. worker.go has
# the same blind spot and this branch does not fix it; see the driver.
#
#   ./scripts/acceptance-transcribe.sh
#
# Environment:
#   POLY_TRANSCRIBE_DOWNLOAD=1   enables step 6; without it steps 6-7 skip
#   POLY_TRANSCRIBE_MODEL_DIR    where the model is cached
#                                (default ~/.cache/polyemesis-acceptance/models)
#   POLY_TRANSCRIBE_MODEL        which model to use (default: tiny)
set -uo pipefail

SCRIPTS="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPTS/.." && pwd)"
DRIVER="$ROOT/scripts/acceptance_transcribe_driver.go"

cd "$ROOT" || exit 1

pass=0; fail=0; skip=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; pass=$((pass+1)); }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=$((fail+1)); }
sk()   { printf "  \033[33mSKIP\033[0m  %s\n" "$1"; skip=$((skip+1)); }
step() { printf "\n\033[1m%s\033[0m\n" "$1"; }
note() { printf "        %s\n" "$1"; }

# drive runs one driver command and captures its key=value lines.
drive() { go run "$DRIVER" "$@" 2>&1; }

# val <output> <key> -> the value, or empty. Anchored, which here is doing real
# work rather than being defensive: whisper.cpp prints its backend probe on
# stderr and several of those lines contain an = sign.
val() { printf '%s\n' "$1" | sed -n "s/^$2=//p" | head -1; }

printf "\033[1mtranscription, against the real model host and the real binaries\033[0m\n"

# ---------------------------------------------------------------------------
step "1. Every model in the catalogue is really on the host"

OUT="$(drive catalogue)"
MODELS="$(val "$OUT" models)"

case "$MODELS" in
  ''|*[!0-9]*) bad "the driver did not report a catalogue size"; MODELS=0 ;;
  *) : ;;
esac

if [ "${MODELS:-0}" -gt 0 ] && [ "$(val "$OUT" resolved)" = "$MODELS" ]; then
  ok "all $MODELS catalogue URLs resolve on huggingface.co"
else
  bad "only $(val "$OUT" resolved) of $MODELS catalogue URLs resolve"
  note "$(val "$OUT" firstBad)"
fi

# NOT THE SAME CHECK AS "IT RESOLVED". download.go's first integrity check
# exists because the common failure is a proxy or login wall serving an HTML
# error page with a 200, so a URL that answers is not yet a URL that serves a
# model. Four bytes per model settles it.
if [ "${MODELS:-0}" -gt 0 ] && [ "$(val "$OUT" magicOK)" = "$MODELS" ]; then
  ok "all $MODELS begin with the ggml magic, so they are models and not error pages"
else
  bad "$(val "$OUT" magicOK) of $MODELS begin with the ggml magic"
  note "$(val "$OUT" firstBad)"
fi

# The strongest of the three integrity checks is only available if the host
# offers a content hash. When it does not, Download does not fail -- it quietly
# drops to a length check and records "length" in a field nobody reads.
if [ "${MODELS:-0}" -gt 0 ] && [ "$(val "$OUT" sha256OK)" = "$MODELS" ]; then
  ok "all $MODELS advertise a sha256, so the strongest check is available"
else
  bad "only $(val "$OUT" sha256OK) of $MODELS advertise a sha256"
  note "the rest would silently fall back to a byte-count check"
fi

# ---------------------------------------------------------------------------
step "2. The catalogue's published sizes still match the host"

# THE RESTRICTIVE-DIRECTION CHECK. VerifyModelFile rejects a model more than a
# tenth away from the catalogue's Bytes. That tolerance is generous, but it is
# still a hardcoded number describing somebody else's server, and when it goes
# stale the failure is a good download reported as a corrupt one.
if [ "${MODELS:-0}" -gt 0 ] && [ "$(val "$OUT" inBand)" = "$MODELS" ]; then
  ok "all $MODELS sizes are inside the band VerifyModelFile accepts"
else
  bad "$(val "$OUT" inBand) of $MODELS are inside the band; a good download would be rejected"
  note "$(val "$OUT" firstBad)"
fi

# Exactness is a separate and weaker statement than the band, and it is the
# early warning for it: a size that has drifted at all has been re-uploaded, and
# the next re-upload is the one that leaves the band.
if [ "${MODELS:-0}" -gt 0 ] && [ "$(val "$OUT" exact)" = "$MODELS" ]; then
  ok "all $MODELS still match the published byte count exactly"
else
  bad "$(val "$OUT" exact) of $MODELS match exactly; the catalogue has drifted"
  note "update Bytes in models.go before the drift leaves the band"
fi

# ---------------------------------------------------------------------------
step "3. A model the host does not have is refused, and leaves nothing behind"

# Download opens its .part file BEFORE it has verified anything, so the
# interesting question is not only whether a 404 is reported but whether the
# directory is clean afterwards. A leftover part-file under a model's real name
# is a file InstalledModels would go on to offer.
OUT="$(drive refusal)"

if [ "$(val "$OUT" refused)" = true ]; then
  ok "a missing model is refused: $(val "$OUT" error)"
else
  bad "a model that does not exist upstream was ACCEPTED as a download"
  note "wrote $(val "$OUT" acceptedBytes) bytes to $(val "$OUT" acceptedPath)"
fi

if [ "$(val "$OUT" filesLeft)" = 0 ]; then
  ok "nothing was left in the model directory"
else
  bad "$(val "$OUT" filesLeft) file(s) left behind: $(val "$OUT" leftNames)"
fi

# ---------------------------------------------------------------------------
step "4. The right microphone comes out of the right track"

# THE PACKAGE'S DIFFERENTIATOR, AND ITS QUIETEST FAILURE. The doc comment says
# "the track index IS the speaker attribution"; ExtractArgs' comment says the
# map must be 0:a:N and not 0:N because the absolute form counts the video track
# and would be "silently transcribing the wrong microphone". A transcript
# attributed to the wrong speaker is fluent, plausible and wrong, and nothing
# downstream can tell. So the two tracks are made tellable apart -- 440 Hz and
# 880 Hz -- and what comes out is measured.
OUT="$(drive tracks)"

if [ "$(val "$OUT" ffmpeg)" != true ]; then
  sk "no ffmpeg on PATH; track extraction was not exercised"
  sk "track 0 was not measured"
  sk "track 1 was not measured"
  note "install ffmpeg to run this step"
else
  # THE PREMISE OF THE OTHER TWO CHECKS, asserted rather than assumed. Without a
  # video stream the absolute and stream-relative indices agree, and the two
  # measurements below would pass under exactly the bug they exist to catch.
  if [ "$(val "$OUT" videoStreams)" = 1 ] && [ "$(val "$OUT" audioStreams)" = 2 ]; then
    ok "the fixture has a video stream and two audio tracks, so the indices differ"
  else
    bad "fixture is video=$(val "$OUT" videoStreams) audio=$(val "$OUT" audioStreams); the next two checks would prove nothing"
  fi

  if [ "$(val "$OUT" track0Correct)" = true ]; then
    ok "track 0 extracted at $(val "$OUT" track0Hz) Hz, as recorded"
  else
    bad "track 0 came out at $(val "$OUT" track0Hz) Hz, expected ~440"
    note "$(val "$OUT" track0Error)"
  fi

  # The decisive one: under `0:N` this would be the 440 Hz track.
  if [ "$(val "$OUT" track1Correct)" = true ]; then
    ok "track 1 extracted at $(val "$OUT" track1Hz) Hz, not track 0's 440"
  else
    bad "track 1 came out at $(val "$OUT" track1Hz) Hz, expected ~880 -- the wrong microphone"
    note "$(val "$OUT" track1Error)"
  fi
fi

# ---------------------------------------------------------------------------
step "5. The real whisper.cpp build has the flags the argument builder gates on"

OUT="$(drive whisper)"

if [ "$(val "$OUT" found)" != true ] && [ "$(val "$OUT" onPath)" != true ]; then
  sk "whisper.cpp is not installed; the flag gating was not exercised"
  sk "the help text was not parsed"
  sk "the gated flags were not checked against a real build"
  note "macOS: brew install whisper-cpp"
elif [ "$(val "$OUT" found)" != true ]; then
  # NOT A SKIP. A binary is sitting on PATH under a name whisper.cpp ships
  # under, and Detect walked past it -- which on a real install is transcription
  # silently unavailable with the tool present. Distinguishing this from "not
  # installed" is the whole reason the driver searches PATH itself.
  bad "$(val "$OUT" onPathBinary) is on PATH but Detect did not find it"
  note "$(val "$OUT" error)"
  sk "the help text was not parsed"
  sk "the gated flags were not checked against a real build"
else
  ok "detected $(val "$OUT" binary)"

  # THE ANTI-VACUITY CHECK, and without it step 5 is worthless. Tools.HasFlag
  # returns true for EVERY name when the flag set is empty -- deliberately, so
  # an unreadable help text fails open rather than switching features off. The
  # consequence is that "no flags are missing" is trivially true for a build
  # whose help we failed to parse, which is precisely the case where the answer
  # matters. So the parse is asserted before the conclusion drawn from it.
  FLAGS="$(val "$OUT" flagCount)"
  case "$FLAGS" in
    ''|*[!0-9]*) bad "the help text did not parse into a flag set" ;;
    *) if [ "$FLAGS" -gt 0 ]; then
         ok "its help text parsed into $FLAGS flags, so HasFlag is answering from evidence"
       else
         bad "the help text parsed into zero flags; HasFlag would fail open and say yes to everything"
       fi ;;
  esac

  if [ "$(val "$OUT" missing)" = 0 ]; then
    ok "all $(val "$OUT" gated) gated flags are advertised by this build"
  else
    bad "$(val "$OUT" missing) gated flag(s) absent: $(val "$OUT" missingNames)"
    note "WhisperArgs would omit them, or a wrong gate would cost the whole job"
  fi
fi

# ---------------------------------------------------------------------------
step "6. A real model, downloaded and verified"

# THE ONLY STEP THAT MOVES REAL WEIGHT -- 74 MB -- so it is opt-in, in the shape
# the chat suite uses for its credentialed step. Step 1 already established that
# a sha256 is on OFFER for every model; this establishes that one was ENFORCED,
# which is a different claim and the one that broke.
if [ -z "${POLY_TRANSCRIBE_DOWNLOAD:-}" ]; then
  sk "no POLY_TRANSCRIBE_DOWNLOAD; the 74 MB model download was not attempted"
  sk "the download's checksum was not enforced"
  sk "the downloaded model was not re-verified at rest"
  note "set POLY_TRANSCRIBE_DOWNLOAD=1 to run it; the model is cached after the first run"
else
  OUT="$(drive download)"
  if [ "$(val "$OUT" ok)" = true ]; then
    ok "fetched $(val "$OUT" model) in $(val "$OUT" elapsedMs)ms, $(val "$OUT" bytes) bytes"
  else
    bad "the download failed: $(val "$OUT" error)"
  fi

  # "length" here is not a pass. It is the silent downgrade: the transfer
  # succeeded, the file is probably fine, and the strongest check did not run.
  case "$(val "$OUT" verified)" in
    sha256) ok "the content sha256 was enforced against the host's own figure" ;;
    '')     bad "no verification level was reported" ;;
    *)      bad "verified via '$(val "$OUT" verified)', not sha256 -- the strong check did not run" ;;
  esac

  # The two checks that decide whether the model is usable after the download:
  # the gate applied before whisper is handed it, and the listing the picker
  # reads. They disagreed with the download-time check once already.
  if [ "$(val "$OUT" verifyAtRest)" = true ] && [ "$(val "$OUT" listedAsKnown)" = true ]; then
    ok "it verifies at rest and is listed as a known model"
  else
    bad "atRest=$(val "$OUT" verifyAtRest) listed=$(val "$OUT" listedAsKnown); $(val "$OUT" verifyError)"
  fi
fi

# ---------------------------------------------------------------------------
step "7. A recording becomes a parsed transcript"

# WHAT THIS PROVES: that both argument builders produce command lines the real
# programs ACCEPT, and that the JSON lands where the package says it will.
# args.go and worker.go agree on JSONPath(prefix) by convention and only
# whisper.cpp can confirm the convention.
#
# WHAT IT DOES NOT PROVE: accuracy. The audio is a tone, so there are no words
# to get right, and nothing here asserts anything about the text.
OUT="$(drive endtoend)"

if [ "$(val "$OUT" ready)" != true ]; then
  sk "not ready: $(val "$OUT" error)"
  sk "whisper was not run on a real extraction"
  sk "no transcript was parsed"
  note "needs ffmpeg, whisper.cpp, and a downloaded model (see step 6)"
else
  # A usage dump is the specific shape of "we passed a flag this build lacks",
  # which is the outcome the gating in step 5 exists to prevent.
  if [ "$(val "$OUT" accepted)" = true ]; then
    ok "whisper accepted all $(val "$OUT" argc) arguments and ran in $(val "$OUT" elapsedMs)ms"
  elif [ "$(val "$OUT" usageDump)" = true ]; then
    bad "whisper answered the argument list with a usage dump; a flag gate is wrong"
    note "$(val "$OUT" tail)"
  else
    bad "whisper failed: $(val "$OUT" exitError)"
    note "$(val "$OUT" tail)"
  fi

  if [ "$(val "$OUT" jsonWritten)" = true ]; then
    ok "the JSON landed exactly at JSONPath(), $(val "$OUT" jsonBytes) bytes"
  else
    bad "nothing at JSONPath(): $(val "$OUT" jsonError)"
    note "worker.go reads that path and would find nothing"
  fi

  # Parsed AND sane. ParseJSON returning no error on an empty document would
  # otherwise read as a pass, and offsets outside the audio would mean the
  # millisecond fields were misread -- which the text cannot reveal.
  if [ "$(val "$OUT" parsed)" = true ] && [ "$(val "$OUT" offsetsSane)" = true ]; then
    ok "ParseJSON read $(val "$OUT" segments) segment(s), language=$(val "$OUT" language), offsets inside the audio"
  else
    bad "parsed=$(val "$OUT" parsed) offsetsSane=$(val "$OUT" offsetsSane): $(val "$OUT" parseError)"
  fi
fi

# ---------------------------------------------------------------------------
printf "\n"
printf "  \033[1m%d passed, %d failed, %d skipped\033[0m\n\n" "$pass" "$fail" "$skip"

# The floor, so a run that dies halfway cannot report a green tally over four
# checks. FIXED, not a range: every branch contributes the same number either
# way -- steps 4, 5, 6 and 7 each count three whether they run or skip -- so the
# total does not move with what happens to be installed.
EXPECTED_CHECKS=19
total=$((pass + fail + skip))
if [ "$total" -lt "$EXPECTED_CHECKS" ]; then
  printf "  \033[31mINCOMPLETE\033[0m  only %d of %d checks ran; the run stopped early\n\n" \
    "$total" "$EXPECTED_CHECKS"
  exit 1
fi
if [ "$total" -gt "$EXPECTED_CHECKS" ]; then
  printf "  \033[33mNOTE\033[0m  %d checks ran, %d expected. If checks were added,\n" \
    "$total" "$EXPECTED_CHECKS"
  printf "        raise EXPECTED_CHECKS so the guard keeps its teeth.\n"
fi
[ "$fail" -eq 0 ]
