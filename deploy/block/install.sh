#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
ARTIFACT_DIR=
CONFIG_FILE=
VERSION=
EXECUTE=false

PREFIX=/opt/block
RELEASES_ROOT=/opt/block/releases
CURRENT_LINK=/opt/block/current
STATE_ROOT=/var/lib/block-release
CONFIG_ROOT=/etc/block
SYSTEMD_ROOT=/etc/systemd/system

usage() {
  cat <<'EOF'
Usage:
  sudo deploy/block/install.sh --execute --artifact-dir <artifact-dir>
      --config <block.env> --version <version>

The artifact must contain bin/block-agent, web/index.html,
web/assets/points.json, and VERSION.  This script changes only local release
paths and systemd units.  It does not configure Wi-Fi, BDM, or PLC points.
EOF
}

fail() {
  printf 'install: %s\n' "$*" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --artifact-dir)
      [ "$#" -ge 2 ] || fail "--artifact-dir needs a value"
      ARTIFACT_DIR=$2
      shift 2
      ;;
    --config)
      [ "$#" -ge 2 ] || fail "--config needs a value"
      CONFIG_FILE=$2
      shift 2
      ;;
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
[ -n "$ARTIFACT_DIR" ] || fail "--artifact-dir is required"
[ -n "$CONFIG_FILE" ] || fail "--config is required"
[ -n "$VERSION" ] || fail "--version is required"
case "$ARTIFACT_DIR" in
  /*) ;;
  *) fail "--artifact-dir must be an absolute path" ;;
esac
case "$CONFIG_FILE" in
  /*) ;;
  *) fail "--config must be an absolute path" ;;
esac
case "$VERSION" in
  *[!A-Za-z0-9._-]*|'') fail "version may contain only letters, digits, dot, underscore, and dash" ;;
esac

[ -x "$ARTIFACT_DIR/bin/block-agent" ] || fail "missing artifact binary"
[ -f "$ARTIFACT_DIR/web/index.html" ] || fail "missing artifact index.html"
[ -f "$ARTIFACT_DIR/web/assets/points.json" ] || fail "missing artifact points.json"
[ -f "$ARTIFACT_DIR/VERSION" ] || fail "missing artifact VERSION"
[ -f "$CONFIG_FILE" ] || fail "missing config file"
[ "$(cat "$ARTIFACT_DIR/VERSION")" = "$VERSION" ] || fail "artifact VERSION does not match --version"
if grep -Eq '^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*_)?POINTS?(_[A-Za-z0-9_]+)?=' "$CONFIG_FILE"; then
  fail "Block configuration must not persist points"
fi
if grep -Fx 'BLOCK_MAINTENANCE_HTTPS_ADDRESS=127.0.0.1:8443' "$CONFIG_FILE" >/dev/null; then
  sed -i 's/^BLOCK_MAINTENANCE_HTTPS_ADDRESS=127\.0\.0\.1:8443$/BLOCK_MAINTENANCE_HTTPS_ADDRESS=0.0.0.0:8443/' "$CONFIG_FILE"
fi
command -v systemctl >/dev/null 2>&1 || fail "systemctl is required"

"$SCRIPT_DIR/install-users.sh"

install -d -m 0755 "$PREFIX"
install -d -o block -g block -m 0755 "$RELEASES_ROOT"
install -d -o block -g block -m 0750 /var/lib/block
install -d -o root -g block -m 0750 "$CONFIG_ROOT"
install -d -o root -g root -m 0755 "$SYSTEMD_ROOT"
install -d -o root -g root -m 0750 "$STATE_ROOT"

RELEASE_DIR=$RELEASES_ROOT/$VERSION
[ ! -e "$RELEASE_DIR" ] || fail "release already exists: $RELEASE_DIR"
install -d -o block -g block -m 0755 "$RELEASE_DIR/bin" "$RELEASE_DIR/web"
install -m 0755 -o block -g block "$ARTIFACT_DIR/bin/block-agent" "$RELEASE_DIR/bin/block-agent"
install -m 0644 -o block -g block "$ARTIFACT_DIR/VERSION" "$RELEASE_DIR/VERSION"
cp -a "$ARTIFACT_DIR/web/." "$RELEASE_DIR/web/"
chown -R block:block "$RELEASE_DIR/web"

install -d -o root -g root -m 0755 "$RELEASE_DIR/deploy/config" "$RELEASE_DIR/deploy/systemd" "$RELEASE_DIR/deploy/tests"
for TOOL in build.sh install-users.sh install.sh health-check.sh version.sh rollback.sh verify-install.sh verify-static.sh; do
  install -m 0755 -o root -g root "$SCRIPT_DIR/$TOOL" "$RELEASE_DIR/deploy/$TOOL"
done
install -m 0644 -o root -g root "$SCRIPT_DIR/README.md" "$RELEASE_DIR/deploy/README.md"
install -m 0644 -o root -g root "$SCRIPT_DIR/config/block.env.example" "$RELEASE_DIR/deploy/config/block.env.example"
install -m 0644 -o root -g root "$SCRIPT_DIR/systemd/block.service" "$RELEASE_DIR/deploy/systemd/block.service"
install -m 0644 -o root -g root "$SCRIPT_DIR/systemd/block-kiosk.service" "$RELEASE_DIR/deploy/systemd/block-kiosk.service"
install -m 0755 -o root -g root "$SCRIPT_DIR/tests/deploy-regression.sh" "$RELEASE_DIR/deploy/tests/deploy-regression.sh"

install -m 0640 -o root -g block "$CONFIG_FILE" "$CONFIG_ROOT/block.env"
install -m 0644 -o root -g root "$SCRIPT_DIR/systemd/block.service" "$SYSTEMD_ROOT/block.service"
install -m 0644 -o root -g root "$SCRIPT_DIR/systemd/block-kiosk.service" "$SYSTEMD_ROOT/block-kiosk.service"

if [ -L "$CURRENT_LINK" ]; then
  PREVIOUS_RELEASE=$(readlink -f "$CURRENT_LINK")
  case "$PREVIOUS_RELEASE" in
    "$RELEASES_ROOT"/*) printf '%s\n' "$PREVIOUS_RELEASE" > "$STATE_ROOT/previous-release" ;;
    *) fail "current release is outside $RELEASES_ROOT" ;;
  esac
else
  : > "$STATE_ROOT/previous-release"
fi

NEXT_LINK=$PREFIX/.current-next-$$
ln -s "$RELEASE_DIR" "$NEXT_LINK"
mv -Tf "$NEXT_LINK" "$CURRENT_LINK"

for LEGACY_UNIT in block-agent.service block-hmi.service block-plc-simulator.service; do
  systemctl disable --now "$LEGACY_UNIT" >/dev/null 2>&1 || true
  rm -f "$SYSTEMD_ROOT/$LEGACY_UNIT"
done

systemctl daemon-reload
systemctl enable --now block.service
"$CURRENT_LINK/deploy/health-check.sh" --expected-version "$VERSION" --retries 30 --delay 1
systemctl enable --now block-kiosk.service
printf '%s\n' "$VERSION" > "$STATE_ROOT/current-version"

printf 'installed Block release %s\n' "$VERSION"
