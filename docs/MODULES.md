# Modules and projects

Everything polyemesis depends on, at the versions currently pinned.

[DEPENDENCIES.md](DEPENDENCIES.md) explains *why* the significant ones were
chosen and what was rejected. This file is the inventory: what, which version,
which licence.

Licences were read from the module cache and the lockfile rather than recalled,
so they reflect what is actually vendored.

---

## Go — direct dependencies

Ten, and the count is deliberate. Every one is either doing something genuinely
hard or is the Go project's own code.

| Module | Version | Licence | What it does |
|---|---|---|---|
| `modernc.org/sqlite` | v1.54.0 | BSD-3-Clause | SQLite, transpiled to pure Go. The reason the binary needs no cgo |
| `github.com/go-chi/chi/v5` | v5.3.1 | MIT | HTTP router. `net/http`-shaped, no framework |
| `github.com/gorilla/websocket` | v1.5.3 | BSD-2-Clause | WebSocket for live status, levels, logs, chat |
| `github.com/golang-jwt/jwt/v5` | v5.3.1 | MIT | Session token signing |
| `golang.org/x/crypto` | v0.54.0 | BSD-3-Clause | bcrypt, NaCl secretbox, ACME |
| `golang.org/x/sys` | v0.47.0 | BSD-3-Clause | Process groups, disk stats, Windows job objects |
| `github.com/shirou/gopsutil/v4` | v4.26.6 | BSD-3-Clause | Host CPU, memory and disk for the monitoring page |
| `github.com/datarhei/gosrt` | v0.11.0 | MIT | Pure-Go SRT. Powers one-port token-addressed ingest |
| `github.com/eclipse/paho.golang` | v0.23.0 | EPL-2.0 (dual EDL-1.0) | MQTT 5 client for retained telemetry |
| `gopkg.in/yaml.v3` | v3.0.1 | Apache-2.0 (dual MIT) | Reads `config.yaml` |

## Go — indirect dependencies

Pulled in by the above. Those marked **linked** end up in the shipped binary;
the rest are build-time or test-only for their parent.

| Module | Version | Licence | Pulled in by | Linked |
|---|---|---|---|---|
| `modernc.org/libc` | v1.74.4 | BSD-3-Clause | sqlite | ✅ |
| `modernc.org/mathutil` | v1.7.1 | BSD-3-Clause | sqlite | ✅ |
| `modernc.org/memory` | v1.11.0 | BSD-3-Clause | sqlite | ✅ |
| `github.com/remyoudompheng/bigfft` | 20230129 | BSD-3-Clause | mathutil | ✅ |
| `github.com/dustin/go-humanize` | v1.0.1 | MIT | sqlite | ✅ |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause | sqlite | ✅ |
| `github.com/ncruces/go-strftime` | v1.0.0 | MIT | sqlite | ✅ |
| `github.com/mattn/go-isatty` | v0.0.24 | MIT | libc | ✅ |
| `github.com/benburkert/openpgp` | 20160410 | see note | gosrt (AES key-wrap) | ✅ |
| `github.com/ebitengine/purego` | v0.10.2 | Apache-2.0 | gopsutil | ✅ |
| `github.com/tklauser/go-sysconf` | v0.4.0 | BSD-3-Clause | gopsutil | ✅ |
| `golang.org/x/net` | v0.57.0 | BSD-3-Clause | crypto | ✅ |
| `golang.org/x/text` | v0.40.0 | BSD-3-Clause | net | ✅ |
| `github.com/go-ole/go-ole` | v1.3.0 | MIT | gopsutil (Windows) | platform |
| `github.com/yusufpapurcu/wmi` | v1.2.4 | MIT | gopsutil (Windows) | platform |
| `github.com/lufia/plan9stats` | 20260627 | BSD-3-Clause | gopsutil (Plan 9) | platform |
| `github.com/power-devops/perfstat` | 20240221 | MIT | gopsutil (AIX) | platform |
| `github.com/tklauser/numcpus` | v0.12.0 | Apache-2.0 | go-sysconf | platform |

