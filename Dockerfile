# polyemesis — optional container image.
#
# Docker is never required: `make build` produces a self-contained binary that
# runs on any host with FFmpeg installed. This image exists for people who
# prefer containers, and it bundles FFmpeg so there is nothing else to install.
#
# Alpine rather than distroless, deliberately: polyemesis shells out to ffmpeg
# and ffprobe, so the image needs a real FFmpeg with SRT support. Alpine's
# ffmpeg package is built with libsrt; distroless has no package manager and
# would mean vendoring an FFmpeg build by hand.
#
# See docs/INSTALL.md for the per-platform install guide this image is one of.
#
# This image has NO hardware encoding. Alpine's ffmpeg carries the VA-API and
# NVENC wrappers, but the vendor runtimes they dlopen() at startup are not here
# and are not installable from the host by passing a device in — so every
# rendition on this image software-encodes, which will not hold 4K60. For a GPU,
# build Dockerfile.cuda (NVIDIA) or Dockerfile.vaapi (Intel/AMD) instead, and
# read docs/HARDWARE.md first: the passthrough flags are the part that goes
# wrong.

# ---------- stage 1: build the web UI ----------
# Vite 8 requires Node ^20.19 || >=22.12; the 24 line satisfies that and keeps
# satisfying it as it moves. Keep this in step with ui/package.json's
# @types/node major: typing against a newer Node than the one that runs the
# build is how you get code that compiles here and throws at runtime.
#
# --platform=$BUILDPLATFORM pins this stage to the machine doing the building,
# never the target architecture. The output is JavaScript and CSS, which are
# architecture-independent, so emulating this stage under QEMU for an arm64
# target would burn minutes to produce identical bytes.
FROM --platform=$BUILDPLATFORM node:24-alpine AS ui
WORKDIR /src/ui
# Copy manifests first so a dependency-only change reuses the install layer.
COPY ui/package.json ui/package-lock.json* ./
RUN npm ci --ignore-scripts
COPY ui/ ./
# The Vite config writes to ../internal/web/dist, so that path must exist.
RUN mkdir -p /src/internal/web && npm run build

# ---------- stage 2: build the Go binary ----------
# This tag must be >= the `go` directive in go.mod. The official golang images
# set GOTOOLCHAIN=local, so a too-old image does not quietly download a newer
# toolchain the way a developer's machine does — it fails the build outright
# with "go.mod requires go >= …". Bump this line whenever go.mod's floor moves.
# Also pinned to $BUILDPLATFORM, and then cross-compiled with GOARCH below.
# The alternative — running the Go toolchain under QEMU for the target arch —
# is both far slower and unreliable: `go mod download` fetching from
# proxy.golang.org through emulation times out its TLS handshake often enough
# to fail a multi-arch build outright, which is exactly what it did here.
#
# Cross-compiling is safe because CGO_ENABLED=0 already: there is no C
# toolchain in the picture, so a native Go compiler targeting another GOARCH
# produces the same static binary emulation would have.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Bring in the compiled UI so go:embed picks it up rather than the placeholder.
COPY --from=ui /src/internal/web/dist ./internal/web/dist
ARG VERSION=docker
# Supplied automatically by buildx, and defaulted so a plain `docker build`
# with no buildx still works.
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/polyemesis ./cmd/polyemesis

# ---------- stage 3: runtime ----------
#
# Alpine 3.24, which ships FFmpeg 8.1.2.
#
# This image sat on 3.22/FFmpeg 6.1.x for a while on the theory that 6.1 was
# "the series this project is exercised against". That had it backwards. The
# six host acceptance suites — 156 checks covering multitrack routing, the
# loudness analyser, ducking, denoise, delay, the rendition ladder and the
# encoder probes — run against whatever FFmpeg is on the developer machine,
# and that has been 8.1.2. The 6.1.x container was the LESS tested
# configuration, not the more tested one, and pinning to it meant shipping
# something no suite had ever exercised end to end.
#
# The tag is deliberately left floating at the patch level (3.24, not 3.24.1) so
# rebuilds pick up musl/openssl security fixes. If you need a bit-exact rebuild
# instead, pin the digest as well — verified 2026-07-27, amd64 and arm64:
#   FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
FROM alpine:3.24

ARG VERSION=dev

