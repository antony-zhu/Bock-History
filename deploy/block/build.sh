#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd -P)
OUTPUT_DIR=
VERSION=

usage() {
  cat <<'EOF'
Usage:
  deploy/block/build.sh --output <absolute-artifact-dir> --version <version>

Creates one immutable release artifact:
  bin/block-agent
  web/index.html
  web/assets/points.json and other HMI assets
  VERSION
EOF
}

fail() {
  printf 'build: %s\n' "$*" >&2
  exit 1
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
command -v go >/dev/null 2>&1 || fail "go is required"

install -d -m 0755 "$OUTPUT_DIR/bin" "$OUTPUT_DIR/web"
go -C "$REPO_ROOT/services/block-agent" build -trimpath -o "$OUTPUT_DIR/bin/block-agent" ./cmd/block-agent
install -m 0644 "$HMI_DIR/index.html" "$OUTPUT_DIR/web/index.html"
cp -a "$HMI_DIR/assets" "$OUTPUT_DIR/web/assets"
printf '%s\n' "$VERSION" > "$OUTPUT_DIR/VERSION"

printf 'built Block release artifact: %s\n' "$OUTPUT_DIR"
