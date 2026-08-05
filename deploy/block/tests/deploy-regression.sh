#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
DEPLOY_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd -P)

fail() {
  printf 'deploy-regression: %s\n' "$*" >&2
  exit 1
}

"$DEPLOY_DIR/verify-static.sh"

"$DEPLOY_DIR/build.sh" --help >/dev/null
"$DEPLOY_DIR/install.sh" --help >/dev/null
"$DEPLOY_DIR/rollback.sh" --help >/dev/null
"$DEPLOY_DIR/verify-install.sh" --help >/dev/null
"$DEPLOY_DIR/health-check.sh" --help >/dev/null

if "$DEPLOY_DIR/install.sh" --artifact-dir /tmp/block-artifact --config /tmp/block.env --version test >/dev/null 2>&1; then
  fail "install must require --execute"
fi
if "$DEPLOY_DIR/rollback.sh" >/dev/null 2>&1; then
  fail "rollback must require --execute"
fi
if "$DEPLOY_DIR/health-check.sh" --retries 0 >/dev/null 2>&1; then
  fail "health check accepted zero retries"
fi

TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT
ARTIFACT_DIR=$TEST_ROOT/artifact
mkdir -p "$ARTIFACT_DIR/bin" "$ARTIFACT_DIR/web/assets"
touch "$ARTIFACT_DIR/web/index.html" "$ARTIFACT_DIR/web/assets/points.json"
printf '%s\n' test > "$ARTIFACT_DIR/VERSION"
printf '%s\n' '#!/usr/bin/env sh' 'exit 0' > "$ARTIFACT_DIR/bin/block-agent"
chmod +x "$ARTIFACT_DIR/bin/block-agent"

BASH_BIN=$(command -v bash)
CAT_BIN=$(command -v cat)
GREP_BIN=$(command -v grep)
DIRNAME_BIN=$(command -v dirname)
id() { printf '%s\n' 0; }
cat() { "$CAT_BIN" "$@"; }
grep() { "$GREP_BIN" "$@"; }
dirname() { "$DIRNAME_BIN" "$@"; }
export BASH_BIN CAT_BIN GREP_BIN DIRNAME_BIN
export -f id cat grep dirname

preflight_config() {
  PATH=/nonexistent "$BASH_BIN" "$DEPLOY_DIR/install.sh" --execute --artifact-dir "$ARTIFACT_DIR" --config "$1" --version test 2>&1
}

ALLOWED_CONFIG=$TEST_ROOT/allowed.env
printf '%s\n' 'BLOCK_MQTTS_V2_ENDPOINT=mqtts://bdm.example.invalid:8883' > "$ALLOWED_CONFIG"
if OUTPUT=$(preflight_config "$ALLOWED_CONFIG"); then
  fail "install preflight unexpectedly reached system changes"
fi
case "$OUTPUT" in
  *'Block configuration must not persist points'*) fail "MQTTS endpoint was treated as a point table" ;;
  *'systemctl is required'*) ;;
  *) fail "install preflight did not reach the systemctl guard" ;;
esac

REJECTED_CONFIG=$TEST_ROOT/rejected.env
printf '%s\n' 'POINTS_FILE=/etc/block/points.json' > "$REJECTED_CONFIG"
if OUTPUT=$(preflight_config "$REJECTED_CONFIG"); then
  fail "install accepted persisted point configuration"
fi
case "$OUTPUT" in
  *'Block configuration must not persist points'*) ;;
  *) fail "install did not reject POINTS_FILE" ;;
esac

printf 'Block deployment regression passed\n'
