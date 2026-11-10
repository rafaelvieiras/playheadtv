#!/usr/bin/env bash
# Shell tests of the launcher (packaging/linux/playheadtv), install.sh, the
# AppImage entry point (packaging/appimage/AppRun) and release_notes.sh, with
# fake updater and app executables: no network, no GUI.
#   client/tool/test_launcher.sh
set -u

HERE="$(cd "$(dirname "$0")/.." && pwd -P)"
LAUNCHER="$HERE/packaging/linux/playheadtv"
INSTALL="$HERE/packaging/linux/install.sh"
FAILS=0
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

check() { # check <description> <expected> <actual>
    if [ "$2" = "$3" ]; then echo "ok   - $1"; else echo "FAIL - $1 (expected '$2', got '$3')"; FAILS=$((FAILS + 1)); fi
}

# A root with versions 1.0.0 (current) and the given fake updater behaviour.
make_root() { # make_root <name> <updater script body or "none">
    local root="$TMP/$1"
    mkdir -p "$root/versions/1.0.0/bundle" "$root/versions/2.0.0/bundle"
    for v in 1.0.0 2.0.0; do
        printf '#!/bin/sh\necho "app %s args:$*"\n' "$v" >"$root/versions/$v/bundle/client"
        chmod +x "$root/versions/$v/bundle/client"
    done
    ln -s versions/1.0.0 "$root/current"
    cp "$LAUNCHER" "$root/playheadtv"
    if [ "$2" != none ]; then
        printf '#!/bin/sh\n%s\n' "$2" >"$root/versions/1.0.0/bundle/playheadtv-updater"
        chmod +x "$root/versions/1.0.0/bundle/playheadtv-updater"
    fi
    echo "$root"
}

# 1. The updater runs first and its swap is what starts.
root=$(make_root swap 'ln -sfn versions/2.0.0 "$PLAYHEADTV_ROOT/current.tmp" && mv -T "$PLAYHEADTV_ROOT/current.tmp" "$PLAYHEADTV_ROOT/current"')
check "starts the version the updater swapped in" "app 2.0.0 args:a b" "$("$root/playheadtv" a b)"

# 2. No updater at all: the installed version starts.
root=$(make_root noupdater none)
check "starts without an updater" "app 1.0.0 args:" "$("$root/playheadtv")"

# 3. A failing updater never blocks the start.
root=$(make_root failing 'echo boom >&2; exit 7')
check "starts when the updater fails" "app 1.0.0 args:" "$("$root/playheadtv")"

# 4. A hanging updater is bounded by the timeout.
root=$(make_root hanging 'sleep 30')
start=$(date +%s)
out=$(PLAYHEADTV_PREFLIGHT_TIMEOUT=1 PLAYHEADTV_UPDATE_TIMEOUT=1 "$root/playheadtv")
elapsed=$(( $(date +%s) - start ))
check "starts when the updater hangs" "app 1.0.0 args:" "$out"
[ "$elapsed" -lt 15 ] && check "the hang was cut by the timeout" ok ok || check "the hang was cut by the timeout" ok "took ${elapsed}s"

# 5. A broken current falls back to previous.
root=$(make_root fallback none)
ln -sfn versions/2.0.0 "$root/previous"
rm "$root/versions/1.0.0/bundle/client"
check "falls back to previous when current is not runnable" "app 2.0.0 args:" "$("$root/playheadtv" 2>/dev/null)"

# 6. The updater sees the root and the config path; the launcher works through a symlink.
root=$(make_root env 'echo "$PLAYHEADTV_ROOT|$PLAYHEADTV_CONFIG" > "$PLAYHEADTV_ROOT/seen"')
mkdir -p "$TMP/bin" && ln -s "$root/playheadtv" "$TMP/bin/playheadtv"
"$TMP/bin/playheadtv" >/dev/null
check "updater gets root and config path (also via a symlink)" "$root|$root/config.json" "$(cat "$root/seen")"

