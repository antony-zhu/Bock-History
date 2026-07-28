#!/usr/bin/env bash
set -euo pipefail

readonly ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

assert_line() {
  local file="$1"
  local expected="$2"
  grep -Fqx -- "${expected}" "${file}" ||
    die "${file#${ROOT}/} is missing: ${expected}"
}

assert_absent() {
  local pattern="$1"
  shift
  if grep -En -- "${pattern}" "$@" >/dev/null; then
    grep -En -- "${pattern}" "$@" >&2 || true
    die "forbidden legacy or insecure pattern found"
  fi
}

for shell_file in \
  "${ROOT}/health-check.sh" \
  "${ROOT}/install-users.sh" \
  "${ROOT}/install.sh" \
  "${ROOT}/rollback.sh" \
  "${ROOT}/verify-install.sh" \
  "${ROOT}/verify-static.sh" \
  "${ROOT}/tests/deploy-regression.sh"; do
  [[ -x "${shell_file}" ]] || die "shell script is not executable: ${shell_file#${ROOT}/}"
  bash -n "${shell_file}"
done

python3 -m json.tool "${ROOT}/config/block-agent.example.json" >/dev/null
python3 -m json.tool "${ROOT}/config/block-agent-bdm.example.json" >/dev/null
python3 -m json.tool "${ROOT}/config/block-agent-simulator-bdm.example.json" >/dev/null
python3 -m json.tool "${ROOT}/config/block-agent-simulator.example.json" >/dev/null
python3 -m json.tool "${ROOT}/config/plc-simulator.example.json" >/dev/null

assert_line "${ROOT}/systemd/block-agent.service" "User=block-agent"
assert_line "${ROOT}/systemd/block-agent.service" "Group=block-agent"
assert_line "${ROOT}/systemd/block-agent.service" "After=local-fs.target block-plc-simulator.service"
assert_line "${ROOT}/systemd/block-agent.service" "RequiresMountsFor=/var/lib/block"
assert_line "${ROOT}/systemd/block-agent.service" "ConditionPathIsMountPoint=/var/lib/block"
assert_line "${ROOT}/systemd/block-agent.service" "SupplementaryGroups=block-hmi-api block-sim-io"
assert_line "${ROOT}/systemd/block-agent.service" "StateDirectory=block"
assert_line "${ROOT}/systemd/block-agent.service" "RestrictAddressFamilies=AF_UNIX AF_INET"
assert_line "${ROOT}/systemd/block-agent.service" "IPAddressDeny=any"
assert_line "${ROOT}/systemd/block-agent.service" "IPAddressAllow=192.168.1.105/32"
assert_line "${ROOT}/systemd/block-agent.service" "ReadWritePaths=/run/block-agent /var/lib/block"
assert_line "${ROOT}/systemd/block-agent.service" "InaccessiblePaths=-/run/block-plc/control -/var/lib/block-plc-sim"

assert_line "${ROOT}/systemd/block-hmi.service" "User=block-hmi"
assert_line "${ROOT}/systemd/block-hmi.service" "Group=block-hmi"
assert_line "${ROOT}/systemd/block-hmi.service" "SupplementaryGroups=block-hmi-api"
assert_line "${ROOT}/systemd/block-hmi.service" "Environment=BLOCK_HMI_ADDR=127.0.0.1:8443"
assert_line "${ROOT}/systemd/block-hmi.service" "Environment=BLOCK_HMI_AGENT_TIMEOUT=8s"
assert_line "${ROOT}/systemd/block-hmi.service" "IPAddressDeny=any"
assert_line "${ROOT}/systemd/block-hmi.service" "IPAddressAllow=localhost"
assert_line "${ROOT}/systemd/block-hmi.service" "InaccessiblePaths=-/run/block-plc -/var/lib/block -/var/lib/block-plc-sim"

assert_line "${ROOT}/systemd/block-plc-simulator.service" "User=block-simulator"
assert_line "${ROOT}/systemd/block-plc-simulator.service" "Group=block-simulator"
assert_line "${ROOT}/systemd/block-plc-simulator.service" "SupplementaryGroups=block-sim-io block-sim-control"
assert_line "${ROOT}/systemd/block-plc-simulator.service" "PrivateNetwork=true"
assert_line "${ROOT}/systemd/block-plc-simulator.service" "RestrictAddressFamilies=AF_UNIX"
assert_line "${ROOT}/systemd/block-plc-simulator.service" "InaccessiblePaths=-/run/block-agent -/var/lib/block"

