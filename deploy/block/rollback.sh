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
readonly SYSTEMD_ROOT="${HOST_ROOT}/etc/systemd/system"
readonly CONFIG_ROOT="${HOST_ROOT}/etc/block"
readonly LOCK_ROOT="${HOST_ROOT}/run/lock"

ca_file="${CONFIG_ROOT}/certs/ca.crt"
hmi_url="https://127.0.0.1:8443/healthz"
execute_confirmed="false"

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage:
  sudo env BLOCK_RELEASE_ROLE=BLK-REL ./rollback.sh --execute \
    [--ca /etc/block/certs/ca.crt] \
    [--hmi-url https://LOOPBACK:8443/healthz]

The rollback restores the previous /opt/block/current target, managed unit
files, non-secret application configuration and prior enable/active state.
It does not delete releases, SQLite data, Simulator state, logs or certificates.
EOF
}

atomic_write_line() {
  local destination="$1"
  local value="$2"
  local temporary="${destination}.new.$$"

  printf '%s\n' "${value}" >"${temporary}"
  chmod 0600 "${temporary}"
  mv -fT "${temporary}" "${destination}"
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
      "${CONFIG_ROOT}/certs")
      return 0
      ;;
    *)
      return 1
      ;;
  esac
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

  if [[ ! -e "${state_file}" && ! -L "${state_file}" ]]; then
    printf 'NOTICE: legacy transaction has no managed-directory metadata; existing directories will not be relabelled\n'
    return
  fi
  [[ -f "${state_file}" && ! -L "${state_file}" ]] ||
    die "transaction managed-directory metadata is unsafe: ${state_file}"
  while IFS=$'\t' read -r path owner group mode extra; do
    if [[ -z "${path}" || -n "${extra}" ]] ||
      ! is_managed_directory_path "${path}" ||
      [[ ! "${owner}" =~ ^[0-9]+$ || ! "${group}" =~ ^[0-9]+$ ||
        ! "${mode}" =~ ^[0-7]{3,4}$ ]] ||
      [[ -n "${seen["${path}"]+present}" ]]; then
      die "invalid managed-directory metadata in ${state_file}"
    fi
    seen["${path}"]="present"
    if [[ -e "${path}" || -L "${path}" ]]; then
      [[ -d "${path}" && ! -L "${path}" ]] ||
        die "refusing unsafe managed directory during rollback: ${path}"
      chown --no-dereference -- "${owner}:${group}" "${path}"
      chmod -- "${mode}" "${path}"
    else
      install -d -o "${owner}" -g "${group}" -m "${mode}" "${path}"
    fi
  done <"${state_file}"
  for expected_path in \
    "${OPT_ROOT}" \
    "${RELEASE_ROOT}" \
    "${STATE_ROOT}" \
    "${STATE_ROOT}/transactions" \
    "${CONFIG_ROOT}" \
    "${CONFIG_ROOT}/certs"; do
    [[ -n "${seen["${expected_path}"]+present}" ]] ||
      die "transaction is missing managed-directory metadata for ${expected_path}"
  done
}

require_safe_restore_parent() {
  local path="$1"
  local parent

  parent="$(dirname -- "${path}")"
  [[ -d "${parent}" && ! -L "${parent}" ]] ||
    die "restore parent is missing or unsafe: ${parent}"
}

restore_path() {
  local path="$1"
  local tx_dir="$2"
  local backup="${tx_dir}/files/${path#/}"

  if [[ -e "${backup}" || -L "${backup}" ]]; then
    require_safe_restore_parent "${path}"
    rm -f -- "${path}"
    cp -a --no-dereference "${backup}" "${path}"
  elif grep -Fqx -- "${path}" "${tx_dir}/missing-files"; then
    rm -f -- "${path}"
  else
    die "transaction has no backup decision for ${path}"
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
      die "unsupported previous enablement state for ${unit}: ${state}"
      ;;
  esac
}

previous_active_state() {
  local wanted_unit="$1"
  awk -F'\t' -v wanted="${wanted_unit}" '$1 == wanted { print $3 }' "${tx_dir}/unit-state.tsv"
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --execute)
      execute_confirmed="true"
      shift
      ;;
    --ca|--hmi-url)
      [[ "$#" -ge 2 ]] || die "missing value for $1"
      option="$1"
      value="$2"
      shift 2
      case "${option}" in
        --ca) ca_file="${value}" ;;
        --hmi-url) hmi_url="${value}" ;;
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
case "${hmi_url}" in
  https://127.0.0.1:8443/*|https://localhost:8443/*|https://\[::1\]:8443/*) ;;
  *) die "--hmi-url must be loopback HTTPS on port 8443" ;;
esac
[[ -f "${STATE_ROOT}/current-transaction" && ! -L "${STATE_ROOT}/current-transaction" ]] ||
  die "there is no recorded installation transaction to roll back"
[[ -L "${CURRENT_LINK}" ]] || die "${CURRENT_LINK} must be a symlink"

for command_name in \
  awk chmod chown cp curl date dirname flock grep install ln mv readlink rm \
  sha256sum systemctl; do
  command -v "${command_name}" >/dev/null 2>&1 ||
    die "required command is missing: ${command_name}"
done

ensure_directory_exists_without_relabel "${LOCK_ROOT}" root root 0755
exec 9>"${LOCK_ROOT}/block-release.lock"
flock -n 9 || die "another Block install or rollback is running"

tx_dir="$(cat "${STATE_ROOT}/current-transaction")"
[[ "${tx_dir}" == "${STATE_ROOT}/transactions/"* ]] ||
  die "invalid transaction path: ${tx_dir}"