# ---- supervisor: a signal death is reported and restarted ----
# A root whose client crashes <crashes> times before it stays up, plus a fake
# notify-send that records what the user was told.
make_crasher() { # make_crasher <name> <crashes before it stays up>
    local root="$TMP/$1"
    mkdir -p "$root/versions/1.0.0/bundle" "$TMP/bin-$1"
    {
        echo '#!/bin/sh'
        echo "n=\$(cat \"$root/runs\" 2>/dev/null || echo 0); n=\$((n+1)); echo \$n >\"$root/runs\""
        echo "[ \$n -le $2 ] && kill -SEGV \$\$"
        echo 'echo "app up"'
    } >"$root/versions/1.0.0/bundle/client"
    chmod +x "$root/versions/1.0.0/bundle/client"
    ln -s versions/1.0.0 "$root/current"
    cp "$LAUNCHER" "$root/playheadtv"
    printf '#!/bin/sh\necho "$*" >>"%s/notified"\n' "$root" >"$TMP/bin-$1/notify-send"
    chmod +x "$TMP/bin-$1/notify-send"
    echo "$root"
}

root=$(make_crasher crash-once 1)
out=$(PATH="$TMP/bin-crash-once:$PATH" PLAYHEADTV_CRASH_DELAY=0 "$root/playheadtv" 2>"$TMP/crash-err")
check "supervisor: a crash is followed by a restart" "app up" "$out"
check "supervisor: the app ran twice" "2" "$(cat "$root/runs")"
check "supervisor: the user was told what happened" "yes" "$(grep -q 'signal 11' "$root/notified" && echo yes || echo no)"
check "supervisor: the crash is on stderr too" "yes" "$(grep -q 'killed by signal 11' "$TMP/crash-err" && echo yes || echo no)"

root=$(make_crasher crash-always 99)
PATH="$TMP/bin-crash-always:$PATH" PLAYHEADTV_CRASH_DELAY=0 PLAYHEADTV_CRASH_RESTARTS=2 \
    "$root/playheadtv" >/dev/null 2>"$TMP/giveup-err"
status=$?
check "supervisor: a crash that repeats stops at the restart budget" "2" "$(cat "$root/runs")"
check "supervisor: giving up returns the crash status" "139" "$status"
check "supervisor: giving up says so" "yes" "$(grep -q 'giving up after 2 crashes' "$TMP/giveup-err" && echo yes || echo no)"

root="$TMP/exit7"
mkdir -p "$root/versions/1.0.0/bundle"
printf '#!/bin/sh\nexit 7\n' >"$root/versions/1.0.0/bundle/client"
chmod +x "$root/versions/1.0.0/bundle/client"
ln -s versions/1.0.0 "$root/current"
cp "$LAUNCHER" "$root/playheadtv"
"$root/playheadtv" >/dev/null 2>&1
status=$?
check "supervisor: a plain non-zero exit is passed through, not restarted" "7" "$status"

# ---- the start does not wait for the network half of the update ----
# The updater is called twice: `preflight` before the app (local rollback only)
# and `prepare` in the background. The app must be running while the latter is
# still sleeping.
root=$(make_root bgnouveau 'case "$1" in
    prepare) sleep 2; date +%s%N >"$PLAYHEADTV_ROOT/upd-marker" ;;
    *) date +%s%N >"$PLAYHEADTV_ROOT/pre-marker" ;;
esac')
printf '#!/bin/sh\ndate +%%s%%N >"%s/app-marker"\necho "app started"\n' "$root" >"$root/versions/1.0.0/bundle/client"
chmod +x "$root/versions/1.0.0/bundle/client"
check "starts while the background update is still running" "app started" "$(PLAYHEADTV_UPDATE_TIMEOUT=30 "$root/playheadtv")"
check "the rollback half ran before the app" ok \
    "$([ -f "$root/pre-marker" ] && echo ok || echo missing)"
check "the app did not wait for the background update" ok \
    "$( [ "$(cat "$root/pre-marker")" -lt "$(cat "$root/app-marker")" ] && \
        [ "$(cat "$root/app-marker")" -lt "$(cat "$root/upd-marker")" ] && echo ok || echo out-of-order)"
check "the background update still completed" ok "$([ -f "$root/upd-marker" ] && echo ok || echo missing)"

