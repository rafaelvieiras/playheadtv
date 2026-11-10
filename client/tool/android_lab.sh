#!/usr/bin/env bash
# Android validation lab: one headless emulator (or one leased physical device),
# one integration suite, and a verdict an agent can act on without looking at a
# screen. See AGENTS.md "Android validation" for the contract and for why the
# suites run here instead of on a developer's phone or set-top box.
#
#   tool/android_lab.sh avd up [--profile tv|phone] [--port 5554] [--cold|--warm] [--wipe]
#   tool/android_lab.sh avd down <serial>|--all
#   tool/android_lab.sh run <suite> --serial <serial> [-o DIR] [-t SECS] [--no-backend] [--no-retry] [-r]
#   tool/android_lab.sh run <suite> --profile tv [...]      (boots one, kills it after)
#   tool/android_lab.sh verdict <attempt-dir>
#
# The device layer is not here. Booting, taming, leasing, pressing, the widget
# tree, the screenshot and the golden diff belong to the shared kit at
# android-lab (ANDROID_LAB_BIN), so the next Android
# project uses the same one and only one script knows how an emulator is driven.
# What is left here is what belongs to this project: the host backend, the media
# the suites play, the evidence a failing suite leaves behind, and the verdict.
#
# `avd up|down` stay because tool/run_android_suites.sh calls them; they pass
# through to `lab up|down`, as do `profiles`, `device`, `grab` and `lease`
# (`lab profiles|status|evidence|lease`).
#
# Profiles:
#   tv     Android TV, 1920x1080 @ 320dpi, 2 GB RAM: the geometry and the memory
#          of a 1080p set-top box, so a layout that only works with a desktop's
#          RAM or a phone's aspect ratio fails here.
#   phone  phone, the AVD's own geometry.
# Override the system images with ANDROID_LAB_IMAGE_TV / ANDROID_LAB_IMAGE_PHONE.
#
# Output of `run`: artifacts per attempt under <out>/<suite>/attempt-N/ and one
# summary line on stdout, tab-separated: suite, status, duration, note.
#
# Exit codes: 0 PASS, 0 FLAKY (with the flake named in the note), 1 FAIL,
# 2 usage or setup error, 3 device busy, 124 timeout.
set -uo pipefail

