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
assert.match(source, /crypto\.subtle\.digest\("SHA-256"/);
assert.match(source, /localAdminStorageKey = "block-hmi-local-admin-v1"/);
assert.match(source, /localSessionStorageKey = "block-hmi-local-session-v1"/);
assert.match(source, /localSettingsStorageKey = "block-hmi-local-settings-v1"/);
assert.match(source, /this\.prepareGuestHMI\(\);[\s\S]*?this\.openSocket\(\);/);
assert.match(source, /private moveLocalAdministrationToMaintenance\(\): void/);
assert.match(source, /private requirePermission\(permission: "operate" \| "maintenance"\): boolean/);
const becomeGuest = source.match(/private becomeGuest\(\): void \{[\s\S]*?\n  \}\n\n  private openSocket/);
assert.notEqual(becomeGuest, null);
assert.match(becomeGuest[0], /new Event\("block-hmi-guest"\)/);
assert.doesNotMatch(becomeGuest[0], /socket\.close\(/);
assert.match(source, /private sendPLCScan\(\): void \{[\s\S]*?requirePermission\("maintenance"\)/);
assert.match(source, /private sendCommand\([\s\S]*?requirePermission\("operate"\)/);
assert.doesNotMatch(source, /\/api\/v2\/auth\/status/);
assert.doesNotMatch(source, /jsonRequest\(/);
assert.doesNotMatch(source, /event\.code === 4401/);
assert.match(source, /new WebSocket\(websocketURL\(\)\)/);
assert.match(source, /buildRuntimeConfigure\(this\.config\.points\)/);
assert.match(index, /window\.BlockHMIReady\.then\(syncFrontendPermissions\)/);
assert.match(index, /function requireFrontendPermission\(permission\)/);
assert.match(index, /name === "maintenance" && !requireFrontendPermission\("maintenance"\)/);
assert.match(index, /\.page\[data-page="maintenance"\] \.settings-layout \{[\s\S]*?overflow-y: auto;[\s\S]*?overscroll-behavior: contain;/);
assert.match(index, /window\.addEventListener\("block-hmi-guest", \(\) => \{[\s\S]*?switchPage\("home"\)/);
assert.match(index, /if \(!requireFrontendPermission\("operate"\)\) return false;/);
assert.match(index, /if \(!requireFrontendPermission\("maintenance"\)\) return;/);
assert.doesNotMatch(index, /api-client\.js/);
assert.equal(existsSync(new URL("./machine-bin.png", import.meta.url)), true);
assert.equal(existsSync(new URL("./soft-keyboard.js", import.meta.url)), true);
