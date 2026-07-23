#!/usr/bin/env bash
set -euo pipefail

readonly NOLOGIN_SHELL="/usr/sbin/nologin"

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

require_release_role() {
  [[ "${EUID}" -eq 0 ]] || die "run as root"
  [[ "${BLOCK_RELEASE_ROLE:-}" == "BLK-REL" ]] ||
    die "set BLOCK_RELEASE_ROLE=BLK-REL; only BLK-REL may change a host"
}

ensure_group() {
  local group_name="$1"
  local existing_gid

  if existing_gid="$(getent group "${group_name}" | awk -F: '{print $3}')"; then
    [[ -n "${existing_gid}" ]] || die "group ${group_name} has an invalid entry"
    [[ "${existing_gid}" -ne 0 ]] || die "group ${group_name} must not use GID 0"
    return
  fi

  groupadd --system "${group_name}"
}

ensure_user() {
  local user_name="$1"
  local primary_group="$2"
  local supplementary_groups="$3"
  local uid
  local password_status

  if getent passwd "${user_name}" >/dev/null; then
    uid="$(id -u "${user_name}")"
    [[ "${uid}" -gt 0 && "${uid}" -lt 1000 ]] ||
      die "existing ${user_name} is not a non-root system account"
    usermod \
      --gid "${primary_group}" \
      --groups "${supplementary_groups}" \
      --home /nonexistent \
      --shell "${NOLOGIN_SHELL}" \
      "${user_name}"
  else
    useradd \
      --system \
      --gid "${primary_group}" \
      --groups "${supplementary_groups}" \
      --home-dir /nonexistent \
      --no-create-home \
      --shell "${NOLOGIN_SHELL}" \
      "${user_name}"
  fi

  usermod --lock "${user_name}"
  password_status="$(passwd -S "${user_name}" | awk '{print $2}')"
  [[ "${password_status}" == "L" || "${password_status}" == "LK" ]] ||
    die "${user_name} password must be locked; status is ${password_status:-unknown}"
}

assert_membership() {
  local user_name="$1"
  shift
  local group_name
  local actual_groups

  actual_groups=" $(id -nG "${user_name}") "
  for group_name in "$@"; do
    [[ "${actual_groups}" == *" ${group_name} "* ]] ||
      die "${user_name} is not a member of ${group_name}"
  done
}

require_release_role

for command_name in awk getent groupadd id passwd useradd usermod; do
  command -v "${command_name}" >/dev/null 2>&1 ||
    die "required command is missing: ${command_name}"
done

for group_name in \
  block-agent \
  block-hmi \
  block-simulator \
  block-hmi-api \
  block-sim-io \
  block-sim-control; do
  ensure_group "${group_name}"
done

# Exact supplementary memberships are intentional. There is no shared "block"
# group: each cross-process path has one narrowly-scoped access group.
ensure_user block-agent block-agent "block-hmi-api,block-sim-io"
ensure_user block-hmi block-hmi "block-hmi-api"
ensure_user block-simulator block-simulator "block-sim-io,block-sim-control"

assert_membership block-agent block-agent block-hmi-api block-sim-io
assert_membership block-hmi block-hmi block-hmi-api
assert_membership block-simulator block-simulator block-sim-io block-sim-control

printf 'OK: Block service users and least-privilege groups are present\n'