# 7. install.sh lays out a package and a desktop entry, and is re-runnable.
pkg="$TMP/pkg/playheadtv-client-3.0.0-linux-x64"
mkdir -p "$pkg/bundle"
printf '#!/bin/sh\necho app 3.0.0\n' >"$pkg/bundle/client" && chmod +x "$pkg/bundle/client"
cp "$LAUNCHER" "$INSTALL" "$HERE/packaging/linux/playheadtv.desktop.in" "$HERE/packaging/linux/config.example.json" "$pkg/"
echo 3.0.0 >"$pkg/VERSION"
export XDG_DATA_HOME="$TMP/xdg"
"$pkg/install.sh" >/dev/null
iroot="$XDG_DATA_HOME/playheadtv"
check "install: current points at the version" "versions/3.0.0" "$(readlink "$iroot/current")"
check "install: launcher runs the app" "app 3.0.0" "$("$iroot/playheadtv")"
check "install: config created private" "600" "$(stat -c %a "$iroot/config.json")"
check "install: desktop entry points at the launcher" "Exec=\"$iroot/playheadtv\"" "$(grep '^Exec=' "$XDG_DATA_HOME/applications/playheadtv.desktop")"
echo 3.1.0 >"$pkg/VERSION"
"$pkg/install.sh" --no-desktop >/dev/null
check "reinstall: previous keeps the old version" "versions/3.0.0" "$(readlink "$iroot/previous")"
check "reinstall: current is the new version" "versions/3.1.0" "$(readlink "$iroot/current")"
check "reinstall keeps an edited config" "600" "$(stat -c %a "$iroot/config.json")"


# ---- AppImage entry point (packaging/appimage/AppRun) ----
APPRUN="$HERE/packaging/appimage/AppRun"

# An AppDir with a fake app, a fake updater (body given) and a fake "AppImage
# file" that stands for the file the runtime exports as $APPIMAGE.
make_appdir() { # make_appdir <name> <updater body or "none">
    local d="$TMP/$1"
    mkdir -p "$d/appdir/bundle" "$d/h"
    cp "$APPRUN" "$d/appdir/AppRun"
    chmod +x "$d/appdir/AppRun"
    printf '#!/bin/sh\necho "app from-appdir args:$*"\n' >"$d/appdir/bundle/client"
    chmod +x "$d/appdir/bundle/client"
    echo 1.0.0 >"$d/appdir/VERSION"
    echo '{}' >"$d/appdir/config.example.json"
    printf '#!/bin/sh\necho "app from-appimage-file reexec=$PLAYHEADTV_REEXEC args:$*"\n' >"$d/Fake.AppImage"
    chmod +x "$d/Fake.AppImage"
    if [ "$2" != none ]; then
        printf '#!/bin/sh\n%s\n' "$2" >"$d/appdir/bundle/playheadtv-updater"
        chmod +x "$d/appdir/bundle/playheadtv-updater"
    fi
    echo "$d"
}
run_apprun() { # run_apprun <dir> [args]: run with an isolated HOME and APPIMAGE set
    local d="$1"; shift
    env -i PATH="$PATH" HOME="$d/h" APPDIR="$d/appdir" APPIMAGE="$d/Fake.AppImage" "$d/appdir/AppRun" "$@"
}

d=$(make_appdir ar-plain 'echo "playheadtv-updater: upToDate: nothing to do"')
check "AppRun: starts the app from the AppDir when there is nothing to update" "app from-appdir args:a b" "$(run_apprun "$d" a b)"
check "AppRun: the update line goes to the log in the state dir" "2" "$(grep -c 'upToDate: nothing to do' "$d/h/.local/state/playheadtv/update.log")"
check "AppRun: config created from the example, private" "600" "$(stat -c %a "$d/h/.config/playheadtv/config.json")"

d=$(make_appdir ar-args 'echo "$*" >>"$HOME/updater-args"; echo "playheadtv-updater: upToDate: x"')
run_apprun "$d" >/dev/null
# Both halves get the same target; the arguments are identical on purpose (the
# network half is the same command with more time).
check "AppRun: both halves get the AppImage path, the running version and the state dir" \
    "preflight --appimage $d/Fake.AppImage --version 1.0.0 --state-dir $d/h/.local/state/playheadtv
prepare --appimage $d/Fake.AppImage --version 1.0.0 --state-dir $d/h/.local/state/playheadtv" "$(cat "$d/h/updater-args")"

# An update installed in the background belongs to the NEXT start: this launch
# keeps running the version it started.
d=$(make_appdir ar-updated 'echo "playheadtv-updater: updated: updated 1.0.0 -> 1.1.0"')
check "AppRun: an update found in the background does not restart this launch" "app from-appdir args:a" "$(run_apprun "$d" a)"

d=$(make_appdir ar-rolledback 'echo "playheadtv-updater: rolledBack: 1.1.0"')
check "AppRun: after a rollback it starts the restored file" "app from-appimage-file reexec=1 args:" "$(run_apprun "$d")"

d=$(make_appdir ar-reexec 'echo "playheadtv-updater: updated: again"')
check "AppRun: the re-executed instance does not run the updater again" "app from-appdir args:" \
    "$(env -i PATH="$PATH" HOME="$d/h" APPDIR="$d/appdir" APPIMAGE="$d/Fake.AppImage" PLAYHEADTV_REEXEC=1 "$d/appdir/AppRun")"

