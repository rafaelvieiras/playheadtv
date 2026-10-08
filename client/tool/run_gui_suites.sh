#!/usr/bin/env bash
# Runs the GUI integration suites (client/integration_test) headless, one
# container per suite, several in parallel. See docs/ARCHITECTURE.md
# "Headless GUI suites in containers".
#
#   client/tool/run_gui_suites.sh                      all suites, 2 in parallel
#   client/tool/run_gui_suites.sh -j 3 epg_window_test osd_test
#   client/tool/run_gui_suites.sh --shard 1/4          every 4th suite (CI matrix)
#
# Options:
#   -j N          suites in parallel (default 2; keep it modest, see Limits)
#   -t SECS       per-suite timeout (default 900)
#   -r            record the screen of each run (screen.mp4)
#   -o DIR        artifacts root (default: client/build/gui-runs)
#   -i IMAGE      image to use (default playheadtv-gui:local; built when absent)
#   --worktree    snapshot the working tree (tracked files) instead of HEAD
#   --shard I/N   run only the suites at positions I, I+N, ... (1-based)
#   --no-retry    never retry
#   --prune       remove the cache volumes and exit
#   -h            this help
# Suites are names (`osd_test`) or paths relative to client/.
#
# Each suite runs against a snapshot (git archive of HEAD by default, so the
# run is not disturbed by edits in progress), in a container with its own
# Xvfb 1920x1080x24, D-Bus, window manager, HOME and cache directory. Limits per
# container: 2 CPUs, 4 GB RAM, 1 GB /dev/shm (override: GUI_CPUS, GUI_MEMORY,
# GUI_SHM). Cache volumes (pub, Go build/modules, Mesa shader cache):
# playheadtv-gui-pub, playheadtv-gui-go and playheadtv-gui-mesa; remove with
# --prune. The shader cache must stay warm: libmdk crashes on a cold one (see
# entry.sh), so the first run per image primes it with one throwaway suite run.
#
# Result labels: PASS; FLAKY (failed with a known environmental signature,
# passed on the single retry: reported, never hidden); FAIL (an assertion
# failure, or a failure that repeated); TIMEOUT.
# Artifacts per suite and attempt: <out>/<run>/<suite>/attempt-N/ with
# suite.log, results.json, backend.log, screen.mp4 (-r) and, when the host kernel
# log is readable, kernel_segfaults.log.
# Limits: Docker does not exercise real GPU rendering, hardware decoders, input
# devices, window-manager fullscreen/focus, audio output or packaging; keep a
# manual desktop pass.
set -uo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
IMAGE=playheadtv-gui:local
J=2; TMO=900; RECORD=0; RETRY=1; WORKTREE=0; SHARD=""; OUTROOT="$ROOT/client/build/gui-runs"
PUBVOL=playheadtv-gui-pub; GOVOL=playheadtv-gui-go; MESAVOL=playheadtv-gui-mesa
# Failures that are environmental (the app died or never started, teardown race),
# never an assertion failure.
FLAKE_RE='Unable to start the app|Error waiting for a debug connection|did not complete|Test process is no longer needed|stalled'
ASSERT_RE='Expected: |EXCEPTION CAUGHT BY FLUTTER TEST FRAMEWORK|TestFailure'

while [ $# -gt 0 ]; do
  case $1 in
    -j) J=$2; shift 2;;
    -t) TMO=$2; shift 2;;
    -r) RECORD=1; shift;;
    -o) OUTROOT=$2; shift 2;;
    -i) IMAGE=$2; shift 2;;
    --worktree) WORKTREE=1; shift;;
    --shard) SHARD=$2; shift 2;;
    --no-retry) RETRY=0; shift;;
    --prune) docker volume rm "$PUBVOL" "$GOVOL" "$MESAVOL"; exit $?;;
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
    suites+=("integration_test/$s.dart")
  done
else
  for f in "$ROOT"/client/integration_test/*_test.dart; do suites+=("integration_test/$(basename "$f")"); done
fi
if [ -n "$SHARD" ]; then
  i=${SHARD%/*}; n=${SHARD#*/}; picked=()
  for k in "${!suites[@]}"; do [ $(( k % n + 1 )) = "$i" ] && picked+=("${suites[$k]}"); done
  suites=("${picked[@]}")
