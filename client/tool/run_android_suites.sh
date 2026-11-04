#!/usr/bin/env bash
# Runs the Android integration suites on the lab: one headless emulator (or one
# leased device) reused across suites, evidence per suite, one verdict for the
# run. See AGENTS.md "Android validation".
#
#   tool/run_android_suites.sh                       every suite, TV profile
#   tool/run_android_suites.sh osd_test epg_window_test
#   tool/run_android_suites.sh -p phone --shard 1/2
#   tool/run_android_suites.sh --serial <adb-serial>  against a physical device
#   tool/run_android_suites.sh --changed             the suites the diff touches
#   tool/run_android_suites.sh --changed --dry-run   only show that selection
#
# Options:
#   -o DIR     artifacts root (default client/build/android-runs)
#   -t SECS    per-suite timeout (default 900)
#   -p PROF    profile: tv (default) or phone
#   --port N   emulator port (default 5554); one port per run, so two shards run
#              in parallel only from separate checkouts (one Flutter build dir
#              per working tree; see AGENTS.md)
#   --serial S use this device instead of booting one (takes a lease on it)
#   --changed  pick the suites the changed files touch, through tool/suite_map.tsv
#              plus the words of every path; a change nothing names yet runs BOOT
#   --since REF with --changed, also the files committed since REF (e.g. master)
#   --dry-run  print the selection and exit without booting anything
#   --all-suites also select the suites that cannot pass on a device
#   --shard I/N only the suites at positions I, I+N, ... (1-based)
#   --no-retry never retry an environmental failure
#   --no-backend run without the host backend (suites that manage it themselves)
#   --no-watch skip the rolling screen capture (lighter under perf suites)
#   --keep     leave the emulator running
#   -r         record the device screen per suite
#
# Results: <out>/<run>/summary.tsv (suite, status, duration, note),
# <out>/<run>/results.json (array, one object per suite), per-suite artifacts
# under <out>/<run>/suites/<suite>/. Exit 0 only when every suite passed.
set -uo pipefail

# From the environment when this is the private copy the guard below runs: that
# copy lives in /tmp, so its own path says nothing about the repository.
TOOL=${TOOL:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}
ROOT=${ROOT:-$(cd "$TOOL/../.." && pwd)}
LAB=$TOOL/android_lab.sh
OUTROOT=$ROOT/client/build/android-runs
PROFILE=tv; PORT=5554; SERIAL=; TMO=900; SHARD=; RETRY=1; BACKEND=1; KEEP=0; RECORD=0; WATCH=1
CHANGED=0; SINCE=; DRY=0; ALLSUITES=0

# bash reads a script as it runs, so editing one under a live run makes that run
# execute garbage: it happened twice, each time an hour into a census. The rest
# of the run continues from a private copy, and the next edit only reaches the
# next run.
if [ -z "${LAB_SELF_COPY:-}" ]; then
  LAB_SELF_COPY=$(mktemp "${TMPDIR:-/tmp}/$(basename "${BASH_SOURCE[0]}" .sh).XXXXXX")
  cat "${BASH_SOURCE[0]}" >"$LAB_SELF_COPY"
  LAB_SELF_COPY=$LAB_SELF_COPY TOOL=$TOOL ROOT=$ROOT exec bash "$LAB_SELF_COPY" "$@"
fi

while [ $# -gt 0 ]; do
  case $1 in
    -o) OUTROOT=$2; shift 2;;
    -t) TMO=$2; shift 2;;
    -p|--profile) PROFILE=$2; shift 2;;
    --port) PORT=$2; shift 2;;
    --serial) SERIAL=$2; shift 2;;
    --shard) SHARD=$2; shift 2;;
    --changed) CHANGED=1; shift;;
    --since) SINCE=$2; CHANGED=1; shift 2;;
    --dry-run) DRY=1; shift;;
    --all-suites) ALLSUITES=1; shift;;
    --no-retry) RETRY=0; shift;;
    --no-backend) BACKEND=0; shift;;
    --no-watch) WATCH=0; shift;;
    --keep) KEEP=1; shift;;
    -r) RECORD=1; shift;;
    -h|--help) sed -n '2,/^set -u/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0;;
    --) shift; break;;
    -*) echo "unknown option $1" >&2; exit 2;;
    *) break;;
  esac
done

