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

# ---------- stage 1: build the web UI ----------
FROM node:22-alpine AS ui
WORKDIR /src/ui
# Copy manifests first so a dependency-only change reuses the install layer.
COPY ui/package.json ui/package-lock.json* ./
RUN npm ci
COPY ui/ ./
# The Vite config writes to ../internal/web/dist, so that path must exist.
RUN mkdir -p /src/internal/web && npm run build

# ---------- stage 2: build the Go binary ----------
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Bring in the compiled UI so go:embed picks it up rather than the placeholder.
COPY --from=ui /src/internal/web/dist ./internal/web/dist
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/polyemesis ./cmd/polyemesis

# ---------- stage 3: runtime ----------
FROM alpine:3.20
RUN apk add --no-cache ffmpeg ca-certificates tzdata \
 && adduser -D -u 10001 polyemesis
COPY --from=build /out/polyemesis /usr/local/bin/polyemesis

# The data directory holds the database, the secret key and recordings.
# Mount a volume here or everything is lost when the container is replaced.
RUN mkdir -p /data && chown polyemesis:polyemesis /data
VOLUME ["/data"]
USER polyemesis
WORKDIR /data

EXPOSE 8080/tcp
# SRT is UDP. Forgetting the /udp suffix is the classic reason an SRT ingest
# silently never receives anything.
EXPOSE 6000/udp
EXPOSE 1935/tcp

# The health endpoint is unauthenticated precisely so this works.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s \
  CMD wget -qO- http://127.0.0.1:8080/api/v1/health || exit 1

ENTRYPOINT ["polyemesis"]
CMD ["-addr", ":8080", "-data", "/data"]
