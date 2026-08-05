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

python3 - "$SCRIPT_DIR/config/block.env.example" <<'PY'
import sys

path = sys.argv[1]
config = {}
for line in open(path, encoding="utf-8"):
    line = line.strip()
    if not line or line.startswith("#"):
        continue
    if "=" not in line:
        raise SystemExit(f"invalid environment line: {line}")
    key, value = line.split("=", 1)
    config[key] = value

expected_keys = {
    "BLOCK_LOCAL_HTTP_ADDRESS",
    "BLOCK_STATE_DB",
    "BLOCK_HMI_STATIC_DIR",
    "BLOCK_MAINTENANCE_HTTPS_ADDRESS",
    "BLOCK_MAINTENANCE_TLS_CERT",
    "BLOCK_MAINTENANCE_TLS_KEY",
    "BLOCK_MAINTENANCE_SUPER_KEY_HASH",
    "BLOCK_MAINTENANCE_AUTHORIZED_KEYS",
    "BLOCK_MAINTENANCE_DEVICE_ID",
    "BLOCK_MAINTENANCE_ADVERTISED_HOST",
    "BLOCK_MQTTS_V2_ENABLED",
    "BLOCK_MQTTS_V2_ENDPOINT",
    "BLOCK_MQTTS_V2_CA",
    "BLOCK_MQTTS_V2_CLIENT_CERT",
    "BLOCK_MQTTS_V2_CLIENT_KEY",
    "BLOCK_MQTTS_V2_PRINCIPAL",
    "BLOCK_MQTTS_V2_SITE_ID",
    "BLOCK_MQTTS_V2_BLOCK_ID",
    "BLOCK_MQTTS_V2_DEVICE_ID",
}
if set(config) != expected_keys:
    raise SystemExit(f"unexpected environment keys: {set(config)}")
if any(key == "POINT" or key == "POINTS" or key.startswith("POINT_") or key.startswith("POINTS_") or "_POINT_" in key or "_POINTS_" in key or "WIFI" in key for key in config):
    raise SystemExit("points and Wi-Fi must not be persisted in deployment configuration")
if config["BLOCK_LOCAL_HTTP_ADDRESS"] != "127.0.0.1:8080":
    raise SystemExit("local HTTP must bind only 127.0.0.1:8080")
if config["BLOCK_MAINTENANCE_HTTPS_ADDRESS"] != "0.0.0.0:8443":
    raise SystemExit("maintenance HTTPS must bind 0.0.0.0:8443")
if config["BLOCK_HMI_STATIC_DIR"] != "/opt/block/current/web":
    raise SystemExit("the Block process must serve release web resources")
if config["BLOCK_MQTTS_V2_ENABLED"] != "false":
    raise SystemExit("the default configuration must start without BDM")
if config["BLOCK_MQTTS_V2_ENDPOINT"] != "mqtts://bdm.example.invalid:8883":
    raise SystemExit("BDM connection defaults must be MQTTS on 8883")
PY

UNIT_COUNT=$(find "$SCRIPT_DIR/systemd" -maxdepth 1 -type f -name '*.service' | wc -l)
if [ "$UNIT_COUNT" -ne 2 ] || [ ! -f "$SCRIPT_DIR/systemd/block.service" ] || [ ! -f "$SCRIPT_DIR/systemd/block-kiosk.service" ]; then
  fail "exactly block.service and block-kiosk.service are required"
fi

grep -Fx 'EnvironmentFile=/etc/block/block.env' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx '  -local-http-address $BLOCK_LOCAL_HTTP_ADDRESS \' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx '  -state-db $BLOCK_STATE_DB \' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx '  -hmi-static-dir $BLOCK_HMI_STATIC_DIR \' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx '  -maintenance-https-address $BLOCK_MAINTENANCE_HTTPS_ADDRESS \' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx '  -mqtts-v2-enabled $BLOCK_MQTTS_V2_ENABLED \' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx '  -mqtts-v2-device-id $BLOCK_MQTTS_V2_DEVICE_ID' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx 'ReadWritePaths=/var/lib/block' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx 'After=display-manager.service graphical.target block.service' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
grep -Fx 'User=block-ui' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
grep -Fx 'Group=block-ui' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
grep -Fx 'PermissionsStartOnly=true' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
grep -Fx 'Environment=DISPLAY=:0' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
if grep -Fx 'Environment=XAUTHORITY=/home/block-ui/.Xauthority' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null; then
  fail "kiosk must not require the missing block-ui Xauthority file"
fi
grep -Fx 'ExecStartPre=/usr/bin/env DISPLAY=:0 XAUTHORITY=/var/run/lightdm/root/:0 /usr/bin/xhost +SI:localuser:block-ui' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
grep -Fx 'ExecStartPre=/opt/block/current/deploy/health-check.sh --url http://127.0.0.1:8080/healthz --retries 30 --delay 1' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
grep -Fx 'ExecStart=/usr/bin/chromium-browser --kiosk --no-first-run --disable-session-crashed-bubble http://127.0.0.1:8080/' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
grep -Fx 'ExecStopPost=/usr/bin/env DISPLAY=:0 XAUTHORITY=/var/run/lightdm/root/:0 /usr/bin/xhost -SI:localuser:block-ui' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
if grep -R -n -E 'network-online|block-hmi|block-plc-simulator' "$SCRIPT_DIR/systemd"; then
  fail "v2 units must not depend on network-online or legacy services"
fi

printf 'Block deployment static verification passed\n'
