import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import vm from "node:vm";
import { assertAuthKeyboardLayout, assertAuthSubmitLayout, authKeyboardSafeGap, rectangleIntersectionArea } from "../tools/auth-layout-probe.mjs";
import { assertMaintenanceFullKeyboardLayout } from "../tools/maintenance-keyboard-layout-probe.mjs";
import {
  demoDisplayScale,
  demoFrameParameter,
  demoFrameURL,
  demoVisibleURL,
  demoViewport
} from "./demo-shell.mjs";
import {
  ActivationFilter,
  applyAbsoluteValues,
  buildPLCConnect,
  buildPLCDeviceID,
  buildPLCDisconnect,
  buildPLCScan,
  buildPointCommand,
  buildPointsSnapshotGet,
  buildRuntimeConfigure,
  clearTransientRuntime,
  defaultIdleTimeoutSeconds,
  demoAuthPreviewFromSearch,
  demoManualRoleFromSearch,
  frontendSessionIsActive,
  isDisplayPath,
  renewFrontendSession,
  PointCommandReceipt
} from "./hmi.mjs";

assert.equal(isDisplayPath("home.machine.start"), true);
assert.equal(isDisplayPath("Home.machine.start"), false);

assert.equal(defaultIdleTimeoutSeconds, 300);
assert.equal(frontendSessionIsActive({
  username: "admin", role: "ADMIN", permissions: { operate: true, maintenance: true }, expiresAt: 200
}, 199), true);
assert.equal(frontendSessionIsActive({
  username: "admin", role: "ADMIN", permissions: { operate: true, maintenance: true }, expiresAt: 200
}, 200), false);
const expiredBeforeTimeoutCallback = {
  username: "admin", role: "ADMIN", permissions: { operate: true, maintenance: true }, expiresAt: 200
};
assert.equal(renewFrontendSession(expiredBeforeTimeoutCallback, 300, 200), null);
assert.equal(renewFrontendSession(expiredBeforeTimeoutCallback, 300, 201), null);
assert.deepEqual(renewFrontendSession({
  username: "admin", role: "ADMIN", permissions: { operate: true, maintenance: true }, expiresAt: 201
}, 300, 200), {
  username: "admin", role: "ADMIN", permissions: { operate: true, maintenance: true }, expiresAt: 300200
});
assert.equal(demoAuthPreviewFromSearch("?demo=1&auth=bootstrap"), "bootstrap");
assert.equal(demoAuthPreviewFromSearch("?demo=1&auth=login"), "login");
assert.equal(demoAuthPreviewFromSearch("?demo=1"), null);
assert.equal(demoManualRoleFromSearch("?demo=1&manualRole=operator"), "OPERATOR");
assert.equal(demoManualRoleFromSearch("?demo=1&manualRole=admin"), "ADMIN");
assert.equal(demoManualRoleFromSearch("?demo=1&manualRole=guest"), "GUEST");
assert.equal(demoManualRoleFromSearch("?manualRole=admin"), "GUEST");

assert.deepEqual(demoViewport, { width: 1920, height: 1080 });
assert.equal(demoDisplayScale(1920, 1080), 1);
assert.equal(demoDisplayScale(1600, 900), 5 / 6);
assert.equal(demoDisplayScale(1280, 720), 2 / 3);
assert.equal(demoDisplayScale(3840, 2160), 1);
const bootstrapFrameURL = demoFrameURL("http://127.0.0.1:4173/demo-shell.html?demo=1&auth=bootstrap&performance=1#auth");
assert.equal(bootstrapFrameURL.pathname, "/index.html");
assert.equal(bootstrapFrameURL.searchParams.get("demo"), "1");
assert.equal(bootstrapFrameURL.searchParams.get("auth"), "bootstrap");
assert.equal(bootstrapFrameURL.searchParams.get("performance"), "1");
assert.equal(bootstrapFrameURL.searchParams.get(demoFrameParameter), "1");
assert.equal(bootstrapFrameURL.hash, "#auth");
const visibleDemoURL = demoVisibleURL("http://127.0.0.1:4173/demo-shell.html?demo=1&auth=login#auth");
assert.equal(visibleDemoURL.pathname, "/");
assert.equal(visibleDemoURL.search, "?demo=1&auth=login");
assert.equal(visibleDemoURL.hash, "#auth");

const filter = new ActivationFilter();
assert.equal(filter.accept({ type: "click", pointerId: 1, detail: 1, timeStamp: 100 }), true);
assert.equal(filter.accept({ type: "click", pointerId: 1, detail: 1, timeStamp: 120 }), false);

const timestamp = "2026-08-05T08:00:00Z";
const configured = buildRuntimeConfigure([{
  pointId: "machine.startCommand", address: "D504.1", type: "bool", access: "read_write",
  readPoint: "machine.startFeedback", writePoint: "machine.startCommand", writeMethod: "maskWrite",
  write: { mode: "pulse", activeValue: true, defaultValue: false, pulseMs: 100 },
  displayPath: "home.machine.start", description: "启动设备"
}], "configure", timestamp);
assert.equal(configured.type, "runtime.configure");
assert.equal(configured.scanIntervalMs, 500);
assert.equal(JSON.stringify(configured).includes("displayPath"), false);
assert.equal(JSON.stringify(configured).includes("description"), false);
const alarmConfigured = buildRuntimeConfigure([{
  pointId: "alarm.emergency.stop.pressed", address: "D500.0", type: "bool", access: "read",
  readPoint: "alarm.emergency.stop.pressed", writePoint: null, writeMethod: null,
  alarm: { normalValue: false, alarmValue: true, message: "急停按下", level: "danger" }
}], "alarm-configure", timestamp);
assert.deepEqual(alarmConfigured.points[0].alarm, {
  normalValue: false, alarmValue: true, message: "急停按下", level: "danger"
});
const simulatorConfigured = buildRuntimeConfigure([{
  pointId: "manual.motion.x.jog.speed.parameter", address: "D800", type: "float32", access: "read_write",
  readPoint: "manual.motion.x.jog.speed.parameter", writePoint: "manual.motion.x.jog.speed.parameter", writeMethod: "fc10",
  registerCount: 2, wordOrder: "low-high", write: { mode: "set" }
}], "numeric-configure", timestamp);
assert.deepEqual(simulatorConfigured.points[0], {
  pointId: "manual.motion.x.jog.speed.parameter", address: "D800", type: "float32", access: "read_write",
  readPoint: "manual.motion.x.jog.speed.parameter", writePoint: "manual.motion.x.jog.speed.parameter", writeMethod: "fc10",
  registerCount: 2, wordOrder: "low-high", write: { mode: "set", activeValue: undefined, defaultValue: undefined, pulseMs: undefined }
});
assert.deepEqual(buildPLCScan("192.168.1.0/24", 1502, 1, "scan", timestamp), {
  protocolVersion: "1.0",
  type: "plc.scan",
  requestId: "scan",
  timestamp,
  addressRange: "192.168.1.0/24",
  port: 1502,
  unitId: 1
});
assert.equal(buildPLCDeviceID("192.168.10.87", 1502, 1), "easy521://192.168.10.87:1502?unitId=1");
assert.equal(buildPLCConnect("easy521://127.0.0.1:1502?unitId=1", "connect", timestamp).type, "plc.connect");
assert.equal(buildPLCDisconnect("disconnect", timestamp).type, "plc.disconnect");
assert.equal(buildPointsSnapshotGet("snapshot", timestamp).type, "points.snapshot.get");
assert.equal(buildPointCommand("machine.startCommand", "pulse", "start", timestamp).type, "point.command");
assert.deepEqual(buildPointCommand("machine.enabled", "toggle", "mode", timestamp), {
  protocolVersion: "1.0",
  type: "point.command",
  requestId: "mode",
  timestamp,
  pointId: "machine.enabled",
  action: "toggle"
});
assert.deepEqual(buildPointCommand("manual.motion.x.jog.speed.parameter", "set", "speed", timestamp, 8.25), {
  protocolVersion: "1.0",
  type: "point.command",
  requestId: "speed",
  timestamp,
  pointId: "manual.motion.x.jog.speed.parameter",
  action: "set",
  value: 8.25
});

const receipt = new PointCommandReceipt(100);
const pending = receipt.waitFor("start");
assert.equal(receipt.receive({ type: "point.result", requestId: "other", success: true }), false);
assert.equal(receipt.receive({ type: "point.result", requestId: "start", success: true }), true);
await pending;
const dispatchReceipt = new PointCommandReceipt(100);
const sentPointCommands = [];
const firstDispatch = dispatchReceipt.dispatch("start", () => sentPointCommands.push("start"));
assert.throws(
  () => dispatchReceipt.dispatch("mode", () => sentPointCommands.push("mode")),
  { code: "command_pending", status: 409 }
);
assert.deepEqual(sentPointCommands, ["start"]);
assert.equal(dispatchReceipt.receive({ type: "point.result", requestId: "start", success: true }), true);
await firstDispatch;
const nextDispatch = dispatchReceipt.dispatch("mode", () => sentPointCommands.push("mode"));
assert.deepEqual(sentPointCommands, ["start", "mode"]);
assert.equal(dispatchReceipt.receive({ type: "point.result", requestId: "mode", success: true }), true);
await nextDispatch;
const failedReceipt = new PointCommandReceipt(100);
const failed = failedReceipt.waitFor("mode-failed");
assert.equal(failedReceipt.receive({
  type: "point.result",
  requestId: "mode-failed",
  success: false,
  error: { code: "plc_write_failed", message: "PLC 写入失败" }
}), true);
await assert.rejects(failed, { code: "plc_write_failed", status: 502 });
const timeoutReceipt = new PointCommandReceipt(0);
await assert.rejects(timeoutReceipt.waitFor("mode-timeout"), { code: "timeout", status: 504 });

const values = new Map();
applyAbsoluteValues(values, {
  "machine.startFeedback": { value: true, quality: "good", updatedAt: timestamp }
});
const devices = [{ deviceId: "device", name: "PLC", address: "127.0.0.1", state: "connected", selected: true, metadata: {} }];
clearTransientRuntime(values, devices);
assert.equal(values.size, 0);
assert.equal(devices.length, 0);

const productionValues = new Map();
applyAbsoluteValues(productionValues, {
  "maintenance.production.target": { value: 145, quality: "good", updatedAt: timestamp },
  "production.output.today": { value: 12345678, quality: "good", updatedAt: timestamp }
});
assert.equal(productionValues.get("maintenance.production.target").value, 145);
assert.equal(productionValues.get("production.output.today").value, 12345678);

const source = readFileSync(new URL("./hmi.mts", import.meta.url), "utf8");
const compiledSource = readFileSync(new URL("./hmi.mjs", import.meta.url), "utf8");
const index = readFileSync(new URL("../index.html", import.meta.url), "utf8");
const demoShell = readFileSync(new URL("../demo-shell.html", import.meta.url), "utf8");
const keyboardSource = readFileSync(new URL("./soft-keyboard.js", import.meta.url), "utf8");
const keyboardCSS = readFileSync(new URL("./soft-keyboard.css", import.meta.url), "utf8");

const bridgeModule = await import(`data:text/javascript;base64,${Buffer.from(`${compiledSource}\nexport { AppleBridge };`).toString("base64")}`);
const originalWindow = globalThis.window;
const originalDocument = globalThis.document;
const originalEvent = globalThis.Event;
const originalCustomEvent = globalThis.CustomEvent;
const originalHTMLInputElement = globalThis.HTMLInputElement;
const originalHTMLTextAreaElement = globalThis.HTMLTextAreaElement;
class HMIStateEvent {
  constructor(type) {
    this.type = type;
  }
}
class HMIStateCustomEvent extends HMIStateEvent {
  constructor(type, options = {}) {
    super(type);
    this.detail = options.detail;
  }
}
class HMIStateInput {
  constructor({ connected = true, hidden = false, inert = false } = {}) {
    this.connected = connected;
    this.hidden = hidden;
    this.inert = inert;
  }

  get isConnected() {
    return this.connected;
  }

