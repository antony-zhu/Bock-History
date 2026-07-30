#!/usr/bin/env bash
set -euo pipefail

readonly ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
execute="false"
version=""
artifact_dir=""
config=""
tls_cert=""
tls_key=""
tls_ca=""
admin_public_key=""
ssh_ca_private=""
ssh_ca_public=""
health_host=""
server_name=""
git_commit=""
common_baseline=""
transaction=""
rollback_armed="false"

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

rollback_install_error() {
  local status="$?"
  trap - ERR
  if [[ "${rollback_armed}" == "true" ]]; then
    printf 'ERROR: SSH bootstrap install failed; rolling back transaction %s\n' \
      "${transaction}" >&2
    if ! "${ROOT}/rollback.sh" --execute; then
      printf 'ERROR: automatic rollback failed for transaction %s\n' \
        "${transaction}" >&2
    fi
  fi
  exit "${status}"
}

trap rollback_install_error ERR

usage() {
  cat <<'EOF'
usage: install.sh --execute --version VERSION --artifact-dir DIR \
  --config FILE --tls-cert FILE --tls-key FILE --tls-ca FILE \
  --admin-public-key FILE --ssh-ca-private FILE --ssh-ca-public FILE \
  --health-host HOST --git-commit SHA --common-baseline SHA [--server-name DNS]
EOF
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --execute) execute="true"; shift ;;
    --version) version="$2"; shift 2 ;;
    --artifact-dir) artifact_dir="$2"; shift 2 ;;
    --config) config="$2"; shift 2 ;;
    --tls-cert) tls_cert="$2"; shift 2 ;;
    --tls-key) tls_key="$2"; shift 2 ;;
    --tls-ca) tls_ca="$2"; shift 2 ;;
    --admin-public-key) admin_public_key="$2"; shift 2 ;;
    --ssh-ca-private) ssh_ca_private="$2"; shift 2 ;;
    --ssh-ca-public) ssh_ca_public="$2"; shift 2 ;;
    --health-host) health_host="$2"; shift 2 ;;
    --server-name) server_name="$2"; shift 2 ;;
    --git-commit) git_commit="$2"; shift 2 ;;
    --common-baseline) common_baseline="$2"; shift 2 ;;
    --help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ "${execute}" == "true" ]] || die "--execute is required"
[[ "${BLOCK_RELEASE_ROLE:-}" == "BLK-REL" ]] || die "BLOCK_RELEASE_ROLE=BLK-REL is required"
[[ "${EUID}" -eq 0 ]] || die "install.sh must run as root"
for value in version artifact_dir config tls_cert tls_key tls_ca admin_public_key ssh_ca_private ssh_ca_public health_host git_commit common_baseline; do
  [[ -n "${!value}" ]] || die "--${value//_/-} is required"
done
[[ "${version}" =~ ^[A-Za-z0-9._-]+$ ]] || die "version is invalid"
[[ "${git_commit}" =~ ^[0-9a-f]{40}$ ]] || die "git commit must be a full SHA"
[[ "${common_baseline}" =~ ^[0-9a-f]{40}$ ]] || die "Common baseline must be a full SHA"

for file in \
  "${artifact_dir}/bin/ssh-bootstrapd" \
  "${config}" \
  "${tls_cert}" \
  "${tls_key}" \
  "${tls_ca}" \
  "${admin_public_key}" \
  "${ssh_ca_private}" \
  "${ssh_ca_public}"; do
  [[ -f "${file}" ]] || die "required file is missing: ${file}"
done

python3 - "${config}" <<'PY'
import json
import pathlib
import sys

config = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
required = {
    "listenAddress", "targetNode", "siteId", "blockId", "deviceId",
    "advertisedHost", "sshPort", "administratorKid",
    "administratorPublicKeyPath", "tlsCertificatePath", "tlsPrivateKeyPath",
    "sshUserCaPrivateKeyPath", "sshUserCaPublicKeyPath",
    "sshHostKeyFingerprint", "nonceDatabasePath", "releaseUsername",
    "debugUsername",
}
if set(config) != required:
    raise SystemExit("config keys do not exactly match SSH bootstrap v1")
if config["targetNode"] != "BLOCK":
    raise SystemExit("targetNode must be BLOCK")
if config["releaseUsername"] != "release" or config["debugUsername"] != "debug":
    raise SystemExit("v1 usernames must be release/debug")
