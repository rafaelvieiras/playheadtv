#!/usr/bin/env bash
# Prints the changelog of a client release as plain text: the subjects of the
# commits since the previous `client-v*` tag that touched the client (client/ and
# shared/; the repository also holds the backend and the web UI, whose commits
# do not belong in a client changelog).
#
#   client/tool/release_notes.sh <rev> [output file]
#
# <rev> is the release tag (or any revision, e.g. HEAD for a dry run). The
# previous tag is the nearest `client-v*` tag reachable from <rev>'s parent.
# The text becomes the head of the GitHub Release body, and the client shows it
# once after an update as plain text, so it stays simple: no links, no markup
# beyond "- " bullets.
set -euo pipefail

REV="${1:?usage: release_notes.sh <rev> [output file]}"
OUT="${2:-}"

PREV="$(git describe --tags --abbrev=0 --match 'client-v[0-9]*' "$REV^" 2>/dev/null || true)"
if [ -n "$PREV" ]; then RANGE="$PREV..$REV"; else RANGE="$REV"; fi

COMMITS="$(git log --no-merges --format='- %s' "$RANGE" -- client shared | head -n 100)"
TEXT="$(
    if [ -n "$PREV" ]; then
        echo "Changes since ${PREV#client-v}:"
    else
        echo "Changes:"
    fi
    echo
    if [ -n "$COMMITS" ]; then
        printf '%s\n' "$COMMITS"
    else
        echo "- No changes to the client since the previous release."
    fi
)"
# Never reopen /dev/stdout: when the caller redirects a group into a file, that
# would truncate the file the caller is still writing to.
if [ -n "$OUT" ]; then printf '%s\n' "$TEXT" >"$OUT"; else printf '%s\n' "$TEXT"; fi
