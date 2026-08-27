# Investigations

Scripts that answered a question about a bug, kept because **a negative result
is a result** and re-deriving one costs the same as deriving it the first time.

Nothing here runs in CI. Each is standalone: Docker, `apk add ffmpeg`, no
polyemesis involved. They are the exact scripts that produced the numbers
quoted on the issue, so a reader can disagree with the conclusion by running it
rather than by trusting the summary.

## #398 — FFmpeg 8.1 and a file destination fed a mid-stream resolution change

The claim under test: *a file destination fed content whose parameters change
halfway through produces an empty recording on the FFmpeg version users
actually run.* CI's 6.1.1 could never have seen it; the Docker image pins
8.1.2.

Run any of these against both versions — `alpine:3.20` ships 6.1.1 and
`alpine:3.24` ships 8.1.2:

```sh
docker run --rm --network host -v "$PWD/docs/investigations/398-e-probe-window.sh:/r.sh:ro" \
  alpine:3.24 sh -c 'apk add --no-cache ffmpeg >/dev/null 2>&1 && sh /r.sh'
```

| Script | Question | Answer |
|---|---|---|
| `398-b-stop-signals.sh` | Does the parameter change plus a SIGKILL/SIGTERM/SIGINT explain a header-only file? | **No.** Identical on both versions; SIGKILL loses the trailer but still leaves 3 MB of clusters. |
| `398-c-suite-ordering-over-udp.sh` | Does the failover suite's own ordering, read from `udp://` unseekable, reproduce it? | **No.** 8.1.2 handled the change *more cleanly* than 6.1.1 — no decoder errors at all. |
| `398-d-real-destination-argv.sh` | Does the command polyemesis actually builds — video `-c:v copy`, audio through the routing filtergraph — behave differently? | **Inconclusive.** The harness could not keep a decodable stream up; see the note below. |
| `398-e-probe-window.sh` | The real command carries `-analyzeduration 15000000`. Does a destination stopped inside that window write only a header? | **No.** Stopped at 8s it holds 1.7 MB and 8.18s of media: FFmpeg starts writing as soon as it has enough, long before the ceiling. |

### What the real destination command is

Read out of a running container rather than reconstructed, because the
difference from a hand-rolled `-c copy` turned out to matter:

```
ffmpeg -hide_banner -nostdin -loglevel warning -nostats -progress pipe:1
  -fflags +genpts -thread_queue_size 1024
  -analyzeduration 15000000 -probesize 33554432
  -i udp://127.0.0.1:PORT?fifo_size=32768&overrun_nonfatal=1
  -filter_complex "[0:a:0]pan=stereo|...[a_t0];[0:a:1]pan=stereo|...[a_t1];
                   [a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];
                   [a_mix]alimiter=limit=0.95:level=disabled[a_norm];
                   [a_norm]aresample=48000:async=1:first_pts=0[aout]"
  -map 0:v:0 -c:v copy
  -map [aout] -c:a aac -b:a 160k -ac 2 -ar 48000
  -f matroska /data/recordings/NAME.mkv
```

The video is copied and the audio is **re-encoded through a filtergraph**. Every
earlier attempt at this bug copied both, which is not what runs.

### A harness lesson worth not repeating

`cat a.ts b.ts` produces non-monotonic timestamps at the junction, and `-re`
paces from those timestamps — so the publisher stalls or races and the
subscriber sees `non-existing PPS 0` for the rest of the run. Script `d` still
has this flaw and its result should be read as "the harness broke", not "the
product broke". A real parameter change arrives from a source *selector*
switching between two live encoders, which is not the same thing.

### What is still open

The muxer is not the cause: it accepts the change on 8.1.2 at every fidelity
tried.

**This is corroboration, not a discovery.** `scripts/acceptance-failover.sh`
already says the same thing, and says it more precisely, in the comment above
its `#398 watch` block:

> Measured since: FFmpeg 8.1.2 does NOT refuse a mid-stream resolution change
> [...] So the empty file is not the muxer refusing the change; it is the file
> a destination had already stopped writing to when it **respawned**.

The scripts here were written without noticing that, and reached the same
conclusion by a different route on two FFmpeg versions rather than one. That
is worth keeping — an independent confirmation of a conclusion nobody had
tested against 6.1.1 — but the credit for the conclusion belongs to that
comment, and the open question is the one it names:

**why does the destination respawn at all?**

Not the muxer, not the stop signal, not the probe window. The suite already
reports a header-only sibling when it sees one, so the next recurrence should
arrive with its restart count attached.

## FFmpeg 9.0.1 — is the upgrade from 8.1.2 safe?

[`ffmpeg-9-upgrade-risk.md`](ffmpeg-9-upgrade-risk.md). Short answer: **not
yet, and the blocker is packaging rather than behaviour** — Alpine has not
packaged any 9.x, `edge` included, so `apk add ffmpeg=9.0.1` has nothing to
install and the upgrade means changing where FFmpeg comes from.

Behaviour measured rather than assumed: every Go package that shells out to
FFmpeg passes against 9.0.1, including `internal/ffmpeg`, which is where the
version-sensitive knowledge lives. The one failure is an artefact of the
container harness and fails identically on 8.1.2 — the control is what proved
that, and it is why the control was run.
