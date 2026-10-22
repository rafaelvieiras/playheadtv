#!/usr/bin/env bash
# Measures opening the guide and moving around it on an Android TV device over
# adb, using the app's opt-in frame trace (lib/services/perf_trace.dart). The app
# must be a build made with --dart-define=PLAYHEADTV_PERF_TRACE=true, installed and
# configured, with a channel playing.
#
#   tool/mibox_perf.sh <adb-serial> [rounds]
#
# Each round: Select (OSD), Select (guide), a burst of Down then Right, Back.
# Prints the trace lines recorded during the run (one JSON per window). This takes
# over the screen of the device for the length of the run.
set -uo pipefail
SERIAL=${1:?usage: mibox_perf.sh <adb-serial> [rounds]}
ROUNDS=${2:-3}
PKG=${PKG:-io.github.rafaelvieiras.playheadtv.client}
A="adb -s $SERIAL"
key() { $A shell "input keyevent $1"; }

before=$($A shell "run-as $PKG sh -c 'cat files/perf_trace.jsonl 2>/dev/null | wc -l'" | tr -d '\r ')
before=${before:-0}
for i in $(seq 1 "$ROUNDS"); do
  key KEYCODE_DPAD_CENTER; sleep 1.2      # OSD
  key KEYCODE_DPAD_CENTER; sleep 4        # guide opens and settles
  # One shell for the whole burst: each `adb shell` call costs ~0.5 s by itself,
  # which would split the burst into several windows.
  $A shell 'for k in 1 2 3 4 5 6; do input keyevent KEYCODE_DPAD_DOWN; sleep 0.25; done; for k in 1 2 3 4 5 6; do input keyevent KEYCODE_DPAD_RIGHT; sleep 0.25; done'
  sleep 3                                  # the burst window closes (1.5 s idle)
  key KEYCODE_BACK; sleep 3                # back to the player
done
sleep 2
$A shell "run-as $PKG sh -c 'tail -n +$((before + 1)) files/perf_trace.jsonl'" | tr -d '\r'
