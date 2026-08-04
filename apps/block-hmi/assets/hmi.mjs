const debounceMilliseconds = 50;
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
export function buildRuntimeConfigure(points) {
    return {
        type: "runtime.configure",
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
    };
}
export function applyAbsoluteValues(target, values) {
    for (const [pointId, pointValue] of Object.entries(values)) {
        target.set(pointId, { ...pointValue });
    }
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
    for (const group of config.layout) {
        if (!isDisplayPath(group.displayPath) || !/[\u3400-\u9fff]/.test(group.description)) {
            throw new Error("layout 的 displayPath 必须为英文点路径，description 必须为中文");
        }
    }
    return config;
}
async function loadConfiguration() {
    const response = await fetch(new URL("./points.json", import.meta.url), { cache: "no-store" });
    if (!response.ok) {
        throw new Error("无法读取 points.json");
    }
    return configurationFrom(await response.json());
}
async function postJSON(path, body) {
    const response = await fetch(path, {
        method: "POST",
        credentials: "same-origin",
        cache: "no-store",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body)
    });
    if (!response.ok) {
        throw new Error(await responseMessage(response));
    }
    return response;
}
async function responseMessage(response) {
    try {
        const body = await response.json();
        return body.error?.message ?? "请求失败（HTTP " + String(response.status) + "）";
    }
    catch {
        return "请求失败（HTTP " + String(response.status) + "）";
    }
}
function formValue(form, name) {
    return String(new FormData(form).get(name) ?? "");
}
function websocketURL() {
    const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
    return scheme + "//" + window.location.host + "/ws";
}
class HMI {
    config = null;
    socket = null;
    signedIn = false;
    configured = false;
    reconnectDelay = 1000;
    reconnectTimer = null;
    lastActivityAt = Number.NEGATIVE_INFINITY;
    activationFilter = new ActivationFilter();
    values = new Map();
    commandButtons = [];
    pointViews = new Map();
    notice = document.querySelector("#notice");
    status = document.querySelector("#connection-status");
    authPanel = document.querySelector("#auth-panel");
    runtimePanel = document.querySelector("#runtime-panel");
    runtimeLayout = document.querySelector("#runtime-layout");
    async start() {
        this.bindAuthForms();
        this.bindActivityReporting();
        try {
            this.config = await loadConfiguration();
            document.querySelector("#page-title").textContent = this.config.title;
            this.renderLayout();
        }
        catch (error) {
            this.setNotice(error instanceof Error ? error.message : "无法加载页面配置");
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
        document.querySelector("#logout-button").addEventListener("click", () => {
            void this.logout();
        });
    }
    bindActivityReporting() {
        const report = () => {
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
        try {
            await postJSON("/api/v2/auth/login", { username, password });
            this.beginSession();
        }
        catch (error) {
            this.setNotice(error instanceof Error ? error.message : "登录失败");
        }
    }
    async createInitialAdmin(username, password, confirmPassword) {
        if (password !== confirmPassword) {
            this.setNotice("两次输入的密码不一致");
            return;
        }
        try {
            await postJSON("/api/v2/auth/initial-admin", { username, password, confirmPassword });
            this.beginSession();
        }
        catch (error) {
            this.setNotice(error instanceof Error ? error.message : "创建管理员失败");
        }
    }
    async changePassword(currentPassword, newPassword, confirmPassword) {
        if (newPassword !== confirmPassword) {
            this.setNotice("两次输入的新密码不一致");
            return;
        }
        try {
            await postJSON("/api/v2/auth/password", { currentPassword, newPassword, confirmPassword });
            this.setNotice("密码已修改");
        }
        catch (error) {
            this.setNotice(error instanceof Error ? error.message : "修改密码失败");
        }
    }
    beginSession() {
        if (this.config === null) {
            this.setNotice("页面配置尚未准备好");
            return;
        }
        this.signedIn = true;
        this.authPanel.hidden = true;
        this.runtimePanel.hidden = false;
        this.setNotice("");
        this.openSocket();
    }
    async logout() {
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
    }
    endSession(message) {
        this.signedIn = false;
        this.configured = false;
        this.values.clear();
        this.closeSocket();
        this.authPanel.hidden = false;
        this.runtimePanel.hidden = true;
        this.renderValues();
        this.setNotice(message);
        this.setStatus("请登录后连接设备");
    }
    openSocket() {
        if (!this.signedIn || this.config === null || this.socket !== null) {
            return;
        }
        const socket = new WebSocket(websocketURL());
        this.socket = socket;
        this.setStatus("正在连接本机服务");
        socket.addEventListener("open", () => {
            if (this.socket !== socket || this.config === null) {
                return;
            }
            this.reconnectDelay = 1000;
            this.configured = false;
            this.values.clear();
            this.renderValues();
            this.setCommandsEnabled();
            socket.send(JSON.stringify(buildRuntimeConfigure(this.config.points)));
            this.setStatus("正在同步点位表");
        });
        socket.addEventListener("message", (event) => {
            this.handleSocketMessage(event.data);
        });
        socket.addEventListener("close", (event) => {
            if (this.socket !== socket) {
                return;
            }
            this.socket = null;
            this.configured = false;
            this.values.clear();
            this.renderValues();
            this.setCommandsEnabled();
            if (event.code === 4401) {
                this.endSession("会话已过期，请重新登录");
                return;
            }
            this.setStatus("连接中断");
            this.scheduleReconnect();
        });
    }
    closeSocket() {
        if (this.reconnectTimer !== null) {
            window.clearTimeout(this.reconnectTimer);
            this.reconnectTimer = null;
        }
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
            this.setNotice("收到无法识别的服务消息");
            return;
        }
        if (message.type === "runtime.configured") {
            this.configured = true;
            this.setCommandsEnabled();
            this.setStatus("点位表已同步，等待 PLC 读取");
            return;
        }
        if (message.type === "points.snapshot" && message.values !== undefined) {
            this.values.clear();
            applyAbsoluteValues(this.values, message.values);
            this.renderValues();
            this.setStatus("已接收 PLC 当前状态");
            return;
        }
        if (message.type === "points.changed" && message.values !== undefined) {
            applyAbsoluteValues(this.values, message.values);
            this.renderValues();
            return;
        }
        if (message.type === "point.result" && message.success === false) {
            this.setNotice(message.error?.message ?? "PLC 操作失败，等待新鲜反馈");
        }
    }
    renderLayout() {
        if (this.config === null) {
            return;
        }
        this.runtimeLayout.replaceChildren();
        this.commandButtons.length = 0;
        this.pointViews.clear();
        const bindings = new Map(this.config.bindings.map((binding) => [binding.displayPath, binding]));
        for (const group of this.config.layout) {
            const section = document.createElement("section");
            section.className = "layout-group";
            const heading = document.createElement("h2");
            heading.textContent = group.description;
            section.append(heading);
            const grid = document.createElement("div");
            grid.className = "binding-grid";
            for (const displayPath of group.bindings) {
                const binding = bindings.get(displayPath);
                if (binding === undefined) {
                    continue;
                }
                grid.append(this.renderBinding(binding));
            }
            section.append(grid);
            this.runtimeLayout.append(section);
        }
        this.renderValues();
        this.setCommandsEnabled();
    }
    renderBinding(binding) {
        if (binding.component === "button") {
            return this.renderButton(binding);
        }
        const box = document.createElement("div");
        box.className = "point-value";
        const title = document.createElement("strong");
        title.textContent = binding.description;
        const value = document.createElement("span");
        value.textContent = "等待 PLC 读取";
        box.append(title, value);
        const existing = this.pointViews.get(binding.readPoint) ?? [];
        existing.push(value);
        this.pointViews.set(binding.readPoint, existing);
        return box;
    }
    renderButton(binding) {
        const button = document.createElement("button");
        button.type = "button";
        button.textContent = binding.description;
        button.disabled = true;
        this.commandButtons.push(button);
        if (binding.writePoint === undefined || binding.writePoint === null || binding.action === undefined) {
            return button;
        }
        if (binding.action === "momentary") {
            let pressed = false;
            button.addEventListener("pointerdown", (event) => {
                if (!this.activationFilter.accept(event) || !this.canSendCommands()) {
                    return;
                }
                pressed = true;
                button.setPointerCapture(event.pointerId);
                this.sendCommand(binding.writePoint, "press");
            });
            const release = () => {
                if (!pressed) {
                    return;
                }
                pressed = false;
                this.sendCommand(binding.writePoint, "release");
            };
            button.addEventListener("pointerup", release);
            button.addEventListener("pointercancel", release);
            return button;
        }
        button.addEventListener("click", (event) => {
            if (!this.activationFilter.accept(event) || !this.canSendCommands()) {
                return;
            }
            if (binding.action === "pulse" || binding.action === "toggle") {
                this.sendCommand(binding.writePoint, binding.action);
            }
        });
        return button;
    }
    canSendCommands() {
        return this.configured && this.socket?.readyState === WebSocket.OPEN;
    }
    sendCommand(pointId, action) {
        if (!this.canSendCommands() || this.socket === null) {
            return;
        }
        this.socket.send(JSON.stringify({ type: "point.command", pointId, action }));
    }
    renderValues() {
        for (const [pointId, nodes] of this.pointViews) {
            const point = this.values.get(pointId);
            const text = point === undefined
                ? "等待 PLC 读取"
                : formatValue(point.value) + " · " + point.quality;
            for (const node of nodes) {
                node.textContent = text;
            }
        }
    }
    setCommandsEnabled() {
        const enabled = this.canSendCommands();
        for (const button of this.commandButtons) {
            button.disabled = !enabled;
        }
    }
    setNotice(message) {
        this.notice.textContent = message;
    }
    setStatus(message) {
        this.status.textContent = message;
    }
}
function formatValue(value) {
    if (value === null) {
        return "无";
    }
    return String(value);
}
if (typeof document !== "undefined") {
    const hmi = new HMI();
    void hmi.start();
}
