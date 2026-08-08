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
assert.equal(JSON.stringify(configured).includes("displayPath"), false);
assert.equal(JSON.stringify(configured).includes("description"), false);
assert.equal(buildPLCScan("192.168.1.0/24", "scan", timestamp).type, "plc.scan");
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

const source = readFileSync(new URL("./hmi.mts", import.meta.url), "utf8");
const compiledSource = readFileSync(new URL("./hmi.mjs", import.meta.url), "utf8");
const index = readFileSync(new URL("../index.html", import.meta.url), "utf8");
const demoShell = readFileSync(new URL("../demo-shell.html", import.meta.url), "utf8");
const keyboardSource = readFileSync(new URL("./soft-keyboard.js", import.meta.url), "utf8");
const keyboardCSS = readFileSync(new URL("./soft-keyboard.css", import.meta.url), "utf8");
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
const pointCommand = source.match(/private sendCommand\(command: string, payload: Record<string, unknown> = \{\}\): Promise<\{ state: LegacyState \}> \{[\s\S]*?\n  \}\n\n  private acknowledgeAlarm/);
assert.notEqual(pointCommand, null);
assert.match(pointCommand[0], /command === "set_mode"[\s\S]*?displayPath: "home\.machine\.enabled", action: "toggle"[\s\S]*?buildPointCommand\(pointID, operation\.action, requestId\)/);
assert.match(pointCommand[0], /pendingPointCommand\.dispatch\(requestId, \(\) => \{[\s\S]*?this\.socket!\.send/);
assert.match(source, /const enabled = this\.valueFor\("home\.machine\.enabled"\);[\s\S]*?state\.mode = enabled === true \? "auto" : "manual";/);
const productionPolicy = source.match(/private deferProductionPolicy\(\): void \{[\s\S]*?\n  \}\n\}/);
assert.notEqual(productionPolicy, null);
assert.match(productionPolicy[0], /\.control-button:not\(\.manual-entry-button\)/);
assert.match(productionPolicy[0], /const available = runtimeEnabled && button === start;/);
assert.match(productionPolicy[0], /mode\.dataset\.backendUnavailable = runtimeEnabled \? "false" : "true";/);
assert.doesNotMatch(productionPolicy[0], /mode\.dataset\.backendUnavailable = "true"/);
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
assert.match(index, /#hmi-footer \.nav-button,[\s\S]*?#hmi-footer #operatorName,[\s\S]*?#hmi-footer \.mode \{[\s\S]*?height: var\(--footer-control-height\);[\s\S]*?min-height: var\(--footer-control-height\);/);
assert.match(index, /#hmi-footer \.operator \{[\s\S]*?height: var\(--footer-control-height\);/);
assert.match(index, /#hmi-footer #operatorName,[\s\S]*?#hmi-footer \.mode \{[\s\S]*?width: 168px;[\s\S]*?border-radius: 18px;/);
assert.match(index, /#hmi-footer #operatorName::before \{[\s\S]*?data:image\/svg\+xml/);
assert.match(index, /#hmi-footer \.mode\.is-auto \{[\s\S]*?color: #176b38;[\s\S]*?background: #e8f7ec;/);
assert.match(index, /#hmi-footer \.mode\.is-manual \{[\s\S]*?color: #8a6200;[\s\S]*?background: #fff5d7;/);
assert.match(index, /modeToggle\.classList\.toggle\("is-auto", state\.mode === "auto"\);[\s\S]*?modeToggle\.classList\.toggle\("is-manual", state\.mode === "manual"\);/);
for (const asset of [
  'assets/soft-keyboard.css?v=20260808.3',
  'assets/soft-keyboard.js?v=20260808.3',
  './assets/hmi.mjs?v=20260808.1'
]) {
  assert.ok(index.includes(asset), `cache version is missing from ${asset}`);
}
assert.match(index, /function requireFrontendPermission\(permission\)/);
assert.match(index, /name === "maintenance" && !requireFrontendPermission\("maintenance"\)/);
assert.match(index, /\.page\[data-page="maintenance"\] \.settings-layout \{[\s\S]*?overflow: hidden;/);
assert.match(index, /\.maintenance-panel \{[\s\S]*?overflow-y: auto;[\s\S]*?overscroll-behavior: contain;/);
assert.match(index, /window\.addEventListener\("block-hmi-guest", \(\) => \{[\s\S]*?switchPage\("home"\)/);
assert.match(index, /if \(!requireFrontendPermission\("operate"\)\) return false;/);
const modeChange = index.match(/async function requestModeChange\(\) \{[\s\S]*?\n      \}\n\n      async function handleAction/);
assert.notEqual(modeChange, null);
assert.match(modeChange[0], /if \(!requireFrontendPermission\("operate"\)\) return false;/);
assert.match(modeChange[0], /state\.mode === "auto" \? "manual" : "auto"/);
assert.match(modeChange[0], /backend\.sendCommand\("set_mode"/);
assert.match(modeChange[0], /runBackendMutation\(/);
assert.match(index, /data-page="manual"[\s\S]*?id="manualPageTitle"/);
assert.match(index, /id="manualPageEntry" type="button">手动模式<\/button>/);
assert.doesNotMatch(index, /id="manualPageEntry"[^>]*data-action=/);
assert.match(index, /\$\("#manualPageEntry"\)\.addEventListener\("click", \(\) => switchPage\("manual"\)\);/);
assert.match(index, /\["home", "manual", "data", "alarm", "history"\]\.includes\(name\)/);
assert.match(index, /\.control-button:not\(\.manual-entry-button\)/);
const manualHandler = index.match(/function handleManualAction\(button\) \{[\s\S]*?\n      \}\n\n      function bindManualPage/);
assert.notEqual(manualHandler, null);
assert.match(manualHandler[0], /if \(!requireFrontendPermission\("operate"\)\) return false;/);
assert.match(manualHandler[0], /if \(!demoMode\) \{[\s\S]*?当前页面未绑定现场写入/);
assert.doesNotMatch(manualHandler[0], /sendCommand|point\.command|WebSocket/);
assert.match(index, /function renderManualAdmin\(\) \{[\s\S]*?mount\.replaceChildren\(\);[\s\S]*?if \(manualRole !== "ADMIN"\) return;/);
assert.match(index, /权限来自当前产品设计：源点位表没有管理员角色列。/);
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
assert.match(index, /setTimeout\(\(\) => \{[\s\S]*?saveProductionSettings\(\);[\s\S]*?\}, 650\)/);
assert.match(index, /"\/api\/maintenance\/production"/);
assert.match(index, /"\/api\/maintenance\/connectivity"/);
assert.match(index, /"\/api\/maintenance\/wifi\/connect"/);
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
assert.match(keyboardSource, /function clearError\(input\) \{[\s\S]*?input\.hasAttribute\("aria-invalid"\)[\s\S]*?validationLine\.textContent !== ""/);
assert.match(keyboardSource, /disableButtonHold: true/);
assert.match(keyboardSource, /function ensureKeyboard\(\) \{\s*if \(keyboard\) return true;/);
assert.match(keyboardSource, /function bindInput\(input\) \{\s*if \(input\.getAttribute\("data-soft-keyboard-bound"\) === "true"\) return;/);
assert.match(keyboardSource, /function clearInput\(input\) \{[\s\S]*?if \(input === activeInput\) \{\s*originalValue = "";\s*committedValue = "";\s*\}[\s\S]*?keyboard\.setInput\("", inputName\);[\s\S]*?dispatchFieldEvent\(input, "input"\);/);

const keyboardHarnessSource = keyboardSource.replace(
  "  window.HMISoftKeyboard = {",
  `  window.__keyboardCancelHarness = {
    setActive: function (input, nextKeyboard, nextDock, original) {
      activeInput = input;
      activeInputName = getInputName(input);
      keyboard = nextKeyboard;
      dock = nextDock;
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
assert.match(source, /private publishLiveState\(\): void \{[\s\S]*?this\.isUserInputActive\(\)[\s\S]*?this\.deferredLiveState = true;/);
assert.match(source, /message\.type === "points\.changed"[\s\S]*?this\.publishLiveState\(\);/);
assert.match(index, /function applyServerState\(nextState, options = \{\}\) \{[\s\S]*?if \(inputInteractionActive\(\)\) \{[\s\S]*?deferredServerRender = true;[\s\S]*?return true;[\s\S]*?renderAll\(\);/);
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
