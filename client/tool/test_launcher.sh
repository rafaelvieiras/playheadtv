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
out=$(PLAYHEADTV_UPDATE_TIMEOUT=1 "$root/playheadtv")
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
check "AppRun: the update line goes to the log in the state dir" "playheadtv-updater: upToDate: nothing to do" "$(cat "$d/h/.local/state/playheadtv/update.log")"
check "AppRun: config created from the example, private" "600" "$(stat -c %a "$d/h/.config/playheadtv/config.json")"

d=$(make_appdir ar-args 'echo "$*" >"$HOME/updater-args"; echo "playheadtv-updater: upToDate: x"')
run_apprun "$d" >/dev/null
check "AppRun: the updater gets the AppImage path, the running version and the state dir" \
    "prepare --appimage $d/Fake.AppImage --version 1.0.0 --state-dir $d/h/.local/state/playheadtv" "$(cat "$d/h/updater-args")"

d=$(make_appdir ar-updated 'echo "playheadtv-updater: updated: updated 1.0.0 -> 1.1.0"')
check "AppRun: after an update it starts the new file, once" "app from-appimage-file reexec=1 args:a" "$(run_apprun "$d" a)"

d=$(make_appdir ar-rolledback 'echo "playheadtv-updater: upToDate: x (rolled back 1.1.0)"')
check "AppRun: after a rollback it starts the restored file" "app from-appimage-file reexec=1 args:" "$(run_apprun "$d")"

d=$(make_appdir ar-reexec 'echo "playheadtv-updater: updated: again"')
check "AppRun: the re-executed instance does not run the updater again" "app from-appdir args:" \
    "$(env -i PATH="$PATH" HOME="$d/h" APPDIR="$d/appdir" APPIMAGE="$d/Fake.AppImage" PLAYHEADTV_REEXEC=1 "$d/appdir/AppRun")"

d=$(make_appdir ar-failing 'echo boom >&2; exit 7')
check "AppRun: starts when the updater fails" "app from-appdir args:" "$(run_apprun "$d")"

d=$(make_appdir ar-nofile 'echo "playheadtv-updater: updated: x"')
rm "$d/Fake.AppImage"
check "AppRun: starts the AppDir app when the updated file cannot be run" "app from-appdir args:" "$(run_apprun "$d")"

d=$(make_appdir ar-hanging 'sleep 30')
start=$(date +%s)
out=$(env -i PATH="$PATH" HOME="$d/h" APPDIR="$d/appdir" APPIMAGE="$d/Fake.AppImage" PLAYHEADTV_UPDATE_TIMEOUT=1 "$d/appdir/AppRun")
elapsed=$(( $(date +%s) - start ))
check "AppRun: starts when the updater hangs" "app from-appdir args:" "$out"
[ "$elapsed" -lt 15 ] && check "AppRun: the hang was cut by the timeout" ok ok || check "AppRun: the hang was cut by the timeout" ok "took ${elapsed}s"

d=$(make_appdir ar-noappimage 'echo "playheadtv-updater: updated: x" >"$HOME/ran"')
env -i PATH="$PATH" HOME="$d/h" APPDIR="$d/appdir" "$d/appdir/AppRun" >/dev/null
check "AppRun: without \$APPIMAGE (extracted AppDir) the updater is not run" "no" "$([ -e "$d/h/ran" ] && echo yes || echo no)"

d=$(make_appdir ar-config 'true')
mkdir -p "$d/h/.config/playheadtv" && echo '{"x":1}' >"$d/h/.config/playheadtv/config.json"
run_apprun "$d" >/dev/null
check "AppRun: an existing config is kept" '{"x":1}' "$(cat "$d/h/.config/playheadtv/config.json")"

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

[ "$FAILS" = 0 ] && echo "all launcher tests passed" || { echo "$FAILS launcher test(s) failed"; exit 1; }
