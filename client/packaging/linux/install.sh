#!/bin/sh
# Installs this Playhead TV client package for the current user, no root needed.
#
#   ./install.sh [--root DIR] [--no-desktop]
#
# Everything goes under ONE user-writable directory (default
# ${XDG_DATA_HOME:-$HOME/.local/share}/playheadtv): the launcher, config.json,
# and the versions the updater manages. Re-running it installs this package as
# the current version (the previous one is kept for rollback).
set -eu

HERE=$(cd "$(dirname "$0")" && pwd -P)
ROOT="${XDG_DATA_HOME:-$HOME/.local/share}/playheadtv"
DESKTOP=1
while [ $# -gt 0 ]; do
    case "$1" in
        --root) ROOT="$2"; shift 2 ;;
        --no-desktop) DESKTOP=0; shift ;;
        -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
        *) echo "install.sh: unknown option $1" >&2; exit 2 ;;
    esac
done

VERSION=$(cat "$HERE/VERSION")
[ -x "$HERE/bundle/client" ] || { echo "install.sh: bundle/client not found next to this script" >&2; exit 1; }

mkdir -p "$ROOT/versions"
DEST="$ROOT/versions/$VERSION"
if [ -d "$DEST" ]; then rm -rf "$DEST"; fi
cp -a "$HERE" "$DEST"
rm -f "$DEST/install.sh"

# previous := whatever current was, current := this version (atomic renames).
if [ -L "$ROOT/current" ]; then
    OLD=$(readlink "$ROOT/current")
    if [ "$OLD" != "versions/$VERSION" ]; then
        ln -sfn "$OLD" "$ROOT/previous.tmp" && mv -T "$ROOT/previous.tmp" "$ROOT/previous"
    fi
fi
ln -sfn "versions/$VERSION" "$ROOT/current.tmp"
mv -T "$ROOT/current.tmp" "$ROOT/current"

# The launcher is shared by every version and lives at the root.
cp "$HERE/playheadtv" "$ROOT/playheadtv"
chmod 755 "$ROOT/playheadtv"

if [ ! -f "$ROOT/config.json" ]; then
    cp "$HERE/config.example.json" "$ROOT/config.json"
    chmod 600 "$ROOT/config.json" # it may hold a device token
    echo "Created $ROOT/config.json: edit it to set your backend URL and device token."
fi

if [ "$DESKTOP" = 1 ]; then
    APPS="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
    mkdir -p "$APPS"
    sed "s|@ROOT@|$ROOT|g" "$HERE/playheadtv.desktop.in" >"$APPS/playheadtv.desktop"
    echo "Desktop entry: $APPS/playheadtv.desktop"
fi

echo "Installed Playhead TV client $VERSION in $ROOT"
echo "Start it with: $ROOT/playheadtv"
