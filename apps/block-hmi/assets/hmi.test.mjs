import assert from "node:assert/strict";
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
  isDisplayPath
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
