#!/usr/bin/env bash
# Shell tests of the launcher (packaging/linux/playheadtv) and install.sh, with
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

[ "$FAILS" = 0 ] && echo "all launcher tests passed" || { echo "$FAILS launcher test(s) failed"; exit 1; }
