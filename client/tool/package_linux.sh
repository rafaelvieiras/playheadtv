#!/usr/bin/env bash
# Builds the Linux x86_64 client release package.
#
#   client/tool/package_linux.sh <version> [output dir]
#
# Produces, in the output dir (default client/dist):
#   playheadtv-client-<version>-linux-x64.tar.gz   the package
#   playheadtv-client-<version>-checksums.txt      SHA-256 (sha256sum format)
#
# The package holds one top directory with the Flutter bundle (plus the
# headless updater), the launcher, install.sh, the desktop entry template and
# an example config. It is the same script CI runs (.github/workflows/
# client-release.yml); docs/RELEASING.md describes the release flow.
#
# Environment: FLUTTER overrides the flutter command (default: "fvm flutter"
# when fvm is installed, else "flutter"); DART likewise ("fvm dart" / "dart");
# SKIP_BUILD=1 reuses an existing build/linux/x64/release/bundle (used to test
# the packaging alone).
set -euo pipefail

VERSION="${1:?usage: package_linux.sh <version> [output dir]}"
VERSION="${VERSION#client-v}"
VERSION="${VERSION#v}"
if ! printf '%s' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "package_linux.sh: version must be MAJOR.MINOR.PATCH, got '$VERSION'" >&2
    exit 2
fi

CLIENT_DIR="$(cd "$(dirname "$0")/.." && pwd -P)"
OUT_DIR="$(mkdir -p "${2:-$CLIENT_DIR/dist}" && cd "${2:-$CLIENT_DIR/dist}" && pwd -P)"
cd "$CLIENT_DIR"

if [ -z "${FLUTTER:-}" ]; then
    if command -v fvm >/dev/null 2>&1 && [ -f .fvmrc ]; then FLUTTER="fvm flutter"; else FLUTTER="flutter"; fi
fi
if [ -z "${DART:-}" ]; then
    if [ "$FLUTTER" = "fvm flutter" ]; then DART="fvm dart"; else DART="dart"; fi
fi

BUNDLE="$CLIENT_DIR/build/linux/x64/release/bundle"
if [ "${SKIP_BUILD:-0}" != 1 ]; then
    $FLUTTER pub get
    $FLUTTER build linux --release --dart-define=PLAYHEADTV_CLIENT_VERSION="$VERSION"
    mkdir -p build
    $DART compile exe tool/updater.dart -o build/playheadtv-updater
fi
[ -x "$BUNDLE/client" ] || { echo "package_linux.sh: $BUNDLE/client not found" >&2; exit 1; }
[ -x build/playheadtv-updater ] || { echo "package_linux.sh: build/playheadtv-updater not found" >&2; exit 1; }

NAME="playheadtv-client-$VERSION-linux-x64"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
TOP="$STAGE/$NAME"
mkdir -p "$TOP"
cp -a "$BUNDLE" "$TOP/bundle"
cp build/playheadtv-updater "$TOP/bundle/playheadtv-updater"
cp packaging/linux/playheadtv packaging/linux/install.sh packaging/linux/playheadtv.desktop.in \
    packaging/linux/config.example.json "$TOP/"
chmod 755 "$TOP/playheadtv" "$TOP/install.sh" "$TOP/bundle/client" "$TOP/bundle/playheadtv-updater"
printf '%s\n' "$VERSION" >"$TOP/VERSION"

ARCHIVE="$OUT_DIR/$NAME.tar.gz"
CHECKSUMS="$OUT_DIR/playheadtv-client-$VERSION-checksums.txt"
# Stable ordering and ownership; gzip -n drops the timestamp. The file mtimes
# are pinned to SOURCE_DATE_EPOCH when set (CI sets it to the commit time).
TAR_OPTS=(--sort=name --owner=0 --group=0 --numeric-owner)
if [ -n "${SOURCE_DATE_EPOCH:-}" ]; then TAR_OPTS+=("--mtime=@$SOURCE_DATE_EPOCH"); fi
tar "${TAR_OPTS[@]}" -cf - -C "$STAGE" "$NAME" | gzip -n -9 >"$ARCHIVE"

(cd "$OUT_DIR" && sha256sum "$NAME.tar.gz" >"$CHECKSUMS")
echo "wrote $ARCHIVE"
echo "wrote $CHECKSUMS"
cat "$CHECKSUMS"
