#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf 'install-users: %s\n' "$*" >&2
  exit 1
}

[ "$(id -u)" -eq 0 ] || fail "must run as root"

if ! getent group block >/dev/null; then
  groupadd --system block
fi
if ! id -u block >/dev/null 2>&1; then
  useradd --system --gid block --create-home --home-dir /home/block --shell /bin/bash block
fi

if ! getent group block-ui >/dev/null; then
  groupadd --system block-ui
fi
if ! id -u block-ui >/dev/null 2>&1; then
  useradd --system --gid block-ui --create-home --home-dir /home/block-ui --shell /usr/sbin/nologin block-ui
fi
