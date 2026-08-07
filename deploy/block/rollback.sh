#!/usr/bin/env bash
set -euo pipefail

VERSION=
SNAPSHOT=
EXECUTE=false
PREFIX=/opt/block
RELEASES_ROOT=/opt/block/releases
CURRENT_LINK=/opt/block/current
STATE_ROOT=/var/lib/block-release
CONFIG_ROOT=/etc/block
CONFIG_FILE=/etc/block/block.env
SYSTEMD_ROOT=/etc/systemd/system
SNAPSHOTS_ROOT=/var/lib/block-release/unit-snapshots

usage() {
  cat <<'EOF'
Usage:
  sudo /opt/block/current/deploy/rollback.sh --execute [--version <version>]

Without --version, restores the release recorded before the most recent
install.  The target release's own systemd units and health check are used so
an HTTP-era release is not evaluated with TLS-only health-check arguments.
EOF
}

fail() {
  printf 'rollback: %s\n' "$*" >&2
  exit 1
}

config_value() {
  local wanted=$1 key value
  [ -f "$CONFIG_FILE" ] || return 1
  while IFS='=' read -r key value; do
    if [ "$key" = "$wanted" ]; then
      printf '%s\n' "$value"
      return 0
    fi
  done < "$CONFIG_FILE"
  return 1
}