# Suites that need the backend on the same machine as the app: they stop and
# start it, or start a second fixture of their own. The backend runs on the host
# here, so these cannot pass on a device and are not selected by default.
DESKTOP_ONLY="offline_coldstart_test offline_exhausted_test offline_playback_test
offline_reconcile_test settings_offline_test epg_window_test"
# Boot and wiring: what to run when a change is central enough to reach anything.
BOOT_SUITES="osd_test exit_flow_test fullscreen_test warm_start_test
focus_lifecycle_test screen_awake_test loading_navigation_test"

all_suites() { local f; for f in "$ROOT"/client/integration_test/*_test.dart; do basename "$f" .dart; done; }
is_desktop_only() { case " $(echo $DESKTOP_ONLY) " in *" $1 "*) return 0;; esac; return 1; }

suites=()
if [ $# -gt 0 ]; then
  for s in "$@"; do
    s=${s#client/}; s=${s#integration_test/}; s=${s%.dart}
    [ -f "$ROOT/client/integration_test/$s.dart" ] || { echo "no such suite: $s" >&2; exit 2; }
    suites+=("$s")
  done
elif [ $CHANGED = 0 ]; then
  for s in $(all_suites); do
    if is_desktop_only "$s" && [ $ALLSUITES = 0 ]; then
      echo "skipping $s: it needs the backend on the device (see AGENTS.md)"
    else
      suites+=("$s")
    fi
  done
fi

# Which suites a diff touches, from tool/suite_map.tsv plus the words of the
# paths. It errs wide on purpose: a suite that is not selected is a suite that
# did not run, so the answer is a union of every plausible reading of the change.
if [ $CHANGED = 1 ]; then
  pairs=(); picked=()
  add() { # one suite name, or the ALL/BOOT keyword
    case $1 in
      ALL) local f; for f in $(all_suites); do picked+=("$f"); done;;
      BOOT) local b; for b in $(echo $BOOT_SUITES); do picked+=("$b"); done;;
      *) picked+=("$1");;
    esac
  }
  files=$(cd "$ROOT" && {
    [ -n "$SINCE" ] && git diff --name-only "$SINCE"...HEAD 2>/dev/null
    git diff --name-only 2>/dev/null
    git diff --cached --name-only 2>/dev/null
    git ls-files --others --exclude-standard 2>/dev/null
  } | sort -u)
  [ -n "$files" ] || { echo "nothing changed since ${SINCE:-HEAD}; pass suites explicitly" >&2; exit 2; }

  unmatched=()
  for f in $files; do
    mine=()
    while IFS=$'\t' read -r -a fields; do
      # The last field names the suites, everything before it names files, so a
      # stray tab is a wide choice and never a suite that does not exist.
      [ ${#fields[@]} -ge 2 ] || continue
      pats=${fields[*]:0:$((${#fields[@]} - 1))}
      names=${fields[$((${#fields[@]} - 1))]}
      case ${pats:-} in ''|'#'*) continue;; esac
      hit=0
      for pat in $pats; do
        # shellcheck disable=SC2254
        case $f in $pat) hit=1; break;; esac
      done
      [ $hit = 1 ] || continue
      for s in $names; do mine+=("$s"); done
    done <"$TOOL/suite_map.tsv"
    # The map is hand-written and falls behind, but when it has an answer that
    # answer is the considered one; the words of the path are the fallback for
    # everything nobody has mapped yet (`client/test/dimmer_test.dart` reaches
    # dimmer_test, a golden of the guide reaches the guide suites).
    if [ ${#mine[@]} -gt 0 ]; then
      for s in "${mine[@]}"; do add "$s"; done
      pairs+=("$f"$'\t'"$(printf '%s\n' "${mine[@]}" | sort -u | tr '\n' ' ')")
      continue
    fi
    words=$(printf '%s' "$f" | tr '/_.-' '    ' | tr ' ' '\n' | awk 'length($0) >= 3' |
      grep -vxE 'lib|dart|client|test|integration|support|tool|page|pages|widget|widgets|service|services|util|utils|file|files|main|app|new' | tr '\n' ' ')
    for s in $(all_suites); do
      for w in $(printf '%s' "${s%_test}" | tr '_' ' '); do
        case " $words " in *" $w "*) mine+=("$s"); break;; esac
      done
    done
    if [ ${#mine[@]} = 0 ]; then
      unmatched+=("$f")
    else
      for s in "${mine[@]}"; do add "$s"; done
      pairs+=("$f"$'\t'"$(printf '%s\n' "${mine[@]}" | sort -u | tr '\n' ' ')")
    fi
  done

  if [ ${#picked[@]} = 0 ]; then
    echo "no suite is named by this change; running the boot set so the run is not empty" >&2
    for s in $(echo $BOOT_SUITES); do picked+=("$s"); done
  fi

  for s in $(printf '%s\n' "${picked[@]}" | sort -u); do
    [ -f "$ROOT/client/integration_test/$s.dart" ] || { echo "suite_map.tsv names a suite that does not exist: $s" >&2; continue; }
    if is_desktop_only "$s" && [ $ALLSUITES = 0 ]; then
      echo "skipping $s: the map sent this change to it, but it needs the backend on the device" >&2
      continue
    fi
    suites+=("$s")
  done
  [ ${#unmatched[@]} = 0 ] || printf 'no suite is named by: %s\n' "${unmatched[*]}" >&2
fi
[ ${#suites[@]} -gt 0 ] || { echo "no suites to run" >&2; exit 2; }

if [ -n "$SHARD" ]; then
  i=${SHARD%/*}; n=${SHARD#*/}; picked_shard=()
  for k in "${!suites[@]}"; do [ $(( k % n + 1 )) = "$i" ] && picked_shard+=("${suites[$k]}"); done
  suites=("${picked_shard[@]}")
