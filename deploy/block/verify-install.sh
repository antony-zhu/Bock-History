#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
readonly CURRENT_ROOT="/opt/block/current"
readonly SYSTEMD_ROOT="/etc/systemd/system"

profile=""
ca_file="/etc/block/certs/ca.crt"
hmi_url="https://127.0.0.1:8443/healthz"
declare -a control_admins=()

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage:
  sudo ./verify-install.sh --profile production|lab \
    [--ca /etc/block/certs/ca.crt] \
    [--hmi-url https://LOOPBACK:8443/healthz] \
    [--control-admin USER ...]

Each --control-admin explicitly allowlists one lab operator who may access the
Simulator control socket. No administrator is allowlisted by default.
EOF
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command is missing: $1"
}

assert_file_metadata() {
  local path="$1"
  local owner="$2"
  local group="$3"
  local mode="$4"

  [[ -f "${path}" && ! -L "${path}" ]] || die "missing regular file: ${path}"
  [[ "$(stat -c '%U' "${path}")" == "${owner}" ]] ||
    die "${path} owner must be ${owner}"
  [[ "$(stat -c '%G' "${path}")" == "${group}" ]] ||
    die "${path} group must be ${group}"
  [[ "$(stat -c '%a' "${path}")" == "${mode}" ]] ||
    die "${path} mode must be ${mode}"
}

assert_socket_metadata() {
  local path="$1"
  local owner="$2"
  local group="$3"
  local mode="$4"

  [[ -S "${path}" ]] || die "missing Unix socket: ${path}"
  [[ "$(stat -c '%U' "${path}")" == "${owner}" ]] ||
    die "${path} owner must be ${owner}"
  [[ "$(stat -c '%G' "${path}")" == "${group}" ]] ||
    die "${path} group must be ${group}"
  [[ "$(stat -c '%a' "${path}")" == "${mode}" ]] ||
    die "${path} mode must be ${mode}"
}

assert_directory_metadata() {
  local path="$1"
  local owner="$2"
  local group="$3"
  local mode="$4"

  [[ -d "${path}" && ! -L "${path}" ]] || die "missing directory: ${path}"
  [[ "$(stat -c '%U' "${path}")" == "${owner}" ]] ||
    die "${path} owner must be ${owner}"
  [[ "$(stat -c '%G' "${path}")" == "${group}" ]] ||
    die "${path} group must be ${group}"
  [[ "$(stat -c '%a' "${path}")" == "${mode}" ]] ||
    die "${path} mode must be ${mode}"
}

assert_unit_line() {
  local unit="$1"
  local expected="$2"
  grep -Fqx -- "${expected}" "${SYSTEMD_ROOT}/${unit}" ||
    die "${unit} is missing required static gate: ${expected}"
}

assert_service_active() {
  systemctl is-active --quiet "$1" || die "$1 is not active"
}

assert_service_enabled() {
  systemctl is-enabled --quiet "$1" || die "$1 is not enabled"
}

assert_access() {
  local user_name="$1"
  local path="$2"
  local expectation="$3"

  if runuser -u "${user_name}" -- test -r "${path}" &&
    runuser -u "${user_name}" -- test -w "${path}"; then
    [[ "${expectation}" == "allow" ]] ||
      die "${user_name} unexpectedly has read/write access to ${path}"
  else
    [[ "${expectation}" == "deny" ]] ||
      die "${user_name} lacks required read/write access to ${path}"
  fi
}

assert_user_groups_exact() {
  local user_name="$1"
  shift
  local expected
  local actual

  expected="$(printf '%s\n' "$@" | sort | paste -sd, -)"
  actual="$(
    id -nG "${user_name}" |
      tr ' ' '\n' |
      sort |
      paste -sd, -
  )"
  [[ "${actual}" == "${expected}" ]] ||
    die "${user_name} groups are ${actual}; expected ${expected}"
}

