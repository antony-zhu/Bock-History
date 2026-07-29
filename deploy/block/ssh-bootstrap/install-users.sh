#!/usr/bin/env bash
set -euo pipefail

[[ "${EUID}" -eq 0 ]] || {
  printf 'ERROR: install-users.sh must run as root\n' >&2
  exit 1
}

if ! getent group ssh-bootstrap >/dev/null; then
  groupadd --system ssh-bootstrap
fi
if ! id -u ssh-bootstrap >/dev/null 2>&1; then
  useradd --system \
    --gid ssh-bootstrap \
    --home-dir /var/lib/ssh-bootstrap \
    --shell /usr/sbin/nologin \
    ssh-bootstrap
fi
usermod --lock ssh-bootstrap

for account in release debug; do
  if ! id -u "${account}" >/dev/null 2>&1; then
    useradd --create-home --shell /bin/bash "${account}"
  fi
  [[ "$(id -u "${account}")" -ne 0 ]] || {
    printf 'ERROR: %s must not be root\n' "${account}" >&2
    exit 1
  }
  usermod --lock "${account}"
done