# From the environment when this is the private copy the guard below runs.
ROOT=${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
CLIENT=${CLIENT:-$ROOT/client}
SDK=${ANDROID_SDK_ROOT:-${ANDROID_HOME:-}}
# The SDK this project pins (.fvmrc), not whatever `flutter` is first on PATH: a
# machine can have more than one Flutter install, they share one
# .dart_tool/hooks_runner, and a precompiled hook.dill built by one cannot be
# loaded by the other's Dart ("Can't load Kernel binary: Invalid SDK hash", the
# hook exits 253 and Gradle reports "Building native assets failed"). FLUTTER
# overrides; the rest mirrors tool/package_linux.sh.
lab_flutter() {
  if [ -n "${FLUTTER:-}" ]; then echo "$FLUTTER"; return; fi
  if [ -x "$CLIENT/.fvm/flutter_sdk/bin/flutter" ]; then echo "$CLIENT/.fvm/flutter_sdk/bin/flutter"; return; fi
  if command -v fvm >/dev/null 2>&1 && [ -f "$CLIENT/.fvmrc" ]; then echo "fvm flutter"; return; fi
  command -v flutter
}
STATE=${ANDROID_LAB_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/android-lab}
# The device layer — boot, tame, lease, press, screenshot, the widget tree — lives
# in the shared kit, so the next Android project uses the same one. Point
# ANDROID_LAB_BIN at it, or have `lab` on PATH.
LAB=${ANDROID_LAB_BIN:-$(command -v lab 2>/dev/null || echo "${ANDROID_LAB_HOME:-android-lab}/lab")}
TMO=900; OUT=; SERIAL=; PROFILE=; PORT=5554; COLD=1; WIPE=0; RETRY=1; BACKEND=1; MEDIA=1; WATCH=1; FRAME_UI=0
RECORD=0; WAIT=0; LEASE_WAIT=0; PKG=io.github.rafaelvieiras.playheadtv.client
FLAKE_RE='Unable to start the app|Error waiting for a debug connection|did not complete|Test process is no longer needed|stalled|device offline'
ASSERT_RE='Expected: |EXCEPTION CAUGHT BY FLUTTER TEST FRAMEWORK|TestFailure|Failed assertion'

# bash reads a script as it runs, so editing one under a live run makes that run
# execute garbage: it happened twice, each time an hour into a census. The rest
# of the run continues from a private copy, and the next edit only reaches the
# next run.
if [ -z "${LAB_SELF_COPY:-}" ]; then
  LAB_SELF_COPY=$(mktemp "${TMPDIR:-/tmp}/$(basename "${BASH_SOURCE[0]}" .sh).XXXXXX")
  cat "${BASH_SOURCE[0]}" >"$LAB_SELF_COPY"
  LAB_SELF_COPY=$LAB_SELF_COPY ROOT=$ROOT CLIENT=$CLIENT exec bash "$LAB_SELF_COPY" "$@"
fi

die() { echo "android_lab: $*" >&2; exit 2; }
[ -x "$LAB" ] || die "the shared kit is not at $LAB; set ANDROID_LAB_BIN to its lab script"
note() { echo "android_lab: $*" >&2; }

sdk_paths() {
  [ -n "$SDK" ] || die "set ANDROID_SDK_ROOT (or ANDROID_HOME) to the Android SDK"
  ADB=$SDK/platform-tools/adb; EMU=$SDK/emulator/emulator
  AVDMANAGER=$SDK/cmdline-tools/latest/bin/avdmanager
  SDKMANAGER=$SDK/cmdline-tools/latest/bin/sdkmanager
  for b in "$ADB" "$EMU" "$AVDMANAGER"; do [ -x "$b" ] || die "missing $b"; done
}

# A usable emulator needs /dev/kvm: without it qemu falls back to TCG and a cold
# boot takes tens of minutes, which is worse than no device at all. Say so once,
# with the fix, instead of hanging in the boot wait.
# The backend and the media server keep fixed ports: the address is compiled into
# the app with --dart-define, and a reused build with an old define fails as
# "connection refused" on a port no log announces. A busy port is a stop with the
# owner named, never a silently different number.
LAB_BACKEND_PORT=${ANDROID_LAB_BACKEND_PORT:-18100}
LAB_MEDIA_PORT=${ANDROID_LAB_MEDIA_PORT:-18101}

want_port() { # port what
  local owner
  (exec 3<>/dev/tcp/127.0.0.1/"$1") 2>/dev/null || return 0
  owner=$(ss -ltnp 2>/dev/null | awk -v p=":$1" '$4 ~ p"$" {print $6; exit}')
  die "port $1 is busy${owner:+ ($owner)} and $2 needs it; stop that process or set ANDROID_LAB_BACKEND_PORT / ANDROID_LAB_MEDIA_PORT"
}

# A device is shared with a human and with other runs, so whoever drives it takes
# the same lease the shared kit takes: one file, one convention. The kit names the
# file (lock-path) and this script holds it for as long as it drives the device.
take_lease() { # serial wait-seconds
  local serial=$1 wait=$2 lf
  lf=$("$LAB" lock-path "$serial") || die "the kit could not name the lease for $serial"
  exec 9>"$lf"
  if [ "$wait" = 0 ]; then
    flock -n 9 || { echo "BLOCKED(busy) $serial is leased by another run" >&2; exit 3; }
  else
    flock -w "$wait" 9 || { echo "BLOCKED(busy) $serial stayed leased for ${wait}s" >&2; exit 3; }
  fi
  echo "android_lab: lease on $serial held by $$" >&2
}

evidence() {
  local serial=$1 out=$2 pid
  mkdir -p "$out"
  "$ADB" -s "$serial" exec-out screencap -p >"$out/screenshot.png" 2>/dev/null || rm -f "$out/screenshot.png"
  # The accessibility tree as text: it is what lets a text-only agent tell "the
  # guide is open on channel 3" from "the app is on its splash screen".
  if "$ADB" -s "$serial" shell uiautomator dump /sdcard/lab-ui.xml >/dev/null 2>&1; then
    "$ADB" -s "$serial" pull /sdcard/lab-ui.xml "$out/ui.xml" >/dev/null 2>&1 || rm -f "$out/ui.xml"
  fi
  # The app's own log, which survives the process and is the only place it
  # explains itself (why it reloaded the channel, which engine it picked): logcat
  # has the native side, not this. A debug build allows reading its data dir.
  local f
  "$ADB" -s "$serial" shell run-as "$PKG" ls -R files 2>/dev/null | tr -d '\r' >"$out/app-files.txt"
  [ -s "$out/app-files.txt" ] || rm -f "$out/app-files.txt"
  grep -E '\.log$|\.jsonl$|\.log\.[0-9]+$' "$out/app-files.txt" 2>/dev/null | while read -r f; do
    case $f in
      /*) path=${f#/};; *) path=files/$f;;
    esac
    "$ADB" -s "$serial" shell run-as "$PKG" cat "$path" 2>/dev/null >"$out/app-$(basename "$f")"
  done
  # Screenshots the suite took for itself, out of the app sandbox: a visual suite
  # that fails has to leave its evidence where the host can reach it.
  mkdir -p "$out/shots"
  "$ADB" -s "$serial" shell run-as "$PKG" ls files/lab-shots 2>/dev/null | tr -d '\r' |
    grep -E '\.png$|\.txt$' | while read -r f; do
      [ -n "$f" ] && "$ADB" -s "$serial" shell run-as "$PKG" cat "files/lab-shots/$f" 2>/dev/null >"$out/shots/$f"
    done
  rmdir "$out/shots" 2>/dev/null
  # The guest clock drifts under load, and everything the app schedules by wall
  # clock moves with it: it belongs in the evidence, not in the reader's guesses.
  local guest
  guest=$("$ADB" -s "$serial" shell date +%s 2>/dev/null | tr -d '\r')
  case $guest in ''|*[!0-9]*) ;; *) echo "$(( $(date +%s) - guest ))" >"$out/skew.txt";; esac
  "$ADB" -s "$serial" shell dumpsys gfxinfo "$PKG" >"$out/gfxinfo.txt" 2>/dev/null || rm -f "$out/gfxinfo.txt"
  "$ADB" -s "$serial" shell dumpsys activity processes 2>/dev/null | grep -E 'ANR in |crash' >"$out/anr.txt"
  [ -s "$out/anr.txt" ] || rm -f "$out/anr.txt"
  pid=$("$ADB" -s "$serial" shell pidof "$PKG" 2>/dev/null | tr -d '\r')
  [ -n "$pid" ] && echo "$pid" >"$out/pid.txt"
  # The buffer was cleared when the attempt started, so the whole dump is exactly
  # this run: keep it (an app that never logged to `flutter` is still explained
  # by the native side) and a filtered copy for whoever reads the report.
  "$ADB" -s "$serial" logcat -d -v threadtime 2>/dev/null | tail -n 20000 >"$out/logcat-full.txt"
  grep -E "flutter|AndroidRuntime|FATAL|ANR|libc|mdk|AudioTrack|GLES|EGL|$PKG" "$out/logcat-full.txt" >"$out/logcat.txt"
  [ -s "$out/logcat.txt" ] || rm -f "$out/logcat.txt"
  [ -s "$out/logcat-full.txt" ] || rm -f "$out/logcat-full.txt"
}

# A failure that only exists on the screen (a black frame, an error banner, a
# spinner that never ends) is gone by the time the suite ends: the app is torn
# down and the launcher comes back, so the post-mortem screenshot shows the
# launcher and nothing else. Keep a rolling window of frames from the run.
watch_screen() { # serial dir
  local serial=$1 dir=$2 keep=${3:-12} i=0 f
  mkdir -p "$dir"
  while :; do
    i=$((i + 1))
    f=$(printf '%s/frame-%03d' "$dir" "$i")
    "$ADB" -s "$serial" exec-out screencap -p >"$f.png" 2>/dev/null || rm -f "$f.png"
    # No `uiautomator dump` here: it turns on the accessibility service, which
    # turns on Flutter's semantics tree inside the app and makes the suite end
    # with "A SemanticsHandle was active at the end of the test" — the lab
    # failing the suite it is supposed to observe. `--watch-ui` opts in when a
    # frame is not enough and the suite is known not to care.
    if [ "$FRAME_UI" = 1 ] && [ $((i % 3)) = 0 ]; then
      "$ADB" -s "$serial" shell uiautomator dump /sdcard/lab-watch.xml >/dev/null 2>&1 &&
        "$ADB" -s "$serial" pull /sdcard/lab-watch.xml "$f.xml" >/dev/null 2>&1 || rm -f "$f.xml"
    fi
    # Rolling window: a 15 minute suite must not leave 180 screenshots behind.
    ls "$dir"/frame-*.png 2>/dev/null | head -n "-$keep" | while read -r old; do
      rm -f "$old" "${old%.png}.xml"
    done
    sleep "${FRAME_IV:-5}"
  done
}

# The suite log is the primary evidence; logcat and the screen only matter for
# what the harness could not see (a native crash, an ANR, a black frame).
report() {
  local dir=$1 suite=$2 status=$3 note=$4 dur=$5 sig=$6 limit=${7:-4000}
  local log=$dir/suite.log body tmp
  tmp=$(mktemp)
  {
    echo "# $suite: $status"
    echo
    echo "- target: $(cat "$dir/target" 2>/dev/null || echo '?')"
    echo "- duration: ${dur}s, attempts: $(ls -d "$dir"/attempt-* 2>/dev/null | wc -l | tr -d ' ')"
    [ -n "$sig" ] && echo "- signature: \`$sig\`"
    [ -n "$note" ] && echo "- note: $note"
    echo "- attempt dirs: $(ls -d "$dir"/attempt-* 2>/dev/null | xargs -r -n1 basename | tr '\n' ' ')"
    echo "- files: $(ls "$dir"/attempt-*/ 2>/dev/null | grep -v '^$' | sort -u | tr '\n' ' ')"
    local skew
    skew=$(cat "$dir"/attempt-*/skew.txt 2>/dev/null | tail -1)
    if [ -n "$skew" ]; then
      # Past a few seconds the app and the backend disagree about "now", and a
      # suite about schedules or the guide reads a window that has moved.
      if [ "$skew" -gt 5 ] || [ "$skew" -lt -5 ]; then
        echo "- clock skew: ${skew}s (the device clock is off the host: suites about schedules read a window that moved)"
      else
        echo "- clock skew: ${skew}s"
      fi
    fi
    local frames
    frames=$(ls "$dir"/attempt-*/frames/*.png 2>/dev/null | tail -1)
    [ -n "$frames" ] && echo "- screen while it ran: $(ls "$dir"/attempt-*/frames/*.png 2>/dev/null | wc -l | tr -d ' ') frames, last \`$frames\`"
    echo
    echo '## Failures in the suite log'
    echo '```'
    # The line under a failure is the message ("OSD never auto-showed"); the
    # header alone says nothing. Stack frames are noise for a reader.
    grep -hA6 -E "TestFailure was thrown|Expected: |Actual: |FATAL EXCEPTION|Unhandled exception|Failed assertion|Failing tests:" \
      "$log" "$dir"/attempt-*/suite.log 2>/dev/null |
      grep -vE '^\s*#[0-9]|asynchronous suspension|elided [a-z]+ frames' | head -60
    echo '```'
    if [ -s "$dir/attempt-1/logcat.txt" ]; then
      echo '## App logcat (its own log and crashes, tail)'
      echo '```'
      # The package filter matters: the device log is mostly system noise
      # (AppOps, Finsky) that says nothing about this suite.
      grep -hE "flutter :|FATAL EXCEPTION|ANR in |AndroidRuntime|$PKG" "$dir"/attempt-*/logcat.txt 2>/dev/null |
        awk '!/AppOps|BackupHelper|Finsky/' | uniq | tail -40
      echo '```'
    fi
  } >"$tmp"
  head -c "$limit" "$tmp" >"$dir/report.md"
  rm -f "$tmp"
}