assert_group_members_allowed() {
  local group_name="$1"
  shift
  local group_gid
  local members
  local member_name
  local allowed_name
  local member_allowed

  group_gid="$(getent group "${group_name}" | awk -F: '{print $3}')"
  [[ -n "${group_gid}" ]] || die "group is missing: ${group_name}"
  members="$(
    {
      getent group "${group_name}" | awk -F: '{print $4}' | tr ',' ' '
      getent passwd | awk -F: -v wanted_gid="${group_gid}" '$4 == wanted_gid { print $1 }'
    } |
      tr ' ' '\n' |
      sort -u
  )"
  for member_name in ${members}; do
    member_allowed="false"
    for allowed_name in "$@"; do
      if [[ "${member_name}" == "${allowed_name}" ]]; then
        member_allowed="true"
        break
      fi
    done
    [[ "${member_allowed}" == "true" ]] ||
      die "${group_name} contains non-allowlisted member: ${member_name}"
  done
}

listener_addresses() {
  local protocol="$1"
  local port="$2"
  local option="-ltn"
  if [[ "${protocol}" == "udp" ]]; then
    option="-lun"
  fi

  ss -H "${option}" |
    awk -v wanted="${port}" '
      {
        endpoint=$4
        sub(/^.*:/, "", endpoint)
        if (endpoint == wanted) {
          local_address=$4
          sub(/:[^:]*$/, "", local_address)
          print local_address
        }
      }
    '
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --profile|--ca|--hmi-url|--control-admin)
      [[ "$#" -ge 2 ]] || die "missing value for $1"
      option="$1"
      value="$2"
      shift 2
      case "${option}" in
        --profile) profile="${value}" ;;
        --ca) ca_file="${value}" ;;
        --hmi-url) hmi_url="${value}" ;;
        --control-admin) control_admins+=("${value}") ;;
      esac
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

[[ "${EUID}" -eq 0 ]] || die "run as root so Linux access checks are authoritative"
[[ "${profile}" == "production" || "${profile}" == "lab" ]] ||
  die "--profile must be production or lab"
case "${hmi_url}" in
  https://127.0.0.1:8443/*|https://localhost:8443/*|https://\[::1\]:8443/*) ;;
  *) die "--hmi-url must be loopback HTTPS on port 8443" ;;
esac

for command_name in cmp curl find getent grep id paste readlink runuser sort ss stat systemctl tr; do
  require_command "${command_name}"
done

for unit_name in block-agent.service block-hmi.service block-plc-simulator.service; do
  [[ -f "${SYSTEMD_ROOT}/${unit_name}" ]] || die "unit is not installed: ${unit_name}"
  cmp -s \
    "${SYSTEMD_ROOT}/${unit_name}" \
    "${CURRENT_ROOT}/deploy/systemd/${unit_name}" ||
    die "installed ${unit_name} differs from the current release"
  [[ -z "$(systemctl show "${unit_name}" -p DropInPaths --value)" ]] ||
    die "${unit_name} has an unreviewed drop-in"
done

assert_unit_line block-agent.service "PrivateNetwork=true"
assert_unit_line block-agent.service "RestrictAddressFamilies=AF_UNIX"
assert_unit_line block-agent.service "SupplementaryGroups=block-hmi-api block-sim-io"
assert_unit_line block-agent.service "InaccessiblePaths=-/run/block-plc/control -/var/lib/block-plc-sim"
assert_unit_line block-plc-simulator.service "PrivateNetwork=true"
assert_unit_line block-plc-simulator.service "RestrictAddressFamilies=AF_UNIX"
assert_unit_line block-plc-simulator.service "SupplementaryGroups=block-sim-io block-sim-control"
assert_unit_line block-plc-simulator.service "InaccessiblePaths=-/run/block-agent -/var/lib/block-agent"
assert_unit_line block-hmi.service "Environment=BLOCK_HMI_ADDR=127.0.0.1:8443"
assert_unit_line block-hmi.service "Environment=BLOCK_HMI_AGENT_TIMEOUT=8s"
assert_unit_line block-hmi.service "SupplementaryGroups=block-hmi-api"
assert_unit_line block-hmi.service "IPAddressDeny=any"
assert_unit_line block-hmi.service "IPAddressAllow=localhost"
assert_unit_line block-hmi.service "InaccessiblePaths=-/run/block-plc -/var/lib/block-agent -/var/lib/block-plc-sim"