# OCI labels, on the image itself rather than only in the workflow.
#
# The workflow's metadata-action injects these for the default image, but a
# label baked into the Dockerfile survives a `docker build` run by hand -- which
# is how the GPU images are built by anyone with the hardware to test them, and
# how this image is built by anyone auditing it. A registry listing with no
# description, source or licence is one people scroll past.
LABEL org.opencontainers.image.title="polyemesis" \
      org.opencontainers.image.description="Self-hosted restreaming with per-destination audio routing. One ingest, many platforms, a different audio mix for each." \
      org.opencontainers.image.url="https://github.com/rainmanjam/polyemesis" \
      org.opencontainers.image.source="https://github.com/rainmanjam/polyemesis" \
      org.opencontainers.image.documentation="https://github.com/rainmanjam/polyemesis/tree/main/docs" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.vendor="polyemesis" \
      org.opencontainers.image.version="${VERSION}"


# FFMPEG_VERSION is pinned, not floating, because `apk add ffmpeg` resolves to
# whatever the branch happens to hold on the day you build. That makes an image
# rebuilt six months from now a different product with the same tag — and the
# thing that drifts is the transcoder, where a behaviour change shows up as a
# broken stream rather than a build error.
#
# Pinned means a rebuild FAILS LOUDLY ("unable to select packages") once Alpine
# republishes ffmpeg at a new -r revision, since the branch index only carries
# the current one. That failure is the feature: it is the prompt to bump this
# on purpose. To bump:
#
#   1. docker run --rm alpine:3.24 sh -c 'apk update >/dev/null && apk list ffmpeg'
#      (or https://pkgs.alpinelinux.org/packages?name=ffmpeg&branch=v3.24)
#   2. Set FFMPEG_VERSION to what it prints, matching on BOTH amd64 and arm64.
#   3. Confirm the new build still has SRT before shipping it:
#      docker run --rm <image> ffmpeg -protocols | tr ' ' '\n' | grep -x srt
#
# 8.1.2-r0 verified on linux/amd64 and linux/arm64: srt + rtmp protocols,
# libx264 and native aac encoders all present.
#
# The trailing grep is not decoration: it fails the build rather than shipping
# an image whose multitrack SRT ingest cannot work. `grep srt` would be no check
# at all — every build lists `srtp`, which is a different protocol.
ARG FFMPEG_VERSION=8.1.2-r0
RUN apk add --no-cache "ffmpeg=${FFMPEG_VERSION}" ca-certificates tzdata \
 && adduser -D -u 10001 polyemesis \
 && ffmpeg -hide_banner -protocols | tr ' ' '\n' | grep -qx srt
COPY --from=build /out/polyemesis /usr/local/bin/polyemesis

# The data directory holds the database, the secret key and recordings.
# Mount a volume here or everything is lost when the container is replaced.
# 0750, not the default 0755: /data holds polyemesis.db, which carries every
# destination stream key in plaintext. See issue #297.
RUN mkdir -p /data && chown polyemesis:polyemesis /data && chmod 0750 /data
VOLUME ["/data"]
USER polyemesis
WORKDIR /data

EXPOSE 8080/tcp
# SRT is UDP. Forgetting the /udp suffix is the classic reason an SRT ingest
# silently never receives anything.
EXPOSE 6000/udp
EXPOSE 1935/tcp
# Only needed for `tls.mode: acme` (and for `auto` when it resolves to acme).
# Let's Encrypt validates over HTTP-01, which means it must reach
# http://<hostname>/.well-known/acme-challenge/… on port 80 from the public
# internet — so the port has to be published on the host too, not merely
# declared here. Every other TLS mode leaves this unused: polyemesis still binds
# it for the HTTP->HTTPS redirect when it terminates TLS, but a failure to bind
# is a warning, not a fatal error.
EXPOSE 80/tcp

# The health endpoint is unauthenticated precisely so this works. It speaks
# plain HTTP: if you turn on TLS *inside* the container rather than at a proxy
# in front of it, change this to `wget --no-check-certificate -qO- https://…`
# or the container will be reported unhealthy while serving perfectly well.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s \
  CMD wget -qO- http://127.0.0.1:8080/api/v1/health || exit 1

ENTRYPOINT ["polyemesis"]
CMD ["-addr", ":8080", "-data", "/data"]
