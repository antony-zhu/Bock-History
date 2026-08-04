#!/usr/bin/env bash
set -euo pipefail

CURRENT_LINK=/opt/block/current

[ -L "$CURRENT_LINK" ] || {
  printf 'version: no current Block release\n' >&2
  exit 1
}
[ -f "$CURRENT_LINK/VERSION" ] || {
  printf 'version: current Block release has no VERSION file\n' >&2
  exit 1
}

cat "$CURRENT_LINK/VERSION"
