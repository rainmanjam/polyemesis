# The homepage tour

`web/public/media/tour.mp4` (about 100 s, silent) is recorded from the real console running the sports demo scene, assembled in a HyperFrames project and encoded by one script. This folder holds the source. Nothing large is committed: raw captures, trimmed clips and renders are git-ignored.

## Poster

The poster is the frame at 52.2 s of the render, written by `publish.sh` to `web/public/media/tour-poster.jpg`.

## Prerequisites

Docker (the rig), Go (the version in `go.mod`), Node, `ffmpeg` and `ffprobe`, system Google Chrome, Python 3 with `pip install playwright`. For the frame check: `tesseract`. HyperFrames is run as `npx hyperframes@0.9.39` (pinned in `hf/package.json`; no install step).

## Order

1. `scripts/demo-seed.sh --scene sports --no-capture` starts the rig (console at `http://127.0.0.1:8097`; set `TOUR_BASE` for another URL). The sign-in password is read from `${DEMO_WORK:-${TMPDIR:-/tmp}/polyemesis-demo-sports}/admin.pw` and is never printed.
2. `python3 scripts/tour/shots_run.py 1a 1b 2a 2b 2c 3a 3b 3c` films each shot (a CDP screencast with per-frame timestamps, so the cursor and clicks are paced in real time). Raw captures go to `scripts/tour/raw/` (about 1.2 GB).
3. `python3 scripts/tour/build_assets.py` trims the raw shots into `scripts/tour/hf/assets/shots/` (about 32 MB), copies the three fonts from `web/public/fonts/` into `hf/assets/fonts/`, and writes `edit.json` (each shot's duration).
4. `python3 scripts/tour/build.py` regenerates `hf/index.html` and `hf/compositions/*.html` from `edit.json`, and `chapters.json`. The committed HTML is its output.
5. In `scripts/tour/hf/`: `npx hyperframes@0.9.39 render`, then move the render to `scripts/tour/out/tour-master.mp4`.
6. `bash scripts/tour/publish.sh` encodes the master into `web/public/media/tour.mp4` (H.264 high, `+faststart`, no audio, 1920x1080, `-crf` 20 up to 30 until it is at most 8,000,000 bytes) and extracts the poster. These are its only two outputs. It then runs the check below.
7. `bash scripts/tour/check_frames.sh scripts/tour/out/tour-master.mp4` OCRs a frame every 0.5 s and exits 1 listing the times where `Nothing is live`, `No chat account`, `not connected`, `YouTube · YouTube` or `No platform accounts connected` appear. It exits 2 when `tesseract` is missing. Work files go to `scripts/tour/check/`.
8. `python3 scripts/tour/frames.py <video>` then `python3 scripts/tour/sheets.py` make contact sheets in `scripts/tour/out/sheets/` to look at.

`bash scripts/tour/publish.sh --check` writes nothing and exits 1 unless the mp4 exists, is at most 8,000,000 bytes, has no audio stream and is 1920x1080; the poster exists; the mp4's duration is within 0.5 s of `TOUR.duration` in `web/src/data/tour.mjs`; and `TOUR.chapters` there has the same starts and titles as `chapters.json`. (`chapters.json` holds the on-video form, `01 · ONE STREAM IN`; `tour.mjs` the display form, `One stream in`. They match with the number prefix dropped and case ignored.) After a re-film, update `web/src/data/tour.mjs` to the new duration and starts.

## Where things go

| Artefact | Path | Committed |
|---|---|---|
| Raw captures | `scripts/tour/raw/` | no |
| Trimmed clips, fonts | `scripts/tour/hf/assets/` | no |
| `edit.json`, master render, sheets | `scripts/tour/edit.json`, `out/` | no |
| Frame-check work | `scripts/tour/check/` | no |
| HyperFrames' generated `AGENTS.md`, `CLAUDE.md` | `scripts/tour/hf/` | no |
| Chapters | `scripts/tour/chapters.json` | yes |
| Published tour and poster | `web/public/media/` | yes (at most 8 MB) |

## Rules

- Captions and callouts are off-white, never red, coral or orange: those colours mean audio and failure on the console.
- The shots show the console as it is. Frame or crop in the HyperFrames composition; never hide or edit elements of the console.
- No voiceover, music or sound effects.
- The tour is re-recorded when the console's layout in the shots changes.
- The scripts are Python because the capture needs a CDP screencast with per-frame timestamps; porting them is a separate decision.
