#!/usr/bin/env bash
set -euo pipefail

[[ "${1:-}" == "--execute" && "$#" -eq 1 ]] || {
  printf 'ERROR: rollback.sh requires --execute\n' >&2
  exit 1
}
[[ "${BLOCK_RELEASE_ROLE:-}" == "BLK-REL" ]] || {
  printf 'ERROR: BLOCK_RELEASE_ROLE=BLK-REL is required\n' >&2
  exit 1
}
test_root="${SSH_BOOTSTRAP_TEST_ROOT:-}"
if [[ -n "${test_root}" ]]; then
  [[ "${test_root}" == /* && "${test_root}" != "/" && -d "${test_root}" ]] || {
    printf 'ERROR: SSH_BOOTSTRAP_TEST_ROOT must be an existing absolute non-root directory\n' >&2
    exit 1
  }
elif [[ "${EUID}" -ne 0 ]]; then
  printf 'ERROR: rollback.sh must run as root\n' >&2
  exit 1
fi

target_path() {
  printf '%s%s' "${test_root}" "$1"
}

pointer="$(target_path /var/lib/ssh-bootstrap-release/current-transaction)"
[[ -f "${pointer}" ]] || {
  printf 'ERROR: no SSH bootstrap transaction is recorded\n' >&2
  exit 1
}
recorded_transaction="$(cat "${pointer}")"
transaction="${recorded_transaction}"
transaction_root="$(target_path "${transaction}")"
[[ "${transaction}" == /var/lib/ssh-bootstrap-release/transactions/* && -f "${transaction_root}/managed.tsv" ]] || {
  printf 'ERROR: transaction pointer is invalid\n' >&2
  exit 1
}
sshd_mode="$(cat "${transaction_root}/sshd-mode")"
[[ "${sshd_mode}" == "drop-in" || "${sshd_mode}" == "inline" ]] || {
  printf 'ERROR: transaction has an invalid sshd configuration mode\n' >&2
  exit 1
}

systemctl disable --now ssh-bootstrapd.service 2>/dev/null || true
while IFS=$'\t' read -r path state key; do
  destination="$(target_path "${path}")"
  if [[ "${state}" == "present" ]]; then
    install -d "$(dirname "${destination}")"
    if [[ -d "${destination}" && ! -L "${destination}" ]]; then
      rm -rf -- "${destination}"
    else
      rm -f -- "${destination}"
    fi
    cp -a "${transaction_root}/backup/${key}" "${destination}"
  else
    if [[ -d "${destination}" && ! -L "${destination}" ]]; then
      rm -rf -- "${destination}"
    else
      rm -f -- "${destination}"
    fi
  fi
done <"${transaction_root}/managed.tsv"

current="$(target_path /opt/ssh-bootstrap/current)"
if [[ -f "${transaction_root}/previous-current" ]]; then
  previous="$(cat "${transaction_root}/previous-current")"
  ln -sfn "${previous}" "${current}.new"
  mv -Tf "${current}.new" "${current}"
else
  rm -f "${current}"
fi

sshd -t
systemctl reload ssh.service 2>/dev/null || systemctl reload sshd.service
systemctl daemon-reload
if [[ "$(cat "${transaction_root}/previous-enabled" 2>/dev/null || true)" == "enabled" ]]; then
  systemctl enable ssh-bootstrapd.service
fi
if [[ "$(cat "${transaction_root}/previous-active" 2>/dev/null || true)" == "active" ]]; then
  systemctl start ssh-bootstrapd.service
fi
if [[ -f "${transaction_root}/previous-transaction" ]]; then
  cp -a "${transaction_root}/previous-transaction" "${pointer}.new"
  mv -Tf "${pointer}.new" "${pointer}"
else
  rm -f "${pointer}"
fi
printf 'rolled-back\n' >"${transaction_root}/state"
printf 'rolled back SSH bootstrap transaction %s; nonce database and release files were retained\n' "${transaction}"
