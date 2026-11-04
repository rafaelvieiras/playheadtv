#!/usr/bin/env bash
# Android validation lab: one headless emulator (or one leased physical device),
# one integration suite, and a verdict an agent can act on without looking at a
# screen. See AGENTS.md "Android validation" for the contract and for why the
# suites run here instead of on a developer's phone or set-top box.
#
#   tool/android_lab.sh profiles
#   tool/android_lab.sh avd create [--profile tv|phone]
#   tool/android_lab.sh avd up [--profile tv|phone] [--port 5554] [--cold] [--wipe]
#   tool/android_lab.sh avd list
#   tool/android_lab.sh avd down <serial>|--all
#   tool/android_lab.sh device <serial>
#   tool/android_lab.sh grab <serial> [-o DIR]
#   tool/android_lab.sh run <suite> --serial <serial> [-o DIR] [-t SECS] [--no-backend] [--no-retry] [-r]
#   tool/android_lab.sh run <suite> --profile tv [...]      (boots one, kills it after)
#   tool/android_lab.sh verdict <attempt-dir>
#   tool/android_lab.sh lease <serial> [--wait SECS]
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

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
CLIENT=$ROOT/client
SDK=${ANDROID_SDK_ROOT:-${ANDROID_HOME:-}}
STATE=${ANDROID_LAB_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/android-lab}
IMAGE_TV=${ANDROID_LAB_IMAGE_TV:-system-images;android-36;android-tv;x86_64}
IMAGE_PHONE=${ANDROID_LAB_IMAGE_PHONE:-system-images;android-33;google_apis;x86_64}
TMO=900; OUT=; SERIAL=; PROFILE=; PORT=5554; COLD=0; WIPE=0; RETRY=1; BACKEND=1
RECORD=0; WAIT=0; LEASE_WAIT=0; PKG=io.github.rafaelvieiras.playheadtv.client
FLAKE_RE='Unable to start the app|Error waiting for a debug connection|did not complete|Test process is no longer needed|stalled|device offline'
ASSERT_RE='Expected: |EXCEPTION CAUGHT BY FLUTTER TEST FRAMEWORK|TestFailure|Failed assertion'

die() { echo "android_lab: $*" >&2; exit 2; }
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
require_accel() {
  if [ ! -e /dev/kvm ]; then
    die "/dev/kvm is missing: the emulator would run without acceleration.
  Load the module once:  sudo modprobe kvm_amd && echo kvm_amd | sudo tee /etc/modules-load.d/kvm-amd.conf
  Check:                 $EMU -accel-check"
  fi
}

avd_name() { echo "lab-$1"; }
profile_image() {
  case $1 in
    tv) echo "$IMAGE_TV" ;;
    phone) echo "$IMAGE_PHONE" ;;
    *) die "unknown profile '$1' (tv|phone)" ;;
  esac
}
profile_device() {
  case $1 in
    tv) echo tv_1080p ;;
    phone) echo pixel_5 ;;
  esac
}
profile_ram() { case $1 in tv) echo 2048 ;; phone) echo 3072 ;; esac; }

# `avdmanager` writes its own hardware profile; the lab's keys are applied over
# it. Each key is replaced, not appended twice: config.ini is read key by key and
# a duplicate is at best ambiguous.
set_ini() { # file key value
  local f=$1 k=$2 v=$3
  sed -i "/^${k//./\\.}=/d" "$f"
  printf '%s=%s\n' "$k" "$v" >>"$f"
}

# The AVD is a disposable lab artifact: if it points at an image the profile no
# longer selects, rebuild it instead of running yesterday's system.
ensure_avd() {
  local profile=$1 name img cfg dir
  name=$(avd_name "$profile"); img=$(profile_image "$profile")
  dir=$HOME/.android/avd/$name.avd; cfg=$dir/config.ini
  if [ -f "$cfg" ] && ! grep -qF "${img//;/\/}" "$cfg"; then
    note "the $name AVD is on another image; recreating it"
    "$AVDMANAGER" delete avd -n "$name" >/dev/null 2>&1
    rm -f "$STATE/$name/boot-snapshot"
  fi
  if [ ! -f "$cfg" ]; then
    if [ ! -f "$SDK/${img//;//}/system.img" ]; then
      note "image $img is not installed; downloading it (that can take a few minutes)"
      yes | "$SDKMANAGER" "$img" >&2 || die "could not install $img"
    fi
    echo no | "$AVDMANAGER" create avd -n "$name" -k "$img" -d "$(profile_device "$profile")" --force >&2 ||
      die "could not create the $name AVD"
    # Hardware the lab needs and hardware it must not pretend to have: no
    # camera or GPS (flaky services cost boot time), keyboard on (a D-pad is
    # not enough to type into the few text fields the app has), and RAM at the
    # target device's size so a memory-hungry layout fails here, not on the box.
    set_ini "$cfg" hw.keyboard yes
    set_ini "$cfg" hw.mainKeys no
    set_ini "$cfg" hw.audioInput no
    set_ini "$cfg" hw.gps no
    set_ini "$cfg" hw.camera.back none
    set_ini "$cfg" hw.camera.front none
    set_ini "$cfg" hw.ramSize "$(profile_ram "$profile")"
    set_ini "$cfg" disk.dataPartition.size 4G
    set_ini "$cfg" hw.gpu.enabled yes
    set_ini "$cfg" hw.gpu.mode swiftshader_indirect
    set_ini "$cfg" showDeviceFrame no
  fi
  mkdir -p "$STATE/$name"
  echo "$name"
}

