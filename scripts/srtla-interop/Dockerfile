# SRTLA interop image: BELABOX's reference sender (srtla_send) and an ffmpeg
# with libsrt to feed it. The receiver under test (scripts/srtla-interop/rec)
# is cross-compiled on the host and copied into the container by run.sh, so
# this image never sees the polyemesis source tree.
#
# srtla is AGPL-3.0. It is cloned and built here, inside the image only, and
# is never vendored into the repository; nothing from it is copied into
# polyemesis. Pinned so a run is reproducible.
FROM debian:bookworm AS srtla
ARG SRTLA_COMMIT=37862da3d0c13b46956efd3f88877053293d97d6
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates build-essential cmake pkg-config \
 && rm -rf /var/lib/apt/lists/*
RUN git clone https://github.com/BELABOX/srtla /src \
 && cd /src && git checkout --quiet "$SRTLA_COMMIT" \
 && git submodule update --init --recursive
# Branches on which build files exist, nothing more: run.sh only needs the
# srtla_send binary out of it. On failure only the last lines are shown.
RUN cd /src \
 && if [ -f CMakeLists.txt ]; then \
      { cmake -S . -B build -DCMAKE_BUILD_TYPE=Release && cmake --build build -j"$(nproc)"; } >/tmp/build.log 2>&1 \
        || { tail -5 /tmp/build.log; exit 1; }; \
    else \
      make -j"$(nproc)" >/tmp/build.log 2>&1 || { tail -5 /tmp/build.log; exit 1; }; \
    fi \
 && install -m 0755 "$(find /src -type f -name srtla_send -perm -u+x | head -1)" /usr/local/bin/srtla_send

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg \
 && rm -rf /var/lib/apt/lists/* \
 && ffmpeg -hide_banner -protocols 2>/dev/null | grep -q '^ *srt$'
COPY --from=srtla /usr/local/bin/srtla_send /usr/local/bin/srtla_send
COPY interop.sh /interop.sh
RUN chmod 0755 /interop.sh
ENTRYPOINT ["/interop.sh"]
