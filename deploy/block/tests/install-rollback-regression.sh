#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
CACHE_ROOT=$ROOT/../../.cache/block-install-rollback
TEST_ROOT=

fail() {
  printf 'install-rollback-regression: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  [ -n "$TEST_ROOT" ] && rm -rf -- "$TEST_ROOT"
}
trap cleanup EXIT

make_mock_commands() {
  local directory=$1 real_install real_stat
  real_install=$(command -v install)
  real_stat=$(command -v stat)
  mkdir -p "$directory"

  cat > "$directory/id" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "-u" ]; then
  printf '0\n'
  exit 0
fi
exit 1
EOF
  cat > "$directory/getent" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "passwd" ] && { [ "${2:-}" = "block" ] || [ "${2:-}" = "block-ui" ]; }; then
  printf '%s:x:1000:1000::/:/usr/sbin/nologin\n' "$2"
  exit 0
fi
exit 2
EOF
  cat > "$directory/runuser" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "${1:-}" = "-u" ]
[ -n "${2:-}" ]
[ "${3:-}" = "--" ]
shift 3
exec "$@"
EOF
  cat > "$directory/install" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
args=()
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o|-g)
      shift 2
      ;;
    *)
      args+=("$1")
      shift
      ;;
  esac
done
exec "$REAL_INSTALL" "${args[@]}"
EOF
  cat > "$directory/chown" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
  cat > "$directory/stat" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "-c" ] && [ "${2:-}" = "%a" ] && [[ "${3:-}" == "$TEST_ROOT/tls/"* ]]; then
  case "$3" in
    *.key) printf '640\n' ;;
    *) printf '644\n' ;;
  esac
  exit 0
fi
exec "$REAL_STAT" "$@"
EOF
  cat > "$directory/systemctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$MOCK_SYSTEMCTL_LOG"
case "${1:-}" in
  daemon-reload|enable|disable|stop)
    exit 0
    ;;
  restart)
    if [ "${2:-}" = "block.service" ] && [ "${MOCK_FAIL_NEW_RESTART:-0}" = "1" ] && \
      [ "$(readlink -f "$TEST_ROOT/opt/block/current")" = "$TEST_ROOT/opt/block/releases/new" ]; then
      exit 1
    fi
    exit 0
    ;;
  *)
    printf 'unexpected systemctl invocation: %s\n' "$*" >&2
    exit 1
    ;;
