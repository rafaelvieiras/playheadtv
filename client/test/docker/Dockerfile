# syntax=docker/dockerfile:1
# Image for running the client GUI integration suites headless
# (client/tool/run_gui_suites.sh). Build context: this directory.
#   docker build -t playheadtv-gui:local \
#     --build-arg GO_VERSION="$(awk '/^go /{print $2}' backend/go.mod)" client/test/docker
# FLUTTER_VERSION must match the version pinned in .github/workflows/*.yml.
FROM ubuntu:24.04
ARG FLUTTER_VERSION=3.47.5
ARG GO_VERSION=1.26.4
ENV DEBIAN_FRONTEND=noninteractive

# Linux build dependencies are the ones of the client-release workflow; the rest
# is the headless desktop (Xvfb, window manager, D-Bus), Mesa software GL
# (llvmpipe), ffmpeg (video fixture and screen recording) and optional PulseAudio.
RUN apt-get update && apt-get install -y --no-install-recommends \
      clang cmake ninja-build pkg-config libgtk-3-dev liblzma-dev \
      libstdc++-14-dev libc++-dev libc++abi-dev curl unzip xz-utils \
      ca-certificates git zip file procps psmisc \
      xvfb xauth x11-utils openbox dbus dbus-x11 \
      libgl1 libgl1-mesa-dri libegl1 libegl-mesa0 libgles2 libglx-mesa0 \
      libvulkan1 mesa-vulkan-drivers mesa-utils \
      pulseaudio libpulse0 alsa-utils ffmpeg libunwind8 libsqlite3-0 \
 && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -C /usr/local -xz
RUN curl -fsSL "https://storage.googleapis.com/flutter_infra_release/releases/stable/linux/flutter_linux_${FLUTTER_VERSION}-stable.tar.xz" | tar -C /opt -xJ

ENV PATH=/opt/flutter/bin:/usr/local/go/bin:$PATH \
    FLUTTER_SUPPRESS_ANALYTICS=true CI=true
RUN git config --global --add safe.directory '*' \
 && flutter config --no-analytics --enable-linux-desktop \
 && flutter precache --linux

COPY entry.sh /usr/local/bin/gui-entry.sh
RUN chmod 0755 /usr/local/bin/gui-entry.sh
ENTRYPOINT ["/usr/local/bin/gui-entry.sh"]
