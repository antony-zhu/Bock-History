#!/usr/bin/env bash
set -euo pipefail

host=""
port="9443"
ca=""
server_name=""

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --host) host="$2"; shift 2 ;;
    --port) port="$2"; shift 2 ;;
    --ca) ca="$2"; shift 2 ;;
    --server-name) server_name="$2"; shift 2 ;;
    --help)
      printf 'usage: health-check.sh --host HOST --ca CA [--port 9443] [--server-name DNS]\n'
      exit 0
      ;;
    *) printf 'ERROR: unknown argument: %s\n' "$1" >&2; exit 1 ;;
  esac
done

[[ -n "${host}" && -n "${ca}" ]] || {
  printf 'ERROR: --host and --ca are required\n' >&2
  exit 1
}
[[ -r "${ca}" ]] || {
  printf 'ERROR: CA is not readable: %s\n' "${ca}" >&2
  exit 1
}

verify_args=(-verify_return_error)
if [[ -n "${server_name}" ]]; then
  verify_args+=(-servername "${server_name}" -verify_hostname "${server_name}")
elif [[ "${host}" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ || "${host}" == *:* ]]; then
  verify_args+=(-verify_ip "${host}")
fi

tls_output="$(
  openssl s_client \
    -connect "${host}:${port}" \
    -CAfile "${ca}" \
    "${verify_args[@]}" \
    </dev/null 2>&1
)"
grep -Fq 'Verify return code: 0 (ok)' <<<"${tls_output}" || {
  printf 'ERROR: trusted HTTPS TLS handshake failed\n' >&2
  exit 1
}

if curl --silent --show-error --max-time 2 --output /dev/null "http://${host}:${port}/v1/ssh/cert"; then
  printf 'ERROR: plaintext HTTP unexpectedly received a successful response\n' >&2
  exit 1
fi

printf 'ssh-bootstrap HTTPS listener is healthy; signed certificate issuance remains an external Release check\n'
