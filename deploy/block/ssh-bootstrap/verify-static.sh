#!/usr/bin/env bash
set -euo pipefail

readonly ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

for script in \
  "${ROOT}/health-check.sh" \
  "${ROOT}/install-users.sh" \
  "${ROOT}/install.sh" \
  "${ROOT}/rollback.sh" \
  "${ROOT}/verify-install.sh" \
  "${ROOT}/verify-static.sh" \
  "${ROOT}/tests/deploy-regression.sh" \
  "${ROOT}/tests/install-failure-rollback.sh"; do
  [[ -x "${script}" ]] || die "script is not executable: ${script#${ROOT}/}"
  bash -n "${script}"
done

command -v node >/dev/null || die "node is required for static validation"
node -e 'JSON.parse(require("fs").readFileSync(process.argv[1], "utf8"))' \
  "${ROOT}/config/ssh-bootstrap.example.json"
grep -Fqx 'User=ssh-bootstrap' "${ROOT}/systemd/ssh-bootstrapd.service"
grep -Fqx 'Group=ssh-bootstrap' "${ROOT}/systemd/ssh-bootstrapd.service"
grep -Fqx 'ExecStart=/opt/ssh-bootstrap/current/bin/ssh-bootstrapd -config /etc/ssh-bootstrap/config.json' "${ROOT}/systemd/ssh-bootstrapd.service"
grep -Fqx 'TrustedUserCAKeys /etc/ssh-bootstrap/ssh-user-ca.pub' "${ROOT}/sshd/60-ssh-bootstrap.conf"
grep -Fqx 'AuthorizedPrincipalsFile /etc/ssh-bootstrap/principals/%u' "${ROOT}/sshd/60-ssh-bootstrap.conf"
[[ "$(cat "${ROOT}/principals/release")" == "release" ]]
[[ "$(cat "${ROOT}/principals/debug")" == "debug" ]]
[[ ! -e "${ROOT}/principals/root" ]]
grep -Fq '"listenAddress": "0.0.0.0:9443"' "${ROOT}/config/ssh-bootstrap.example.json"
grep -Fq '"targetNode": "BLOCK"' "${ROOT}/config/ssh-bootstrap.example.json"
grep -Fq '"nonceDatabasePath": "/var/lib/ssh-bootstrap/nonces.db"' "${ROOT}/config/ssh-bootstrap.example.json"

if grep -REn \
  --exclude='verify-static.sh' \
  --exclude='health-check.sh' \
  -- \
  'curl[[:space:]].*(-k|--insecure)|InsecureSkipVerify|http://.*9443|BEGIN (OPENSSH |RSA |EC )?PRIVATE KEY|password[[:space:]]*[:=]' \
  "${ROOT}" >/dev/null; then
  die "forbidden TLS bypass, private key, password or plaintext bootstrap URL found"
fi

"${ROOT}/tests/deploy-regression.sh"
printf 'SSH bootstrap deployment static verification passed\n'
