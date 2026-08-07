import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { assertAuthKeyboardLayout, authKeyboardSafeGap } from "../tools/auth-layout-probe.mjs";
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
  demoAuthScreenForPreview,
  isDisplayPath,
  localAdminStorageKey,
  localAdministratorFrom,
  localSessionFrom,
  localSessionIsActive,
  localSettingsStorageKey,
  passwordDigest,
  readLocalAdministrator,
  readLocalSettings,
  StartCommandReceipt
} from "./hmi.mjs";

assert.equal(isDisplayPath("home.machine.start"), true);
assert.equal(isDisplayPath("Home.machine.start"), false);

const storageValues = new Map();
const storage = () => ({
  getItem(key) { return storageValues.get(key) ?? null; },
  setItem(key, value) { storageValues.set(key, value); },
  removeItem(key) { storageValues.delete(key); }
});
const passwordHash = await passwordDigest("admin-password");
assert.equal(passwordHash.length, 64);
assert.match(passwordHash, /^[a-f0-9]{64}$/);
assert.equal(localAdministratorFrom({ username: "admin", password: "plain" }), null);
assert.equal(readLocalAdministrator(storage), null);
storageValues.set(localAdminStorageKey, JSON.stringify({
  username: "admin", passwordHash, permissions: { operate: true, maintenance: true }
}));
assert.deepEqual(readLocalAdministrator(storage), {
  username: "admin", passwordHash, permissions: { operate: true, maintenance: true }
});
assert.deepEqual(readLocalSettings(storage), { idleTimeoutSeconds: defaultIdleTimeoutSeconds });
storageValues.set(localSettingsStorageKey, JSON.stringify({ idleTimeoutSeconds: 600 }));
assert.deepEqual(readLocalSettings(storage), { idleTimeoutSeconds: 600 });
assert.deepEqual(localSessionFrom({
  username: "admin", permissions: { operate: true, maintenance: true }, lastActivity: 100, expiresAt: 200
}), { username: "admin", permissions: { operate: true, maintenance: true }, lastActivity: 100, expiresAt: 200 });
assert.equal(localSessionIsActive({
  username: "admin", permissions: { operate: true, maintenance: true }, lastActivity: 100, expiresAt: 200
}, 199), true);
assert.equal(localSessionIsActive({
  username: "admin", permissions: { operate: true, maintenance: true }, lastActivity: 100, expiresAt: 200
}, 200), false);

assert.equal(demoAuthPreviewFromSearch("?demo=1&auth=bootstrap"), "bootstrap");
assert.equal(demoAuthPreviewFromSearch("?demo=1&auth=login"), "login");
assert.equal(demoAuthPreviewFromSearch("?demo=1"), null);
assert.equal(demoAuthScreenForPreview("login", () => ({ getItem: () => null })), "bootstrap");
assert.equal(demoAuthScreenForPreview("login", storage), "login");
assert.equal(demoAuthScreenForPreview("bootstrap", storage), "bootstrap");

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

const receipt = new StartCommandReceipt(100);
const pending = receipt.waitFor("start");
assert.equal(receipt.receive({ type: "point.result", requestId: "other", success: true }), false);
assert.equal(receipt.receive({ type: "point.result", requestId: "start", success: true }), true);
await pending;

const values = new Map();
applyAbsoluteValues(values, {
  "machine.startFeedback": { value: true, quality: "good", updatedAt: timestamp }
});
const devices = [{ deviceId: "device", name: "PLC", address: "127.0.0.1", state: "connected", selected: true, metadata: {} }];
clearTransientRuntime(values, devices);
assert.equal(values.size, 0);
assert.equal(devices.length, 0);