  closest(selector) {
    assert.equal(selector, "[hidden], [inert]");
    return this.hidden || this.inert ? {} : null;
  }
}
const hmiStateEvents = [];
const hmiAuthPanel = { hidden: true, contains: () => false };
const hmiStatus = { textContent: "" };
globalThis.Event = HMIStateEvent;
globalThis.CustomEvent = HMIStateCustomEvent;
globalThis.HTMLInputElement = HMIStateInput;
globalThis.HTMLTextAreaElement = HMIStateInput;
globalThis.window = {
  HMISoftKeyboard: { getMode: () => "native", isOpen: () => false },
  dispatchEvent: (event) => {
    hmiStateEvents.push(event);
    return true;
  },
  setTimeout,
  clearTimeout
};
globalThis.document = {
  activeElement: null,
  querySelector: (selector) => {
    if (selector === "#auth-panel") return hmiAuthPanel;
    if (selector === "#plc-status") return hmiStatus;
    return null;
  }
};
try {
  const d500Points = Array.from({ length: 11 }, (_, bit) => ({
    pointId: `alarm.d500.${bit}`,
    address: `D500.${bit}`,
    readPoint: `alarm.d500.${bit}`,
    alarm: { normalValue: false, alarmValue: true, message: `D500.${bit}`, level: "danger" }
  }));
  const bridge = new bridgeModule.AppleBridge({ points: d500Points, bindings: [] }, false, null, "GUEST");
  const pointValue = { value: true, quality: "good", updatedAt: timestamp };
  globalThis.document.activeElement = new HMIStateInput({ hidden: true });
  bridge.handleSocketMessage(JSON.stringify({
    type: "points.snapshot",
    values: Object.fromEntries([2, 9, 10].map((bit) => [`alarm.d500.${bit}`, pointValue]))
  }));
  assert.equal(hmiStateEvents.length, 1);
  assert.deepEqual(hmiStateEvents.at(-1).detail.state.alarms.map((alarm) => alarm.id), ["D500.2", "D500.9", "D500.10"]);
  bridge.handleSocketMessage(JSON.stringify({
    type: "points.changed",
    values: Object.fromEntries([0, 1, 3, 4, 5, 6, 7, 8].map((bit) => [`alarm.d500.${bit}`, pointValue]))
  }));
  assert.equal(hmiStateEvents.length, 2, "a hidden retained input must not suppress live state dispatch");
  const latestStateEvent = hmiStateEvents.at(-1);
  assert.equal(latestStateEvent.type, "block-hmi-state");
  assert.equal(latestStateEvent.detail.forceRender, false);
  assert.equal(latestStateEvent.detail.state.revision, 2);
  assert.deepEqual(latestStateEvent.detail.state.alarms.map((alarm) => alarm.id), d500Points.map((point) => point.address));

  globalThis.document.activeElement = new HMIStateInput({ inert: true });
  assert.equal(bridge.isUserInputActive(), false, "an inert input must not defer live rendering");
  globalThis.document.activeElement = new HMIStateInput();
  bridge.handleSocketMessage(JSON.stringify({
    type: "points.changed",
    values: { "alarm.d500.0": { value: false, quality: "good", updatedAt: timestamp } }
  }));
  assert.equal(hmiStateEvents.length, 2, "a visible native input must defer live rendering while edited");
  assert.equal(bridge.deferredLiveState, true);
  assert.equal(bridge.flushDeferredLiveState(), false);
  globalThis.document.activeElement = null;
  assert.equal(bridge.flushDeferredLiveState(), true);
  assert.equal(hmiStateEvents.length, 3);
  assert.equal(hmiStateEvents.at(-1).detail.state.alarms.some((alarm) => alarm.id === "D500.0"), false);
} finally {
  if (originalWindow === undefined) delete globalThis.window;
  else globalThis.window = originalWindow;
  if (originalDocument === undefined) delete globalThis.document;
  else globalThis.document = originalDocument;
  if (originalEvent === undefined) delete globalThis.Event;
  else globalThis.Event = originalEvent;
  if (originalCustomEvent === undefined) delete globalThis.CustomEvent;
  else globalThis.CustomEvent = originalCustomEvent;
  if (originalHTMLInputElement === undefined) delete globalThis.HTMLInputElement;
  else globalThis.HTMLInputElement = originalHTMLInputElement;
  if (originalHTMLTextAreaElement === undefined) delete globalThis.HTMLTextAreaElement;
  else globalThis.HTMLTextAreaElement = originalHTMLTextAreaElement;
}

function htmlFunctionSource(name) {
  const start = index.indexOf(`function ${name}(`);
  assert.notEqual(start, -1, `missing ${name}`);
  let parenthesisDepth = 0;
  let bodyStart = -1;
  for (let position = index.indexOf("(", start); position < index.length; position += 1) {
    if (index[position] === "(") parenthesisDepth += 1;
    if (index[position] === ")" && --parenthesisDepth === 0) {
      bodyStart = index.indexOf("{", position);
      break;
    }
  }
  assert.notEqual(bodyStart, -1, `missing ${name} body`);
  let braceDepth = 0;
  for (let position = bodyStart; position < index.length; position += 1) {
    if (index[position] === "{") braceDepth += 1;
    if (index[position] === "}" && --braceDepth === 0) return index.slice(start, position + 1);
  }
  assert.fail(`unterminated ${name}`);
}

const serverRenderHarness = `
  class TestInput {
    constructor(options = {}) {
      this.connected = options.connected !== false;
      this.hidden = options.hidden === true;
      this.inert = options.inert === true;
    }
    get isConnected() { return this.connected; }
    closest(selector) {
      if (selector !== "[hidden], [inert]") throw new Error(selector);
      return this.hidden || this.inert ? {} : null;
    }
  }
  class TestTextarea extends TestInput {}
  const HTMLInputElement = TestInput;
  const HTMLTextAreaElement = TestTextarea;
  const authPanel = { hidden: true, contains: () => false };
  const document = { activeElement: null };
  const window = { HMISoftKeyboard: { getMode: () => "native", isOpen: () => false } };
  function $(selector) {
    if (selector === "#auth-panel") return authPanel;
    throw new Error(selector);
  }
  let deferredServerRender = false;
  let renderCount = 0;
  let renderedAlarms = [];
  const state = { revision: 0, bins: [], alarms: [], history: [] };
  const serverStateFields = ["revision", "alarms", "updatedAt"];
  function syncPLCTargetInput() {}
  function renderAll() { renderCount += 1; renderedAlarms = state.alarms.slice(); }
  function renderDataFreshness() {}
  ${htmlFunctionSource("applyServerState")}
  ${htmlFunctionSource("inputInteractionActive")}
  ${htmlFunctionSource("flushDeferredServerRender")}
  globalThis.hmiRenderDiagnostic = {
    document,
    hiddenInput: new TestInput({ hidden: true }),
    visibleInput: new TestInput(),
    apply: applyServerState,
    flush: flushDeferredServerRender,
    get deferred() { return deferredServerRender; },
    get renderCount() { return renderCount; },
    get renderedAlarms() { return renderedAlarms; }
  };
`;
const serverRenderContext = {};
vm.runInNewContext(serverRenderHarness, serverRenderContext, { filename: "index.html live render harness" });
const serverRender = serverRenderContext.hmiRenderDiagnostic;
const initialAlarmIDs = ["D500.2", "D500.9", "D500.10"];
const allAlarmIDs = Array.from({ length: 11 }, (_, bit) => `D500.${bit}`);
serverRender.document.activeElement = serverRender.hiddenInput;
serverRender.apply({ revision: 1, alarms: initialAlarmIDs, updatedAt: timestamp });
assert.equal(serverRender.renderCount, 1, "a hidden retained input must render an incoming snapshot");
assert.deepEqual([...serverRender.renderedAlarms], initialAlarmIDs);
serverRender.apply({ revision: 2, alarms: allAlarmIDs, updatedAt: timestamp });
assert.equal(serverRender.renderCount, 2, "a hidden retained input must render a live change");
assert.deepEqual([...serverRender.renderedAlarms], allAlarmIDs);
assert.equal(serverRender.deferred, false);
serverRender.document.activeElement = serverRender.visibleInput;
serverRender.apply({ revision: 3, alarms: allAlarmIDs.filter((alarm) => alarm !== "D500.0"), updatedAt: timestamp });
assert.equal(serverRender.renderCount, 2, "a visible input edit must defer rendering");
assert.equal(serverRender.deferred, true);
serverRender.flush();
assert.equal(serverRender.renderCount, 2, "a focused visible input must keep rendering deferred");
serverRender.document.activeElement = null;
serverRender.flush();
assert.equal(serverRender.renderCount, 3, "focus loss must flush the deferred server render");
assert.equal(serverRender.renderedAlarms.includes("D500.0"), false);

function cssRule(selector) {
  const start = keyboardCSS.indexOf(`${selector} {`);
  assert.notEqual(start, -1, `missing CSS rule: ${selector}`);
  const bodyStart = keyboardCSS.indexOf("{", start) + 1;
  const end = keyboardCSS.indexOf("\n}", bodyStart);
  assert.notEqual(end, -1, `unterminated CSS rule: ${selector}`);
  return keyboardCSS.slice(bodyStart, end);
}