jesc() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e 's/\t/ /g'; }

write_json() {
  local dir=$1 suite=$2 status=$3 note=$4 dur=$5 sig=$6
  cat >"$dir/results.json" <<EOF
{
  "suite": "$(jesc "$suite")",
  "target": "$(jesc "$(cat "$dir/target" 2>/dev/null || echo unknown)")",
  "status": "$status",
  "duration_s": ${dur:-0},
  "attempts": $(ls -d "$dir"/attempt-* 2>/dev/null | wc -l | tr -d ' '),
  "signature": "$(jesc "$sig")",
  "note": "$(jesc "$note")",
  "artifacts": {
    "report": "$(jesc "$dir/report.md")",
    "log": "$(jesc "$dir/attempt-1/suite.log")",
    "logcat": "$(jesc "$dir/attempt-1/logcat.txt")",
    "screenshot": "$(jesc "$dir/attempt-1/screenshot.png")"
  }
}
EOF
}

# Flutter's Android build writes to one fixed build directory per working tree,
# so two suites never run at once against the same checkout; parallel runs take
# `git worktree` copies of the tree (see AGENTS.md).
attempt() {
  local suite=$1 serial=$2 name=$3 n=$4 t0=$SECONDS rc
  # Not on the line above: bash builds every name in a `local` list before it
  # assigns any, so `out=...$name...` would read an unset name under `set -u`.
  local out="$OUT/$name/attempt-$n"
  mkdir -p "$out"; echo "$serial" >"$OUT/$name/target"
  "$ADB" -s "$serial" logcat -c >/dev/null 2>&1
  local extra=() watcher=
  [ "$BACKEND" = 1 ] && extra+=("--dart-define=PLAYHEADTV_IT_BACKEND_URL=$BACKEND_URL")
  [ -n "${MEDIA_URL:-}" ] && extra+=("--dart-define=PLAYHEADTV_IT_MEDIA_BASE_URL=$MEDIA_URL/")
  [ -f "$CLIENT/.dart_tool/package_config.json" ] && extra+=(--no-pub)
  if [ "$WATCH" = 1 ]; then
    watch_screen "$serial" "$out/frames" >/dev/null 2>&1 9>&- &
    watcher=$!
  fi
  # 9>&- everywhere a child outlives the attempt: the lease fd must die with
  # this process, or the next run finds its own device leased by a ghost (and a
  # gradle daemon under `flutter test` would hold it for hours).
  # shellcheck disable=SC2046 # lab_flutter may answer "fvm flutter": two words
  run_suite() {
    ( cd "$CLIENT" && timeout --signal=TERM "$TMO" $(lab_flutter) test \
        "integration_test/$suite.dart" -d "$serial" "${extra[@]}" ) >"$out/suite.log" 2>&1 9>&-
  }
  run_suite
  rc=$?
  if [ $rc != 0 ] && grep -q 'Invalid SDK hash' "$out/suite.log"; then
    # Flutter does not key that cache by SDK, so a hook.dill from another
    # install poisons every later build until it is dropped. Cheap, once.
    note "$suite: build hooks were built by another Flutter SDK; rebuilding them"
    rm -rf "$CLIENT/.dart_tool/hooks_runner"
    run_suite
    rc=$?
  fi
  if [ -n "$watcher" ]; then
    kill "$watcher" 2>/dev/null
    wait "$watcher" 2>/dev/null
  fi
  local dur=$((SECONDS - t0))
  evidence "$serial" "$out"
  # Frames cost a few MB per suite: keep them only where somebody will look.
  [ $rc = 0 ] && rm -rf "$out/frames"
  echo "$rc $dur" >"$out/rc"
  grep '^\[backend\]' "$out/suite.log" >"$out/backend.log" 2>/dev/null; [ -s "$out/backend.log" ] || rm -f "$out/backend.log"
  return $rc
}