expected = {
    "administratorPublicKeyPath": "/etc/ssh-bootstrap/admin-ed25519-public.pem",
    "tlsCertificatePath": "/etc/ssh-bootstrap/tls/server.crt",
    "tlsPrivateKeyPath": "/etc/ssh-bootstrap/tls/server.key",
    "sshUserCaPrivateKeyPath": "/etc/ssh-bootstrap/ssh-user-ca",
    "sshUserCaPublicKeyPath": "/etc/ssh-bootstrap/ssh-user-ca.pub",
    "nonceDatabasePath": "/var/lib/ssh-bootstrap/nonces.db",
}
for key, value in expected.items():
    if config[key] != value:
        raise SystemExit(f"{key} must be {value}")
PY

grep -q -- 'BEGIN PUBLIC KEY' "${admin_public_key}" || die "administrator verifier must be a public PEM"
! grep -Eiq -- 'PRIVATE KEY|password|token|secret' "${admin_public_key}" ||
  die "administrator verifier file contains forbidden private material"
openssl pkey -pubin -in "${admin_public_key}" -text -noout 2>&1 |
  grep -Fqi 'ED25519' || die "administrator verifier must be ED25519"
grep -Fq -- 'BEGIN CERTIFICATE' "${tls_cert}" || die "TLS certificate is invalid"
! grep -Fq -- 'PRIVATE KEY' "${tls_cert}" || die "TLS certificate file contains a private key"
grep -Fq -- 'BEGIN CERTIFICATE' "${tls_ca}" || die "TLS CA is invalid"
! grep -Fq -- 'PRIVATE KEY' "${tls_ca}" || die "TLS CA file contains a private key"
! grep -Fq -- 'PRIVATE KEY' "${ssh_ca_public}" || die "SSH CA public file contains a private key"
derived_ca="$(ssh-keygen -y -f "${ssh_ca_private}")"
configured_ca="$(awk '{print $1 " " $2}' "${ssh_ca_public}")"
[[ "$(awk '{print $1 " " $2}' <<<"${derived_ca}")" == "${configured_ca}" ]] ||
  die "SSH CA public and private keys do not match"
cert_public_hash="$(openssl x509 -in "${tls_cert}" -pubkey -noout | openssl pkey -pubin -outform DER | sha256sum | awk '{print $1}')"
key_public_hash="$(openssl pkey -in "${tls_key}" -pubout -outform DER | sha256sum | awk '{print $1}')"
[[ "${cert_public_hash}" == "${key_public_hash}" ]] || die "TLS certificate and private key do not match"
openssl verify -CAfile "${tls_ca}" "${tls_cert}" >/dev/null ||
  die "TLS certificate does not verify against the supplied CA"
if [[ -n "${server_name}" ]]; then
  openssl x509 -in "${tls_cert}" -noout -checkhost "${server_name}" >/dev/null ||
    die "TLS certificate does not cover the configured DNS name"
elif [[ "${health_host}" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ || "${health_host}" == *:* ]]; then
  openssl x509 -in "${tls_cert}" -noout -checkip "${health_host}" >/dev/null ||
    die "TLS certificate does not cover the configured IP address"
fi

"${ROOT}/install-users.sh"

release_root="/opt/ssh-bootstrap/releases/${version}"
[[ ! -e "${release_root}" ]] || die "release already exists: ${release_root}"
state_root="/var/lib/ssh-bootstrap-release"
pointer="${state_root}/current-transaction"
transaction="${state_root}/transactions/$(date -u +%Y%m%dT%H%M%SZ)-${version}"
mkdir -p "${transaction}/backup" "${release_root}/bin" "${release_root}/deploy"
chmod 0700 "${transaction}"

managed=(
  /etc/ssh-bootstrap/config.json
  /etc/ssh-bootstrap/admin-ed25519-public.pem
  /etc/ssh-bootstrap/tls/server.crt
  /etc/ssh-bootstrap/tls/server.key
  /etc/ssh-bootstrap/tls/ca.crt
  /etc/ssh-bootstrap/ssh-user-ca
  /etc/ssh-bootstrap/ssh-user-ca.pub
  /etc/ssh-bootstrap/principals/release
  /etc/ssh-bootstrap/principals/debug
  /etc/ssh/sshd_config
  /etc/ssh/sshd_config.d/60-ssh-bootstrap.conf
  /etc/systemd/system/ssh-bootstrapd.service
)
for path in "${managed[@]}"; do
  key="$(printf '%s' "${path}" | sha256sum | awk '{print $1}')"
  if [[ -e "${path}" ]]; then
    cp -a "${path}" "${transaction}/backup/${key}"
    printf '%s\tpresent\t%s\n' "${path}" "${key}" >>"${transaction}/managed.tsv"
  else
    printf '%s\tabsent\t%s\n' "${path}" "${key}" >>"${transaction}/managed.tsv"
  fi