assert_user_groups_exact block-agent block-agent block-hmi-api block-sim-io
assert_user_groups_exact block-hmi block-hmi block-hmi-api
assert_user_groups_exact block-simulator block-sim-control block-sim-io block-simulator

declare -a allowed_control_members=(block-simulator)
for admin_name in "${control_admins[@]}"; do
  getent passwd "${admin_name}" >/dev/null || die "unknown control administrator: ${admin_name}"
  actual_groups=" $(id -nG "${admin_name}") "
  [[ "${actual_groups}" == *" block-sim-control "* ]] ||
    die "${admin_name} is not a member of block-sim-control"
  allowed_control_members+=("${admin_name}")
done

assert_group_members_allowed block-hmi-api block-agent block-hmi
assert_group_members_allowed block-sim-io block-agent block-simulator
assert_group_members_allowed block-sim-control "${allowed_control_members[@]}"

assert_file_metadata /etc/block/block-agent.json root block-agent 640
assert_file_metadata /etc/block/certs/block-hmi.crt root root 644
assert_file_metadata /etc/block/certs/ca.crt root root 644
assert_file_metadata /etc/block/certs/block-hmi.key root block-hmi 640
assert_directory_metadata /etc/block/certs root block-hmi 750
[[ ! -e /etc/block/block-profile.env && ! -L /etc/block/block-profile.env ]] ||
  die "legacy block-profile.env must not remain installed"
