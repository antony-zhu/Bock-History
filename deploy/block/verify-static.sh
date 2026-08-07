#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)

fail() {
  printf 'verify-static: %s\n' "$*" >&2
  exit 1
}

for SCRIPT in build.sh install-users.sh install.sh health-check.sh version.sh rollback.sh verify-install.sh verify-static.sh tests/deploy-regression.sh tests/install-rollback-regression.sh; do
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
    "BLOCK_LOCAL_HTTPS_ADDRESS",
    "BLOCK_LOCAL_TLS_CERT",
    "BLOCK_LOCAL_TLS_KEY",
    "BLOCK_LOCAL_TLS_CA",
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
if config["BLOCK_LOCAL_HTTPS_ADDRESS"] != "127.0.0.1:8444":
    raise SystemExit("local HTTPS must bind only 127.0.0.1:8444")
if config["BLOCK_LOCAL_TLS_CERT"] != "/etc/block/certs/block-hmi.crt":
    raise SystemExit("local HMI must use the verified local leaf certificate path")
if config["BLOCK_LOCAL_TLS_KEY"] != "/etc/block/certs/block-hmi.key":
    raise SystemExit("local HMI must use the verified local private-key path")
if config["BLOCK_LOCAL_TLS_CA"] != "/usr/local/share/ca-certificates/block-dmp-blk-rel-001.crt":
    raise SystemExit("local HMI health checks must use the deployed public CA path")
if config["BLOCK_MAINTENANCE_HTTPS_ADDRESS"] != "0.0.0.0:8443":
    raise SystemExit("maintenance HTTPS must bind 0.0.0.0:8443")
if config["BLOCK_HMI_STATIC_DIR"] != "/opt/block/current/web":
    raise SystemExit("the Block process must serve release web resources")
if config["BLOCK_MQTTS_V2_ENABLED"] != "false":
    raise SystemExit("the default configuration must start without BDM")
if config["BLOCK_MQTTS_V2_ENDPOINT"] != "mqtts://bdm.example.invalid:8883":
    raise SystemExit("BDM connection defaults must be MQTTS on 8883")
PY

python3 - "$SCRIPT_DIR/install.sh" <<'PY'
import sys

source = open(sys.argv[1], encoding="utf-8").read()
for required in (
    'CANDIDATE_DEPLOY_DIR=$ARTIFACT_DIR/deploy',
    'validate_candidate_deploy',
    'run the candidate artifact\'s deploy/install.sh, not an installer from /opt/block/current',
    '"$CANDIDATE_DEPLOY_DIR/$TOOL" "$RELEASE_DIR/deploy/$TOOL"',
    '"$CANDIDATE_DEPLOY_DIR/tests/install-rollback-regression.sh" "$RELEASE_DIR/deploy/tests/install-rollback-regression.sh"',
):
    if required not in source:
        raise SystemExit(f"install is missing candidate deploy behavior: {required}")
steps = (
    'ROLLBACK_ARMED=true',
    'install -m 0640 -o root -g block "$CONFIG_FILE" "$CONFIG_ROOT/block.env"',
    'mv -Tf "$NEXT_LINK" "$CURRENT_LINK"',
    'systemctl daemon-reload',
    'systemctl enable block.service',
    'systemctl enable block-kiosk.service',
    'systemctl restart block.service',
    '"$CURRENT_LINK/deploy/health-check.sh" --ca-file "$BLOCK_LOCAL_TLS_CA" --expected-version "$VERSION" --retries 30 --delay 1',
    'systemctl restart block-kiosk.service',
)
positions = []
for step in steps:
    try:
        positions.append(source.index(step))
    except ValueError:
        raise SystemExit(f"install is missing release activation step: {step}")
if positions != sorted(positions):
    raise SystemExit("install must enable then restart Block, pass the health gate, then restart kiosk")
if "systemctl enable --now" in source:
    raise SystemExit("install must restart active services after enabling them")
if 'trap rollback_install ERR' not in source or '"$SCRIPT_DIR/rollback.sh" --execute --snapshot "$INSTALL_SNAPSHOT"' not in source:
    raise SystemExit("install failure after current switch must enter rollback")
if source.rfind("ROLLBACK_ARMED=false") <= positions[-1]:
    raise SystemExit("install rollback must remain armed through the kiosk restart")
tls_validation = source.rindex('validate_local_tls_material "$BLOCK_LOCAL_TLS_CERT" "$BLOCK_LOCAL_TLS_KEY" "$BLOCK_LOCAL_TLS_CA"')
snapshot = source.rindex('\nsnapshot_install_state\nROLLBACK_ARMED=true')
if tls_validation >= source.index('"$SCRIPT_DIR/install-users.sh"'):
    raise SystemExit("TLS material must be validated before any install-user mutation")
if snapshot >= source.index('install -m 0640 -o root -g block "$CONFIG_FILE"'):
    raise SystemExit("install must snapshot the old state before writing the new config")
PY

python3 - "$SCRIPT_DIR/build.sh" <<'PY'
import sys

source = open(sys.argv[1], encoding="utf-8").read()
for required in (
    'copy_deploy_bundle()',
    '"$OUTPUT_DIR/deploy/config" "$OUTPUT_DIR/deploy/systemd" "$OUTPUT_DIR/deploy/tests"',
    'for tool in build.sh install-users.sh install.sh health-check.sh version.sh rollback.sh verify-install.sh verify-static.sh; do',
    '"$SCRIPT_DIR/config/block.env.example" "$OUTPUT_DIR/deploy/config/block.env.example"',
    '"$SCRIPT_DIR/systemd/block.service" "$OUTPUT_DIR/deploy/systemd/block.service"',
    '"$SCRIPT_DIR/systemd/block-kiosk.service" "$OUTPUT_DIR/deploy/systemd/block-kiosk.service"',
    '"$SCRIPT_DIR/tests/install-rollback-regression.sh" "$OUTPUT_DIR/deploy/tests/install-rollback-regression.sh"',
    'copy_deploy_bundle',
):
    if required not in source:
        raise SystemExit(f"build is missing candidate deploy bundle content: {required}")
