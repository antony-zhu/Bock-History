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
CHROMIUM_POLICY_ROOT=/etc/chromium-browser/policies/managed
CHROMIUM_POLICY_FILE=$CHROMIUM_POLICY_ROOT/block-kiosk.json
SNAPSHOTS_ROOT=/var/lib/block-release/unit-snapshots
ROLLBACK_ARMED=false
INSTALL_SNAPSHOT=
CANDIDATE_DEPLOY_DIR=

rollback_install() {
  STATUS=$?
  trap - ERR
  if [ "$ROLLBACK_ARMED" = true ]; then
    "$SCRIPT_DIR/rollback.sh" --execute --snapshot "$INSTALL_SNAPSHOT"
  fi
  exit "$STATUS"
}

trap rollback_install ERR

usage() {
  cat <<'EOF'
Usage:
  sudo <artifact-dir>/deploy/install.sh --execute --artifact-dir <artifact-dir>
      --config <block.env> --version <version>

The artifact must contain bin/block-agent, web/index.html,
web/assets/points.json, deploy/, and VERSION. Run only the candidate artifact's
deploy/install.sh; do not use an installer from /opt/block/current. This script
changes only local release paths and systemd units. It does not configure Wi-Fi,
BDM, or PLC points.
EOF
}

fail() {
  printf 'install: %s\n' "$*" >&2
  exit 1
}

config_value() {
  local wanted=$1 key value
  while IFS='=' read -r key value; do
    if [ "$key" = "$wanted" ]; then
      printf '%s\n' "$value"
      return 0
    fi
  done < "$CONFIG_FILE"
  return 1
}

required_config_value() {
  local name=$1 value
  value=$(config_value "$name") || fail "missing $name in config"
  [ -n "$value" ] || fail "$name must not be empty"
  printf '%s\n' "$value"
}

