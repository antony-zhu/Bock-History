#!/usr/bin/env bash
set -euo pipefail
umask 077

readonly SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
readonly TEST_MODE="${BLOCK_DEPLOY_TEST_MODE:-false}"
if [[ "${TEST_MODE}" == "true" ]]; then
  : "${BLOCK_DEPLOY_TEST_ROOT:?BLOCK_DEPLOY_TEST_ROOT is required in test mode}"
  [[ "${BLOCK_DEPLOY_TEST_ROOT}" == /* && "${BLOCK_DEPLOY_TEST_ROOT}" != "/" ]] ||
    {
      printf 'ERROR: BLOCK_DEPLOY_TEST_ROOT must be a non-root absolute path\n' >&2
      exit 2
    }
  readonly HOST_ROOT="${BLOCK_DEPLOY_TEST_ROOT%/}"
else
  readonly HOST_ROOT=""
fi
readonly OPT_ROOT="${HOST_ROOT}/opt/block"
readonly RELEASE_ROOT="${OPT_ROOT}/releases"
readonly CURRENT_LINK="${OPT_ROOT}/current"
readonly STATE_ROOT="${HOST_ROOT}/var/lib/block-release"
readonly DATA_ROOT="${HOST_ROOT}/var/lib/block"
readonly SYSTEMD_ROOT="${HOST_ROOT}/etc/systemd/system"
readonly CONFIG_ROOT="${HOST_ROOT}/etc/block"
readonly LOCK_ROOT="${HOST_ROOT}/run/lock"
readonly AGENT_SOCKET="${HOST_ROOT}/run/block-agent/api/block-agent.sock"
if [[ "${TEST_MODE}" == "true" ]]; then
  readonly AGENT_FILE_GROUP="root"
  readonly HMI_FILE_GROUP="root"
  readonly SIMULATOR_FILE_GROUP="root"
else
  readonly AGENT_FILE_GROUP="block-agent"
  readonly HMI_FILE_GROUP="block-hmi"
  readonly SIMULATOR_FILE_GROUP="block-simulator"
fi

profile=""
version=""
artifact_dir=""
agent_config=""
simulator_config=""
tls_cert=""
tls_key=""
tls_ca=""
bdm_ca=""
bdm_client_cert=""
bdm_client_key=""
bdm_enabled="False"
git_commit=""
common_baseline=""
hmi_url="https://127.0.0.1:8443/healthz"
execute_confirmed="false"
declare -a control_admins=()
tx_dir=""
staging_dir=""
release_dir=""
transaction_armed="false"
install_succeeded="false"

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage:
  sudo env BLOCK_RELEASE_ROLE=BLK-REL ./install.sh \
    --execute --profile production|lab --version VERSION \
    --artifact-dir DIR --agent-config FILE \
    --tls-cert FILE --tls-key FILE --tls-ca FILE \
    [--bdm-ca FILE --bdm-client-cert FILE --bdm-client-key FILE] \
    --git-commit COMMIT --common-baseline COMMIT \
    [--simulator-config FILE] [--hmi-url https://LOOPBACK:8443/healthz] \
    [--control-admin USER ...]

DIR must contain bin/block-agent and bin/block-hmi. Lab mode also requires
bin/plc-simulator and --simulator-config. The three BDM certificate arguments
are required together only when agent-config has bdm.enabled=true. This script
changes only the local Block host; it never connects to a remote host.
EOF
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command is missing: $1"
}

require_regular_file() {
  local path="$1"
  [[ -f "${path}" && ! -L "${path}" ]] || die "expected a regular, non-symlink file: ${path}"
}

validate_single_line() {
  local label="$1"
  local value="$2"
  [[ -n "${value}" ]] || die "${label} is required"
  [[ "${value}" != *$'\n'* && "${value}" != *$'\r'* ]] ||
    die "${label} must be a single line"
}

validate_json_config() {
  local path="$1"
  python3 -m json.tool "${path}" >/dev/null
  if grep -Eiq '"(password|passwd|secret|token|api[_-]?key|private[_-]?key|wifi[_-]?(ssid|password))"[[:space:]]*:' "${path}"; then
    die "configuration appears to contain a secret-bearing key: ${path}"
  fi
}

json_value() {
  local path="$1"
  local expression="$2"
  python3 -c \
    'import json,sys
data=json.load(open(sys.argv[1], encoding="utf-8"))
value=data
for key in sys.argv[2].split("."):
    value=value[key]
print(value)' \
    "${path}" "${expression}"
}

validate_bdm_metadata() {
  python3 - "${agent_config}" "${version}" <<'PY_BDM_METADATA'
import json
import re
import sys


def fail(message):
    print(message, file=sys.stderr)
    raise SystemExit(1)


config_path, release_version = sys.argv[1:]
with open(config_path, encoding="utf-8") as handle:
    config = json.load(handle)

if type(config) is not dict:
    fail("Agent configuration must be a JSON object")
bdm = config.get("bdm")
if type(bdm) is not dict:
    fail("Agent bdm must be a JSON object")

enabled = bdm.get("enabled")
if type(enabled) is not bool:
    fail("Agent bdm.enabled must be a JSON boolean")

string_fields = (
    "endpoint",
    "principal",
    "caFile",
    "clientCertFile",
    "clientKeyFile",
    "softwareVersion",
    "osVersion",
    "architecture",
    "hardwareModel",
    "streamGeneration",
)
values = {}
for field in string_fields:
    if field not in bdm and not enabled:
        continue
    value = bdm.get(field)
    if type(value) is not str:
        fail(f"Agent bdm.{field} must be a JSON string")
    trimmed = value.strip()
    if not trimmed:
        fail(f"Agent bdm.{field} must not be empty or whitespace")
    if value != trimmed:
        fail(f"Agent bdm.{field} must not have surrounding whitespace")
    values[field] = value

placeholder_values = {
    "changeme",
    "placeholder",
    "replace-at-release",
    "replace-me",
    "replace_me",
    "tbd",
    "todo",
}
for field in string_fields:
    if field in values and values[field].lower() in placeholder_values:
        fail(f"Agent bdm.{field} must not contain a placeholder value")

if not enabled:
    print("False")
    raise SystemExit(0)

if values["endpoint"] != "mqtts://192.168.1.105:8883":
    fail("current lab baseline requires bdm.endpoint mqtts://192.168.1.105:8883")
if not re.fullmatch(r"blk-[0-9a-f]{32}", values["principal"], flags=re.ASCII):
    fail("Agent bdm.principal must be an opaque blk-<32 lowercase hex> identity")
expected_paths = {
    "caFile": "/etc/block/bdm-certs/ca.crt",
    "clientCertFile": "/etc/block/bdm-certs/client.crt",
    "clientKeyFile": "/etc/block/bdm-certs/client.key",
}
for field, expected in expected_paths.items():
    if values[field] != expected:
        fail(f"Agent bdm.{field} must be {expected}")
if values["softwareVersion"] != release_version:
    fail("Agent bdm.softwareVersion must exactly match --version")
if values["architecture"] != "arm64":
    fail("Agent bdm.architecture must be arm64 for this release")

generation = values["streamGeneration"]
if not re.fullmatch(r"[1-9][0-9]{0,15}", generation, flags=re.ASCII):
    fail("Agent bdm.streamGeneration must be a canonical positive decimal string")
if int(generation) > 9007199254740991:
    fail("Agent bdm.streamGeneration exceeds the contract maximum")

print("True")
PY_BDM_METADATA
}

validate_profile_config() {
  local adapter_type
  local configured_bdm_enabled

  [[ "$(json_value "${agent_config}" localApiSocket)" == "/run/block-agent/api/block-agent.sock" ]] ||
    die "Agent localApiSocket must use the authoritative API path"
  [[ "$(json_value "${agent_config}" localApiSocketGroup)" == "block-hmi-api" ]] ||
    die "Agent localApiSocketGroup must be block-hmi-api"
  [[ "$(json_value "${agent_config}" databasePath)" == "/var/lib/block/block.db" ]] ||
    die "Agent databasePath must be /var/lib/block/block.db"

  if ! configured_bdm_enabled="$(validate_bdm_metadata)"; then
    die "Agent BDM metadata validation failed"
  fi
  bdm_enabled="${configured_bdm_enabled}"

  adapter_type="$(json_value "${agent_config}" adapter.type)"
  if [[ "${profile}" == "production" ]]; then
    [[ "${adapter_type}" == "disabled" ]] ||
      die "production Agent adapter.type must be disabled"
  else
    [[ "${adapter_type}" == "simulator" ]] ||
      die "lab Agent adapter.type must be simulator"
    [[ "$(json_value "${agent_config}" adapter.ioSocket)" == "/run/block-plc/io/io.sock" ]] ||
      die "lab Agent adapter.ioSocket must use the authoritative Simulator I/O path"
    [[ "$(json_value "${simulator_config}" ioSocket)" == "/run/block-plc/io/io.sock" ]] ||
      die "Simulator ioSocket must use the authoritative I/O path"
    [[ "$(json_value "${simulator_config}" ioSocketGroup)" == "block-sim-io" ]] ||
      die "Simulator ioSocketGroup must be block-sim-io"
    [[ "$(json_value "${simulator_config}" controlSocket)" == "/run/block-plc/control/control.sock" ]] ||
      die "Simulator controlSocket must use the authoritative control path"
    [[ "$(json_value "${simulator_config}" controlSocketGroup)" == "block-sim-control" ]] ||
      die "Simulator controlSocketGroup must be block-sim-control"
  fi
}

verify_bdm_certificate_material() {
  local cert_public_hash
  local extended_key_usage
  local key_public_hash
  local principal
  local subject
  local subject_cn

  openssl x509 -in "${bdm_ca}" -noout -checkend 0 >/dev/null ||
    die "BDM server CA bundle is invalid or expired"
  openssl x509 -in "${bdm_client_cert}" -noout -checkend 0 >/dev/null ||
    die "Block MQTT client certificate is invalid or expired"
  extended_key_usage="$(
    openssl x509 -in "${bdm_client_cert}" -noout -ext extendedKeyUsage 2>/dev/null
  )"
  grep -Eq 'TLS Web Client Authentication|Any Extended Key Usage|clientAuth' \
    <<<"${extended_key_usage}" ||
    die "Block MQTT client certificate lacks clientAuth extended key usage"
  principal="$(json_value "${agent_config}" bdm.principal)"
  subject="$(openssl x509 -in "${bdm_client_cert}" -noout -subject -nameopt RFC2253)"
  subject="${subject#subject=}"
  subject_cn="$(
    printf '%s\n' "${subject}" |
      tr ',' '\n' |
      awk -F= '$1 == "CN" { print substr($0, 4); exit }'
  )"
  [[ "${subject_cn}" == "${principal}" ]] ||
    die "Block MQTT client certificate CN must exactly equal bdm.principal"
  cert_public_hash="$(
    openssl x509 -in "${bdm_client_cert}" -pubkey -noout |
      openssl pkey -pubin -outform DER 2>/dev/null |
      sha256sum |
      awk '{print $1}'
  )"
  key_public_hash="$(
    openssl pkey -in "${bdm_client_key}" -pubout -outform DER 2>/dev/null |
      sha256sum |
      awk '{print $1}'
  )"
  [[ -n "${cert_public_hash}" && "${cert_public_hash}" == "${key_public_hash}" ]] ||
    die "Block MQTT client certificate and private key do not match"
}

validate_data_root() {
  local metadata

  if [[ "${TEST_MODE}" == "true" ]]; then
    return
  fi
  [[ -d "${DATA_ROOT}" && ! -L "${DATA_ROOT}" ]] ||
    die "${DATA_ROOT} must be a real directory backed by the prepared data partition"
  mountpoint -q "${DATA_ROOT}" ||
    die "${DATA_ROOT} must be a mounted filesystem before Block installation"
  metadata="$(stat -c '%U:%G:%a' "${DATA_ROOT}")"
  [[ "${metadata}" == "block-agent:block-agent:700" ]] ||
    die "${DATA_ROOT} must be block-agent:block-agent mode 0700; found ${metadata}"
}

validate_private_key_mode() {
  local path="$1"
  local mode

  # DrvFS exposes workspace test fixtures as 0777 even after chmod. Production
  # never enters test mode and always enforces the source-key permission gate.
  if [[ "${TEST_MODE}" == "true" ]]; then
    return
  fi
  mode="$(stat -c '%a' "${path}")"
  (( (8#${mode} & 077) == 0 )) ||
    die "TLS private-key source must not be group/world accessible: ${path} (${mode})"
}

metadata_matches_expected() {
  local path="$1"
  local expected="$2"

  # DrvFS cannot represent the Linux owner/group/mode installed into the
  # regression sandbox. This opt-in is test-only; production always performs
  # the exact metadata comparison below.
  if [[ "${TEST_MODE}" == "true" &&
    "${BLOCK_DEPLOY_TEST_SKIP_METADATA:-false}" == "true" ]]; then
    [[ -f "${path}" && ! -L "${path}" ]]
    return
  fi
  [[ "$(stat -c '%U:%G:%a' "${path}" 2>/dev/null || true)" == "${expected}" ]]
}

verify_certificate_material() {
  local cert_public_hash
  local key_public_hash

  openssl x509 -in "${tls_cert}" -noout -checkend 0 >/dev/null ||
    die "HMI certificate is invalid or expired"
  openssl verify -CAfile "${tls_ca}" "${tls_cert}" >/dev/null ||
    die "HMI certificate is not trusted by the supplied CA"

  cert_public_hash="$(
    openssl x509 -in "${tls_cert}" -pubkey -noout |
      openssl pkey -pubin -outform DER 2>/dev/null |
      sha256sum |
      awk '{print $1}'
  )"
  key_public_hash="$(
    openssl pkey -in "${tls_key}" -pubout -outform DER 2>/dev/null |
      sha256sum |
      awk '{print $1}'
  )"
  [[ -n "${cert_public_hash}" && "${cert_public_hash}" == "${key_public_hash}" ]] ||
    die "HMI certificate and private key do not match"
}

reject_private_key_blocks() {
  local label="$1"
  local path="$2"

  if grep -Eq -- '-----BEGIN ([A-Z0-9 ]+ )?PRIVATE KEY-----' "${path}"; then
    die "${label} must contain certificates only; private-key PEM block found: ${path}"
  fi
}

backup_file() {
  local path="$1"
  local backup_root="$2"
  local relative="${path#/}"
  local destination="${backup_root}/files/${relative}"

  if [[ -e "${path}" || -L "${path}" ]]; then
    install -d -m 0700 "$(dirname -- "${destination}")"
    cp -a --no-dereference "${path}" "${destination}"
  else
    printf '%s\n' "${path}" >>"${backup_root}/missing-files"
  fi
}

ensure_directory_exists_without_relabel() {
  local path="$1"
  local owner="$2"
  local group="$3"
  local mode="$4"

  if [[ -e "${path}" || -L "${path}" ]]; then
    [[ -d "${path}" && ! -L "${path}" ]] ||
      die "directory path is missing or unsafe: ${path}"
    return
  fi
  install -d -o "${owner}" -g "${group}" -m "${mode}" "${path}"
}

is_managed_directory_path() {
  local path="$1"

  case "${path}" in
    "${OPT_ROOT}"|\
      "${RELEASE_ROOT}"|\
      "${STATE_ROOT}"|\
      "${STATE_ROOT}/transactions"|\
      "${CONFIG_ROOT}"|\
      "${CONFIG_ROOT}/certs"|\
      "${CONFIG_ROOT}/bdm-certs")
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

capture_managed_directory_states() {
  local transaction="$1"
  local state_file="${transaction}/directory-state.tsv"
  local path

  : >"${state_file}"
  for path in \
    "${OPT_ROOT}" \
    "${RELEASE_ROOT}" \
    "${STATE_ROOT}" \
    "${STATE_ROOT}/transactions" \
    "${CONFIG_ROOT}" \
    "${CONFIG_ROOT}/certs" \
    "${CONFIG_ROOT}/bdm-certs"; do
    [[ -d "${path}" && ! -L "${path}" ]] ||
      die "managed directory is missing or unsafe: ${path}"
    printf '%s\t%s\t%s\t%s\n' \
      "${path}" \
      "$(stat -c '%u' "${path}")" \
      "$(stat -c '%g' "${path}")" \
      "$(stat -c '%a' "${path}")" \
      >>"${state_file}"
  done
  chmod 0600 "${state_file}"
}

restore_managed_directory_states() {
  local transaction="$1"
  local state_file="${transaction}/directory-state.tsv"
  local path
  local owner
  local group
  local mode
  local extra
  local expected_path
  declare -A seen=()
  declare -A recorded_owner=()
  declare -A recorded_group=()
  declare -A recorded_mode=()

  [[ -f "${state_file}" && ! -L "${state_file}" ]] || {
    printf 'ERROR: transaction lacks safe managed-directory metadata: %s\n' \
      "${state_file}" >&2
    return 1
  }
  while IFS=$'\t' read -r path owner group mode extra; do
    if [[ -z "${path}" || -n "${extra}" ]] ||
      ! is_managed_directory_path "${path}" ||
      [[ ! "${owner}" =~ ^[0-9]+$ || ! "${group}" =~ ^[0-9]+$ ||
        ! "${mode}" =~ ^[0-7]{3,4}$ ]] ||
      [[ -n "${seen["${path}"]+present}" ]]; then
      printf 'ERROR: invalid managed-directory metadata in %s\n' "${state_file}" >&2
      return 1
    fi
    seen["${path}"]="present"
    recorded_owner["${path}"]="${owner}"
    recorded_group["${path}"]="${group}"
    recorded_mode["${path}"]="${mode}"
  done <"${state_file}"
  for expected_path in \
    "${OPT_ROOT}" \
    "${RELEASE_ROOT}" \
    "${STATE_ROOT}" \
    "${STATE_ROOT}/transactions" \
    "${CONFIG_ROOT}" \
    "${CONFIG_ROOT}/certs" \
    "${CONFIG_ROOT}/bdm-certs"; do
    if [[ -z "${seen["${expected_path}"]+present}" ]]; then
      printf 'ERROR: transaction is missing managed-directory metadata for %s\n' \
        "${expected_path}" >&2
      return 1
    fi
    if [[ -e "${expected_path}" || -L "${expected_path}" ]] &&
      [[ ! -d "${expected_path}" || -L "${expected_path}" ]]; then
      printf 'ERROR: refusing unsafe managed directory during recovery: %s\n' \
        "${expected_path}" >&2
      return 1
    fi
  done
  for expected_path in \
    "${OPT_ROOT}" \
    "${RELEASE_ROOT}" \
    "${STATE_ROOT}" \
    "${STATE_ROOT}/transactions" \
    "${CONFIG_ROOT}" \
    "${CONFIG_ROOT}/certs" \
    "${CONFIG_ROOT}/bdm-certs"; do
    owner="${recorded_owner["${expected_path}"]}"
    group="${recorded_group["${expected_path}"]}"
    mode="${recorded_mode["${expected_path}"]}"
    if [[ -e "${expected_path}" || -L "${expected_path}" ]]; then
      chown --no-dereference -- "${owner}:${group}" "${expected_path}" ||
        return 1
      chmod -- "${mode}" "${expected_path}" || return 1
    else
      install -d -o "${owner}" -g "${group}" -m "${mode}" \
        "${expected_path}" || return 1
    fi
  done
}

require_safe_restore_parent() {
  local path="$1"
  local parent

  parent="$(dirname -- "${path}")"
  [[ -d "${parent}" && ! -L "${parent}" ]] || {
    printf 'ERROR: restore parent is missing or unsafe: %s\n' "${parent}" >&2
    return 1
  }
}

capture_unit_state() {
  local unit="$1"
  local tx_dir="$2"
  local enabled_state="not-found"
  local active_state="inactive"

  enabled_state="$(systemctl is-enabled "${unit}" 2>/dev/null || true)"
  active_state="$(systemctl is-active "${unit}" 2>/dev/null || true)"
  [[ -n "${enabled_state}" ]] || enabled_state="not-found"
  [[ -n "${active_state}" ]] || active_state="inactive"
  printf '%s\t%s\t%s\n' "${unit}" "${enabled_state}" "${active_state}" \
    >>"${tx_dir}/unit-state.tsv"
}

atomic_write_line() {
  local destination="$1"
  local value="$2"
  local temporary="${destination}.new.$$"

  printf '%s\n' "${value}" >"${temporary}"
  chmod 0600 "${temporary}"
  mv -fT "${temporary}" "${destination}"
}

restore_path_from_transaction() {
  local path="$1"
  local transaction="$2"
  local backup="${transaction}/files/${path#/}"

  if [[ -e "${backup}" || -L "${backup}" ]]; then
    require_safe_restore_parent "${path}" || return 1
    rm -f -- "${path}"
    cp -a --no-dereference "${backup}" "${path}"
  elif grep -Fqx -- "${path}" "${transaction}/missing-files"; then
    rm -f -- "${path}"
  else
    printf 'ERROR: transaction has no backup decision for %s\n' "${path}" >&2
    return 1
  fi
}

restore_enablement() {
  local unit="$1"
  local state="$2"

  systemctl unmask "${unit}" >/dev/null 2>&1 || true
  case "${state}" in
    enabled|enabled-runtime|linked|linked-runtime)
      systemctl enable "${unit}" >/dev/null
      ;;
    masked|masked-runtime)
      systemctl mask "${unit}" >/dev/null
      ;;
    disabled)
      systemctl disable "${unit}" >/dev/null 2>&1 || true
      ;;
    not-found|static|indirect|generated|transient|alias|"")
      ;;
    *)
      printf 'ERROR: unsupported previous enablement state for %s: %s\n' "${unit}" "${state}" >&2
      return 1
      ;;
  esac
}

restore_failed_install() {
  local reason="$1"
  local restore_failed="false"
  local unit_name
  local enabled_state
  local active_state
  local previous_current
  local previous_transaction
  local previous_profile

  trap - ERR
  set +e
  printf 'ERROR: installation failed (%s); restoring the captured host state\n' "${reason}" >&2

  systemctl stop block-hmi.service >/dev/null 2>&1 || true
  systemctl stop block-agent.service >/dev/null 2>&1 || true
  systemctl stop block-plc-simulator.service >/dev/null 2>&1 || true

  restore_managed_directory_states "${tx_dir}" ||
    restore_failed="true"
  for managed_path in \
    "${SYSTEMD_ROOT}/block-agent.service" \
    "${SYSTEMD_ROOT}/block-hmi.service" \
    "${SYSTEMD_ROOT}/block-plc-simulator.service" \
    "${CONFIG_ROOT}/block-agent.json" \
    "${CONFIG_ROOT}/plc-simulator.json" \
    "${CONFIG_ROOT}/block-profile.env" \
    "${CONFIG_ROOT}/certs/block-hmi.crt" \
    "${CONFIG_ROOT}/certs/block-hmi.key" \
    "${CONFIG_ROOT}/certs/ca.crt" \
    "${CONFIG_ROOT}/bdm-certs/ca.crt" \
    "${CONFIG_ROOT}/bdm-certs/client.crt" \
    "${CONFIG_ROOT}/bdm-certs/client.key"; do
    restore_path_from_transaction "${managed_path}" "${tx_dir}" ||
      restore_failed="true"
  done

  if [[ -f "${tx_dir}/previous-current" ]]; then
    previous_current="$(cat "${tx_dir}/previous-current")"
    if [[ "${previous_current}" == "${RELEASE_ROOT}/"* &&
      -d "${previous_current}" && ! -L "${previous_current}" ]]; then
      temporary_link="${OPT_ROOT}/.current.restore.$$"
      rm -f -- "${temporary_link}"
      ln -s "${previous_current}" "${temporary_link}" &&
        mv -fT "${temporary_link}" "${CURRENT_LINK}" ||
        restore_failed="true"
    else
      printf 'ERROR: invalid previous release during failed-install recovery: %s\n' \
        "${previous_current}" >&2
      restore_failed="true"
    fi
  elif [[ -L "${CURRENT_LINK}" || ! -e "${CURRENT_LINK}" ]]; then
    rm -f -- "${CURRENT_LINK}"
  else
    printf 'ERROR: refusing to remove non-symlink current path during recovery\n' >&2
    restore_failed="true"
  fi

  if [[ -f "${tx_dir}/previous-profile" ]]; then
    previous_profile="$(cat "${tx_dir}/previous-profile")"
    atomic_write_line "${STATE_ROOT}/current-profile" "${previous_profile}" ||
      restore_failed="true"
  else
    rm -f -- "${STATE_ROOT}/current-profile"
  fi
  if [[ -f "${tx_dir}/previous-transaction" ]]; then
    previous_transaction="$(cat "${tx_dir}/previous-transaction")"
    if [[ "${previous_transaction}" == "${STATE_ROOT}/transactions/"* ]]; then
      atomic_write_line "${STATE_ROOT}/current-transaction" "${previous_transaction}" ||
        restore_failed="true"
    else
      printf 'ERROR: invalid previous transaction marker during recovery\n' >&2
      restore_failed="true"
    fi
  else
    rm -f -- "${STATE_ROOT}/current-transaction"
  fi

  systemctl daemon-reload >/dev/null 2>&1 || restore_failed="true"
  while IFS=$'\t' read -r unit_name enabled_state active_state; do
    restore_enablement "${unit_name}" "${enabled_state}" ||
      restore_failed="true"
  done <"${tx_dir}/unit-state.tsv"
  while IFS=$'\t' read -r unit_name enabled_state active_state; do
    if [[ "${active_state}" == "active" ]]; then
      systemctl start "${unit_name}" >/dev/null 2>&1 ||
        restore_failed="true"
    fi
  done <"${tx_dir}/unit-state.tsv"

  if [[ -n "${staging_dir}" && "${staging_dir}" == "${RELEASE_ROOT}/."* &&
    -d "${staging_dir}" && ! -L "${staging_dir}" ]]; then
    rm -rf --one-file-system -- "${staging_dir}" || restore_failed="true"
  fi
  printf '%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"${tx_dir}/failed-at"
  chmod 0600 "${tx_dir}/failed-at"

  if [[ "${restore_failed}" == "true" ]]; then
    printf 'ERROR: failed-install recovery was incomplete; inspect %s before any retry\n' \
      "${tx_dir}" >&2
    return 1
  fi
  printf 'NOTICE: pre-install host state restored; transaction evidence retained at %s\n' \
    "${tx_dir}" >&2
  return 0
}

install_error_trap() {
  local status="$1"
  local line="$2"

  if [[ "${transaction_armed}" == "true" && "${install_succeeded}" != "true" ]]; then
    restore_failed_install "exit ${status} at line ${line}" || true
  fi
  exit "${status}"
}

maybe_failpoint() {
  local point="$1"

  if [[ "${TEST_MODE}" == "true" &&
    "${BLOCK_DEPLOY_TEST_FAILPOINT:-}" == "${point}" ]]; then
    printf 'ERROR: injected deployment failure at %s\n' "${point}" >&2
    return 97
  fi
}

agent_socket_exists() {
  if [[ "${TEST_MODE}" == "true" ]]; then
    [[ -e "${AGENT_SOCKET}" && ! -L "${AGENT_SOCKET}" ]]
  else
    [[ -S "${AGENT_SOCKET}" ]]
  fi
}

wait_for_agent_ready() {
  local max_attempts=30
  local retry_seconds=1
  local deadline=$((SECONDS + 30))
  local attempt
  local attempts_run=0
  local active_state

  if [[ "${TEST_MODE}" == "true" ]]; then
    max_attempts="${BLOCK_DEPLOY_TEST_AGENT_READY_ATTEMPTS:-5}"
    retry_seconds="${BLOCK_DEPLOY_TEST_AGENT_READY_INTERVAL_SECONDS:-0}"
    [[ "${max_attempts}" =~ ^[1-9][0-9]*$ && "${max_attempts}" -le 120 ]] ||
      die "BLOCK_DEPLOY_TEST_AGENT_READY_ATTEMPTS must be between 1 and 120"
    [[ "${retry_seconds}" =~ ^[0-9]+$ && "${retry_seconds}" -le 10 ]] ||
      die "BLOCK_DEPLOY_TEST_AGENT_READY_INTERVAL_SECONDS must be between 0 and 10"
  fi

  for ((attempt = 1; attempt <= max_attempts; attempt++)); do
    attempts_run="${attempt}"
    if systemctl is-active --quiet block-agent.service &&
      agent_socket_exists &&
      curl --fail --silent --show-error \
        --connect-timeout 1 \
        --max-time 2 \
        --proto '=http' \
        --unix-socket "${AGENT_SOCKET}" \
        http://localhost/healthz >/dev/null 2>&1; then
      printf 'NOTICE: Agent UDS ready after %d/%d probe(s): %s\n' \
        "${attempt}" "${max_attempts}" "${AGENT_SOCKET}"
      return 0
    fi
    if ((attempt < max_attempts)); then
      if [[ "${TEST_MODE}" != "true" && "${SECONDS}" -ge "${deadline}" ]]; then
        break
      fi
      sleep "${retry_seconds}"
    fi
  done

  active_state="$(systemctl is-active block-agent.service 2>/dev/null || true)"
  [[ -n "${active_state}" ]] || active_state="unknown"
  printf 'ERROR: Agent did not become ready after %d probe(s) (unit=%s, socket=%s); refusing to start HMI\n' \
    "${attempts_run}" "${active_state}" "${AGENT_SOCKET}" >&2
  return 1
}

trap 'install_error_trap "$?" "${LINENO}"' ERR

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --execute)
      execute_confirmed="true"
      shift
      ;;
    --profile|--version|--artifact-dir|--agent-config|--simulator-config|--tls-cert|--tls-key|--tls-ca|--bdm-ca|--bdm-client-cert|--bdm-client-key|--git-commit|--common-baseline|--hmi-url|--control-admin)
      [[ "$#" -ge 2 ]] || die "missing value for $1"
      option="$1"
      value="$2"
      shift 2
      case "${option}" in
        --profile) profile="${value}" ;;
        --version) version="${value}" ;;
        --artifact-dir) artifact_dir="${value}" ;;
        --agent-config) agent_config="${value}" ;;
        --simulator-config) simulator_config="${value}" ;;
        --tls-cert) tls_cert="${value}" ;;
        --tls-key) tls_key="${value}" ;;
        --tls-ca) tls_ca="${value}" ;;
        --bdm-ca) bdm_ca="${value}" ;;
        --bdm-client-cert) bdm_client_cert="${value}" ;;
        --bdm-client-key) bdm_client_key="${value}" ;;
        --git-commit) git_commit="${value}" ;;
        --common-baseline) common_baseline="${value}" ;;
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

[[ "${execute_confirmed}" == "true" ]] || die "--execute is required"
[[ "${EUID}" -eq 0 ]] || die "run as root"
[[ "${BLOCK_RELEASE_ROLE:-}" == "BLK-REL" ]] ||
  die "set BLOCK_RELEASE_ROLE=BLK-REL; only BLK-REL may change a host"
[[ "${profile}" == "production" || "${profile}" == "lab" ]] ||
  die "--profile must be production or lab"
[[ "${version}" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$ ]] ||
  die "VERSION contains unsafe characters"
case "${hmi_url}" in
  https://127.0.0.1:8443/*|https://localhost:8443/*|https://\[::1\]:8443/*) ;;
  *) die "--hmi-url must be loopback HTTPS on port 8443" ;;
esac

for command_name in \
  awk basename cat chmod chown cmp cp curl cut date dirname flock getent grep \
  install ln mountpoint mv openssl python3 readlink rm sha256sum sleep stat systemctl tr; do
  require_command "${command_name}"
done

validate_single_line "git commit" "${git_commit}"
validate_single_line "Common baseline" "${common_baseline}"
validate_single_line "HMI URL" "${hmi_url}"
[[ "${git_commit}" =~ ^[0-9A-Fa-f]{40,64}$ ]] ||
  die "--git-commit must be a full hexadecimal commit ID"
[[ "${common_baseline}" =~ ^[0-9A-Fa-f]{40,64}$ ]] ||
  die "--common-baseline must be a full hexadecimal commit ID"
for admin_name in "${control_admins[@]}"; do
  [[ "${admin_name}" =~ ^[a-z_][a-z0-9_-]*[$]?$ ]] ||
    die "unsafe --control-admin name: ${admin_name}"
done

[[ -d "${artifact_dir}" ]] || die "artifact directory is missing: ${artifact_dir}"
require_regular_file "${artifact_dir}/bin/block-agent"
require_regular_file "${artifact_dir}/bin/block-hmi"
[[ -x "${artifact_dir}/bin/block-agent" && -x "${artifact_dir}/bin/block-hmi" ]] ||
  die "Block binaries must be executable"
require_regular_file "${agent_config}"
require_regular_file "${tls_cert}"
require_regular_file "${tls_key}"
require_regular_file "${tls_ca}"
for source_script in \
  install.sh \
  install-users.sh \
  health-check.sh \
  verify-install.sh \
  rollback.sh \
  verify-static.sh \
  tests/deploy-regression.sh; do
  [[ -x "${SCRIPT_DIR}/${source_script}" ]] ||
    die "deployment script must be executable: ${SCRIPT_DIR}/${source_script}"
done
validate_json_config "${agent_config}"
validate_private_key_mode "${tls_key}"
reject_private_key_blocks "HMI certificate" "${tls_cert}"
reject_private_key_blocks "trusted CA bundle" "${tls_ca}"
verify_certificate_material

if [[ "${profile}" == "lab" ]]; then
  require_regular_file "${artifact_dir}/bin/plc-simulator"
  [[ -x "${artifact_dir}/bin/plc-simulator" ]] ||
    die "plc-simulator must be executable"
  [[ -n "${simulator_config}" ]] || die "lab profile requires --simulator-config"
  require_regular_file "${simulator_config}"
  validate_json_config "${simulator_config}"
elif [[ -n "${simulator_config}" ]]; then
  die "--simulator-config is accepted only with --profile lab"
fi
validate_profile_config
if [[ "${bdm_enabled}" == "True" ]]; then
  [[ -n "${bdm_ca}" && -n "${bdm_client_cert}" && -n "${bdm_client_key}" ]] ||
    die "bdm.enabled=true requires --bdm-ca, --bdm-client-cert and --bdm-client-key"
  require_regular_file "${bdm_ca}"
  require_regular_file "${bdm_client_cert}"
  require_regular_file "${bdm_client_key}"
  validate_private_key_mode "${bdm_client_key}"
  reject_private_key_blocks "BDM client certificate" "${bdm_client_cert}"
  reject_private_key_blocks "BDM trusted CA bundle" "${bdm_ca}"
  verify_bdm_certificate_material
elif [[ -n "${bdm_ca}" || -n "${bdm_client_cert}" || -n "${bdm_client_key}" ]]; then
  die "BDM certificate arguments are accepted only when bdm.enabled=true"
fi

release_dir="${RELEASE_ROOT}/${version}"
agent_config_hash="$(sha256sum "${agent_config}" | awk '{print $1}')"
simulator_config_hash="not-installed"
if [[ "${profile}" == "lab" ]]; then
  simulator_config_hash="$(sha256sum "${simulator_config}" | awk '{print $1}')"
fi

ensure_directory_exists_without_relabel "${LOCK_ROOT}" root root 0755
exec 9>"${LOCK_ROOT}/block-release.lock"
flock -n 9 || die "another Block install or rollback is running"

if [[ "${TEST_MODE}" != "true" ]]; then
  "${SCRIPT_DIR}/install-users.sh"
fi
validate_data_root

ensure_directory_exists_without_relabel "${OPT_ROOT}" root root 0755
ensure_directory_exists_without_relabel "${RELEASE_ROOT}" root root 0755
ensure_directory_exists_without_relabel "${STATE_ROOT}" root root 0700
ensure_directory_exists_without_relabel "${STATE_ROOT}/transactions" root root 0700
ensure_directory_exists_without_relabel "${CONFIG_ROOT}" root root 0755
ensure_directory_exists_without_relabel \
  "${CONFIG_ROOT}/certs" root "${HMI_FILE_GROUP}" 0750
ensure_directory_exists_without_relabel \
  "${CONFIG_ROOT}/bdm-certs" root "${AGENT_FILE_GROUP}" 0750
maybe_failpoint "after-directory-preflight"

release_reused="false"
if [[ -e "${release_dir}" || -L "${release_dir}" ]]; then
  [[ -d "${release_dir}" && ! -L "${release_dir}" ]] ||
    die "existing release path is not a safe directory: ${release_dir}"
  [[ -f "${release_dir}/manifest.txt" ]] ||
    die "existing release is incomplete: ${release_dir}"
  grep -Fqx "version=${version}" "${release_dir}/manifest.txt" ||
    die "existing release has a different version manifest"
  grep -Fqx "profile=${profile}" "${release_dir}/manifest.txt" ||
    die "existing release has a different profile"
  grep -Fqx "git_commit=${git_commit}" "${release_dir}/manifest.txt" ||
    die "existing release has a different Git commit"
  grep -Fqx "common_baseline=${common_baseline}" "${release_dir}/manifest.txt" ||
    die "existing release has a different Common baseline"
  grep -Fqx "agent_config_sha256=${agent_config_hash}" "${release_dir}/manifest.txt" ||
    die "existing release has a different Agent configuration hash"
  grep -Fqx "simulator_config_sha256=${simulator_config_hash}" "${release_dir}/manifest.txt" ||
    die "existing release has a different Simulator configuration hash"
  cmp -s "${artifact_dir}/bin/block-agent" "${release_dir}/bin/block-agent" ||
    die "existing release block-agent differs from the supplied artifact"
  cmp -s "${artifact_dir}/bin/block-hmi" "${release_dir}/bin/block-hmi" ||
    die "existing release block-hmi differs from the supplied artifact"
  if [[ "${profile}" == "lab" ]]; then
    cmp -s "${artifact_dir}/bin/plc-simulator" "${release_dir}/bin/plc-simulator" ||
      die "existing release plc-simulator differs from the supplied artifact"
  elif [[ -e "${release_dir}/bin/plc-simulator" ]]; then
    die "production release unexpectedly contains plc-simulator"
  fi
  for deploy_script in \
    health-check.sh \
    install-users.sh \
    install.sh \
    rollback.sh \
    verify-install.sh \
    verify-static.sh \
    tests/deploy-regression.sh; do
    cmp -s "${SCRIPT_DIR}/${deploy_script}" "${release_dir}/deploy/${deploy_script}" ||
      die "existing release ${deploy_script} differs from the supplied deployment bundle"
  done
  for config_example in \
    block-agent.example.json \
    block-agent-bdm.example.json \
    block-agent-simulator-bdm.example.json \
    block-agent-simulator.example.json \
    plc-simulator.example.json; do
    cmp -s \
      "${SCRIPT_DIR}/config/${config_example}" \
      "${release_dir}/deploy/config/${config_example}" ||
      die "existing release ${config_example} differs from the supplied deployment bundle"
  done
  cmp -s "${SCRIPT_DIR}/README.md" "${release_dir}/deploy/README.md" ||
    die "existing release README differs from the supplied deployment bundle"
  for metadata_spec in \
    "${release_dir}/bin/block-agent:root:root:755" \
    "${release_dir}/bin/block-hmi:root:root:755" \
    "${release_dir}/deploy/verify-static.sh:root:root:755" \
    "${release_dir}/deploy/tests/deploy-regression.sh:root:root:755" \
    "${release_dir}/deploy/config/block-agent.example.json:root:root:644" \
    "${release_dir}/deploy/config/block-agent-bdm.example.json:root:root:644" \
    "${release_dir}/deploy/config/block-agent-simulator-bdm.example.json:root:root:644" \
    "${release_dir}/deploy/config/block-agent-simulator.example.json:root:root:644" \
    "${release_dir}/deploy/config/plc-simulator.example.json:root:root:644" \
    "${release_dir}/manifest.txt:root:root:644"; do
    metadata_path="${metadata_spec%%:*}"
    metadata_expected="${metadata_spec#*:}"
    metadata_matches_expected "${metadata_path}" "${metadata_expected}" ||
      die "existing release has unsafe metadata: ${metadata_path}"
  done
  if [[ "${profile}" == "lab" ]]; then
    metadata_matches_expected "${release_dir}/bin/plc-simulator" "root:root:755" ||
      die "existing release has unsafe plc-simulator metadata"
  fi
  for unit_name in block-agent.service block-hmi.service block-plc-simulator.service; do
    cmp -s "${SCRIPT_DIR}/systemd/${unit_name}" "${release_dir}/deploy/systemd/${unit_name}" ||
      die "existing release ${unit_name} differs from the supplied deployment bundle"
  done
  release_reused="true"
fi

same_current="false"
if [[ -L "${CURRENT_LINK}" && "$(readlink -f "${CURRENT_LINK}")" == "${release_dir}" ]]; then
  same_current="true"
fi

host_matches_desired="false"
if [[ "${same_current}" == "true" ]] &&
  cmp -s "${agent_config}" "${CONFIG_ROOT}/block-agent.json" &&
  cmp -s "${tls_cert}" "${CONFIG_ROOT}/certs/block-hmi.crt" &&
  cmp -s "${tls_key}" "${CONFIG_ROOT}/certs/block-hmi.key" &&
  cmp -s "${tls_ca}" "${CONFIG_ROOT}/certs/ca.crt"; then
  host_matches_desired="true"
  if [[ "${bdm_enabled}" == "True" ]] &&
    { ! cmp -s "${bdm_ca}" "${CONFIG_ROOT}/bdm-certs/ca.crt" ||
      ! cmp -s "${bdm_client_cert}" "${CONFIG_ROOT}/bdm-certs/client.crt" ||
      ! cmp -s "${bdm_client_key}" "${CONFIG_ROOT}/bdm-certs/client.key"; }; then
    host_matches_desired="false"
  fi
  if [[ "${profile}" == "lab" ]] &&
    ! cmp -s "${simulator_config}" "${CONFIG_ROOT}/plc-simulator.json"; then
    host_matches_desired="false"
  elif [[ "${profile}" == "production" &&
    ( -e "${CONFIG_ROOT}/plc-simulator.json" || -L "${CONFIG_ROOT}/plc-simulator.json" ) ]]; then
    host_matches_desired="false"
  fi
  for unit_name in block-agent.service block-hmi.service block-plc-simulator.service; do
    if ! cmp -s \
      "${release_dir}/deploy/systemd/${unit_name}" \
      "${SYSTEMD_ROOT}/${unit_name}"; then
      host_matches_desired="false"
    fi
  done
  for metadata_spec in \
    "${CONFIG_ROOT}/block-agent.json:root:${AGENT_FILE_GROUP}:640" \
    "${CONFIG_ROOT}/certs/block-hmi.crt:root:root:644" \
    "${CONFIG_ROOT}/certs/ca.crt:root:root:644" \
    "${CONFIG_ROOT}/certs/block-hmi.key:root:${HMI_FILE_GROUP}:640" \
    "${release_dir}/bin/block-agent:root:root:755" \
    "${release_dir}/bin/block-hmi:root:root:755" \
    "${release_dir}/manifest.txt:root:root:644"; do
    metadata_path="${metadata_spec%%:*}"
    metadata_expected="${metadata_spec#*:}"
    if ! metadata_matches_expected "${metadata_path}" "${metadata_expected}"; then
      host_matches_desired="false"
    fi
  done
  if [[ "${profile}" == "lab" ]]; then
    metadata_matches_expected "${CONFIG_ROOT}/plc-simulator.json" "root:${SIMULATOR_FILE_GROUP}:640" ||
      host_matches_desired="false"
    metadata_matches_expected "${release_dir}/bin/plc-simulator" "root:root:755" ||
      host_matches_desired="false"
  fi
  if [[ "${bdm_enabled}" == "True" ]]; then
    for metadata_spec in \
      "${CONFIG_ROOT}/bdm-certs/ca.crt:root:${AGENT_FILE_GROUP}:640" \
      "${CONFIG_ROOT}/bdm-certs/client.crt:root:${AGENT_FILE_GROUP}:640" \
      "${CONFIG_ROOT}/bdm-certs/client.key:root:${AGENT_FILE_GROUP}:640"; do
      metadata_path="${metadata_spec%%:*}"
      metadata_expected="${metadata_spec#*:}"
      metadata_matches_expected "${metadata_path}" "${metadata_expected}" ||
        host_matches_desired="false"
    done
  fi
  for unit_name in block-agent.service block-hmi.service block-plc-simulator.service; do
    metadata_matches_expected "${SYSTEMD_ROOT}/${unit_name}" "root:root:644" ||
      host_matches_desired="false"
  done
  for metadata_spec in \
    "${OPT_ROOT}:root:root:755" \
    "${RELEASE_ROOT}:root:root:755" \
    "${STATE_ROOT}:root:root:700" \
    "${STATE_ROOT}/transactions:root:root:700" \
    "${CONFIG_ROOT}:root:root:755" \
    "${CONFIG_ROOT}/certs:root:${HMI_FILE_GROUP}:750" \
    "${CONFIG_ROOT}/bdm-certs:root:${AGENT_FILE_GROUP}:750"; do
    metadata_path="${metadata_spec%%:*}"
    metadata_expected="${metadata_spec#*:}"
    if [[ "${TEST_MODE}" == "true" &&
      "${BLOCK_DEPLOY_TEST_SKIP_METADATA:-false}" == "true" ]]; then
      [[ -d "${metadata_path}" && ! -L "${metadata_path}" ]] ||
        host_matches_desired="false"
    elif [[ "$(stat -c '%U:%G:%a' "${metadata_path}" 2>/dev/null || true)" != "${metadata_expected}" ]]; then
      host_matches_desired="false"
    fi
  done
fi

if [[ "${host_matches_desired}" == "true" ]]; then
  systemctl daemon-reload
  if [[ "${profile}" == "lab" ]]; then
    systemctl enable block-plc-simulator.service block-agent.service block-hmi.service
    systemctl restart block-plc-simulator.service
  else
    systemctl disable --now block-plc-simulator.service >/dev/null 2>&1 || true
    systemctl enable block-agent.service block-hmi.service
  fi
  systemctl restart block-agent.service
  wait_for_agent_ready
  systemctl restart block-hmi.service
  verify_args=(
    --profile "${profile}"
    --ca "${CONFIG_ROOT}/certs/ca.crt"
    --hmi-url "${hmi_url}"
  )
  for admin_name in "${control_admins[@]}"; do
    verify_args+=(--control-admin "${admin_name}")
  done
  if [[ "${TEST_MODE}" == "true" &&
    "${BLOCK_DEPLOY_TEST_SKIP_VERIFY:-false}" == "true" ]]; then
    printf 'NOTICE: test mode skipped host verification\n'
  else
    "${release_dir}/deploy/verify-install.sh" "${verify_args[@]}"
  fi
  printf 'OK: release %s already matched; services were converged and verified\n' "${version}"
  exit 0
fi

readonly timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
tx_dir="${STATE_ROOT}/transactions/${version}.${timestamp}.$$"
staging_dir="${RELEASE_ROOT}/.${version}.staging.$$"
install -d -o root -g root -m 0700 "${tx_dir}"
: >"${tx_dir}/missing-files"
: >"${tx_dir}/unit-state.tsv"
capture_managed_directory_states "${tx_dir}"

if [[ -L "${CURRENT_LINK}" ]]; then
  previous_current="$(readlink -f "${CURRENT_LINK}")"
  [[ "${previous_current}" == "${RELEASE_ROOT}/"* ]] ||
    die "refusing unexpected current target: ${previous_current}"
  printf '%s\n' "${previous_current}" >"${tx_dir}/previous-current"
elif [[ -e "${CURRENT_LINK}" ]]; then
  die "${CURRENT_LINK} exists and is not a symlink"
fi

if [[ -f "${STATE_ROOT}/current-transaction" ]]; then
  cp -a "${STATE_ROOT}/current-transaction" "${tx_dir}/previous-transaction"
fi
if [[ -f "${STATE_ROOT}/current-profile" ]]; then
  cp -a "${STATE_ROOT}/current-profile" "${tx_dir}/previous-profile"
fi

for managed_path in \
  "${SYSTEMD_ROOT}/block-agent.service" \
  "${SYSTEMD_ROOT}/block-hmi.service" \
  "${SYSTEMD_ROOT}/block-plc-simulator.service" \
  "${CONFIG_ROOT}/block-agent.json" \
  "${CONFIG_ROOT}/plc-simulator.json" \
  "${CONFIG_ROOT}/block-profile.env"; do
  [[ ! -L "${managed_path}" ]] || die "managed path must not be a symlink: ${managed_path}"
  backup_file "${managed_path}" "${tx_dir}"
done
for certificate_path in \
  "${CONFIG_ROOT}/certs/block-hmi.crt" \
  "${CONFIG_ROOT}/certs/block-hmi.key" \
  "${CONFIG_ROOT}/certs/ca.crt" \
  "${CONFIG_ROOT}/bdm-certs/ca.crt" \
  "${CONFIG_ROOT}/bdm-certs/client.crt" \
  "${CONFIG_ROOT}/bdm-certs/client.key"; do
  [[ ! -L "${certificate_path}" ]] ||
    die "certificate destination must not be a symlink: ${certificate_path}"
  backup_file "${certificate_path}" "${tx_dir}"
done

for unit_name in \
  block-plc-simulator.service \
  block-agent.service \
  block-hmi.service; do
  capture_unit_state "${unit_name}" "${tx_dir}"
done
transaction_armed="true"

install -d -o root -g root -m 0755 "${OPT_ROOT}" "${RELEASE_ROOT}"
install -d -o root -g root -m 0700 "${STATE_ROOT}" "${STATE_ROOT}/transactions"
install -d -o root -g root -m 0755 "${CONFIG_ROOT}"
install -d -o root -g "${HMI_FILE_GROUP}" -m 0750 "${CONFIG_ROOT}/certs"
install -d -o root -g "${AGENT_FILE_GROUP}" -m 0750 "${CONFIG_ROOT}/bdm-certs"

if [[ "${release_reused}" == "false" ]]; then
  install -d -o root -g root -m 0755 \
    "${staging_dir}/bin" \
    "${staging_dir}/deploy" \
    "${staging_dir}/deploy/config" \
    "${staging_dir}/deploy/tests" \
    "${staging_dir}/deploy/systemd"
  install -o root -g root -m 0755 \
    "${artifact_dir}/bin/block-agent" \
    "${staging_dir}/bin/block-agent"
  install -o root -g root -m 0755 \
    "${artifact_dir}/bin/block-hmi" \
    "${staging_dir}/bin/block-hmi"
  if [[ "${profile}" == "lab" ]]; then
    install -o root -g root -m 0755 \
      "${artifact_dir}/bin/plc-simulator" \
      "${staging_dir}/bin/plc-simulator"
  fi

  for deploy_script in \
    health-check.sh \
    install-users.sh \
    install.sh \
    rollback.sh \
    verify-install.sh \
    verify-static.sh \
    tests/deploy-regression.sh; do
    install -o root -g root -m 0755 \
      "${SCRIPT_DIR}/${deploy_script}" \
      "${staging_dir}/deploy/${deploy_script}"
  done
  for config_example in \
    block-agent.example.json \
    block-agent-bdm.example.json \
    block-agent-simulator-bdm.example.json \
    block-agent-simulator.example.json \
    plc-simulator.example.json; do
    install -o root -g root -m 0644 \
      "${SCRIPT_DIR}/config/${config_example}" \
      "${staging_dir}/deploy/config/${config_example}"
  done
  install -o root -g root -m 0644 \
    "${SCRIPT_DIR}/README.md" \
    "${staging_dir}/deploy/README.md"
  for unit_name in block-agent.service block-hmi.service block-plc-simulator.service; do
    install -o root -g root -m 0644 \
      "${SCRIPT_DIR}/systemd/${unit_name}" \
      "${staging_dir}/deploy/systemd/${unit_name}"
  done

  agent_hash="$(sha256sum "${staging_dir}/bin/block-agent" | awk '{print $1}')"
  hmi_hash="$(sha256sum "${staging_dir}/bin/block-hmi" | awk '{print $1}')"
  simulator_hash="not-installed"
  if [[ "${profile}" == "lab" ]]; then
    simulator_hash="$(sha256sum "${staging_dir}/bin/plc-simulator" | awk '{print $1}')"
  fi
  certificate_fingerprint="$(openssl x509 -in "${tls_cert}" -noout -fingerprint -sha256 | cut -d= -f2)"
  bdm_certificate_fingerprint="not-enabled"
  if [[ "${bdm_enabled}" == "True" ]]; then
    bdm_certificate_fingerprint="$(
      openssl x509 -in "${bdm_client_cert}" -noout -fingerprint -sha256 |
        cut -d= -f2
    )"
  fi
  previous_version="none"
  if [[ -f "${tx_dir}/previous-current" ]]; then
    previous_version="$(basename -- "$(cat "${tx_dir}/previous-current")")"
  fi

  cat >"${staging_dir}/manifest.txt" <<EOF
version=${version}
profile=${profile}
git_commit=${git_commit}
common_baseline=${common_baseline}
installed_at_utc=${timestamp}
previous_version=${previous_version}
block_agent_sha256=${agent_hash}
block_hmi_sha256=${hmi_hash}
plc_simulator_sha256=${simulator_hash}
agent_config_sha256=${agent_config_hash}
simulator_config_sha256=${simulator_config_hash}
hmi_certificate_sha256=${certificate_fingerprint}
bdm_enabled=${bdm_enabled}
bdm_client_certificate_sha256=${bdm_certificate_fingerprint}
transaction=${tx_dir}
EOF
  chmod 0644 "${staging_dir}/manifest.txt"
  mv -T "${staging_dir}" "${release_dir}"
else
  previous_version="none"
  if [[ -f "${tx_dir}/previous-current" ]]; then
    previous_version="$(basename -- "$(cat "${tx_dir}/previous-current")")"
  fi
fi

printf '%s\n' "${release_dir}" >"${tx_dir}/installed-release"
sha256sum "${release_dir}/manifest.txt" |
  awk '{print $1}' >"${tx_dir}/release-manifest.sha256"
chmod 0600 "${tx_dir}/installed-release" "${tx_dir}/release-manifest.sha256"
atomic_write_line "${STATE_ROOT}/current-transaction" "${tx_dir}"
maybe_failpoint "after-transaction-marker"

install -o root -g "${AGENT_FILE_GROUP}" -m 0640 "${agent_config}" "${CONFIG_ROOT}/block-agent.json"
if [[ "${profile}" == "lab" ]]; then
  install -o root -g "${SIMULATOR_FILE_GROUP}" -m 0640 \
    "${simulator_config}" \
    "${CONFIG_ROOT}/plc-simulator.json"
else
  rm -f -- "${CONFIG_ROOT}/plc-simulator.json"
fi
install -o root -g root -m 0644 "${tls_cert}" "${CONFIG_ROOT}/certs/block-hmi.crt"
install -o root -g root -m 0644 "${tls_ca}" "${CONFIG_ROOT}/certs/ca.crt"
install -o root -g "${HMI_FILE_GROUP}" -m 0640 "${tls_key}" "${CONFIG_ROOT}/certs/block-hmi.key"
if [[ "${bdm_enabled}" == "True" ]]; then
  install -o root -g "${AGENT_FILE_GROUP}" -m 0640 \
    "${bdm_ca}" "${CONFIG_ROOT}/bdm-certs/ca.crt"
  install -o root -g "${AGENT_FILE_GROUP}" -m 0640 \
    "${bdm_client_cert}" "${CONFIG_ROOT}/bdm-certs/client.crt"
  install -o root -g "${AGENT_FILE_GROUP}" -m 0640 \
    "${bdm_client_key}" "${CONFIG_ROOT}/bdm-certs/client.key"
fi
rm -f -- "${CONFIG_ROOT}/block-profile.env"
maybe_failpoint "after-config"

for unit_name in block-agent.service block-hmi.service block-plc-simulator.service; do
  install -o root -g root -m 0644 \
    "${release_dir}/deploy/systemd/${unit_name}" \
    "${SYSTEMD_ROOT}/${unit_name}"
done
maybe_failpoint "after-units"

temporary_link="${OPT_ROOT}/.current.new.$$"
ln -s "${release_dir}" "${temporary_link}"
mv -fT "${temporary_link}" "${CURRENT_LINK}"
atomic_write_line "${STATE_ROOT}/current-profile" "${profile}"
maybe_failpoint "after-current-switch"

systemctl daemon-reload
if [[ "${profile}" == "lab" ]]; then
  systemctl enable block-plc-simulator.service block-agent.service block-hmi.service
  systemctl restart block-plc-simulator.service
else
  systemctl disable --now block-plc-simulator.service >/dev/null 2>&1 || true
  systemctl enable block-agent.service block-hmi.service
fi
systemctl restart block-agent.service
wait_for_agent_ready
systemctl restart block-hmi.service
maybe_failpoint "after-service-restart"

verify_args=(
  --profile "${profile}"
  --ca "${CONFIG_ROOT}/certs/ca.crt"
  --hmi-url "${hmi_url}"
)
for admin_name in "${control_admins[@]}"; do
  verify_args+=(--control-admin "${admin_name}")
done
if [[ "${TEST_MODE}" == "true" &&
  "${BLOCK_DEPLOY_TEST_SKIP_VERIFY:-false}" == "true" ]]; then
  printf 'NOTICE: test mode skipped host verification\n'
elif ! "${release_dir}/deploy/verify-install.sh" "${verify_args[@]}"; then
  printf 'ERROR: post-install verification failed\n' >&2
  restore_failed_install "post-install verification failed" || true
  exit 1
fi

install_succeeded="true"
transaction_armed="false"
trap - ERR
printf 'OK: installed Block release %s (%s); previous release: %s\n' \
  "${version}" "${profile}" "${previous_version}"