# Booting, inspecting and grabbing belong to the shared kit. These verbs stay
# because tool/run_android_suites.sh and the habit of this repo already call
# them; each one passes the question straight through.
boot_device() { # profile port cold wipe -> the serial on stdout
  local p=${1:-tv} n=${2:-5554} c=${3:-1} w=${4:-0} out args=(up)
  args+=(--profile "$p" --port "$n")
  [ "$c" = 1 ] && args+=(--cold) || args+=(--warm)
  [ "$w" = 1 ] && args+=(--wipe)
  out=$("$LAB" "${args[@]}") || return 2
  # The kit answers in JSON, one object per call: the serial is a field, so it is
  # read out of the answer rather than printed by it.
  if printf '%s' "$out" | sed -n 's/.*"serial": *"\([^"]*\)".*/\1/p' | head -1 | grep .; then
    return 0
  fi
  printf '%s\n' "$out" >&2
  die "the kit booted nothing it could name"
}

cmd_profiles() { exec "$LAB" profiles; }
cmd_device() { exec "$LAB" status; }
cmd_grab() { exec "$LAB" evidence "$@"; }

cmd_avd() {
  local sub=${1:-up}; shift || true
  case $sub in
    up)
      while [ $# -gt 0 ]; do
        case $1 in
          --profile|-p) PROFILE=${2:?}; shift 2;;
          --port) PORT=${2:?}; shift 2;;
          --cold) COLD=1; shift;;
          --warm) COLD=0; shift;;
          --wipe) WIPE=1; COLD=1; shift;;
          *) die "unknown option $1";;
        esac
      done
      boot_device "${PROFILE:-tv}" "$PORT" "$COLD" "$WIPE" || exit 2
      ;;
    down) exec "$LAB" down "$@";;
    create) die "avd create is gone: 'avd up' creates the AVD and boots it, 'avd up --wipe' starts over";;
    list) exec "$LAB" status;;
    *) die "unknown avd subcommand '$sub' (up|down)";;
  esac
}

