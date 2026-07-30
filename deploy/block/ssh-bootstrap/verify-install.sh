#!/usr/bin/env bash
set -euo pipefail

readonly ROOT="/etc/ssh-bootstrap"
readonly MANAGED_BEGIN='# BEGIN SSH-BOOTSTRAP MANAGED BLOCK'
readonly MANAGED_END='# END SSH-BOOTSTRAP MANAGED BLOCK'
readonly PRINCIPALS_ROOT="/opt/ssh-bootstrap/principals"
host=""
ca=""
server_name=""

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --host) host="$2"; shift 2 ;;
    --ca) ca="$2"; shift 2 ;;
    --server-name) server_name="$2"; shift 2 ;;
    --help)
      printf 'usage: verify-install.sh --host HOST --ca CA [--server-name DNS]\n'
      exit 0
      ;;
    *) printf 'ERROR: unknown argument: %s\n' "$1" >&2; exit 1 ;;
  esac
done

[[ -n "${host}" && -n "${ca}" ]] || {
  printf 'ERROR: --host and --ca are required\n' >&2
  exit 1
}

systemctl is-active --quiet ssh-bootstrapd.service
systemctl is-enabled --quiet ssh-bootstrapd.service
sshd -t
sshd -T | grep -Fqx 'trustedusercakeys /etc/ssh-bootstrap/ssh-user-ca.pub'
sshd -T | grep -Fqx 'authorizedprincipalsfile /opt/ssh-bootstrap/principals/%u'

sshd_mode="$(cat "${ROOT}/sshd-mode")"
case "${sshd_mode}" in
  drop-in)
    grep -Eq \
      '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/\*\.conf([[:space:]]|$)' \
      /etc/ssh/sshd_config
    cmp -s \
      /opt/ssh-bootstrap/current/deploy/sshd/60-ssh-bootstrap.conf \
      /etc/ssh/sshd_config.d/60-ssh-bootstrap.conf
    ;;
  inline)
    [[ "$(grep -Fxc "${MANAGED_BEGIN}" /etc/ssh/sshd_config)" == "1" ]]
    [[ "$(grep -Fxc "${MANAGED_END}" /etc/ssh/sshd_config)" == "1" ]]
    grep -Fqx 'TrustedUserCAKeys /etc/ssh-bootstrap/ssh-user-ca.pub' /etc/ssh/sshd_config
    grep -Fqx 'AuthorizedPrincipalsFile /opt/ssh-bootstrap/principals/%u' /etc/ssh/sshd_config
    ;;
  *)
    printf 'ERROR: unknown installed sshd configuration mode: %s\n' "${sshd_mode}" >&2
    exit 1
    ;;
esac

[[ "$(stat -c '%U:%G:%a' "${ROOT}/config.json")" == "root:ssh-bootstrap:640" ]]
[[ "$(stat -c '%U:%G:%a' "${ROOT}/tls/server.key")" == "root:ssh-bootstrap:640" ]]
[[ "$(stat -c '%U:%G:%a' "${ROOT}/tls/ca.crt")" == "root:root:644" ]]
[[ "$(stat -c '%U:%G:%a' "${ROOT}/ssh-user-ca")" == "root:ssh-bootstrap:640" ]]
[[ "$(stat -c '%U:%G:%a' /var/lib/ssh-bootstrap)" == "ssh-bootstrap:ssh-bootstrap:700" ]]
[[ "$(stat -c '%U:%G:%a' "${PRINCIPALS_ROOT}")" == "root:root:755" ]]
[[ "$(cat "${PRINCIPALS_ROOT}/release")" == "release" ]]
[[ "$(cat "${PRINCIPALS_ROOT}/debug")" == "debug" ]]
[[ ! -e "${PRINCIPALS_ROOT}/root" ]]
[[ "$(id -u release)" -ne 0 && "$(id -u debug)" -ne 0 ]]

version_output="$(/opt/ssh-bootstrap/current/bin/ssh-bootstrapd -version)"
[[ -n "${version_output}" && "${version_output}" != "development" ]]

health_args=(--host "${host}" --ca "${ca}")
if [[ -n "${server_name}" ]]; then
  health_args+=(--server-name "${server_name}")
fi
/opt/ssh-bootstrap/current/deploy/health-check.sh "${health_args[@]}"

printf 'installed ssh-bootstrapd passed static host, sshd, version and trusted TLS checks\n'
