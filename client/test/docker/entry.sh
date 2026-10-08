#!/usr/bin/env bash
# Container entry point: a private headless desktop, then one GUI suite.
# Usage: gui-entry.sh <suite file relative to client/> [extra flutter test args]
# Environment (all optional):
#   OUT=/out          where logs and artifacts go
#   XVFB_SCREEN       default 1920x1080x24 (a smaller/8-bit screen breaks the app)
#   NO_WM=1           skip the window manager (openbox)
#   NO_DBUS=1         skip the private D-Bus session
#   WITH_AUDIO=1      start PulseAudio with a null sink (off by default: it made
#                     the suites less stable)
#   RECORD=1          record the screen to $OUT/screen.mp4 (ffmpeg x11grab)
#   HOST_UID/HOST_GID chown the artifacts to that user on exit
#   SRC=/src          read-only checkout to copy (backend/ and client/)
set -uo pipefail

SUITE="${1:?suite file required}"; shift
OUT=${OUT:-/out}; SRC=${SRC:-/src}
SCREEN=${XVFB_SCREEN:-1920x1080x24}
mkdir -p "$OUT"
# Artifacts are written as root; hand them back to the invoking user on exit.
if [ -n "${HOST_UID:-}" ]; then trap 'chown -R "$HOST_UID:${HOST_GID:-$HOST_UID}" "$OUT" 2>/dev/null' EXIT; fi

# Private HOME/XDG: shared_preferences and the offline cache are real files, so
# nothing may leak between containers or from the host.
WORK=$(mktemp -d /tmp/gui-work.XXXXXX)
export HOME=$WORK/h XDG_CONFIG_HOME=$WORK/h/.config \
       XDG_DATA_HOME=$WORK/h/.local/share XDG_CACHE_HOME=$WORK/h/.cache \
       XDG_RUNTIME_DIR=$WORK/run
mkdir -p "$HOME" "$XDG_RUNTIME_DIR" "$WORK/repo"; chmod 700 "$XDG_RUNTIME_DIR"
git config --global --add safe.directory '*'
tar -C "$SRC" -cf - backend client | tar -C "$WORK/repo" -xf -

# mdk probes CUDA first and segfaults on the raster thread under Xvfb on hosts
# with a proprietary NVIDIA driver; software GL keeps the run host independent.
export CUDA_VISIBLE_DEVICES=-1 LIBVA_DRIVER_NAME=dummy \
       LIBGL_ALWAYS_SOFTWARE=1 GALLIUM_DRIVER=llvmpipe LP_NUM_THREADS="${LP_NUM_THREADS:-2}"
export PUB_CACHE=${PUB_CACHE:-/cache/pub} GOCACHE=${GOCACHE:-/cache/go/build} \
       GOMODCACHE=${GOMODCACHE:-/cache/go/mod}
mkdir -p "$PUB_CACHE" "$GOCACHE" "$GOMODCACHE"

export DISPLAY=:99
Xvfb :99 -screen 0 "$SCREEN" -nolisten tcp >"$OUT/xvfb.log" 2>&1 &
for _ in $(seq 100); do xdpyinfo >/dev/null 2>&1 && break; sleep 0.1; done
[ "${NO_DBUS:-0}" = 1 ] || eval "$(dbus-launch --sh-syntax)"
[ "${NO_WM:-0}" = 1 ] || { openbox >"$OUT/wm.log" 2>&1 & }
if [ "${WITH_AUDIO:-0}" = 1 ]; then
  pulseaudio --start --exit-idle-time=-1 --load="module-null-sink sink_name=null" \
    --log-target=file:"$OUT/pulse.log" 2>/dev/null
fi
REC=
if [ "${RECORD:-0}" = 1 ]; then
  ffmpeg -loglevel error -y -f x11grab -framerate 5 -video_size "${SCREEN%x*}" \
    -i :99 -pix_fmt yuv420p "$OUT/screen.mp4" 2>"$OUT/record.log" &
  REC=$!
fi

cd "$WORK/repo/client" || exit 2
flutter pub get >"$OUT/pubget.log" 2>&1 || { echo "flutter pub get failed" | tee -a "$OUT/suite.log"; exit 3; }
start=$(date +%s)
flutter test "$SUITE" -d linux --file-reporter "json:$OUT/results.json" "$@" 2>&1 | tee "$OUT/suite.log"
rc=${PIPESTATUS[0]}
echo "EXIT=$rc DURATION=$(( $(date +%s) - start ))s" | tee -a "$OUT/suite.log"
if [ -n "$REC" ]; then kill -INT "$REC" 2>/dev/null; wait "$REC" 2>/dev/null; fi
exit "$rc"
