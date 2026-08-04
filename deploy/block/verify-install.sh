#!/usr/bin/env bash
set -euo pipefail

EXPECTED_VERSION=
CURRENT_LINK=/opt/block/current
CONFIG_FILE=/etc/block/block.env

usage() {
  cat <<'EOF'
Usage:
  /opt/block/current/deploy/verify-install.sh [--expected-version <version>]
EOF
}

fail() {
  printf 'verify-install: %s\n' "$*" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --expected-version)
      [ "$#" -ge 2 ] || fail "--expected-version needs a value"
      EXPECTED_VERSION=$2
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

[ -L "$CURRENT_LINK" ] || fail "no current Block release"
[ -x "$CURRENT_LINK/bin/block-agent" ] || fail "missing Block binary"
[ -f "$CURRENT_LINK/VERSION" ] || fail "missing release VERSION"
[ -f "$CURRENT_LINK/web/index.html" ] || fail "missing HMI index.html"
[ -f "$CURRENT_LINK/web/assets/points.json" ] || fail "missing HMI points.json"
[ -f "$CONFIG_FILE" ] || fail "missing Block configuration"
if grep -Eq '^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*POINTS?[A-Za-z0-9_]*=' "$CONFIG_FILE"; then
  fail "Block configuration persists points"
fi

if [ -n "$EXPECTED_VERSION" ]; then
  ACTUAL_VERSION=$(cat "$CURRENT_LINK/VERSION")
  [ "$ACTUAL_VERSION" = "$EXPECTED_VERSION" ] || fail "current release version is $ACTUAL_VERSION, expected $EXPECTED_VERSION"
fi

systemctl is-active --quiet block.service || fail "block.service is not active"
if systemctl is-active --quiet block-hmi.service; then
  fail "legacy block-hmi.service is still active"
fi

"$CURRENT_LINK/deploy/health-check.sh" --expected-version "$EXPECTED_VERSION"
printf 'Block installation verification passed\n'
