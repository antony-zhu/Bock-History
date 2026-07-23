#!/usr/bin/env bash
set -euo pipefail

: "${BLOCK_HMI_CA:?set BLOCK_HMI_CA to the trusted CA certificate path}"
: "${BLOCK_HMI_HEALTH_URL:=https://127.0.0.1:8443/healthz}"
: "${BLOCK_AGENT_SOCKET:=/run/block-agent/api/block-agent.sock}"
: "${BLOCK_SIMULATOR_IO_SOCKET:=/run/block-plc/io/io.sock}"
: "${BLOCK_EXPECT_SIMULATOR:=false}"

case "${BLOCK_HMI_HEALTH_URL}" in
  https://127.0.0.1:8443/*|https://localhost:8443/*|https://\[::1\]:8443/*) ;;
  *)
    printf 'ERROR: HMI health URL must be loopback HTTPS on port 8443\n' >&2
    exit 2
    ;;
esac

if [[ ! -r "${BLOCK_HMI_CA}" ]]; then
  printf 'ERROR: trusted CA is not readable: %s\n' "${BLOCK_HMI_CA}" >&2
  exit 2
fi

if [[ ! -S "${BLOCK_AGENT_SOCKET}" ]]; then
  printf 'ERROR: Agent Unix socket is missing: %s\n' "${BLOCK_AGENT_SOCKET}" >&2
  exit 1
fi

curl --fail --silent --show-error \
  --connect-timeout 3 \
  --max-time 8 \
  --proto '=https' \
  --tlsv1.2 \
  --cacert "${BLOCK_HMI_CA}" \
  "${BLOCK_HMI_HEALTH_URL}"

curl --fail --silent --show-error \
  --connect-timeout 3 \
  --max-time 8 \
  --proto '=http' \
  --unix-socket "${BLOCK_AGENT_SOCKET}" \
  http://localhost/healthz

if [[ "${BLOCK_EXPECT_SIMULATOR}" == "true" && ! -S "${BLOCK_SIMULATOR_IO_SOCKET}" ]]; then
  printf 'ERROR: Simulator I/O Unix socket is missing: %s\n' "${BLOCK_SIMULATOR_IO_SOCKET}" >&2
  exit 1
fi

printf 'OK: trusted HMI TLS, Agent UDS%s\n' \
  "$([[ "${BLOCK_EXPECT_SIMULATOR}" == "true" ]] && printf ', Simulator UDS' || true)"
