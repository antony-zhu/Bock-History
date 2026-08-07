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
"$DEPLOY_DIR/tests/install-rollback-regression.sh"

for ENABLED in false true; do
  ARG="-mqtts-v2-enabled=$ENABLED"
  [ "$ARG" = "-mqtts-v2-enabled=$ENABLED" ] || fail "MQTTS bool flag did not remain one argument"
done

if "$DEPLOY_DIR/install.sh" --artifact-dir /tmp/block-artifact --config /tmp/block.env --version test >/dev/null 2>&1; then
  fail "install must require --execute"
fi
if "$DEPLOY_DIR/rollback.sh" >/dev/null 2>&1; then
  fail "rollback must require --execute"
fi
if "$DEPLOY_DIR/health-check.sh" --retries 0 >/dev/null 2>&1; then
  fail "health check accepted zero retries"
fi
if "$DEPLOY_DIR/health-check.sh" --url http://127.0.0.1:8080/healthz --ca-file /tmp/ca >/dev/null 2>&1; then
  fail "health check accepted a plaintext local URL"
fi

TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT
ARTIFACT_DIR=$TEST_ROOT/artifact
mkdir -p "$ARTIFACT_DIR/bin" "$ARTIFACT_DIR/web/assets"
touch "$ARTIFACT_DIR/web/index.html" "$ARTIFACT_DIR/web/assets/points.json"
printf '%s\n' test > "$ARTIFACT_DIR/VERSION"
printf '%s\n' '#!/usr/bin/env sh' 'exit 0' > "$ARTIFACT_DIR/bin/block-agent"
chmod +x "$ARTIFACT_DIR/bin/block-agent"
cp -a "$DEPLOY_DIR" "$ARTIFACT_DIR/deploy"

BASH_BIN=$(command -v bash)
CAT_BIN=$(command -v cat)
GREP_BIN=$(command -v grep)
DIRNAME_BIN=$(command -v dirname)
SED_BIN=$(command -v sed)
id() { printf '%s\n' 0; }
cat() { "$CAT_BIN" "$@"; }
grep() { "$GREP_BIN" "$@"; }
dirname() { "$DIRNAME_BIN" "$@"; }
sed() { "$SED_BIN" "$@"; }
export BASH_BIN CAT_BIN GREP_BIN DIRNAME_BIN SED_BIN
export -f id cat grep dirname sed

preflight_config() {
  PATH=/nonexistent "$BASH_BIN" "$ARTIFACT_DIR/deploy/install.sh" --execute --artifact-dir "$ARTIFACT_DIR" --config "$1" --version test 2>&1
}

ALLOWED_CONFIG=$TEST_ROOT/allowed.env
printf '%s\n' \
  'BLOCK_LOCAL_HTTPS_ADDRESS=127.0.0.1:8444' \
  'BLOCK_LOCAL_TLS_CERT=/etc/block/certs/block-hmi.crt' \
  'BLOCK_LOCAL_TLS_KEY=/etc/block/certs/block-hmi.key' \
  'BLOCK_LOCAL_TLS_CA=/usr/local/share/ca-certificates/block-dmp-blk-rel-001.crt' \
  'BLOCK_MQTTS_V2_ENDPOINT=mqtts://bdm.example.invalid:8883' \
  'BLOCK_MAINTENANCE_HTTPS_ADDRESS=127.0.0.1:8443' \
  'BLOCK_MAINTENANCE_DEVICE_ID=block-0001' > "$ALLOWED_CONFIG"
if OUTPUT=$(preflight_config "$ALLOWED_CONFIG"); then
  fail "install preflight unexpectedly reached system changes"
fi
case "$OUTPUT" in
  *'Block configuration must not persist points'*) fail "MQTTS endpoint was treated as a point table" ;;
  *'systemctl is required'*) ;;
  *) fail "install preflight did not reach the systemctl guard" ;;
esac
PLAINTEXT_CONFIG=$TEST_ROOT/plaintext.env
printf '%s\n' \
  'BLOCK_LOCAL_HTTP_ADDRESS=127.0.0.1:8080' \
  'BLOCK_LOCAL_HTTPS_ADDRESS=127.0.0.1:8444' \
  'BLOCK_LOCAL_TLS_CERT=/etc/block/certs/block-hmi.crt' \
  'BLOCK_LOCAL_TLS_KEY=/etc/block/certs/block-hmi.key' \
  'BLOCK_LOCAL_TLS_CA=/usr/local/share/ca-certificates/block-dmp-blk-rel-001.crt' > "$PLAINTEXT_CONFIG"