fi
if [ $DRY = 1 ]; then
  # Whatever the run would do, without a device: what booted and installed for
  # nothing teaches nobody which suite the change was supposed to reach.
  head_line="${#suites[@]} suite(s)"
  [ -n "$SINCE" ] && head_line="$head_line since $SINCE"
  [ $CHANGED = 1 ] && head_line="$head_line, from $(echo $files | wc -w) changed file(s)"
  echo "$head_line:"
  for s in "${suites[@]}"; do echo "  $s"; done
  if [ ${#pairs[@]} -gt 0 ]; then
    echo
    echo "file -> suites"
    printf '%s\n' "${pairs[@]}" | sed 's/^/  /'
  fi
  exit 0
fi

RUN=$OUTROOT/$(date +%Y%m%d-%H%M%S); mkdir -p "$RUN/suites"
if [ $CHANGED = 1 ]; then
  # The selection is evidence too: a run that passed 3 suites has to say how it
  # picked those 3.
  {
    echo "# selection: ${#suites[@]} suite(s), ${SINCE:+since $SINCE }from $(echo $files | wc -w) changed file(s)"
    for s in "${suites[@]}"; do echo "suite\t$s"; done
    [ ${#pairs[@]} -eq 0 ] || { echo "# file\tsuites"; printf '%s\n' "${pairs[@]}"; }
  } >"$RUN/selection.tsv"
fi
booted=0
cleanup() {
  if [ $booted = 1 ] && [ $KEEP = 0 ]; then "$LAB" avd down "$SERIAL" >/dev/null 2>&1; fi
  rm -f "${LAB_SELF_COPY:-}"
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

# A leftover child of the previous suite can still hold the lease for a
# moment; wait it out instead of calling the whole run blocked.
args=(-t "$TMO" --serial "$SERIAL" --lease-wait 180)
[ $RETRY = 0 ] && args+=(--no-retry)
[ $BACKEND = 0 ] && args+=(--no-backend)
[ $WATCH = 0 ] && args+=(--no-watch)
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

# A suite that produced no verdict is a failure, not an absence: a lab that dies
# halfway (or a device that vanished) must not look like a clean run. Fill in the
# missing result here, so the report an agent reads never has a silent hole.
for suite in "${suites[@]}"; do
  [ -f "$RUN/suites/$suite/results.json" ] && continue
  grep -qP "^$suite\t" "$RUN/summary.tsv" ||
    printf '%s\tFAIL\t0\tsuite produced no result (traceback in %s)\n' "$suite" "${suite}.err" >>"$RUN/summary.tsv"
  mkdir -p "$RUN/suites/$suite"
  printf '{\n  "suite": "%s",\n  "target": "%s",\n  "status": "FAIL",\n  "duration_s": 0,\n  "note": "no result from the lab; see %s.err",\n  "artifacts": {"dir": "%s"}\n}\n' \
    "$suite" "$SERIAL" "$suite" "suites/$suite" >"$RUN/suites/$suite/results.json"
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
! grep -qP '\t(FAIL|TIMEOUT)\t' "$RUN/summary.tsv" && [ "$total" = "${#suites[@]}" ]
