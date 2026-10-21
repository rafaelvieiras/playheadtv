#!/usr/bin/env bash
# Runs the performance suite in profile mode on the desktop session and keeps
# one JSON report per run. Usage: tool/run_perf.sh [-n RUNS] [-o DIR] [suite]
#
# Needs a real desktop session (DISPLAY) and the Go toolchain (the suite builds
# its own backend). The window takes focus for the length of a run, so do not
# use the machine meanwhile. For headless, comparable runs use
# tool/run_gui_suites.sh perf_boot_epg_test (debug mode, software GL).
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
RUNS=3; OUT=build/perf; SUITE=perf_boot_epg_test
while getopts "n:o:" o; do
  case $o in n) RUNS=$OPTARG;; o) OUT=$OPTARG;; *) exit 2;; esac
done
shift $((OPTIND-1)); [ $# -gt 0 ] && SUITE=${1%.dart}
: "${DISPLAY:?needs a desktop session (DISPLAY)}"
mkdir -p "$OUT"
stamp=$(date +%Y%m%d-%H%M%S)
for i in $(seq 1 "$RUNS"); do
  report="$OUT/$SUITE-$stamp-$i.json"
  PERF_OUT="$PWD/$report" fvm flutter drive --profile -d linux \
    --driver=test_driver/integration_test.dart \
    --target="integration_test/$SUITE.dart" > "$OUT/$SUITE-$stamp-$i.log" 2>&1
  rc=$?
  echo "run $i: exit $rc -> $report"
  [ $rc -eq 0 ] || tail -5 "$OUT/$SUITE-$stamp-$i.log"
done
