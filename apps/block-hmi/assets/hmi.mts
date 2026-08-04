type Scalar = boolean | number | string | null;

type PointValue = {
  value: Scalar;
  quality: "good" | "stale" | "error";
  updatedAt: string;
  alarmActive?: boolean | null;
};

type PLCDevice = {
  deviceId: string;
  name: string;
  address: string;
  state: "disconnected" | "connecting" | "connected" | "reconnecting" | "error";
  selected: boolean;
  metadata: Record<string, unknown>;
};

type PointDefinition = {
  pointId: string;
  address: string;
  type: "bool" | "int" | "float" | "string";
  access: "read" | "write" | "read_write";
  readPoint: string;
  writePoint: string | null;
  writeMethod: "maskWrite" | null;
  write?: {
    mode: "set" | "pulse" | "momentary" | "toggle";
    activeValue: boolean;
    defaultValue: boolean;
    pulseMs?: number;
  };
};

type Binding = {
  displayPath: string;
  description: string;
  component: "button" | "value";
  readPoint: string;
  writePoint?: string | null;
  action?: "pulse" | "momentary" | "toggle";
};

type LayoutGroup = {
  displayPath: string;
  description: string;
  bindings: string[];
};

type PageConfiguration = {
  title: string;
  scanIntervalMs: number;
  points: PointDefinition[];
  bindings: Binding[];
  layout: LayoutGroup[];
};

type ActivationEvent = {
  type: string;
  timeStamp: number;
  detail: number;
  pointerId?: number;
};

const debounceMilliseconds = 50;
const defaultPLCAddressRange = "192.168.1.0/24";

function requestID(): string {
  return crypto.randomUUID();
}

function request(type: string, fields: object, id = requestID(), timestamp = new Date().toISOString()): object {
  return {
    protocolVersion: "1.0",
    type,
    requestId: id,
    timestamp,
    ...fields
  };
}

export function isDisplayPath(value: string): boolean {
  return /^[a-z]+(?:[a-z]*)(?:\.[a-z]+(?:[a-z]*)?)+$/.test(value);
}

export class ActivationFilter {
  private lastSignature = "";
  private lastTime = Number.NEGATIVE_INFINITY;