done
if [[ -L /opt/ssh-bootstrap/current ]]; then
  readlink /opt/ssh-bootstrap/current >"${transaction}/previous-current"
fi
systemctl is-enabled ssh-bootstrapd.service >"${transaction}/previous-enabled" 2>/dev/null || true
systemctl is-active ssh-bootstrapd.service >"${transaction}/previous-active" 2>/dev/null || true
if [[ -f "${pointer}" ]]; then
  cp -a "${pointer}" "${transaction}/previous-transaction"
fi

printf 'prepared\n' >"${transaction}/state"
printf '%s\n' "${transaction}" >"${pointer}.new"
chmod 0600 "${pointer}.new"
mv -Tf "${pointer}.new" "${pointer}"
rollback_armed="true"

install -m 0755 "${artifact_dir}/bin/ssh-bootstrapd" "${release_root}/bin/ssh-bootstrapd"
cp -a "${ROOT}/." "${release_root}/deploy/"
find "${release_root}/deploy" -type f -name '*.sh' -exec chmod 0755 {} +
cat >"${release_root}/manifest.txt" <<EOF
version=${version}
gitCommit=${git_commit}
commonBaseline=${common_baseline}
builtAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)
binarySHA256=$(sha256sum "${release_root}/bin/ssh-bootstrapd" | awk '{print $1}')
EOF

install -d -m 0750 -o root -g ssh-bootstrap /etc/ssh-bootstrap /etc/ssh-bootstrap/tls
install -d -m 0755 -o root -g root /etc/ssh-bootstrap/principals /etc/ssh/sshd_config.d
install -d -m 0700 -o ssh-bootstrap -g ssh-bootstrap /var/lib/ssh-bootstrap
install -m 0640 -o root -g ssh-bootstrap "${config}" /etc/ssh-bootstrap/config.json
install -m 0644 -o root -g root "${admin_public_key}" /etc/ssh-bootstrap/admin-ed25519-public.pem
install -m 0644 -o root -g root "${tls_cert}" /etc/ssh-bootstrap/tls/server.crt
install -m 0640 -o root -g ssh-bootstrap "${tls_key}" /etc/ssh-bootstrap/tls/server.key
install -m 0644 -o root -g root "${tls_ca}" /etc/ssh-bootstrap/tls/ca.crt
install -m 0640 -o root -g ssh-bootstrap "${ssh_ca_private}" /etc/ssh-bootstrap/ssh-user-ca
install -m 0644 -o root -g root "${ssh_ca_public}" /etc/ssh-bootstrap/ssh-user-ca.pub
install -m 0644 -o root -g root "${ROOT}/principals/release" /etc/ssh-bootstrap/principals/release
install -m 0644 -o root -g root "${ROOT}/principals/debug" /etc/ssh-bootstrap/principals/debug
install -m 0644 -o root -g root "${ROOT}/sshd/60-ssh-bootstrap.conf" /etc/ssh/sshd_config.d/60-ssh-bootstrap.conf
install -m 0644 -o root -g root "${ROOT}/systemd/ssh-bootstrapd.service" /etc/systemd/system/ssh-bootstrapd.service

if ! grep -Eq '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/\*\.conf([[:space:]]|$)' /etc/ssh/sshd_config; then
  {
    printf 'Include /etc/ssh/sshd_config.d/*.conf\n'
    cat /etc/ssh/sshd_config
  } >/etc/ssh/sshd_config.new
  chown --reference=/etc/ssh/sshd_config /etc/ssh/sshd_config.new
  chmod --reference=/etc/ssh/sshd_config /etc/ssh/sshd_config.new
  mv /etc/ssh/sshd_config.new /etc/ssh/sshd_config
fi
sshd -t
systemctl reload ssh.service 2>/dev/null || systemctl reload sshd.service
ln -sfn "${release_root}" /opt/ssh-bootstrap/current.new
mv -Tf /opt/ssh-bootstrap/current.new /opt/ssh-bootstrap/current
systemctl daemon-reload
systemctl enable --now ssh-bootstrapd.service

health_args=(--host "${health_host}" --ca /etc/ssh-bootstrap/tls/ca.crt)
if [[ -n "${server_name}" ]]; then
  health_args+=(--server-name "${server_name}")
fi
"${ROOT}/verify-install.sh" "${health_args[@]}"
printf 'committed\n' >"${transaction}/state"
rollback_armed="false"
trap - ERR
printf 'installed ssh-bootstrapd %s; transaction=%s\n' "${version}" "${transaction}"
