#!/usr/bin/env bash
set -euo pipefail

readonly ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
readonly CACHE_ROOT="${BLOCK_DMP_CACHE_ROOT:-/mnt/d/codex/Block-DMP/.cache}"

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

make_systemctl_stub() {
  local directory="$1"
  install -d -m 0755 "${directory}"
  cat >"${directory}/systemctl" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  is-enabled)
    printf 'disabled\n'
    exit 1
    ;;
  is-active)
    printf 'inactive\n'
    exit 1
    ;;
  show)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
EOF
  chmod 0755 "${directory}/systemctl"
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

make_inputs() {
  local directory="$1"

  install -d -m 0755 "${directory}/artifact/bin"
  for binary in block-agent block-hmi plc-simulator; do
    printf '#!/usr/bin/env bash\nexit 0\n' >"${directory}/artifact/bin/${binary}"
    chmod 0755 "${directory}/artifact/bin/${binary}"
  done
  cp "${ROOT}/config/block-agent.example.json" "${directory}/agent.json"
  cp "${ROOT}/config/block-agent-simulator.example.json" "${directory}/agent-lab.json"
  cp "${ROOT}/config/plc-simulator.example.json" "${directory}/simulator.json"
  make_certificate_fixture "${directory}/tls"
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

make_systemctl_stub "${TEST_ROOT}/bin"
make_inputs "${TEST_ROOT}/inputs"

# Existing installation: an injected failure after switching current must restore
# every managed file, certificate, marker and the previous current symlink.
existing_root="${TEST_ROOT}/existing-host"
old_release="${existing_root}/opt/block/releases/1.0.0"
install -d -m 0755 \
  "${old_release}" \
  "${existing_root}/etc/systemd/system" \
  "${existing_root}/etc/block/certs" \
  "${existing_root}/var/lib/block-release"
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
[[ ! -e "${existing_root}/var/lib/block-release/current-transaction" ]] ||
  fail "failed existing-host install left a current transaction marker"

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
  "${ROOT}/rollback.sh" --execute \
  >"${TEST_ROOT}/rollback.out" 2>&1; then
  fail "rollback accepted a transaction not bound to current release"
fi
grep -Fq 'current transaction is not bound' "${TEST_ROOT}/rollback.out" ||
  fail "rollback binding rejection did not report the expected reason"
[[ "$(readlink -f "${rollback_root}/opt/block/current")" == "${current_release}" ]] ||
  fail "rejected rollback changed current release"

printf 'OK: deploy failure recovery, fresh cleanup, immutable config hashes, private-key rejection, rollback binding and packaged static dependencies\n'
