#!/usr/bin/env bash
set -euo pipefail

URL=https://127.0.0.1:8444/healthz
CA_FILE=/etc/block/certs/maintenance-ca.crt
EXPECTED_VERSION=
RETRIES=1
DELAY_SECONDS=1

usage() {
  cat <<'EOF'
Usage:
  deploy/block/health-check.sh [--url <local-health-url>]
      [--ca-file <public-ca-certificate>]
      [--expected-version <version>] [--retries <count>] [--delay <seconds>]

The business health endpoint is loopback HTTPS on 127.0.0.1:8444. Certificate
and hostname validation are required; this script never disables TLS checks.
External maintenance HTTPS remains on port 8443 and is not probed here.
EOF
}

fail() {
  printf 'health-check: %s\n' "$*" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --url)
      [ "$#" -ge 2 ] || fail "--url needs a value"
      URL=$2
      shift 2
      ;;
    --ca-file)
      [ "$#" -ge 2 ] || fail "--ca-file needs a value"
      CA_FILE=$2
      shift 2
      ;;
    --expected-version)
      [ "$#" -ge 2 ] || fail "--expected-version needs a value"
      EXPECTED_VERSION=$2
      shift 2
      ;;
    --retries)
      [ "$#" -ge 2 ] || fail "--retries needs a value"
      RETRIES=$2
      shift 2
      ;;
    --delay)
      [ "$#" -ge 2 ] || fail "--delay needs a value"
      DELAY_SECONDS=$2
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

case "$RETRIES" in
  ''|*[!0-9]*) fail "--retries must be a positive integer" ;;
esac
[ "$RETRIES" -gt 0 ] || fail "--retries must be a positive integer"
case "$DELAY_SECONDS" in
  ''|*[!0-9]*) fail "--delay must be a non-negative integer" ;;
esac
command -v curl >/dev/null 2>&1 || fail "curl is required"
case "$URL" in
  https://127.0.0.1:8444/*) ;;
  *) fail "local health URL must be https://127.0.0.1:8444/..." ;;
esac
[ -n "$CA_FILE" ] || fail "--ca-file is required"
[ -r "$CA_FILE" ] || fail "CA certificate is not readable: $CA_FILE"

if [ -n "$EXPECTED_VERSION" ]; then
  SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
  CURRENT_VERSION=$(cat "$SCRIPT_DIR/../VERSION")
  [ "$CURRENT_VERSION" = "$EXPECTED_VERSION" ] || fail "current release version is $CURRENT_VERSION, expected $EXPECTED_VERSION"
fi

ATTEMPT=1
while [ "$ATTEMPT" -le "$RETRIES" ]; do
  if curl --proto '=https' --tlsv1.2 --cacert "$CA_FILE" --fail --silent --show-error --max-time 5 "$URL" >/dev/null; then
    printf 'health check passed: %s\n' "$URL"
    exit 0
  fi
  if [ "$ATTEMPT" -lt "$RETRIES" ]; then
    sleep "$DELAY_SECONDS"
  fi
  ATTEMPT=$((ATTEMPT + 1))
done

fail "health endpoint did not respond: $URL"
