import assert from "node:assert/strict";
import { ActivationFilter, applyAbsoluteValues, buildRuntimeConfigure, isDisplayPath } from "./hmi.mjs";

assert.equal(isDisplayPath("home.machine.start"), true);
assert.equal(isDisplayPath("home.设备.start"), false);
assert.equal(isDisplayPath("Home.machine.start"), false);

const filter = new ActivationFilter();
assert.equal(filter.accept({ type: "click", pointerId: 1, detail: 1, timeStamp: 100 }), true);
assert.equal(filter.accept({ type: "click", pointerId: 1, detail: 1, timeStamp: 120 }), false);
assert.equal(filter.accept({ type: "click", pointerId: 1, detail: 2, timeStamp: 130 }), true);

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
}]);
assert.deepEqual(configure, {
  type: "runtime.configure",
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