  accept(event: ActivationEvent): boolean {
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

export function buildRuntimeConfigure(
  points: readonly PointDefinition[],
  id = requestID(),
  timestamp = new Date().toISOString()
): object {
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

export function buildPointsSnapshotGet(id = requestID(), timestamp = new Date().toISOString()): object {
  return request("points.snapshot.get", {}, id, timestamp);
}

export function buildPLCScan(addressRange = defaultPLCAddressRange, id = requestID(), timestamp = new Date().toISOString()): object {
  return request("plc.scan", { addressRange }, id, timestamp);
}

export function buildPLCConnect(deviceId: string, id = requestID(), timestamp = new Date().toISOString()): object {
  return request("plc.connect", { deviceId }, id, timestamp);
}

export function buildPLCDisconnect(id = requestID(), timestamp = new Date().toISOString()): object {
  return request("plc.disconnect", {}, id, timestamp);
}

export function buildPointCommand(
  pointId: string,
  action: "pulse" | "press" | "release" | "toggle",
  id = requestID(),
  timestamp = new Date().toISOString()
): object {
  return request("point.command", { pointId, action }, id, timestamp);
}

export function applyAbsoluteValues(target: Map<string, PointValue>, values: Record<string, PointValue>): void {
  for (const [pointId, pointValue] of Object.entries(values)) {
    target.set(pointId, { ...pointValue });
  }
}

export function clearTransientRuntime(values: Map<string, PointValue>, devices: PLCDevice[]): void {
  values.clear();
  devices.splice(0, devices.length);
}

function configurationFrom(value: unknown): PageConfiguration {
  if (typeof value !== "object" || value === null) {
    throw new Error("points.json 必须是对象");
  }
  const config = value as Partial<PageConfiguration>;
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
  return config as PageConfiguration;
}

async function loadConfiguration(): Promise<PageConfiguration> {
  const response = await fetch(new URL("./points.json", import.meta.url), { cache: "no-store" });
  if (!response.ok) {
    throw new Error("无法读取 points.json");
  }
  return configurationFrom(await response.json());
}

async function postJSON(path: string, body: object): Promise<Response> {
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

async function responseMessage(response: Response): Promise<string> {
  try {
    const body = await response.json() as { error?: { message?: string } };
    return body.error?.message ?? "请求失败（HTTP " + String(response.status) + "）";
  } catch {
    return "请求失败（HTTP " + String(response.status) + "）";
  }
}

function formValue(form: HTMLFormElement, name: string): string {
  return String(new FormData(form).get(name) ?? "");
}

function websocketURL(): string {
  const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
  return scheme + "//" + window.location.host + "/ws";
}

class HMI {
  private config: PageConfiguration | null = null;
  private socket: WebSocket | null = null;
  private signedIn = false;
  private configured = false;
  private reconnectDelay = 1000;
  private reconnectTimer: number | null = null;
  private lastActivityAt = Number.NEGATIVE_INFINITY;
  private plcState: PLCDevice["state"] = "disconnected";
  private readonly activationFilter = new ActivationFilter();
  private readonly values = new Map<string, PointValue>();
  private readonly plcDevices: PLCDevice[] = [];
  private readonly commandButtons: HTMLButtonElement[] = [];
  private readonly pointViews = new Map<string, HTMLElement[]>();
  private readonly notice = document.querySelector<HTMLElement>("#notice")!;
  private readonly status = document.querySelector<HTMLElement>("#connection-status")!;
  private readonly authPanel = document.querySelector<HTMLElement>("#auth-panel")!;
  private readonly runtimePanel = document.querySelector<HTMLElement>("#runtime-panel")!;
  private readonly runtimeLayout = document.querySelector<HTMLElement>("#runtime-layout")!;
  private readonly plcStatus = document.querySelector<HTMLElement>("#plc-status")!;
  private readonly plcCandidates = document.querySelector<HTMLElement>("#plc-candidates")!;
  private readonly plcScanButton = document.querySelector<HTMLButtonElement>("#plc-scan-button")!;
  private readonly plcDisconnectButton = document.querySelector<HTMLButtonElement>("#plc-disconnect-button")!;
  private readonly snapshotButton = document.querySelector<HTMLButtonElement>("#snapshot-button")!;

  async start(): Promise<void> {
    this.bindAuthForms();
    this.bindRuntimeControls();
    this.bindActivityReporting();
    try {
      this.config = await loadConfiguration();
      document.querySelector<HTMLElement>("#page-title")!.textContent = this.config.title;
      this.renderLayout();
    } catch (error) {
      this.setNotice(error instanceof Error ? error.message : "无法加载页面配置");
    }
  }

  private bindAuthForms(): void {
    const login = document.querySelector<HTMLFormElement>("#login-form")!;
    login.addEventListener("submit", (event) => {
      event.preventDefault();
      void this.login(formValue(login, "username"), formValue(login, "password"));
    });

    const initialAdmin = document.querySelector<HTMLFormElement>("#initial-admin-form")!;
    initialAdmin.addEventListener("submit", (event) => {
      event.preventDefault();
      void this.createInitialAdmin(
        formValue(initialAdmin, "username"),
        formValue(initialAdmin, "password"),
        formValue(initialAdmin, "confirmPassword")
      );
    });

    const password = document.querySelector<HTMLFormElement>("#password-form")!;
    password.addEventListener("submit", (event) => {
      event.preventDefault();
      void this.changePassword(
        formValue(password, "currentPassword"),
        formValue(password, "newPassword"),
        formValue(password, "confirmPassword")
      );
    });

    document.querySelector<HTMLButtonElement>("#logout-button")!.addEventListener("click", () => {
      void this.logout();
    });
  }

  private bindRuntimeControls(): void {
    this.plcScanButton.addEventListener("click", (event) => {
      if (this.activationFilter.accept(event) && this.canSendCommands()) {
        this.sendPLCScan();
      }
    });
    this.plcDisconnectButton.addEventListener("click", (event) => {
      if (this.activationFilter.accept(event) && this.canSendCommands()) {
        this.sendPLCDisconnect();
      }
    });
    this.snapshotButton.addEventListener("click", (event) => {
      if (this.activationFilter.accept(event) && this.canSendCommands()) {
        this.sendPointsSnapshotGet();
      }
    });
    this.setRuntimeControlsEnabled();
  }

  private bindActivityReporting(): void {
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

  private async login(username: string, password: string): Promise<void> {
    try {
      await postJSON("/api/v2/auth/login", { username, password });
      this.beginSession();
    } catch (error) {
      this.setNotice(error instanceof Error ? error.message : "登录失败");
    }
  }

  private async createInitialAdmin(username: string, password: string, confirmPassword: string): Promise<void> {
    if (password !== confirmPassword) {
      this.setNotice("两次输入的密码不一致");
      return;
    }
    try {
      await postJSON("/api/v2/auth/initial-admin", { username, password, confirmPassword });
      this.beginSession();
    } catch (error) {
      this.setNotice(error instanceof Error ? error.message : "创建管理员失败");
    }
  }

  private async changePassword(currentPassword: string, newPassword: string, confirmPassword: string): Promise<void> {
    if (newPassword !== confirmPassword) {
      this.setNotice("两次输入的新密码不一致");
      return;
    }
    try {
      await postJSON("/api/v2/auth/password", { currentPassword, newPassword, confirmPassword });
      this.setNotice("密码已修改");
    } catch (error) {
      this.setNotice(error instanceof Error ? error.message : "修改密码失败");
    }
  }

  private beginSession(): void {
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

  private async logout(): Promise<void> {
    try {
      await fetch("/api/v2/auth/logout", {
        method: "POST",
        credentials: "same-origin",
        cache: "no-store"
      });
    } finally {
      this.endSession("已退出登录");
    }
  }

  private endSession(message: string): void {
    this.signedIn = false;
    this.configured = false;
    this.discardTransientRuntime();
    this.closeSocket();
    this.authPanel.hidden = false;
    this.runtimePanel.hidden = true;
    this.setNotice(message);
    this.setStatus("请登录后连接设备");
  }

  private openSocket(): void {
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
      this.discardTransientRuntime();
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
      this.discardTransientRuntime();
      this.setCommandsEnabled();
      if (event.code === 4401) {
        this.endSession("会话已过期，请重新登录");
        return;
      }
      this.setStatus("连接中断");
      this.scheduleReconnect();
    });
  }

  private closeSocket(): void {
    if (this.reconnectTimer !== null) {
      window.clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    const socket = this.socket;
    this.socket = null;
    socket?.close();
  }

  private scheduleReconnect(): void {
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

  private handleSocketMessage(raw: unknown): void {
    if (typeof raw !== "string") {
      return;
    }
    let message: {
      type?: string;
      values?: Record<string, PointValue>;
      success?: boolean;
      error?: { code?: string; message?: string };
      devices?: PLCDevice[];
      deviceId?: string;
      state?: PLCDevice["state"];
    };
    try {
      message = JSON.parse(raw) as typeof message;
    } catch {
      this.setNotice("收到无法识别的服务消息");
      return;
    }
    if (message.type === "runtime.configured") {
      this.configured = true;
      this.setCommandsEnabled();
      this.setRuntimeControlsEnabled();
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
    if (message.type === "plc.scan.result" && message.success === true && Array.isArray(message.devices)) {
      this.plcDevices.splice(0, this.plcDevices.length, ...message.devices);
      this.setRuntimeControlsEnabled();
      this.setStatus("发现 " + String(this.plcDevices.length) + " 个 PLC 候选设备");
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
      this.setRuntimeControlsEnabled();
      this.setStatus(plcStateText(message.state, message.deviceId));
      return;
    }
    if (message.success === false) {
      this.setNotice(errorText(message.error?.code));
    }
  }

  private renderLayout(): void {
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
    this.setRuntimeControlsEnabled();
  }

  private renderBinding(binding: Binding): HTMLElement {
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

  private renderButton(binding: Binding): HTMLButtonElement {
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
        this.sendCommand(binding.writePoint!, "press");
      });
      const release = () => {
        if (!pressed) {
          return;
        }
        pressed = false;
        this.sendCommand(binding.writePoint!, "release");
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
        this.sendCommand(binding.writePoint!, binding.action);
      }
    });
    return button;
  }

  private renderPLCCandidates(): void {
    this.plcCandidates.replaceChildren();
    this.plcStatus.textContent = plcStateText(this.plcState);
    if (this.plcDevices.length === 0) {
      const empty = document.createElement("li");
      empty.textContent = "尚未扫描 PLC";
      this.plcCandidates.append(empty);
      return;
    }
    for (const device of this.plcDevices) {
      const row = document.createElement("li");
      const identity = document.createElement("strong");
      identity.textContent = device.deviceId;
      const detail = document.createElement("span");
      detail.textContent = device.name + " · " + device.address + " · " + plcStateText(device.state);
      const connect = document.createElement("button");
      connect.type = "button";
      connect.textContent = "连接";
      connect.disabled = !this.canSendCommands() || device.state === "connected" || device.state === "connecting";
      connect.addEventListener("click", (event) => {
        if (this.activationFilter.accept(event) && this.canSendCommands()) {
          this.sendPLCConnect(device.deviceId);
        }
      });
      row.append(identity, detail, connect);
      this.plcCandidates.append(row);
    }
  }

  private canSendCommands(): boolean {
    return this.configured && this.socket?.readyState === WebSocket.OPEN;
  }

  private sendPointsSnapshotGet(): void {
    this.sendRuntimeRequest(buildPointsSnapshotGet());
  }

  private sendPLCScan(): void {
    this.sendRuntimeRequest(buildPLCScan());
  }

  private sendPLCConnect(deviceId: string): void {
    this.sendRuntimeRequest(buildPLCConnect(deviceId));
  }

  private sendPLCDisconnect(): void {
    this.sendRuntimeRequest(buildPLCDisconnect());
  }

  private sendRuntimeRequest(message: object): void {
    if (!this.canSendCommands() || this.socket === null) {
      return;
    }
    this.socket.send(JSON.stringify(message));
  }

  private sendCommand(pointId: string, action: "pulse" | "press" | "release" | "toggle"): void {
    this.sendRuntimeRequest(buildPointCommand(pointId, action));
  }

  private renderValues(): void {
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

  private setCommandsEnabled(): void {
    const enabled = this.canSendCommands();
    for (const button of this.commandButtons) {
      button.disabled = !enabled;
    }
  }

  private setRuntimeControlsEnabled(): void {
    const enabled = this.canSendCommands();
    this.plcScanButton.disabled = !enabled;
    this.snapshotButton.disabled = !enabled;
    this.plcDisconnectButton.disabled = !enabled || this.plcState === "disconnected";
    this.renderPLCCandidates();
  }

  private discardTransientRuntime(): void {
    clearTransientRuntime(this.values, this.plcDevices);
    this.plcState = "disconnected";
    this.renderValues();
    this.setRuntimeControlsEnabled();
  }

  private setNotice(message: string): void {
    this.notice.textContent = message;
  }

  private setStatus(message: string): void {
    this.status.textContent = message;
  }
}

function formatValue(value: Scalar): string {
  if (value === null) {
    return "无";
  }
  return String(value);
}

function plcStateText(state: PLCDevice["state"], deviceId?: string): string {
  const text: Record<PLCDevice["state"], string> = {
    disconnected: "PLC 未连接",
    connecting: "正在连接 PLC",
    connected: "PLC 已连接",
    reconnecting: "PLC 正在重连",
    error: "PLC 连接错误"
  };
  return text[state] + (deviceId === undefined ? "" : "：" + deviceId);
}

function errorText(code: string | undefined): string {
  const messages: Record<string, string> = {
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

if (typeof document !== "undefined") {
  const hmi = new HMI();
  void hmi.start();
}