if OUTPUT=$(preflight_config "$PLAINTEXT_CONFIG"); then
  fail "install accepted plaintext local business configuration"
fi
case "$OUTPUT" in
  *'plaintext BLOCK_LOCAL_HTTP_ADDRESS is retired'*) ;;
  *) fail "install did not reject BLOCK_LOCAL_HTTP_ADDRESS" ;;
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

VERIFY_ROOT=$TEST_ROOT/verify-install
mkdir -p "$VERIFY_ROOT/release/bin" "$VERIFY_ROOT/release/web/assets" "$VERIFY_ROOT/release/deploy/chromium" "$VERIFY_ROOT/etc/chromium-browser/policies/managed" "$VERIFY_ROOT/bin"
printf '%s\n' '#!/usr/bin/env sh' 'exit 0' > "$VERIFY_ROOT/release/bin/block-agent"
chmod +x "$VERIFY_ROOT/release/bin/block-agent"
printf '%s\n' test > "$VERIFY_ROOT/release/VERSION"
: > "$VERIFY_ROOT/release/web/index.html"
: > "$VERIFY_ROOT/release/web/assets/points.json"
cp "$DEPLOY_DIR/chromium/block-kiosk.json" "$VERIFY_ROOT/release/deploy/chromium/block-kiosk.json"
cp "$DEPLOY_DIR/chromium/block-kiosk.json" "$VERIFY_ROOT/etc/chromium-browser/policies/managed/block-kiosk.json"
printf '%s\n' '#!/usr/bin/env sh' 'exit 0' > "$VERIFY_ROOT/release/deploy/health-check.sh"
chmod +x "$VERIFY_ROOT/release/deploy/health-check.sh"
ln -s "$VERIFY_ROOT/release" "$VERIFY_ROOT/current"
printf '%s\n' \
  'BLOCK_LOCAL_HTTPS_ADDRESS=127.0.0.1:8444' \
  'BLOCK_LOCAL_TLS_CA=/public/ca.crt' \
  'BLOCK_MQTTS_V2_ENDPOINT=mqtts://bdm.example.invalid:8883' > "$VERIFY_ROOT/block.env"
printf '%s\n' \
  '#!/usr/bin/env sh' \
  '[ "$1" = is-active ] && [ "$2" = --quiet ] || exit 1' \
  '[ "$3" = block.service ] && exit 0' \
  '[ "$3" = block-kiosk.service ] && exit 0' \
  '[ "$3" = block-hmi.service ] && exit 3' \
  'exit 1' > "$VERIFY_ROOT/bin/systemctl"
chmod +x "$VERIFY_ROOT/bin/systemctl"
cp "$DEPLOY_DIR/verify-install.sh" "$VERIFY_ROOT/verify-install.sh"
sed -i \
  -e "s|/opt/block/current|$VERIFY_ROOT/current|g" \
  -e "s|/etc/chromium-browser|$VERIFY_ROOT/etc/chromium-browser|g" \
  -e "s|/etc/block/block.env|$VERIFY_ROOT/block.env|g" \
  "$VERIFY_ROOT/verify-install.sh"
chmod +x "$VERIFY_ROOT/verify-install.sh"
if ! PATH="$VERIFY_ROOT/bin:$PATH" "$VERIFY_ROOT/verify-install.sh" --expected-version test >/dev/null; then
  fail "verify-install rejected a legal BLOCK_MQTTS_V2_ENDPOINT"
fi
printf '%s\n' 'POINTS_FILE=/etc/block/points.json' >> "$VERIFY_ROOT/block.env"
if OUTPUT=$(PATH="$VERIFY_ROOT/bin:$PATH" "$VERIFY_ROOT/verify-install.sh" --expected-version test 2>&1); then
  fail "verify-install accepted persisted point configuration"
fi
case "$OUTPUT" in
  *'Block configuration persists points'*) ;;
  *) fail "verify-install did not reject POINTS_FILE" ;;
esac

printf 'Block deployment regression passed\n'
