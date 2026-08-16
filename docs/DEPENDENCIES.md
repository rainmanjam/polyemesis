# Dependencies

Why each dependency is here, and why some obvious alternatives are not.

This file exists so that the next person to run `go list -u -m all` or `npm
audit` can tell the difference between "this is stale and should be bumped" and
"this is pinned on purpose, leave it alone". If you change something here,
update this file in the same commit.

Last reviewed: 2026-07-26.

## The constraints that decide everything else

Four properties drive nearly every dependency choice in this repo. When a
candidate library conflicts with one of them, the library loses.

1. **One self-contained binary.** `make build` produces a single executable
   with the web UI embedded via `go:embed`. No sidecar files, no runtime asset
   directory, no separate frontend server.
2. **cgo-free.** The binary must build with `CGO_ENABLED=0`. This is what makes
   the cross-compile matrix a one-line command instead of a fleet of C
   toolchains, and it is what lets the Docker image run on Alpine (musl)
   without a glibc shim.
3. **Four targets, cross-compiled from anywhere.** linux/amd64, linux/arm64,
   darwin/arm64, windows/amd64. A dependency that only builds on the host OS is
   not a candidate.
4. **FFmpeg stays at arm's length.** It is a child process, never a linked
   library. See [FFmpeg](#ffmpeg-a-subprocess-not-a-library) below — this one is
   a licensing decision as much as an architectural one.

Go toolchain: **1.26.6**.

## Direct Go dependencies

Ten, deliberately. Each one earns its place below.

| Module | Version | Used by |
| --- | --- | --- |
| `github.com/bluenviron/gortmplib` | v1.0.0 | `internal/rtmpserver` |
| `github.com/datarhei/gosrt` | v0.11.0 | `internal/srtserver` |
| `github.com/eclipse/paho.golang` | v0.23.0 | `internal/mqtt` |
| `github.com/go-chi/chi/v5` | v5.3.1 | `internal/api` |
| `github.com/golang-jwt/jwt/v5` | v5.3.1 | `internal/auth` |
| `github.com/gorilla/websocket` | v1.5.3 | `internal/api` |
| `github.com/shirou/gopsutil/v4` | v4.26.6 | `internal/stats` |
| `golang.org/x/crypto` | v0.54.0 | `internal/secrets`, `internal/db`, `internal/tlsx` |
| `golang.org/x/sys` | v0.47.0 | `internal/recording`, `internal/supervisor`, service wrapper |
| `gopkg.in/yaml.v3` | v3.0.1 | `internal/config` |
| `modernc.org/sqlite` | v1.54.0 | `internal/db` |

### `github.com/datarhei/gosrt` — and the test a protocol dependency must pass

Added for one-port token-addressed ingest. It is the only dependency here that
implements a *wire protocol*, and it is the precedent every future one gets
measured against.

The rule it establishes: **a protocol dependency is justified only when FFmpeg
cannot do the job.**

That is what separates it from `yutopp/go-rtmp`, which was measured and
rejected — see
[DESIGN-ONE-PORT-ONLY.md](DESIGN-ONE-PORT-ONLY.md#rtmp). go-rtmp would have
pulled seven further modules including `logrus` (a second logging framework in a
binary that uses `log/slog`) plus `pkg/errors` and `mapstructure`, both dead
upstream. gosrt had no such alternative: FFmpeg's SRT support is a client and a
single-connection listener, and neither can demultiplex many publishers by
`streamid` on one port.

The same reasoning eventually applied to RTMP, and it is why
`github.com/bluenviron/gortmplib` joined the tree on 2026-08-06: three
transitive modules against go-rtmp's seven, at v1.0.0 rather than v0.0.7, from
the people who maintain gortsplib and MediaMTX. Re-measured, not assumed —
see `evidence/multi-source-rtmp.md`.

> **The RTMP half of that comparison was retired on 2026-08-06.** It read "and
> `ffmpeg -listen 1` was already a complete answer for RTMP", and that was the
> claim that failed: `-listen 1` is *also* a single-connection listener, so it
> could not demultiplex many publishers by `app/streamkey` on one port either —
> the identical objection, which nobody had noticed applied to both. The rule
> survives unchanged and was in fact what settled the question; only the belief
> that FFmpeg satisfied it for RTMP was wrong. See
> [gortmplib](#githubcombluenvirongortmplib--the-third-protocol-dependency)
> below.

Pure Go, MIT, and `CGO_ENABLED=0` clean.

> One licence detail worth knowing, recorded in
> [MODULES.md](MODULES.md#one-licence-note-worth-knowing):
> gosrt reaches `github.com/benburkert/openpgp` for AES key wrapping, and that
> module ships no LICENSE file despite every source file carrying the Go
> Authors' BSD header.

### `github.com/eclipse/paho.golang` — the second protocol dependency

Added for [retained MQTT telemetry](MQTT.md), and it clears the bar gosrt set
more easily than gosrt did: **FFmpeg does not speak MQTT at all**, so there was
no "does the tool we already ship cover this" question to answer.

The measured cost is the smallest of any dependency here. Its build graph is
three modules — itself, `gorilla/websocket` and `golang.org/x/net` — and
polyemesis already had both of the latter as direct dependencies. **Net-new
modules: exactly one.** Binary cost, measured against a paired build rather than
quoted: **+586 KB on 25.4 MB (+2.4%)**.

Pure Go, EPL-2.0/BSD dual, and `CGO_ENABLED=0` clean on all four shipping
targets.

Two things an operator has to know, both consequences of the library rather than
of our code:

- **It implements MQTT 5.0 only.** Its own README says so. A broker pinned to
  3.1.1 will not complete a connection — not a degraded mode, a failure to
  connect.
- **`autopaho`'s `Queue: nil` is silently replaced with an in-memory queue**
  (`auto.go:271`), contradicting the field's own comment. It is harmless here
  because `Publish` never touches the queue, but anyone reaching for
  `PublishViaQueue` needs to know the default is not "no buffering".

`paho.mqtt.golang` — the older sibling — was not taken: it is in maintenance
mode and speaks 3.1.1, which is the wrong direction for a new integration.

### `github.com/bluenviron/gortmplib` — the third protocol dependency

Added for `internal/rtmpserver`, so that how many sources an install can carry
does not depend on which protocol the encoder speaks.

It clears gosrt's bar for exactly the reason gosrt did, applied to the protocol
nobody had thought to apply it to. FFmpeg's RTMP support is a client and
`-listen 1`, a single-connection listener — the same shape as its SRT support,
and the same failure: neither can demultiplex many publishers by `app/streamkey`
on one port. The earlier "FFmpeg was already a complete answer for RTMP" was
true only of the single-source case, which is the case that was the problem.

The dependency that was rejected is not this one. That measurement is worth
keeping side by side, because the difference is the whole justification:

| | net-new modules | version | provenance |
|---|---|---|---|
| `datarhei/gosrt` | 1 | v0.11.0 | datarhei |
| **`bluenviron/gortmplib`** | **3** — `abema/go-mp4`, `bluenviron/mediacommon/v2`, `google/uuid`* | v1.0.0 | gortsplib / MediaMTX maintainers |
| `yutopp/go-rtmp` (rejected) | 7 — `logrus`, `pkg/errors`, `mapstructure`, … | v0.0.7 | unchanged since 2021 |

\* `google/uuid` was already in the tree via sqlite, so the true net-new count
is two.

Three modules is a real cost and not a free one. What makes it payable rather
than a reversal of the earlier decision:

- **None of it is dead upstream**, which was the specific objection to go-rtmp's
  tree — `mapstructure` pinned at a 2021 release, `pkg/errors` archived.
- **No second logging framework.** go-rtmp brings `logrus` into a binary that
  uses `log/slog`.
- **v1.0.0, not v0.0.7**, from the people who maintain gortsplib and MediaMTX —
  the same provenance argument that justified gosrt.
- All three are MIT and `CGO_ENABLED=0` clean.

**Only the `Conn` seam is used, never `Reader`.** gortmplib will hand over
decoded access units if you ask it to; taking that would put a muxer in the
critical path of every frame and make a class of bug ours that currently belongs
to FFmpeg. `internal/rtmpserver` forwards RTMP *messages*, which is why
`-map 0 -c copy` downstream is untouched and Enhanced RTMP multitrack works
without the package knowing what a track is. That restraint is the reason this
is a ~540-line package and not a media stack.

### `modernc.org/sqlite` — and why not `mattn/go-sqlite3`

This is the single most consequential dependency in the project, and the one
place where the cgo-free rule does real work.

`mattn/go-sqlite3` is the better-known SQLite driver and in isolation it is the
more mature one. It is also cgo. Taking it would mean:

- a C cross-compiler for every target, so `GOOS=windows go build` stops being
  something you can run on a laptop;
- a glibc/musl split in the Docker image, since a cgo binary built against
  glibc does not run on Alpine;
- `CGO_ENABLED=0` builds failing outright, which is the default posture for
  reproducible Go releases.

`modernc.org/sqlite` is a pure-Go translation of the SQLite C source. It is
slower than the cgo driver under write-heavy load, and that trade is
acceptable here: this database holds configuration, users, destinations and
recording metadata — kilobytes of rows read and written at human speed, not a
hot path. The media path never touches SQLite.

The driver registers itself as `sqlite` (not `sqlite3`), so `sql.Open("sqlite",
dsn)` is correct and is what `internal/db/db.go` does.

**Treat this dependency with more care than the others.** It pulls
`modernc.org/libc`, which is a large generated surface. Any bump to either
should be followed by an explicit `go test ./internal/db/ ./internal/engine/`
run, not just a whole-suite pass.

### `golang.org/x/crypto`

Four separate subpackages, all load-bearing, which is why this is a direct
dependency rather than something vendored more narrowly:

- `bcrypt` — user password hashing (`internal/db/users.go`).
- `nacl/secretbox` — encrypting stored destination credentials at rest
  (`internal/secrets`).
- `acme` and `acme/autocert` — Let's Encrypt certificate issuance in
  `internal/tlsx`.

### `golang.org/x/sys`

The portability seam for everything the standard library will not do.
`unix` for the disk-space statfs call and process-group signalling; `windows`
for the equivalents; `windows/svc` and `windows/svc/eventlog` for running as a
Windows service. Because it is the layer that makes per-OS behaviour possible,
a bump here should always be re-checked against all four cross-compile targets.

### `github.com/go-chi/chi/v5`

The HTTP router. Chosen because it is `net/http`-native: handlers are plain
`http.HandlerFunc`, middleware is plain `func(http.Handler) http.Handler`, and
nothing in `internal/api` has to be written against a framework's own types.
That keeps the handlers testable with `httptest` alone and means chi could be
removed without rewriting the handlers.

### `github.com/golang-jwt/jwt/v5`

Session tokens. The v5 line is the maintained continuation of the original
`dgrijalva/jwt-go`, which was abandoned; do not let anything pull the old path
back in.

### `github.com/gorilla/websocket`

One file, `internal/api/ws.go`: the live stats/event feed that drives the UI.
Gorilla was un-archived and is maintained again. `nhooyr.io/websocket` /
`coder/websocket` would also work; there is no reason to churn.

### `github.com/shirou/gopsutil/v4`

CPU, memory and per-process metrics for the monitoring page. This is the
dependency with the widest OS-specific surface, and it is the source of most of
the indirect modules below (`go-ole`, `purego`, `tklauser/*`, `plan9stats`,
`perfstat`, `wmi`). It is pure Go on every target we ship.

### `gopkg.in/yaml.v3`

Config file parsing, one file. Frozen upstream at v3.0.1 for years — that is
its steady state, not neglect. `go list -u` will never show an update for it.

## FFmpeg: a subprocess, not a library

polyemesis executes `ffmpeg` and `ffprobe` as child processes and talks to them
over argv, stdin/stdout and signals. It does **not** link `libavcodec`,
`libavformat` or any other FFmpeg library, and it must not start doing so.

There are two independent reasons, and either alone would be sufficient.

**Licensing.** FFmpeg is LGPL-2.1-or-later at its core, but a great many real
builds — including most distribution packages, and anything configured with
`--enable-gpl` for x264, x265 or libpostproc — are GPL-2.0-or-later. Linking
those libraries into this binary would make the combined work subject to the
GPL. Executing FFmpeg as a separate program communicating over documented
process boundaries (argv, pipes, signals) is arm's-length aggregation, not
derivation, so the licence of the FFmpeg build the operator installs does not
propagate to polyemesis. This is the same posture taken by every other
supervisor-style tool in this space, and it is why the operator supplies their
own FFmpeg.

**Operational.** A crash inside a linked codec takes the whole server down with
it — every other stream included. A crashed child process is a supervised
restart of one stream. It also means the operator can upgrade, downgrade or
swap their FFmpeg build (hardware encoders, SRT support, patched codecs)
without us shipping a new binary.

The consequence to keep in mind: FFmpeg's behaviour is part of this product's
contract but not part of its build. `internal/ffmpeg` probes the installed
binary at startup for version, protocols and encoders rather than assuming.

The Docker image bundles Alpine's `ffmpeg` package as a convenience — Alpine's
build has libsrt, which is why the image is Alpine rather than distroless. That
bundling is aggregation in an image, and does not change the analysis above.

> polyemesis is MIT licensed; see [LICENSE](../LICENSE). The arm's-length
> posture above is what keeps a GPL FFmpeg build from propagating into that.

## Indirect Go dependencies

Every indirect module traces to one of a small, countable set of roots:
`gopsutil`, `modernc.org/sqlite`, `golang.org/x/crypto`, `gosrt` (one module,
`benburkert/openpgp`) and `gortmplib` (`abema/go-mp4`,
`bluenviron/mediacommon/v2`, and `google/uuid`, which sqlite already pulled).
There are no other transitive sources. Keeping that list short enough to write
down is the property worth preserving; `go mod why -m` should always have a
one-line answer.

They are kept current on a plain `go get -u ./... && go mod tidy`, gated on the
full verification below. As of the last review all indirect modules resolve to
their latest published versions.

A handful of modules appear in `go.sum` but not in `go.mod` — `google/pprof`,
`golang.org/x/mod`, `golang.org/x/tools`, `golang.org/x/xerrors`,
`check.v1`, `modernc.org/cc/v4`, `modernc.org/gc/v3`, and from the gortmplib
side `orcaman/writerseeker`, `sunfish-shogi/bufseekio`, `asticode/go-astits`,
`asticode/go-astikit` and `stretchr/testify`. These are build- and test-time
dependencies of other modules' own suites and code generation. They are not in
the binary — `go.mod`'s indirect block is the list that is, which is why the
net-new count above is three and not thirteen. `go list -u -m all` will report
some of them as outdated forever; that is expected and is not a finding.

## Frontend dependencies

The UI is built by Vite and embedded into the Go binary, so it has no runtime
dependency story — everything below is build-time or ships as bundled JS.

### TypeScript 7

Moved from `~6.0.2` to `~7.0.2` on 2026-07-26.

TypeScript 7 is the native (Go) port of the compiler, so this is a genuine
implementation change, not just a version number. It was adopted only after
checking the things a rewrite is most likely to break:

- `tsc -b --noEmit --force` over the whole project: clean, zero errors.
- **No tsconfig changes were required.** `erasableSyntaxOnly` and
  `verbatimModuleSyntax` — the two settings most exposed to a compiler
  rewrite — behave identically.
- TypeScript 7 ships a **per-platform native binary** as an optional
  dependency. Since the Docker UI stage builds on Alpine (musl), the real risk
  was a glibc-linked binary failing to exec. Verified by running
  `npm ci && npm run build` inside the image on **both** `linux/amd64` and
  `linux/arm64`: the build completes and emits the expected bundle. The binaries
  are statically linked and musl-safe.

  That verification was performed against `node:22-alpine`, which the UI stage
  used at the time; it is now `node:24-alpine`. The musl question is a property
  of Alpine rather than of the Node major, so the conclusion carries — but the
  check has not been repeated since the bump.

If a future TypeScript bump is being considered, repeat that Alpine check
against whatever the Dockerfile currently uses. A clean typecheck on macOS
proves nothing about the container build.

### `@types/node` tracks the Node the UI is actually built on

Currently `^24.13.3`, matching `FROM node:24-alpine` in the Dockerfile's UI
stage.

The rule, which is what matters rather than the number: type definitions should
describe the runtime you actually have. Declaring Node 26 types while building
on Node 24 means the compiler accepts APIs that do not exist at runtime — false
confidence in exchange for nothing, since the only Node surface in the project
is `node:path` and `__dirname` in `vite.config.ts`. Declaring older types than
the runtime is the harmless direction, and was the state here while the image
was on Node 22.

`^24.13.3` satisfies Vite 8's peer range (`^20.19.0 || >=22.12.0`).

**These two values are supposed to agree.** If you move the Dockerfile's Node,
move this pin with it — and if you find them disagreeing, the Dockerfile is the
source of truth, because it is what actually runs the build.

### react-router — resolved by the v8 migration

`react-router-dom` is gone. The UI is on **`react-router@^8.3.0`**, and the
advisory that used to sit against this dependency is fixed rather than accepted.

- **Advisory:** [GHSA-qwww-vcr4-c8h2](https://github.com/advisories/GHSA-qwww-vcr4-c8h2)
  — "React Router: RSC Mode CSRF Bypass Allows Action Execution Before 400
  Response". Affected range `>=7.12.0 <8.3.0`, fixed in 8.3.0.
- For most of this project's life there was **no fixed version to move to**: the
  fix landed in 8.3.0, `react-router-dom` never published a v8, and v8 exists
  only under the `react-router` package name. So the position was documented
  non-exploitability — the vulnerable surface is RSC mode with server actions,
  and polyemesis is a static SPA embedded in a Go binary with no RSC runtime, no
  server actions and no Node server rendering it.
- The v7→v8 migration has since happened, which moves the answer from *not
  exploitable here* to *not present*. That is the better place to be, and it is
  why the migration was worth doing even though it was never a security
  emergency.

The imports remain the same eleven component and hook APIs — `BrowserRouter`,
`Routes`, `Route`, `Outlet`, `Navigate`, `NavLink`, `Link`, `useParams`,
`useLocation`, `useNavigate`, `useSearchParams`. There is still no
`createBrowserRouter`, no `RouterProvider`, no `loader:`, no `action:`, no
fetchers and no RSC entry point, so the surface that advisory covers is one this
app would have to grow before it could be reached.

Worth keeping in mind for the next advisory: "we do not use the affected code
path" is a defensible position, but it expires the moment somebody adds a data
router. Preferring the fixed version is less to remember.

## Verifying a dependency change

A bump that breaks the build is worse than a dependency one patch behind.
Anything touching `go.mod`, `go.sum`, `ui/package.json` or
`ui/package-lock.json` must clear all of this:

```sh
go build ./...
go vet ./...
go test ./...
go test -race ./...

# explicitly, for anything touching modernc.org/sqlite or modernc.org/libc
go test -count=1 ./internal/db/ ./internal/engine/

# all four shipping targets
for t in windows/amd64 linux/arm64 linux/amd64 darwin/arm64; do
  GOOS=${t%/*} GOARCH=${t#*/} go build ./... || echo "FAIL $t"
done

# UI
cd ui && npx tsc -b --noEmit && npx oxlint
```

Plus the three acceptance scripts, which exercise the real binary against a
real FFmpeg: `scripts/acceptance.sh` (13/13),
`scripts/acceptance-renditions.sh` (29/29), `scripts/acceptance-tls.sh` (35/35).

For a frontend toolchain change specifically, also reproduce the container
build, because the host toolchain is not the one that ships:

```sh
docker run --rm --platform linux/amd64 -v "$PWD/ui:/src:ro" node:24-alpine \
  sh -c 'mkdir -p /work/ui /work/internal/web && cp -r /src/. /work/ui/ \
         && cd /work/ui && npm ci && npm run build'
```
