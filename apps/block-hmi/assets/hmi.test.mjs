import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import {
  ActivationFilter,
  applyAbsoluteValues,
  buildPLCConnect,
  buildPLCDisconnect,
  buildPLCScan,
  buildPointCommand,
  buildPointsSnapshotGet,
  buildRuntimeConfigure,
  clearTransientRuntime,
  isDisplayPath,
  StartCommandReceipt
} from "./hmi.mjs";

assert.equal(isDisplayPath("home.machine.start"), true);
assert.equal(isDisplayPath("home.设备.start"), false);
assert.equal(isDisplayPath("Home.machine.start"), false);

const filter = new ActivationFilter();
assert.equal(filter.accept({ type: "click", pointerId: 1, detail: 1, timeStamp: 100 }), true);
assert.equal(filter.accept({ type: "click", pointerId: 1, detail: 1, timeStamp: 120 }), false);
assert.equal(filter.accept({ type: "click", pointerId: 1, detail: 2, timeStamp: 130 }), true);

const requestId = "315a1ea6-1cdc-47d9-96f9-b4f80ffbda7c";
const timestamp = "2026-08-05T08:00:00Z";
const configure = buildRuntimeConfigure([{
  pointId: "machine.startCommand",
  address: "D504.1",
  type: "bool",
  access: "read_write",
  readPoint: "machine.startFeedback",
  writePoint: "machine.startCommand",
  writeMethod: "maskWrite",
  write: { mode: "pulse", activeValue: true, defaultValue: false, pulseMs: 100 },
  displayPath: "home.machine.start",
  description: "启动设备"
}], requestId, timestamp);
assert.deepEqual(configure, {
  protocolVersion: "1.0",
  type: "runtime.configure",
  requestId,
  timestamp,
  scanIntervalMs: 50,
  points: [{
    pointId: "machine.startCommand",
    address: "D504.1",
    type: "bool",
    access: "read_write",
    readPoint: "machine.startFeedback",
    writePoint: "machine.startCommand",
    writeMethod: "maskWrite",
    write: { mode: "pulse", activeValue: true, defaultValue: false, pulseMs: 100 }
  }]
});
assert.equal(JSON.stringify(configure).includes("displayPath"), false);
assert.equal(JSON.stringify(configure).includes("description"), false);

assert.deepEqual(
  buildPLCScan("192.168.1.0/24", "827c4e95-57b3-414e-85c3-3ab4513e0157", "2026-08-05T08:00:04Z"),
  {
    protocolVersion: "1.0",
    type: "plc.scan",
    requestId: "827c4e95-57b3-414e-85c3-3ab4513e0157",
    timestamp: "2026-08-05T08:00:04Z",
    addressRange: "192.168.1.0/24"
  }
);
assert.deepEqual(
  buildPLCConnect("plc-192-168-1-10", "936bd1a6-934e-446b-b1b4-c5a6f5170a5c", "2026-08-05T08:00:05Z"),
  {
    protocolVersion: "1.0",
    type: "plc.connect",
    requestId: "936bd1a6-934e-446b-b1b4-c5a6f5170a5c",
    timestamp: "2026-08-05T08:00:05Z",
    deviceId: "plc-192-168-1-10"
  }
);
assert.deepEqual(
  buildPLCDisconnect("a42f4e1a-01ef-4843-a654-2ee9bc155b1e", "2026-08-05T08:00:06Z"),
  {
    protocolVersion: "1.0",
    type: "plc.disconnect",
    requestId: "a42f4e1a-01ef-4843-a654-2ee9bc155b1e",
    timestamp: "2026-08-05T08:00:06Z"
  }
);
assert.deepEqual(
  buildPointsSnapshotGet("71821c24-cee2-4c1a-a5bb-3f8b9aa1221f", "2026-08-05T08:00:03Z"),
  {
    protocolVersion: "1.0",
    type: "points.snapshot.get",
    requestId: "71821c24-cee2-4c1a-a5bb-3f8b9aa1221f",
    timestamp: "2026-08-05T08:00:03Z"
  }
);
assert.deepEqual(
  buildPointCommand("machine.startCommand", "pulse", "58fc3653-55d3-445b-a9f1-8202e08af72d", "2026-08-05T08:00:01Z"),
  {
    protocolVersion: "1.0",
    type: "point.command",
    requestId: "58fc3653-55d3-445b-a9f1-8202e08af72d",
    timestamp: "2026-08-05T08:00:01Z",
    pointId: "machine.startCommand",
    action: "pulse"
  }
);

const waitingForStart = new StartCommandReceipt(50);
const noResult = waitingForStart.waitFor("start-no-result");
let noResultSettled = false;
void noResult.then(() => { noResultSettled = true; }, () => { noResultSettled = true; });
await Promise.resolve();
assert.equal(noResultSettled, false, "a sent Start command must wait for point.result");
assert.equal(waitingForStart.receive({ type: "point.result", requestId: "unrelated-result", success: true }), false);
await Promise.resolve();
assert.equal(noResultSettled, false, "an unrelated point.result must not confirm Start");
waitingForStart.cancel("本机服务连接已关闭，启动结果未知", 503, "network_error");
await assert.rejects(noResult, (error) => error.code === "network_error");

