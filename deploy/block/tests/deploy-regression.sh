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

if "$DEPLOY_DIR/install.sh" --artifact-dir /tmp/block-artifact --config /tmp/block.json --version test >/dev/null 2>&1; then
  fail "install must require --execute"
fi
if "$DEPLOY_DIR/rollback.sh" >/dev/null 2>&1; then
  fail "rollback must require --execute"
fi
if "$DEPLOY_DIR/health-check.sh" --retries 0 >/dev/null 2>&1; then
  fail "health check accepted zero retries"
fi

printf 'Block deployment regression passed\n'