require_absolute_file() {
  local label=$1 path=$2
  case "$path" in
    /*) ;;
    *) fail "$label must be an absolute path" ;;
  esac
  [ -f "$path" ] || fail "$label is not a regular file: $path"
}

require_user_readable() {
  local user=$1 path=$2
  getent passwd "$user" >/dev/null 2>&1 || fail "required service user does not exist: $user"
  runuser -u "$user" -- test -r "$path" || fail "$user cannot read $path"
}

require_safe_tls_mode() {
  local label=$1 path=$2 mode value
  mode=$(stat -c '%a' "$path") || fail "cannot read mode for $label: $path"
  case "$mode" in
    ''|*[!0-7]*) fail "invalid mode for $label: $path" ;;
  esac
  value=$((8#$mode))
  if (( (value & 0022) != 0 )); then
    fail "$label must not be group- or world-writable: $path"
  fi
  if [ "$label" = "local TLS private key" ] && (( (value & 0007) != 0 )); then
    fail "local TLS private key must not be accessible by other users: $path"
  fi
}

validate_local_tls_material() {
  local cert=$1 key=$2 ca=$3 cert_public key_public

  command -v openssl >/dev/null 2>&1 || fail "openssl is required for TLS validation"
  command -v getent >/dev/null 2>&1 || fail "getent is required for TLS validation"
  command -v runuser >/dev/null 2>&1 || fail "runuser is required for TLS validation"
  command -v stat >/dev/null 2>&1 || fail "stat is required for TLS validation"
  command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required for TLS validation"

  require_absolute_file "local TLS certificate" "$cert"
  require_absolute_file "local TLS private key" "$key"
  require_absolute_file "local TLS CA" "$ca"
  require_safe_tls_mode "local TLS certificate" "$cert"
  require_safe_tls_mode "local TLS private key" "$key"
  require_safe_tls_mode "local TLS CA" "$ca"
  require_user_readable block "$cert"
  require_user_readable block "$key"
  require_user_readable block "$ca"
  require_user_readable block-ui "$ca"

  openssl verify -CAfile "$ca" "$cert" >/dev/null 2>&1 ||
    fail "local TLS certificate does not verify against BLOCK_LOCAL_TLS_CA"
  openssl x509 -in "$cert" -noout -checkip 127.0.0.1 >/dev/null 2>&1 ||
    fail "local TLS certificate does not cover 127.0.0.1"
  cert_public=$(openssl x509 -in "$cert" -pubkey -noout | openssl pkey -pubin -outform DER | sha256sum | awk '{print $1}')
  key_public=$(openssl pkey -in "$key" -pubout -outform DER | sha256sum | awk '{print $1}')
  [ -n "$cert_public" ] && [ "$cert_public" = "$key_public" ] ||
    fail "local TLS certificate and private key do not match"
}

validate_candidate_deploy() {
  local tool

  [ "$SCRIPT_DIR" = "$CANDIDATE_DEPLOY_DIR" ] ||
    fail "run the candidate artifact's deploy/install.sh, not an installer from /opt/block/current"
  for tool in build.sh install.sh rollback.sh health-check.sh install-users.sh version.sh verify-install.sh verify-static.sh tests/deploy-regression.sh tests/install-rollback-regression.sh; do
    [ -x "$CANDIDATE_DEPLOY_DIR/$tool" ] || fail "missing executable candidate deploy tool: $tool"
  done
  for tool in README.md config/block.env.example systemd/block.service systemd/block-kiosk.service chromium/block-kiosk.json; do
    [ -f "$CANDIDATE_DEPLOY_DIR/$tool" ] || fail "missing candidate deploy file: $tool"
  done
}

snapshot_state_file() {
  local name=$1 source
  source="$STATE_ROOT/$name"
  if [ -f "$source" ]; then
    cp -a -- "$source" "$INSTALL_SNAPSHOT/state/$name"
    printf 'present\n' > "$INSTALL_SNAPSHOT/state/$name.state"
  else
    printf 'absent\n' > "$INSTALL_SNAPSHOT/state/$name.state"
  fi
}

snapshot_chromium_policy() {
  if [ -f "$CHROMIUM_POLICY_FILE" ]; then
    cp -a -- "$CHROMIUM_POLICY_FILE" "$INSTALL_SNAPSHOT/state/block-kiosk.json"
    printf 'present\n' > "$INSTALL_SNAPSHOT/state/block-kiosk.json.state"
  else
    printf 'absent\n' > "$INSTALL_SNAPSHOT/state/block-kiosk.json.state"
  fi
}

snapshot_install_state() {
  local unit source

  INSTALL_SNAPSHOT="$SNAPSHOTS_ROOT/pre-$VERSION"
  [ ! -e "$INSTALL_SNAPSHOT" ] || fail "install snapshot already exists: $INSTALL_SNAPSHOT"
  install -d -o root -g root -m 0700 "$INSTALL_SNAPSHOT/units" "$INSTALL_SNAPSHOT/state"

  : > "$INSTALL_SNAPSHOT/units.state"
  for unit in block.service block-kiosk.service; do
    source="$SYSTEMD_ROOT/$unit"
    if [ -f "$source" ]; then
      cp -a -- "$source" "$INSTALL_SNAPSHOT/units/$unit"
      printf '%s\tpresent\n' "$unit" >> "$INSTALL_SNAPSHOT/units.state"
    else
      printf '%s\tabsent\n' "$unit" >> "$INSTALL_SNAPSHOT/units.state"
    fi
  done

  if [ -L "$CURRENT_LINK" ]; then
    readlink "$CURRENT_LINK" > "$INSTALL_SNAPSHOT/current-link"
    readlink -f "$CURRENT_LINK" > "$INSTALL_SNAPSHOT/current-release"
    printf 'present\n' > "$INSTALL_SNAPSHOT/current-link.state"
  else
    printf 'absent\n' > "$INSTALL_SNAPSHOT/current-link.state"
  fi
  if [ -f "$CONFIG_ROOT/block.env" ]; then
    cp -a -- "$CONFIG_ROOT/block.env" "$INSTALL_SNAPSHOT/state/block.env"
    printf 'present\n' > "$INSTALL_SNAPSHOT/state/block.env.state"
  else
    printf 'absent\n' > "$INSTALL_SNAPSHOT/state/block.env.state"
  fi
  snapshot_chromium_policy
  snapshot_state_file previous-release
  snapshot_state_file current-version
  snapshot_state_file current-unit-snapshot
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

[ -d "$ARTIFACT_DIR" ] || fail "artifact directory does not exist: $ARTIFACT_DIR"
ARTIFACT_DIR=$(CDPATH= cd -- "$ARTIFACT_DIR" && pwd -P)
CANDIDATE_DEPLOY_DIR=$ARTIFACT_DIR/deploy
validate_candidate_deploy

[ -x "$ARTIFACT_DIR/bin/block-agent" ] || fail "missing artifact binary"
[ -f "$ARTIFACT_DIR/web/index.html" ] || fail "missing artifact index.html"
[ -f "$ARTIFACT_DIR/web/assets/points.json" ] || fail "missing artifact points.json"
[ -f "$ARTIFACT_DIR/VERSION" ] || fail "missing artifact VERSION"
[ -f "$CONFIG_FILE" ] || fail "missing config file"
[ "$(cat "$ARTIFACT_DIR/VERSION")" = "$VERSION" ] || fail "artifact VERSION does not match --version"
if grep -Eq '^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*_)?POINTS?(_[A-Za-z0-9_]+)?=' "$CONFIG_FILE"; then
  fail "Block configuration must not persist points"
fi
if config_value BLOCK_LOCAL_HTTP_ADDRESS >/dev/null; then
  fail "plaintext BLOCK_LOCAL_HTTP_ADDRESS is retired; use BLOCK_LOCAL_HTTPS_ADDRESS"
fi
BLOCK_LOCAL_HTTPS_ADDRESS=$(required_config_value BLOCK_LOCAL_HTTPS_ADDRESS)
[ "$BLOCK_LOCAL_HTTPS_ADDRESS" = "127.0.0.1:8444" ] || fail "BLOCK_LOCAL_HTTPS_ADDRESS must be 127.0.0.1:8444"
BLOCK_LOCAL_TLS_CERT=$(required_config_value BLOCK_LOCAL_TLS_CERT)
BLOCK_LOCAL_TLS_KEY=$(required_config_value BLOCK_LOCAL_TLS_KEY)
BLOCK_LOCAL_TLS_CA=$(required_config_value BLOCK_LOCAL_TLS_CA)
command -v systemctl >/dev/null 2>&1 || fail "systemctl is required"
validate_local_tls_material "$BLOCK_LOCAL_TLS_CERT" "$BLOCK_LOCAL_TLS_KEY" "$BLOCK_LOCAL_TLS_CA"

RELEASE_DIR=$RELEASES_ROOT/$VERSION
[ ! -e "$RELEASE_DIR" ] || fail "release already exists: $RELEASE_DIR"
PREVIOUS_RELEASE=
if [ -L "$CURRENT_LINK" ]; then
  PREVIOUS_RELEASE=$(readlink -f "$CURRENT_LINK")
  case "$PREVIOUS_RELEASE" in
    "$RELEASES_ROOT"/*) ;;
    *) fail "current release is outside $RELEASES_ROOT" ;;
  esac
fi

"$SCRIPT_DIR/install-users.sh"

install -d -m 0755 "$PREFIX"
install -d -o block -g block -m 0755 "$RELEASES_ROOT"
install -d -o block -g block -m 0750 /var/lib/block
install -d -o root -g block -m 0750 "$CONFIG_ROOT"
install -d -o root -g root -m 0755 "$SYSTEMD_ROOT"
install -d -o root -g root -m 0755 "$CHROMIUM_POLICY_ROOT"
install -d -o root -g root -m 0750 "$STATE_ROOT"
snapshot_install_state
ROLLBACK_ARMED=true

if grep -Fx 'BLOCK_MAINTENANCE_HTTPS_ADDRESS=127.0.0.1:8443' "$CONFIG_FILE" >/dev/null; then
  sed -i 's/^BLOCK_MAINTENANCE_HTTPS_ADDRESS=127\.0\.0\.1:8443$/BLOCK_MAINTENANCE_HTTPS_ADDRESS=0.0.0.0:8443/' "$CONFIG_FILE"
fi

install -d -o block -g block -m 0755 "$RELEASE_DIR/bin" "$RELEASE_DIR/web"
install -m 0755 -o block -g block "$ARTIFACT_DIR/bin/block-agent" "$RELEASE_DIR/bin/block-agent"
install -m 0644 -o block -g block "$ARTIFACT_DIR/VERSION" "$RELEASE_DIR/VERSION"
cp -a "$ARTIFACT_DIR/web/." "$RELEASE_DIR/web/"
chown -R block:block "$RELEASE_DIR/web"

install -d -o root -g root -m 0755 "$RELEASE_DIR/deploy/chromium" "$RELEASE_DIR/deploy/config" "$RELEASE_DIR/deploy/systemd" "$RELEASE_DIR/deploy/tests"
for TOOL in build.sh install-users.sh install.sh health-check.sh version.sh rollback.sh verify-install.sh verify-static.sh; do
  install -m 0755 -o root -g root "$CANDIDATE_DEPLOY_DIR/$TOOL" "$RELEASE_DIR/deploy/$TOOL"
done
install -m 0644 -o root -g root "$CANDIDATE_DEPLOY_DIR/README.md" "$RELEASE_DIR/deploy/README.md"
install -m 0644 -o root -g root "$CANDIDATE_DEPLOY_DIR/chromium/block-kiosk.json" "$RELEASE_DIR/deploy/chromium/block-kiosk.json"
install -m 0644 -o root -g root "$CANDIDATE_DEPLOY_DIR/config/block.env.example" "$RELEASE_DIR/deploy/config/block.env.example"
install -m 0644 -o root -g root "$CANDIDATE_DEPLOY_DIR/systemd/block.service" "$RELEASE_DIR/deploy/systemd/block.service"
install -m 0644 -o root -g root "$CANDIDATE_DEPLOY_DIR/systemd/block-kiosk.service" "$RELEASE_DIR/deploy/systemd/block-kiosk.service"
install -m 0755 -o root -g root "$CANDIDATE_DEPLOY_DIR/tests/deploy-regression.sh" "$RELEASE_DIR/deploy/tests/deploy-regression.sh"
install -m 0755 -o root -g root "$CANDIDATE_DEPLOY_DIR/tests/install-rollback-regression.sh" "$RELEASE_DIR/deploy/tests/install-rollback-regression.sh"

install -m 0640 -o root -g block "$CONFIG_FILE" "$CONFIG_ROOT/block.env"
install -m 0644 -o root -g root "$CANDIDATE_DEPLOY_DIR/chromium/block-kiosk.json" "$CHROMIUM_POLICY_FILE"
install -m 0644 -o root -g root "$CANDIDATE_DEPLOY_DIR/systemd/block.service" "$SYSTEMD_ROOT/block.service"
install -m 0644 -o root -g root "$CANDIDATE_DEPLOY_DIR/systemd/block-kiosk.service" "$SYSTEMD_ROOT/block-kiosk.service"

if [ -n "$PREVIOUS_RELEASE" ]; then
  printf '%s\n' "$PREVIOUS_RELEASE" > "$STATE_ROOT/previous-release"
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
systemctl enable block.service
systemctl enable block-kiosk.service
systemctl restart block.service
"$CURRENT_LINK/deploy/health-check.sh" --ca-file "$BLOCK_LOCAL_TLS_CA" --expected-version "$VERSION" --retries 30 --delay 1
systemctl restart block-kiosk.service
printf '%s\n' "$VERSION" > "$STATE_ROOT/current-version"
printf '%s\n' "$INSTALL_SNAPSHOT" > "$STATE_ROOT/current-unit-snapshot"
ROLLBACK_ARMED=false
trap - ERR

printf 'installed Block release %s\n' "$VERSION"