[[ -d "${tx_dir}" && ! -L "${tx_dir}" ]] ||
  die "transaction directory is missing or unsafe: ${tx_dir}"
[[ "$(readlink -f "${tx_dir}")" == "${tx_dir}" ]] ||
  die "transaction path must be canonical: ${tx_dir}"
current_target="$(readlink -f "${CURRENT_LINK}")"
[[ "${current_target}" == "${RELEASE_ROOT}/"* &&
  -d "${current_target}" && ! -L "${current_target}" ]] ||
  die "current release target is missing or outside ${RELEASE_ROOT}"
[[ -f "${current_target}/manifest.txt" && ! -L "${current_target}/manifest.txt" ]] ||
  die "current release manifest is missing or unsafe"
[[ -f "${tx_dir}/installed-release" && ! -L "${tx_dir}/installed-release" &&
  -f "${tx_dir}/release-manifest.sha256" && ! -L "${tx_dir}/release-manifest.sha256" ]] ||
  die "current transaction lacks release binding evidence"
bound_release="$(cat "${tx_dir}/installed-release")"
expected_manifest_hash="$(cat "${tx_dir}/release-manifest.sha256")"
[[ "${bound_release}" == "${current_target}" &&
  "${expected_manifest_hash}" =~ ^[0-9a-f]{64}$ &&
  "$(sha256sum "${current_target}/manifest.txt" | awk '{print $1}')" == "${expected_manifest_hash}" ]] ||
  die "current transaction is not bound to the current release manifest"
[[ -f "${tx_dir}/previous-current" ]] ||
  die "this was a fresh install; no previous release exists"
[[ -f "${tx_dir}/unit-state.tsv" && -f "${tx_dir}/missing-files" ]] ||
  die "transaction is incomplete: ${tx_dir}"

previous_current="$(cat "${tx_dir}/previous-current")"
[[ "${previous_current}" == "${RELEASE_ROOT}/"* ]] ||
  die "invalid previous release path: ${previous_current}"
[[ -d "${previous_current}" && ! -L "${previous_current}" ]] ||
  die "previous release is missing or unsafe: ${previous_current}"

previous_profile="production"
if [[ -f "${tx_dir}/previous-profile" ]]; then
  previous_profile="$(cat "${tx_dir}/previous-profile")"
  [[ "${previous_profile}" == "production" || "${previous_profile}" == "lab" ]] ||
    die "invalid previous profile: ${previous_profile}"
fi

systemctl stop block-hmi.service >/dev/null 2>&1 || true
systemctl stop block-agent.service >/dev/null 2>&1 || true
systemctl stop block-plc-simulator.service >/dev/null 2>&1 || true

restore_managed_directory_states "${tx_dir}"
for managed_path in \
  "${SYSTEMD_ROOT}/block-agent.service" \
  "${SYSTEMD_ROOT}/block-hmi.service" \
  "${SYSTEMD_ROOT}/block-plc-simulator.service" \
  "${CONFIG_ROOT}/block-agent.json" \
  "${CONFIG_ROOT}/plc-simulator.json" \
  "${CONFIG_ROOT}/block-profile.env"; do
  restore_path "${managed_path}" "${tx_dir}"
done

temporary_link="${OPT_ROOT}/.current.rollback.$$"
ln -s "${previous_current}" "${temporary_link}"
mv -fT "${temporary_link}" "${CURRENT_LINK}"
atomic_write_line "${STATE_ROOT}/current-profile" "${previous_profile}"

systemctl daemon-reload
while IFS=$'\t' read -r unit_name enabled_state active_state; do
  restore_enablement "${unit_name}" "${enabled_state}"
done <"${tx_dir}/unit-state.tsv"

for unit_name in \
  block-plc-simulator.service \
  block-agent.service \
  block-hmi.service; do
  if [[ "$(previous_active_state "${unit_name}")" == "active" ]]; then
    systemctl start "${unit_name}"
  fi
done

agent_was_active="$(previous_active_state block-agent.service)"
hmi_was_active="$(previous_active_state block-hmi.service)"
if [[ "${agent_was_active}" == "active" && "${hmi_was_active}" == "active" ]]; then
  health_ok="false"
  for attempt in 1 2 3; do
    if BLOCK_HMI_CA="${ca_file}" \
      BLOCK_HMI_HEALTH_URL="${hmi_url}" \
      BLOCK_EXPECT_SIMULATOR="$(
        [[ "$(previous_active_state block-plc-simulator.service)" == "active" ]] &&
          printf true ||
          printf false
      )" \
      "${SCRIPT_DIR}/health-check.sh"; then
      health_ok="true"
      break
    fi
    sleep 1
  done
  [[ "${health_ok}" == "true" ]] ||
    die "previous release was restored but its health check failed"
else
  printf 'NOTICE: previous Agent/HMI state was not active; health request was not run\n'
fi

if [[ -f "${tx_dir}/previous-transaction" ]]; then
  previous_transaction="$(cat "${tx_dir}/previous-transaction")"
  [[ "${previous_transaction}" == "${STATE_ROOT}/transactions/"* ]] ||
    die "invalid previous transaction marker"
  atomic_write_line "${STATE_ROOT}/current-transaction" "${previous_transaction}"
else
  rm -f -- "${STATE_ROOT}/current-transaction"
fi
printf '%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"${tx_dir}/rolled-back-at"
chmod 0600 "${tx_dir}/rolled-back-at"

printf 'OK: restored %s; application data and release directories were preserved\n' \
  "${previous_current}"