PY

python3 - "$SCRIPT_DIR/rollback.sh" <<'PY'
import sys

source = open(sys.argv[1], encoding="utf-8").read()
for required in (
    'restore_snapshot_units',
    'restore_snapshot_current_link',
    'install_target_units',
    'release_health_check',
    '"$health_check" --help 2>&1 | grep -F -- \'--ca-file\'',
    '"$health_check" --ca-file "$ca"',
):
    if required not in source:
        raise SystemExit(f"rollback is missing required compatibility behavior: {required}")
if '"$CURRENT_LINK/deploy/health-check.sh" --ca-file' in source:
    raise SystemExit("rollback must not impose TLS health arguments on a target release")
gate = source.index('target release crosses local HTTP/TLS topology but has no recorded config/unit snapshot')
manual_stop = source.rindex('systemctl stop block-kiosk.service')
if gate >= manual_stop:
    raise SystemExit("cross-topology rollback must reject before stopping services")
PY

UNIT_COUNT=$(find "$SCRIPT_DIR/systemd" -maxdepth 1 -type f -name '*.service' | wc -l)
if [ "$UNIT_COUNT" -ne 2 ] || [ ! -f "$SCRIPT_DIR/systemd/block.service" ] || [ ! -f "$SCRIPT_DIR/systemd/block-kiosk.service" ]; then
  fail "exactly block.service and block-kiosk.service are required"
fi

grep -Fx 'EnvironmentFile=/etc/block/block.env' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx '  -local-https-address $BLOCK_LOCAL_HTTPS_ADDRESS \' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx '  -local-tls-cert $BLOCK_LOCAL_TLS_CERT \' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx '  -local-tls-key $BLOCK_LOCAL_TLS_KEY \' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx '  -state-db $BLOCK_STATE_DB \' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx '  -hmi-static-dir $BLOCK_HMI_STATIC_DIR \' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx '  -maintenance-https-address $BLOCK_MAINTENANCE_HTTPS_ADDRESS \' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx '  -mqtts-v2-enabled=${BLOCK_MQTTS_V2_ENABLED} \' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx '  -mqtts-v2-device-id $BLOCK_MQTTS_V2_DEVICE_ID' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx 'ReadWritePaths=/var/lib/block' "$SCRIPT_DIR/systemd/block.service" >/dev/null
grep -Fx 'After=display-manager.service graphical.target block.service' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
grep -Fx 'User=block-ui' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
grep -Fx 'Group=block-ui' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
grep -Fx 'PermissionsStartOnly=true' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
grep -Fx 'Environment=DISPLAY=:0' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
if grep -Fq 'EnvironmentFile=' "$SCRIPT_DIR/systemd/block-kiosk.service"; then
  fail "kiosk must not receive the complete Block environment file"
fi
if grep -Fx 'Environment=XAUTHORITY=/home/block-ui/.Xauthority' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null; then
  fail "kiosk must not require the missing block-ui Xauthority file"
fi
grep -Fx 'ExecStartPre=/usr/bin/env DISPLAY=:0 XAUTHORITY=/var/run/lightdm/root/:0 /usr/bin/xhost +SI:localuser:block-ui' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
grep -Fx 'ExecStartPre=/opt/block/current/deploy/health-check.sh --url https://127.0.0.1:8444/healthz --ca-file /usr/local/share/ca-certificates/block-dmp-blk-rel-001.crt --retries 30 --delay 1' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
if grep -Fq '$BLOCK_' "$SCRIPT_DIR/systemd/block-kiosk.service"; then
  fail "kiosk must not depend on a Block environment variable"
fi
grep -Fx 'ExecStart=/usr/bin/chromium-browser --kiosk --no-first-run --disable-session-crashed-bubble https://127.0.0.1:8444/' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
grep -Fx 'ExecStopPost=/usr/bin/env DISPLAY=:0 XAUTHORITY=/var/run/lightdm/root/:0 /usr/bin/xhost -SI:localuser:block-ui' "$SCRIPT_DIR/systemd/block-kiosk.service" >/dev/null
if grep -R -n -E 'network-online|block-hmi|block-plc-simulator' "$SCRIPT_DIR/systemd"; then
  fail "v2 units must not depend on network-online or legacy services"
fi
if grep -R -n -E 'local-http-address|http://127\.0\.0\.1|127\.0\.0\.1:8080|127\.0\.0\.1:8081' "$SCRIPT_DIR/systemd"; then
  fail "business units must not retain plaintext local HTTP listeners or URLs"
fi
grep -Fx 'URL=https://127.0.0.1:8444/healthz' "$SCRIPT_DIR/health-check.sh" >/dev/null
grep -Fx 'CA_FILE=/usr/local/share/ca-certificates/block-dmp-blk-rel-001.crt' "$SCRIPT_DIR/health-check.sh" >/dev/null
grep -F -- "--proto '=https'" "$SCRIPT_DIR/health-check.sh" >/dev/null
grep -F -- '--tlsv1.2' "$SCRIPT_DIR/health-check.sh" >/dev/null
grep -F -- '--cacert "$CA_FILE"' "$SCRIPT_DIR/health-check.sh" >/dev/null
if grep -n -E '(^|[[:space:]])(-k|--insecure)([[:space:]]|$)' "$SCRIPT_DIR/health-check.sh"; then
  fail "health checks must not bypass certificate validation"
fi

printf 'Block deployment static verification passed\n'
