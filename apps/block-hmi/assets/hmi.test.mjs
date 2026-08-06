import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { rectangleIntersectionArea } from "../tools/auth-layout-probe.mjs";
import {
  ActivationFilter,
  applyAbsoluteValues,
  authScreenForStatus,
  buildPLCConnect,
  buildPLCDisconnect,
  buildPLCScan,
  buildPointCommand,
  buildPointsSnapshotGet,
  buildRuntimeConfigure,
  clearTransientRuntime,
  demoAuthPreviewFromSearch,
  isDisplayPath,
  parseAuthStatus,
  responseCreatesSession,
  StartCommandReceipt
} from "./hmi.mjs";

assert.equal(isDisplayPath("home.machine.start"), true);
assert.equal(isDisplayPath("home.设备.start"), false);
assert.equal(isDisplayPath("Home.machine.start"), false);

assert.deepEqual(parseAuthStatus({ authenticated: false, bootstrapRequired: true }), {
  authenticated: false,
  bootstrapRequired: true
});
assert.deepEqual(parseAuthStatus({ authenticated: true, bootstrapRequired: false }), {
  authenticated: true,
  bootstrapRequired: false
});
assert.equal(parseAuthStatus({ authenticated: false, bootstrapRequired: "true" }), null);
assert.equal(parseAuthStatus({ authenticated: false }), null);
assert.equal(parseAuthStatus({ authenticated: true, bootstrapRequired: true }), null);
assert.equal(parseAuthStatus({ authenticated: false, bootstrapRequired: false, user: "admin" }), null);
assert.equal(authScreenForStatus(parseAuthStatus({ authenticated: false, bootstrapRequired: true })), "bootstrap");
assert.equal(authScreenForStatus(parseAuthStatus({ authenticated: false, bootstrapRequired: false })), "login");
assert.equal(authScreenForStatus(null), "login");
assert.equal(responseCreatesSession({ username: "admin", role: "admin", expiresAt: "2026-08-06T00:00:00Z" }), true);
assert.equal(responseCreatesSession({ username: "admin" }), false);
assert.equal(demoAuthPreviewFromSearch("?demo=1&auth=bootstrap"), "bootstrap");
assert.equal(demoAuthPreviewFromSearch("?demo=1&auth=login"), "login");
assert.equal(demoAuthPreviewFromSearch("?demo=1"), null);
assert.equal(demoAuthPreviewFromSearch("?auth=bootstrap"), null);

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
const keyboardSource = readFileSync(new URL("./soft-keyboard.js", import.meta.url), "utf8");
const keyboardStyles = readFileSync(new URL("./soft-keyboard.css", import.meta.url), "utf8");