const successfulStart = new StartCommandReceipt(50);
const startConfirmed = successfulStart.waitFor("start-success");
assert.equal(successfulStart.receive({ type: "point.result", requestId: "start-success", success: true }), true);
await startConfirmed;

const failedStart = new StartCommandReceipt(50);
const startFailed = failedStart.waitFor("start-failure");
assert.equal(failedStart.receive({
  type: "point.result",
  requestId: "start-failure",
  success: false,
  error: { code: "PLC_WRITE_FAILED", message: "PLC 写入失败" }
}), true);
await assert.rejects(startFailed, (error) => error.code === "PLC_WRITE_FAILED" && error.message === "PLC 写入失败");

const timedOutStart = new StartCommandReceipt(1);
await assert.rejects(timedOutStart.waitFor("start-timeout"), (error) =>
  error.code === "timeout" && error.message === "未收到 PLC 执行结果，结果未知"
);

const values = new Map();
applyAbsoluteValues(values, {
  "machine.startFeedback": {
    value: false,
    quality: "good",
    updatedAt: "2026-08-05T00:00:00Z"
  }
});
applyAbsoluteValues(values, {
  "machine.startFeedback": {
    value: true,
    quality: "good",
    updatedAt: "2026-08-05T00:00:01Z"
  }
});
assert.equal(values.get("machine.startFeedback").value, true);

const candidates = [{
  deviceId: "plc-192-168-1-10",
  name: "一号PLC",
  address: "192.168.1.10",
  state: "connected",
  selected: true,
  metadata: {}
}];
clearTransientRuntime(values, candidates);
assert.equal(values.size, 0);
assert.equal(candidates.length, 0);

const index = readFileSync(new URL("../index.html", import.meta.url), "utf8");
const source = readFileSync(new URL("./hmi.mts", import.meta.url), "utf8");

for (const page of ["home", "data", "maintenance", "alarm", "history"]) {
  assert.match(index, new RegExp('data-page="' + page + '"'));
  assert.match(index, new RegExp('data-nav="' + page + '"'));
}

for (const theme of ["light", "graphite", "ocean", "midnight", "titanium"]) {
  assert.match(index, new RegExp('data-theme-option="' + theme + '"'));
}

for (const identifier of [
  'id="themeMenu"',
  'id="toast"',
  'id="softKeyboardDock"',
  'id="softKeyboardHost"',
  'id="auth-panel"',
  'id="login-form"',
  'id="initial-admin-form"',
  'id="password-form"',
  'id="logout-button"',
  'id="plc-scan-button"',
  'id="snapshot-button"'
]) {
  assert.ok(index.includes(identifier), "index is missing " + identifier);
}

assert.doesNotMatch(index, /api-client\.js/);
assert.match(index, /import\("\.\/assets\/hmi\.mjs"\)/);
assert.match(source, /if \(demo\) \{\s+return demoConfiguration\(\);/);
assert.match(source, /if \(this\.demo \|\| !this\.signedIn\) \{/);
assert.match(source, /"\/api\/v2\/auth\/login"/);
assert.match(source, /"\/api\/v2\/auth\/initial-admin"/);
assert.match(source, /"\/api\/v2\/auth\/password"/);
assert.match(source, /"\/api\/v2\/auth\/logout"/);
assert.match(source, /"\/api\/v2\/config\/session"/);
assert.match(source, /new WebSocket\(websocketURL\(\)\)/);
assert.match(source, /buildRuntimeConfigure\(this\.config\.points\)/);
assert.match(source, /displayPath === "home\.machine\.start"/);
assert.match(source, /pendingStartCommand\.waitFor\(requestId\)/);
assert.match(source, /buildPointCommand\(binding\.writePoint, binding\.action, requestId\)/);
assert.match(source, /pendingStartCommand\.receive\(message\)/);
assert.match(source, /pendingStartCommand\.cancel\("本机服务连接中断，启动结果未知"/);
assert.match(source, /pendingStartCommand\.cancel\("本机服务连接已关闭，启动结果未知"/);
assert.match(source, /pendingStartCommand\.cancel\("已退出登录，启动结果未知"/);
assert.equal(existsSync(new URL("./api-client.js", import.meta.url)), false);

const loadConfigurationStart = source.indexOf("async function loadConfiguration");
const demoReturn = source.indexOf("return demoConfiguration()", loadConfigurationStart);
const configurationFetch = source.indexOf('fetch(new URL("./points.json"', loadConfigurationStart);
assert.ok(loadConfigurationStart >= 0 && demoReturn > loadConfigurationStart && configurationFetch > demoReturn);

const startDemoBranch = source.indexOf("if (this.demo) {", source.indexOf("start(): void"));
const startSocket = source.indexOf("this.openSocket()", startDemoBranch);
assert.ok(startDemoBranch >= 0 && startSocket > startDemoBranch);

for (const asset of [
  "./machine-bin.png",
  "./soft-keyboard.css",
  "./soft-keyboard.js",
  "./vendor/simple-keyboard/index.css",
  "./vendor/simple-keyboard/index.js",
  "./vendor/simple-keyboard/LICENSE"
]) {
  assert.equal(existsSync(new URL(asset, import.meta.url)), true, "missing V1 asset " + asset);
}
