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

Go toolchain: **1.26.5**.

## Direct Go dependencies

Ten, deliberately. Each one earns its place below.

| Module | Version | Used by |
| --- | --- | --- |
| `github.com/datarhei/gosrt` | v0.6.0 | `internal/srtserver` |
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
upstream — and `ffmpeg -listen 1` was already a complete answer for RTMP. gosrt
had no such alternative: FFmpeg's SRT support is a client and a
single-connection listener, and neither can demultiplex many publishers by
`streamid` on one port.

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

> There is currently no `LICENSE` file at the repo root. The reasoning above
> stands on its own, but the project's own licence should be stated explicitly
> before any public release.

## Indirect Go dependencies

All indirect modules come from exactly two roots: `gopsutil` and
`modernc.org/sqlite`. There are no other transitive sources, which is a
property worth preserving.

They are kept current on a plain `go get -u ./... && go mod tidy`, gated on the
full verification below. As of the last review all indirect modules resolve to
their latest published versions.

A handful of modules appear in `go.sum` but not in `go.mod` — `google/pprof`,
`golang.org/x/mod`, `golang.org/x/tools`, `golang.org/x/xerrors`,
`check.v1`, `modernc.org/cc/v4`, `modernc.org/gc/v3`. These are build- and
test-time dependencies of modernc's own code generation. They are not in the
binary. `go list -u -m all` will report some of them as outdated forever; that
is expected and is not a finding.

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
  dependency. Since the Docker UI stage builds on `node:22-alpine` (musl), the
  real risk was a glibc-linked binary failing to exec. Verified by running
  `npm ci && npm run build` inside `node:22-alpine` on **both** `linux/amd64`
  and `linux/arm64`: the build completes and emits the expected bundle. The
  binaries are statically linked and musl-safe.

If a future TypeScript bump is being considered, repeat that Alpine check. A
clean typecheck on macOS proves nothing about the container build.

### `@types/node` is pinned to the 22 line, on purpose

`^24.13.3` → `^22.20.1`. This is deliberately a *downgrade*, and `npm outdated`
will complain about it forever.

The reason: the UI is built on **Node 22** (`FROM node:22-alpine` in the
Dockerfile). Type definitions should describe the runtime you actually have.
Declaring Node 24 or 26 types while building on Node 22 means the compiler will
happily accept APIs that do not exist at runtime — false confidence, in
exchange for nothing, since the only Node surface in the project is
`node:path` and `__dirname` in `vite.config.ts`.

`^22.20.1` satisfies Vite 8's peer range (`^20.19.0 || >=22.12.0`).

**If the Dockerfile moves to a newer Node, move this pin with it.** Those two
values are supposed to agree.

### react-router — the `npm audit` advisory you should NOT "fix"

`npm audit` reports **2 high severity** advisories against `react-router` /
`react-router-dom` and will keep doing so. This has been assessed twice and the
conclusion is **take no action**. Do not run `npm audit fix --force`.

- **Advisory:** [GHSA-qwww-vcr4-c8h2](https://github.com/advisories/GHSA-qwww-vcr4-c8h2)
  — "React Router: RSC Mode CSRF Bypass Allows Action Execution Before 400
  Response". Affected range `>=7.12.0 <8.3.0`. We are on 7.18.1.
- **The vulnerable surface is RSC mode with server actions.** polyemesis is a
  static SPA embedded in a Go binary. There is no React Server Components
  runtime, no server actions, and no Node server rendering it — the Go binary
  serves prebuilt files.
- **We do not use the affected APIs.** The complete set of react-router imports
  across `ui/src` is: `BrowserRouter`, `Routes`, `Route`, `Outlet`, `Navigate`,
  `NavLink`, `Link`, `useParams`, `useLocation`, `useNavigate`,
  `useSearchParams` — eleven component/hook APIs across five files. There is no
  `createBrowserRouter`, no `RouterProvider`, no `loader:`, no `action:`, no
  fetchers, and no RSC entry points anywhere in the source.
- **There is no fixed version to move to on this line.** The advisory is fixed
  in 8.3.0, but `react-router-dom`'s latest published release is 7.18.1 — v8
  moved to the `react-router` package. Reaching a non-vulnerable version would
  mean a v7→v8 framework migration, which is not a security action.
- **npm's suggested fix is a downgrade to `react-router-dom@7.11.0`**, flagged
  `isSemVerMajor`. Taking it would discard seven minor versions of real bug
  fixes in order to mitigate a code path we cannot reach.

Re-verify rather than trust this note if the app ever gains a data router,
server-side rendering, or an RSC build. Until then, the audit output is noise.

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
docker run --rm --platform linux/amd64 -v "$PWD/ui:/src:ro" node:22-alpine \
  sh -c 'mkdir -p /work/ui /work/internal/web && cp -r /src/. /work/ui/ \
         && cd /work/ui && npm ci && npm run build'
```