const editableControls = [...index.matchAll(/<(input|textarea)\b[^>]*>/gi)]
  .map((match) => match[0])
  .filter((tag) => !/\b(?:hidden|disabled|readonly)\b/i.test(tag))
  .filter((tag) => !/\btype=(?:"|')(?:hidden|button|submit|reset|checkbox|radio|file|image)(?:"|')/i.test(tag));
assert.equal(editableControls.length, 12);
for (const control of editableControls) {
  assert.match(control, /\bdata-soft-keyboard=(?:"|')(?:full|numeric)(?:"|')/i);
}

function keyboardLayouts(formID) {
  const form = index.match(new RegExp('<form\\b[^>]*\\bid="' + formID + '"[^>]*>([\\s\\S]*?)<\\/form>', "i"));
  assert.ok(form, "missing form " + formID);
  return [...form[1].matchAll(/<(?:input|textarea)\b[^>]*\bdata-soft-keyboard="([^"]+)"[^>]*>/gi)]
    .map((match) => match[1]);
}

assert.deepEqual(keyboardLayouts("login-form"), ["full", "full"]);
assert.deepEqual(keyboardLayouts("initial-admin-form"), ["full", "full", "full"]);
assert.deepEqual(keyboardLayouts("password-form"), ["full", "full", "full"]);
assert.deepEqual(keyboardLayouts("session-policy-form"), ["numeric"]);
assert.deepEqual(keyboardLayouts("settingsForm"), ["numeric", "numeric", "numeric"]);
assert.match(index, /<section class="auth-overlay"[^>]*role="dialog"[^>]*aria-modal="true"/);
assert.match(index, /<section class="auth-overlay"[^>]*id="auth-panel"[^>]*hidden/);
assert.match(index, /<section id="authLogin" hidden>/);
assert.match(index, /<section id="authBootstrap" hidden>/);
assert.doesNotMatch(index, /auth-first-install/);
assert.doesNotMatch(index, /<details\b/i);
const authOverlayRule = index.match(/\.auth-overlay\s*\{([\s\S]*?)\n    \}/)?.[1] ?? "";
assert.match(authOverlayRule, /background: transparent;/);
assert.match(authOverlayRule, /background-color: transparent;/);
assert.match(authOverlayRule, /backdrop-filter: none;/);
assert.match(authOverlayRule, /-webkit-backdrop-filter: none;/);
assert.match(authOverlayRule, /box-shadow: none;/);
assert.match(authOverlayRule, /filter: none;/);
assert.match(authOverlayRule, /opacity: 1;/);
assert.match(authOverlayRule, /pointer-events: none;/);
assert.doesNotMatch(authOverlayRule, /rgba\(|blur\(/);
assert.match(index, /\.auth-sheet\s*\{[\s\S]*?pointer-events: auto;/);
assert.match(index, /\.auth-sheet\s*\{[\s\S]*?background: #f8fafb;/);
for (const region of ["hmi-topbar", "hmi-pages", "hmi-footer"]) {
  assert.match(index, new RegExp('id="' + region + '" inert aria-hidden="true"'));
}
assert.match(index, /id="softKeyboardLayer"/);
assert.match(keyboardSource, /function isKeyboardCandidate\(input\)/);
assert.match(keyboardSource, /document\.querySelectorAll\("input, textarea"\)/);
assert.match(keyboardSource, /\["hidden", "button", "submit", "reset", "checkbox", "radio", "file", "image"\]/);
assert.match(keyboardSource, /new window\.MutationObserver/);
assert.match(keyboardSource, /attributeFilter: \["disabled", "hidden", "type", "inputmode"\]/);
assert.match(keyboardSource, /activeInput\.type === "password"/);
assert.match(keyboardSource, /function syncAuthKeyboardLayout\(\) \{/);
assert.match(keyboardSource, /dock\.getBoundingClientRect\(\)\.top/);
assert.match(keyboardSource, /authPanel\.setAttribute\("data-keyboard-open", "true"\)/);
assert.match(keyboardSource, /authPanel\.removeAttribute\("data-keyboard-open"\)/);
assert.match(keyboardSource, /activeInput\.scrollIntoView\(\{ block: "nearest", inline: "nearest" \}\)/);
assert.match(keyboardStyles, /#auth-panel\[data-keyboard-open="true"\] \{[\s\S]*?align-items: flex-start;/);
assert.match(keyboardStyles, /#auth-panel\[data-keyboard-open="true"\] \.auth-sheet \{[\s\S]*?max-height: calc\(var\(--auth-keyboard-top, 50vh\) - 52px\);[\s\S]*?overflow-y: auto;/);
assert.match(keyboardStyles, /#auth-panel\[data-keyboard-open="true"\] ~ #hmi \.soft-keyboard-dock \{[\s\S]*?background: #f8fafc;[\s\S]*?backdrop-filter: none;/);
assert.match(keyboardStyles, /#auth-panel\[data-auth-mode\] ~ #hmi #softKeyboardClose \{[\s\S]*?display: none;/);
assert.equal(rectangleIntersectionArea(
  { left: 0, top: 0, right: 520, bottom: 299 },
  { left: 0, top: 351, right: 1319, bottom: 700 }
), 0);
assert.equal(rectangleIntersectionArea(
  { left: 0, top: 0, right: 520, bottom: 488 },
  { left: 0, top: 540, right: 1536, bottom: 864 }
), 0);
assert.match(keyboardSource, /function closeKeyboard\(action\) \{[\s\S]*?isOpen = false;\s*activeInput = null;/);
assert.match(keyboardSource, /if \(pinned\) return true;/);
assert.match(keyboardSource, /function setPinned\(nextPinned\) \{[\s\S]*?data-soft-keyboard-pinned/);
assert.match(keyboardSource, /if \(pinned\) requested = "soft";/);
assert.match(keyboardSource, /setPinned: setPinned/);
assert.match(keyboardSource, /function openForInput\(input\) \{[\s\S]*?activeInput = input;[\s\S]*?isOpen = true;/);
assert.match(keyboardSource, /if \(isOpen && previousInput && previousInput !== input\) \{[\s\S]*?activeInput = input;/);

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
assert.match(source, /function demoAuthPreviewMode\(\): DemoAuthPreview/);
assert.match(source, /auth === "login" \|\| auth === "bootstrap"/);
assert.match(source, /if \(this\.authPreview !== null\) \{\s*this\.showAuthentication\(this\.authPreview\);/);
assert.match(source, /private async resolveInitialAuthentication\(\): Promise<void>/);
assert.match(source, /fetch\("\/api\/v2\/auth\/status", \{[\s\S]*?credentials: "same-origin"/);
assert.match(source, /typeof value\.bootstrapRequired !== "boolean"[\s\S]*?typeof value\.authenticated !== "boolean"/);
assert.match(source, /Object\.keys\(value\)\.sort\(\)\.join\(","\) !== "authenticated,bootstrapRequired"/);
assert.match(source, /value\.bootstrapRequired === true && value\.authenticated === true/);
assert.match(source, /private prepareAuthentication\(\): void \{[\s\S]*?panel\.hidden = true;[\s\S]*?aria-busy/);
assert.match(source, /this\.loginSection\(\)\.hidden = screen !== "login";/);
assert.match(source, /this\.bootstrapSection\(\)\.hidden = screen !== "bootstrap";/);
assert.match(source, /if \(authScreenForStatus\(status\) === "bootstrap"\) \{\s*this\.showBootstrap\(\);[\s\S]*?if \(status\.authenticated\) \{/);
assert.match(source, /this\.showLogin\("本机认证服务不可用，请检查服务后重试。"\);/);
assert.match(source, /keyboard\.setMode\("soft", false\);[\s\S]*?keyboard\.setPinned\(true\);/);
assert.match(source, /keyboard\?\.setPinned\(false\);[\s\S]*?keyboard\?\.close\("keep"\);/);
assert.match(source, /if \(responseCreatesSession\(result\)\) \{[\s\S]*?this\.beginSession\(\);[\s\S]*?this\.showLogin\("管理员已创建，请使用新账号登录。"\);/);
assert.doesNotMatch(source, /auth-first-install|localStorage/);
assert.match(source, /private setHMIInteractive\(interactive: boolean\)/);
assert.match(source, /element\.toggleAttribute\("inert", !interactive\)/);
for (const boundary of ["prepareAuthentication", "showAuthentication", "showAccount", "hideAccount", "beginSession", "logout", "endSession"]) {
  const boundaryStart = source.search(new RegExp("\\bprivate\\s+(?:async\\s+)?" + boundary + "\\("));
  assert.ok(boundaryStart >= 0, "missing HMI boundary " + boundary);
  const nextBoundary = source.indexOf("\n  private ", boundaryStart + 1);
  const boundarySource = source.slice(boundaryStart, nextBoundary === -1 ? source.length : nextBoundary);
  assert.match(boundarySource, /this\.endAuthenticationKeyboard\(\)/);
}
assert.match(source, /if \(event\.code === 4401\) \{\s*this\.endSession\(/);
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
const authPreviewBranch = source.indexOf("if (this.authPreview !== null)", startDemoBranch);
const authPreviewReturn = source.indexOf("return;", authPreviewBranch);
assert.ok(authPreviewBranch > startDemoBranch && authPreviewReturn > authPreviewBranch && authPreviewReturn < startSocket);

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