# Starts the real Go backend on the host, once per run, on a fixed port, and
# links it into the device with `adb reverse`.
start_backend() { # dir tag
  local dir=$1 tag=${2:-run} port
  local bin="$dir/server" db="$dir/db-$tag" log="$dir/$tag-backend.log"
  BACKEND_BIN=$bin
  if [ ! -x "$bin" ]; then
    ( cd "$ROOT/backend" && go build -o "$bin" ./cmd/server ) >"$dir/build.log" 2>&1 ||
      { note "backend build failed: $(tail -3 "$dir/build.log")"; return 1; }
  fi
  # One database per suite: the server refuses a second app_user, and a suite
  # that inherits the previous one's tenant fails on its own setup, which reads
  # as a product bug in the report.
  port=$LAB_BACKEND_PORT; want_port "$port" "the host backend"; BACKEND_URL=http://127.0.0.1:$port
  PLAYHEADTV_DB_PATH=$db PLAYHEADTV_ADDR=:$port "$bin" >"$log" 2>&1 9>&- &
  BACKEND_PID=$!
  local i
  for i in $(seq 1 150); do
    curl -fs "http://127.0.0.1:$port/healthz" >/dev/null 2>&1 && return 0
    kill -0 "$BACKEND_PID" 2>/dev/null || { note "backend died: $(tail -3 "$log")"; return 1; }
    sleep 0.2
  done
  note "backend did not answer /healthz in 30s"; return 1
}

