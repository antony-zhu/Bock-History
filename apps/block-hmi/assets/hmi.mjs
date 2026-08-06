const debounceMilliseconds = 50;
const defaultPLCAddressRange = "192.168.1.0/24";
function requestID() {
    return crypto.randomUUID();
}
function request(type, fields, id = requestID(), timestamp = new Date().toISOString()) {
    return {
        protocolVersion: "1.0",
        type,
        requestId: id,
        timestamp,
        ...fields
    };
}
export function isDisplayPath(value) {
    return /^[a-z]+(?:[a-z]*)(?:\.[a-z]+(?:[a-z]*)?)+$/.test(value);
}
export class ActivationFilter {
    lastSignature = "";
    lastTime = Number.NEGATIVE_INFINITY;
    accept(event) {
        if (event.detail === 0) {
            return true;
        }
        const signature = event.type + ":" + String(event.pointerId ?? "mouse") + ":" + String(event.detail);
        const duplicate = signature === this.lastSignature &&
            event.timeStamp >= this.lastTime &&
            event.timeStamp - this.lastTime < debounceMilliseconds;
        this.lastSignature = signature;
        this.lastTime = event.timeStamp;
        return !duplicate;
    }
}
export function buildRuntimeConfigure(points, id = requestID(), timestamp = new Date().toISOString()) {
    return request("runtime.configure", {
        scanIntervalMs: 50,
        points: points.map((point) => ({
            pointId: point.pointId,
            address: point.address,
            type: point.type,
            access: point.access,
            readPoint: point.readPoint,
            writePoint: point.writePoint,
            writeMethod: point.writeMethod,
            write: point.write === undefined ? undefined : {
                mode: point.write.mode,
                activeValue: point.write.activeValue,
                defaultValue: point.write.defaultValue,
                pulseMs: point.write.pulseMs
            }
        }))
    }, id, timestamp);
}
export function buildPointsSnapshotGet(id = requestID(), timestamp = new Date().toISOString()) {
    return request("points.snapshot.get", {}, id, timestamp);
}
export function buildPLCScan(addressRange = defaultPLCAddressRange, id = requestID(), timestamp = new Date().toISOString()) {
    return request("plc.scan", { addressRange }, id, timestamp);
}
export function buildPLCConnect(deviceID, id = requestID(), timestamp = new Date().toISOString()) {
    return request("plc.connect", { deviceId: deviceID }, id, timestamp);
}
export function buildPLCDisconnect(id = requestID(), timestamp = new Date().toISOString()) {
    return request("plc.disconnect", {}, id, timestamp);
}
export function buildPointCommand(pointID, action, id = requestID(), timestamp = new Date().toISOString()) {
    return request("point.command", { pointId: pointID, action }, id, timestamp);
}
export function applyAbsoluteValues(target, values) {
    for (const [pointID, pointValue] of Object.entries(values)) {
        target.set(pointID, { ...pointValue });
    }
}
export function clearTransientRuntime(values, devices) {
    values.clear();
    devices.splice(0, devices.length);
}
function configurationFrom(value) {
    if (typeof value !== "object" || value === null) {
        throw new Error("points.json 必须是对象");
    }
    const config = value;
    if (config.scanIntervalMs !== 50 || !Array.isArray(config.points) || !Array.isArray(config.bindings) || !Array.isArray(config.layout)) {
        throw new Error("points.json 缺少完整点位或页面配置");
    }
    for (const binding of config.bindings) {
        if (!isDisplayPath(binding.displayPath) || !/[\u3400-\u9fff]/.test(binding.description)) {
            throw new Error("bindings 的 displayPath 必须为英文点路径，description 必须为中文");
        }
    }
    return config;
}
function demoConfiguration() {
    return {
        title: "Block 本地控制",
        scanIntervalMs: 50,
        points: [
            {
                pointId: "machine.startCommand",
                address: "D504.1",
                type: "bool",
                access: "read_write",
                readPoint: "machine.startFeedback",
                writePoint: "machine.startCommand",
                writeMethod: "maskWrite",
                write: { mode: "pulse", activeValue: true, defaultValue: false, pulseMs: 100 }
            },
            {
                pointId: "machine.startFeedback",
                address: "D504.2",
                type: "bool",
                access: "read",
                readPoint: "machine.startFeedback",
                writePoint: null,
                writeMethod: null
            },
            {
                pointId: "machine.jogForward",
                address: "D504.3",
                type: "bool",
                access: "read_write",
                readPoint: "machine.jogForward",
                writePoint: "machine.jogForward",
                writeMethod: "maskWrite",
                write: { mode: "momentary", activeValue: true, defaultValue: false }
            },
            {
                pointId: "machine.enabled",
                address: "D504.4",
                type: "bool",
                access: "read_write",
                readPoint: "machine.enabled",
                writePoint: "machine.enabled",
                writeMethod: "maskWrite",
                write: { mode: "toggle", activeValue: true, defaultValue: false }
            }
        ],
        bindings: [
            {
                displayPath: "home.machine.start",
                description: "启动设备",
                component: "button",
                readPoint: "machine.startFeedback",
                writePoint: "machine.startCommand",
                action: "pulse"
            },
            {
                displayPath: "home.machine.jog.forward",
                description: "正向点动",
                component: "button",
                readPoint: "machine.jogForward",
                writePoint: "machine.jogForward",
                action: "momentary"
            },
            {
                displayPath: "home.machine.enabled",
                description: "设备使能",
                component: "button",
                readPoint: "machine.enabled",
                writePoint: "machine.enabled",
                action: "toggle"
            },
            {
                displayPath: "home.machine.start.feedback",
                description: "启动反馈",
                component: "value",
                readPoint: "machine.startFeedback"
            }
        ],
        layout: [
            {
                displayPath: "home.machine.controls",
                description: "设备控制",
                bindings: ["home.machine.start", "home.machine.jog.forward", "home.machine.enabled"]
            },
            {
                displayPath: "home.machine.status",
                description: "设备状态",
                bindings: ["home.machine.start.feedback"]
            }
        ]
    };
}
async function loadConfiguration(demo) {
    if (demo) {
        return demoConfiguration();
    }
    const response = await fetch(new URL("./points.json", import.meta.url), { cache: "no-store" });
    if (!response.ok) {
        throw new Error("无法读取 points.json");
    }
    return configurationFrom(await response.json());
}
async function jsonRequest(method, path, body) {
    const response = await fetch(path, {
        method,
        credentials: "same-origin",
        cache: "no-store",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body)
    });
    if (!response.ok) {
        throw new HMIAPIError(await responseMessage(response), response.status, "request_failed");
    }
    return response;
}
async function responseMessage(response) {
    try {
        const body = await response.json();
        if (typeof body.error === "string") {
            return body.error;
        }
        return body.error?.message ?? "请求失败（HTTP " + String(response.status) + "）";
    }
    catch {
        return "请求失败（HTTP " + String(response.status) + "）";
    }
}
function websocketURL() {
    const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
    return scheme + "//" + window.location.host + "/ws";
}
function isDemoMode() {
    return new URLSearchParams(window.location.search).get("demo") === "1";
}
function cloneState(state) {
    return JSON.parse(JSON.stringify(state));
}
function initialDemoState() {
    return {
        revision: 0,
        running: true,
        mode: "auto",
        singlePaused: false,
        framePaused: false,
        target: 30,
        output: 30,
        cycle: 30,
        oee: 92,
        inspected: 30,
        passed: 30,
        ng: 0,
        pending: 30,
        blank: 30,
        finished: 30,
        toolLimit: 100,
        inspectInterval: 30,
        bins: [
            { type: "full", label: "满料" },
            { type: "warning", label: "需换料" },
            { type: "fault", label: "异常" }
        ],
        alarms: [
            { id: 3, level: "danger", code: "0003", text: "库位3定位异常", time: "09:42:18", acknowledged: false },
            { id: 2, level: "warning", code: "0002", text: "库位2余量不足", time: "09:40:06", acknowledged: false },
            { id: 1, level: "info", code: "0001", text: "系统自检完成", time: "09:35:12", acknowledged: false }
        ],
        history: [
            { id: 3, level: "danger", code: "0003", text: "库位3定位异常", time: "2026-07-19 18:42:18" },
            { id: 2, level: "warning", code: "0002", text: "X按钮急停解除", time: "2026-07-19 17:26:08" },
            { id: 1, level: "info", code: "0001", text: "完成抽检", time: "2026-07-19 16:55:31" }
        ]
    };
}
class HMIAPIError extends Error {
    status;
    code;
    constructor(message, status = 0, code = "request_failed") {
        super(message);
        this.name = "APIError";
        this.status = status;
        this.code = code;
    }
}
export const startCommandResultTimeoutMilliseconds = 5000;
// The V2 HMI currently exposes exactly one real operation: the Start pulse.
// This keeps its one request/result pair explicit rather than introducing a command queue.
export class StartCommandReceipt {
    timeoutMilliseconds;
    pending = null;
    constructor(timeoutMilliseconds = startCommandResultTimeoutMilliseconds) {
        this.timeoutMilliseconds = timeoutMilliseconds;
    }
    waitFor(requestID) {
        if (this.pending !== null) {
            return Promise.reject(new HMIAPIError("启动命令仍在等待 PLC 结果", 409, "command_pending"));
        }
        return new Promise((resolve, reject) => {
            const timeout = setTimeout(() => {
                if (this.pending?.requestID === requestID) {
                    this.cancel("未收到 PLC 执行结果，结果未知", 504, "timeout");
                }
            }, this.timeoutMilliseconds);
            this.pending = { requestID, timeout, resolve, reject };
        });
    }
    receive(message) {
        const pending = this.pending;
        if (pending === null || message.type !== "point.result" || message.requestId !== pending.requestID) {
            return false;
        }
        clearTimeout(pending.timeout);
        this.pending = null;
        if (message.success === true) {
            pending.resolve();
        }
        else {
            const code = message.error?.code ?? "point_command_failed";
            pending.reject(new HMIAPIError(message.error?.message ?? errorText(code), 502, code));
        }
        return true;
    }
    cancel(message, status, code) {
        const pending = this.pending;
        if (pending === null) {
            return;
        }
        clearTimeout(pending.timeout);
        this.pending = null;
        pending.reject(new HMIAPIError(message, status, code));
    }
}
class AppleBridge {
    config;
    demo;
    socket = null;
    signedIn = false;
    configured = false;
    reconnectDelay = 1000;
    reconnectTimer = null;
    lastActivityAt = Number.NEGATIVE_INFINITY;
    revision = 0;
    plcState = "disconnected";
    values = new Map();
    plcDevices = [];
    pendingStartCommand = new StartCommandReceipt();
    demoState = initialDemoState();
    constructor(config, demo) {
        this.config = config;
        this.demo = demo;
    }
    start() {
        this.bindAuthForms();
        this.bindAccountControls();
        this.bindPLCControls();
        this.bindActivityReporting();
        if (this.demo) {
            this.signedIn = true;
            this.configured = true;
            this.authPanel().hidden = true;
            this.setPLCStatus("演示模式（未连接 PLC）");
            this.renderPLCCandidates();
            return;
        }
        this.showLogin();
        this.setPLCStatus("请登录后连接本机服务");
        this.renderPLCCandidates();
    }
    backend() {
        return {
            APIError: HMIAPIError,
            getState: () => this.getState(),
            updateSettings: (settings) => this.updateSettings(settings),
            sendCommand: (command, payload) => this.sendCommand(command, payload),
            acknowledgeAlarm: (alarmID) => this.acknowledgeAlarm(alarmID),
            getAudit: () => Promise.resolve({ events: cloneState(this.currentState()).history })
        };
    }
    authPanel() {
        return document.querySelector("#auth-panel");
    }
    authNotice() {
        return document.querySelector("#authNotice");
    }
    loginSection() {
        return document.querySelector("#authLogin");
    }
    accountSection() {
        return document.querySelector("#authAccount");
    }
    setAuthNotice(message) {
        this.authNotice().textContent = message;
    }
    showLogin(message = "") {
        this.authPanel().hidden = false;
        this.loginSection().hidden = false;
        this.accountSection().hidden = true;
        this.setAuthNotice(message);
    }
    showAccount() {
        if (!this.signedIn) {
            this.showLogin();
            return;
        }
        this.authPanel().hidden = false;
        this.loginSection().hidden = true;
        this.accountSection().hidden = false;
        this.setAuthNotice("");
        this.renderPLCCandidates();
    }
    hideAccount() {
        if (this.signedIn) {
            this.authPanel().hidden = true;
        }
    }
    bindAuthForms() {
        const login = document.querySelector("#login-form");
        login.addEventListener("submit", (event) => {
            event.preventDefault();
            void this.login(formValue(login, "username"), formValue(login, "password"));
        });
        const initialAdmin = document.querySelector("#initial-admin-form");
        initialAdmin.addEventListener("submit", (event) => {
            event.preventDefault();
            void this.createInitialAdmin(formValue(initialAdmin, "username"), formValue(initialAdmin, "password"), formValue(initialAdmin, "confirmPassword"));
        });
        const password = document.querySelector("#password-form");
        password.addEventListener("submit", (event) => {
            event.preventDefault();
            void this.changePassword(formValue(password, "currentPassword"), formValue(password, "newPassword"), formValue(password, "confirmPassword"));
        });
        const policy = document.querySelector("#session-policy-form");
        policy.addEventListener("submit", (event) => {
            event.preventDefault();
            void this.saveSessionPolicy(Number(formValue(policy, "idleTimeoutSeconds")));
        });
    }
    bindAccountControls() {
        const operator = document.querySelector("#operatorName");
        operator.tabIndex = 0;
        operator.setAttribute("role", "button");
        operator.setAttribute("aria-label", "打开本机账户和 PLC 连接");
        operator.addEventListener("click", () => this.showAccount());
        operator.addEventListener("keydown", (event) => {
            if (event.key === "Enter" || event.key === " ") {
                event.preventDefault();
                this.showAccount();
            }
        });
        document.querySelector("#auth-close").addEventListener("click", () => this.hideAccount());
        document.querySelector("#logout-button").addEventListener("click", () => {
            void this.logout();
        });
    }
    bindPLCControls() {
        document.querySelector("#plc-scan-button").addEventListener("click", () => {
            this.sendPLCScan();
        });
        document.querySelector("#plc-disconnect-button").addEventListener("click", () => {
            this.sendPLCDisconnect();
        });
        document.querySelector("#snapshot-button").addEventListener("click", () => {
            this.sendPointsSnapshotGet();
        });
    }
    bindActivityReporting() {
        const report = () => {
            if (this.demo || !this.signedIn) {
                return;
            }
            const now = performance.now();
            if (now - this.lastActivityAt < 500) {
                return;
            }
            this.lastActivityAt = now;
            void fetch("/api/v2/auth/activity", {
                method: "POST",
                credentials: "same-origin",
                cache: "no-store"
            }).then((response) => {
                if (response.status === 401) {
                    this.endSession("会话已过期，请重新登录");
                }
            }).catch(() => undefined);
        };
        document.addEventListener("pointerdown", report, { passive: true });
        document.addEventListener("touchstart", report, { passive: true });
        document.addEventListener("keydown", report);
    }
    async login(username, password) {
        if (this.demo) {
            this.beginSession();
            return;
        }
        try {
            await jsonRequest("POST", "/api/v2/auth/login", { username, password });
            this.beginSession();
        }
        catch (error) {
            this.setAuthNotice(error instanceof Error ? error.message : "登录失败");
        }
    }
    async createInitialAdmin(username, password, confirmPassword) {
        if (password !== confirmPassword) {
            this.setAuthNotice("两次输入的密码不一致");
            return;
        }
        if (this.demo) {
            this.beginSession();
            return;
        }
        try {
            await jsonRequest("POST", "/api/v2/auth/initial-admin", { username, password, confirmPassword });
            this.beginSession();
        }
        catch (error) {
            this.setAuthNotice(error instanceof Error ? error.message : "创建管理员失败");
        }
    }
    async changePassword(currentPassword, newPassword, confirmPassword) {
        if (newPassword !== confirmPassword) {
            this.setAuthNotice("两次输入的新密码不一致");
            return;
        }
        if (this.demo) {
            this.setAuthNotice("演示模式未修改密码");
            return;
        }
        try {
            await jsonRequest("POST", "/api/v2/auth/password", { currentPassword, newPassword, confirmPassword });
            this.setAuthNotice("密码已修改");
        }
        catch (error) {
            this.setAuthNotice(error instanceof Error ? error.message : "修改密码失败");
        }
    }
    async saveSessionPolicy(idleTimeoutSeconds) {
        if (!Number.isInteger(idleTimeoutSeconds) || idleTimeoutSeconds < 60) {
            this.setAuthNotice("不活动退出时长至少为 60 秒");
            return;
        }
        if (this.demo) {
            this.setAuthNotice("演示模式已保留会话时长设置");
            return;
        }
        try {
            await jsonRequest("PUT", "/api/v2/config/session", { idleTimeoutSeconds });
            this.setAuthNotice("会话时长已保存");
        }
        catch (error) {
            this.setAuthNotice(error instanceof Error ? error.message : "保存会话时长失败");
        }
    }
    beginSession() {
        this.signedIn = true;
        this.authPanel().hidden = true;
        this.setAuthNotice("");
        if (this.demo) {
            this.configured = true;
            this.emitState();
            return;
        }
        this.openSocket();
    }
    async logout() {
        if (!this.demo) {
            this.pendingStartCommand.cancel("已退出登录，启动结果未知", 401, "unauthenticated");
            try {
                await fetch("/api/v2/auth/logout", {
                    method: "POST",
                    credentials: "same-origin",
                    cache: "no-store"
                });
            }
            finally {
                this.endSession("已退出登录");
            }
            return;
        }
        this.hideAccount();
    }
    endSession(message) {
        this.signedIn = false;
        this.configured = false;
        this.closeSocket();
        clearTransientRuntime(this.values, this.plcDevices);
        this.plcState = "disconnected";
        this.renderPLCCandidates();
        this.showLogin(message);
        this.deferProductionPolicy();
    }
    openSocket() {
        if (!this.signedIn || this.socket !== null) {
            return;
        }
        const socket = new WebSocket(websocketURL());
        this.socket = socket;
        this.setPLCStatus("正在连接本机服务");
        this.renderPLCCandidates();
        socket.addEventListener("open", () => {
            if (this.socket !== socket) {
                return;
            }
            this.reconnectDelay = 1000;
            this.configured = false;
            socket.send(JSON.stringify(buildRuntimeConfigure(this.config.points)));
            this.setPLCStatus("正在同步点位表");
            this.renderPLCCandidates();
        });
        socket.addEventListener("message", (event) => {
            this.handleSocketMessage(event.data);
        });
        socket.addEventListener("close", (event) => {
            if (this.socket !== socket) {
                return;
            }
            this.pendingStartCommand.cancel("本机服务连接中断，启动结果未知", 503, "network_error");
            this.socket = null;
            this.configured = false;
            clearTransientRuntime(this.values, this.plcDevices);
            this.plcState = "disconnected";
            this.renderPLCCandidates();
            if (event.code === 4401) {
                this.endSession("会话已过期，请重新登录");
                return;
            }
            this.setPLCStatus("本机服务连接中断");
            this.deferProductionPolicy();
            this.scheduleReconnect();
        });
    }
    closeSocket() {
        if (this.reconnectTimer !== null) {
            window.clearTimeout(this.reconnectTimer);
            this.reconnectTimer = null;
        }
        this.pendingStartCommand.cancel("本机服务连接已关闭，启动结果未知", 503, "network_error");
        const socket = this.socket;
        this.socket = null;
        socket?.close();
    }
    scheduleReconnect() {
        if (!this.signedIn || this.reconnectTimer !== null) {
            return;
        }
        const delay = this.reconnectDelay;
        this.reconnectDelay = Math.min(this.reconnectDelay * 2, 30000);
        this.reconnectTimer = window.setTimeout(() => {
            this.reconnectTimer = null;
            this.openSocket();
        }, delay);
    }
    handleSocketMessage(raw) {
        if (typeof raw !== "string") {
            return;
        }
        let message;
        try {
            message = JSON.parse(raw);
        }
        catch {
            this.setAuthNotice("收到无法识别的本机服务消息");
            return;
        }
        if (this.pendingStartCommand.receive(message)) {
            return;
        }
        if (message.type === "runtime.configured") {
            this.configured = true;
            this.setPLCStatus("点位表已同步，等待 PLC 读取");
            this.renderPLCCandidates();
            this.emitState();
            return;
        }
        if (message.type === "points.snapshot" && message.values !== undefined) {
            this.values.clear();
            applyAbsoluteValues(this.values, message.values);
            this.revision += 1;
            this.setPLCStatus("已接收 PLC 当前状态");
            this.emitState();
            return;
        }
        if (message.type === "points.changed" && message.values !== undefined) {
            applyAbsoluteValues(this.values, message.values);
            this.revision += 1;
            this.emitState();
            return;
        }
        if (message.type === "plc.scan.result" && message.success === true && Array.isArray(message.devices)) {
            this.plcDevices.splice(0, this.plcDevices.length, ...message.devices);
            this.setPLCStatus("发现 " + String(this.plcDevices.length) + " 个 PLC 候选设备");
            this.renderPLCCandidates();
            return;
        }
        if (message.type === "plc.connection.changed" && message.state !== undefined) {
            this.plcState = message.state;
            if (message.deviceId !== undefined) {
                for (const device of this.plcDevices) {
                    if (device.deviceId === message.deviceId) {
                        device.state = message.state;
                        device.selected = message.state === "connected";
                    }
                }
            }
            if (message.state === "disconnected") {
                this.plcDevices.splice(0, this.plcDevices.length);
            }
            this.setPLCStatus(plcStateText(message.state, message.deviceId));
            this.renderPLCCandidates();
            return;
        }
        if (message.success === false) {
            this.setAuthNotice(errorText(message.error?.code));
        }
    }
    getState() {
        if (!this.demo && !this.signedIn) {
            return Promise.reject(new HMIAPIError("请登录后连接设备", 401, "unauthenticated"));
        }
        if (!this.demo && !this.canSendRuntime()) {
            return Promise.reject(new HMIAPIError("本机服务正在连接", 503, "runtime_unavailable"));
        }
        if (!this.demo) {
            this.deferProductionPolicy();
        }
        return Promise.resolve({ state: cloneState(this.currentState()) });
    }
    currentState() {
        if (this.demo) {
            return this.demoState;
        }
        const state = initialDemoState();
        const startFeedback = this.valueFor("home.machine.start.feedback");
        const enabled = this.valueFor("home.machine.enabled");
        state.revision = this.revision;
        state.running = startFeedback === true;
        state.mode = enabled === true ? "auto" : "manual";
        state.target = 0;
        state.output = 0;
        state.cycle = 0;
        state.oee = 0;
        state.inspected = 0;
        state.passed = 0;
        state.ng = 0;
        state.pending = 0;
        state.blank = 0;
        state.finished = 0;
        state.toolLimit = 0;
        state.inspectInterval = 0;
        state.bins = [
            { type: "empty", label: "暂无数据" },
            { type: "empty", label: "暂无数据" },
            { type: "empty", label: "暂无数据" }
        ];
        state.alarms = [
            { id: 1, level: "info", code: "V2", text: "当前 v2 未提供报警历史", time: "--", acknowledged: true }
        ];
        state.history = [
            { id: 1, level: "info", code: "V2", text: "当前 v2 未提供历史记录", time: "--" }
        ];
        return state;
    }
    valueFor(displayPath) {
        const binding = this.config.bindings.find((item) => item.displayPath === displayPath);
        if (binding === undefined) {
            return undefined;
        }
        return this.values.get(binding.readPoint)?.value;
    }
    updateSettings(settings) {
        if (!this.demo) {
            return Promise.reject(new HMIAPIError("当前 v2 未提供维护参数写入", 501, "not_supported"));
        }
        const state = cloneState(this.demoState);
        state.target = positiveInteger(settings.target, state.target);
        state.toolLimit = positiveInteger(settings.toolLimit, state.toolLimit);
        state.inspectInterval = positiveInteger(settings.inspectInterval, state.inspectInterval);
        state.revision += 1;
        this.demoState = state;
        this.emitState();
        return Promise.resolve({ state: cloneState(state) });
    }
    sendCommand(command, payload = {}) {
        if (this.demo) {
            return Promise.resolve({ state: this.applyDemoCommand(command, payload) });
        }
        if (command !== "start") {
            return Promise.reject(new HMIAPIError("当前 v2 未提供此操作", 501, "not_supported"));
        }
        const binding = this.config.bindings.find((item) => item.displayPath === "home.machine.start");
        if (binding?.writePoint === undefined || binding.writePoint === null || binding.action !== "pulse") {
            return Promise.reject(new HMIAPIError("points.json 未配置启动点位", 500, "point_not_configured"));
        }
        if (!this.canSendRuntime()) {
            return Promise.reject(new HMIAPIError("PLC 尚未连接", 503, "plc_not_connected"));
        }
        const requestId = requestID();
        const confirmation = this.pendingStartCommand.waitFor(requestId);
        try {
            this.socket.send(JSON.stringify(buildPointCommand(binding.writePoint, binding.action, requestId)));
        }
        catch {
            this.pendingStartCommand.cancel("启动命令未发送，结果未知", 503, "network_error");
        }
        return confirmation.then(() => ({ state: cloneState(this.currentState()) }));
    }
    acknowledgeAlarm(alarmID) {
        if (!this.demo) {
            return Promise.reject(new HMIAPIError("当前 v2 不支持报警确认", 501, "not_supported"));
        }
        const state = cloneState(this.demoState);
        const alarm = state.alarms.find((item) => item.id === alarmID);
        if (alarm !== undefined) {
            alarm.acknowledged = true;
            state.revision += 1;
            this.demoState = state;
            this.emitState();
        }
        return Promise.resolve({ state: cloneState(state) });
    }
    applyDemoCommand(command, payload) {
        const state = cloneState(this.demoState);
        if (command === "start") {
            state.running = true;
        }
        else if (command === "pause") {
            state.running = false;
        }
        else if (command === "reset") {
            state.output = 0;
            state.cycle = 0;
        }
        else if (command === "inspect") {
            state.inspected += 1;
            state.passed += 1;
        }
        else if (command === "clear_bins") {
            state.bins = state.bins.map(() => ({ type: "empty", label: "已清空" }));
        }
        else if (command === "set_single_paused") {
            state.singlePaused = Boolean(payload.paused);
        }
        else if (command === "set_frame_paused") {
            state.framePaused = Boolean(payload.paused);
        }
        else if (command === "set_mode") {
            state.mode = payload.mode === "manual" ? "manual" : "auto";
        }
        state.revision += 1;
        this.demoState = state;
        this.emitState();
        return cloneState(state);
    }
    sendPLCScan() {
        if (this.demo) {
            this.setPLCStatus("演示模式不扫描 PLC");
            return;
        }
        this.sendRuntimeRequest(buildPLCScan());
    }
    sendPLCConnect(deviceID) {
        if (this.demo) {
            return;
        }
        this.sendRuntimeRequest(buildPLCConnect(deviceID));
    }
    sendPLCDisconnect() {
        if (this.demo) {
            return;
        }
        this.sendRuntimeRequest(buildPLCDisconnect());
    }
    sendPointsSnapshotGet() {
        if (this.demo) {
            this.emitState();
            return;
        }
        this.sendRuntimeRequest(buildPointsSnapshotGet());
    }
    sendRuntimeRequest(message) {
        if (!this.canSendRuntime() || this.socket === null) {
            return;
        }
        this.socket.send(JSON.stringify(message));
    }
    canSendRuntime() {
        return this.configured && (this.demo || this.socket?.readyState === WebSocket.OPEN);
    }
    setPLCStatus(message) {
        document.querySelector("#plc-status").textContent = message;
    }
    renderPLCCandidates() {
        const list = document.querySelector("#plc-candidates");
        const scan = document.querySelector("#plc-scan-button");
        const disconnect = document.querySelector("#plc-disconnect-button");
        const snapshot = document.querySelector("#snapshot-button");
        const active = this.canSendRuntime();
        scan.disabled = !active;
        snapshot.disabled = !active;
        disconnect.disabled = !active || this.plcState === "disconnected";
        list.replaceChildren();
        if (this.demo) {
            const item = document.createElement("li");
            item.textContent = "演示模式不需要 PLC";
            list.append(item);
            return;
        }
        if (this.plcDevices.length === 0) {
            const item = document.createElement("li");
            item.textContent = "尚未扫描 PLC";
            list.append(item);
            return;
        }
        for (const device of this.plcDevices) {
            const item = document.createElement("li");
            const detail = document.createElement("span");
            detail.textContent = device.name + " · " + device.address + " · " + plcStateText(device.state);
            const connect = document.createElement("button");
            connect.type = "button";
            connect.textContent = "连接";
            connect.disabled = !active || device.state === "connected" || device.state === "connecting";
            connect.addEventListener("click", () => this.sendPLCConnect(device.deviceId));
            item.append(detail, document.createTextNode(" "), connect);
            list.append(item);
        }
    }
    emitState() {
        window.dispatchEvent(new Event("block-hmi-state"));
    }
    deferProductionPolicy() {
        if (this.demo) {
            return;
        }
        window.setTimeout(() => {
            const start = document.querySelector('[data-action="start"]');
            const startEnabled = this.canSendRuntime();
            document.querySelectorAll(".control-button").forEach((button) => {
                button.disabled = button !== start || !startEnabled;
            });
            const mode = document.querySelector("#modeToggle");
            if (mode !== null) {
                mode.disabled = true;
            }
            const save = document.querySelector(".save-button");
            if (save !== null) {
                save.disabled = true;
            }
            document.querySelectorAll(".ack-button").forEach((button) => {
                button.disabled = true;
            });
            const gap = document.querySelector("#v2-data-gap");
            if (gap !== null) {
                gap.hidden = false;
            }
        }, 0);
    }
}
function formValue(form, name) {
    return String(new FormData(form).get(name) ?? "");
}
function positiveInteger(value, fallback) {
    const numberValue = Number(value);
    return Number.isInteger(numberValue) && numberValue > 0 ? numberValue : fallback;
}
function plcStateText(state, deviceID) {
    const text = {
        disconnected: "PLC 未连接",
        connecting: "正在连接 PLC",
        connected: "PLC 已连接",
        reconnecting: "PLC 正在重连",
        error: "PLC 连接错误"
    };
    return text[state] + (deviceID === undefined ? "" : "：" + deviceID);
}
function errorText(code) {
    const messages = {
        BUSY: "PLC 扫描正在进行，请稍后再试",
        PLC_NOT_CONNECTED: "PLC 尚未连接",
        PLC_READ_FAILED: "PLC 读取失败",
        PLC_WRITE_FAILED: "PLC 写入失败",
        POINT_NOT_FOUND: "点位不存在",
        POINT_NOT_WRITABLE: "点位不可写",
        TIMEOUT: "PLC 请求超时",
        INVALID_REQUEST: "PLC 请求无效",
        INTERNAL_ERROR: "PLC 服务内部错误"
    };
    return code === undefined ? "PLC 请求失败" : messages[code] ?? "PLC 请求失败：" + code;
}
export async function installHMIBackend() {
    const demo = isDemoMode();
    const bridge = new AppleBridge(await loadConfiguration(demo), demo);
    bridge.start();
    const backend = bridge.backend();
    window.HMIBackend = backend;
    return backend;
}
