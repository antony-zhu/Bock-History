#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd -P)
OUTPUT_DIR=
VERSION=
PLC_PROFILE=${BLOCK_PLC_PROFILE:-default}

usage() {
  cat <<'EOF'
Usage:
  BLOCK_PLC_PROFILE=simulatorFloat32 deploy/block/build.sh --output <absolute-artifact-dir> --version <version>

Creates one immutable release artifact:
  bin/block-agent
  web/index.html
  web/assets/points.json and other HMI assets
  deploy/install.sh, rollback.sh, health-check.sh, units, and release helpers
  VERSION

The default profile contains the approved real PLC point table. Set
BLOCK_PLC_PROFILE=simulatorFloat32 only for the explicit legacy computer
simulator test artifact.
EOF
}

fail() {
  printf 'build: %s\n' "$*" >&2
  exit 1
}

copy_deploy_bundle() {
  local tool

  install -d -m 0755 "$OUTPUT_DIR/deploy/chromium" "$OUTPUT_DIR/deploy/config" "$OUTPUT_DIR/deploy/systemd" "$OUTPUT_DIR/deploy/tests"
  for tool in build.sh install-users.sh install.sh health-check.sh version.sh rollback.sh verify-install.sh verify-static.sh; do
    install -m 0755 "$SCRIPT_DIR/$tool" "$OUTPUT_DIR/deploy/$tool"
  done
  install -m 0644 "$SCRIPT_DIR/README.md" "$OUTPUT_DIR/deploy/README.md"
  install -m 0644 "$SCRIPT_DIR/chromium/block-kiosk.json" "$OUTPUT_DIR/deploy/chromium/block-kiosk.json"
  install -m 0644 "$SCRIPT_DIR/config/block.env.example" "$OUTPUT_DIR/deploy/config/block.env.example"
  install -m 0644 "$SCRIPT_DIR/systemd/block.service" "$OUTPUT_DIR/deploy/systemd/block.service"
  install -m 0644 "$SCRIPT_DIR/systemd/block-kiosk.service" "$OUTPUT_DIR/deploy/systemd/block-kiosk.service"
  install -m 0755 "$SCRIPT_DIR/tests/deploy-regression.sh" "$OUTPUT_DIR/deploy/tests/deploy-regression.sh"
  install -m 0755 "$SCRIPT_DIR/tests/install-rollback-regression.sh" "$OUTPUT_DIR/deploy/tests/install-rollback-regression.sh"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --output)
      [ "$#" -ge 2 ] || fail "--output needs a value"
      OUTPUT_DIR=$2
      shift 2
      ;;
    --version)
      [ "$#" -ge 2 ] || fail "--version needs a value"
      VERSION=$2
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

[ -n "$OUTPUT_DIR" ] || fail "--output is required"
[ -n "$VERSION" ] || fail "--version is required"
case "$OUTPUT_DIR" in
  /*) ;;
  *) fail "--output must be an absolute path" ;;
esac
case "$VERSION" in
  *[!A-Za-z0-9._-]*|'') fail "version may contain only letters, digits, dot, underscore, and dash" ;;
esac
[ ! -e "$OUTPUT_DIR" ] || fail "artifact directory already exists: $OUTPUT_DIR"

HMI_DIR=$REPO_ROOT/apps/block-hmi
[ -f "$HMI_DIR/index.html" ] || fail "missing HMI index.html"
[ -f "$HMI_DIR/assets/points.json" ] || fail "missing HMI points.json"
case "$PLC_PROFILE" in
  default)
    POINTS_SOURCE=$HMI_DIR/assets/points.json
    ;;
  simulatorFloat32)
    POINTS_SOURCE=$HMI_DIR/assets/points.simulatorFloat32.json
    ;;
  *)
    fail "unknown BLOCK_PLC_PROFILE: $PLC_PROFILE (allowed: default, simulatorFloat32)"
    ;;
esac
[ -f "$POINTS_SOURCE" ] || fail "missing PLC profile point table: $POINTS_SOURCE"
command -v go >/dev/null 2>&1 || fail "go is required"

install -d -m 0755 "$OUTPUT_DIR/bin" "$OUTPUT_DIR/web"
go -C "$REPO_ROOT/services/block-agent" build -trimpath -o "$OUTPUT_DIR/bin/block-agent" ./cmd/block-agent
install -m 0644 "$HMI_DIR/index.html" "$OUTPUT_DIR/web/index.html"
cp -a "$HMI_DIR/assets" "$OUTPUT_DIR/web/assets"
rm -f "$OUTPUT_DIR/web/assets/points.simulatorFloat32.json"
install -m 0644 "$POINTS_SOURCE" "$OUTPUT_DIR/web/assets/points.json"
copy_deploy_bundle
printf '%s\n' "$VERSION" > "$OUTPUT_DIR/VERSION"

printf 'built Block release artifact: %s\n' "$OUTPUT_DIR"
