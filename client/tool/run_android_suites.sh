#!/usr/bin/env bash
# Runs the Android integration suites on the lab: one headless emulator (or one
# leased device) reused across suites, evidence per suite, one verdict for the
# run. See AGENTS.md "Android validation".
#
#   tool/run_android_suites.sh                       every suite, TV profile
#   tool/run_android_suites.sh osd_test epg_window_test
#   tool/run_android_suites.sh -p phone --shard 1/2
#   tool/run_android_suites.sh --serial <adb-serial>  against a physical device
#
# Options:
#   -o DIR     artifacts root (default client/build/android-runs)
#   -t SECS    per-suite timeout (default 900)
#   -p PROF    profile: tv (default) or phone
#   --port N   emulator port (default 5554); one port per run, so two shards run
#              in parallel only from separate checkouts (one Flutter build dir
#              per working tree; see AGENTS.md)
#   --serial S use this device instead of booting one (takes a lease on it)
#   --shard I/N only the suites at positions I, I+N, ... (1-based)
#   --no-retry never retry an environmental failure
#   --no-backend run without the host backend (suites that manage it themselves)
#   --keep     leave the emulator running
#   -r         record the device screen per suite
#
# Results: <out>/<run>/summary.tsv (suite, status, duration, note),
# <out>/<run>/results.json (array, one object per suite), per-suite artifacts
# under <out>/<run>/suites/<suite>/. Exit 0 only when every suite passed.
set -uo pipefail

TOOL=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$TOOL/../.." && pwd)
LAB=$TOOL/android_lab.sh
OUTROOT=$ROOT/client/build/android-runs
PROFILE=tv; PORT=5554; SERIAL=; TMO=900; SHARD=; RETRY=1; BACKEND=1; KEEP=0; RECORD=0

while [ $# -gt 0 ]; do
  case $1 in
    -o) OUTROOT=$2; shift 2;;
    -t) TMO=$2; shift 2;;
    -p|--profile) PROFILE=$2; shift 2;;
    --port) PORT=$2; shift 2;;
    --serial) SERIAL=$2; shift 2;;
    --shard) SHARD=$2; shift 2;;
    --no-retry) RETRY=0; shift;;
    --no-backend) BACKEND=0; shift;;
    --keep) KEEP=1; shift;;
    -r) RECORD=1; shift;;
    -h|--help) sed -n '2,/^set -u/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0;;
    --) shift; break;;
    -*) echo "unknown option $1" >&2; exit 2;;
    *) break;;
  esac
done

suites=()
if [ $# -gt 0 ]; then
  for s in "$@"; do
    s=${s#client/}; s=${s#integration_test/}; s=${s%.dart}
    [ -f "$ROOT/client/integration_test/$s.dart" ] || { echo "no such suite: $s" >&2; exit 2; }
    suites+=("$s")
  done
else
  for f in "$ROOT"/client/integration_test/*_test.dart; do suites+=("$(basename "$f" .dart)"); done
fi
if [ -n "$SHARD" ]; then
  i=${SHARD%/*}; n=${SHARD#*/}; picked=()
  for k in "${!suites[@]}"; do [ $(( k % n + 1 )) = "$i" ] && picked+=("${suites[$k]}"); done
  suites=("${picked[@]}")
fi
[ ${#suites[@]} -gt 0 ] || { echo "no suites to run" >&2; exit 2; }

RUN=$OUTROOT/$(date +%Y%m%d-%H%M%S); mkdir -p "$RUN/suites"
booted=0
cleanup() {
  if [ $booted = 1 ] && [ $KEEP = 0 ]; then "$LAB" avd down "$SERIAL" >/dev/null 2>&1; fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

if [ -z "$SERIAL" ]; then
  SERIAL=$("$LAB" avd up --profile "$PROFILE" --port "$PORT") || exit 2
  booted=1
elif [ -n "$SERIAL" ]; then
  # A physical device is shared with a human: lease it for the whole run so a
  # second run is told it is busy instead of fighting for the screen.
  "$LAB" lease "$SERIAL" || exit 3
fi

args=(-t "$TMO" --serial "$SERIAL")
[ $RETRY = 0 ] && args+=(--no-retry)
[ $BACKEND = 0 ] && args+=(--no-backend)
[ $RECORD = 1 ] && args+=(-r)

: >"$RUN/summary.tsv"; start=$SECONDS
echo "target $SERIAL, ${#suites[@]} suite(s), timeout ${TMO}s, artifacts in $RUN"
for suite in "${suites[@]}"; do
  # One suite at a time: Flutter's Android build and install share a single
  # directory per working tree, so the parallelism unit is a device, driven by
  # separate checkouts (see AGENTS.md).
  #
  # shellcheck disable=SC2086
  line=$("$LAB" run "$suite" -o "$RUN/suites" "${args[@]}" 2>>"$RUN/suites/$suite.err")
  rc=$?
  [ -n "$line" ] && printf '%s\n' "$line" >>"$RUN/summary.tsv"
  [ $rc = 3 ] && { echo "device became busy during the run" >&2; break; }
done
sort -o "$RUN/summary.tsv" "$RUN/summary.tsv"

# One machine-readable verdict for the whole run, so an agent reads one file
# instead of counting exits of a shell loop.
{
  echo '['
  first=1
  for f in "$RUN"/suites/*/results.json; do
    [ -f "$f" ] || continue
    [ $first = 1 ] || echo ','
    first=0
    sed 's/^/  /' "$f"
  done
  echo ']'
} >"$RUN/results.json"

echo; column -t -s$'\t' "$RUN/summary.tsv" 2>/dev/null || cat "$RUN/summary.tsv"
echo "wall $((SECONDS - start))s, artifacts in $RUN"
total=$(wc -l <"$RUN/summary.tsv" | tr -d ' ')
passed=$(grep -cP '\tPASS\t' "$RUN/summary.tsv" || true)
flaky=$(grep -cP '\tFLAKY\t' "$RUN/summary.tsv" || true)
echo "VERDICT: $passed/$total passed, $flaky flaky, $((total - passed)) not passing  ($RUN/results.json)"
! grep -qP '\t(FAIL|TIMEOUT)\t' "$RUN/summary.tsv"