assert_line "${ROOT}/config/block-agent.example.json" '  "localApiSocket": "/run/block-agent/api/block-agent.sock",'
assert_line "${ROOT}/config/block-agent.example.json" '  "databasePath": "/var/lib/block/block.db",'
assert_line "${ROOT}/config/block-agent.example.json" '    "enabled": false'
assert_line "${ROOT}/config/block-agent-bdm.example.json" '  "databasePath": "/var/lib/block/block.db",'
assert_line "${ROOT}/config/block-agent-bdm.example.json" '    "endpoint": "mqtts://192.168.1.105:8883",'
assert_line "${ROOT}/config/block-agent-bdm.example.json" '    "principal": "INVALID_APPROVED_BLOCK_PRINCIPAL_REQUIRED",'
assert_line "${ROOT}/config/block-agent-bdm.example.json" '    "caFile": "/etc/block/bdm-certs/ca.crt",'
assert_line "${ROOT}/config/block-agent-simulator-bdm.example.json" '    "type": "simulator",'
assert_line "${ROOT}/config/block-agent-simulator-bdm.example.json" '  "databasePath": "/var/lib/block/block.db",'
assert_line "${ROOT}/config/block-agent-simulator-bdm.example.json" '    "enabled": true,'
assert_line "${ROOT}/config/block-agent-simulator-bdm.example.json" '    "endpoint": "mqtts://192.168.1.105:8883",'
assert_line "${ROOT}/config/block-agent-simulator-bdm.example.json" '    "principal": "INVALID_APPROVED_BLOCK_PRINCIPAL_REQUIRED",'
assert_line "${ROOT}/config/block-agent-simulator.example.json" '  "databasePath": "/var/lib/block/block.db",'
assert_line "${ROOT}/config/block-agent-simulator.example.json" '    "ioSocket": "/run/block-plc/io/io.sock"'
assert_line "${ROOT}/config/plc-simulator.example.json" '  "ioSocket": "/run/block-plc/io/io.sock",'
assert_line "${ROOT}/config/plc-simulator.example.json" '  "controlSocket": "/run/block-plc/control/control.sock",'

assert_absent \
  '/run/block-plc-sim|/run/block/block-agent\.sock|SupplementaryGroups=block([[:space:]]|$)' \
  "${ROOT}/systemd/block-agent.service" \
  "${ROOT}/systemd/block-hmi.service" \
  "${ROOT}/systemd/block-plc-simulator.service" \
  "${ROOT}/config/block-agent.example.json" \
  "${ROOT}/config/block-agent-bdm.example.json" \
  "${ROOT}/config/block-agent-simulator-bdm.example.json" \
  "${ROOT}/config/block-agent-simulator.example.json" \
  "${ROOT}/config/plc-simulator.example.json"

assert_absent \
  'network-online\.target|PrivateNetwork=true' \
  "${ROOT}/systemd/block-agent.service"

assert_absent \
  '/var/lib/block-agent' \
  "${ROOT}/systemd/block-agent.service" \
  "${ROOT}/systemd/block-hmi.service" \
  "${ROOT}/systemd/block-plc-simulator.service" \
  "${ROOT}/config/block-agent.example.json" \
  "${ROOT}/config/block-agent-bdm.example.json" \
  "${ROOT}/config/block-agent-simulator-bdm.example.json" \
  "${ROOT}/config/block-agent-simulator.example.json"

# A reviewed release must inject its approved identity into a secure staging
# configuration. No usable opaque Block/BDM principal may ship in deploy input
# files, including examples and regression source. The regression constructs
# its synthetic principal from separated string fragments at runtime.
opaque_principal_pattern='(bdm|blk)-''[0-9a-f]{32}'
if grep -Ern -- "${opaque_principal_pattern}" "${ROOT}" >/dev/null; then
  grep -Ern -- "${opaque_principal_pattern}" "${ROOT}" >&2 || true
  die "usable opaque principal found in deploy input"
fi

# Construct these tokens so this verifier does not match itself.
insecure_long="--""insecure"
insecure_short="curl[[:space:]].*-[^-[:space:]]*""k"
assert_absent \
  "${insecure_long}|${insecure_short}" \
  "${ROOT}/health-check.sh" \
  "${ROOT}/install.sh" \
  "${ROOT}/rollback.sh" \
  "${ROOT}/verify-install.sh"