wait_boot() {
  local serial=$1 i
  "$ADB" -s "$serial" wait-for-device >/dev/null 2>&1 || return 1
  # One adb shell for the whole wait: every separate `adb shell` costs ~0.2 s and
  # a cold boot would need hundreds of them.
  timeout 600 "$ADB" -s "$serial" shell 'while [ "$(getprop sys.boot_completed)" != 1 ]; do sleep 1; done' \
    >/dev/null 2>&1 || return 1
  for i in $(seq 1 60); do
    "$ADB" -s "$serial" shell pm list packages >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}

# Determinism: an animation that is still running when the harness pumps makes a
# golden differ run to run, and a screen that sleeps mid-suite kills the app.
tame_device() {
  local serial=$1 profile=$2
  [ "$profile" = tv ] && "$ADB" -s "$serial" shell 'wm size 1920x1080; wm density 320' >/dev/null 2>&1
  "$ADB" -s "$serial" shell '
    settings put global window_animation_scale 0
    settings put global transition_animation_scale 0
    settings put global animator_duration_scale 0
    settings put global stay_on_while_plugged_in 3
    settings put system screen_off_timeout 2147483647
    settings put secure immersive_mode_confirmations confirmed
    svc power stayon true
    input keyevent KEYCODE_WAKEUP
    wm dismiss-keyguard
  ' >/dev/null 2>&1
}

boot() {
  local profile=$1 port=$2 cold=$3 wipe=$4 name serial marker
  name=$(ensure_avd "$profile"); serial=emulator-$port; marker=$STATE/$name/boot-snapshot
  if "$ADB" devices | grep -q "^$serial[[:space:]]"; then
    "$ADB" -s "$serial" emu kill >/dev/null 2>&1; sleep 2
  fi
  local args=(-avd "$name" -port "$port" -no-window -no-audio -no-boot-anim
              -gpu swiftshader_indirect -netdelay none -netspeed full -no-metrics)
  if [ "$cold" = 1 ] || [ ! -f "$marker" ]; then
    args+=(-no-snapshot-load -no-snapshot-save)
  else
    # A saved boot snapshot is the difference between a ~10 s and a ~60 s start,
    # which is what makes a suite-per-change loop usable.
    args+=(-snapshot "$(cat "$marker")" -no-snapshot-save)
  fi
  [ "$wipe" = 1 ] && args+=(-wipe-data)
  note "booting $name on $serial"
  ("$EMU" "${args[@]}" >"$STATE/$name/emulator-$port.log" 2>&1 &) || die "could not start the emulator"
  wait_boot "$serial" || { note "see $STATE/$name/emulator-$port.log"; die "the emulator did not boot"; }
  tame_device "$serial" "$profile"
  if [ "$cold" = 1 ] || [ ! -f "$marker" ]; then
    "$ADB" -s "$serial" emu avd snapshot save lab >/dev/null 2>&1 && echo lab >"$marker"
  fi
  echo "$serial"
}

free_port() {
  local p
  for p in $(seq 18100 18200); do
    # Bash's /dev/tcp is the cheapest port probe available without a dependency.
    if ! (exec 3<>/dev/tcp/127.0.0.1/"$p") 2>/dev/null; then echo "$p"; return 0; fi
  done
  die "no free port in 18100-18200"
}

lockfile() { echo "$STATE/lease-${1//[^a-zA-Z0-9]/_}.lock"; }

