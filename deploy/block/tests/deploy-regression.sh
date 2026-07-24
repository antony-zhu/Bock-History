#!/usr/bin/env bash
set -euo pipefail

readonly ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
readonly CACHE_ROOT="${BLOCK_DMP_CACHE_ROOT:-/mnt/d/codex/Block-DMP/.cache}"
readonly REAL_INSTALL="$(command -v install)"
readonly REAL_CHMOD="$(command -v chmod)"
readonly REAL_CHOWN="$(command -v chown)"

case "${CACHE_ROOT}" in
  /mnt/d/codex/Block-DMP/.cache|/mnt/d/codex/Block-DMP/.cache/*) ;;
  *)
    printf 'ERROR: BLOCK_DMP_CACHE_ROOT must stay inside /mnt/d/codex/Block-DMP/.cache\n' >&2
    exit 2
    ;;
esac
if [[ "${EUID}" -ne 0 ]]; then
  exec sudo -n env \
    PATH="${PATH}" \
    BLOCK_DMP_CACHE_ROOT="${CACHE_ROOT}" \
    BLOCK_DEPLOY_TEST_KEEP="${BLOCK_DEPLOY_TEST_KEEP:-false}" \
    "${BASH_SOURCE[0]}"
fi
mkdir -p "${CACHE_ROOT}"
readonly TEST_ROOT="$(mktemp -d "${CACHE_ROOT}/deploy-regression.XXXXXX")"

cleanup() {
  if [[ "${BLOCK_DEPLOY_TEST_KEEP:-false}" == "true" ]]; then
    printf 'NOTICE: retained regression sandbox at %s\n' "${TEST_ROOT}" >&2
    return
  fi
  case "${TEST_ROOT}" in
    /mnt/d/codex/Block-DMP/.cache/deploy-regression.*)
      rm -rf --one-file-system -- "${TEST_ROOT}"
      ;;
  esac
}
trap cleanup EXIT

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_file_equals() {
  local expected="$1"
  local path="$2"

  [[ -f "${path}" ]] || fail "missing file: ${path}"
  [[ "$(cat "${path}")" == "${expected}" ]] ||
    fail "unexpected contents in ${path}"
}

assert_event_order() {
  local log_path="$1"
  shift
  local previous=0
  local event
  local line

  for event in "$@"; do
    line="$(awk -v wanted="${event}" '$0 == wanted { print NR; exit }' "${log_path}")"
    [[ -n "${line}" ]] || fail "missing deployment event '${event}' in ${log_path}"
    ((line > previous)) ||
      fail "deployment event '${event}' occurred out of order in ${log_path}"
    previous="${line}"
  done
}

directory_metadata() {
  stat -c '%u:%g:%a' "$1"
}

assert_directory_state() {
  local transaction="$1"
  local path="$2"
  local metadata="$3"
  local owner="${metadata%%:*}"
  local remainder="${metadata#*:}"
  local group="${remainder%%:*}"
  local mode="${remainder#*:}"

  grep -Fqx -- "${path}"$'\t'"${owner}"$'\t'"${group}"$'\t'"${mode}" \
    "${transaction}/directory-state.tsv" ||
    fail "transaction did not capture directory metadata for ${path}"
}

directory_install_count() {
  local log_path="$1"
  local wanted="$2"

  if [[ ! -f "${log_path}" ]]; then
    printf '0\n'
    return
  fi
  awk -F'\t' -v wanted="${wanted}" '
    {
      is_directory = 0
      has_target = 0
      for (field = 1; field <= NF; field++) {
        if ($field == "-d") {
          is_directory = 1
        }
        if ($field == wanted) {
          has_target = 1
        }
      }
      if (is_directory && has_target) {
        count++
      }
    }
    END { print count + 0 }
  ' "${log_path}"
}

assert_directory_restore_commands() {
  local host_root="$1"
  local path="$2"
  local metadata="$3"
  local owner="${metadata%%:*}"
  local remainder="${metadata#*:}"
  local group="${remainder%%:*}"
  local mode="${remainder#*:}"

  grep -Fqx -- \
    "--no-dereference"$'\t'"--"$'\t'"${owner}:${group}"$'\t'"${path}" \
    "${host_root}/chown-command-test.log" ||
    fail "restore did not reapply owner ${owner}:${group} to ${path}"
  grep -Fqx -- "--"$'\t'"${mode}"$'\t'"${path}" \
    "${host_root}/chmod-command-test.log" ||
    fail "restore did not reapply mode ${mode} to ${path}"
}

make_systemctl_stub() {
  local directory="$1"
  install -d -m 0755 "${directory}"
  cat >"${directory}/systemctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

root="${BLOCK_DEPLOY_TEST_ROOT:?}"
state_root="${root}/var/lib/block-release"
socket_path="${root}/run/block-agent/api/block-agent.sock"
log_path="${state_root}/systemctl-test.log"
install -d -m 0700 "${state_root}"
command_name="${1:-}"
shift || true
printf '%s%s\n' "${command_name}" "$([[ "$#" -gt 0 ]] && printf ' %s' "$*" || true)" >>"${log_path}"

unit=""
for argument in "$@"; do
  if [[ "${argument}" == *.service ]]; then
    unit="${argument}"
  fi
done

case "${command_name}" in
  is-enabled)
    printf 'disabled\n'
    exit 1
    ;;
  is-active)
    if [[ -n "${unit}" && -f "${state_root}/active-${unit}" ]]; then
      if [[ "${unit}" == "block-agent.service" &&
        "${BLOCK_DEPLOY_TEST_AGENT_READY_MODE:-delayed}" != "never" ]]; then
        count=0
        [[ ! -f "${state_root}/agent-probe-count" ]] ||
          count="$(cat "${state_root}/agent-probe-count")"
        count=$((count + 1))
        printf '%s\n' "${count}" >"${state_root}/agent-probe-count"
        ready_after="${BLOCK_DEPLOY_TEST_AGENT_READY_AFTER:-2}"
        if ((count >= ready_after)); then
          install -d -m 0755 "$(dirname -- "${socket_path}")"
          : >"${socket_path}"
        fi
      fi
      printf 'active\n'
      exit 0
    fi
    printf 'inactive\n'
    exit 1
    ;;
  show)
    exit 0
    ;;
  restart|start)
    for argument in "$@"; do
      [[ "${argument}" == *.service ]] || continue
      if [[ "${argument}" == "block-hmi.service" && ! -e "${socket_path}" ]]; then
        printf 'ERROR: test HMI start occurred before Agent readiness marker\n' >&2
        exit 42
      fi
      : >"${state_root}/active-${argument}"
      if [[ "${argument}" == "block-agent.service" ]]; then
        rm -f -- "${socket_path}" "${state_root}/agent-probe-count"
      fi
    done
    exit 0
    ;;
  stop)
    for argument in "$@"; do
      [[ "${argument}" == *.service ]] || continue
      rm -f -- "${state_root}/active-${argument}"
      if [[ "${argument}" == "block-agent.service" ]]; then
        rm -f -- "${socket_path}" "${state_root}/agent-probe-count"
      fi
    done
    exit 0
    ;;
  disable)
    if [[ " $* " == *" --now "* && -n "${unit}" ]]; then
      rm -f -- "${state_root}/active-${unit}"
    fi
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
EOF
  chmod 0755 "${directory}/systemctl"
}

make_curl_stub() {
  local directory="$1"
  cat >"${directory}/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

root="${BLOCK_DEPLOY_TEST_ROOT:?}"
state_root="${root}/var/lib/block-release"
socket_path=""
previous=""
for argument in "$@"; do
  if [[ "${previous}" == "--unix-socket" ]]; then
    socket_path="${argument}"
    break
  fi
  previous="${argument}"
done
if [[ -n "${socket_path}" && -e "${socket_path}" && ! -L "${socket_path}" ]]; then
  printf 'agent-health-ready\n' >>"${state_root}/systemctl-test.log"
  exit 0
fi
if [[ -z "${socket_path}" ]]; then
  printf 'hmi-health-ready\n' >>"${state_root}/systemctl-test.log"
  exit 0
fi
printf 'ERROR: test curl did not find a ready Agent socket\n' >&2
exit 22
EOF
  chmod 0755 "${directory}/curl"
}

make_filesystem_command_stubs() {
  local directory="$1"

  cat >"${directory}/install" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

root="${BLOCK_DEPLOY_TEST_ROOT:?}"
real_install="${BLOCK_DEPLOY_TEST_REAL_INSTALL:?}"
separator=""
for argument in "$@"; do
  printf '%s%s' "${separator}" "${argument}" >>"${root}/install-command-test.log"
  separator=$'\t'
done
printf '\n' >>"${root}/install-command-test.log"
exec "${real_install}" "$@"
EOF
  cat >"${directory}/chmod" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

root="${BLOCK_DEPLOY_TEST_ROOT:?}"
real_command="${BLOCK_DEPLOY_TEST_REAL_CHMOD:?}"
separator=""
for argument in "$@"; do
  printf '%s%s' "${separator}" "${argument}" >>"${root}/chmod-command-test.log"
  separator=$'\t'
done
printf '\n' >>"${root}/chmod-command-test.log"
exec "${real_command}" "$@"
EOF
  cat >"${directory}/chown" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

root="${BLOCK_DEPLOY_TEST_ROOT:?}"
real_command="${BLOCK_DEPLOY_TEST_REAL_CHOWN:?}"
separator=""
for argument in "$@"; do
  printf '%s%s' "${separator}" "${argument}" >>"${root}/chown-command-test.log"
  separator=$'\t'
done
printf '\n' >>"${root}/chown-command-test.log"
exec "${real_command}" "$@"
EOF
  chmod 0755 "${directory}/install" "${directory}/chmod" "${directory}/chown"
}

make_certificate_fixture() {
  local directory="$1"

  install -d -m 0700 "${directory}"
  openssl req -x509 -newkey rsa:2048 -nodes \
    -subj '/CN=Block deploy regression CA' \
    -keyout "${directory}/ca.key" \
    -out "${directory}/ca.crt" \
    -days 2 >/dev/null 2>&1
  openssl req -newkey rsa:2048 -nodes \
    -subj '/CN=127.0.0.1' \
    -addext 'subjectAltName=IP:127.0.0.1' \
    -keyout "${directory}/hmi.key" \
    -out "${directory}/hmi.csr" >/dev/null 2>&1
  cat >"${directory}/extensions.cnf" <<'EOF'
subjectAltName=IP:127.0.0.1
extendedKeyUsage=serverAuth
EOF
  openssl x509 -req \
    -in "${directory}/hmi.csr" \
    -CA "${directory}/ca.crt" \
    -CAkey "${directory}/ca.key" \
    -CAcreateserial \
    -out "${directory}/hmi.crt" \
    -days 2 \
    -extfile "${directory}/extensions.cnf" >/dev/null 2>&1
  chmod 0600 "${directory}/hmi.key" "${directory}/ca.key"
}

make_bdm_certificate_fixture() {
  local directory="$1"
  local principal="blk-0123456789abcdef0123456789abcdef"

  install -d -m 0700 "${directory}"
  openssl req -x509 -newkey rsa:2048 -nodes \
    -subj '/CN=BDM server trust CA' \
    -keyout "${directory}/server-ca.key" \
    -out "${directory}/server-ca.crt" \
    -days 2 >/dev/null 2>&1
  openssl req -x509 -newkey rsa:2048 -nodes \
    -subj '/CN=Block client issuing CA' \
    -keyout "${directory}/client-ca.key" \
    -out "${directory}/client-ca.crt" \
    -days 2 >/dev/null 2>&1
  openssl req -newkey rsa:2048 -nodes \
    -subj "/CN=${principal}" \
    -keyout "${directory}/client.key" \
    -out "${directory}/client.csr" >/dev/null 2>&1
  cat >"${directory}/client-extensions.cnf" <<'EOF'
extendedKeyUsage=clientAuth
EOF
  openssl x509 -req \
    -in "${directory}/client.csr" \
    -CA "${directory}/client-ca.crt" \
    -CAkey "${directory}/client-ca.key" \
    -CAcreateserial \
    -out "${directory}/client.crt" \
    -days 2 \
    -extfile "${directory}/client-extensions.cnf" >/dev/null 2>&1
  chmod 0600 \
    "${directory}/server-ca.key" \
    "${directory}/client-ca.key" \
    "${directory}/client.key"
}

make_inputs() {
  local directory="$1"

  install -d -m 0755 "${directory}/artifact/bin"
  for binary in block-agent block-hmi plc-simulator; do
    printf '#!/usr/bin/env bash\nexit 0\n' >"${directory}/artifact/bin/${binary}"
    chmod 0755 "${directory}/artifact/bin/${binary}"
  done
  cp "${ROOT}/config/block-agent.example.json" "${directory}/agent.json"
  cp "${ROOT}/config/block-agent-bdm.example.json" "${directory}/agent-bdm.json"
  cp "${ROOT}/config/block-agent-simulator-bdm.example.json" "${directory}/agent-simulator-bdm.json"
  cp "${ROOT}/config/block-agent-simulator.example.json" "${directory}/agent-lab.json"
  cp "${ROOT}/config/plc-simulator.example.json" "${directory}/simulator.json"
  make_certificate_fixture "${directory}/tls"
  make_bdm_certificate_fixture "${directory}/bdm-tls"
}

run_install() {
  local host_root="$1"
  local inputs="$2"
  shift 2

  env \
    PATH="${TEST_ROOT}/bin:${PATH}" \
    BLOCK_RELEASE_ROLE=BLK-REL \
    BLOCK_DEPLOY_TEST_MODE=true \
    BLOCK_DEPLOY_TEST_ROOT="${host_root}" \
    BLOCK_DEPLOY_TEST_REAL_INSTALL="${REAL_INSTALL}" \
    BLOCK_DEPLOY_TEST_REAL_CHMOD="${REAL_CHMOD}" \
    BLOCK_DEPLOY_TEST_REAL_CHOWN="${REAL_CHOWN}" \
    "$@" \
    "${ROOT}/install.sh" \
    --execute \
    --profile production \
    --version 1.2.3-test \
    --artifact-dir "${inputs}/artifact" \
    --agent-config "${inputs}/agent.json" \
    --tls-cert "${inputs}/tls/hmi.crt" \
    --tls-key "${inputs}/tls/hmi.key" \
    --tls-ca "${inputs}/tls/ca.crt" \
    --git-commit 1111111111111111111111111111111111111111 \
    --common-baseline 2222222222222222222222222222222222222222
}

run_lab_install() {
  local host_root="$1"
  local inputs="$2"
  shift 2

  env \
    PATH="${TEST_ROOT}/bin:${PATH}" \
    BLOCK_RELEASE_ROLE=BLK-REL \
    BLOCK_DEPLOY_TEST_MODE=true \
    BLOCK_DEPLOY_TEST_ROOT="${host_root}" \
    BLOCK_DEPLOY_TEST_REAL_INSTALL="${REAL_INSTALL}" \
    BLOCK_DEPLOY_TEST_REAL_CHMOD="${REAL_CHMOD}" \
    BLOCK_DEPLOY_TEST_REAL_CHOWN="${REAL_CHOWN}" \
    "$@" \
    "${ROOT}/install.sh" \
    --execute \
    --profile lab \
    --version 1.2.3-lab-test \
    --artifact-dir "${inputs}/artifact" \
    --agent-config "${inputs}/agent-lab.json" \
    --simulator-config "${inputs}/simulator.json" \
    --tls-cert "${inputs}/tls/hmi.crt" \
    --tls-key "${inputs}/tls/hmi.key" \
    --tls-ca "${inputs}/tls/ca.crt" \
    --git-commit 1111111111111111111111111111111111111111 \
    --common-baseline 2222222222222222222222222222222222222222
}

run_bdm_install() {
  local host_root="$1"
  local inputs="$2"
  shift 2

  env \
    PATH="${TEST_ROOT}/bin:${PATH}" \
    BLOCK_RELEASE_ROLE=BLK-REL \
    BLOCK_DEPLOY_TEST_MODE=true \
    BLOCK_DEPLOY_TEST_ROOT="${host_root}" \
    BLOCK_DEPLOY_TEST_REAL_INSTALL="${REAL_INSTALL}" \
    BLOCK_DEPLOY_TEST_REAL_CHMOD="${REAL_CHMOD}" \
    BLOCK_DEPLOY_TEST_REAL_CHOWN="${REAL_CHOWN}" \
    "$@" \
    "${ROOT}/install.sh" \
    --execute \
    --profile lab \
    --version 1.2.3-bdm-lab-test \
    --artifact-dir "${inputs}/artifact" \
    --agent-config "${inputs}/agent-simulator-bdm.json" \
    --simulator-config "${inputs}/simulator.json" \
    --tls-cert "${inputs}/tls/hmi.crt" \
    --tls-key "${inputs}/tls/hmi.key" \
    --tls-ca "${inputs}/tls/ca.crt" \
    --bdm-ca "${inputs}/bdm-tls/server-ca.crt" \
    --bdm-client-cert "${inputs}/bdm-tls/client.crt" \
    --bdm-client-key "${inputs}/bdm-tls/client.key" \
    --git-commit 1111111111111111111111111111111111111111 \
    --common-baseline 2222222222222222222222222222222222222222
}

make_systemctl_stub "${TEST_ROOT}/bin"
make_curl_stub "${TEST_ROOT}/bin"
make_inputs "${TEST_ROOT}/inputs"
make_filesystem_command_stubs "${TEST_ROOT}/bin"

# A failure after directory preflight but before the transaction is armed must
# not relabel an existing protected certificate directory.
prearm_root="${TEST_ROOT}/prearm-host"
install -d -m 0755 "${prearm_root}/etc/systemd/system"
install -d -o root -g root -m 0750 "${prearm_root}/etc/block/certs"
prearm_certs="${prearm_root}/etc/block/certs"
prearm_certs_metadata="$(directory_metadata "${prearm_certs}")"
if run_install \
  "${prearm_root}" \
  "${TEST_ROOT}/inputs" \
  BLOCK_DEPLOY_TEST_FAILPOINT=after-directory-preflight \
  >"${TEST_ROOT}/prearm.out" 2>&1; then
  fail "pre-transaction failpoint unexpectedly succeeded"
fi
grep -Fq 'injected deployment failure at after-directory-preflight' \
  "${TEST_ROOT}/prearm.out" ||
  fail "pre-transaction test did not reach the directory preflight failpoint"
[[ "$(directory_metadata "${prearm_certs}")" == "${prearm_certs_metadata}" ]] ||
  fail "directory preflight changed existing certificate-directory metadata"
[[ "$(directory_install_count \
  "${prearm_root}/install-command-test.log" "${prearm_certs}")" == "0" ]] ||
  fail "directory preflight relabelled the existing certificate directory"

# Existing installation: an injected failure after switching current must restore
# every managed file, certificate, parent-directory metadata, marker and the
# previous current symlink.
existing_root="${TEST_ROOT}/existing-host"
old_release="${existing_root}/opt/block/releases/1.0.0"
install -d -m 0755 \
  "${old_release}" \
  "${existing_root}/etc/systemd/system" \
  "${existing_root}/var/lib/block-release"
install -d -o root -g root -m 0750 "${existing_root}/etc/block/certs"
existing_certs="${existing_root}/etc/block/certs"
existing_certs_metadata="$(directory_metadata "${existing_certs}")"
printf 'old release\n' >"${old_release}/manifest.txt"
ln -s "${old_release}" "${existing_root}/opt/block/current"
printf 'production\n' >"${existing_root}/var/lib/block-release/current-profile"
for unit in block-agent.service block-hmi.service block-plc-simulator.service; do
  printf 'old-%s\n' "${unit}" >"${existing_root}/etc/systemd/system/${unit}"
done
printf 'old-agent-config\n' >"${existing_root}/etc/block/block-agent.json"
printf 'old-simulator-config\n' >"${existing_root}/etc/block/plc-simulator.json"
printf 'legacy-profile\n' >"${existing_root}/etc/block/block-profile.env"
printf 'old-cert\n' >"${existing_root}/etc/block/certs/block-hmi.crt"
printf 'old-key\n' >"${existing_root}/etc/block/certs/block-hmi.key"
printf 'old-ca\n' >"${existing_root}/etc/block/certs/ca.crt"

if run_install \
  "${existing_root}" \
  "${TEST_ROOT}/inputs" \
  BLOCK_DEPLOY_TEST_FAILPOINT=after-current-switch \
  >"${TEST_ROOT}/existing.out" 2>&1; then
  fail "injected existing-host install unexpectedly succeeded"
fi
grep -Fq 'injected deployment failure at after-current-switch' "${TEST_ROOT}/existing.out" ||
  fail "existing-host test did not reach the requested failpoint"
grep -Fq 'pre-install host state restored' "${TEST_ROOT}/existing.out" ||
  fail "existing-host failure did not report successful restoration"
[[ "$(readlink -f "${existing_root}/opt/block/current")" == "${old_release}" ]] ||
  fail "existing current symlink was not restored"
assert_file_equals "old-agent-config" "${existing_root}/etc/block/block-agent.json"
assert_file_equals "old-simulator-config" "${existing_root}/etc/block/plc-simulator.json"
assert_file_equals "legacy-profile" "${existing_root}/etc/block/block-profile.env"
assert_file_equals "old-cert" "${existing_root}/etc/block/certs/block-hmi.crt"
assert_file_equals "old-key" "${existing_root}/etc/block/certs/block-hmi.key"
assert_file_equals "old-ca" "${existing_root}/etc/block/certs/ca.crt"
[[ "$(directory_metadata "${existing_certs}")" == "${existing_certs_metadata}" ]] ||
  fail "failed-install recovery changed certificate-directory metadata"
[[ "$(directory_install_count \
  "${existing_root}/install-command-test.log" "${existing_certs}")" == "1" ]] ||
  fail "failed-install recovery relabelled the certificate parent directory"
grep -Fqx -- \
  "-d"$'\t'"-o"$'\t'"root"$'\t'"-g"$'\t'"root"$'\t'"-m"$'\t'"0750"$'\t'"${existing_certs}" \
  "${existing_root}/install-command-test.log" ||
  fail "installer did not converge the test-equivalent root:root 0750 certificate boundary"
shopt -s nullglob
existing_failed_transactions=(
  "${existing_root}/var/lib/block-release/transactions/"*
)
shopt -u nullglob
[[ "${#existing_failed_transactions[@]}" -eq 1 ]] ||
  fail "failed install did not retain exactly one transaction"
assert_directory_state \
  "${existing_failed_transactions[0]}" \
  "${existing_certs}" \
  "${existing_certs_metadata}"
assert_directory_restore_commands \
  "${existing_root}" \
  "${existing_certs}" \
  "${existing_certs_metadata}"
[[ ! -e "${existing_root}/var/lib/block-release/current-transaction" ]] ||
  fail "failed existing-host install left a current transaction marker"

# A successful upgrade followed by manual rollback must preserve the same
# certificate-directory boundary without restoring or relabelling cert files.
for unit in \
  block-agent.service \
  block-hmi.service; do
  : >"${existing_root}/var/lib/block-release/active-${unit}"
done
: >"${existing_root}/install-command-test.log"
: >"${existing_root}/chmod-command-test.log"
: >"${existing_root}/chown-command-test.log"
run_install \
  "${existing_root}" \
  "${TEST_ROOT}/inputs" \
  BLOCK_DEPLOY_TEST_SKIP_METADATA=true \
  BLOCK_DEPLOY_TEST_SKIP_VERIFY=true \
  >"${TEST_ROOT}/existing-success.out" 2>&1
upgrade_transaction="$(
  cat "${existing_root}/var/lib/block-release/current-transaction"
)"
assert_directory_state \
  "${upgrade_transaction}" \
  "${existing_certs}" \
  "${existing_certs_metadata}"
upgrade_release="$(readlink -f "${existing_root}/opt/block/current")"
cp -a \
  "${upgrade_transaction}/directory-state.tsv" \
  "${upgrade_transaction}/directory-state.valid.tsv"
sed '$d' \
  "${upgrade_transaction}/directory-state.valid.tsv" \
  >"${upgrade_transaction}/directory-state.tsv"
: >"${existing_root}/install-command-test.log"
: >"${existing_root}/chmod-command-test.log"
: >"${existing_root}/chown-command-test.log"
if env \
  PATH="${TEST_ROOT}/bin:${PATH}" \
  BLOCK_RELEASE_ROLE=BLK-REL \
  BLOCK_DEPLOY_TEST_MODE=true \
  BLOCK_DEPLOY_TEST_ROOT="${existing_root}" \
  BLOCK_DEPLOY_TEST_REAL_INSTALL="${REAL_INSTALL}" \
  BLOCK_DEPLOY_TEST_REAL_CHMOD="${REAL_CHMOD}" \
  BLOCK_DEPLOY_TEST_REAL_CHOWN="${REAL_CHOWN}" \
  "${ROOT}/rollback.sh" --execute \
  >"${TEST_ROOT}/existing-corrupt-directory-state.out" 2>&1; then
  fail "manual rollback accepted truncated parent-directory metadata"
fi
grep -Fq 'transaction is missing managed-directory metadata' \
  "${TEST_ROOT}/existing-corrupt-directory-state.out" ||
  fail "manual rollback did not reject truncated parent-directory metadata"
[[ ! -s "${existing_root}/chmod-command-test.log" &&
  ! -s "${existing_root}/chown-command-test.log" ]] ||
  fail "manual rollback partially applied invalid parent-directory metadata"
[[ "$(readlink -f "${existing_root}/opt/block/current")" == "${upgrade_release}" ]] ||
  fail "invalid parent-directory metadata changed current release"
[[ "$(directory_metadata "${existing_certs}")" == "${existing_certs_metadata}" ]] ||
  fail "invalid parent-directory metadata changed certificate-directory metadata"
rm -f -- "${upgrade_transaction}/directory-state.tsv"
mv -- \
  "${upgrade_transaction}/directory-state.valid.tsv" \
  "${upgrade_transaction}/directory-state.tsv"
: >"${existing_root}/install-command-test.log"
: >"${existing_root}/chmod-command-test.log"
: >"${existing_root}/chown-command-test.log"
: >"${existing_root}/var/lib/block-release/systemctl-test.log"
env \
  PATH="${TEST_ROOT}/bin:${PATH}" \
  BLOCK_RELEASE_ROLE=BLK-REL \
  BLOCK_DEPLOY_TEST_MODE=true \
  BLOCK_DEPLOY_TEST_ROOT="${existing_root}" \
  BLOCK_DEPLOY_TEST_REAL_INSTALL="${REAL_INSTALL}" \
  BLOCK_DEPLOY_TEST_REAL_CHMOD="${REAL_CHMOD}" \
  BLOCK_DEPLOY_TEST_REAL_CHOWN="${REAL_CHOWN}" \
  "${ROOT}/rollback.sh" --execute \
  >"${TEST_ROOT}/existing-rollback.out" 2>&1
grep -Fq 'OK: restored' "${TEST_ROOT}/existing-rollback.out" ||
  fail "manual rollback did not complete"
[[ "$(readlink -f "${existing_root}/opt/block/current")" == "${old_release}" ]] ||
  fail "manual rollback did not restore the previous release"
[[ "$(directory_metadata "${existing_certs}")" == "${existing_certs_metadata}" ]] ||
  fail "manual rollback changed certificate-directory metadata"
[[ "$(directory_install_count \
  "${existing_root}/install-command-test.log" "${existing_certs}")" == "0" ]] ||
  fail "manual rollback relabelled the certificate parent directory"
assert_directory_restore_commands \
  "${existing_root}" \
  "${existing_certs}" \
  "${existing_certs_metadata}"
assert_event_order \
  "${existing_root}/var/lib/block-release/systemctl-test.log" \
  "start block-agent.service" \
  "agent-health-ready" \
  "start block-hmi.service"

# Fresh installation: the same failure must remove all managed host files and
# current pointers even though manual rollback intentionally has no predecessor.
fresh_root="${TEST_ROOT}/fresh-host"
install -d -m 0755 "${fresh_root}/etc/systemd/system"
if run_install \
  "${fresh_root}" \
  "${TEST_ROOT}/inputs" \
  BLOCK_DEPLOY_TEST_FAILPOINT=after-current-switch \
  >"${TEST_ROOT}/fresh.out" 2>&1; then
  fail "injected fresh-host install unexpectedly succeeded"
fi
grep -Fq 'injected deployment failure at after-current-switch' "${TEST_ROOT}/fresh.out" ||
  fail "fresh-host test did not reach the requested failpoint"
grep -Fq 'pre-install host state restored' "${TEST_ROOT}/fresh.out" ||
  fail "fresh-host failure did not report successful restoration"
[[ ! -e "${fresh_root}/opt/block/current" && ! -L "${fresh_root}/opt/block/current" ]] ||
  fail "fresh failed install left current symlink"
for path in \
  "${fresh_root}/etc/block/block-agent.json" \
  "${fresh_root}/etc/block/plc-simulator.json" \
  "${fresh_root}/etc/block/block-profile.env" \
  "${fresh_root}/etc/block/certs/block-hmi.crt" \
  "${fresh_root}/etc/block/certs/block-hmi.key" \
  "${fresh_root}/etc/block/certs/ca.crt" \
  "${fresh_root}/etc/systemd/system/block-agent.service" \
  "${fresh_root}/etc/systemd/system/block-hmi.service" \
  "${fresh_root}/etc/systemd/system/block-plc-simulator.service" \
  "${fresh_root}/var/lib/block-release/current-transaction"; do
  [[ ! -e "${path}" && ! -L "${path}" ]] ||
    fail "fresh failed install retained managed path: ${path}"
done

# Production success removes a stale lab Simulator configuration.
production_root="${TEST_ROOT}/production-host"
install -d -m 0755 \
  "${production_root}/etc/block" \
  "${production_root}/etc/systemd/system"
printf 'stale-lab-config\n' >"${production_root}/etc/block/plc-simulator.json"
printf 'legacy-profile\n' >"${production_root}/etc/block/block-profile.env"
run_install \
  "${production_root}" \
  "${TEST_ROOT}/inputs" \
  BLOCK_DEPLOY_TEST_SKIP_VERIFY=true \
  >"${TEST_ROOT}/production.out" 2>&1
[[ ! -e "${production_root}/etc/block/plc-simulator.json" ]] ||
  fail "production install retained Simulator configuration"
[[ ! -e "${production_root}/etc/block/block-profile.env" ]] ||
  fail "production install retained legacy block-profile.env"
assert_event_order \
  "${production_root}/var/lib/block-release/systemctl-test.log" \
  "restart block-agent.service" \
  "agent-health-ready" \
  "restart block-hmi.service"

# BDM server trust and Block client identity use deliberately separate CAs.
# A valid installation must not try to verify the client certificate against
# the server CA bundle.
bdm_root="${TEST_ROOT}/bdm-host"
install -d -m 0755 "${bdm_root}/etc/systemd/system"
run_bdm_install \
  "${bdm_root}" \
  "${TEST_ROOT}/inputs" \
  BLOCK_DEPLOY_TEST_SKIP_VERIFY=true \
  >"${TEST_ROOT}/bdm.out" 2>&1
cmp -s \
  "${TEST_ROOT}/inputs/bdm-tls/server-ca.crt" \
  "${bdm_root}/etc/block/bdm-certs/ca.crt" ||
  fail "installer did not preserve the distinct BDM server CA"
cmp -s \
  "${TEST_ROOT}/inputs/bdm-tls/client.crt" \
  "${bdm_root}/etc/block/bdm-certs/client.crt" ||
  fail "installer did not install the separately issued Block client certificate"

# Reusing an unchanged current release must enforce the same Agent-ready gate.
: >"${production_root}/var/lib/block-release/systemctl-test.log"
run_install \
  "${production_root}" \
  "${TEST_ROOT}/inputs" \
  BLOCK_DEPLOY_TEST_SKIP_METADATA=true \
  BLOCK_DEPLOY_TEST_SKIP_VERIFY=true \
  >"${TEST_ROOT}/production-reuse.out" 2>&1
grep -Fq 'already matched; services were converged and verified' "${TEST_ROOT}/production-reuse.out" ||
  fail "unchanged release did not use the convergence branch"
assert_event_order \
  "${production_root}/var/lib/block-release/systemctl-test.log" \
  "restart block-agent.service" \
  "agent-health-ready" \
  "restart block-hmi.service"

# Lab startup must converge Simulator, then Agent readiness, then HMI.
lab_root="${TEST_ROOT}/lab-host"
install -d -m 0755 "${lab_root}/etc/systemd/system"
run_lab_install \
  "${lab_root}" \
  "${TEST_ROOT}/inputs" \
  BLOCK_DEPLOY_TEST_SKIP_VERIFY=true \
  >"${TEST_ROOT}/lab.out" 2>&1
assert_event_order \
  "${lab_root}/var/lib/block-release/systemctl-test.log" \
  "restart block-plc-simulator.service" \
  "restart block-agent.service" \
  "agent-health-ready" \
  "restart block-hmi.service"

# If Agent readiness never converges, HMI must not start and the install
# transaction must restore the fresh-host state.
readiness_failure_root="${TEST_ROOT}/readiness-failure-host"
install -d -m 0755 "${readiness_failure_root}/etc/systemd/system"
if run_lab_install \
  "${readiness_failure_root}" \
  "${TEST_ROOT}/inputs" \
  BLOCK_DEPLOY_TEST_AGENT_READY_MODE=never \
  BLOCK_DEPLOY_TEST_AGENT_READY_ATTEMPTS=2 \
  BLOCK_DEPLOY_TEST_AGENT_READY_INTERVAL_SECONDS=0 \
  BLOCK_DEPLOY_TEST_SKIP_VERIFY=true \
  >"${TEST_ROOT}/readiness-failure.out" 2>&1; then
  fail "install unexpectedly succeeded without Agent readiness"
fi
grep -Fq 'Agent did not become ready after 2 probe(s)' "${TEST_ROOT}/readiness-failure.out" ||
  fail "Agent readiness timeout lacked a clear bounded error"
grep -Fq 'pre-install host state restored' "${TEST_ROOT}/readiness-failure.out" ||
  fail "Agent readiness failure did not restore the captured host state"
if grep -Fqx 'restart block-hmi.service' \
  "${readiness_failure_root}/var/lib/block-release/systemctl-test.log"; then
  fail "HMI was started despite Agent readiness failure"
fi
[[ ! -e "${readiness_failure_root}/opt/block/current" &&
  ! -L "${readiness_failure_root}/opt/block/current" ]] ||
  fail "Agent readiness failure left the fresh current symlink"

if [[ "${BLOCK_DEPLOY_SKIP_PACKAGED_STATIC_ASSERT:-false}" != "true" ]]; then
  # The installed verify-static entrypoint must retain every runtime dependency,
  # not only the top-level script.
  packaged_deploy="${production_root}/opt/block/releases/1.2.3-test/deploy"
  [[ -x "${packaged_deploy}/verify-static.sh" ]] ||
    fail "release is missing executable verify-static.sh"
  [[ -x "${packaged_deploy}/tests/deploy-regression.sh" ]] ||
    fail "release is missing executable tests/deploy-regression.sh"
  for config_example in \
    block-agent.example.json \
    block-agent-bdm.example.json \
    block-agent-simulator-bdm.example.json \
    block-agent-simulator.example.json \
    plc-simulator.example.json; do
    cmp -s \
      "${ROOT}/config/${config_example}" \
      "${packaged_deploy}/config/${config_example}" ||
      fail "release is missing verify-static config dependency: ${config_example}"
  done
  BLOCK_DEPLOY_SKIP_PACKAGED_STATIC_ASSERT=true \
    BLOCK_DMP_CACHE_ROOT="${CACHE_ROOT}" \
    "${packaged_deploy}/verify-static.sh" \
    >"${TEST_ROOT}/packaged-static.out" 2>&1
  grep -Fq 'OK: deploy/block shell syntax' "${TEST_ROOT}/packaged-static.out" ||
    fail "packaged verify-static did not complete"
fi

# Existing release configuration hashes are immutable.
cp "${TEST_ROOT}/inputs/agent.json" "${TEST_ROOT}/inputs/agent.changed.json"
python3 - "${TEST_ROOT}/inputs/agent.changed.json" <<'PY'
import json
import sys

path = sys.argv[1]
with open(path, encoding="utf-8") as handle:
    value = json.load(handle)
value["samplePeriod"] = "2s"
with open(path, "w", encoding="utf-8") as handle:
    json.dump(value, handle, indent=2)
    handle.write("\n")
PY
mv "${TEST_ROOT}/inputs/agent.json" "${TEST_ROOT}/inputs/agent.original.json"
mv "${TEST_ROOT}/inputs/agent.changed.json" "${TEST_ROOT}/inputs/agent.json"
if run_install \
  "${production_root}" \
  "${TEST_ROOT}/inputs" \
  BLOCK_DEPLOY_TEST_SKIP_VERIFY=true \
  >"${TEST_ROOT}/hash.out" 2>&1; then
  fail "existing release accepted a different Agent configuration"
fi
grep -Fq 'different Agent configuration hash' "${TEST_ROOT}/hash.out" ||
  fail "configuration-hash rejection did not report the expected reason"
mv "${TEST_ROOT}/inputs/agent.original.json" "${TEST_ROOT}/inputs/agent.json"

# A CA bundle containing a private-key block must be rejected before mutation.
cp "${TEST_ROOT}/inputs/tls/ca.crt" "${TEST_ROOT}/inputs/tls/ca-with-key.crt"
cat "${TEST_ROOT}/inputs/tls/ca.key" >>"${TEST_ROOT}/inputs/tls/ca-with-key.crt"
if env \
  PATH="${TEST_ROOT}/bin:${PATH}" \
  BLOCK_RELEASE_ROLE=BLK-REL \
  BLOCK_DEPLOY_TEST_MODE=true \
  BLOCK_DEPLOY_TEST_ROOT="${TEST_ROOT}/private-key-host" \
  BLOCK_DEPLOY_TEST_REAL_INSTALL="${REAL_INSTALL}" \
  BLOCK_DEPLOY_TEST_REAL_CHMOD="${REAL_CHMOD}" \
  BLOCK_DEPLOY_TEST_REAL_CHOWN="${REAL_CHOWN}" \
  "${ROOT}/install.sh" \
  --execute \
  --profile production \
  --version 1.2.4-test \
  --artifact-dir "${TEST_ROOT}/inputs/artifact" \
  --agent-config "${TEST_ROOT}/inputs/agent.json" \
  --tls-cert "${TEST_ROOT}/inputs/tls/hmi.crt" \
  --tls-key "${TEST_ROOT}/inputs/tls/hmi.key" \
  --tls-ca "${TEST_ROOT}/inputs/tls/ca-with-key.crt" \
  --git-commit 1111111111111111111111111111111111111111 \
  --common-baseline 2222222222222222222222222222222222222222 \
  >"${TEST_ROOT}/private-key.out" 2>&1; then
  fail "CA bundle containing a private key was accepted"
fi
grep -Fq 'private-key PEM block found' "${TEST_ROOT}/private-key.out" ||
  fail "private-key rejection did not report the expected reason"

# Rollback must reject a valid-looking but stale transaction marker before any
# restore operation.
rollback_root="${TEST_ROOT}/rollback-host"
current_release="${rollback_root}/opt/block/releases/2.0.0"
correct_tx="${rollback_root}/var/lib/block-release/transactions/correct"
stale_tx="${rollback_root}/var/lib/block-release/transactions/stale"
install -d -m 0755 "${current_release}" "${correct_tx}" "${stale_tx}"
printf 'transaction=%s\n' "${correct_tx}" >"${current_release}/manifest.txt"
ln -s "${current_release}" "${rollback_root}/opt/block/current"
printf '%s\n' "${stale_tx}" >"${rollback_root}/var/lib/block-release/current-transaction"
printf '%s\n' "${current_release}" >"${stale_tx}/installed-release"
printf '%064d\n' 0 >"${stale_tx}/release-manifest.sha256"
if env \
  PATH="${TEST_ROOT}/bin:${PATH}" \
  BLOCK_RELEASE_ROLE=BLK-REL \
  BLOCK_DEPLOY_TEST_MODE=true \
  BLOCK_DEPLOY_TEST_ROOT="${rollback_root}" \
  BLOCK_DEPLOY_TEST_REAL_INSTALL="${REAL_INSTALL}" \
  BLOCK_DEPLOY_TEST_REAL_CHMOD="${REAL_CHMOD}" \
  BLOCK_DEPLOY_TEST_REAL_CHOWN="${REAL_CHOWN}" \
  "${ROOT}/rollback.sh" --execute \
  >"${TEST_ROOT}/rollback.out" 2>&1; then
  fail "rollback accepted a transaction not bound to current release"
fi
grep -Fq 'current transaction is not bound' "${TEST_ROOT}/rollback.out" ||
  fail "rollback binding rejection did not report the expected reason"
[[ "$(readlink -f "${rollback_root}/opt/block/current")" == "${current_release}" ]] ||
  fail "rejected rollback changed current release"

printf 'OK: deploy failure recovery, parent-directory metadata preservation, manual rollback with Agent-before-HMI readiness, release reuse, fresh cleanup, immutable config hashes, private-key rejection, rollback binding and packaged static dependencies\n'
