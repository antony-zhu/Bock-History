#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)

fail() {
  printf 'verify-static: %s\n' "$*" >&2
  exit 1
}

for SCRIPT in build.sh install-users.sh install.sh health-check.sh version.sh rollback.sh verify-install.sh verify-static.sh tests/deploy-regression.sh; do
  bash -n "$SCRIPT_DIR/$SCRIPT"
done

python3 - "$SCRIPT_DIR/config/block.example.json" <<'PY'
import json
import sys

path = sys.argv[1]
with open(path, encoding="utf-8") as source:
    config = json.load(source)

expected_top_level = {"identity", "paths", "server", "plc", "mqtt", "session", "ssh"}
if set(config) != expected_top_level:
    raise SystemExit(f"unexpected configuration sections: {set(config)}")

def reject_points(value, path=""):
    if isinstance(value, dict):
        for key, item in value.items():
            current = f"{path}.{key}" if path else key
            if key == "points":
                raise SystemExit(f"points must not be persisted in configuration: {current}")
            reject_points(item, current)
    elif isinstance(value, list):
        for index, item in enumerate(value):
            reject_points(item, f"{path}[{index}]")

reject_points(config)
if config["server"]["localHttpAddress"] != "127.0.0.1:8080":
    raise SystemExit("local HTTP must bind only 127.0.0.1:8080")
if config["server"]["maintenanceHttpsAddress"] != "0.0.0.0:8443":
    raise SystemExit("maintenance HTTPS must bind 0.0.0.0:8443")
if config["paths"]["webRoot"] != "/opt/block/current/web":
    raise SystemExit("the Block process must serve release web resources")
if config["plc"]["pollInterval"] != "50ms":
    raise SystemExit("PLC poll interval must be 50ms")
if config["mqtt"]["enabled"] is not False:
    raise SystemExit("the default configuration must start without BDM")
if config["mqtt"]["scheme"] != "mqtts" or config["mqtt"]["port"] != 8883:
    raise SystemExit("BDM connection defaults must be MQTTS on 8883")
if config["mqtt"]["qos"] != 0:
    raise SystemExit("MQTTS v2 status must use QoS 0")
for forbidden in ("outbox", "replayAfterDisconnect", "wifi"):
    if forbidden in config["mqtt"] or forbidden in config:
        raise SystemExit(f"forbidden deployment configuration field: {forbidden}")
PY

UNIT_LIST=$(find "$SCRIPT_DIR/systemd" -maxdepth 1 -type f -name '*.service' -printf '%f\n' | sort)
EXPECTED_UNITS='block-kiosk.service
block.service'
[ "$UNIT_LIST" = "$EXPECTED_UNITS" ] || fail "exactly block.service and block-kiosk.service are required"

grep -Fx 'ExecStart=/opt/block/current/bin/block-agent -config /etc/block/block.json' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx 'ReadWritePaths=/var/lib/block' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx 'ExecStartPre=/opt/block/current/deploy/health-check.sh --url http://127.0.0.1:8080/healthz --retries 30 --delay 1' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
grep -Fx 'ExecStart=/usr/bin/chromium-browser --kiosk --no-first-run --disable-session-crashed-bubble http://127.0.0.1:8080/' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
if grep -R -n -E 'network-online|block-hmi|block-plc-simulator' "$SCRIPT_DIR/systemd"; then
  fail "v2 units must not depend on network-online or legacy services"
fi

printf 'Block deployment static verification passed\n'