# A physical device is shared with a human, so a run takes a lease on it and a
# second run is told to wait instead of fighting for the screen.
take_lease() {
  local serial=$1 wait=$2 lf; lf=$(lockfile "$serial"); mkdir -p "$STATE"
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
  "$ADB" -s "$serial" shell dumpsys gfxinfo "$PKG" >"$out/gfxinfo.txt" 2>/dev/null || rm -f "$out/gfxinfo.txt"
  "$ADB" -s "$serial" shell dumpsys activity processes 2>/dev/null | grep -E 'ANR in |crash' >"$out/anr.txt"
  [ -s "$out/anr.txt" ] || rm -f "$out/anr.txt"
  pid=$("$ADB" -s "$serial" shell pidof "$PKG" 2>/dev/null | tr -d '\r')
  if [ -n "$pid" ]; then
    "$ADB" -s "$serial" logcat -d -v threadtime --pid="$pid" >"$out/logcat.txt" 2>/dev/null
  else
    "$ADB" -s "$serial" logcat -d -v threadtime -s flutter >"$out/logcat.txt" 2>/dev/null
  fi
  [ -s "$out/logcat.txt" ] || rm -f "$out/logcat.txt"
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
    echo
    echo '## Failures in the suite log'
    echo '```'
    grep -hE "E/flutter|FATAL EXCEPTION|Unhandled exception|Expected: |Actual: |Failed assertion|TestFailure|Error waiting for a debug connection" \
      "$log" "$dir"/attempt-*/suite.log 2>/dev/null | tail -40
    echo '```'
    if [ -s "$dir/attempt-1/logcat.txt" ]; then
      echo '## App logcat (errors, tail)'
      echo '```'
      grep -hE " E |FATAL|Exception|ANR in " "$dir"/attempt-*/logcat.txt 2>/dev/null | tail -30
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
  local suite=$1 serial=$2 name=$3 n=$4 out="$OUT/$name/attempt-$n" t0=$SECONDS rc
  mkdir -p "$out"; echo "$serial" >"$OUT/$name/target"
  [ -n "${BACKEND_BIN:-}" ] && "$ADB" -s "$serial" logcat -c >/dev/null 2>&1
  local extra=()
  [ "$BACKEND" = 1 ] && extra+=("--dart-define=PLAYHEADTV_IT_BACKEND_URL=$BACKEND_URL")
  [ -f "$CLIENT/.dart_tool/package_config.json" ] && extra+=(--no-pub)
  ( cd "$CLIENT" && timeout --signal=TERM "$TMO" "$(command -v flutter)" test \
      "integration_test/$suite.dart" -d "$serial" "${extra[@]}" ) >"$out/suite.log" 2>&1
  rc=$?
  local dur=$((SECONDS - t0))
  evidence "$serial" "$out"
  echo "$rc $dur" >"$out/rc"
  grep '^\[backend\]' "$out/suite.log" >"$out/backend.log" 2>/dev/null; [ -s "$out/backend.log" ] || rm -f "$out/backend.log"
  return $rc
}

cmd_profiles() {
  printf '%-6s %-46s %-12s %s\n' PROFILE IMAGE DEVICE RAM
  printf '%-6s %-46s %-12s %s\n' tv "$IMAGE_TV" "$(profile_device tv)" "$(profile_ram tv)MB"
  printf '%-6s %-46s %-12s %s\n' phone "$IMAGE_PHONE" "$(profile_device phone)" "$(profile_ram phone)MB"
}

cmd_avd() {
  local sub=${1:-list}; shift || true
  case $sub in
    create)
      # Creates or refreshes the AVD without booting it: useful on a machine
      # that cannot run the emulator yet, and it fails early on a missing image.
      while [ $# -gt 0 ]; do
        case $1 in
          --profile) PROFILE=${2:?}; shift 2;;
          *) die "unknown option $1";;
        esac
      done
      PROFILE=${PROFILE:-tv}; sdk_paths
      ensure_avd "$PROFILE"
      ;;
    up)
      while [ $# -gt 0 ]; do
        case $1 in
          --profile) PROFILE=${2:?}; shift 2;;
          --port) PORT=${2:?}; shift 2;;
          --cold) COLD=1; shift;;
          --wipe) WIPE=1; COLD=1; shift;;
          *) die "unknown option $1";;
        esac
      done
      PROFILE=${PROFILE:-tv}; sdk_paths; require_accel
      boot "$PROFILE" "$PORT" "$COLD" "$WIPE"
      ;;
    list)
      sdk_paths
      "$EMU" -list-avds 2>/dev/null | while read -r a; do
        case $a in lab-*) printf '%s\n' "$a";; esac
      done
      "$ADB" devices | awk 'NR>1 && $2=="device" {print "running: " $1}'
      ;;
    down)
      [ $# -gt 0 ] || die "usage: avd down <serial>|--all"
      sdk_paths
      if [ "$1" = --all ]; then
        "$ADB" devices | awk 'NR>1 && $1 ~ /^emulator-/ {print $1}' | while read -r s; do
          "$ADB" -s "$s" emu kill >/dev/null 2>&1 && echo "killed $s"
        done
      else
        "$ADB" -s "$1" emu kill >/dev/null 2>&1 || die "could not kill $1 (not an emulator?)"
        echo "killed $1"
      fi
      ;;
    *) die "unknown avd subcommand '$sub'";;
  esac
}