Also in the module graph but **not** in the binary — build tooling for
`modernc.org/sqlite`'s C-to-Go transpilation, and test dependencies of upstream
modules: `modernc.org/{cc,ccgo,gc,goabi0,opt,sortutil,strutil,token,fileutil}`
(all BSD-3-Clause), `golang.org/x/{mod,sync,tools}` (BSD-3-Clause),
`github.com/google/{go-cmp,pprof}`, `github.com/stretchr/testify`,
`github.com/davecgh/go-spew`, `github.com/pmezard/go-difflib`,
`github.com/hashicorp/golang-lru/v2` (MPL-2.0), `gopkg.in/check.v1`.

### One licence note worth knowing

**`github.com/benburkert/openpgp` ships in the binary and has no LICENSE file.**

It is a 2016 fork of `golang.org/x/crypto/openpgp`, reached through
`datarhei/gosrt` → `gosrt/crypto` → `openpgp/aes/keywrap`, which SRT uses for
key wrapping. Every source file carries the Go Authors' header —

```
// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
```

— but the fork did not carry the LICENSE file across. So it is BSD-3-Clause by
provenance and by the headers, with the licence text itself absent from the
module. Recorded here rather than left for someone to discover during a licence
audit. It is compatible with polyemesis's MIT licence either way.

---

## Frontend — runtime

Ships in the bundle, which is embedded in the binary.

| Package | Version | Licence | What it does |
|---|---|---|---|
| `react` / `react-dom` | 19.2.8 | MIT | The UI |
| `react-router` | 8.3.0 | MIT | Routing |
| `recharts` | 3.10.1 | MIT | Bitrate and loudness charts |
| `hls.js` | 1.6.16 | Apache-2.0 | HLS playback in the built-in player |
| `lucide-react` | 1.27.0 | ISC | Icons |
| `sonner` | 2.0.7 | MIT | Toasts |
| `clsx` | 2.1.1 | MIT | Conditional class names |
| `tailwind-merge` | 3.6.0 | MIT | Resolves conflicting Tailwind classes |
| `class-variance-authority` | 0.7.1 | Apache-2.0 | Typed component variants |

### Radix UI primitives

All MIT. Unstyled, accessible primitives — this is the shadcn/ui foundation, so
the components are in the repo and Radix supplies behaviour and accessibility
rather than appearance.

| Package | Version | | Package | Version |
|---|---|---|---|---|
| `react-accordion` | 1.2.20 | | `react-select` | 2.3.7 |
| `react-checkbox` | 1.3.11 | | `react-separator` | 1.1.15 |
| `react-dialog` | 1.1.23 | | `react-slider` | 1.4.7 |
| `react-dropdown-menu` | 2.1.24 | | `react-slot` | 1.3.3 |
| `react-label` | 2.1.15 | | `react-switch` | 1.3.7 |
| `react-progress` | 1.1.16 | | `react-tabs` | 1.1.21 |
| `react-toast` | 1.2.23 | | `react-tooltip` | 1.2.16 |

## Frontend — build and development

Not shipped. Present only to produce the bundle.

| Package | Version | Licence | What it does |
|---|---|---|---|
| `vite` | 8.1.5 | MIT | Bundler and dev server |
| `typescript` | 7.0.2 | Apache-2.0 | Types |
| `tailwindcss` | 4.3.3 | MIT | Styling |
| `@tailwindcss/vite` | 4.3.3 | MIT | Tailwind 4's Vite integration |
| `@vitejs/plugin-react` | 6.0.4 | MIT | React fast refresh |
| `oxlint` | 1.76.0 | MIT | Linter (Rust; replaced ESLint) |
| `@playwright/test` | 1.62.0 | Apache-2.0 | Browser end-to-end suite |
| `tw-animate-css` | 1.4.0 | MIT | Animation utilities |
| `@types/node` | 24.13.3 | MIT | Node type definitions |
| `@types/react` | 19.2.17 | MIT | React type definitions |
| `@types/react-dom` | 19.2.3 | MIT | React DOM type definitions |