assert_line "${ROOT}/health-check.sh" "  --proto '=https' \\"
assert_line "${ROOT}/health-check.sh" '  --cacert "${BLOCK_HMI_CA}" \'
grep -Fq 'systemctl disable --now block-plc-simulator.service' "${ROOT}/install.sh" ||
  die "production installation does not disable Simulator"
grep -Fq 'systemctl enable block-plc-simulator.service block-agent.service block-hmi.service' "${ROOT}/install.sh" ||
  die "lab installation does not explicitly enable Simulator"
grep -Fq 'BLOCK_RELEASE_ROLE=BLK-REL' "${ROOT}/install.sh" ||
  die "installer lacks BLK-REL guard"
grep -Fq 'BLOCK_RELEASE_ROLE=BLK-REL' "${ROOT}/rollback.sh" ||
  die "rollback lacks BLK-REL guard"
grep -Fq 'restore_failed_install' "${ROOT}/install.sh" ||
  die "installer lacks failed-install restoration"
grep -Fq 'capture_managed_directory_states "${tx_dir}"' "${ROOT}/install.sh" ||
  die "installer does not capture managed parent-directory metadata"
grep -Fq 'restore_managed_directory_states "${tx_dir}"' "${ROOT}/install.sh" ||
  die "failed-install recovery does not restore managed parent-directory metadata"
grep -Fq 'restore_managed_directory_states "${tx_dir}"' "${ROOT}/rollback.sh" ||
  die "manual rollback does not restore managed parent-directory metadata"
grep -Fq 'wait_for_agent_ready' "${ROOT}/rollback.sh" ||
  die "manual rollback does not wait for Agent UDS readiness"
grep -Fq 'refusing to start HMI' "${ROOT}/rollback.sh" ||
  die "manual rollback does not fail closed before HMI startup"
grep -Fq 'require_safe_restore_parent "${path}"' "${ROOT}/install.sh" ||
  die "failed-install recovery does not preserve existing restore-parent metadata"
grep -Fq 'require_safe_restore_parent "${path}"' "${ROOT}/rollback.sh" ||
  die "manual rollback does not preserve existing restore-parent metadata"
if grep -Fq 'install -d -m 0755 "$(dirname -- "${path}")"' \
  "${ROOT}/install.sh" "${ROOT}/rollback.sh"; then
  die "restore path still relabels existing parent directories to 0755"
fi
[[ "$(grep -Fc 'wait_for_agent_ready' "${ROOT}/install.sh")" -ge 3 ]] ||
  die "installer does not gate HMI startup on Agent readiness in both convergence paths"
grep -Fq 'refusing to start HMI' "${ROOT}/install.sh" ||
  die "Agent readiness timeout does not fail closed with a clear error"
grep -Fq 'current transaction is not bound to the current release manifest' "${ROOT}/rollback.sh" ||
  die "rollback lacks current-release transaction binding"
grep -Fq 'private-key PEM block found' "${ROOT}/install.sh" ||
  die "installer does not reject private keys in certificate-only inputs"
grep -Fq 'mountpoint -q "${DATA_ROOT}"' "${ROOT}/install.sh" ||
  die "installer does not require the dedicated Block data filesystem"
if grep -Fq 'openssl verify -CAfile "${bdm_ca}" "${bdm_client_cert}"' \
  "${ROOT}/install.sh"; then
  die "installer confuses the BDM server trust CA with the Block client issuing CA"
fi
grep -Fq 'agent_config_sha256=${agent_config_hash}' "${ROOT}/install.sh" ||
  die "existing release does not bind Agent configuration hash"
grep -Fq 'rm -f -- "${CONFIG_ROOT}/plc-simulator.json"' "${ROOT}/install.sh" ||
  die "production installation does not remove Simulator configuration"
grep -Fq 'password must be locked' "${ROOT}/install-users.sh" ||
  die "service account password lock is not enforced"
legacy_simulation_variable="BLOCK_HMI_""SIMULATION"
assert_absent \
  "${legacy_simulation_variable}" \
  "${ROOT}/install.sh" \
  "${ROOT}/rollback.sh" \
  "${ROOT}/verify-install.sh" \
  "${ROOT}/systemd/block-hmi.service"

"${ROOT}/tests/deploy-regression.sh"

printf 'OK: deploy/block shell syntax, JSON, socket paths, privilege matrix and TLS/static network gates\n'