resolve_release() {
  local candidate=$1 resolved
  [ -d "$candidate" ] || fail "release does not exist: $candidate"
  resolved=$(readlink -f "$candidate")
  case "$resolved" in
    "$RELEASES_ROOT"/*) ;;
    *) fail "release is outside $RELEASES_ROOT" ;;
  esac
  [ -x "$resolved/bin/block-agent" ] || fail "release has no Block binary"
  [ -f "$resolved/VERSION" ] || fail "release has no VERSION file"
  printf '%s\n' "$resolved"
}

set_current_release() {
  local release=$1 next_link
  next_link=$PREFIX/.rollback-$$
  ln -s "$release" "$next_link"
  mv -Tf "$next_link" "$CURRENT_LINK"
}

install_target_units() {
  local release=$1 units_dir
  units_dir=$release/deploy/systemd
  [ -f "$units_dir/block.service" ] || return 1
  [ -f "$units_dir/block-kiosk.service" ] || return 1
  install -d -m 0755 "$SYSTEMD_ROOT"
  install -m 0644 -o root -g root "$units_dir/block.service" "$SYSTEMD_ROOT/block.service"
  install -m 0644 -o root -g root "$units_dir/block-kiosk.service" "$SYSTEMD_ROOT/block-kiosk.service"
}

validate_snapshot() {
  case "$SNAPSHOT" in
    "$SNAPSHOTS_ROOT"/pre-*) ;;
    *) fail "snapshot is outside $SNAPSHOTS_ROOT" ;;
  esac
  [ -d "$SNAPSHOT" ] || fail "snapshot does not exist: $SNAPSHOT"
  [ -f "$SNAPSHOT/units.state" ] || fail "snapshot has no unit state"
  [ -f "$SNAPSHOT/current-link.state" ] || fail "snapshot has no current-link state"
}

restore_snapshot_units() {
  local unit state
  while IFS=$'\t' read -r unit state; do
    case "$unit" in
      block.service|block-kiosk.service) ;;
      *) fail "snapshot has an unexpected unit: $unit" ;;
    esac
    case "$state" in
      present)
        [ -f "$SNAPSHOT/units/$unit" ] || fail "snapshot is missing $unit"
        install -d -m 0755 "$SYSTEMD_ROOT"
        cp -a -- "$SNAPSHOT/units/$unit" "$SYSTEMD_ROOT/$unit"
        ;;
      absent) rm -f -- "$SYSTEMD_ROOT/$unit" ;;
      *) fail "snapshot has invalid state for $unit" ;;
    esac
  done < "$SNAPSHOT/units.state"
}

restore_snapshot_file() {
  local name=$1 destination=$2 state
  state=$(cat "$SNAPSHOT/state/$name.state")
  case "$state" in
    present)
      [ -f "$SNAPSHOT/state/$name" ] || fail "snapshot is missing $name"
      install -d -m 0750 "$(dirname "$destination")"
      cp -a -- "$SNAPSHOT/state/$name" "$destination"
      ;;
    absent) rm -f -- "$destination" ;;
    *) fail "snapshot has invalid state for $name" ;;
  esac
}

restore_snapshot_current_link() {
  local state target
  state=$(cat "$SNAPSHOT/current-link.state")
  case "$state" in
    present)
      [ -s "$SNAPSHOT/current-link" ] || fail "snapshot is missing current-link"
      target=$(cat "$SNAPSHOT/current-link")
      ln -s "$target" "$PREFIX/.rollback-restore-$$"
      mv -Tf "$PREFIX/.rollback-restore-$$" "$CURRENT_LINK"
      ;;
    absent) rm -f -- "$CURRENT_LINK" ;;
    *) fail "snapshot has invalid current-link state" ;;
  esac
}

release_health_check() {
  local release=$1 health_check ca
  health_check=$release/deploy/health-check.sh
  [ -x "$health_check" ] || fail "release has no executable health check: $health_check"

  if "$health_check" --help 2>&1 | grep -F -- '--ca-file' >/dev/null; then
    ca=$(config_value BLOCK_LOCAL_TLS_CA) || fail "missing BLOCK_LOCAL_TLS_CA for TLS health check"
    [ -n "$ca" ] || fail "BLOCK_LOCAL_TLS_CA must not be empty"
    "$health_check" --ca-file "$ca"
  else
    "$health_check"
  fi
}

snapshot_records_release() {
  local snapshot=$1 release=$2 recorded
  [ -f "$snapshot/current-release" ] || return 1
  recorded=$(cat "$snapshot/current-release")
  [ "$recorded" = "$release" ]
}

snapshot_for_release() {
  local release=$1 candidate
  if [ -s "$STATE_ROOT/current-unit-snapshot" ]; then
    candidate=$(cat "$STATE_ROOT/current-unit-snapshot")
    if [ -d "$candidate" ] && snapshot_records_release "$candidate" "$release"; then
      printf '%s\n' "$candidate"
      return 0
    fi
  fi
  for candidate in "$SNAPSHOTS_ROOT"/pre-*; do
    [ -d "$candidate" ] || continue
    if snapshot_records_release "$candidate" "$release"; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

rollback_snapshot() {
  local restored_release
  validate_snapshot

  systemctl stop block-kiosk.service >/dev/null 2>&1 || true
  systemctl stop block.service >/dev/null 2>&1 || true
  restore_snapshot_file block.env "$CONFIG_FILE"
  restore_snapshot_units
  systemctl daemon-reload
  restore_snapshot_current_link
  restore_snapshot_file previous-release "$STATE_ROOT/previous-release"
  restore_snapshot_file current-version "$STATE_ROOT/current-version"
  restore_snapshot_file current-unit-snapshot "$STATE_ROOT/current-unit-snapshot"

  if [ -L "$CURRENT_LINK" ]; then
    restored_release=$(resolve_release "$CURRENT_LINK")
    systemctl restart block.service
    release_health_check "$restored_release"
    systemctl restart block-kiosk.service
  fi
  printf 'rolled back Block install snapshot %s\n' "$SNAPSHOT"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || fail "--version needs a value"
      VERSION=$2
      shift 2
      ;;
    --snapshot)
      [ "$#" -ge 2 ] || fail "--snapshot needs a value"
      SNAPSHOT=$2
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

if [ -n "$SNAPSHOT" ]; then
  [ -z "$VERSION" ] || fail "--snapshot cannot be combined with --version"
  rollback_snapshot
  exit 0
fi

if [ -z "$VERSION" ]; then
  [ -s "$STATE_ROOT/previous-release" ] || fail "no previous release is recorded"
  RELEASE_DIR=$(cat "$STATE_ROOT/previous-release")
else
  case "$VERSION" in
    *[!A-Za-z0-9._-]*|'') fail "invalid version" ;;
  esac
  RELEASE_DIR=$RELEASES_ROOT/$VERSION
fi
TARGET_RELEASE=$(resolve_release "$RELEASE_DIR")

if [ -L "$CURRENT_LINK" ]; then
  CURRENT_RELEASE=$(resolve_release "$CURRENT_LINK")
else
  CURRENT_RELEASE=
fi

TARGET_SNAPSHOT=$(snapshot_for_release "$TARGET_RELEASE" || true)
if [ -n "$TARGET_SNAPSHOT" ]; then
  SNAPSHOT=$TARGET_SNAPSHOT
  validate_snapshot
fi

systemctl stop block-kiosk.service >/dev/null 2>&1 || true
systemctl stop block.service >/dev/null 2>&1 || true
if [ -n "$TARGET_SNAPSHOT" ]; then
  restore_snapshot_file block.env "$CONFIG_FILE"
fi
if ! install_target_units "$TARGET_RELEASE"; then
  [ -n "$TARGET_SNAPSHOT" ] || fail "target release has no systemd units and no install snapshot"
  restore_snapshot_units
fi
systemctl daemon-reload
set_current_release "$TARGET_RELEASE"
systemctl restart block.service
release_health_check "$TARGET_RELEASE"
systemctl restart block-kiosk.service

if [ -n "$CURRENT_RELEASE" ]; then
  printf '%s\n' "$CURRENT_RELEASE" > "$STATE_ROOT/previous-release"
fi
cat "$TARGET_RELEASE/VERSION" > "$STATE_ROOT/current-version"
printf 'rolled back to Block release %s\n' "$(cat "$TARGET_RELEASE/VERSION")"
