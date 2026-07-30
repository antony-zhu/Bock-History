#!/usr/bin/env bash

readonly SSH_BOOTSTRAP_MANAGED_BEGIN='# BEGIN SSH-BOOTSTRAP MANAGED BLOCK'
readonly SSH_BOOTSTRAP_MANAGED_END='# END SSH-BOOTSTRAP MANAGED BLOCK'

ssh_bootstrap_detect_include_support() {
  local sshd_command="$1"
  local probe="$2"
  local probe_error="$3"

  printf 'Include /dev/null\n' >"${probe}"
  if "${sshd_command}" -t -f "${probe}" 2>"${probe_error}"; then
    return 0
  fi
  if grep -Eiq \
    'Bad configuration option:[[:space:]]*Include|Unsupported option[[:space:]]+Include' \
    "${probe_error}"; then
    return 1
  fi
  printf 'ERROR: unable to determine sshd Include support:\n' >&2
  cat "${probe_error}" >&2
  return 2
}

ssh_bootstrap_ensure_drop_in_include() {
  local sshd_config="$1"
  local new_config="${sshd_config}.ssh-bootstrap.new"

  if grep -Eq \
    '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/\*\.conf([[:space:]]|$)' \
    "${sshd_config}" 2>/dev/null; then
    return
  fi

  {
    printf 'Include /etc/ssh/sshd_config.d/*.conf\n'
    if [[ -f "${sshd_config}" ]]; then
      cat "${sshd_config}"
    fi
  } >"${new_config}"
  if [[ -e "${sshd_config}" ]]; then
    chown --reference="${sshd_config}" "${new_config}"
    chmod --reference="${sshd_config}" "${new_config}"
  else
    chown root:root "${new_config}"
    chmod 0644 "${new_config}"
  fi
  mv -Tf "${new_config}" "${sshd_config}"
}

ssh_bootstrap_ensure_inline_block() {
  local sshd_config="$1"
  local directive_file="$2"
  local begin_count
  local end_count
  local actual
  local expected

  begin_count="$(grep -Fxc "${SSH_BOOTSTRAP_MANAGED_BEGIN}" "${sshd_config}" 2>/dev/null || true)"
  end_count="$(grep -Fxc "${SSH_BOOTSTRAP_MANAGED_END}" "${sshd_config}" 2>/dev/null || true)"
  if [[ "${begin_count}" == "1" && "${end_count}" == "1" ]]; then
    actual="$(sed -n \
      "\\|^${SSH_BOOTSTRAP_MANAGED_BEGIN}$|,\\|^${SSH_BOOTSTRAP_MANAGED_END}$|p" \
      "${sshd_config}")"
    expected="$(
      printf '%s\n' "${SSH_BOOTSTRAP_MANAGED_BEGIN}"
      cat "${directive_file}"
      printf '%s\n' "${SSH_BOOTSTRAP_MANAGED_END}"
    )"
    [[ "${actual}" == "${expected}" ]] || {
      printf 'ERROR: existing SSH bootstrap managed block does not match this release\n' >&2
      return 1
    }
    return
  fi
  [[ "${begin_count}" == "0" && "${end_count}" == "0" ]] || {
    printf 'ERROR: sshd_config contains an incomplete or duplicate SSH bootstrap managed block\n' >&2
    return 1
  }

  if [[ ! -e "${sshd_config}" ]]; then
    install -m 0644 -o root -g root /dev/null "${sshd_config}"
  fi
  {
    if [[ -s "${sshd_config}" ]]; then
      printf '\n'
    fi
    printf '%s\n' "${SSH_BOOTSTRAP_MANAGED_BEGIN}"
    cat "${directive_file}"
    printf '%s\n' "${SSH_BOOTSTRAP_MANAGED_END}"
  } >>"${sshd_config}"
}
