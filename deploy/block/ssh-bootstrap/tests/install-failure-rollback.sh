#!/usr/bin/env bash
set -euo pipefail

readonly ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
readonly INSTALL_SCRIPT="${ROOT}/install.sh"
readonly ROLLBACK_SCRIPT="${ROOT}/rollback.sh"

node - "${INSTALL_SCRIPT}" "${ROLLBACK_SCRIPT}" <<'JS'
const fs = require("fs");
const install = fs.readFileSync(process.argv[2], "utf8");
const rollback = fs.readFileSync(process.argv[3], "utf8");

const pointerPublished = install.indexOf('mv -Tf "${pointer}.new" "${pointer}"');
const firstManagedSwitch = install.indexOf(
  "install -d -m 0750 -o root -g ssh-bootstrap /etc/ssh-bootstrap",
);
if (pointerPublished < 0 || firstManagedSwitch < 0 || pointerPublished >= firstManagedSwitch) {
  throw new Error("transaction pointer is not published before the first managed target switch");
}
for (const required of [
  "trap rollback_install_error ERR",
  '"${ROOT}/rollback.sh" --execute',
  'printf \'prepared\\n\' >"${transaction}/state"',
  'printf \'committed\\n\' >"${transaction}/state"',
]) {
  if (!install.includes(required)) {
    throw new Error(`install failure transaction requirement missing: ${required}`);
  }
}
if (install.includes("authorized_keys") || rollback.includes("authorized_keys")) {
  throw new Error("install or rollback must not touch existing authorized_keys");
}
JS

make_mock_commands() {
  local directory="$1"
  mkdir -p "${directory}"
  cat >"${directory}/systemctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  disable)
    printf 'disabled\n' >"${MOCK_SERVICE_STATE}/enabled"
    printf 'inactive\n' >"${MOCK_SERVICE_STATE}/active"
    ;;
  reload|daemon-reload)
    ;;
  enable)
    printf 'enabled\n' >"${MOCK_SERVICE_STATE}/enabled"
    ;;
  start)
    printf 'active\n' >"${MOCK_SERVICE_STATE}/active"
    ;;
  *)
    printf 'unexpected systemctl invocation: %s\n' "$*" >&2
    exit 1
    ;;
esac
EOF
  cat >"${directory}/sshd" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${1:-}" == "-t" ]]
EOF
  cat >"${directory}/ln" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${1:-}" == "-sfn" ]]
printf '%s\n' "$2" >"$3"
EOF
  chmod 0755 "${directory}/systemctl" "${directory}/sshd" "${directory}/ln"
}

record_present() {
  local target_root="$1"
  local transaction_root="$2"
  local path="$3"
  local content="$4"
  local destination="${target_root}${path}"
  local key

  install -d "$(dirname "${destination}")"
  printf '%s\n' "${content}" >"${destination}"
  key="$(printf '%s' "${path}" | sha256sum | awk '{print $1}')"
  cp -a "${destination}" "${transaction_root}/backup/${key}"
  printf '%s\tpresent\t%s\n' "${path}" "${key}" >>"${transaction_root}/managed.tsv"
}

run_failure_case() {
  local stage="$1"
  local previous_enabled="$2"
  local previous_active="$3"
  local test_root
  local transaction="/var/lib/ssh-bootstrap-release/transactions/failure-${stage}"
  local transaction_root
  local pointer
  local service_state
  local mock_bin
  local authorized_keys
  local authorized_keys_hash

  test_root="$(mktemp -d)"
  transaction_root="${test_root}${transaction}"
  pointer="${test_root}/var/lib/ssh-bootstrap-release/current-transaction"
  service_state="${test_root}/mock-service"
  mock_bin="${test_root}/mock-bin"
  mkdir -p "${transaction_root}/backup" "$(dirname "${pointer}")" \
    "${service_state}" "${test_root}/root/.ssh" \
    "${test_root}/opt/ssh-bootstrap/releases/old" \
    "${test_root}/opt/ssh-bootstrap/releases/new"
  make_mock_commands "${mock_bin}"

  record_present "${test_root}" "${transaction_root}" \
    /etc/ssh/sshd_config "Port 22"
  record_present "${test_root}" "${transaction_root}" \
    /etc/ssh/sshd_config.d/60-ssh-bootstrap.conf "old drop-in"
  record_present "${test_root}" "${transaction_root}" \
    /etc/systemd/system/ssh-bootstrapd.service "old unit"

  printf 'releases/old\n' >"${transaction_root}/previous-current"
  printf '%s\n' "${previous_enabled}" >"${transaction_root}/previous-enabled"
  printf '%s\n' "${previous_active}" >"${transaction_root}/previous-active"
  printf '/var/lib/ssh-bootstrap-release/transactions/previous\n' \
    >"${transaction_root}/previous-transaction"
  printf 'prepared\n' >"${transaction_root}/state"
  printf '%s\n' "${transaction}" >"${pointer}"

  printf 'releases/new\n' >"${test_root}/opt/ssh-bootstrap/current"
  printf 'Include /etc/ssh/sshd_config.d/*.conf\nPort 22\n' \
    >"${test_root}/etc/ssh/sshd_config"
  printf 'new drop-in\n' \
    >"${test_root}/etc/ssh/sshd_config.d/60-ssh-bootstrap.conf"
  printf 'new unit\n' \
    >"${test_root}/etc/systemd/system/ssh-bootstrapd.service"
  printf 'enabled\n' >"${service_state}/enabled"
  printf 'active\n' >"${service_state}/active"

  authorized_keys="${test_root}/root/.ssh/authorized_keys"
  printf 'existing-root-key-must-survive\n' >"${authorized_keys}"
  authorized_keys_hash="$(sha256sum "${authorized_keys}" | awk '{print $1}')"

  env \
    BLOCK_RELEASE_ROLE=BLK-REL \
    SSH_BOOTSTRAP_TEST_ROOT="${test_root}" \
    MOCK_SERVICE_STATE="${service_state}" \
    PATH="${mock_bin}:${PATH}" \
    "${ROLLBACK_SCRIPT}" --execute >/dev/null

  [[ "$(cat "${test_root}/etc/ssh/sshd_config")" == "Port 22" ]]
  [[ "$(cat "${test_root}/etc/ssh/sshd_config.d/60-ssh-bootstrap.conf")" == "old drop-in" ]]
  [[ "$(cat "${test_root}/etc/systemd/system/ssh-bootstrapd.service")" == "old unit" ]]
  [[ "$(cat "${test_root}/opt/ssh-bootstrap/current")" == "releases/old" ]]
  [[ "$(cat "${service_state}/enabled")" == "${previous_enabled}" ]]
  [[ "$(cat "${service_state}/active")" == "${previous_active}" ]]
  [[ "$(cat "${pointer}")" == "/var/lib/ssh-bootstrap-release/transactions/previous" ]]
  [[ "$(cat "${transaction_root}/state")" == "rolled-back" ]]
  [[ -d "${test_root}/opt/ssh-bootstrap/releases/new" ]]
  [[ "$(sha256sum "${authorized_keys}" | awk '{print $1}')" == "${authorized_keys_hash}" ]]

  rm -rf -- "${test_root}"
}

run_failure_case sshd-validate disabled inactive
run_failure_case ssh-reload enabled inactive
run_failure_case service-start disabled active
run_failure_case health-check enabled active

printf 'SSH bootstrap install failure rollback regression passed\n'
