#!/bin/sh
# Passive display-presence signal logger (spike for docs/DISPLAY_PRESENCE.md).
#
# Polls, once per interval, every signal that might tell that a TV or monitor
# stopped showing this machine's output, and appends one timestamped line per
# poll to a log. It only READS: it never changes a setting, never restarts
# anything and writes only inside its output directory.
#
# Usage:
#   DISPLAY=:0 [XAUTHORITY=<path>] sh display_signal_logger.sh [seconds] [outdir]
#
#   seconds  how long to run (default 120; 0 = until interrupted)
#   outdir   where to write (default /tmp/display_signal_log)
#   INTERVAL environment variable: poll period in seconds (default 1)
#
# Files written in outdir:
#   signals.log  one line per poll: wall clock, DRM, X11 and CEC state
#   changes.log  only the polls whose state differs from the previous one
#   udev.log     raw `udevadm monitor` drm events, if udevadm is available
#
# Works with POSIX sh (busybox ash included). Each tool is optional; a signal
# whose tool is missing is logged as "n/a".

SECONDS_TOTAL=${1:-120}
OUT=${2:-/tmp/display_signal_log}
INTERVAL=${INTERVAL:-1}

mkdir -p "$OUT" || exit 1
: >> "$OUT/signals.log"
: >> "$OUT/changes.log"

UDEV_PID=
if command -v udevadm >/dev/null 2>&1; then
  udevadm monitor --kernel --udev --subsystem-match=drm --property \
    >> "$OUT/udev.log" 2>&1 &
  UDEV_PID=$!
fi
cleanup() { [ -n "$UDEV_PID" ] && kill "$UDEV_PID" 2>/dev/null; exit 0; }
trap cleanup INT TERM EXIT

now() { date '+%Y-%m-%dT%H:%M:%S%z'; }

drm_state() {
  out=
  for d in /sys/class/drm/card*-*; do
    [ -e "$d/status" ] || continue
    n=${d##*/}; n=${n#card*-}
    st=$(cat "$d/status" 2>/dev/null)
    en=$(cat "$d/enabled" 2>/dev/null)
    dp=$(cat "$d/dpms" 2>/dev/null)
    ed=$(wc -c < "$d/edid" 2>/dev/null | tr -d ' ')
    out="$out $n=$st/$en/dpms:$dp/edid:${ed:-0}"
  done
  echo "${out# }"
}

x11_state() {
  if ! command -v xrandr >/dev/null 2>&1 || [ -z "$DISPLAY" ]; then
    echo "n/a"; return
  fi
  # --current reports the server's cached view (what an RRNotify client sees)
  # without forcing a hardware re-probe.
  xrandr --current 2>/dev/null | awk '
    / connected| disconnected/ {
      geom = ($3 == "primary") ? $4 : $3
      if ($2 == "disconnected") geom = ""
      printf "%s=%s%s ", $1, $2, (geom ~ /^[0-9]/ ? ":" geom : "")
    }'
}

dpms_state() {
  if ! command -v xset >/dev/null 2>&1 || [ -z "$DISPLAY" ]; then
    echo "n/a"; return
  fi
  xset q 2>/dev/null | awk '
    /DPMS is/ { en = $3 }
    /Monitor is/ { mon = $3 }
    END { printf "%s/%s", (en ? en : "?"), (mon ? mon : "?") }'
}

cec_state() {
  ls /dev/cec* 2>/dev/null | tr '\n' ',' | sed 's/,$//'
}

end=0
[ "$SECONDS_TOTAL" -gt 0 ] && end=$(( $(date +%s) + SECONDS_TOTAL ))
prev=
while :; do
  cur="drm[$(drm_state)] x11[$(x11_state)] dpms[$(dpms_state)] cec[$(cec_state)]"
  ts=$(now)
  echo "$ts $cur" >> "$OUT/signals.log"
  if [ "$cur" != "$prev" ]; then
    echo "$ts $cur" >> "$OUT/changes.log"
    prev=$cur
  fi
  [ "$end" -gt 0 ] && [ "$(date +%s)" -ge "$end" ] && break
  sleep "$INTERVAL"
done