assert_directory_metadata /var/lib/block-agent block-agent block-agent 700
[[ -L "${CURRENT_ROOT}" ]] || die "${CURRENT_ROOT} must be a symlink"
current_target="$(readlink -f "${CURRENT_ROOT}")"
[[ "${current_target}" == /opt/block/releases/* ]] ||
  die "${CURRENT_ROOT} points outside /opt/block/releases"
assert_file_metadata "${CURRENT_ROOT}/manifest.txt" root root 644
grep -Fqx "profile=${profile}" "${CURRENT_ROOT}/manifest.txt" ||
  die "release manifest profile differs from requested verification profile"
assert_file_metadata "${CURRENT_ROOT}/bin/block-agent" root root 755
assert_file_metadata "${CURRENT_ROOT}/bin/block-hmi" root root 755

for attempt in {1..20}; do
  if [[ -f /var/lib/block-agent/block.db ]]; then
    break
  fi
  sleep 0.5
done
[[ -f /var/lib/block-agent/block.db && ! -L /var/lib/block-agent/block.db ]] ||
  die "Agent SQLite database is missing or unsafe"
[[ "$(stat -c '%U:%G' /var/lib/block-agent/block.db)" == "block-agent:block-agent" ]] ||
  die "Agent SQLite database owner/group must be block-agent:block-agent"
if find /var/lib/block-agent -maxdepth 1 -type f \
  \( -name 'block.db' -o -name 'block.db-wal' -o -name 'block.db-shm' \) \
  -perm /077 -print -quit | grep -q .; then
  die "Agent SQLite/WAL files must not grant group/world permissions"
fi
if find /var/lib/block-agent -maxdepth 1 -type l \
  \( -name 'block.db-wal' -o -name 'block.db-shm' \) \
  -print -quit | grep -q .; then
  die "Agent SQLite WAL/SHM paths must not be symlinks"
fi

assert_service_enabled block-agent.service
assert_service_enabled block-hmi.service
assert_service_active block-agent.service
assert_service_active block-hmi.service

for attempt in {1..20}; do
  if [[ -S /run/block-agent/api/block-agent.sock ]]; then
    break
  fi
  sleep 0.5
done
assert_socket_metadata /run/block-agent/api/block-agent.sock block-agent block-hmi-api 660

if [[ "${profile}" == "lab" ]]; then
  assert_file_metadata /etc/block/plc-simulator.json root block-simulator 640
  assert_file_metadata "${CURRENT_ROOT}/bin/plc-simulator" root root 755
  assert_directory_metadata /var/lib/block-plc-sim block-simulator block-simulator 700
  assert_service_enabled block-plc-simulator.service
  assert_service_active block-plc-simulator.service
  for attempt in {1..20}; do
    if [[ -S /run/block-plc/io/io.sock && -S /run/block-plc/control/control.sock ]]; then
      break
    fi
    sleep 0.5
  done
  assert_socket_metadata /run/block-plc/io/io.sock block-simulator block-sim-io 660
  assert_socket_metadata /run/block-plc/control/control.sock block-simulator block-sim-control 660
else
  [[ ! -e /etc/block/plc-simulator.json && ! -L /etc/block/plc-simulator.json ]] ||
    die "production profile must not retain Simulator configuration"
  ! systemctl is-enabled --quiet block-plc-simulator.service ||
    die "Simulator must not be enabled in production"
  ! systemctl is-active --quiet block-plc-simulator.service ||
    die "Simulator must not be active in production"
fi

assert_access block-hmi /run/block-agent/api/block-agent.sock allow
assert_access block-simulator /run/block-agent/api/block-agent.sock deny
assert_access block-agent /var/lib/block-agent allow
assert_access block-hmi /var/lib/block-agent deny
assert_access block-simulator /var/lib/block-agent deny

if [[ "${profile}" == "lab" ]]; then
  assert_access block-agent /run/block-plc/io/io.sock allow
  assert_access block-agent /run/block-plc/control/control.sock deny
  assert_access block-hmi /run/block-plc/io/io.sock deny
  assert_access block-hmi /run/block-plc/control/control.sock deny
  assert_access block-simulator /run/block-plc/io/io.sock allow
  assert_access block-simulator /run/block-plc/control/control.sock allow
  assert_access block-simulator /var/lib/block-plc-sim allow
  assert_access block-agent /var/lib/block-plc-sim deny
  assert_access block-hmi /var/lib/block-plc-sim deny
  for admin_name in "${control_admins[@]}"; do
    assert_access "${admin_name}" /run/block-plc/control/control.sock allow
  done
fi

mapfile -t hmi_listeners < <(listener_addresses tcp 8443)
[[ "${#hmi_listeners[@]}" -gt 0 ]] || die "8443 is not listening"
for listen_address in "${hmi_listeners[@]}"; do
  case "${listen_address}" in
    127.0.0.1|\[::1\]|::1) ;;
    *) die "8443 must be loopback-only; found ${listen_address}" ;;
  esac
done

for forbidden_port in 80 1883 8080 8081; do
  [[ -z "$(listener_addresses tcp "${forbidden_port}")" ]] ||
    die "forbidden plaintext TCP port is listening: ${forbidden_port}"
  [[ -z "$(listener_addresses udp "${forbidden_port}")" ]] ||
    die "forbidden plaintext UDP port is listening: ${forbidden_port}"
done

BLOCK_HMI_CA="${ca_file}" \
BLOCK_HMI_HEALTH_URL="${hmi_url}" \
BLOCK_EXPECT_SIMULATOR="$([[ "${profile}" == "lab" ]] && printf true || printf false)" \
  "${SCRIPT_DIR}/health-check.sh"

printf 'OK: TLS, UDS, Linux permissions, systemd gates, loopback binding and plaintext rejection verified (%s)\n' \
  "${profile}"
