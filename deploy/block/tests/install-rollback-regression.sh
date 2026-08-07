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

write_legacy_http_release() {
  local name=$1 health_marker=$2 release
  release=$TEST_ROOT/opt/block/releases/$name
  mkdir -p "$release/bin" "$release/deploy/systemd"
  printf '%s\n' '#!/usr/bin/env bash' 'exit 0' > "$release/bin/block-agent"
  chmod 0755 "$release/bin/block-agent"
  printf '%s\n' "$name" > "$release/VERSION"
  printf '%s\n' "$name block unit" > "$release/deploy/systemd/block.service"
  printf '%s\n' "$name kiosk unit" > "$release/deploy/systemd/block-kiosk.service"
  printf '%s\n' '#!/usr/bin/env bash' 'set -euo pipefail' '[ "$#" -eq 0 ]' "printf '%s\\n' $health_marker > '$TEST_ROOT/$health_marker-health'" > "$release/deploy/health-check.sh"
  chmod 0755 "$release/deploy/health-check.sh"
  printf '%s\n' '#!/usr/bin/env bash' "printf '%s\\n' legacy-installer > '$TEST_ROOT/legacy-installer-used'" 'exit 93' > "$release/deploy/install.sh"
  chmod 0755 "$release/deploy/install.sh"
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
  local version=$1 artifact
  artifact=$TEST_ROOT/artifact-$version
  mkdir -p "$artifact/bin" "$artifact/web/assets"
  printf '%s\n' '#!/usr/bin/env bash' 'exit 0' > "$artifact/bin/block-agent"
  chmod 0755 "$artifact/bin/block-agent"
  : > "$artifact/web/index.html"
  : > "$artifact/web/assets/points.json"
  printf '%s\n' "$version" > "$artifact/VERSION"
  cp -a "$TEST_ROOT/source-deploy/." "$artifact/deploy"
  printf '%s\n' '#!/usr/bin/env bash' 'set -euo pipefail' 'if [ "${1:-}" = "--help" ]; then printf "%s\\n" --ca-file; fi' 'exit 0' > "$artifact/deploy/health-check.sh"
  chmod 0755 "$artifact/deploy/health-check.sh"
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

mkdir -p "$TEST_ROOT/opt/block/releases" "$TEST_ROOT/etc/systemd/system" "$TEST_ROOT/etc/block" "$TEST_ROOT/var/lib/block-release"
write_legacy_http_release old old
ln -s "$TEST_ROOT/opt/block/releases/old" "$TEST_ROOT/opt/block/current"
printf '%s\n' old-unit > "$TEST_ROOT/etc/systemd/system/block.service"
printf '%s\n' old-kiosk-unit > "$TEST_ROOT/etc/systemd/system/block-kiosk.service"
printf '%s\n' 'BLOCK_LOCAL_HTTP_ADDRESS=127.0.0.1:8080' > "$TEST_ROOT/etc/block/block.env"
printf '%s\n' /prior/release > "$TEST_ROOT/var/lib/block-release/previous-release"
printf '%s\n' old > "$TEST_ROOT/var/lib/block-release/current-version"
write_candidate_config "$TEST_ROOT/candidate.env"

export TEST_ROOT MOCK_SYSTEMCTL_LOG="$TEST_ROOT/systemctl.log"
export PATH="$TEST_ROOT/mock-bin:$PATH"

write_candidate_artifact new
if "$TEST_ROOT/source-deploy/install.sh" --execute --artifact-dir "$TEST_ROOT/artifact-new" --config "$TEST_ROOT/candidate.env" --version new >"$TEST_ROOT/non-candidate-entry.log" 2>&1; then
  fail "installer outside the candidate artifact unexpectedly ran"
fi
grep -Fq "run the candidate artifact's deploy/install.sh" "$TEST_ROOT/non-candidate-entry.log" ||
  fail "installer outside the candidate artifact did not explain the required entrypoint"

cp -a "$TEST_ROOT/candidate.env" "$TEST_ROOT/missing-cert.env"
sed -i "s|$TEST_ROOT/tls/block-hmi.crt|$TEST_ROOT/tls/missing.crt|" "$TEST_ROOT/missing-cert.env"
if "$TEST_ROOT/artifact-new/deploy/install.sh" --execute --artifact-dir "$TEST_ROOT/artifact-new" --config "$TEST_ROOT/missing-cert.env" --version new >/dev/null 2>&1; then
  fail "install accepted missing TLS certificate"
fi
[ "$(readlink -f "$TEST_ROOT/opt/block/current")" = "$TEST_ROOT/opt/block/releases/old" ] || fail "missing certificate changed current release"
[ "$(cat "$TEST_ROOT/etc/systemd/system/block.service")" = old-unit ] || fail "missing certificate changed Block unit"
[ ! -e "$TEST_ROOT/var/lib/block-release/unit-snapshots" ] || fail "missing certificate created a rollback snapshot"

INSTALL_FAILURE_LOG=$TEST_ROOT/install-failure.log
if MOCK_FAIL_NEW_RESTART=1 "$TEST_ROOT/artifact-new/deploy/install.sh" --execute --artifact-dir "$TEST_ROOT/artifact-new" --config "$TEST_ROOT/candidate.env" --version new >"$INSTALL_FAILURE_LOG" 2>&1; then
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
[ ! -e "$TEST_ROOT/legacy-installer-used" ] || fail "candidate install invoked the legacy current installer"

write_candidate_artifact new-success
"$TEST_ROOT/artifact-new-success/deploy/install.sh" --execute --artifact-dir "$TEST_ROOT/artifact-new-success" --config "$TEST_ROOT/candidate.env" --version new-success >/dev/null
[ "$(readlink -f "$TEST_ROOT/opt/block/current")" = "$TEST_ROOT/opt/block/releases/new-success" ] || fail "candidate success install did not activate the new release"
for tool in build.sh install-users.sh install.sh health-check.sh version.sh rollback.sh verify-install.sh verify-static.sh tests/deploy-regression.sh tests/install-rollback-regression.sh; do
  [ -x "$TEST_ROOT/opt/block/releases/new-success/deploy/$tool" ] || fail "successful release is missing deploy tool: $tool"
done
for file in README.md config/block.env.example systemd/block.service systemd/block-kiosk.service; do
  [ -f "$TEST_ROOT/opt/block/releases/new-success/deploy/$file" ] || fail "successful release is missing deploy file: $file"
done
[ ! -e "$TEST_ROOT/legacy-installer-used" ] || fail "successful candidate install invoked the legacy current installer"

write_legacy_http_release no-snapshot no-snapshot
cp -a "$TEST_ROOT/etc/systemd/system/block.service" "$TEST_ROOT/block-unit-before-no-snapshot"
cp -a "$TEST_ROOT/etc/systemd/system/block-kiosk.service" "$TEST_ROOT/kiosk-unit-before-no-snapshot"
cp -a "$TEST_ROOT/etc/block/block.env" "$TEST_ROOT/config-before-no-snapshot"
cp -a "$TEST_ROOT/systemctl.log" "$TEST_ROOT/systemctl-before-no-snapshot"
if "$TEST_ROOT/opt/block/current/deploy/rollback.sh" --execute --version no-snapshot >"$TEST_ROOT/no-snapshot.log" 2>&1; then
  fail "cross-topology rollback without a snapshot unexpectedly succeeded"
fi
grep -Fq 'crosses local HTTP/TLS topology but has no recorded config/unit snapshot' "$TEST_ROOT/no-snapshot.log" ||
  fail "cross-topology rollback without a snapshot did not explain the refusal"
[ "$(readlink -f "$TEST_ROOT/opt/block/current")" = "$TEST_ROOT/opt/block/releases/new-success" ] || fail "no-snapshot rollback changed current release"
cmp -s "$TEST_ROOT/etc/systemd/system/block.service" "$TEST_ROOT/block-unit-before-no-snapshot" || fail "no-snapshot rollback changed Block unit"
cmp -s "$TEST_ROOT/etc/systemd/system/block-kiosk.service" "$TEST_ROOT/kiosk-unit-before-no-snapshot" || fail "no-snapshot rollback changed kiosk unit"
cmp -s "$TEST_ROOT/etc/block/block.env" "$TEST_ROOT/config-before-no-snapshot" || fail "no-snapshot rollback changed config"
cmp -s "$TEST_ROOT/systemctl.log" "$TEST_ROOT/systemctl-before-no-snapshot" || fail "no-snapshot rollback changed service state"

rm -f "$TEST_ROOT/old-health"
"$TEST_ROOT/opt/block/current/deploy/rollback.sh" --execute --version old >/dev/null

[ "$(readlink -f "$TEST_ROOT/opt/block/current")" = "$TEST_ROOT/opt/block/releases/old" ] || fail "manual rollback did not restore current release"
[ "$(cat "$TEST_ROOT/etc/systemd/system/block.service")" = 'old block unit' ] || fail "manual rollback did not install target Block unit"
[ "$(cat "$TEST_ROOT/etc/systemd/system/block-kiosk.service")" = 'old kiosk unit' ] || fail "manual rollback did not install target kiosk unit"
[ "$(cat "$TEST_ROOT/etc/block/block.env")" = 'BLOCK_LOCAL_HTTP_ADDRESS=127.0.0.1:8080' ] || fail "manual rollback did not restore target config snapshot"
[ -f "$TEST_ROOT/old-health" ] || fail "manual rollback passed unsupported options to old health check"

printf 'Block install rollback regression passed\n'