const keyboardDockRule = cssRule(".soft-keyboard-dock");
assert.match(keyboardDockRule, /color: #172a3a;/);
assert.match(keyboardDockRule, /border: 1px solid #657786;/);
assert.match(keyboardDockRule, /background: #e8eef2;/);
assert.doesNotMatch(keyboardDockRule, /\b(?:opacity|transform|filter|backdrop-filter)\s*:/);

const keyboardKeyRule = cssRule(".hmi-simple-keyboard.hg-theme-default .hg-button");
assert.match(keyboardKeyRule, /color: #172a3a;/);
assert.match(keyboardKeyRule, /border: 1px solid #718493;/);
assert.match(keyboardKeyRule, /background: #ffffff;/);
assert.doesNotMatch(keyboardKeyRule, /rgba\(|var\(|\b(?:opacity|filter|backdrop-filter)\s*:/);

const functionKeyRule = cssRule(".hmi-simple-keyboard .hg-button.hg-function-key");
assert.match(functionKeyRule, /color: #172a3a;/);
assert.match(functionKeyRule, /background: #dce5eb;/);
assert.match(cssRule(".hmi-simple-keyboard .hg-button.hg-button-done"), /background: #006fbd;/);
assert.match(cssRule(".hmi-simple-keyboard .hg-button.hg-button-cancel"), /background: #fce4e2;/);

const graphiteDockRule = cssRule('html[data-theme="graphite"] .soft-keyboard-dock');
assert.match(graphiteDockRule, /color: #fff;/);
assert.match(graphiteDockRule, /border-color: #c3cbd3;/);
assert.match(graphiteDockRule, /background: #29323a;/);
assert.doesNotMatch(graphiteDockRule, /rgba\(|var\(|\b(?:opacity|transform|filter|backdrop-filter)\s*:/);
assert.match(
  cssRule('html[data-theme="graphite"] #auth-panel[data-keyboard-open="true"] ~ #hmi .soft-keyboard-dock'),
  /background: #29323a;/
);

const graphiteKeyRule = cssRule('html[data-theme="graphite"] .hmi-simple-keyboard.hg-theme-default .hg-button');
assert.match(graphiteKeyRule, /color: #fff;/);
assert.match(graphiteKeyRule, /border-color: #d7dde3;/);
assert.match(graphiteKeyRule, /background: #3a4650;/);
assert.doesNotMatch(graphiteKeyRule, /rgba\(|var\(|\b(?:opacity|filter|backdrop-filter)\s*:/);

const defaultPointsConfiguration = JSON.parse(readFileSync(new URL("./points.json", import.meta.url), "utf8"));
const simulatorPointsConfiguration = JSON.parse(readFileSync(new URL("./points.simulatorFloat32.json", import.meta.url), "utf8"));
assert.equal(defaultPointsConfiguration.scanIntervalMs, 500);
assert.equal(simulatorPointsConfiguration.scanIntervalMs, 500);
assert.deepEqual(defaultPointsConfiguration.points.map((point) => point.address), [
  "D504.0", "D504.1", "D504.7", "D504.8", "D504.9", "D504.10", "D550.3", "D550.4",
  "D522", "D504.6", "D506.0",
  "D500.0", "D500.1", "D500.2", "D500.3", "D500.4", "D500.5", "D500.6", "D500.7", "D500.8", "D500.9", "D500.10",
  "D502.0", "D502.1", "D502.11", "D502.12",
  "D504.12", "D506.5", "D504.11", "D506.7", "D506.9", "D506.10", "D506.11", "D506.12",
  "D1000", "D900", "D902", "D904", "D906", "D908", "D910", "D912"
]);
assert.ok(defaultPointsConfiguration.points.slice(0, 8).every((point) => point.type === "bool" && point.writeMethod === "maskWrite"));
assert.doesNotMatch(JSON.stringify(defaultPointsConfiguration), /D800|D812/);
const configuredAlarms = defaultPointsConfiguration.points.filter((point) => point.alarm);
assert.equal(configuredAlarms.length, 15);
assert.deepEqual(configuredAlarms.map((point) => ({
  address: point.address, type: point.type, access: point.access, readPoint: point.readPoint,
  writePoint: point.writePoint, writeMethod: point.writeMethod, normalValue: point.alarm.normalValue,
  alarmValue: point.alarm.alarmValue, level: point.alarm.level, message: point.alarm.message
})), [
  { address: "D500.0", type: "bool", access: "read", readPoint: "alarm.emergency.stop.pressed", writePoint: null, writeMethod: null, normalValue: false, alarmValue: true, level: "danger", message: "急停按下" },
  { address: "D500.1", type: "bool", access: "read", readPoint: "alarm.axis.fault", writePoint: null, writeMethod: null, normalValue: false, alarmValue: true, level: "danger", message: "轴故障" },
  { address: "D500.2", type: "bool", access: "read", readPoint: "alarm.light.curtain.triggered", writePoint: null, writeMethod: null, normalValue: false, alarmValue: true, level: "danger", message: "光幕触发" },
  { address: "D500.3", type: "bool", access: "read", readPoint: "alarm.restart.inhibited", writePoint: null, writeMethod: null, normalValue: false, alarmValue: true, level: "danger", message: "不可再次启动" },
  { address: "D500.4", type: "bool", access: "read", readPoint: "alarm.axis.z.upper.limit", writePoint: null, writeMethod: null, normalValue: false, alarmValue: true, level: "danger", message: "Z轴上限位" },
  { address: "D500.5", type: "bool", access: "read", readPoint: "alarm.axis.z.lower.limit", writePoint: null, writeMethod: null, normalValue: false, alarmValue: true, level: "danger", message: "Z轴下限位" },
  { address: "D500.6", type: "bool", access: "read", readPoint: "alarm.axis.z.left.limit", writePoint: null, writeMethod: null, normalValue: false, alarmValue: true, level: "danger", message: "Z轴左限位" },
  { address: "D500.7", type: "bool", access: "read", readPoint: "alarm.axis.z.right.limit", writePoint: null, writeMethod: null, normalValue: false, alarmValue: true, level: "danger", message: "Z轴右限位" },
  { address: "D500.8", type: "bool", access: "read", readPoint: "alarm.material.clear.fault", writePoint: null, writeMethod: null, normalValue: false, alarmValue: true, level: "danger", message: "清料故障" },
  { address: "D500.9", type: "bool", access: "read", readPoint: "alarm.material.conflict", writePoint: null, writeMethod: null, normalValue: false, alarmValue: true, level: "danger", message: "有料冲突" },
  { address: "D500.10", type: "bool", access: "read", readPoint: "alarm.self.check.failed", writePoint: null, writeMethod: null, normalValue: false, alarmValue: true, level: "danger", message: "自检未通过" },
  { address: "D502.0", type: "bool", access: "read", readPoint: "alarm.bin.a.empty", writePoint: null, writeMethod: null, normalValue: false, alarmValue: true, level: "warning", message: "A仓位无车" },
  { address: "D502.1", type: "bool", access: "read", readPoint: "alarm.bin.b.empty", writePoint: null, writeMethod: null, normalValue: false, alarmValue: true, level: "warning", message: "B仓位无车" },
  { address: "D502.11", type: "bool", access: "read", readPoint: "alarm.cylinder.1.timeout", writePoint: null, writeMethod: null, normalValue: false, alarmValue: true, level: "warning", message: "气缸1超时" },
  { address: "D502.12", type: "bool", access: "read", readPoint: "alarm.cylinder.2.timeout", writePoint: null, writeMethod: null, normalValue: false, alarmValue: true, level: "warning", message: "气缸2超时" }
]);
assert.doesNotMatch(JSON.stringify(configuredAlarms), /D502\.(?:2|3|4|5)"/);
assert.deepEqual(defaultPointsConfiguration.points.find((point) => point.pointId === "home.speed.automatic"), {
  pointId: "home.speed.automatic", address: "D522", type: "float32", access: "read",
  readPoint: "home.speed.automatic", writePoint: null, writeMethod: null,
  registerCount: 2, wordOrder: "low-high"
});
assert.deepEqual(defaultPointsConfiguration.bindings.find((binding) => binding.displayPath === "home.speed.automatic"), {
  displayPath: "home.speed.automatic", description: "自动运行速度", component: "number",
  readPoint: "home.speed.automatic", writePoint: null, action: "set",
  permission: "operate", state: "configured", sourceRow: 3, sourceAddress: "D522"
});
assert.deepEqual(defaultPointsConfiguration.points.find((point) => point.pointId === "maintenance.production.target"), {
  pointId: "maintenance.production.target", address: "D1000", type: "int32", access: "read_write",
  readPoint: "maintenance.production.target", writePoint: "maintenance.production.target", writeMethod: "fc10",
  registerCount: 2, wordOrder: "low-high", write: { mode: "set" }
});
assert.deepEqual(defaultPointsConfiguration.bindings.find((binding) => binding.displayPath === "maintenance.production.target"), {
  displayPath: "maintenance.production.target", description: "今日目标产能", component: "number",
  readPoint: "maintenance.production.target", writePoint: "maintenance.production.target", action: "set",
  permission: "maintenance", state: "configured", sourceRow: 36, sourceAddress: "D1000"
});
assert.deepEqual(defaultPointsConfiguration.points.find((point) => point.pointId === "production.cycle.single"), {
  pointId: "production.cycle.single", address: "D900", type: "int16", access: "read",
  readPoint: "production.cycle.single", writePoint: null, writeMethod: null, registerCount: 1
});
assert.deepEqual(defaultPointsConfiguration.bindings.find((binding) => binding.displayPath === "production.cycle.single"), {
  displayPath: "production.cycle.single", description: "单件节拍", component: "value",
  readPoint: "production.cycle.single", state: "configured", sourceRow: 37, sourceAddress: "D900"
});
for (const pointId of ["home.homing", "home.action.reset", "home.action.restart", "home.action.pause", "home.action.clear", "home.cycle.single", "home.cycle.frame"]) {
  const point = defaultPointsConfiguration.points.find((item) => item.pointId === pointId);
  assert.deepEqual(point.write, { mode: "pulse", activeValue: true, defaultValue: false, pulseMs: 100 });
  assert.equal(point.writeMethod, "maskWrite");
}
assert.deepEqual(defaultPointsConfiguration.points.find((point) => point.pointId === "home.cycle.single.feedback"), {
  pointId: "home.cycle.single.feedback", address: "D506.11", type: "bool", access: "read",
  readPoint: "home.cycle.single.feedback", writePoint: null, writeMethod: null
});
assert.deepEqual(defaultPointsConfiguration.points.find((point) => point.pointId === "home.cycle.frame.feedback"), {
  pointId: "home.cycle.frame.feedback", address: "D506.12", type: "bool", access: "read",
  readPoint: "home.cycle.frame.feedback", writePoint: null, writeMethod: null
});
for (const [displayPath, readPoint, writePoint] of [
  ["home.cycle.single", "home.cycle.single.feedback", "home.cycle.single"],
  ["home.cycle.frame", "home.cycle.frame.feedback", "home.cycle.frame"]
]) {
  const binding = defaultPointsConfiguration.bindings.find((item) => item.displayPath === displayPath);
  assert.deepEqual({ readPoint: binding.readPoint, writePoint: binding.writePoint, action: binding.action, permission: binding.permission, state: binding.state }, {
    readPoint, writePoint, action: "pulse", permission: "operate", state: "configured"
  });
}
for (const pointId of ["production.output.today", "production.quality.passed"]) {
  const point = defaultPointsConfiguration.points.find((item) => item.pointId === pointId);
  assert.deepEqual({ type: point.type, registerCount: point.registerCount, wordOrder: point.wordOrder }, {
    type: "int32", registerCount: 2, wordOrder: "low-high"
  });
}
for (const displayPath of [
  "manual.motion.x.absolute.target.parameter", "manual.motion.z.absolute.target.parameter",
  "manual.motion.x.relative.distance.parameter", "manual.motion.z.relative.distance.parameter"
]) {
  const binding = defaultPointsConfiguration.bindings.find((item) => item.displayPath === displayPath);
  assert.deepEqual({ readPoint: binding.readPoint, writePoint: binding.writePoint, state: binding.state }, {
    readPoint: null, writePoint: null, state: "pending"
  });
}
assert.match(source, /fetch\(new URL\("\.\/points\.json", import\.meta\.url\)/);
const pointsConfiguration = simulatorPointsConfiguration;
assert.deepEqual(pointsConfiguration.numericProfiles.simulatorFloat32, {
  scope: "电脑模拟联调",
  dataType: "float32",
  registerCount: 2,
  singleRegisterByteOrder: "big-endian",
  wordOrder: "low-high",
  readFunction: "FC03",
  writeFunction: "FC10",
  verification: "真实 Easy521 投用前，必须用已知浮点基准值核验寄存器跨度和字序；本约定不是现场事实。"
});
for (const configuration of [defaultPointsConfiguration, simulatorPointsConfiguration]) {
  for (const binding of configuration.bindings) {
    assert.match(binding.displayPath, /^[a-z][a-z0-9]*(?:\.[a-z][a-z0-9]*)+$/);
    assert.match(binding.description, /[\u3400-\u9fff]/);
  }
}
const xSpeed = pointsConfiguration.points.find((point) => point.pointId === "manual.motion.x.jog.speed.parameter");
assert.deepEqual(xSpeed, {
  pointId: "manual.motion.x.jog.speed.parameter", address: "D800", type: "float32", access: "read_write",
  readPoint: "manual.motion.x.jog.speed.parameter", writePoint: "manual.motion.x.jog.speed.parameter", writeMethod: "fc10",
  registerCount: 2, wordOrder: "low-high", write: { mode: "set" }
});
const xRelative = pointsConfiguration.points.find((point) => point.pointId === "manual.motion.x.relative.trigger.action");
assert.equal(xRelative.readPoint, null);
assert.equal(xRelative.writeMethod, "maskWrite");
const simulatorPointAddresses = [
  "D504.0", "D504.1", "D504.7", "D504.8", "D504.9", "D504.10", "D550.3", "D550.4",
  "D800", "D806", "D812", "D814", "D816", "D818", "D820", "D822", "D824", "D826",
  "D828", "D830", "D832", "D834", "D836", "D840", "D842", "D844", "D846", "D848", "D850", "D852"
];
assert.equal(pointsConfiguration.points.length, simulatorPointAddresses.length);
assert.deepEqual(pointsConfiguration.points.map((point) => point.address), simulatorPointAddresses);
for (const address of ["D812", "D814", "D816", "D818", "D820", "D822", "D824", "D826", "D828", "D830", "D832", "D834", "D836", "D840", "D842", "D844"]) {
  const point = pointsConfiguration.points.find((item) => item.address === address);
  assert.deepEqual({ type: point.type, access: point.access, writeMethod: point.writeMethod, registerCount: point.registerCount, wordOrder: point.wordOrder, write: point.write }, {
    type: "float32", access: "read_write", writeMethod: "fc10", registerCount: 2, wordOrder: "low-high", write: { mode: "set" }
  });
}
for (const displayPath of [
  "manual.motion.x.absolute.target.parameter", "manual.motion.x.absolute.speed.parameter", "manual.motion.x.absolute.acceleration.parameter", "manual.motion.x.absolute.deceleration.parameter",
  "manual.motion.z.absolute.target.parameter", "manual.motion.z.absolute.speed.parameter", "manual.motion.z.absolute.acceleration.parameter", "manual.motion.z.absolute.deceleration.parameter",
  "manual.motion.x.relative.distance.parameter", "manual.motion.x.relative.speed.parameter", "manual.motion.x.relative.acceleration.parameter", "manual.motion.x.relative.deceleration.parameter",
  "manual.motion.z.relative.distance.parameter", "manual.motion.z.relative.speed.parameter", "manual.motion.z.relative.acceleration.parameter", "manual.motion.z.relative.deceleration.parameter"
]) {
  const binding = pointsConfiguration.bindings.find((item) => item.displayPath === displayPath);
  assert.deepEqual({ component: binding.component, readPoint: binding.readPoint, writePoint: binding.writePoint, action: binding.action, permission: binding.permission, state: binding.state }, {
    component: "number", readPoint: displayPath, writePoint: displayPath, action: "set", permission: "maintenance", state: "configured"
  });
}
for (const displayPath of ["manual.motion.x.absolute.trigger.action", "manual.motion.z.absolute.trigger.action"]) {
  const binding = pointsConfiguration.bindings.find((item) => item.displayPath === displayPath);
  assert.deepEqual({ writePoint: binding.writePoint, state: binding.state }, { writePoint: null, state: "pending" });
}
const userVisibleCopy = (source + index).replace(/\/api(?:\/[a-z-]+)+/gi, "");
assert.doesNotMatch(userVisibleCopy, /\bv2\b/i);
assert.doesNotMatch(source, /localStorage|sessionStorage|crypto\.subtle|passwordDigest|block-hmi-local-/);
assert.match(source, /this\.prepareGuestHMI\(\);[\s\S]*?await this\.loadAuthenticationState\(\);[\s\S]*?this\.openSocket\(\);/);
assert.match(source, /authRequest\("\/api\/auth\/initial-admin", "GET"\)/);
assert.match(source, /authRequest\("\/api\/config\/session", "GET"\)/);
assert.match(source, /authRequest\("\/api\/auth\/login", "POST"/);
assert.match(source, /authRequest\("\/api\/auth\/password", "POST"/);
assert.match(source, /authRequest\("\/api\/config\/session", "PUT"/);
assert.match(source, /private moveLocalAdministrationToMaintenance\(\): void/);
assert.match(source, /private bindPasswordVisibilityToggles\(\): void/);
assert.match(source, /toggle\.addEventListener\("pointerdown", \(event\) => event\.preventDefault\(\)\)/);
assert.match(source, /input\.type = input\.type === "password" \? "text" : "password";/);
const loginAttempt = source.match(/private async login\(username: string, password: string\): Promise<void> \{[\s\S]*?\n  \}\n\n  private finishLoginAttempt/);
assert.notEqual(loginAttempt, null);
assert.match(loginAttempt[0], /if \(this\.loginInFlight\) \{\s*return;\s*\}[\s\S]*?this\.loginInFlight = true;[\s\S]*?this\.setAuthSubmitBusy\(form, true\);[\s\S]*?finally \{[\s\S]*?this\.loginInFlight = false;[\s\S]*?this\.setAuthSubmitBusy\(form, false\);/);
const bootstrapAttempt = source.match(/private async createInitialAdmin\(username: string, password: string, confirmPassword: string\): Promise<void> \{[\s\S]*?\n  \}\n\n  private async changePassword/);
assert.notEqual(bootstrapAttempt, null);
assert.match(bootstrapAttempt[0], /if \(this\.initialAdminInFlight\) \{\s*return;\s*\}[\s\S]*?this\.initialAdminInFlight = true;[\s\S]*?this\.setAuthSubmitBusy\(form, true\);/);
assert.match(bootstrapAttempt[0], /this\.beginSession\(identity\);\s*this\.emitPageNotice\("管理员创建成功"\);/);
assert.doesNotMatch(bootstrapAttempt[0], /authRequest\("\/api\/auth\/login"/);
const authKeyboardOpen = source.match(/private openAuthWithKeyboard\(screen: AuthScreen, message = ""\): void \{[\s\S]*?\n  \}\n\n  private showLogin/);
assert.notEqual(authKeyboardOpen, null);
assert.match(authKeyboardOpen[0], /const keyboard = window\.HMISoftKeyboard;[\s\S]*?keyboard\?\.init\(\);/);
assert.match(authKeyboardOpen[0], /panel\.hidden = false;[\s\S]*?panel\.setAttribute\("data-auth-mode", screen\);/);
assert.match(authKeyboardOpen[0], /screen === "bootstrap" \? "#initial-admin-form" : "#login-form"[\s\S]*?querySelector<HTMLInputElement>\("\[data-soft-keyboard\]"\)/);
assert.match(authKeyboardOpen[0], /keyboard\.setPinned\(true\);[\s\S]*?keyboard\.setMode\("soft", false\);[\s\S]*?keyboard\.open\(input, \{ immediate: true \}\);[\s\S]*?input\.focus\(\{ preventScroll: true \}\);/);
assert.doesNotMatch(authKeyboardOpen[0], /requestAnimationFrame|setTimeout/);
assert.doesNotMatch(source, /private openAuthenticationKeyboard\(/);
assert.doesNotMatch(source, /private openFocusedAuthenticationKeyboard\(/);
const accountAuthToggle = source.match(/private toggleAccountSession\(\): void \{[\s\S]*?\n  \}\n\n  private hasPermission/);
assert.notEqual(accountAuthToggle, null);
assert.match(accountAuthToggle[0], /this\.openAuthWithKeyboard\(this\.authenticationScreen\(\)\)/);
const protectedAuthGate = source.match(/private requirePermission\(permission: "operate" \| "maintenance"\): boolean \{[\s\S]*?\n  \}\n\n  private updateAccountControl/);
assert.notEqual(protectedAuthGate, null);
assert.match(protectedAuthGate[0], /this\.openAuthWithKeyboard\(this\.authenticationScreen\(\)\)/);
assert.match(source, /private requirePermission\(permission: "operate" \| "maintenance"\): boolean/);
assert.match(source, /private setAuthNotice\(message: string\): void \{[\s\S]*?authenticationVisible[\s\S]*?this\.emitPageNotice\(message, "danger"\)/);
const becomeGuest = source.match(/private becomeGuest\(\): void \{[\s\S]*?\n  \}\n\n  private openSocket/);
assert.notEqual(becomeGuest, null);
assert.match(becomeGuest[0], /new Event\("block-hmi-guest"\)/);
assert.doesNotMatch(becomeGuest[0], /socket\.close\(/);
const publicNavigationCancellation = source.match(/window\.addEventListener\("block-hmi-public-navigation", \(\) => \{[\s\S]*?\n    \}\);/);
assert.notEqual(publicNavigationCancellation, null);
assert.match(publicNavigationCancellation[0], /if \(!this\.authPanel\(\)\.hidden\) \{\s*this\.becomeGuest\(\);\s*\}/);
assert.doesNotMatch(publicNavigationCancellation[0], /else/);
assert.match(source, /private sendPLCScan\(\): void \{[\s\S]*?requirePermission\("maintenance"\)/);
assert.match(source, /private sendCommand\([\s\S]*?requirePermission\("operate"\)/);
const legacyPointCommand = source.match(/private sendCommand\(command: string, payload: Record<string, unknown> = \{\}\): Promise<\{ state: LegacyState \}> \{[\s\S]*?\n  \}\n\n  private applyDemoCommand/);
assert.notEqual(legacyPointCommand, null);
assert.doesNotMatch(source, /command === "set_mode"|home\.machine\.enabled|acknowledgeAlarm/);
assert.match(legacyPointCommand[0], /command === "start"[\s\S]*?displayPath: "home\.machine\.start", action: "pulse"[\s\S]*?buildPointCommand\(pointID, operation\.action, requestId\)/);
assert.match(legacyPointCommand[0], /pendingPointCommand\.dispatch\(requestId, \(\) => \{[\s\S]*?this\.socket!\.send/);
assert.match(source, /const manual = this\.valueFor\("footer\.mode\.manual"\);[\s\S]*?state\.running = null;[\s\S]*?state\.mode = manual === true \? "manual" : "auto";/);
assert.match(source, /state\.cycle = this\.numberFor\("production\.cycle\.single"\);/);
const activeAlarms = source.match(/private activeAlarms\(\): LegacyAlarm\[\] \{[\s\S]*?\n  \}/);
assert.notEqual(activeAlarms, null);
assert.match(activeAlarms[0], /this\.config\.points\.flatMap[\s\S]*?value\?\.value !== true[\s\S]*?id: point\.address, level: alarm\.level, text: alarm\.message/);
assert.doesNotMatch(activeAlarms[0], /quality|alarmActive/);
assert.match(source, /const singlePaused = this\.valueFor\("home\.cycle\.single"\);[\s\S]*?state\.singlePaused = typeof singlePaused === "boolean" \? singlePaused : null;/);
assert.match(source, /const framePaused = this\.valueFor\("home\.cycle\.frame"\);[\s\S]*?state\.framePaused = typeof framePaused === "boolean" \? framePaused : null;/);
assert.match(source, /private numberFor\(displayPath: string\): number \| null \{[\s\S]*?return typeof value === "number" && Number\.isFinite\(value\) \? value : null;/);
assert.match(source, /const point = this\.values\.get\(binding\.readPoint\);[\s\S]*?return point\?\.quality === "good" \? point\.value : undefined;/);
assert.match(source, /clearTransientRuntime\(this\.values, this\.plcDevices\);\s*this\.publishLiveState\(true\);\s*this\.plcState = "disconnected";/);
assert.match(compiledSource, /clearTransientRuntime\(this\.values, this\.plcDevices\);\s*this\.publishLiveState\(true\);\s*this\.plcState = "disconnected";/);
const productionPolicy = source.match(/private deferProductionPolicy\(\): void \{[\s\S]*?\n  \}\n\}/);
assert.notEqual(productionPolicy, null);
assert.match(productionPolicy[0], /document\.querySelectorAll<HTMLButtonElement>\("\.control-button"\)/);
assert.match(productionPolicy[0], /const displayPath = button\.dataset\.pointAction;/);
assert.match(productionPolicy[0], /const configured = displayPath !== undefined && this\.config\.bindings\.some/);
assert.match(productionPolicy[0], /const available = runtimeEnabled && configured;/);
assert.doesNotMatch(productionPolicy[0], /mode\.dataset\.backendUnavailable|ack-button/);
assert.doesNotMatch(source, /\/api\/auth\/status/);
assert.doesNotMatch(source, /\/api\/auth\/(activity|logout)/);
assert.match(source, /private refreshFrontendSession\(\): boolean \{[\s\S]*?renewFrontendSession\(this\.session, this\.idleTimeoutSeconds\)[\s\S]*?this\.becomeGuest\(\)/);
assert.match(source, /const report = \(\) => \{[\s\S]*?if \(!this\.refreshFrontendSession\(\)\) \{[\s\S]*?return;/);
assert.match(source, /document\.addEventListener\("pointerdown", report/);
assert.doesNotMatch(source, /document\.addEventListener\("touchstart", report/);
assert.match(source, /document\.addEventListener\("keydown", report\)/);
assert.doesNotMatch(source, /event\.code === 4401/);
assert.match(source, /new WebSocket\(websocketURL\(\)\)/);
assert.match(source, /function websocketURL\(\): string \{[\s\S]*?window\.location\.protocol !== "https:"[\s\S]*?return "wss:\/\/" \+ window\.location\.host \+ "\/ws";/);
assert.doesNotMatch(source, /"ws:"/);
assert.match(source, /buildRuntimeConfigure\(this\.config\.points\)/);
assert.match(index, /window\.BlockHMIReady\.then\(syncFrontendPermissions\)/);
assert.match(index, /class="data-point data-target-point"[\s\S]*?<output id="dataTarget">—<\/output>/);
assert.doesNotMatch(index, /id="dataTarget(?:Form|Input|Save)"|function saveDataTarget|renderDataTargetControl/);
const maintenanceProductionSave = index.match(/async function saveProductionSettings\(manual = false\) \{[\s\S]*?\n      \}\n\n      async function savePiecesPerBox/);
assert.notEqual(maintenanceProductionSave, null);
assert.match(maintenanceProductionSave[0], /requireFrontendPermission\("maintenance"\)/);
assert.match(maintenanceProductionSave[0], /runtime\.command\("maintenance\.production\.target", patch\.targetProduction\)/);
const initialPageState = index.match(/const state = \{[\s\S]*?\n      \};/);
assert.notEqual(initialPageState, null);
assert.match(initialPageState[0], /running: null,[\s\S]*?mode: "auto",[\s\S]*?target: null,[\s\S]*?output: null,[\s\S]*?cycle: null/);
const statusRenderer = index.match(/function renderStatus\(\) \{[\s\S]*?\n      \}\n\n      function renderBins/);
assert.notEqual(statusRenderer, null);
assert.match(statusRenderer[0], /running === true \? "运行中" : running === false \? "运行停止" : "—"/);
assert.doesNotMatch(statusRenderer[0], /modeToggle|modeCn|const mode = state\.mode/);
assert.match(statusRenderer[0], /known \? item\.paused \? "恢复" : "暂停" : "—"/);
const metricsRenderer = index.match(/function renderMetrics\(\) \{[\s\S]*?\n      \}\n\n      function renderEvents/);
assert.notEqual(metricsRenderer, null);
assert.match(index, /function renderNumericReadout\(selector, value\) \{[\s\S]*?output\.textContent = known \? String\(value\) : "—";/);
assert.match(index, /function renderCycleReadout\(selector, value\) \{[\s\S]*?const known = hasNumericValue\(value\);[\s\S]*?output\.textContent = known \? \(value \/ 1000\)\.toFixed\(1\) : "—";[\s\S]*?unit\.hidden = !known;/);
assert.match(metricsRenderer[0], /renderCycleReadout\("#homeCycle", state\.cycle\);[\s\S]*?renderCycleReadout\("#dataCycle", state\.cycle\);/);
assert.match(index, /id="homeCycle">—<\/output>\s*<span hidden>秒<\/span>/);
assert.match(index, /id="dataCycle">—<\/output><span hidden>秒<\/span>/);
assert.doesNotMatch(index, /秒（S）/);
assert.match(metricsRenderer[0], /"目标完成度 —"/);
assert.match(metricsRenderer[0], /progressFill\.hidden = true;/);
assert.doesNotMatch(metricsRenderer[0], /: 100|Math\.max\(1, state\.target\)/);
const automaticSpeedRenderer = index.match(/function renderAutomaticSpeed\(\) \{[\s\S]*?\n      \}\n\n      function manualBinding/);
assert.notEqual(automaticSpeedRenderer, null);
assert.match(automaticSpeedRenderer[0], /const known = hasNumericValue\(incoming\);[\s\S]*?slider\.hidden = false;[\s\S]*?slider\.disabled = true;[\s\S]*?slider\.setAttribute\("aria-disabled", "true"\);/);
assert.match(automaticSpeedRenderer[0], /slider\.value = "0";[\s\S]*?\$\("#automaticSpeedHint"\)\.textContent = "—";/);
assert.doesNotMatch(automaticSpeedRenderer[0], /: 100/);
assert.doesNotMatch(index, /runtime\.command\("home\.speed\.automatic"|updateAutomaticSpeedDraft|commitAutomaticSpeed/);
assert.match(index, /query\.get\("demo"\) !== "1" \|\| query\.get\("__demoFrame"\) === "1"/);
assert.match(index, /new URL\("demo-shell\.html", window\.location\.href\)/);
assert.match(demoShell, /<iframe[\s\S]*?id="demoFrame"[\s\S]*?width="1920"[\s\S]*?height="1080"/);
assert.match(demoShell, /import \{ demoDisplayScale, demoFrameURL, demoVisibleURL \} from "\.\/assets\/demo-shell\.mjs"/);
assert.match(demoShell, /frame\.src = demoFrameURL\(window\.location\.href\)\.href/);
assert.match(demoShell, /window\.history\.replaceState\(null, "", demoVisibleURL\(window\.location\.href\)\.href\)/);
assert.match(demoShell, /id="demoInputBridge"/);
assert.match(demoShell, /frame\.contentWindow\?\.postMessage\(\{ type: "block-hmi-demo-input", \.\.\.point \}, window\.location\.origin\)/);
assert.match(index, /get\("__demoFrame"\) !== "1"/);
assert.match(index, /event\.source !== window\.parent/);
assert.match(index, /document\.elementFromPoint\(x, y\)/);
assert.match(index, /target\.matches\("\.hg-button"\)/);
assert.match(index, /\.auth-sheet \{[\s\S]*?max-height: min\(800px, calc\(100vh - 48px\)\);[\s\S]*?padding: 60px 32px;[\s\S]*?width: min\(500px, 100%\);/);
assert.match(index, /<div class="auth-sheet">[\s\S]*?id="authLogin"[\s\S]*?id="authBootstrap"/);
assert.match(index, /#auth-panel \.auth-form \{[\s\S]*?gap: 22px;/);
assert.match(index, /id="login-form"[\s\S]*?<div class="auth-field auth-actions">[\s\S]*?<button type="submit">/);
assert.match(index, /id="initial-admin-form"[\s\S]*?<div class="auth-field auth-actions">[\s\S]*?<button type="submit">/);
assert.match(index, /#authLogin \.auth-form \.auth-actions,[\s\S]*?#authBootstrap \.auth-form \.auth-actions \{[\s\S]*?justify-self: stretch;[\s\S]*?width: 100%;/);
assert.match(index, /#authLogin \.auth-form \.auth-actions > button\[type="submit"\],[\s\S]*?#authBootstrap \.auth-form \.auth-actions > button\[type="submit"\] \{[\s\S]*?grid-column: 1 \/ -1;[\s\S]*?justify-self: stretch;[\s\S]*?width: 100%;/);
assert.match(index, /id="operatorName">登录<\/div>/);
assert.doesNotMatch(index, /<span class="nav-en">/);
assert.doesNotMatch(index, /<div class="meta-en">Operator<\/div>/);
assert.doesNotMatch(index, /id="modeEn"|id="modeState"|远程联机/);
assert.match(index, /\.bin-row \{[\s\S]*?grid-template-columns: repeat\(2, minmax\(0, 1fr\)\);[\s\S]*?align-items: stretch;/);
assert.deepEqual([...index.matchAll(/<div class="bin [^"]+" data-bin="(\d+)">/g)].map((match) => match[1]), ["0", "1"]);
const renderBins = index.match(/function renderBins\(\) \{[\s\S]*?\n      \}/);
assert.notEqual(renderBins, null);
assert.match(renderBins[0], /state\.bins\[index\]/);
assert.match(index, /#hmi-footer \{[\s\S]*?--footer-control-height: 102px;[\s\S]*?grid-template-columns: 220px minmax\(0, 1fr\) 220px;/);
assert.match(index, /#hmi-footer \.operator \{[\s\S]*?height: var\(--footer-control-height\);/);
assert.match(index, /#hmi-footer #operatorName \{[\s\S]*?width: 168px;[\s\S]*?border-radius: 18px;/);
assert.match(index, /#hmi-footer #operatorName::before \{[\s\S]*?data:image\/svg\+xml/);
assert.doesNotMatch(index, /id="(?:modeToggle|modeCn)"|当前为(?:自动|手动)模式/);
for (const asset of [
  'assets/soft-keyboard.css?v=20260809.1',
  'assets/soft-keyboard.js?v=20260810.1',
  './assets/hmi.mjs?v=20260811.1'
]) {
  assert.ok(index.includes(asset), `cache version is missing from ${asset}`);
}
assert.doesNotMatch(index, /\.\/assets\/hmi\.mjs\?v=20260808\.4/);
assert.doesNotMatch(index, /\.\/assets\/hmi\.mjs\?v=20260810\.10/);
assert.match(index, /function requireFrontendPermission\(permission\)/);
assert.match(index, /name === "maintenance" && !requireFrontendPermission\("maintenance"\)/);
assert.match(index, /\.page\[data-page="maintenance"\] \.settings-layout \{[\s\S]*?overflow: hidden;/);
assert.match(index, /\.maintenance-panel \{[\s\S]*?overflow-y: auto;[\s\S]*?overscroll-behavior: contain;/);
assert.match(index, /window\.addEventListener\("block-hmi-guest", \(\) => \{[\s\S]*?switchPage\("home"\)/);
assert.match(index, /if \(!requireFrontendPermission\("operate"\)\) return false;/);
assert.doesNotMatch(index, /requestModeChange|sendCommand\("set_mode"|acknowledgeAlarm/);
assert.match(index, /data-page="manual"[\s\S]*?id="manualPageTitle"/);
assert.match(index, /id="manualPageEntry" type="button" data-point-action="home\.homing">回原<\/button>/);
assert.doesNotMatch(index, /\$\("#manualPageEntry"\)\.addEventListener\("click", \(\) => switchPage\("manual"\)\);/);
assert.match(index, /\["home", "manual", "data", "alarm", "history"\]\.includes\(name\)/);
assert.match(index, /\$\$\("\.control-button"\)\.forEach\(button =>/);
assert.match(index, /id="manualXSpeedInput" type="number" step="any" inputmode="decimal"/);
assert.match(index, /id="manualZSpeedInput" type="number" step="any" inputmode="decimal"/);
assert.match(index, /input\.type = "number";\s*input\.step = "any";\s*input\.inputMode = "decimal";/);
assert.match(index, /class="manual-status-band" aria-label="轴状态">[\s\S]*?id="manualXPosition"[\s\S]*?id="manualZPosition"[\s\S]*?id="manualXLoad"[\s\S]*?id="manualZLoad"/);
assert.match(index, /\.manual-status-band \{[\s\S]*?grid-template-columns: repeat\(4, minmax\(0, 1fr\)\);/);
assert.match(index, /\.manual-action \{[\s\S]*?min-height: 58px;/);
assert.match(index, /\.manual-z-actions \{[\s\S]*?grid-template-rows: repeat\(2, 58px\);/);
assert.match(index, /\.manual-x-actions \{[\s\S]*?grid-template-rows: 58px;/);
assert.match(index, /\.manual-tool-action \{[\s\S]*?min-height: 54px;/);
assert.match(index, /\.manual-input-field \{[\s\S]*?min-height: 54px;/);
assert.match(index, /\.manual-claw-actions \.manual-action \{[\s\S]*?height: 58px;/);
assert.match(index, /\.manual-admin-actions \.manual-action \{[\s\S]*?min-height: 52px;/);
for (const manualAction of [
  'data-manual-action="z-up">↑ Z轴上移</button>',
  'data-manual-action="z-down">↓ Z轴下移</button>',
  'data-manual-action="x-left">← X轴左移</button>',
  'data-manual-action="x-right">X轴右移 →</button>',
  'id="manualClawState"',
  'data-manual-action="clamp">夹紧</button>',
  'data-manual-action="release">松开</button>'
]) {
  assert.ok(index.includes(manualAction), `manual control is missing ${manualAction}`);
}
assert.doesNotMatch(index, /manual-plane|manual-jog-pad|manual-axis-x|manual-axis-z|manual-carriage|manualCarriage|manualHomeState|manualState\.home/);
assert.match(index, /\.manual-side-rail\.has-admin #manualAdvancedMount \{[\s\S]*?flex: 1 1 auto;[\s\S]*?min-height: 0;/);
assert.match(index, /\.manual-admin-panel \{[\s\S]*?height: 100%;[\s\S]*?overflow-y: auto;[\s\S]*?overscroll-behavior: contain;/);
const manualStyles = index.slice(index.indexOf('.home-homing-button'), index.indexOf('</style>', index.indexOf('.home-homing-button')));
assert.doesNotMatch(manualStyles, /transition:|box-shadow:|linear-gradient|radial-gradient|backdrop-filter/);
assert.doesNotMatch(manualStyles, /min-height: 72px;/);
const manualHandler = index.match(/function handleManualAction\(button\) \{[\s\S]*?\n      \}\n\n      function commitManualNumber/);
assert.notEqual(manualHandler, null);
assert.match(manualHandler[0], /const displayPath = manualPathForButton\(button\);/);
assert.match(manualHandler[0], /binding\.state === "pending"/);
assert.match(manualHandler[0], /requireFrontendPermission\(manualPermission\(displayPath\)\)/);
assert.match(manualHandler[0], /runtime\.command\(displayPath\)/);
assert.match(manualHandler[0], /PLC 指令已确认/);
assert.match(manualHandler[0], /showToast\("电脑预览，不发送 PLC 指令", "info"\)/);
assert.doesNotMatch(manualHandler[0], /sendCommand|point\.command|WebSocket/);
const manualNumberCommit = index.match(/function commitManualNumber\(input\) \{[\s\S]*?\n      \}\n\n      function bindManualPage/);
assert.notEqual(manualNumberCommit, null);
assert.match(manualNumberCommit[0], /Number\(input\.value\)/);
assert.match(manualNumberCommit[0], /input\.value\.trim\(\) === ""/);
assert.match(manualNumberCommit[0], /runtime\.command\(displayPath, value\)/);
assert.doesNotMatch(manualNumberCommit[0], /sendCommand|point\.command|WebSocket/);
assert.doesNotMatch(index, /function manualDemoNumber/);
const manualAdminMountActionSource = index.match(/function manualAdminMountAction\(previousRole, nextRole, hasAdminNodes\) \{[\s\S]*?\n      \}/);
assert.notEqual(manualAdminMountActionSource, null);
const manualAdminMountAction = new Function(`${manualAdminMountActionSource[0]}; return manualAdminMountAction;`)();
assert.equal(manualAdminMountAction("ADMIN", "ADMIN", true), "retain");
assert.equal(manualAdminMountAction("ADMIN", "OPERATOR", true), "clear");
assert.equal(manualAdminMountAction("OPERATOR", "ADMIN", false), "build");
assert.equal(manualAdminMountAction("OPERATOR", "OPERATOR", false), "retain");
const manualAdminRender = index.match(/function renderManualAdmin\(\) \{[\s\S]*?\n      \}\n\n      function renderManual/);
assert.notEqual(manualAdminRender, null);
assert.match(manualAdminRender[0], /if \(mountAction === "retain"\) return false;/);
assert.match(manualAdminRender[0], /if \(mountAction === "clear"\) \{[\s\S]*?mount\.replaceChildren\(\);[\s\S]*?return true;/);
assert.match(manualAdminRender[0], /mount\.replaceChildren\(panel\);[\s\S]*?renderedManualAdminRole = manualRole;/);
assert.match(index, /权限来自当前产品会话；绝对执行 BOOL 未确认，数值参数按已配置点位读写。/);
assert.match(index, /function updateManualNumberInput\(input, displayPath, fallback = ""\) \{/);
assert.match(index, /input\[data-manual-speed\], input\[data-manual-admin-input\]/);
const configuredPointCommand = source.match(/private pointCommand\(displayPath: string, value\?: number\): Promise<void> \{[\s\S]*?\n  \}\n\n  private manualBinding/);
assert.notEqual(configuredPointCommand, null);
assert.match(configuredPointCommand[0], /this\.hasPermission\(permission\)/);
assert.match(configuredPointCommand[0], /this\.pendingPointCommand\.dispatch/);
assert.match(configuredPointCommand[0], /buildPointCommand\([\s\S]*?writePoint,[\s\S]*?action,[\s\S]*?requestId,[\s\S]*?action === "set" \? value : undefined/);
const manualCommand = source.match(/private manualCommand\(displayPath: string, value\?: number\): Promise<void> \{[\s\S]*?\n  \}\n\n  private sendCommand/);
assert.notEqual(manualCommand, null);
assert.match(manualCommand[0], /return this\.pointCommand\(displayPath, value\);/);
assert.match(source, /private manualCanWrite\(displayPath: string\): boolean/);
const staticMarkup = index.slice(0, index.indexOf('<script src="assets/vendor/simple-keyboard/index.js"></script>'));
assert.doesNotMatch(staticMarkup, /data-manual-admin=/);
assert.match(index, /if \(demoManualStart\) switchPage\("manual"\);/);
assert.match(source, /export function demoManualRoleFromSearch\(search: string\): FrontendRole/);
assert.match(source, /role\(\): FrontendRole;/);
assert.match(source, /role: \(\) => this\.frontendRole\(\)/);
assert.match(compiledSource, /export function demoManualRoleFromSearch\(search\)/);
assert.match(index, /async function runBackendMutation\(factory, options = \{\}\) \{[\s\S]*?mutationError[\s\S]*?showToast\(\s*backendErrorMessage\(mutationError\)/);
assert.match(index, /async function saveProductionSettings\(manual = false\) \{[\s\S]*?requireFrontendPermission\("maintenance"\)/);
assert.match(index, /data-maintenance-tab="production"[\s\S]*?data-maintenance-tab="wifi"[\s\S]*?data-maintenance-tab="plc"[\s\S]*?data-maintenance-tab="accounts"/);
assert.match(index, /data-maintenance-panel="production"[\s\S]*?data-maintenance-panel="wifi"[\s\S]*?data-maintenance-panel="plc"[\s\S]*?data-maintenance-panel="accounts"/);
for (const id of ["targetInput", "toolInput", "inspectInput", "piecesPerBoxInput"]) {
  assert.match(index, new RegExp(`id="${id}"[^>]*data-soft-submit="true"`));
}
assert.doesNotMatch(index, /650 ms|softKeyboardToggle|softKeyboardToggleState|productionSaveState|piecesPerBoxSaveState|savePiecesPerBoxButton|立即保存|换型时单独保存/);
assert.match(index, /"\/api\/maintenance\/production"/);
assert.match(index, /"\/api\/maintenance\/connectivity"/);
assert.match(index, /"\/api\/maintenance\/wifi\/connect"/);
assert.match(index, /id="plcSubnetInput"[^>]*value="192\.168\.1\.0\/24"[^>]*data-soft-keyboard="full"/);
assert.match(index, /id="plcHostInput" type="text" inputmode="decimal"[^>]*data-soft-keyboard="decimal"/);
assert.equal([...index.matchAll(/data-soft-keyboard="decimal"/g)].length, 1);
assert.match(index, /id="plcPortInput"[^>]*min="1"[^>]*max="65535"[^>]*data-soft-keyboard="numeric"/);
assert.match(index, /id="plcUnitInput"[^>]*min="1"[^>]*max="247"[^>]*data-soft-keyboard="numeric"/);
assert.match(index, /id="savePlcButton"[^>]*>保存地址并连接/);
for (const removedPLCCopy of [
  "由本机 Agent 扫描、连接和轮询；在此填写独立子网和 PLC 地址。",
  "扫描、连接和轮询均由本机 Agent 执行。保存地址会在首次读取成功后生效；本页不直接访问 PLC。",
  "最近采样",
  "最近错误",
  "点位数",
  "等待 PLC 实时点值"
]) {
  assert.equal(index.includes(removedPLCCopy), false, `${removedPLCCopy} should not remain in the PLC maintenance UI`);
}
for (const removedPLCDiagnostic of ["#plc-last-sample", "#plc-last-error", "#plc-point-count", "#plc-live-points", "点位表已同步，等待 PLC 读取"]) {
  assert.equal((source + compiledSource).includes(removedPLCDiagnostic), false, `${removedPLCDiagnostic} should not remain in PLC rendering`);
}
assert.match(source, /export function buildPLCDeviceID\(host: string, port = defaultPLCPort, unitID = defaultPLCUnitID\): string/);
assert.match(source, /private sendPLCScan\(\): void \{[\s\S]*?readPLCScanSettings\(\)[\s\S]*?buildPLCScan\(settings\.addressRange, settings\.port, settings\.unitID\)/);
assert.match(source, /private sendPLCSave\(\): void \{[\s\S]*?readPLCEndpoint\(\)[\s\S]*?buildPLCDeviceID\(endpoint\.host, endpoint\.port, endpoint\.unitID\)/);
assert.match(source, /scan\.disabled = !active;[\s\S]*?save\.disabled = !active;[\s\S]*?snapshot\.disabled = !active;/);
const wifiConnect = index.match(/async function connectWiFi\(\) \{[\s\S]*?\n      \}\n\n      function switchMaintenanceTab/);
assert.notEqual(wifiConnect, null);
assert.match(wifiConnect[0], /try \{[\s\S]*?await maintenanceRequest\("\/api\/maintenance\/wifi\/connect", "POST", \{ ssid, password \}\);[\s\S]*?passwordInput\.value = "";\s*window\.HMISoftKeyboard\?\.clearInput\(passwordInput\);[\s\S]*?return true;/);
assert.match(wifiConnect[0], /catch \(error\) \{[\s\S]*?return false;[\s\S]*?finally \{\s*wifiRequestInFlight = false;/);
assert.doesNotMatch(wifiConnect[0], /finally \{[\s\S]*?passwordInput\.value/);
assert.doesNotMatch(wifiConnect[0], /localStorage|sessionStorage|console\./);
assert.match(index, /<div class="settings-actions wifi-settings-actions"><button id="wifiRefreshButton" type="button">刷新状态<\/button><button id="saveWifiButton" type="submit">连接 Wi-Fi<\/button><\/div>/);
assert.ok(index.indexOf('<div class="settings-actions wifi-settings-actions"') < index.indexOf('<dl class="connection-facts" aria-label="Wi-Fi 与 BDM 当前状态">'));
assert.match(keyboardCSS, /\.page\[data-page="maintenance"\] \.wifi-settings-actions \{[\s\S]*?display: grid;[\s\S]*?grid-template-columns: repeat\(2, minmax\(0, 1fr\)\);/);
assert.match(keyboardCSS, /\.page\[data-page="maintenance"\] \.wifi-settings-actions button \{[\s\S]*?min-height: 56px;[\s\S]*?cursor: pointer;/);
assert.match(keyboardCSS, /html\[data-soft-keyboard-open="true"\]\[data-soft-keyboard-layout="full"\] \[data-page="maintenance"\]:not\(\[hidden\]\) \.settings-layout \{[\s\S]*?height: 332px;/);
assert.match(keyboardCSS, /html\[data-soft-keyboard-open="true"\]\[data-soft-keyboard-layout="full"\] #wifiSettingsPanel \{[\s\S]*?scroll-padding: 20px 0 92px;/);
assert.match(keyboardCSS, /html\[data-soft-keyboard-open="true"\]\[data-soft-keyboard-layout="full"\] #wifiSettingsPanel \.maintenance-panel-head \{[\s\S]*?display: none;/);
assert.match(keyboardCSS, /html\[data-soft-keyboard-open="true"\]\[data-soft-keyboard-layout="full"\] #wifiSettingsPanel \.wifi-settings-actions \{[\s\S]*?margin-top: 10px;/);
assert.doesNotMatch(index, /\.line-name::after|html\[data-backend-status="online"\] \.line-name::after/);
for (const removedMaintenanceCopy of ["选择一项本机配置", "目标、换刀与装箱", "本机无线连接", "只读采集状态", "本机账户与会话"]) {
  assert.equal(index.includes(removedMaintenanceCopy), false, `${removedMaintenanceCopy} should not remain in the maintenance UI`);
}
assert.doesNotMatch(source + compiledSource + index, /\/api\/v[12]\//);
assert.doesNotMatch(index, /backend\.updateSettings/);
assert.doesNotMatch(index, /api-client\.js/);
const passwordInputIDs = [...index.matchAll(/<input id="([^"]+)"[^>]*type="password"/g)].map((match) => match[1]);
const passwordToggleIDs = [...index.matchAll(/<button[^>]*aria-controls="([^"]+)"[^>]*data-password-toggle/g)].map((match) => match[1]);
assert.equal(passwordInputIDs.length, 7);
assert.deepEqual(passwordToggleIDs.sort(), passwordInputIDs.sort());
assert.match(index, /class="password-visibility-toggle" type="button"[^>]*aria-label="显示密码"[^>]*aria-pressed="false"/);
assert.match(keyboardCSS, /#auth-panel\[data-keyboard-open="true"\] \{[\s\S]*?--auth-sheet-top-gap:[\s\S]*?padding-bottom: calc\(100vh - var\(--auth-keyboard-top/);
assert.match(keyboardCSS, /#auth-panel\[data-keyboard-open="true"\] \.auth-sheet \{[\s\S]*?max-height: calc\(var\(--auth-keyboard-top[\s\S]*?overflow-y: auto;/);
assert.match(keyboardCSS, /#auth-panel\[data-keyboard-open="true"\] \.auth-form \.auth-field \{[\s\S]*?grid-template-columns: 90px minmax\(0, 1fr\);/);
assert.match(keyboardCSS, /#auth-panel\[data-keyboard-open="true"\] ~ #hmi \.soft-keyboard-foot[\s\S]*?display: none;/);
assert.match(keyboardCSS, /\.soft-keyboard-dock\[data-open-immediate="true"\] \{[\s\S]*?transition: none;/);
assert.match(keyboardCSS, /#auth-panel\[data-keyboard-open="true"\] ~ #hmi \.soft-keyboard-dock \{[\s\S]*?transition: none;/);
assert.match(keyboardSource, /dock\.addEventListener\("transitionend", function \(event\) \{[\s\S]*?syncAuthKeyboardLayout\(\);/);
assert.match(keyboardSource, /function notifyReady\(\) \{[\s\S]*?window\.dispatchEvent\(new window\.Event\("hmi-soft-keyboard-ready"\)\)/);
assert.match(keyboardSource, /function showDock\(immediate\) \{[\s\S]*?if \(immediate\) \{[\s\S]*?dock\.setAttribute\("data-open-immediate", "true"\);[\s\S]*?dock\.hidden = false;[\s\S]*?dock\.classList\.add\("is-open"\);[\s\S]*?syncAuthKeyboardLayout\(true\);[\s\S]*?return;/);
assert.match(keyboardSource, /function openForInput\(input, options\) \{[\s\S]*?showDock\(options && options\.immediate === true\);/);
assert.match(keyboardSource, /document\.addEventListener\("keydown", handlePhysicalKeyboard, true\);[\s\S]*?notifyReady\(\);/);
assert.match(keyboardSource, /"1 2 3 4 5 6 7 8 9 0 - = \."/);
assert.match(keyboardSource, /"\{shift\} z x c v b n m \. - \{bksp\}"/);
assert.match(keyboardSource, /var shiftIcon = '<svg[\s\S]*?var backspaceIcon = '<svg/);
assert.match(keyboardSource, /value: "切换大小写"[\s\S]*?value: "退格"/);
assert.match(keyboardSource, /function isAuthenticationInput\(input\) \{[\s\S]*?data-soft-submit/);
assert.match(keyboardSource, /function validateInput\(input, focusOnError\) \{[\s\S]*?isAuthenticationInput\(input\)/);
assert.match(keyboardSource, /function syncActiveValue\(value, emitInput\) \{[\s\S]*?if \(activeInput\.value !== nextValue\) activeInput\.value = nextValue;[\s\S]*?clearError\(activeInput\);/);
assert.match(keyboardSource, /numeric:\s*\[[\s\S]*?"\{cancel\} 0 00 \{done\}"[\s\S]*?\],\s*decimal:/);
assert.match(keyboardSource, /decimal:\s*\[[\s\S]*?"\{cancel\} 0 \. \{done\}"[\s\S]*?\]/);
assert.match(keyboardSource, /function getLayout\(input\) \{[\s\S]*?layout === "full" \|\| layout === "decimal"/);
assert.match(keyboardSource, /activeLayout === "decimal"[\s\S]*?replace\(\/\[\^0-9\.\]\/g, ""\)/);
assert.match(keyboardSource, /activeLayout === "decimal" && !\/\^\[0-9\.\]\$\/\.test\(event\.key\)/);
assert.match(keyboardSource, /function clearError\(input\) \{[\s\S]*?input\.hasAttribute\("aria-invalid"\)[\s\S]*?validationLine\.textContent !== ""/);
assert.match(keyboardSource, /disableButtonHold: true/);
assert.match(keyboardSource, /function ensureKeyboard\(\) \{\s*if \(keyboard\) return true;/);
assert.match(keyboardSource, /function bindInput\(input\) \{\s*if \(input\.getAttribute\("data-soft-keyboard-bound"\) === "true"\) return;/);
assert.match(keyboardSource, /function clearInput\(input\) \{[\s\S]*?if \(input === activeInput\) \{\s*originalValue = "";\s*committedValue = "";\s*\}[\s\S]*?keyboard\.setInput\("", inputName\);[\s\S]*?dispatchFieldEvent\(input, "input"\);/);

const keyboardHarnessSource = keyboardSource.replace(
  "  window.HMISoftKeyboard = {",
  `  window.__keyboardCancelHarness = {
    setActive: function (input, nextKeyboard, nextDock, original, layout) {
      activeInput = input;
      activeInputName = getInputName(input);
      keyboard = nextKeyboard;
      dock = nextDock;
      activeLayout = layout || "numeric";
      isOpen = true;
      pinned = false;
      enabled = true;
      available = true;
      mode = "soft";
      originalValue = original;
      committedValue = original;
    },
    state: function () {
      return { activeInput: activeInput, originalValue: originalValue, committedValue: committedValue };
    },
    sync: function (value) {
      syncActiveValue(value, true);
    }
  };

  window.HMISoftKeyboard = {`
);
assert.notEqual(keyboardHarnessSource, keyboardSource);

function keyboardTestAttributes() {
  const values = new Map();
  return {
    getAttribute(name) { return values.has(name) ? values.get(name) : null; },
    setAttribute(name, value) { values.set(name, String(value)); },
    removeAttribute(name) { values.delete(name); },
    hasAttribute(name) { return values.has(name); },
    toggleAttribute(name, force) {
      const shouldHave = force === undefined ? !values.has(name) : Boolean(force);
      if (shouldHave) values.set(name, "");
      else values.delete(name);
      return shouldHave;
    }
  };
}

function keyboardTestClassList() {
  return { add() {}, remove() {}, toggle() {}, contains() { return false; } };
}

function keyboardTestInput(id, value) {
  return {
    ...keyboardTestAttributes(),
    id,
    name: "",
    value,
    disabled: false,
    hidden: false,
    type: "password",
    readOnly: false,
    required: false,
    nodeType: 1,
    tagName: "INPUT",
    classList: keyboardTestClassList(),
    style: { setProperty() {}, removeProperty() {} },
    addEventListener() {},
    dispatchEvent(event) { this.events = (this.events || []).concat(event.type); },
    getClientRects() { return [{}]; },
    focus() {}
  };
}

function keyboardTestDock() {
  return {
    ...keyboardTestAttributes(),
    classList: keyboardTestClassList(),
    hidden: false,
    addEventListener() {},
    getBoundingClientRect() { return { top: 1 }; }
  };
}

class KeyboardCancelTestKeyboard {
  constructor() {
    this.values = new Map();
    this.options = {};
  }

  getInput(name) { return this.values.get(name) || ""; }
  setInput(value, name) { this.values.set(name, String(value)); }
  setOptions(options) { Object.assign(this.options, options); }
  setCaretPosition() {}
}

class KeyboardCancelTestEvent {
  constructor(type, init = {}) {
    this.type = type;
    Object.assign(this, init);
  }
}

const keyboardTestRoot = {
  ...keyboardTestAttributes(),
  classList: keyboardTestClassList(),
  style: { setProperty() {}, removeProperty() {} }
};
const keyboardTestDocument = {
  documentElement: keyboardTestRoot,
  readyState: "loading",
  body: null,
  getElementById() { return null; },
  addEventListener() {},
  dispatchEvent() {},
  querySelectorAll() { return []; },
  querySelector() { return null; },
  createEvent(type) { return new KeyboardCancelTestEvent(type); }
};
const keyboardTestWindow = {
  location: { search: "" },
  Event: KeyboardCancelTestEvent,
  CustomEvent: KeyboardCancelTestEvent,
  setTimeout() { return 0; },
  clearTimeout() {},
  requestAnimationFrame(callback) { callback(); },
  addEventListener() {},
  dispatchEvent() {},
  getComputedStyle() { return { display: "block", visibility: "visible" }; }
};
vm.runInNewContext(keyboardHarnessSource, { window: keyboardTestWindow, document: keyboardTestDocument }, {
  filename: "soft-keyboard.js"
});

const keyboardCancelHarness = keyboardTestWindow.__keyboardCancelHarness;
const plcIPAddressInput = keyboardTestInput("plcHostInput", "");
plcIPAddressInput.setAttribute("data-soft-keyboard", "decimal");
const plcIPAddressKeyboard = new KeyboardCancelTestKeyboard();
keyboardCancelHarness.setActive(plcIPAddressInput, plcIPAddressKeyboard, keyboardTestDock(), "", "decimal");
keyboardCancelHarness.sync("192.168.10.87");
assert.equal(plcIPAddressInput.value, "192.168.10.87");
keyboardCancelHarness.sync("192x.168.10.87");
assert.equal(plcIPAddressInput.value, "192.168.10.87");
const ordinaryNumericInput = keyboardTestInput("ordinaryNumeric", "");
const ordinaryNumericKeyboard = new KeyboardCancelTestKeyboard();
keyboardCancelHarness.setActive(ordinaryNumericInput, ordinaryNumericKeyboard, keyboardTestDock(), "", "numeric");
keyboardCancelHarness.sync("12.3");
assert.equal(ordinaryNumericInput.value, "123");
const failedWifiPassword = keyboardTestInput("wifiPasswordInput", "failed-password");
const failedWifiKeyboard = new KeyboardCancelTestKeyboard();
const failedWifiDock = keyboardTestDock();
keyboardCancelHarness.setActive(failedWifiPassword, failedWifiKeyboard, failedWifiDock, "failed-password");
assert.equal(failedWifiPassword.value, "failed-password");
keyboardTestWindow.HMISoftKeyboard.clearInput(failedWifiPassword);
assert.equal(failedWifiPassword.value, "");
assert.equal(failedWifiKeyboard.getInput("wifiPasswordInput"), "");
assert.equal(keyboardCancelHarness.state().originalValue, "");
assert.equal(keyboardCancelHarness.state().committedValue, "");
keyboardTestWindow.HMISoftKeyboard.close("cancel");
assert.equal(failedWifiPassword.value, "");
assert.equal(keyboardCancelHarness.state().activeInput, null);

const switchedWifiPassword = keyboardTestInput("wifiPasswordInput", "failed-password");
const switchedWifiKeyboard = new KeyboardCancelTestKeyboard();
keyboardCancelHarness.setActive(switchedWifiPassword, switchedWifiKeyboard, keyboardTestDock(), "failed-password");
keyboardTestWindow.HMISoftKeyboard.clearInput(switchedWifiPassword);
const switchedWifiSsid = keyboardTestInput("wifiSsidInput", "line-wifi");
assert.equal(keyboardTestWindow.HMISoftKeyboard.open(switchedWifiSsid), true);
assert.equal(switchedWifiPassword.value, "");
keyboardTestWindow.HMISoftKeyboard.close("cancel");
assert.equal(switchedWifiPassword.value, "");
assert.equal(switchedWifiSsid.value, "line-wifi");

const ordinaryInput = keyboardTestInput("ordinaryInput", "before");
const ordinaryKeyboard = new KeyboardCancelTestKeyboard();
keyboardCancelHarness.setActive(ordinaryInput, ordinaryKeyboard, keyboardTestDock(), "before");
ordinaryInput.value = "edited";
ordinaryKeyboard.setInput("edited", "ordinaryInput");
keyboardTestWindow.HMISoftKeyboard.close("cancel");
assert.equal(ordinaryInput.value, "before");
assert.match(keyboardSource, /hmi-soft-keyboard-statechange", \{ open: true \}/);
assert.match(keyboardSource, /hmi-soft-keyboard-statechange", \{ open: false \}/);
assert.match(keyboardCSS, /\.soft-keyboard-dock \{[\s\S]*?box-shadow: none;[\s\S]*?transition: none;/);
assert.match(keyboardCSS, /\.hmi-simple-keyboard\.hg-theme-default \.hg-button \{[\s\S]*?box-shadow: none;[\s\S]*?transition: none;/);
assert.match(source, /private publishLiveState\(force = false\): void \{[\s\S]*?this\.isUserInputActive\(\) && !force[\s\S]*?this\.emitState\(force\);/);
assert.match(source, /message\.type === "points\.changed"[\s\S]*?this\.publishLiveState\(\);/);
assert.match(source, /private emitState\(force = false\): void \{[\s\S]*?new CustomEvent\("block-hmi-state", \{[\s\S]*?state: cloneState\(this\.currentState\(\)\), forceRender: force/);
assert.match(compiledSource, /emitState\(force = false\) \{[\s\S]*?new CustomEvent\("block-hmi-state", \{[\s\S]*?state: cloneState\(this\.currentState\(\)\), forceRender: force/);
const visibleNativeInputGuard = /nativeKeyboardInput[\s\S]*?active\.isConnected[\s\S]*?active\.closest\("\[hidden\], \[inert\]"\) === null/;
assert.match(source, visibleNativeInputGuard);
assert.match(compiledSource, visibleNativeInputGuard);
assert.match(index, visibleNativeInputGuard);
assert.match(source, /private getState\(\): Promise<\{ state: LegacyState \}> \{[\s\S]*?if \(!this\.demo && !this\.canSendRuntime\(\)\) \{[\s\S]*?runtime_unavailable/);
assert.match(index, /function applyServerState\(nextState, options = \{\}\) \{[\s\S]*?incomingRevision < state\.revision && !options\.forceRender[\s\S]*?if \(inputInteractionActive\(\) && !options\.forceRender\) \{[\s\S]*?deferredServerRender = true;[\s\S]*?return true;[\s\S]*?renderAll\(\);/);
assert.match(index, /window\.addEventListener\("block-hmi-state", event => \{[\s\S]*?const liveState = event\.detail && event\.detail\.state;[\s\S]*?applyServerState\(liveState, \{ forceRender: event\.detail\.forceRender === true \}\);[\s\S]*?void refreshBackendState\(\);/);
assert.match(index, /id="targetInput" name="target" type="number"[^>]*max="60000"[^>]*value=""[^>]*placeholder="—"[^>]*required/);
assert.doesNotMatch(index, /id="targetInput"[^>]*value="30"/);
assert.match(index, /function syncPLCTargetInput\(\) \{[\s\S]*?\$\("#targetInput"\)\.value = isValidProductionTarget\(state\.target\) \? String\(state\.target\) : "";/);
assert.match(index, /if \(demoMode\) assign\("#targetInput", Number\(production\.targetProduction\)/);
assert.match(index, /function isValidProductionTarget\(value\) \{[\s\S]*?value <= 60000/);
assert.match(index, /function productionPatch\(\) \{[\s\S]*?target <= 60000[\s\S]*?产能设定必须为 1 到 60000 的整数/);
const eventRenderer = index.match(/function renderEvents\(\) \{[\s\S]*?\n      \}\n\n      function renderHistory/);
assert.notEqual(eventRenderer, null);
assert.match(eventRenderer[0], /homeList\.innerHTML = state\.alarms\.map/);
assert.doesNotMatch(eventRenderer[0], /slice\(|acknowledged|item\.code|ack-button/);
assert.match(eventRenderer[0], /class="event-line \$\{safeEventLevel\(item\.level\)\}"[\s\S]*?escapeHTML\(item\.text\)/);
assert.match(index, /\.event-box \{[\s\S]*?overflow-y: auto;[\s\S]*?overscroll-behavior: contain;/);
assert.match(index, /\.event-line\.danger \.dot,[\s\S]*?background: var\(--red\);/);
assert.match(index, /\.event-line\.warning \.dot,[\s\S]*?background: var\(--amber\);/);
assert.match(index, /window\.addEventListener\("hmi-soft-keyboard-statechange", \(\) => \{[\s\S]*?flushDeferredServerRender/);
assert.match(index, /document\.addEventListener\("contextmenu", event => event\.preventDefault\(\)\);/);
assert.doesNotMatch(index, /requestFullscreen|exitFullscreen|\balert\(|\bconfirm\(|\bprompt\(/);
assert.doesNotMatch(source + keyboardSource, /\balert\(|\bconfirm\(|\bprompt\(/);
assert.match(source, /const maintenance = document\.querySelector<HTMLElement>\("#accountSettingsPanel"\)!;/);
assert.match(source, /private renderPLCReadOnly\(\): void/);
assert.equal(existsSync(new URL("./machine-bin.png", import.meta.url)), true);
assert.equal(existsSync(new URL("./soft-keyboard.js", import.meta.url)), true);

function rectangle(top, bottom, left = 0, right = 520) {
  return { top, bottom, left, right, width: right - left, height: bottom - top };
}

function probeElement(rect, computedStyle, options = {}) {
  return {
    hidden: options.hidden ?? false,
    clientHeight: options.clientHeight ?? rect.height,
    scrollHeight: options.scrollHeight ?? rect.height,
    computedStyle,
    getAttribute: options.getAttribute ?? (() => null),
    getBoundingClientRect: () => rect,
    getClientRects: () => options.visible === false ? [] : [rect]
  };
}

const visualStyle = { backgroundColor: "rgb(1, 2, 3)", filter: "none", opacity: "1", backdropFilter: "none" };
const authPanelProbe = probeElement(rectangle(0, 1080, 0, 1920), {
  backgroundColor: "rgba(0, 0, 0, 0)", filter: "none", opacity: "1", overflow: "hidden", pointerEvents: "none"
}, { getAttribute: (name) => name === "data-keyboard-open" ? "true" : null });
const authSheetProbe = probeElement(rectangle(16, 526, 710, 1210), { overflowY: "auto", pointerEvents: "auto" }, { clientHeight: 510, scrollHeight: 570 });
const keyboardDockProbe = probeElement(rectangle(542, 1000, 28, 1892), {}, { visible: true });
const loginFormProbe = probeElement(rectangle(120, 350, 742, 1178), {});
const loginActionsProbe = probeElement(rectangle(360, 422, 742, 1178), {});
const loginSubmitProbe = probeElement(rectangle(370, 422, 742, 1178), {});
const bootstrapFormProbe = probeElement(rectangle(120, 430, 742, 1178), {});
const bootstrapActionsProbe = probeElement(rectangle(440, 502, 742, 1178), {});
const bootstrapSubmitProbe = probeElement(rectangle(450, 502, 742, 1178), {});
const probeElements = new Map([
  ["#auth-panel", authPanelProbe],
  ["#auth-panel .auth-sheet", authSheetProbe],
  ["#softKeyboardDock", keyboardDockProbe],
  ["#login-form", loginFormProbe],
  ["#login-form .auth-actions", loginActionsProbe],
  ["#login-form button[type=\"submit\"]", loginSubmitProbe],
  ["#initial-admin-form", bootstrapFormProbe],
  ["#initial-admin-form .auth-actions", bootstrapActionsProbe],
  ["#initial-admin-form button[type=\"submit\"]", bootstrapSubmitProbe],
  ["#hmi", probeElement(rectangle(0, 1080, 0, 1920), visualStyle)],
  ["#hmi-topbar", probeElement(rectangle(0, 66), visualStyle)],
  ["#hmi-pages", probeElement(rectangle(66, 958, 0, 1920), visualStyle)],
  ["#hmi-footer", probeElement(rectangle(958, 1080, 0, 1920), visualStyle)]
]);
const layoutResult = assertAuthKeyboardLayout({ querySelector: (selector) => probeElements.get(selector) }, {
  innerWidth: 1920,
  innerHeight: 1080,
  getComputedStyle: (element) => element.computedStyle
});
assert.equal(layoutResult.sheetTopGap, authKeyboardSafeGap);
assert.equal(layoutResult.sheetKeyboardGap, authKeyboardSafeGap);
const submitLayoutResult = assertAuthSubmitLayout({ querySelector: (selector) => probeElements.get(selector) }, {
  innerWidth: 1920,
  innerHeight: 1080
});
assert.equal(submitLayoutResult.viewportWidth, 1920);
assert.equal(submitLayoutResult.viewportHeight, 1080);

const maintenanceKeyboardPanelProbe = probeElement(rectangle(208, 500, 472, 1712), { overflowY: "auto", pointerEvents: "auto" }, {
  clientHeight: 292,
  scrollHeight: 764
});
const maintenanceKeyboardDockProbe = probeElement(rectangle(516, 830, 188, 1772), { pointerEvents: "auto" }, { visible: true });
const maintenanceWifiSsidProbe = probeElement(rectangle(266, 320, 650, 1200), { pointerEvents: "auto" });
const maintenanceWifiPasswordProbe = probeElement(rectangle(332, 386, 650, 1200), { pointerEvents: "auto" });
const maintenanceWifiActionsProbe = probeElement(rectangle(408, 464, 650, 1200), { pointerEvents: "auto" });
const maintenanceWifiRefreshProbe = probeElement(rectangle(408, 464, 650, 920), { pointerEvents: "auto" });
const maintenanceWifiConnectProbe = probeElement(rectangle(408, 464, 930, 1200), { pointerEvents: "auto" });
const maintenanceKeyboardElements = new Map([
  ["#wifiSettingsPanel", maintenanceKeyboardPanelProbe],
  ["#softKeyboardDock", maintenanceKeyboardDockProbe],
  [".wifi-settings-actions", maintenanceWifiActionsProbe],
  ["#wifiSsidInput", maintenanceWifiSsidProbe],
  ["#wifiPasswordInput", maintenanceWifiPasswordProbe],
  ["#wifiRefreshButton", maintenanceWifiRefreshProbe],
  ["#saveWifiButton", maintenanceWifiConnectProbe]
]);
const maintenanceKeyboardLayout = assertMaintenanceFullKeyboardLayout({
  documentElement: {
    getAttribute: (name) => name === "data-soft-keyboard-open" ? "true" : (name === "data-soft-keyboard-layout" ? "full" : null)
  },
  querySelector: (selector) => maintenanceKeyboardElements.get(selector)
}, {
  innerWidth: 1920,
  innerHeight: 1080,
  getComputedStyle: (element) => element.computedStyle
});
assert.equal(maintenanceKeyboardLayout.viewportWidth, 1920);
assert.equal(maintenanceKeyboardLayout.viewportHeight, 1080);
assert.ok(maintenanceKeyboardLayout.actionRect.bottom <= maintenanceKeyboardLayout.dockRect.top);
assert.equal(maintenanceKeyboardLayout.actionDockIntersection, 0);

function assertHomeBinScreenshotLayout(performanceMode) {
  const row = rectangle(753, 811, 212, 968);
  const bins = [rectangle(753, 811, 212, 582), rectangle(753, 811, 598, 968)];
  const homeLeft = rectangle(188, 830, 184, 996);
  const protectedRegions = [
    rectangle(294, 694, 202, 978),
    rectangle(188, 830, 1016, 1736),
    rectangle(848, 990, 160, 1760)
  ];
  assert.equal(typeof performanceMode, "boolean");
  if (performanceMode) {
    const performanceRule = index.match(/html\[data-performance="low"\] \*,[\s\S]*?\n    \}/);
    assert.notEqual(performanceRule, null);
    assert.doesNotMatch(performanceRule[0], /\b(?:grid|width|height|position|top|right|bottom|left)\b/);
  }
  assert.equal(bins.length, 2);
  assert.equal(bins[0].top, bins[1].top);
  assert.equal(bins[0].bottom, bins[1].bottom);
  assert.equal(bins[0].width, bins[1].width);
  assert.equal(bins[0].height, bins[1].height);
  assert.ok(bins[0].right < bins[1].left);
  assert.equal(bins[0].left, row.left);
  assert.equal(bins[1].right, row.right);
  assert.ok(row.left >= homeLeft.left && row.right <= homeLeft.right && row.bottom <= homeLeft.bottom);
  for (const protectedRegion of protectedRegions) {
    assert.equal(rectangleIntersectionArea(row, protectedRegion), 0);
  }
}

assertHomeBinScreenshotLayout(false);
assertHomeBinScreenshotLayout(true);