stop_backend() {
  [ -n "${BACKEND_PID:-}" ] && kill "$BACKEND_PID" 2>/dev/null
  BACKEND_PID=
}

# The video fixture shells out to ffmpeg, which a device does not have: encode
# the clips here instead (`tool/gen_it_media.dart` calls the fixture's own ffmpeg
# invocation) and serve that directory, reached through the same `adb reverse`
# tunnel the backend uses. Cached in $STATE/media, since encoding is the slow
# part and the shapes barely change.
start_media() { # log-dir
  local logdir=$1 dir=$STATE/media port i dartbin
  MEDIA_URL=
  mkdir -p "$dir" "$logdir"
  # The generator has to run with the SDK this project uses: a different Dart
  # rebuilds the build hooks with its own hash and dies with "Invalid SDK hash",
  # which reads like a broken checkout. The project's own SDK (fvm) wins, then
  # the one next to `flutter`.
  dartbin=
  for candidate in "$CLIENT/.fvm/flutter_sdk/bin/dart" \
      "$(dirname "$(command -v flutter 2>/dev/null || echo /nonexistent)")/dart" \
      "$(command -v dart 2>/dev/null || echo /nonexistent)"; do
    if [ -x "$candidate" ]; then dartbin=$candidate; break; fi
  done
  [ -n "$dartbin" ] || { note "no dart binary found (looked in .fvm, next to flutter, and on PATH)"; return 1; }
  ( cd "$CLIENT" && "$dartbin" run tool/gen_it_media.dart "$dir" ) >"$logdir/gen-media.log" 2>&1 ||
    { note "fixture media generation failed: $(tail -2 "$logdir/gen-media.log")"; return 1; }
  port=$LAB_MEDIA_PORT; want_port "$port" "the fixture media server"
  ( cd "$dir" && exec python3 -m http.server "$port" --bind 127.0.0.1 9>&- ) \
    >"$logdir/media-server.log" 2>&1 &
  MEDIA_PID=$!
  for i in $(seq 1 50); do
    (exec 3<>/dev/tcp/127.0.0.1/"$port") 2>/dev/null && { MEDIA_URL=http://127.0.0.1:$port; return 0; }
    sleep 0.2
  done
  note "the fixture media server did not answer on port $port"
  stop_media
  return 1
}
stop_media() {
  [ -n "${MEDIA_PID:-}" ] && kill "$MEDIA_PID" 2>/dev/null
  MEDIA_PID=; MEDIA_URL=
}

cmd_run() {
  local suite=${1:?usage: run <suite> --serial <serial>|-p tv|phone}; shift
  while [ $# -gt 0 ]; do
    case $1 in
      --serial) SERIAL=${2:?}; shift 2;;
      --profile|-p) PROFILE=${2:?}; shift 2;;
      --port) PORT=${2:?}; shift 2;;
      -o) OUT=${2:?}; shift 2;;
      -t) TMO=${2:?}; shift 2;;
      -r) RECORD=1; shift;;
      --no-retry) RETRY=0; shift;;
      --no-backend) BACKEND=0; shift;;
      --no-media) MEDIA=0; shift;;
      --no-watch) WATCH=0; shift;;
      --watch-ui) FRAME_UI=1; shift;;
      --no-lease) LEASE_WAIT=-1; shift;;
      --lease-wait) LEASE_WAIT=${2:?}; shift 2;;
      *) die "unknown option $1";;
    esac
  done
  suite=${suite#client/}; suite=${suite#integration_test/}; suite=${suite%.dart}
  [ -f "$CLIENT/integration_test/$suite.dart" ] || die "no such suite: $suite"
  command -v flutter >/dev/null || die "flutter is not on PATH"
  sdk_paths
  local name booted=0
  name=$(basename "$suite")
  if [ -z "$SERIAL" ]; then
    [ -n "$PROFILE" ] || PROFILE=tv
    SERIAL=$(boot_device "$PROFILE" "$PORT" 1 0) || exit 2
    booted=1
  fi
  OUT=${OUT:-$CLIENT/build/android-runs/$(date +%Y%m%d-%H%M%S)}
  mkdir -p "$OUT/$name"
  if [ "$LEASE_WAIT" -ge 0 ]; then take_lease "$SERIAL" "$LEASE_WAIT"; fi

  local bdir=$OUT/.backend rc status=PASS sig= note= dur=0 t0
  if [ "$BACKEND" = 1 ]; then
    mkdir -p "$bdir"
    start_backend "$bdir" "$name" || die "the backend did not start; see $bdir/$name-backend.log"
    "$ADB" -s "$SERIAL" reverse "tcp:${BACKEND_URL##*:}" "tcp:${BACKEND_URL##*:}" >/dev/null 2>&1 ||
      note "adb reverse failed; the app will not reach the host backend"
    echo "===== host backend on $BACKEND_URL (reversed into $SERIAL) =====" >>"$OUT/$name/backend-host.log"
  fi
  local mdir=$OUT/.media
  if [ "$MEDIA" = 1 ]; then
    start_media "$mdir" || die "the fixture media server did not start; see $mdir/gen-media.log"
    "$ADB" -s "$SERIAL" reverse "tcp:${MEDIA_URL##*:}" "tcp:${MEDIA_URL##*:}" >/dev/null 2>&1 ||
      note "adb reverse failed; the suites will not find the fixture media"
    echo "===== fixture media on $MEDIA_URL (reversed into $SERIAL) =====" >>"$OUT/$name/media-host.log"
  fi
  [ "$RECORD" = 1 ] && { "$ADB" -s "$SERIAL" shell screenrecord --time-limit "$TMO" /sdcard/lab-screen.mp4 >/dev/null 2>&1 9>&- & }

  t0=$SECONDS
  attempt "$suite" "$SERIAL" "$name" 1; rc=$?
  if [ $rc = 0 ]; then
    dur=$((SECONDS - t0))
  elif [ $rc = 124 ]; then
    status=TIMEOUT; dur=$((SECONDS - t0))
  elif [ "$RETRY" = 1 ] && grep -qE "$FLAKE_RE" "$OUT/$name/attempt-1/suite.log" &&
       ! grep -qE "$ASSERT_RE" "$OUT/$name/attempt-1/suite.log"; then
    sig=$(grep -oE "$FLAKE_RE" "$OUT/$name/attempt-1/suite.log" | head -1)
    note "retrying after an environmental failure: $sig"
    attempt "$suite" "$SERIAL" "$name" 2; rc=$?
    dur=$((SECONDS - t0))
    if [ $rc = 0 ]; then status=FLAKY; note="passed on retry after: $sig"
    elif [ $rc = 124 ]; then status=TIMEOUT; note="retry after: $sig"
    else status=FAIL; note="failed again after: $sig"; fi
  else
    status=FAIL; dur=$((SECONDS - t0))
    sig=$(grep -ohE "$ASSERT_RE" "$OUT/$name/attempt-1/suite.log" | head -1)
  fi

  if [ "$RECORD" = 1 ]; then
    "$ADB" -s "$SERIAL" shell pkill -INT screenrecord >/dev/null 2>&1; sleep 1
    "$ADB" -s "$SERIAL" pull /sdcard/lab-screen.mp4 "$OUT/$name/screen.mp4" >/dev/null 2>&1 &&
      rm -f "$OUT/$name/screen.mp4"
  fi
  [ "$BACKEND" = 1 ] && {
    "$ADB" -s "$SERIAL" reverse --remove "tcp:${BACKEND_URL##*:}" >/dev/null 2>&1
    stop_backend
  }
  [ "$MEDIA" = 1 ] && {
    "$ADB" -s "$SERIAL" reverse --remove "tcp:${MEDIA_URL##*:}" >/dev/null 2>&1
    stop_media
  }
  report "$OUT/$name" "$suite" "$status" "$note" "$dur" "$sig"
  write_json "$OUT/$name" "$suite" "$status" "$note" "$dur" "$sig"
  printf '%s\t%s\t%ss\t%s\n' "$name" "$status" "$dur" "$note"
  echo "VERDICT: $status  ($OUT/$name/report.md)" >&2
  [ $booted = 1 ] && "$ADB" -s "$SERIAL" emu kill >/dev/null 2>&1
  case $status in PASS|FLAKY) return 0;; TIMEOUT) return 124;; *) return 1;; esac
}