fi
[ ${#suites[@]} -gt 0 ] || { echo "no suites to run" >&2; exit 2; }

command -v docker >/dev/null || { echo "docker is required" >&2; exit 2; }
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo "building $IMAGE"
  docker build -t "$IMAGE" --build-arg GO_VERSION="$(awk '/^go /{print $2}' "$ROOT/backend/go.mod")" \
    "$ROOT/client/test/docker" || exit 2
fi

RUN=$OUTROOT/$(date +%Y%m%d-%H%M%S); mkdir -p "$RUN"
SNAP=$(mktemp -d)
if [ $WORKTREE = 1 ]; then
  (cd "$ROOT" && git ls-files -z backend client | tar --null -T - -cf - 2>/dev/null) | tar -C "$SNAP" -xf -
else
  git -C "$ROOT" archive HEAD backend client | tar -C "$SNAP" -xf -
fi
PREFIX=playheadtv-gui-$$
cleanup() { docker ps -aq --filter "name=^$PREFIX-" | xargs -r docker rm -f >/dev/null 2>&1; rm -rf "$SNAP"; }
trap cleanup EXIT
trap 'exit 130' INT TERM
for v in "$PUBVOL" "$GOVOL" "$MESAVOL"; do docker volume create "$v" >/dev/null; done

attempt() { # suite name n -> exit code; writes <out>/rc as "rc seconds"
  local suite=$1 name=$2 n=$3 out="$RUN/$2/attempt-$3" cname="$PREFIX-$2-$3"
  mkdir -p "$out"; chmod 777 "$out"
  local t0e t0=$SECONDS; t0e=$(date +%s)
  timeout --signal=TERM "$TMO" docker run --rm --init --name "$cname" \
    --cpus "${GUI_CPUS:-2}" --memory "${GUI_MEMORY:-4g}" --shm-size "${GUI_SHM:-1g}" \
    -v "$SNAP":/src:ro -v "$out":/out -v "$PUBVOL":/cache/pub -v "$GOVOL":/cache/go -v "$MESAVOL":/cache/mesa \
    -e RECORD=$RECORD -e OUT=/out -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
    "$IMAGE" "$suite" >"$out/container.log" 2>&1
  local rc=$?
  [ $rc = 124 ] && docker rm -f "$cname" >/dev/null 2>&1
  grep '^\[backend\]' "$out/suite.log" >"$out/backend.log" 2>/dev/null
  # A native crash shows only as "did not complete"; the host kernel log names it.
  if [ $rc != 0 ]; then
    { journalctl -k --since "@$t0e" 2>/dev/null || dmesg 2>/dev/null; } | grep -i segfault >"$out/kernel_segfaults.log" 2>/dev/null
    [ -s "$out/kernel_segfaults.log" ] || rm -f "$out/kernel_segfaults.log"
  fi
  echo "$rc $((SECONDS - t0))" >"$out/rc"
  return $rc
}

job() { # suite -> one summary line
  local suite=$1 name; name=$(basename "$suite" .dart)
  attempt "$suite" "$name" 1; local rc=$? log="$RUN/$name/attempt-1/suite.log" dur
  dur=$(cut -d' ' -f2 "$RUN/$name/attempt-1/rc")
  if [ $rc = 0 ]; then printf '%s\tPASS\t%ss\t\n' "$name" "$dur"; return; fi
  if [ $rc = 124 ]; then printf '%s\tTIMEOUT\t%ss\t\n' "$name" "$dur"; return; fi
  if [ $RETRY = 1 ] && grep -qE "$FLAKE_RE" "$log" && ! grep -qE "$ASSERT_RE" "$log"; then
    local sig; sig=$(grep -oE "$FLAKE_RE" "$log" | head -1)
    attempt "$suite" "$name" 2; rc=$?
    local d2; d2=$(cut -d' ' -f2 "$RUN/$name/attempt-2/rc")
    if [ $rc = 0 ]; then printf '%s\tFLAKY\t%ss+%ss\tpassed on retry after: %s\n' "$name" "$dur" "$d2" "$sig"
    elif [ $rc = 124 ]; then printf '%s\tTIMEOUT\t%ss+%ss\tretry after: %s\n' "$name" "$dur" "$d2" "$sig"
    else printf '%s\tFAIL\t%ss+%ss\tfailed again after: %s\n' "$name" "$dur" "$d2" "$sig"; fi
  else
    printf '%s\tFAIL\t%ss\t\n' "$name" "$dur"
  fi
}

prime() { # warm the Mesa shader cache once per image (see entry.sh)
  local id marker; id=$(docker image inspect -f '{{.Id}}' "$IMAGE" | cut -d: -f2 | cut -c1-12)
  marker=.primed-$id
  docker run --rm -v "$MESAVOL":/m --entrypoint test "$IMAGE" -f "/m/$marker" && return
  echo "priming the shader cache (once per image)"
  # A cold run may itself crash (that is the problem being worked around), but it
  # leaves the cache warmer; repeat until a run passes.
  local n
  for n in 1 2 3; do attempt integration_test/channel_navigation_test.dart _prime "$n" && break; done
  docker run --rm -v "$MESAVOL":/m --entrypoint touch "$IMAGE" "/m/$marker"
}
prime

: >"$RUN/summary.tsv"; start=$SECONDS
echo "image $IMAGE, ${#suites[@]} suite(s), -j $J, timeout ${TMO}s, artifacts in $RUN"
for suite in "${suites[@]}"; do
  while [ "$(jobs -rp | wc -l)" -ge "$J" ]; do wait -n; done
  ( job "$suite" >>"$RUN/summary.tsv" ) &
done
wait
sort -o "$RUN/summary.tsv" "$RUN/summary.tsv"
echo; column -t -s$'\t' "$RUN/summary.tsv"
echo "wall $((SECONDS - start))s with -j $J"
! grep -qP '\t(FAIL|TIMEOUT)\t' "$RUN/summary.tsv"
