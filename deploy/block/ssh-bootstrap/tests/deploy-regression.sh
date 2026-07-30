#!/usr/bin/env bash
set -euo pipefail

readonly ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"

node - "${ROOT}/config/ssh-bootstrap.example.json" <<'JS'
const fs = require("fs");
const config = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
if (
  config.targetNode !== "BLOCK"
  || !config.listenAddress.endsWith(":9443")
  || config.releaseUsername !== "release"
  || config.debugUsername !== "debug"
  || config.nonceDatabasePath !== "/var/lib/ssh-bootstrap/nonces.db"
  || Object.hasOwn(config, "privateKey")
  || Object.hasOwn(config, "password")
) {
  throw new Error("SSH bootstrap example violates deployment invariants");
}
JS

"${ROOT}/install.sh" --help >/dev/null
"${ROOT}/health-check.sh" --help >/dev/null
"${ROOT}/verify-install.sh" --help >/dev/null
if "${ROOT}/install.sh" >/dev/null 2>&1; then
  printf 'ERROR: install unexpectedly ran without --execute and Release role\n' >&2
  exit 1
fi
if BLOCK_RELEASE_ROLE=OTHER "${ROOT}/rollback.sh" --execute >/dev/null 2>&1; then
  printf 'ERROR: rollback unexpectedly accepted the wrong role\n' >&2
  exit 1
fi

grep -Fqx 'release' "${ROOT}/principals/release"
grep -Fqx 'debug' "${ROOT}/principals/debug"
! grep -Rqx 'root' "${ROOT}/principals"
"${ROOT}/tests/install-failure-rollback.sh"
printf 'SSH bootstrap deployment regression passed\n'
