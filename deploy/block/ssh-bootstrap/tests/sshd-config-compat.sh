#!/usr/bin/env bash
set -euo pipefail

readonly ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
readonly CACHE_ROOT="${ROOT}/../../../.cache/ssh-bootstrap-compat"
source "${ROOT}/sshd-config.sh"

mkdir -p "${CACHE_ROOT}"
test_root="$(mktemp -d "${CACHE_ROOT}/config.XXXXXX")"
trap 'rm -rf -- "${test_root}"' EXIT

mock_sshd="${test_root}/sshd"
cat >"${mock_sshd}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${1:-}" == "-t" && "${2:-}" == "-f" && -f "${3:-}" ]]
if [[ "${MOCK_INCLUDE_SUPPORTED}" != "true" ]] &&
  grep -Eq '^[[:space:]]*Include([[:space:]]|$)' "$3"; then
  printf '%s: line 1: Bad configuration option: Include\n' "$3" >&2
  exit 1
fi
EOF
chmod 0755 "${mock_sshd}"

probe="${test_root}/probe.conf"
probe_error="${test_root}/probe.stderr"
if MOCK_INCLUDE_SUPPORTED=false \
  ssh_bootstrap_detect_include_support "${mock_sshd}" "${probe}" "${probe_error}"; then
  printf 'ERROR: Ubuntu 18.04 fixture unexpectedly accepted Include\n' >&2
  exit 1
else
  [[ "$?" -eq 1 ]]
fi
MOCK_INCLUDE_SUPPORTED=true \
  ssh_bootstrap_detect_include_support "${mock_sshd}" "${probe}" "${probe_error}"

fixture="${ROOT}/tests/fixtures/ubuntu-18.04-sshd_config"
sshd_config="${test_root}/sshd_config"
cp "${fixture}" "${sshd_config}"
fixture_size="$(wc -c <"${fixture}")"

ssh_bootstrap_ensure_inline_block \
  "${sshd_config}" \
  "${ROOT}/sshd/60-ssh-bootstrap.conf"
[[ "$(grep -Fxc "${SSH_BOOTSTRAP_MANAGED_BEGIN}" "${sshd_config}")" == "1" ]]
[[ "$(grep -Fxc "${SSH_BOOTSTRAP_MANAGED_END}" "${sshd_config}")" == "1" ]]
grep -Fqx \
  'AuthorizedPrincipalsFile /opt/ssh-bootstrap/principals/%u' \
  "${sshd_config}"
head -c "${fixture_size}" "${sshd_config}" | cmp -s "${fixture}" -

first_install_hash="$(sha256sum "${sshd_config}" | awk '{print $1}')"
ssh_bootstrap_ensure_inline_block \
  "${sshd_config}" \
  "${ROOT}/sshd/60-ssh-bootstrap.conf"
[[ "$(sha256sum "${sshd_config}" | awk '{print $1}')" == "${first_install_hash}" ]]
[[ "$(grep -Fxc "${SSH_BOOTSTRAP_MANAGED_BEGIN}" "${sshd_config}")" == "1" ]]

printf 'SSH bootstrap Ubuntu 18.04 sshd configuration compatibility passed\n'
