#!/usr/bin/env bash
set -euo pipefail

[[ "${1:-}" == "--execute" ]] || {
  printf 'ERROR: rollback.sh requires --execute\n' >&2
  exit 1
}
[[ "${BLOCK_RELEASE_ROLE:-}" == "BLK-REL" ]] || {
  printf 'ERROR: BLOCK_RELEASE_ROLE=BLK-REL is required\n' >&2
  exit 1
}
[[ "${EUID}" -eq 0 ]] || {
  printf 'ERROR: rollback.sh must run as root\n' >&2
  exit 1
}

pointer=/var/lib/ssh-bootstrap-release/current-transaction
[[ -f "${pointer}" ]] || {
  printf 'ERROR: no SSH bootstrap transaction is recorded\n' >&2
  exit 1
}
transaction="$(cat "${pointer}")"
[[ "${transaction}" == /var/lib/ssh-bootstrap-release/transactions/* && -f "${transaction}/managed.tsv" ]] || {
  printf 'ERROR: transaction pointer is invalid\n' >&2
  exit 1
}

systemctl disable --now ssh-bootstrapd.service 2>/dev/null || true
while IFS=$'\t' read -r path state key; do
  if [[ "${state}" == "present" ]]; then
    install -d "$(dirname "${path}")"
    rm -f "${path}"
    cp -a "${transaction}/backup/${key}" "${path}"
  else
    rm -f "${path}"
  fi
done <"${transaction}/managed.tsv"

if [[ -f "${transaction}/previous-current" ]]; then
  previous="$(cat "${transaction}/previous-current")"
  ln -sfn "${previous}" /opt/ssh-bootstrap/current.new
  mv -Tf /opt/ssh-bootstrap/current.new /opt/ssh-bootstrap/current
else
  rm -f /opt/ssh-bootstrap/current
fi

sshd -t
systemctl reload ssh.service 2>/dev/null || systemctl reload sshd.service
systemctl daemon-reload
if [[ "$(cat "${transaction}/previous-enabled" 2>/dev/null || true)" == "enabled" ]]; then
  systemctl enable ssh-bootstrapd.service
fi
if [[ "$(cat "${transaction}/previous-active" 2>/dev/null || true)" == "active" ]]; then
  systemctl start ssh-bootstrapd.service
fi
rm -f "${pointer}"
printf 'rolled back SSH bootstrap transaction %s; nonce database and release files were retained\n' "${transaction}"
