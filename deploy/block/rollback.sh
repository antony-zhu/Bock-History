#!/usr/bin/env bash
set -euo pipefail

VERSION=
EXECUTE=false
PREFIX=/opt/block
RELEASES_ROOT=/opt/block/releases
CURRENT_LINK=/opt/block/current
STATE_ROOT=/var/lib/block-release

usage() {
  cat <<'EOF'
Usage:
  sudo /opt/block/current/deploy/rollback.sh --execute [--version <version>]

Without --version, restores the release recorded before the most recent
install.  Configuration and SQLite data are intentionally left in place.
EOF
}

fail() {
  printf 'rollback: %s\n' "$*" >&2
  exit 1
}

config_value() {
  local wanted=$1 key value
  while IFS='=' read -r key value; do
    if [ "$key" = "$wanted" ]; then
      printf '%s\n' "$value"
      return 0
    fi
  done < /etc/block/block.env
  return 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || fail "--version needs a value"
      VERSION=$2
      shift 2
      ;;
    --execute)
      EXECUTE=true
      shift
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

[ "$EXECUTE" = true ] || fail "pass --execute to change a device"
[ "$(id -u)" -eq 0 ] || fail "must run as root"
command -v systemctl >/dev/null 2>&1 || fail "systemctl is required"
BLOCK_LOCAL_TLS_CA=$(config_value BLOCK_LOCAL_TLS_CA) || fail "missing BLOCK_LOCAL_TLS_CA in /etc/block/block.env"
[ -n "$BLOCK_LOCAL_TLS_CA" ] || fail "BLOCK_LOCAL_TLS_CA must not be empty"

if [ -z "$VERSION" ]; then
  [ -s "$STATE_ROOT/previous-release" ] || fail "no previous release is recorded"
  RELEASE_DIR=$(cat "$STATE_ROOT/previous-release")
else
  case "$VERSION" in
    *[!A-Za-z0-9._-]*|'') fail "invalid version" ;;
  esac
  RELEASE_DIR=$RELEASES_ROOT/$VERSION
fi

[ -d "$RELEASE_DIR" ] || fail "release does not exist: $RELEASE_DIR"
RESOLVED_RELEASE=$(readlink -f "$RELEASE_DIR")
case "$RESOLVED_RELEASE" in
  "$RELEASES_ROOT"/*) ;;
  *) fail "release is outside $RELEASES_ROOT" ;;
esac
[ -x "$RESOLVED_RELEASE/bin/block-agent" ] || fail "release has no Block binary"
[ -f "$RESOLVED_RELEASE/VERSION" ] || fail "release has no VERSION file"

if [ -L "$CURRENT_LINK" ]; then
  CURRENT_RELEASE=$(readlink -f "$CURRENT_LINK")
  case "$CURRENT_RELEASE" in
    "$RELEASES_ROOT"/*) ;;
    *) fail "current release is outside $RELEASES_ROOT" ;;
  esac
else
  CURRENT_RELEASE=
fi

ROLLBACK_LINK=$PREFIX/.rollback-$$
ln -s "$RESOLVED_RELEASE" "$ROLLBACK_LINK"
mv -Tf "$ROLLBACK_LINK" "$CURRENT_LINK"

systemctl daemon-reload
systemctl restart block.service
"$CURRENT_LINK/deploy/health-check.sh" --ca-file "$BLOCK_LOCAL_TLS_CA" --expected-version "$(cat "$RESOLVED_RELEASE/VERSION")" --retries 30 --delay 1
systemctl restart block-kiosk.service

if [ -n "$CURRENT_RELEASE" ]; then
  printf '%s\n' "$CURRENT_RELEASE" > "$STATE_ROOT/previous-release"
fi
cat "$RESOLVED_RELEASE/VERSION" > "$STATE_ROOT/current-version"
printf 'rolled back to Block release %s\n' "$(cat "$RESOLVED_RELEASE/VERSION")"