cmd_device() {
  local serial=${1:?serial}; sdk_paths
  "$ADB" -s "$serial" shell 'getprop ro.product.model; getprop ro.build.version.release;
    getprop ro.build.version.sdk; getprop ro.product.cpu.abi; wm size; wm density' 2>&1 |
    tr -d '\r' | paste -sd' '
  "$ADB" -s "$serial" shell pm list features 2>/dev/null |
    grep -q android.software.leanback && echo "leanback: yes" || echo "leanback: no"
}

# Starts the real Go backend on the host, once per run, on a free port, and
# links it into the device with `adb reverse`: the app and the fixture still see
# http://127.0.0.1:<port>, which is the address the suite already expects, but
# the process and its SQLite file live on the host where Go exists.
start_backend() {
  local dir=$1 bin=$dir/server db=$dir/db port
  BACKEND_BIN=$bin
  if [ ! -x "$bin" ]; then
    ( cd "$ROOT/backend" && go build -o "$bin" ./cmd/server ) >"$dir/build.log" 2>&1 ||
      { note "backend build failed: $(tail -3 "$dir/build.log")"; return 1; }
  fi
  port=$(free_port); BACKEND_URL=http://127.0.0.1:$port
  PLAYHEADTV_DB_PATH=$db PLAYHEADTV_ADDR=:$port "$bin" >"$dir/backend.log" 2>&1 &
  BACKEND_PID=$!
  local i
  for i in $(seq 1 150); do
    curl -fs "http://127.0.0.1:$port/healthz" >/dev/null 2>&1 && return 0
    kill -0 "$BACKEND_PID" 2>/dev/null || { note "backend died: $(tail -3 "$dir/backend.log")"; return 1; }
    sleep 0.2
  done
  note "backend did not answer /healthz in 30s"; return 1
}

stop_backend() {
  [ -n "${BACKEND_PID:-}" ] && kill "$BACKEND_PID" 2>/dev/null
  BACKEND_PID=
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
      --no-lease) LEASE_WAIT=-1; shift;;
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
    require_accel
    SERIAL=$(boot "$PROFILE" "$PORT" 0 0) || exit 2
    booted=1
  fi
  OUT=${OUT:-$CLIENT/build/android-runs/$(date +%Y%m%d-%H%M%S)}
  mkdir -p "$OUT/$name"
  if [ "$LEASE_WAIT" -ge 0 ]; then take_lease "$SERIAL" "$LEASE_WAIT"; fi

  local bdir=$OUT/.backend rc status=PASS sig= note= dur=0 t0
  if [ "$BACKEND" = 1 ]; then
    mkdir -p "$bdir"
    start_backend "$bdir" || die "the backend did not start; see $bdir/backend.log"
    "$ADB" -s "$SERIAL" reverse "tcp:${BACKEND_URL##*:}" "tcp:${BACKEND_URL##*:}" >/dev/null 2>&1 ||
      note "adb reverse failed; the app will not reach the host backend"
    echo "===== host backend on $BACKEND_URL (reversed into $SERIAL) =====" >>"$OUT/$name/backend-host.log"
  fi
  [ "$RECORD" = 1 ] && { "$ADB" -s "$SERIAL" shell screenrecord --time-limit "$TMO" /sdcard/lab-screen.mp4 >/dev/null 2>&1 & }

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
  report "$OUT/$name" "$suite" "$status" "$note" "$dur" "$sig"
  write_json "$OUT/$name" "$suite" "$status" "$note" "$dur" "$sig"
  printf '%s\t%s\t%ss\t%s\n' "$name" "$status" "$dur" "$note"
  echo "VERDICT: $status  ($OUT/$name/report.md)" >&2
  [ $booted = 1 ] && "$ADB" -s "$SERIAL" emu kill >/dev/null 2>&1
  case $status in PASS|FLAKY) return 0;; TIMEOUT) return 124;; *) return 1;; esac
}

cmd_grab() {
  local serial=${1:?serial}; shift; sdk_paths
  local out=$CLIENT/build/android-grab/$(date +%Y%m%d-%H%M%S)
  [ "${1:-}" = -o ] && { out=${2:?}; shift 2; }
  mkdir -p "$out"; evidence "$serial" "$out"
  echo "$out"
  ls -la "$out" | tail -n +2 | awk '{print $5, $9}'
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
    lease)
      shift; local_serial=${1:?serial}; shift || true
      [ "${1:-}" = --wait ] && LEASE_WAIT=${2:?}
      sdk_paths; take_lease "$local_serial" "$LEASE_WAIT"; exit 0;;
    -h|--help) sed -n '2,/^set -u/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0;;
    *) die "unknown command '$1' (see --help)";;
  esac
done
die "nothing to do (see --help)"
