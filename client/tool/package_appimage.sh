#!/usr/bin/env bash
# Builds the Linux x86_64 client AppImage from the SAME Flutter bundle as the
# tar.gz package (run client/tool/package_linux.sh first: it builds the bundle
# and the updater, and writes the checksums file this script appends to).
#
#   client/tool/package_appimage.sh <version> [output dir]
#
# Produces, in the output dir (default client/dist):
#   playheadtv-client-<version>-linux-x64.AppImage
# and adds its SHA-256 to playheadtv-client-<version>-checksums.txt, so the
# AppImage is covered by the release signature like every other artifact.
#
# How it is assembled, and why no appimagetool: an AppImage is the AppImage
# runtime (an ELF executable) with a squashfs image appended. This script
# builds the squashfs with mksquashfs (squashfs-tools) and concatenates it
# behind the PINNED static type2 runtime. The runtime embeds its own FUSE and
# squashfs support, so the host needs no libfuse2 (it still needs the kernel's
# FUSE and a fusermount3/fusermount helper to mount; without them an AppImage
# can be run with --appimage-extract-and-run). The runtime is downloaded from
# its versioned release and its SHA-256 is verified before use; bump
# RUNTIME_VERSION and RUNTIME_SHA256 together.
#
# Environment: SOURCE_DATE_EPOCH pins file times (CI sets it to the commit
# time); RUNTIME_FILE uses an already downloaded runtime (still checksum
# verified); APPIMAGE_CACHE is where the runtime is cached (default
# client/build/appimage-cache).
set -euo pipefail

RUNTIME_VERSION="20251108"
RUNTIME_SHA256="2fca8b443c92510f1483a883f60061ad09b46b978b2631c807cd873a47ec260d"
RUNTIME_URL="https://github.com/AppImage/type2-runtime/releases/download/$RUNTIME_VERSION/runtime-x86_64"

VERSION="${1:?usage: package_appimage.sh <version> [output dir]}"
VERSION="${VERSION#client-v}"
VERSION="${VERSION#v}"
if ! printf '%s' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "package_appimage.sh: version must be MAJOR.MINOR.PATCH, got '$VERSION'" >&2
    exit 2
fi

CLIENT_DIR="$(cd "$(dirname "$0")/.." && pwd -P)"
OUT_DIR="$(mkdir -p "${2:-$CLIENT_DIR/dist}" && cd "${2:-$CLIENT_DIR/dist}" && pwd -P)"
cd "$CLIENT_DIR"

BUNDLE="$CLIENT_DIR/build/linux/x64/release/bundle"
[ -x "$BUNDLE/client" ] || { echo "package_appimage.sh: $BUNDLE/client not found (run package_linux.sh first)" >&2; exit 1; }
[ -x build/playheadtv-updater ] || { echo "package_appimage.sh: build/playheadtv-updater not found (run package_linux.sh first)" >&2; exit 1; }
command -v mksquashfs >/dev/null 2>&1 || { echo "package_appimage.sh: mksquashfs not found (install squashfs-tools)" >&2; exit 1; }

# The pinned runtime, verified before it is ever executed or shipped.
CACHE="${APPIMAGE_CACHE:-$CLIENT_DIR/build/appimage-cache}"
mkdir -p "$CACHE"
RUNTIME="${RUNTIME_FILE:-$CACHE/runtime-x86_64-$RUNTIME_VERSION}"
if [ ! -f "$RUNTIME" ]; then
    curl -fsSL --retry 3 -o "$RUNTIME.part" "$RUNTIME_URL"
    mv "$RUNTIME.part" "$RUNTIME"
fi
if ! printf '%s  %s\n' "$RUNTIME_SHA256" "$RUNTIME" | sha256sum -c --status -; then
    echo "package_appimage.sh: the AppImage runtime does not match the pinned SHA-256 ($RUNTIME_SHA256)" >&2
    exit 1
fi

NAME="playheadtv-client-$VERSION-linux-x64.AppImage"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
APPDIR="$STAGE/AppDir"
mkdir -p "$APPDIR"
cp -a "$BUNDLE" "$APPDIR/bundle"
cp build/playheadtv-updater "$APPDIR/bundle/playheadtv-updater"
cp packaging/appimage/AppRun "$APPDIR/AppRun"
cp packaging/appimage/playheadtv.desktop "$APPDIR/playheadtv.desktop"
cp packaging/linux/playheadtv.png "$APPDIR/playheadtv.png"
cp packaging/linux/config.example.json "$APPDIR/config.example.json"
ln -s playheadtv.png "$APPDIR/.DirIcon"
chmod 755 "$APPDIR/AppRun" "$APPDIR/bundle/client" "$APPDIR/bundle/playheadtv-updater"
printf '%s\n' "$VERSION" >"$APPDIR/VERSION"

# mksquashfs reads SOURCE_DATE_EPOCH itself (file and image times).
SQUASH_OPTS=(-root-owned -noappend -no-xattrs -comp zstd -Xcompression-level 19 -quiet)
mksquashfs "$APPDIR" "$STAGE/appimage.squashfs" "${SQUASH_OPTS[@]}"

OUT="$OUT_DIR/$NAME"
cat "$RUNTIME" "$STAGE/appimage.squashfs" >"$OUT"
chmod 755 "$OUT"

CHECKSUMS="$OUT_DIR/playheadtv-client-$VERSION-checksums.txt"
touch "$CHECKSUMS"
# Idempotent: drop an older line for this file, then append the current one.
grep -v "  $NAME\$" "$CHECKSUMS" >"$CHECKSUMS.tmp" || true
(cd "$OUT_DIR" && sha256sum "$NAME") >>"$CHECKSUMS.tmp"
mv "$CHECKSUMS.tmp" "$CHECKSUMS"

echo "wrote $OUT"
echo "updated $CHECKSUMS"
cat "$CHECKSUMS"