cmd_verdict() {
  local adir=${1:?attempt dir} dir status note sig dur suite rc
  dir=$(dirname "$adir"); suite=$(basename "$dir")
  rc=$(cut -d' ' -f1 "$adir/rc" 2>/dev/null)
  dur=$(cut -d' ' -f2 "$adir/rc" 2>/dev/null || echo 0)
  if grep -qE "$ASSERT_RE" "$adir/suite.log" 2>/dev/null; then
    status=FAIL; sig=$(grep -ohE "$ASSERT_RE" "$adir/suite.log" | head -1)
  elif grep -qE "$FLAKE_RE" "$adir/suite.log" 2>/dev/null; then
    status=FLAKY; sig=$(grep -ohE "$FLAKE_RE" "$adir/suite.log" | head -1)
  else
    status=PASS
  fi
  case $rc in 0) status=PASS;; 124) status=TIMEOUT;; '') : ;; esac
  report "$dir" "$suite" "$status" "${note:-}" "$dur" "${sig:-}"
  write_json "$dir" "$suite" "$status" "${note:-}" "$dur" "${sig:-}"
  echo "VERDICT: $status"
  [ "$status" = PASS ] && return 0
  [ "$status" = TIMEOUT ] && return 124
  return 1
}

while [ $# -gt 0 ]; do
  case $1 in
    profiles) shift; cmd_profiles; exit 0;;
    avd) shift; cmd_avd "$@"; exit $?;;
    device) shift; cmd_device "$@"; exit $?;;
    grab) shift; cmd_grab "$@"; exit $?;;
    run) shift; cmd_run "$@"; exit $?;;
    verdict) shift; cmd_verdict "$@"; exit $?;;
    lease) shift; exec "$LAB" lease "$@";;
    -h|--help) sed -n '2,/^set -u/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0;;
    *) die "unknown command '$1' (see --help)";;
  esac
done
die "nothing to do (see --help)"