d=$(make_appdir ar-failing 'echo boom >&2; exit 7')
check "AppRun: starts when the updater fails" "app from-appdir args:" "$(run_apprun "$d")"

d=$(make_appdir ar-nofile 'echo "playheadtv-updater: rolledBack: 1.1.0"')
rm "$d/Fake.AppImage"
check "AppRun: starts the AppDir app when the rolled-back file cannot be run" "app from-appdir args:" "$(run_apprun "$d")"

# The app must not wait for the network half: here `prepare` sleeps for a while
# and the app has to have started before it finishes.
d=$(make_appdir ar-bg 'case "$1" in prepare) sleep 2 ;; esac; echo "playheadtv-updater: upToDate: x"')
start=$(date +%s)
out=$(run_apprun "$d")
check "AppRun: starts while the background update is still running" "app from-appdir args:" "$out"

# ---- supervisor in the AppImage entry point ----
d=$(make_appdir ar-crash 'echo "playheadtv-updater: upToDate: x"')
{
    echo '#!/bin/sh'
    echo "n=\$(cat \"$d/runs\" 2>/dev/null || echo 0); n=\$((n+1)); echo \$n >\"$d/runs\""
    echo '[ $n -le 1 ] && kill -SEGV $$'
    echo 'echo "app back"'
} >"$d/appdir/bundle/client"
chmod +x "$d/appdir/bundle/client"
mkdir -p "$d/bin"
printf '#!/bin/sh\necho "$*" >>"%s/notified"\n' "$d" >"$d/bin/notify-send"
chmod +x "$d/bin/notify-send"
out=$(env -i PATH="$d/bin:$PATH" HOME="$d/h" APPDIR="$d/appdir" PLAYHEADTV_CRASH_DELAY=0 "$d/appdir/AppRun" 2>/dev/null)
check "AppRun: a crash is followed by a restart" "app back" "$out"
check "AppRun: the user was told what happened" "yes" "$(grep -q 'signal 11' "$d/notified" && echo yes || echo no)"

# ---- release notes (tool/release_notes.sh) ----
NOTES="$HERE/tool/release_notes.sh"
repo="$TMP/notes-repo"
mkdir -p "$repo/client" "$repo/backend" "$repo/shared"
(
    cd "$repo" || exit 1
    git init -q . && git config user.email t@example.test && git config user.name tester && git config commit.gpgsign false && git config tag.gpgsign false
    echo a >client/a && git add . && git commit -q -m "feat(client): first client work" && git tag client-v1.0.0
    echo b >backend/b && git add . && git commit -q -m "feat(backend): backend only"
    echo c >client/c && git add . && git commit -q -m "fix(client): a client fix"
    echo d >shared/d && git add . && git commit -q -m "feat(shared): a contract change"
    git tag client-v1.1.0
    echo e >backend/e && git add . && git commit -q -m "fix(backend): another backend commit"
    git tag client-v1.2.0
)
out=$(cd "$repo" && "$NOTES" client-v1.1.0)
check "release notes: only client/shared commits since the previous client tag" \
    "$(printf 'Changes since 1.0.0:\n\n- feat(shared): a contract change\n- fix(client): a client fix')" "$out"
out=$(cd "$repo" && "$NOTES" client-v1.2.0)
check "release notes: no client commits says so" \
    "$(printf 'Changes since 1.1.0:\n\n- No changes to the client since the previous release.')" "$out"
out=$(cd "$repo" && "$NOTES" client-v1.0.0)
check "release notes: the first release lists everything it has" \
    "$(printf 'Changes:\n\n- feat(client): first client work')" "$out"
# The release workflow prints the notes inside a group redirected to a file and
# appends text after them: the notes must neither truncate nor overwrite it.
(
    cd "$repo" || exit 1
    {
        "$NOTES" client-v1.1.0
        echo
        echo "after the notes"
    } >"$TMP/body.txt"
)
check "release notes: printed inside a redirected group, kept intact" \
    "$(printf 'Changes since 1.0.0:\n\n- feat(shared): a contract change\n- fix(client): a client fix\n\nafter the notes')" "$(cat "$TMP/body.txt")"

[ "$FAILS" = 0 ] && echo "all launcher tests passed" || { echo "$FAILS launcher test(s) failed"; exit 1; }
