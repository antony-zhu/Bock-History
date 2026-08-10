const debounceMilliseconds = 50;
const defaultPLCAddressRange = "192.168.1.0/24";
const defaultPLCPort = 502;
const defaultPLCUnitID = 1;
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
function isIPv4Address(value) {
    const parts = value.split(".");
    return parts.length === 4 && parts.every((part) => {
        if (!/^\d+$/.test(part)) {
            return false;
        }
        const number = Number(part);
        return Number.isInteger(number) && number >= 0 && number <= 255;
    });
}
function isIPv4CIDR(value) {
    const [address, bits, ...extra] = value.split("/");
    return extra.length === 0 && isIPv4Address(address ?? "") && /^\d+$/.test(bits ?? "") && Number(bits) >= 0 && Number(bits) <= 32;
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
        scanIntervalMs: 500,
        points: points.map((point) => ({
            pointId: point.pointId,
            address: point.address,
            type: point.type,
            access: point.access,
            readPoint: point.readPoint,
            writePoint: point.writePoint,
            writeMethod: point.writeMethod,
            registerCount: point.registerCount,
            wordOrder: point.wordOrder,
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
export function buildPLCScan(addressRange = defaultPLCAddressRange, port = defaultPLCPort, unitID = defaultPLCUnitID, id = requestID(), timestamp = new Date().toISOString()) {
    return request("plc.scan", { addressRange, port, unitId: unitID }, id, timestamp);
}
export function buildPLCDeviceID(host, port = defaultPLCPort, unitID = defaultPLCUnitID) {
    return "easy521://" + host + ":" + String(port) + "?unitId=" + String(unitID);
}
export function buildPLCConnect(deviceID, id = requestID(), timestamp = new Date().toISOString()) {
    return request("plc.connect", { deviceId: deviceID }, id, timestamp);
}
export function buildPLCDisconnect(id = requestID(), timestamp = new Date().toISOString()) {
    return request("plc.disconnect", {}, id, timestamp);
}
export function buildPointCommand(pointID, action, id = requestID(), timestamp = new Date().toISOString(), value) {
    return request("point.command", {
        pointId: pointID,
        action,
        ...(action === "set" ? { value } : {})
    }, id, timestamp);
}
export function applyAbsoluteValues(target, values) {
    for (const [pointID, pointValue] of Object.entries(values)) {
        target.set(pointID, { ...pointValue });
    }
}
function latestPointTime(values) {
    let latest = null;
    for (const point of Object.values(values)) {
        if (typeof point.updatedAt === "string" && (latest === null || point.updatedAt > latest)) {
            latest = point.updatedAt;
        }
    }
    return latest;
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
    if (config.scanIntervalMs !== 500 || !Array.isArray(config.points) || !Array.isArray(config.bindings) || !Array.isArray(config.layout)) {
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
        scanIntervalMs: 500,
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
    try {
        const response = await fetch(new URL("./points.json", import.meta.url), { cache: "no-store" });
        if (!response.ok) {
            throw new Error("无法读取 points.json");
        }
        return configurationFrom(await response.json());
    }
    catch (error) {
        if (demo) {
            return demoConfiguration();
        }
        throw error;
    }
}
function websocketURL() {
    if (window.location.protocol !== "https:") {
        throw new Error("Block HMI requires HTTPS before opening WSS");
    }
    return "wss://" + window.location.host + "/ws";
}
function isDemoMode() {
    return new URLSearchParams(window.location.search).get("demo") === "1";
}
export const defaultIdleTimeoutSeconds = 300;
function isRecord(value) {
    return typeof value === "object" && value !== null;
}
function validPermissions(value) {
    return isRecord(value) && typeof value.operate === "boolean" && typeof value.maintenance === "boolean";
}
function backendIdentityFrom(value) {
    if (!isRecord(value) ||
        typeof value.username !== "string" || value.username.trim() === "" ||
        (value.role !== "VIEWER" && value.role !== "OPERATOR" && value.role !== "ADMIN") ||
        !validPermissions(value.permissions)) {
        return null;
    }
    return {
        username: value.username,
        role: value.role,
        permissions: { ...value.permissions }
    };
}
function idleTimeoutFrom(value) {
    if (!isRecord(value) || !Number.isInteger(value.idleTimeoutSeconds)) {
        return null;
    }
    const timeout = Number(value.idleTimeoutSeconds);
    return timeout >= 60 && timeout <= 3600 ? timeout : null;
}
export function frontendSessionIsActive(session, now = Date.now()) {
    return session !== null && session.expiresAt > now;
}
export function renewFrontendSession(session, idleTimeoutSeconds, now = Date.now()) {
    if (session === null || !frontendSessionIsActive(session, now)) {
        return null;
    }
    return { ...session, expiresAt: now + idleTimeoutSeconds * 1000 };
}
export function demoAuthPreviewFromSearch(search) {
    const query = new URLSearchParams(search);
    if (query.get("demo") !== "1") {
        return null;
    }
    const auth = query.get("auth");
    return auth === "login" || auth === "bootstrap" ? auth : null;
}
export function demoManualRoleFromSearch(search) {
    const query = new URLSearchParams(search);
    if (query.get("demo") !== "1") {
        return "GUEST";
    }
    const role = query.get("manualRole");
    if (role === "operator") {
        return "OPERATOR";
    }
    if (role === "admin") {
        return "ADMIN";
    }
    return "GUEST";
}
function demoAuthPreviewMode() {
    return demoAuthPreviewFromSearch(window.location.search);
}
function demoManualRole() {
    return demoManualRoleFromSearch(window.location.search);
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
export const pointCommandResultTimeoutMilliseconds = 5000;
// Keep one point command/result pair explicit instead of introducing a command queue.
export class PointCommandReceipt {
    timeoutMilliseconds;
    pending = null;
    constructor(timeoutMilliseconds = pointCommandResultTimeoutMilliseconds) {
        this.timeoutMilliseconds = timeoutMilliseconds;
    }
    waitFor(requestID) {
        if (this.pending !== null) {
            throw new HMIAPIError("现场操作仍在等待 PLC 结果", 409, "command_pending");
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
    dispatch(requestID, send) {
        const confirmation = this.waitFor(requestID);
        try {
            send();
        }
        catch {
            this.cancel("现场操作未发送，结果未知", 503, "network_error");
        }
        return confirmation;
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
    authPreview;
    manualRole;
    socket = null;
    signedIn = false;
    session = null;
    bootstrapRequired = false;
    loginInFlight = false;
    initialAdminInFlight = false;
    idleTimeoutSeconds = defaultIdleTimeoutSeconds;
    configured = false;
    reconnectDelay = 1000;
    reconnectTimer = null;
    sessionExpiryTimer = null;
    revision = 0;
    plcState = "disconnected";
    lastPLCSampleAt = null;
    lastPLCError = "";
    deferredLiveState = false;
    values = new Map();
    plcDevices = [];
    pendingPointCommand = new PointCommandReceipt();
    demoState = initialDemoState();
    authKeyboardOriginalMode = null;
    constructor(config, demo, authPreview, manualRole) {
        this.config = config;
        this.demo = demo;
        this.authPreview = authPreview;
        this.manualRole = manualRole;
    }
    async start() {
        window.HMIFrontendAuth = {
            hasPermission: (permission) => this.hasPermission(permission),
            requirePermission: (permission) => this.requirePermission(permission),
            permissions: () => ({
                operate: this.hasPermission("operate"),
                maintenance: this.hasPermission("maintenance")
            }),
            role: () => this.frontendRole()
        };
        window.addEventListener("block-hmi-public-navigation", () => {
            if (!this.authPanel().hidden) {
                this.becomeGuest();
            }
        });
        this.moveLocalAdministrationToMaintenance();
        this.bindAuthForms();
        this.bindPasswordVisibilityToggles();
        this.bindAccountControls();
        this.bindPLCControls();
        this.bindActivityReporting();
        window.addEventListener("hmi-soft-keyboard-statechange", () => this.flushDeferredLiveState());
        document.addEventListener("focusout", () => {
            window.setTimeout(() => this.flushDeferredLiveState(), 0);
        });
        this.prepareGuestHMI();
        await this.loadAuthenticationState();
        if (this.demo && this.manualRole !== "GUEST") {
            this.beginSession({
                username: this.manualRole === "ADMIN" ? "admin" : "operator",
                role: this.manualRole,
                permissions: {
                    operate: true,
                    maintenance: this.manualRole === "ADMIN"
                }
            });
        }
        if (this.demo) {
            this.configured = true;
            this.setPLCStatus("演示模式（未连接 PLC）");
            this.renderPLCCandidates();
            this.emitState();
        }
        else {
            this.setPLCStatus("正在连接本机服务");
            this.renderPLCCandidates();
            this.openSocket();
        }
        if (this.authPreview !== null) {
            this.openAuthWithKeyboard(this.authPreview === "bootstrap" ? "bootstrap" : this.authenticationScreen());
        }
    }
    backend() {
        return {
            APIError: HMIAPIError,
            getState: () => this.getState(),
            sendCommand: (command, payload) => this.sendCommand(command, payload),
            acknowledgeAlarm: (alarmID) => this.acknowledgeAlarm(alarmID),
            getAudit: () => Promise.resolve({ events: cloneState(this.currentState()).history }),
            manual: {
                binding: (displayPath) => this.manualBinding(displayPath),
                value: (displayPath) => this.manualValue(displayPath),
                canWrite: (displayPath) => this.manualCanWrite(displayPath),
                command: (displayPath, value) => this.manualCommand(displayPath, value)
            },
            points: {
                binding: (displayPath) => this.pointBinding(displayPath),
                value: (displayPath) => this.pointValue(displayPath),
                canWrite: (displayPath) => this.pointCanWrite(displayPath),
                command: (displayPath, value) => this.pointCommand(displayPath, value)
            }
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
    bootstrapSection() {
        return document.querySelector("#authBootstrap");
    }
    authenticationScreen() {
        return this.bootstrapRequired ? "bootstrap" : "login";
    }
    async authRequest(path, method, body) {
        const response = await fetch(path, {
            method,
            cache: "no-store",
            headers: body === undefined ? undefined : { "Content-Type": "application/json" },
            body: body === undefined ? undefined : JSON.stringify(body)
        });
        const value = response.status === 204 ? null : await response.json().catch(() => null);
        return { response, value };
    }
    async loadAuthenticationState() {
        try {
            const [bootstrap, policy] = await Promise.all([
                this.authRequest("/api/auth/initial-admin", "GET"),
                this.authRequest("/api/config/session", "GET")
            ]);
            const idleTimeoutSeconds = idleTimeoutFrom(policy.value);
            if (!bootstrap.response.ok || !policy.response.ok || !isRecord(bootstrap.value) ||
                typeof bootstrap.value.bootstrapRequired !== "boolean" || idleTimeoutSeconds === null) {
                throw new Error("invalid local authentication response");
            }
            this.bootstrapRequired = bootstrap.value.bootstrapRequired;
            this.idleTimeoutSeconds = idleTimeoutSeconds;
        }
        catch {
            this.bootstrapRequired = false;
            this.idleTimeoutSeconds = defaultIdleTimeoutSeconds;
            this.emitPageNotice("无法读取本机登录配置", "danger");
        }
        this.updateIdleTimeoutInput();
    }
    updateIdleTimeoutInput() {
        const input = document.querySelector("#authAccount [name=\"idleTimeoutSeconds\"]");
        if (input !== null) {
            input.value = String(this.idleTimeoutSeconds);
        }
    }
    setHMIInteractive(interactive) {
        document.querySelectorAll("#hmi-topbar, #hmi-pages, #hmi-footer").forEach((element) => {
            element.toggleAttribute("inert", !interactive);
            if (interactive) {
                element.removeAttribute("aria-hidden");
            }
            else {
                element.setAttribute("aria-hidden", "true");
            }
        });
    }
    setAuthNotice(message) {
        const authenticationVisible = !this.authPanel().hidden && this.authPanel().hasAttribute("data-auth-mode");
        this.authNotice().textContent = authenticationVisible ? "" : message;
        const maintenanceNotice = document.querySelector("#local-admin-notice");
        if (maintenanceNotice !== null) {
            maintenanceNotice.textContent = message;
        }
        if (authenticationVisible && message !== "") {
            this.emitPageNotice(message, "danger");
        }
    }
    prepareGuestHMI() {
        this.endAuthenticationKeyboard();
        const panel = this.authPanel();
        panel.hidden = true;
        panel.setAttribute("aria-busy", "false");
        panel.removeAttribute("data-auth-mode");
        this.loginSection().hidden = true;
        this.bootstrapSection().hidden = true;
        this.setHMIInteractive(true);
        this.setAuthNotice("");
        this.updateAccountControl();
        this.emitPermissionChange();
    }
    openAuthWithKeyboard(screen, message = "") {
        const keyboard = window.HMISoftKeyboard;
        keyboard?.init();
        this.endAuthenticationKeyboard();
        const panel = this.authPanel();
        panel.hidden = false;
        panel.setAttribute("aria-busy", "false");
        panel.setAttribute("data-auth-mode", screen);
        document.querySelector("#authTitle").textContent = screen === "bootstrap" ? "创建管理员" : "登录";
        this.loginSection().hidden = screen !== "login";
        this.bootstrapSection().hidden = screen !== "bootstrap";
        this.setAuthNotice(message);
        const form = document.querySelector(screen === "bootstrap" ? "#initial-admin-form" : "#login-form");
        const input = form.querySelector("[data-soft-keyboard]");
        if (keyboard !== undefined) {
            if (this.authKeyboardOriginalMode === null) {
                this.authKeyboardOriginalMode = keyboard.getMode();
            }
            keyboard.setPinned(true);
            keyboard.setMode("soft", false);
            keyboard.open(input, { immediate: true });
        }
        input.focus({ preventScroll: true });
    }
    showLogin(message = "") {
        this.openAuthWithKeyboard("login", message);
    }
    showBootstrap(message = "") {
        this.openAuthWithKeyboard("bootstrap", message);
    }
    endAuthenticationKeyboard() {
        const keyboard = window.HMISoftKeyboard;
        keyboard?.setPinned(false);
        keyboard?.close("keep");
        if (this.authKeyboardOriginalMode === "native") {
            keyboard?.setMode("native", false);
        }
        this.authKeyboardOriginalMode = null;
    }
    bindAuthForms() {
        const login = document.querySelector("#login-form");
        const submitLogin = () => {
            void this.login(formValue(login, "username"), formValue(login, "password"));
        };
        login.addEventListener("submit", (event) => {
            event.preventDefault();
            submitLogin();
        });
        login.addEventListener("hmi-soft-keyboard-submit", submitLogin);
        const initialAdmin = document.querySelector("#initial-admin-form");
        const submitInitialAdmin = () => {
            void this.createInitialAdmin(formValue(initialAdmin, "username"), formValue(initialAdmin, "password"), formValue(initialAdmin, "confirmPassword"));
        };
        initialAdmin.addEventListener("submit", (event) => {
            event.preventDefault();
            submitInitialAdmin();
        });
        initialAdmin.addEventListener("hmi-soft-keyboard-submit", submitInitialAdmin);
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
    setAuthSubmitBusy(form, busy) {
        form.setAttribute("aria-busy", String(busy));
        const submit = form.querySelector('[type="submit"]');
        if (submit !== null) {
            submit.disabled = busy;
        }
    }
    bindPasswordVisibilityToggles() {
        document.querySelectorAll("[data-password-toggle]").forEach((toggle) => {
            const inputId = toggle.getAttribute("aria-controls");
            const input = inputId === null ? null : document.getElementById(inputId);
            if (!(input instanceof HTMLInputElement)) {
                return;
            }
            const syncLabel = () => {
                const visible = input.type === "text";
                toggle.setAttribute("aria-label", visible ? "隐藏密码" : "显示密码");
                toggle.setAttribute("aria-pressed", String(visible));
            };
            toggle.addEventListener("pointerdown", (event) => event.preventDefault());
            toggle.addEventListener("click", (event) => {
                event.preventDefault();
                input.type = input.type === "password" ? "text" : "password";
                syncLabel();
                input.focus();
            });
            syncLabel();
        });
    }
    bindAccountControls() {
        const operator = document.querySelector("#operatorName");
        operator.tabIndex = 0;
        operator.setAttribute("role", "button");
        operator.addEventListener("click", () => this.toggleAccountSession());
        operator.addEventListener("keydown", (event) => {
            if (event.key === "Enter" || event.key === " ") {
                event.preventDefault();
                this.toggleAccountSession();
            }
        });
    }
    moveLocalAdministrationToMaintenance() {
        const account = document.querySelector("#authAccount");
        const maintenance = document.querySelector("#accountSettingsPanel");
        document.querySelector("#auth-close")?.remove();
        document.querySelector("#logout-button")?.remove();
        const notice = document.createElement("p");
        notice.className = "settings-validation";
        notice.id = "local-admin-notice";
        notice.setAttribute("role", "status");
        notice.setAttribute("aria-live", "polite");
        account.prepend(notice);
        const idleTimeout = account.querySelector("[name=\"idleTimeoutSeconds\"]");
        if (idleTimeout !== null) {
            idleTimeout.value = String(this.idleTimeoutSeconds);
        }
        account.hidden = false;
        if (account.parentElement !== maintenance) {
            maintenance.append(account);
        }
    }
    bindPLCControls() {
        document.querySelector("#plc-scan-button").addEventListener("click", () => {
            this.sendPLCScan();
        });
        document.querySelector("#savePlcButton").addEventListener("click", () => {
            this.sendPLCSave();
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
            if (!this.signedIn || this.session === null) {
                return;
            }
            if (!this.refreshFrontendSession()) {
                return;
            }
        };
        document.addEventListener("pointerdown", report, { passive: true });
        document.addEventListener("keydown", report);
    }
    async login(username, password) {
        if (username.trim() === "" || password === "") {
            this.finishLoginAttempt("登录失败");
            return;
        }
        if (this.loginInFlight) {
            return;
        }
        this.loginInFlight = true;
        const form = document.querySelector("#login-form");
        this.setAuthSubmitBusy(form, true);
        try {
            const { response, value } = await this.authRequest("/api/auth/login", "POST", {
                username: username.trim(), password
            });
            const identity = backendIdentityFrom(value);
            if (response.status === 401) {
                this.finishLoginAttempt("用户名或密码错误");
                return;
            }
            if (!response.ok || identity === null) {
                this.finishLoginAttempt("登录失败");
                return;
            }
            this.beginSession(identity);
            this.emitPageNotice("登录成功");
        }
        catch {
            this.finishLoginAttempt("无法连接本机登录服务");
        }
        finally {
            this.loginInFlight = false;
            this.setAuthSubmitBusy(form, false);
        }
    }
    finishLoginAttempt(message) {
        this.becomeGuest();
        this.emitPageNotice(message, "danger");
    }
    emitPageNotice(message, level = "info") {
        window.dispatchEvent(new CustomEvent("block-hmi-notice", { detail: { message, level } }));
    }
    async createInitialAdmin(username, password, confirmPassword) {
        if (password !== confirmPassword) {
            this.setAuthNotice("两次输入的密码不一致");
            return;
        }
        const normalizedUsername = username.trim();
        if (normalizedUsername === "" || password === "") {
            this.setAuthNotice("请填写管理员用户名和密码");
            return;
        }
        if (this.initialAdminInFlight) {
            return;
        }
        this.initialAdminInFlight = true;
        const form = document.querySelector("#initial-admin-form");
        this.setAuthSubmitBusy(form, true);
        try {
            const { response, value } = await this.authRequest("/api/auth/initial-admin", "POST", {
                username: normalizedUsername, password, confirmPassword
            });
            if (this.signedIn) {
                return;
            }
            if (response.status === 409) {
                this.bootstrapRequired = false;
                this.showLogin("本机管理员已存在，请登录。");
                return;
            }
            const identity = backendIdentityFrom(value);
            if (!response.ok || identity === null) {
                this.setAuthNotice("无法创建本机管理员");
                return;
            }
            this.bootstrapRequired = false;
            this.beginSession(identity);
            this.emitPageNotice("管理员创建成功");
        }
        catch {
            this.setAuthNotice("无法连接本机登录服务");
        }
        finally {
            this.initialAdminInFlight = false;
            this.setAuthSubmitBusy(form, false);
        }
    }
    async changePassword(currentPassword, newPassword, confirmPassword) {
        if (!this.requirePermission("maintenance")) {
            return;
        }
        if (newPassword !== confirmPassword) {
            this.setAuthNotice("两次输入的新密码不一致");
            return;
        }
        if (newPassword === "") {
            this.setAuthNotice("新密码不能为空");
            return;
        }
        try {
            if (this.session === null) {
                return;
            }
            const { response } = await this.authRequest("/api/auth/password", "POST", {
                username: this.session.username, currentPassword, newPassword
            });
            if (response.status === 401) {
                this.setAuthNotice("当前密码不正确");
                return;
            }
            if (!response.ok) {
                this.setAuthNotice("无法修改密码");
                return;
            }
            this.setAuthNotice("密码已修改");
        }
        catch {
            this.setAuthNotice("无法连接本机登录服务");
        }
    }
    async saveSessionPolicy(idleTimeoutSeconds) {
        if (!this.requirePermission("maintenance")) {
            return;
        }
        if (!Number.isInteger(idleTimeoutSeconds) || idleTimeoutSeconds < 60 || idleTimeoutSeconds > 3600) {
            this.setAuthNotice("不活动退出时长必须在 60 到 3600 秒之间");
            return;
        }
        try {
            const { response, value } = await this.authRequest("/api/config/session", "PUT", { idleTimeoutSeconds });
            const savedTimeout = idleTimeoutFrom(value);
            if (!response.ok || savedTimeout === null) {
                this.setAuthNotice("无法保存会话时长");
                return;
            }
            this.idleTimeoutSeconds = savedTimeout;
            this.updateIdleTimeoutInput();
            this.refreshFrontendSession();
            this.setAuthNotice("会话时长已保存");
        }
        catch {
            this.setAuthNotice("无法连接本机登录服务");
        }
    }
    beginSession(identity) {
        this.endAuthenticationKeyboard();
        this.signedIn = true;
        const now = Date.now();
        this.session = {
            username: identity.username,
            role: identity.role,
            permissions: { ...identity.permissions },
            expiresAt: now + this.idleTimeoutSeconds * 1000
        };
        this.scheduleSessionExpiry();
        this.authPanel().hidden = true;
        this.setAuthNotice("");
        this.updateAccountControl();
        this.emitPermissionChange();
        this.renderPLCCandidates();
        if (!this.flushDeferredLiveState()) {
            this.emitState();
        }
    }
    logout() {
        this.endAuthenticationKeyboard();
        this.pendingPointCommand.cancel("已退出登录，现场操作结果未知", 401, "unauthenticated");
        this.becomeGuest();
    }
    becomeGuest() {
        this.endAuthenticationKeyboard();
        this.signedIn = false;
        this.session = null;
        if (this.sessionExpiryTimer !== null) {
            window.clearTimeout(this.sessionExpiryTimer);
            this.sessionExpiryTimer = null;
        }
        this.authPanel().hidden = true;
        if (document.querySelector("[data-page=\"maintenance\"]")?.hidden === false) {
            window.dispatchEvent(new Event("block-hmi-guest"));
        }
        this.updateAccountControl();
        this.emitPermissionChange();
        this.renderPLCCandidates();
        if (!this.flushDeferredLiveState()) {
            this.emitState();
        }
        this.deferProductionPolicy();
    }
    openSocket() {
        if (this.socket !== null) {
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
        socket.addEventListener("close", () => {
            if (this.socket !== socket) {
                return;
            }
            this.pendingPointCommand.cancel("本机服务连接中断，现场操作结果未知", 503, "network_error");
            this.socket = null;
            this.configured = false;
            clearTransientRuntime(this.values, this.plcDevices);
            this.publishLiveState(true);
            this.plcState = "disconnected";
            this.renderPLCCandidates();
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
        this.pendingPointCommand.cancel("本机服务连接已关闭，现场操作结果未知", 503, "network_error");
        const socket = this.socket;
        this.socket = null;
        socket?.close();
    }
    scheduleReconnect() {
        if (this.reconnectTimer !== null) {
            return;
        }
        const delay = this.reconnectDelay;
        this.reconnectDelay = Math.min(this.reconnectDelay * 2, 30000);
        this.reconnectTimer = window.setTimeout(() => {
            this.reconnectTimer = null;
            this.openSocket();
        }, delay);
    }
    refreshFrontendSession() {
        const renewed = renewFrontendSession(this.session, this.idleTimeoutSeconds);
        if (renewed === null) {
            this.becomeGuest();
            return false;
        }
        this.session = renewed;
        this.scheduleSessionExpiry();
        return true;
    }
    scheduleSessionExpiry() {
        if (this.sessionExpiryTimer !== null) {
            window.clearTimeout(this.sessionExpiryTimer);
            this.sessionExpiryTimer = null;
        }
        if (this.session === null) {
            return;
        }
        const delay = Math.max(0, this.session.expiresAt - Date.now());
        this.sessionExpiryTimer = window.setTimeout(() => {
            if (!frontendSessionIsActive(this.session)) {
                this.becomeGuest();
                return;
            }
            this.scheduleSessionExpiry();
        }, delay + 20);
    }
    toggleAccountSession() {
        if (this.signedIn) {
            this.logout();
            return;
        }
        this.openAuthWithKeyboard(this.authenticationScreen());
    }
    hasPermission(permission) {
        return this.signedIn && this.session !== null && this.session.permissions[permission];
    }
    frontendRole() {
        return this.signedIn && this.session !== null ? this.session.role : "GUEST";
    }
    requirePermission(permission) {
        if (this.hasPermission(permission)) {
            return true;
        }
        this.openAuthWithKeyboard(this.authenticationScreen());
        return false;
    }
    updateAccountControl() {
        const operator = document.querySelector("#operatorName");
        const label = operator.parentElement?.querySelector(".meta-cn") ?? null;
        operator.textContent = this.signedIn && this.session !== null ? this.session.username : "登录";
        operator.setAttribute("aria-label", this.signedIn ? "点击退出本机登录" : "点击登录本机管理员");
        if (label !== null) {
            label.textContent = this.signedIn ? "管理员" : "登录";
        }
    }
    emitPermissionChange() {
        window.dispatchEvent(new CustomEvent("block-hmi-auth-changed", {
            detail: {
                signedIn: this.signedIn,
                role: this.frontendRole(),
                operate: this.hasPermission("operate"),
                maintenance: this.hasPermission("maintenance")
            }
        }));
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
        if (this.pendingPointCommand.receive(message)) {
            return;
        }
        if (message.type === "runtime.configured") {
            this.configured = true;
            this.setPLCStatus(plcStateText(this.plcState));
            this.renderPLCCandidates();
            this.emitState();
            return;
        }
        if (message.type === "points.snapshot" && message.values !== undefined) {
            this.values.clear();
            applyAbsoluteValues(this.values, message.values);
            this.revision += 1;
            this.lastPLCSampleAt = latestPointTime(message.values) ?? new Date().toISOString();
            this.setPLCStatus("已接收 PLC 当前状态");
            this.emitState();
            return;
        }
        if (message.type === "points.changed" && message.values !== undefined) {
            applyAbsoluteValues(this.values, message.values);
            this.revision += 1;
            this.lastPLCSampleAt = latestPointTime(message.values) ?? new Date().toISOString();
            this.publishLiveState();
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
                this.populatePLCInputs(message.deviceId);
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
            if (message.state === "error") {
                this.lastPLCError = plcStateText(message.state, message.deviceId);
            }
            this.setPLCStatus(plcStateText(message.state, message.deviceId));
            this.renderPLCCandidates();
            return;
        }
        if (message.success === false) {
            this.lastPLCError = message.error?.message ?? errorText(message.error?.code);
            this.publishLiveState();
            this.setAuthNotice(errorText(message.error?.code));
        }
    }
    getState() {
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
        const manual = this.valueFor("footer.mode.manual");
        const singlePaused = this.valueFor("home.cycle.single");
        const framePaused = this.valueFor("home.cycle.frame");
        state.revision = this.revision;
        state.running = null;
        state.mode = manual === true ? "manual" : manual === false ? "auto" : null;
        state.singlePaused = typeof singlePaused === "boolean" ? singlePaused : null;
        state.framePaused = typeof framePaused === "boolean" ? framePaused : null;
        state.target = this.numberFor("maintenance.production.target");
        state.output = this.numberFor("production.output.today");
        state.cycle = this.numberFor("production.cycle.single");
        state.oee = null;
        state.inspected = null;
        state.passed = this.numberFor("production.quality.passed");
        state.ng = this.numberFor("production.quality.ng");
        state.pending = this.numberFor("production.quality.pending");
        state.blank = this.numberFor("production.bin.blank");
        state.finished = this.numberFor("production.bin.finished");
        state.toolLimit = null;
        state.inspectInterval = null;
        state.bins = [
            { type: "empty", label: "暂无数据" },
            { type: "empty", label: "暂无数据" },
            { type: "empty", label: "暂无数据" }
        ];
        state.alarms = [
            { id: 1, level: "info", code: "提示", text: "当前未提供报警历史", time: "--", acknowledged: true }
        ];
        state.history = [
            { id: 1, level: "info", code: "提示", text: "当前未提供历史记录", time: "--" }
        ];
        return state;
    }
    numberFor(displayPath) {
        const value = this.valueFor(displayPath);
        return typeof value === "number" && Number.isFinite(value) ? value : null;
    }
    valueFor(displayPath) {
        const binding = this.config.bindings.find((item) => item.displayPath === displayPath);
        if (binding === undefined || binding.readPoint === null) {
            return undefined;
        }
        const point = this.values.get(binding.readPoint);
        return point?.quality === "good" ? point.value : undefined;
    }
    pointBinding(displayPath) {
        const binding = this.config.bindings.find((item) => item.displayPath === displayPath);
        return binding === undefined ? null : { ...binding };
    }
    pointValue(displayPath) {
        return this.valueFor(displayPath);
    }
    pointCanWrite(displayPath) {
        const binding = this.config.bindings.find((item) => item.displayPath === displayPath);
        if (binding === undefined || binding.state === "pending" || binding.writePoint === null || binding.writePoint === undefined ||
            (binding.action !== "pulse" && binding.action !== "toggle" && binding.action !== "set")) {
            return false;
        }
        if (!this.hasPermission(binding.permission ?? "operate")) {
            return false;
        }
        return this.demo || this.canSendRuntime();
    }
    pointCommand(displayPath, value) {
        const binding = this.config.bindings.find((item) => item.displayPath === displayPath);
        const action = binding?.action;
        const writePoint = binding?.writePoint;
        if (binding === undefined || binding.state === "pending" || writePoint === null || writePoint === undefined ||
            (action !== "pulse" && action !== "toggle" && action !== "set")) {
            return Promise.reject(new HMIAPIError("点位读写映射待确认", 501, "point_not_configured"));
        }
        const permission = binding.permission ?? "operate";
        if (!this.hasPermission(permission)) {
            return Promise.reject(new HMIAPIError(permission === "maintenance" ? "请使用管理员会话执行此操作" : "请登录后执行现场操作", 403, "permission_denied"));
        }
        if (action === "set" && (typeof value !== "number" || !Number.isFinite(value))) {
            return Promise.reject(new HMIAPIError("请输入有效数值", 400, "invalid_value"));
        }
        if (this.demo) {
            return Promise.resolve();
        }
        if (!this.canSendRuntime()) {
            return Promise.reject(new HMIAPIError("PLC 尚未连接", 503, "plc_not_connected"));
        }
        const requestId = requestID();
        return this.pendingPointCommand.dispatch(requestId, () => {
            this.socket.send(JSON.stringify(buildPointCommand(writePoint, action, requestId, undefined, action === "set" ? value : undefined)));
        });
    }
    manualBinding(displayPath) {
        return this.pointBinding(displayPath);
    }
    manualValue(displayPath) {
        return this.pointValue(displayPath);
    }
    manualCanWrite(displayPath) {
        return this.pointCanWrite(displayPath);
    }
    manualCommand(displayPath, value) {
        return this.pointCommand(displayPath, value);
    }
    sendCommand(command, payload = {}) {
        if (!this.requirePermission("operate")) {
            return Promise.reject(new HMIAPIError("请登录管理员后执行现场操作", 403, "permission_denied"));
        }
        if (this.demo) {
            return Promise.resolve({ state: this.applyDemoCommand(command, payload) });
        }
        const operation = command === "start"
            ? { displayPath: "home.machine.start", action: "pulse", name: "启动" }
            : command === "set_mode"
                ? { displayPath: "home.machine.enabled", action: "toggle", name: "模式切换" }
                : null;
        if (operation === null) {
            return Promise.reject(new HMIAPIError("当前界面未提供此操作", 501, "not_supported"));
        }
        const binding = this.config.bindings.find((item) => item.displayPath === operation.displayPath);
        if (binding?.writePoint === undefined || binding.writePoint === null || binding.action !== operation.action) {
            return Promise.reject(new HMIAPIError(`points.json 未配置${operation.name}点位`, 500, "point_not_configured"));
        }
        if (!this.canSendRuntime()) {
            return Promise.reject(new HMIAPIError("PLC 尚未连接", 503, "plc_not_connected"));
        }
        const pointID = binding.writePoint;
        const requestId = requestID();
        const confirmation = this.pendingPointCommand.dispatch(requestId, () => {
            this.socket.send(JSON.stringify(buildPointCommand(pointID, operation.action, requestId)));
        });
        return confirmation.then(() => ({ state: cloneState(this.currentState()) }));
    }
    acknowledgeAlarm(alarmID) {
        if (!this.requirePermission("operate")) {
            return Promise.reject(new HMIAPIError("请登录管理员后确认报警", 403, "permission_denied"));
        }
        if (!this.demo) {
            return Promise.reject(new HMIAPIError("当前界面不支持报警确认", 501, "not_supported"));
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
            state.inspected = (state.inspected ?? 0) + 1;
            state.passed = (state.passed ?? 0) + 1;
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
        if (!this.requirePermission("maintenance")) {
            return;
        }
        if (this.demo) {
            this.setPLCStatus("演示模式不扫描 PLC");
            return;
        }
        const settings = this.readPLCScanSettings();
        if (settings === null) {
            return;
        }
        this.setPLCStatus("正在扫描 PLC");
        this.sendRuntimeRequest(buildPLCScan(settings.addressRange, settings.port, settings.unitID));
    }
    sendPLCSave() {
        if (!this.requirePermission("maintenance")) {
            return;
        }
        if (this.demo) {
            this.setPLCStatus("演示模式不保存 PLC 地址");
            return;
        }
        const endpoint = this.readPLCEndpoint();
        if (endpoint === null) {
            return;
        }
        this.setPLCStatus("正在保存并连接 PLC 地址");
        this.sendPLCConnect(buildPLCDeviceID(endpoint.host, endpoint.port, endpoint.unitID));
    }
    sendPLCConnect(deviceID) {
        if (!this.requirePermission("maintenance")) {
            return;
        }
        if (this.demo) {
            return;
        }
        this.populatePLCInputs(deviceID);
        this.sendRuntimeRequest(buildPLCConnect(deviceID));
    }
    sendPLCDisconnect() {
        if (!this.requirePermission("maintenance")) {
            return;
        }
        if (this.demo) {
            return;
        }
        this.sendRuntimeRequest(buildPLCDisconnect());
    }
    sendPointsSnapshotGet() {
        if (!this.requirePermission("maintenance")) {
            return;
        }
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
    readPLCScanSettings() {
        const addressRange = document.querySelector("#plcSubnetInput").value.trim();
        if (!isIPv4CIDR(addressRange)) {
            this.setPLCStatus("请输入有效的独立子网（IPv4 CIDR）");
            return null;
        }
        const transport = this.readPLCTransportSettings();
        return transport === null ? null : { addressRange, ...transport };
    }
    readPLCEndpoint() {
        const host = document.querySelector("#plcHostInput").value.trim();
        if (!isIPv4Address(host)) {
            this.setPLCStatus("请输入有效的 PLC IP");
            return null;
        }
        const transport = this.readPLCTransportSettings();
        return transport === null ? null : { host, ...transport };
    }
    readPLCTransportSettings() {
        const port = Number(document.querySelector("#plcPortInput").value);
        const unitID = Number(document.querySelector("#plcUnitInput").value);
        if (!Number.isInteger(port) || port < 1 || port > 65535) {
            this.setPLCStatus("请输入 1 至 65535 的 PLC 端口");
            return null;
        }
        if (!Number.isInteger(unitID) || unitID < 1 || unitID > 247) {
            this.setPLCStatus("请输入 1 至 247 的 Unit ID");
            return null;
        }
        return { port, unitID };
    }
    populatePLCInputs(deviceID) {
        let parsed;
        try {
            parsed = new URL(deviceID);
        }
        catch {
            return;
        }
        if (parsed.protocol !== "easy521:" || !isIPv4Address(parsed.hostname)) {
            return;
        }
        const port = Number(parsed.port);
        const unitID = Number(parsed.searchParams.get("unitId"));
        if (!Number.isInteger(port) || port < 1 || port > 65535 || !Number.isInteger(unitID) || unitID < 1 || unitID > 247) {
            return;
        }
        document.querySelector("#plcHostInput").value = parsed.hostname;
        document.querySelector("#plcPortInput").value = String(port);
        document.querySelector("#plcUnitInput").value = String(unitID);
    }
    canSendRuntime() {
        return this.configured && (this.demo || this.socket?.readyState === WebSocket.OPEN);
    }
    setPLCStatus(message) {
        document.querySelector("#plc-status").textContent = message;
        if (this.isUserInputActive()) {
            this.deferredLiveState = true;
            return;
        }
        this.renderPLCReadOnly();
    }
    isUserInputActive() {
        const active = document.activeElement;
        const authPanel = this.authPanel();
        const activeInHiddenAuthPanel = authPanel.hidden && authPanel.contains(active);
        const nativeKeyboardInput = window.HMISoftKeyboard?.getMode() === "native" &&
            (active instanceof HTMLInputElement || active instanceof HTMLTextAreaElement) &&
            !activeInHiddenAuthPanel;
        return !authPanel.hidden ||
            window.HMISoftKeyboard?.isOpen() === true ||
            nativeKeyboardInput;
    }
    publishLiveState(force = false) {
        if (this.isUserInputActive() && !force) {
            this.deferredLiveState = true;
            return;
        }
        this.deferredLiveState = false;
        this.renderPLCReadOnly();
        this.emitState(force);
    }
    flushDeferredLiveState() {
        if (!this.deferredLiveState || this.isUserInputActive()) {
            return false;
        }
        this.deferredLiveState = false;
        this.renderPLCReadOnly();
        this.emitState();
        return true;
    }
    renderPLCReadOnly() {
        const connection = document.querySelector("#plc-connection-value");
        if (connection === null) {
            return;
        }
        connection.textContent = plcStateText(this.plcState);
    }
    renderPLCCandidates() {
        const list = document.querySelector("#plc-candidates");
        const scan = document.querySelector("#plc-scan-button");
        const save = document.querySelector("#savePlcButton");
        const disconnect = document.querySelector("#plc-disconnect-button");
        const snapshot = document.querySelector("#snapshot-button");
        const active = this.canSendRuntime();
        scan.disabled = !active;
        save.disabled = !active;
        snapshot.disabled = !active;
        disconnect.disabled = !active || this.plcState === "disconnected" || this.plcState === "unconfigured";
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
            connect.className = "plc-candidate-connect";
            connect.textContent = "连接";
            connect.disabled = !active || device.state === "connected" || device.state === "connecting";
            connect.addEventListener("click", () => this.sendPLCConnect(device.deviceId));
            item.append(detail, document.createTextNode(" "), connect);
            list.append(item);
        }
    }
    emitState(force = false) {
        if (this.isUserInputActive() && !force) {
            this.deferredLiveState = true;
            return;
        }
        if (force) {
            window.dispatchEvent(new CustomEvent("block-hmi-state", {
                detail: { state: cloneState(this.currentState()), forceRender: true }
            }));
            return;
        }
        window.dispatchEvent(new Event("block-hmi-state"));
    }
    deferProductionPolicy() {
        if (this.demo) {
            return;
        }
        window.setTimeout(() => {
            const runtimeEnabled = this.canSendRuntime();
            document.querySelectorAll(".control-button").forEach((button) => {
                const displayPath = button.dataset.pointAction;
                const configured = displayPath !== undefined && this.config.bindings.some((binding) => binding.displayPath === displayPath && binding.state !== "pending" && binding.writePoint !== null && binding.action === "pulse");
                const available = runtimeEnabled && configured;
                button.dataset.backendUnavailable = available ? "false" : "true";
            });
            const mode = document.querySelector("#modeToggle");
            if (mode !== null) {
                mode.dataset.backendUnavailable = runtimeEnabled ? "false" : "true";
            }
            document.querySelectorAll(".ack-button").forEach((button) => {
                button.dataset.backendUnavailable = "true";
            });
        }, 0);
    }
}
function formValue(form, name) {
    return String(new FormData(form).get(name) ?? "");
}
function plcStateText(state, deviceID) {
    const text = {
        unconfigured: "PLC 尚未配置",
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
    const bridge = new AppleBridge(await loadConfiguration(demo), demo, demoAuthPreviewMode(), demoManualRole());
    await bridge.start();
    const backend = bridge.backend();
    window.HMIBackend = backend;
    return backend;
}