esac
EOF
  chmod 0755 "$directory"/*
  REAL_INSTALL=$real_install
  REAL_STAT=$real_stat
  export REAL_INSTALL REAL_STAT
}

rewrite_fixture_paths() {
  local script placeholder=__BLOCK_TEST_STATE_ROOT__
  for script in "$TEST_ROOT/source-deploy/install.sh" "$TEST_ROOT/source-deploy/rollback.sh"; do
    sed -i \
      -e "s|/var/lib/block-release|$placeholder|g" \
      -e "s|/var/lib/block|$TEST_ROOT/var/lib/block|g" \
      -e "s|$placeholder|$TEST_ROOT/var/lib/block-release|g" \
      -e "s|/etc/systemd/system|$TEST_ROOT/etc/systemd/system|g" \
      -e "s|/etc/block|$TEST_ROOT/etc/block|g" \
      -e "s|/opt/block|$TEST_ROOT/opt/block|g" \
      "$script"
  done
  printf '%s\n' '#!/usr/bin/env bash' 'exit 0' > "$TEST_ROOT/source-deploy/install-users.sh"
  chmod 0755 "$TEST_ROOT/source-deploy/install-users.sh"
}

write_release() {
  local name=$1 health=$2 release
  release=$TEST_ROOT/opt/block/releases/$name
  mkdir -p "$release/bin" "$release/deploy/systemd"
  printf '%s\n' '#!/usr/bin/env bash' 'exit 0' > "$release/bin/block-agent"
  chmod 0755 "$release/bin/block-agent"
  printf '%s\n' "$name" > "$release/VERSION"
  printf '%s\n' "$name block unit" > "$release/deploy/systemd/block.service"
  printf '%s\n' "$name kiosk unit" > "$release/deploy/systemd/block-kiosk.service"
  printf '%s\n' '#!/usr/bin/env bash' 'set -euo pipefail' '[ "$#" -eq 0 ]' "printf '%s\\n' $health > '$TEST_ROOT/$health-health'" > "$release/deploy/health-check.sh"
  chmod 0755 "$release/deploy/health-check.sh"
}

make_tls_material() {
  local tls=$TEST_ROOT/tls
  mkdir -p "$tls"
  openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
    -keyout "$tls/ca.key" -out "$tls/ca.crt" -subj /CN=block-test-ca >/dev/null 2>&1
  openssl req -newkey rsa:2048 -nodes \
    -keyout "$tls/block-hmi.key" -out "$tls/block-hmi.csr" -subj /CN=block-hmi >/dev/null 2>&1
  printf '%s\n' 'subjectAltName=IP:127.0.0.1' > "$tls/leaf.ext"
  openssl x509 -req -in "$tls/block-hmi.csr" -CA "$tls/ca.crt" -CAkey "$tls/ca.key" -CAcreateserial \
    -out "$tls/block-hmi.crt" -days 2 -extfile "$tls/leaf.ext" >/dev/null 2>&1
  chmod 0644 "$tls/ca.crt" "$tls/block-hmi.crt"
  chmod 0640 "$tls/block-hmi.key"
}

write_candidate_artifact() {
  local artifact=$TEST_ROOT/artifact
  mkdir -p "$artifact/bin" "$artifact/web/assets"
  printf '%s\n' '#!/usr/bin/env bash' 'exit 0' > "$artifact/bin/block-agent"
  chmod 0755 "$artifact/bin/block-agent"
  : > "$artifact/web/index.html"
  : > "$artifact/web/assets/points.json"
  printf '%s\n' new > "$artifact/VERSION"
}

write_candidate_config() {
  local destination=$1 tls=$TEST_ROOT/tls
  cat > "$destination" <<EOF
BLOCK_LOCAL_HTTPS_ADDRESS=127.0.0.1:8444
BLOCK_LOCAL_TLS_CERT=$tls/block-hmi.crt
BLOCK_LOCAL_TLS_KEY=$tls/block-hmi.key
BLOCK_LOCAL_TLS_CA=$tls/ca.crt
BLOCK_MAINTENANCE_HTTPS_ADDRESS=0.0.0.0:8443
EOF
}

mkdir -p "$CACHE_ROOT"
TEST_ROOT=$(mktemp -d "$CACHE_ROOT/fixture.XXXXXX")
TEST_ROOT=$(CDPATH= cd -- "$TEST_ROOT" && pwd -P)
cp -a "$ROOT/." "$TEST_ROOT/source-deploy"
rewrite_fixture_paths
make_mock_commands "$TEST_ROOT/mock-bin"
make_tls_material
write_candidate_artifact

mkdir -p "$TEST_ROOT/opt/block/releases" "$TEST_ROOT/etc/systemd/system" "$TEST_ROOT/etc/block" "$TEST_ROOT/var/lib/block-release"
write_release old old
ln -s "$TEST_ROOT/opt/block/releases/old" "$TEST_ROOT/opt/block/current"
printf '%s\n' old-unit > "$TEST_ROOT/etc/systemd/system/block.service"
printf '%s\n' old-kiosk-unit > "$TEST_ROOT/etc/systemd/system/block-kiosk.service"
printf '%s\n' 'BLOCK_LOCAL_HTTP_ADDRESS=127.0.0.1:8080' > "$TEST_ROOT/etc/block/block.env"
printf '%s\n' /prior/release > "$TEST_ROOT/var/lib/block-release/previous-release"
printf '%s\n' old > "$TEST_ROOT/var/lib/block-release/current-version"
write_candidate_config "$TEST_ROOT/candidate.env"

export TEST_ROOT MOCK_SYSTEMCTL_LOG="$TEST_ROOT/systemctl.log"
export PATH="$TEST_ROOT/mock-bin:$PATH"

cp -a "$TEST_ROOT/candidate.env" "$TEST_ROOT/missing-cert.env"
sed -i "s|$TEST_ROOT/tls/block-hmi.crt|$TEST_ROOT/tls/missing.crt|" "$TEST_ROOT/missing-cert.env"
if "$TEST_ROOT/source-deploy/install.sh" --execute --artifact-dir "$TEST_ROOT/artifact" --config "$TEST_ROOT/missing-cert.env" --version new >/dev/null 2>&1; then
  fail "install accepted missing TLS certificate"
fi
[ "$(readlink -f "$TEST_ROOT/opt/block/current")" = "$TEST_ROOT/opt/block/releases/old" ] || fail "missing certificate changed current release"
[ "$(cat "$TEST_ROOT/etc/systemd/system/block.service")" = old-unit ] || fail "missing certificate changed Block unit"
[ ! -e "$TEST_ROOT/var/lib/block-release/unit-snapshots" ] || fail "missing certificate created a rollback snapshot"

INSTALL_FAILURE_LOG=$TEST_ROOT/install-failure.log
if MOCK_FAIL_NEW_RESTART=1 "$TEST_ROOT/source-deploy/install.sh" --execute --artifact-dir "$TEST_ROOT/artifact" --config "$TEST_ROOT/candidate.env" --version new >"$INSTALL_FAILURE_LOG" 2>&1; then
  fail "candidate install unexpectedly succeeded"
fi
[ "$(readlink -f "$TEST_ROOT/opt/block/current")" = "$TEST_ROOT/opt/block/releases/old" ] || fail "automatic rollback did not restore current release"
[ "$(cat "$TEST_ROOT/etc/systemd/system/block.service")" = old-unit ] || fail "automatic rollback did not restore Block unit"
[ "$(cat "$TEST_ROOT/etc/systemd/system/block-kiosk.service")" = old-kiosk-unit ] || fail "automatic rollback did not restore kiosk unit"
[ "$(cat "$TEST_ROOT/etc/block/block.env")" = 'BLOCK_LOCAL_HTTP_ADDRESS=127.0.0.1:8080' ] || fail "automatic rollback did not restore old config"
[ "$(cat "$TEST_ROOT/var/lib/block-release/previous-release")" = /prior/release ] || fail "automatic rollback did not restore previous-release"
if [ ! -f "$TEST_ROOT/old-health" ]; then
  cat "$INSTALL_FAILURE_LOG" >&2
  fail "automatic rollback did not use the old health check"
fi
grep -Fqx 'daemon-reload' "$TEST_ROOT/systemctl.log" || fail "automatic rollback did not reload restored units"

printf '%s\n' new-unit > "$TEST_ROOT/etc/systemd/system/block.service"
printf '%s\n' new-kiosk-unit > "$TEST_ROOT/etc/systemd/system/block-kiosk.service"
cp -a "$TEST_ROOT/candidate.env" "$TEST_ROOT/etc/block/block.env"
rm -f "$TEST_ROOT/old-health"
ln -sfn "$TEST_ROOT/opt/block/releases/new" "$TEST_ROOT/opt/block/current"
printf '%s\n' "$TEST_ROOT/opt/block/releases/old" > "$TEST_ROOT/var/lib/block-release/previous-release"
printf '%s\n' "$TEST_ROOT/var/lib/block-release/unit-snapshots/pre-new" > "$TEST_ROOT/var/lib/block-release/current-unit-snapshot"
"$TEST_ROOT/opt/block/releases/new/deploy/rollback.sh" --execute --version old >/dev/null

[ "$(readlink -f "$TEST_ROOT/opt/block/current")" = "$TEST_ROOT/opt/block/releases/old" ] || fail "manual rollback did not restore current release"
[ "$(cat "$TEST_ROOT/etc/systemd/system/block.service")" = 'old block unit' ] || fail "manual rollback did not install target Block unit"
[ "$(cat "$TEST_ROOT/etc/systemd/system/block-kiosk.service")" = 'old kiosk unit' ] || fail "manual rollback did not install target kiosk unit"
[ "$(cat "$TEST_ROOT/etc/block/block.env")" = 'BLOCK_LOCAL_HTTP_ADDRESS=127.0.0.1:8080' ] || fail "manual rollback did not restore target config snapshot"
[ -f "$TEST_ROOT/old-health" ] || fail "manual rollback passed unsupported options to old health check"

printf 'Block install rollback regression passed\n'