const source = readFileSync(new URL("./hmi.mts", import.meta.url), "utf8");
const index = readFileSync(new URL("../index.html", import.meta.url), "utf8");
const demoShell = readFileSync(new URL("../demo-shell.html", import.meta.url), "utf8");
const keyboardSource = readFileSync(new URL("./soft-keyboard.js", import.meta.url), "utf8");
const keyboardCSS = readFileSync(new URL("./soft-keyboard.css", import.meta.url), "utf8");
assert.match(source, /crypto\.subtle\.digest\("SHA-256"/);
assert.match(source, /localAdminStorageKey = "block-hmi-local-admin-v1"/);
assert.match(source, /localSessionStorageKey = "block-hmi-local-session-v1"/);
assert.match(source, /localSettingsStorageKey = "block-hmi-local-settings-v1"/);
assert.match(source, /this\.prepareGuestHMI\(\);[\s\S]*?this\.openSocket\(\);/);
assert.match(source, /private moveLocalAdministrationToMaintenance\(\): void/);
assert.match(source, /private bindPasswordVisibilityToggles\(\): void/);
assert.match(source, /toggle\.addEventListener\("pointerdown", \(event\) => event\.preventDefault\(\)\)/);
assert.match(source, /input\.type = input\.type === "password" \? "text" : "password";/);
assert.match(source, /window\.addEventListener\("hmi-soft-keyboard-ready", \(\) => this\.openFocusedAuthenticationKeyboard\(\), \{ once: true \}\)/);
assert.match(source, /private openFocusedAuthenticationKeyboard\(\): void \{[\s\S]*?document\.activeElement === input[\s\S]*?this\.openAuthenticationKeyboardInput\(input\)/);
assert.match(source, /private openAuthenticationKeyboardInput\(input: HTMLInputElement\): void \{[\s\S]*?keyboard\.open\(input\)/);
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
assert.doesNotMatch(source, /\/api\/v2\/auth\/status/);
assert.doesNotMatch(source, /jsonRequest\(/);
assert.doesNotMatch(source, /event\.code === 4401/);
assert.match(source, /new WebSocket\(websocketURL\(\)\)/);
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
assert.match(index, /\.auth-sheet \{[\s\S]*?padding: 60px 32px;[\s\S]*?width: min\(744px, 100%\);/);
assert.match(index, /#auth-panel \.auth-form \{[\s\S]*?gap: 22px;/);
assert.match(index, /id="operatorName">登录<\/div>/);
assert.doesNotMatch(index, /<span class="nav-en">/);
assert.doesNotMatch(index, /<div class="meta-en">Operator<\/div>/);
assert.doesNotMatch(index, /id="modeEn"|id="modeState"|远程联机/);
assert.match(index, /#hmi-footer \{[\s\S]*?grid-template-columns: 220px minmax\(0, 1fr\) 220px;/);
assert.match(index, /#hmi-footer #operatorName,[\s\S]*?#hmi-footer \.mode \{[\s\S]*?height: 58px;[\s\S]*?width: 168px;[\s\S]*?border-radius: 18px;/);
assert.match(index, /#hmi-footer #operatorName::before \{[\s\S]*?data:image\/svg\+xml/);
assert.match(index, /#hmi-footer \.mode\.is-auto \{[\s\S]*?color: #176b38;[\s\S]*?background: #e8f7ec;/);
assert.match(index, /#hmi-footer \.mode\.is-manual \{[\s\S]*?color: #8a6200;[\s\S]*?background: #fff5d7;/);
assert.match(index, /modeToggle\.classList\.toggle\("is-auto", state\.mode === "auto"\);[\s\S]*?modeToggle\.classList\.toggle\("is-manual", state\.mode === "manual"\);/);
assert.match(index, /import\("\.\/assets\/hmi\.mjs\?v=20260807\.3"\)/);
assert.match(index, /function requireFrontendPermission\(permission\)/);
assert.match(index, /name === "maintenance" && !requireFrontendPermission\("maintenance"\)/);
assert.match(index, /\.page\[data-page="maintenance"\] \.settings-layout \{[\s\S]*?overflow: hidden;/);
assert.match(index, /\.maintenance-panel \{[\s\S]*?overflow-y: auto;[\s\S]*?overscroll-behavior: contain;/);
assert.match(index, /window\.addEventListener\("block-hmi-guest", \(\) => \{[\s\S]*?switchPage\("home"\)/);
assert.match(index, /if \(!requireFrontendPermission\("operate"\)\) return false;/);
assert.match(index, /async function saveProductionSettings\(manual = false\) \{[\s\S]*?requireFrontendPermission\("maintenance"\)/);
assert.match(index, /data-maintenance-tab="production"[\s\S]*?data-maintenance-tab="wifi"[\s\S]*?data-maintenance-tab="plc"[\s\S]*?data-maintenance-tab="accounts"/);
assert.match(index, /data-maintenance-panel="production"[\s\S]*?data-maintenance-panel="wifi"[\s\S]*?data-maintenance-panel="plc"[\s\S]*?data-maintenance-panel="accounts"/);
assert.match(index, /setTimeout\(\(\) => \{[\s\S]*?saveProductionSettings\(\);[\s\S]*?\}, 650\)/);
assert.match(index, /"\/api\/v2\/maintenance\/production"/);
assert.match(index, /"\/api\/v2\/maintenance\/connectivity"/);
assert.match(index, /"\/api\/v2\/maintenance\/wifi\/connect"/);
assert.doesNotMatch(index, /backend\.updateSettings/);
assert.doesNotMatch(index, /api-client\.js/);
const passwordInputIDs = [...index.matchAll(/<input id="([^"]+)"[^>]*type="password"/g)].map((match) => match[1]);
const passwordToggleIDs = [...index.matchAll(/<button[^>]*aria-controls="([^"]+)"[^>]*data-password-toggle/g)].map((match) => match[1]);
assert.equal(passwordInputIDs.length, 7);
assert.deepEqual(passwordToggleIDs.sort(), passwordInputIDs.sort());
assert.match(index, /class="password-visibility-toggle" type="button"[^>]*aria-label="显示密码"[^>]*aria-pressed="false"/);
assert.match(keyboardCSS, /#auth-panel\[data-keyboard-open="true"\] \{[\s\S]*?--auth-sheet-top-gap:[\s\S]*?padding-bottom: calc\(100vh - var\(--auth-keyboard-top/);
assert.match(keyboardCSS, /#auth-panel\[data-keyboard-open="true"\] \.auth-sheet \{[\s\S]*?max-height: calc\(var\(--auth-keyboard-top[\s\S]*?overflow-y: auto;/);
assert.match(keyboardCSS, /#auth-panel\[data-keyboard-open="true"\] ~ #hmi \.soft-keyboard-foot[\s\S]*?display: none;/);
assert.match(keyboardSource, /dock\.addEventListener\("transitionend", function \(event\) \{[\s\S]*?syncAuthKeyboardLayout\(\);/);
assert.match(keyboardSource, /function notifyReady\(\) \{[\s\S]*?window\.dispatchEvent\(new window\.Event\("hmi-soft-keyboard-ready"\)\)/);
assert.match(keyboardSource, /document\.addEventListener\("keydown", handlePhysicalKeyboard, true\);[\s\S]*?notifyReady\(\);/);
assert.match(keyboardSource, /"1 2 3 4 5 6 7 8 9 0 - = \."/);
assert.match(keyboardSource, /"\{shift\} z x c v b n m \. - \{bksp\}"/);
assert.match(keyboardSource, /var shiftIcon = '<svg[\s\S]*?var backspaceIcon = '<svg/);
assert.match(keyboardSource, /value: "切换大小写"[\s\S]*?value: "退格"/);
assert.match(keyboardSource, /function isAuthenticationInput\(input\) \{[\s\S]*?data-soft-submit/);
assert.match(keyboardSource, /function validateInput\(input, focusOnError\) \{[\s\S]*?isAuthenticationInput\(input\)/);
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
const authPanelProbe = probeElement(rectangle(0, 900), {
  backgroundColor: "rgba(0, 0, 0, 0)", filter: "none", opacity: "1", overflow: "hidden", pointerEvents: "none"
}, { getAttribute: (name) => name === "data-keyboard-open" ? "true" : null });
const authSheetProbe = probeElement(rectangle(16, 386), { overflowY: "auto", pointerEvents: "auto" }, { clientHeight: 370, scrollHeight: 430 });
const keyboardDockProbe = probeElement(rectangle(402, 740, 28, 1572), {}, { visible: true });
const probeElements = new Map([
  ["#auth-panel", authPanelProbe],
  ["#auth-panel .auth-sheet", authSheetProbe],
  ["#softKeyboardDock", keyboardDockProbe],
  ["#hmi", probeElement(rectangle(0, 900), visualStyle)],
  ["#hmi-topbar", probeElement(rectangle(0, 66), visualStyle)],
  ["#hmi-pages", probeElement(rectangle(66, 758), visualStyle)],
  ["#hmi-footer", probeElement(rectangle(758, 900), visualStyle)]
]);
const layoutResult = assertAuthKeyboardLayout({ querySelector: (selector) => probeElements.get(selector) }, {
  innerHeight: 900,
  getComputedStyle: (element) => element.computedStyle
});
assert.equal(layoutResult.sheetTopGap, authKeyboardSafeGap);
assert.equal(layoutResult.sheetKeyboardGap, authKeyboardSafeGap);