---

## External runtime dependencies

Not linked, not vendored — executed as subprocesses. This is a deliberate
architectural choice, not an omission: see
[DEPENDENCIES.md](DEPENDENCIES.md#ffmpeg-a-subprocess-not-a-library).

| Tool | Requirement | Licence | Why |
|---|---|---|---|
| **FFmpeg** | 6.0+ required, 8.1.2 recommended, **libsrt needed for multitrack** | LGPL-2.1+/GPL-2+ by build | Every ingest, route, encode and recording |
| **ffprobe** | ships with FFmpeg | same | Stream probing, keyframe indexes, verification |
| **whisper.cpp** | optional (`whisper`, `whisper-cli`, `whisper-cpp`) | MIT | Transcription. Absent means the feature is off, not broken |

Because FFmpeg is a subprocess rather than a linked library, its GPL/LGPL terms
do not reach polyemesis's own MIT-licensed code.

## Toolchain

| | Version | Notes |
|---|---|---|
| **Go** | 1.26.5 (floor in `go.mod`) | `CGO_ENABLED=0` everywhere |
| **Node** | 24 in the images; 20.19+ / 22.12+ is Vite 8's floor | Build-time only |

## Container base images

| Image | Base | FFmpeg | Platforms |
|---|---|---|---|
| `Dockerfile` (default) | `alpine:3.24` | pinned `8.1.2-r0` | linux/amd64, linux/arm64 |
| `Dockerfile.cuda` | `nvidia/cuda:12.6.3-base-ubuntu24.04` | pinned | linux/amd64 |
| `Dockerfile.vaapi` | `ubuntu:24.04` | pinned | linux/amd64 |
| build stages | `golang:1.26-alpine`, `node:24-alpine` | — | cross-compiled from `$BUILDPLATFORM` |

Runtime packages: `ffmpeg` (pinned exactly), `ca-certificates`, `tzdata`; plus
`intel-media-va-driver`, `i965-va-driver` and `mesa-va-drivers` in the VA-API
image.

The FFmpeg version is **pinned rather than floating** on purpose. `apk add
ffmpeg` resolves to whatever the branch holds that day, which would make an
image rebuilt in six months a different product under the same tag. Each image
also verifies SRT is present at build time — with `grep -qx srt`, because a
plain `grep srt` matches `srtp` and would pass on every build.

## CI and release actions

| Action | Version |
|---|---|
| `actions/checkout` | v7 |
| `actions/setup-go` | v7 |
| `actions/setup-node` | v7 |
| `docker/setup-qemu-action` | v4 |
| `docker/setup-buildx-action` | v4 |
| `docker/login-action` | v4 |
| `docker/metadata-action` | v6 |
| `docker/build-push-action` | v7 |
| `softprops/action-gh-release` | v3 |

---

## Summary

| Category | Count |
|---|---|
| Go direct | 9 |
| Go indirect, linked into the binary | 13 |
| Go indirect, platform-specific | 5 |
| Go, build/test tooling only | 17 |
| Frontend runtime (incl. 14 Radix) | 23 |
| Frontend build/dev | 11 |
| External tools | 3 (2 required, 1 optional) |
| GitHub Actions | 9 |

Licences across everything shipped: MIT, BSD-2-Clause, BSD-3-Clause, ISC and
Apache-2.0 — all permissive and all compatible with polyemesis's MIT licence.
The single MPL-2.0 module (`hashicorp/golang-lru/v2`) is a test dependency of an
upstream module and is **not** linked into the binary.

## Keeping this current

```sh
go list -m -f '{{.Path}} {{.Version}}' all      # the full module graph
go version -m ./polyemesis                       # what is ACTUALLY in the binary
npm --prefix ui ls --depth=0                     # frontend, resolved
```

`go version -m` is the one that matters for a licence question: the module graph
includes build tooling and upstream test dependencies that never reach a user.
