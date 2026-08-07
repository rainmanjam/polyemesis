# OBS Studio, headless, as a REAL publisher for the E-RTMP multitrack path.
#
# Everything else that tests this path uses FFmpeg as the publisher. FFmpeg is
# not OBS: what stays unverified is OBS's RTMP connect/handshake, the onMetaData
# it sends (including audioTrackIdInfoMap), and which trackIds it assigns. That
# is the gap docs/notes/enhanced-rtmp-multitrack.md calls "a confirmation, not
# an investigation", and it is the last thing standing between this and a
# claim we can make in the docs.
#
# HEADLESS IS THE WHOLE DIFFICULTY. OBS is a GUI application that requires an
# OpenGL context; it has no batch mode. So:
#   - Xvfb supplies a display that is not attached to anything
#   - Mesa's llvmpipe supplies OpenGL in software, since a container has no GPU
#   - --startstreaming makes it begin without anyone clicking Start Streaming
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

# BACKPORTS, not universe and not the PPA.
#
# Ubuntu 24.04 universe ships OBS 30.0.2, and multitrack audio over Enhanced
# RTMP is the thing under test — docs/FAQ.md puts it at 30.2+, so the stock
# build cannot answer the question this container exists to ask.
#
# The obsproject PPA was the first attempt and is wrong here: it publishes
# amd64 only, so the image built but silently fell back to universe's 30.0.2 on
# arm64. noble-backports carries 30.2.3 for both architectures, which matters
# because this runs on an Apple Silicon laptop and on amd64 CI.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
 && apt-get install -y --no-install-recommends -t noble-backports \
      obs-studio \
      xvfb x11-utils \
      libgl1-mesa-dri libglx-mesa0 mesa-utils \
      pulseaudio pulseaudio-utils \
      ffmpeg \
      jq procps \
 && rm -rf /var/lib/apt/lists/*

# llvmpipe: no GPU in a container, and OBS will not start without a GL context.
ENV LIBGL_ALWAYS_SOFTWARE=1 \
    GALLIUM_DRIVER=llvmpipe \
    QT_QPA_PLATFORM=xcb \
    DISPLAY=:99

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
