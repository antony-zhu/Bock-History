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
  state: "unconfigured" | "disconnected" | "connecting" | "connected" | "reconnecting" | "error";
  selected: boolean;
  metadata: Record<string, unknown>;
};

type PointDefinition = {
  pointId: string;
  address: string;
  type: "bool" | "int" | "float" | "string" | "int16" | "uint16" | "int32" | "float32";
  access: "read" | "write" | "read_write";
  readPoint: string | null;
  writePoint: string | null;
  writeMethod: "maskWrite" | "fc06" | "fc10" | null;
  registerCount?: number;
  wordOrder?: "low-high" | "high-low";
  alarm?: AlarmDefinition;
  write?: {
    mode: "set" | "pulse" | "momentary" | "toggle";
    activeValue?: boolean | number;
    defaultValue?: boolean | number;
    pulseMs?: number;
  };
};

type AlarmDefinition = {
  normalValue: Scalar;
  alarmValue: Scalar;
  message: string;
  level: "danger" | "warning";
};

type Binding = {
  displayPath: string;
  description: string;
  component: "button" | "value" | "number";
  readPoint: string | null;
  writePoint?: string | null;
  action?: "pulse" | "momentary" | "toggle" | "set";
  permission?: "operate" | "maintenance";
  state?: "configured" | "pending";
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
  numericProfiles?: Record<string, unknown>;
};

type ActivationEvent = {
  type: string;
  timeStamp: number;
  detail: number;
  pointerId?: number;
};

type LegacyBin = {
  type: string;
  label: string;
};

type LegacyAlarm = {
  id: string;
  level: "danger" | "warning";
  text: string;
};

type LegacyHistory = {
  id: number;
  level: "danger" | "warning" | "info";
  code: string;
  text: string;
  time: string;
};

type LegacyState = {
  revision: number;
  running: boolean | null;
  mode: "auto" | "manual";
  singlePaused: boolean | null;
  framePaused: boolean | null;
  target: number | null;
  output: number | null;
  cycle: number | null;
  oee: number | null;
  inspected: number | null;
  passed: number | null;
  ng: number | null;
  pending: number | null;
  blank: number | null;
  finished: number | null;
  toolLimit: number | null;
  inspectInterval: number | null;
  bins: LegacyBin[];
  alarms: LegacyAlarm[];
  history: LegacyHistory[];
};

type LegacyBackend = {
  APIError: typeof HMIAPIError;
  getState(): Promise<{ state: LegacyState }>;
  sendCommand(command: string, payload?: Record<string, unknown>, context?: unknown): Promise<{ state: LegacyState }>;
  getAudit(options?: unknown): Promise<{ events: LegacyHistory[] }>;
  manual: {
    binding(displayPath: string): Binding | null;
    value(displayPath: string): Scalar | undefined;
    canWrite(displayPath: string): boolean;
    command(displayPath: string, value?: number): Promise<void>;
  };
  points: {
    binding(displayPath: string): Binding | null;
    value(displayPath: string): Scalar | undefined;
    canWrite(displayPath: string): boolean;
    command(displayPath: string, value?: number): Promise<void>;
  };
};

type SoftKeyboardMode = "soft" | "native";

type HMISoftKeyboard = {
  close(action?: "cancel" | "commit" | "keep"): boolean;
  getMode(): SoftKeyboardMode;
  init(): boolean;
  isOpen(): boolean;
  open(input: HTMLInputElement | HTMLTextAreaElement, options?: { immediate?: boolean }): boolean;
  setMode(mode: SoftKeyboardMode, persist?: boolean): SoftKeyboardMode;
  setPinned(pinned: boolean): void;
};

type HMIFrontendAuth = {
  hasPermission(permission: "operate" | "maintenance"): boolean;
  requirePermission(permission: "operate" | "maintenance"): boolean;
  permissions(): FrontendPermissions;
  role(): FrontendRole;
};

declare global {
  interface Window {
    HMIBackend?: LegacyBackend;
    HMISoftKeyboard?: HMISoftKeyboard;
    HMIFrontendAuth?: HMIFrontendAuth;
  }
}

const debounceMilliseconds = 50;
const defaultPLCAddressRange = "192.168.1.0/24";
const defaultPLCPort = 502;
const defaultPLCUnitID = 1;

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

function isIPv4Address(value: string): boolean {
  const parts = value.split(".");
  return parts.length === 4 && parts.every((part) => {
    if (!/^\d+$/.test(part)) {
      return false;
    }
    const number = Number(part);
    return Number.isInteger(number) && number >= 0 && number <= 255;
  });
}

function isIPv4CIDR(value: string): boolean {
  const [address, bits, ...extra] = value.split("/");
  return extra.length === 0 && isIPv4Address(address ?? "") && /^\d+$/.test(bits ?? "") && Number(bits) >= 0 && Number(bits) <= 32;
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
      ...(point.alarm === undefined ? {} : { alarm: {
        normalValue: point.alarm.normalValue,
        alarmValue: point.alarm.alarmValue,
        message: point.alarm.message,
        level: point.alarm.level
      } }),
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

export function buildPLCScan(
  addressRange = defaultPLCAddressRange,
  port = defaultPLCPort,
  unitID = defaultPLCUnitID,
  id = requestID(),
  timestamp = new Date().toISOString()
): object {
  return request("plc.scan", { addressRange, port, unitId: unitID }, id, timestamp);
}

export function buildPLCDeviceID(host: string, port = defaultPLCPort, unitID = defaultPLCUnitID): string {
  return "easy521://" + host + ":" + String(port) + "?unitId=" + String(unitID);
}

export function buildPLCConnect(deviceID: string, id = requestID(), timestamp = new Date().toISOString()): object {
  return request("plc.connect", { deviceId: deviceID }, id, timestamp);
}

export function buildPLCDisconnect(id = requestID(), timestamp = new Date().toISOString()): object {
  return request("plc.disconnect", {}, id, timestamp);
}

export function buildPointCommand(
  pointID: string,
  action: "pulse" | "press" | "release" | "toggle" | "set",
  id = requestID(),
  timestamp = new Date().toISOString(),
  value?: Scalar
): object {
  return request("point.command", {
    pointId: pointID,
    action,
    ...(action === "set" ? { value } : {})
  }, id, timestamp);
}

export function applyAbsoluteValues(target: Map<string, PointValue>, values: Record<string, PointValue>): void {
  for (const [pointID, pointValue] of Object.entries(values)) {
    target.set(pointID, { ...pointValue });
  }
}

function latestPointTime(values: Record<string, PointValue>): string | null {
  let latest: string | null = null;
  for (const point of Object.values(values)) {
    if (typeof point.updatedAt === "string" && (latest === null || point.updatedAt > latest)) {
      latest = point.updatedAt;
    }
  }
  return latest;
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
  if (config.scanIntervalMs !== 500 || !Array.isArray(config.points) || !Array.isArray(config.bindings) || !Array.isArray(config.layout)) {
    throw new Error("points.json 缺少完整点位或页面配置");
  }
  for (const binding of config.bindings) {
    if (!isDisplayPath(binding.displayPath) || !/[\u3400-\u9fff]/.test(binding.description)) {
      throw new Error("bindings 的 displayPath 必须为英文点路径，description 必须为中文");
    }
  }
  return config as PageConfiguration;
}

function demoConfiguration(): PageConfiguration {
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
        bindings: ["home.machine.start", "home.machine.jog.forward"]
      },
      {
        displayPath: "home.machine.status",
        description: "设备状态",
        bindings: ["home.machine.start.feedback"]
      }
    ]
  };
}

async function loadConfiguration(demo: boolean): Promise<PageConfiguration> {
  try {
    const response = await fetch(new URL("./points.json", import.meta.url), { cache: "no-store" });
    if (!response.ok) {
      throw new Error("无法读取 points.json");
    }
    return configurationFrom(await response.json());
  } catch (error) {
    if (demo) {
      return demoConfiguration();
    }
    throw error;
  }
}

function websocketURL(): string {
  if (window.location.protocol !== "https:") {
    throw new Error("Block HMI requires HTTPS before opening WSS");
  }
  return "wss://" + window.location.host + "/ws";
}

function isDemoMode(): boolean {
  return new URLSearchParams(window.location.search).get("demo") === "1";
}

type DemoAuthPreview = "login" | "bootstrap" | null;
type AuthScreen = Exclude<DemoAuthPreview, null>;
type FrontendPermissions = { operate: boolean; maintenance: boolean };
type BackendIdentity = {
  username: string;
  role: "VIEWER" | "OPERATOR" | "ADMIN";
  permissions: FrontendPermissions;
};
type FrontendRole = BackendIdentity["role"] | "GUEST";
export type FrontendSession = {
  username: string;
  role: BackendIdentity["role"];
  permissions: FrontendPermissions;
  expiresAt: number;
};
export const defaultIdleTimeoutSeconds = 300;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function validPermissions(value: unknown): value is FrontendPermissions {
  return isRecord(value) && typeof value.operate === "boolean" && typeof value.maintenance === "boolean";
}

function backendIdentityFrom(value: unknown): BackendIdentity | null {
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

function idleTimeoutFrom(value: unknown): number | null {
  if (!isRecord(value) || !Number.isInteger(value.idleTimeoutSeconds)) {
    return null;
  }
  const timeout = Number(value.idleTimeoutSeconds);
  return timeout >= 60 && timeout <= 3600 ? timeout : null;
}

export function frontendSessionIsActive(session: FrontendSession | null, now = Date.now()): boolean {
  return session !== null && session.expiresAt > now;
}

export function renewFrontendSession(session: FrontendSession | null, idleTimeoutSeconds: number, now = Date.now()): FrontendSession | null {
  if (session === null || !frontendSessionIsActive(session, now)) {
    return null;
  }
  return { ...session, expiresAt: now + idleTimeoutSeconds * 1000 };
}

export function demoAuthPreviewFromSearch(search: string): DemoAuthPreview {
  const query = new URLSearchParams(search);
  if (query.get("demo") !== "1") {
    return null;
  }
  const auth = query.get("auth");
  return auth === "login" || auth === "bootstrap" ? auth : null;
}

export function demoManualRoleFromSearch(search: string): FrontendRole {
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

function demoAuthPreviewMode(): DemoAuthPreview {
  return demoAuthPreviewFromSearch(window.location.search);
}

function demoManualRole(): FrontendRole {
  return demoManualRoleFromSearch(window.location.search);
}

function cloneState(state: LegacyState): LegacyState {
  return JSON.parse(JSON.stringify(state)) as LegacyState;
}

function initialDemoState(): LegacyState {
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
    alarms: [],
    history: []
  };
}

class HMIAPIError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(message: string, status = 0, code = "request_failed") {
    super(message);
    this.name = "APIError";
    this.status = status;
    this.code = code;
  }
}

type PointResultMessage = {
  type?: string;
  requestId?: string;
  success?: boolean;
  error?: { code?: string; message?: string };
};

type PendingPointCommand = {
  requestID: string;
  timeout: ReturnType<typeof setTimeout>;
  resolve: () => void;
  reject: (error: HMIAPIError) => void;
};

export const pointCommandResultTimeoutMilliseconds = 5000;

// Keep one point command/result pair explicit instead of introducing a command queue.
export class PointCommandReceipt {
  private pending: PendingPointCommand | null = null;

  constructor(private readonly timeoutMilliseconds = pointCommandResultTimeoutMilliseconds) {}

  waitFor(requestID: string): Promise<void> {
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

  dispatch(requestID: string, send: () => void): Promise<void> {
    const confirmation = this.waitFor(requestID);
    try {
      send();
    } catch {
      this.cancel("现场操作未发送，结果未知", 503, "network_error");
    }
    return confirmation;
  }

  receive(message: PointResultMessage): boolean {
    const pending = this.pending;
    if (pending === null || message.type !== "point.result" || message.requestId !== pending.requestID) {
      return false;
    }
    clearTimeout(pending.timeout);
    this.pending = null;
    if (message.success === true) {
      pending.resolve();
    } else {
      const code = message.error?.code ?? "point_command_failed";
      pending.reject(new HMIAPIError(message.error?.message ?? errorText(code), 502, code));
    }
    return true;
  }

  cancel(message: string, status: number, code: string): void {
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
  private socket: WebSocket | null = null;
  private signedIn = false;
  private session: FrontendSession | null = null;
  private bootstrapRequired = false;
  private loginInFlight = false;
  private initialAdminInFlight = false;
  private idleTimeoutSeconds = defaultIdleTimeoutSeconds;
  private configured = false;
  private reconnectDelay = 1000;
  private reconnectTimer: number | null = null;
  private sessionExpiryTimer: number | null = null;
  private revision = 0;
  private plcState: PLCDevice["state"] = "disconnected";
  private lastPLCSampleAt: string | null = null;
  private lastPLCError = "";
  private deferredLiveState = false;
  private readonly values = new Map<string, PointValue>();
  private readonly plcDevices: PLCDevice[] = [];
  private readonly pendingPointCommand = new PointCommandReceipt();
  private demoState = initialDemoState();
  private authKeyboardOriginalMode: SoftKeyboardMode | null = null;

  constructor(
    private readonly config: PageConfiguration,
    private readonly demo: boolean,
    private readonly authPreview: DemoAuthPreview,
    private readonly manualRole: FrontendRole
  ) {}

  async start(): Promise<void> {
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
    } else {
      this.setPLCStatus("正在连接本机服务");
      this.renderPLCCandidates();
      this.openSocket();
    }
    if (this.authPreview !== null) {
      this.openAuthWithKeyboard(this.authPreview === "bootstrap" ? "bootstrap" : this.authenticationScreen());
    }
  }

  backend(): LegacyBackend {
    return {
      APIError: HMIAPIError,
      getState: () => this.getState(),
      sendCommand: (command, payload) => this.sendCommand(command, payload),
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

  private authPanel(): HTMLElement {
    return document.querySelector<HTMLElement>("#auth-panel")!;
  }

  private authNotice(): HTMLElement {
    return document.querySelector<HTMLElement>("#authNotice")!;
  }

  private loginSection(): HTMLElement {
    return document.querySelector<HTMLElement>("#authLogin")!;
  }

  private bootstrapSection(): HTMLElement {
    return document.querySelector<HTMLElement>("#authBootstrap")!;
  }

  private authenticationScreen(): AuthScreen {
    return this.bootstrapRequired ? "bootstrap" : "login";
  }

  private async authRequest(path: string, method: "GET" | "POST" | "PUT", body?: Record<string, unknown>): Promise<{ response: Response; value: unknown }> {
    const response = await fetch(path, {
      method,
      cache: "no-store",
      headers: body === undefined ? undefined : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body)
    });
    const value = response.status === 204 ? null : await response.json().catch(() => null);
    return { response, value };
  }

  private async loadAuthenticationState(): Promise<void> {
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
    } catch {
      this.bootstrapRequired = false;
      this.idleTimeoutSeconds = defaultIdleTimeoutSeconds;
      this.emitPageNotice("无法读取本机登录配置", "danger");
    }
    this.updateIdleTimeoutInput();
  }

  private updateIdleTimeoutInput(): void {
    const input = document.querySelector<HTMLInputElement>("#authAccount [name=\"idleTimeoutSeconds\"]");
    if (input !== null) {
      input.value = String(this.idleTimeoutSeconds);
    }
  }

  private setHMIInteractive(interactive: boolean): void {
    document.querySelectorAll<HTMLElement>("#hmi-topbar, #hmi-pages, #hmi-footer").forEach((element) => {
      element.toggleAttribute("inert", !interactive);
      if (interactive) {
        element.removeAttribute("aria-hidden");
      } else {
        element.setAttribute("aria-hidden", "true");
      }
    });
  }

  private setAuthNotice(message: string): void {
    const authenticationVisible = !this.authPanel().hidden && this.authPanel().hasAttribute("data-auth-mode");
    this.authNotice().textContent = authenticationVisible ? "" : message;
    const maintenanceNotice = document.querySelector<HTMLElement>("#local-admin-notice");
    if (maintenanceNotice !== null) {
      maintenanceNotice.textContent = message;
    }
    if (authenticationVisible && message !== "") {
      this.emitPageNotice(message, "danger");
    }
  }

  private prepareGuestHMI(): void {
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

  private openAuthWithKeyboard(screen: AuthScreen, message = ""): void {
    const keyboard = window.HMISoftKeyboard;
    keyboard?.init();
    this.endAuthenticationKeyboard();
    const panel = this.authPanel();
    panel.hidden = false;
    panel.setAttribute("aria-busy", "false");
    panel.setAttribute("data-auth-mode", screen);
    document.querySelector<HTMLElement>("#authTitle")!.textContent = screen === "bootstrap" ? "创建管理员" : "登录";
    this.loginSection().hidden = screen !== "login";
    this.bootstrapSection().hidden = screen !== "bootstrap";
    this.setAuthNotice(message);
    const form = document.querySelector<HTMLFormElement>(screen === "bootstrap" ? "#initial-admin-form" : "#login-form")!;
    const input = form.querySelector<HTMLInputElement>("[data-soft-keyboard]")!;
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

  private showLogin(message = ""): void {
    this.openAuthWithKeyboard("login", message);
  }

  private showBootstrap(message = ""): void {
    this.openAuthWithKeyboard("bootstrap", message);
  }

  private endAuthenticationKeyboard(): void {
    const keyboard = window.HMISoftKeyboard;
    keyboard?.setPinned(false);
    keyboard?.close("keep");
    if (this.authKeyboardOriginalMode === "native") {
      keyboard?.setMode("native", false);
    }
    this.authKeyboardOriginalMode = null;
  }

  private bindAuthForms(): void {
    const login = document.querySelector<HTMLFormElement>("#login-form")!;
    const submitLogin = () => {
      void this.login(formValue(login, "username"), formValue(login, "password"));
    };
    login.addEventListener("submit", (event) => {
      event.preventDefault();
      submitLogin();
    });
    login.addEventListener("hmi-soft-keyboard-submit", submitLogin);

    const initialAdmin = document.querySelector<HTMLFormElement>("#initial-admin-form")!;
    const submitInitialAdmin = () => {
      void this.createInitialAdmin(
        formValue(initialAdmin, "username"),
        formValue(initialAdmin, "password"),
        formValue(initialAdmin, "confirmPassword")
      );
    };
    initialAdmin.addEventListener("submit", (event) => {
      event.preventDefault();
      submitInitialAdmin();
    });
    initialAdmin.addEventListener("hmi-soft-keyboard-submit", submitInitialAdmin);

    const password = document.querySelector<HTMLFormElement>("#password-form")!;
    password.addEventListener("submit", (event) => {
      event.preventDefault();
      void this.changePassword(
        formValue(password, "currentPassword"),
        formValue(password, "newPassword"),
        formValue(password, "confirmPassword")
      );
    });

    const policy = document.querySelector<HTMLFormElement>("#session-policy-form")!;
    policy.addEventListener("submit", (event) => {
      event.preventDefault();
      void this.saveSessionPolicy(Number(formValue(policy, "idleTimeoutSeconds")));
    });
  }

  private setAuthSubmitBusy(form: HTMLFormElement, busy: boolean): void {
    form.setAttribute("aria-busy", String(busy));
    const submit = form.querySelector<HTMLButtonElement>('[type="submit"]');
    if (submit !== null) {
      submit.disabled = busy;
    }
  }

  private bindPasswordVisibilityToggles(): void {
    document.querySelectorAll<HTMLButtonElement>("[data-password-toggle]").forEach((toggle) => {
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

  private bindAccountControls(): void {
    const operator = document.querySelector<HTMLElement>("#operatorName")!;
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

  private moveLocalAdministrationToMaintenance(): void {
    const account = document.querySelector<HTMLElement>("#authAccount")!;
    const maintenance = document.querySelector<HTMLElement>("#accountSettingsPanel")!;
    document.querySelector<HTMLElement>("#auth-close")?.remove();
    document.querySelector<HTMLElement>("#logout-button")?.remove();
    const notice = document.createElement("p");
    notice.className = "settings-validation";
    notice.id = "local-admin-notice";
    notice.setAttribute("role", "status");
    notice.setAttribute("aria-live", "polite");
    account.prepend(notice);
    const idleTimeout = account.querySelector<HTMLInputElement>("[name=\"idleTimeoutSeconds\"]");
    if (idleTimeout !== null) {
      idleTimeout.value = String(this.idleTimeoutSeconds);
    }
    account.hidden = false;
    if (account.parentElement !== maintenance) {
      maintenance.append(account);
    }
  }

  private bindPLCControls(): void {
    document.querySelector<HTMLButtonElement>("#plc-scan-button")!.addEventListener("click", () => {
      this.sendPLCScan();
    });
    document.querySelector<HTMLButtonElement>("#savePlcButton")!.addEventListener("click", () => {
      this.sendPLCSave();
    });
    document.querySelector<HTMLButtonElement>("#plc-disconnect-button")!.addEventListener("click", () => {
      this.sendPLCDisconnect();
    });
    document.querySelector<HTMLButtonElement>("#snapshot-button")!.addEventListener("click", () => {
      this.sendPointsSnapshotGet();
    });
  }

  private bindActivityReporting(): void {
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

  private async login(username: string, password: string): Promise<void> {
    if (username.trim() === "" || password === "") {
      this.finishLoginAttempt("登录失败");
      return;
    }
    if (this.loginInFlight) {
      return;
    }
    this.loginInFlight = true;
    const form = document.querySelector<HTMLFormElement>("#login-form")!;
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
    } catch {
      this.finishLoginAttempt("无法连接本机登录服务");
    } finally {
      this.loginInFlight = false;
      this.setAuthSubmitBusy(form, false);
    }
  }

  private finishLoginAttempt(message: string): void {
    this.becomeGuest();
    this.emitPageNotice(message, "danger");
  }

  private emitPageNotice(message: string, level: "info" | "danger" = "info"): void {
    window.dispatchEvent(new CustomEvent("block-hmi-notice", { detail: { message, level } }));
  }

  private async createInitialAdmin(username: string, password: string, confirmPassword: string): Promise<void> {
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
    const form = document.querySelector<HTMLFormElement>("#initial-admin-form")!;
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
    } catch {
      this.setAuthNotice("无法连接本机登录服务");
    } finally {
      this.initialAdminInFlight = false;
      this.setAuthSubmitBusy(form, false);
    }
  }

  private async changePassword(currentPassword: string, newPassword: string, confirmPassword: string): Promise<void> {
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
    } catch {
      this.setAuthNotice("无法连接本机登录服务");
    }
  }

  private async saveSessionPolicy(idleTimeoutSeconds: number): Promise<void> {
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
    } catch {
      this.setAuthNotice("无法连接本机登录服务");
    }
  }

  private beginSession(identity: BackendIdentity): void {
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

  private logout(): void {
    this.endAuthenticationKeyboard();
    this.pendingPointCommand.cancel("已退出登录，现场操作结果未知", 401, "unauthenticated");
    this.becomeGuest();
  }

  private becomeGuest(): void {
    this.endAuthenticationKeyboard();
    this.signedIn = false;
    this.session = null;
    if (this.sessionExpiryTimer !== null) {
      window.clearTimeout(this.sessionExpiryTimer);
      this.sessionExpiryTimer = null;
    }
    this.authPanel().hidden = true;
    if (document.querySelector<HTMLElement>("[data-page=\"maintenance\"]")?.hidden === false) {
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

  private openSocket(): void {
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

  private closeSocket(): void {
    if (this.reconnectTimer !== null) {
      window.clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.pendingPointCommand.cancel("本机服务连接已关闭，现场操作结果未知", 503, "network_error");
    const socket = this.socket;
    this.socket = null;
    socket?.close();
  }

  private scheduleReconnect(): void {
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

  private refreshFrontendSession(): boolean {
    const renewed = renewFrontendSession(this.session, this.idleTimeoutSeconds);
    if (renewed === null) {
      this.becomeGuest();
      return false;
    }
    this.session = renewed;
    this.scheduleSessionExpiry();
    return true;
  }

  private scheduleSessionExpiry(): void {
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

  private toggleAccountSession(): void {
    if (this.signedIn) {
      this.logout();
      return;
    }
    this.openAuthWithKeyboard(this.authenticationScreen());
  }

  private hasPermission(permission: "operate" | "maintenance"): boolean {
    return this.signedIn && this.session !== null && this.session.permissions[permission];
  }

  private frontendRole(): FrontendRole {
    return this.signedIn && this.session !== null ? this.session.role : "GUEST";
  }

  private requirePermission(permission: "operate" | "maintenance"): boolean {
    if (this.hasPermission(permission)) {
      return true;
    }
    this.openAuthWithKeyboard(this.authenticationScreen());
    return false;
  }

  private updateAccountControl(): void {
    const operator = document.querySelector<HTMLElement>("#operatorName")!;
    const label = operator.parentElement?.querySelector<HTMLElement>(".meta-cn") ?? null;
    operator.textContent = this.signedIn && this.session !== null ? this.session.username : "登录";
    operator.setAttribute("aria-label", this.signedIn ? "点击退出本机登录" : "点击登录本机管理员");
    if (label !== null) {
      label.textContent = this.signedIn ? "管理员" : "登录";
    }
  }

  private emitPermissionChange(): void {
    window.dispatchEvent(new CustomEvent("block-hmi-auth-changed", {
      detail: {
        signedIn: this.signedIn,
        role: this.frontendRole(),
        operate: this.hasPermission("operate"),
        maintenance: this.hasPermission("maintenance")
      }
    }));
  }

  private handleSocketMessage(raw: unknown): void {
    if (typeof raw !== "string") {
      return;
    }
    let message: {
      type?: string;
      requestId?: string;
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

  private getState(): Promise<{ state: LegacyState }> {
    if (!this.demo && !this.canSendRuntime()) {
      return Promise.reject(new HMIAPIError("本机服务正在连接", 503, "runtime_unavailable"));
    }
    if (!this.demo) {
      this.deferProductionPolicy();
    }
    return Promise.resolve({ state: cloneState(this.currentState()) });
  }

  private currentState(): LegacyState {
    if (this.demo) {
      return this.demoState;
    }
    const state = initialDemoState();
    const manual = this.valueFor("footer.mode.manual");
    const singlePaused = this.valueFor("home.cycle.single");
    const framePaused = this.valueFor("home.cycle.frame");
    state.revision = this.revision;
    state.running = null;
    state.mode = manual === true ? "manual" : "auto";
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
    state.alarms = this.activeAlarms();
    state.history = [];
    return state;
  }

  private activeAlarms(): LegacyAlarm[] {
    return this.config.points.flatMap((point) => {
      const alarm = point.alarm;
      const value = point.readPoint === null ? undefined : this.values.get(point.readPoint);
      if (alarm === undefined || value?.value !== true) {
        return [];
      }
      return [{ id: point.address, level: alarm.level, text: alarm.message }];
    });
  }

  private numberFor(displayPath: string): number | null {
    const value = this.valueFor(displayPath);
    return typeof value === "number" && Number.isFinite(value) ? value : null;
  }

  private valueFor(displayPath: string): Scalar | undefined {
    const binding = this.config.bindings.find((item) => item.displayPath === displayPath);
    if (binding === undefined || binding.readPoint === null) {
      return undefined;
    }
    const point = this.values.get(binding.readPoint);
    return point?.quality === "good" ? point.value : undefined;
  }

  private pointBinding(displayPath: string): Binding | null {
    const binding = this.config.bindings.find((item) => item.displayPath === displayPath);
    return binding === undefined ? null : { ...binding };
  }

  private pointValue(displayPath: string): Scalar | undefined {
    return this.valueFor(displayPath);
  }

  private pointCanWrite(displayPath: string): boolean {
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

  private pointCommand(displayPath: string, value?: number): Promise<void> {
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
      this.socket!.send(JSON.stringify(buildPointCommand(
        writePoint,
        action,
        requestId,
        undefined,
        action === "set" ? value : undefined
      )));
    });
  }

  private manualBinding(displayPath: string): Binding | null {
    return this.pointBinding(displayPath);
  }

  private manualValue(displayPath: string): Scalar | undefined {
    return this.pointValue(displayPath);
  }

  private manualCanWrite(displayPath: string): boolean {
    return this.pointCanWrite(displayPath);
  }

  private manualCommand(displayPath: string, value?: number): Promise<void> {
    return this.pointCommand(displayPath, value);
  }

  private sendCommand(command: string, payload: Record<string, unknown> = {}): Promise<{ state: LegacyState }> {
    if (!this.requirePermission("operate")) {
      return Promise.reject(new HMIAPIError("请登录管理员后执行现场操作", 403, "permission_denied"));
    }
    if (this.demo) {
      return Promise.resolve({ state: this.applyDemoCommand(command, payload) });
    }
    const operation = command === "start"
      ? { displayPath: "home.machine.start", action: "pulse" as const, name: "启动" }
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
      this.socket!.send(JSON.stringify(buildPointCommand(pointID, operation.action, requestId)));
    });
    return confirmation.then(() => ({ state: cloneState(this.currentState()) }));
  }

  private applyDemoCommand(command: string, payload: Record<string, unknown>): LegacyState {
    const state = cloneState(this.demoState);
    if (command === "start") {
      state.running = true;
    } else if (command === "pause") {
      state.running = false;
    } else if (command === "reset") {
      state.output = 0;
      state.cycle = 0;
    } else if (command === "inspect") {
      state.inspected = (state.inspected ?? 0) + 1;
      state.passed = (state.passed ?? 0) + 1;
    } else if (command === "clear_bins") {
      state.bins = state.bins.map(() => ({ type: "empty", label: "已清空" }));
    } else if (command === "set_single_paused") {
      state.singlePaused = Boolean(payload.paused);
    } else if (command === "set_frame_paused") {
      state.framePaused = Boolean(payload.paused);
    }
    state.revision += 1;
    this.demoState = state;
    this.emitState();
    return cloneState(state);
  }

  private sendPLCScan(): void {
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

  private sendPLCSave(): void {
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

  private sendPLCConnect(deviceID: string): void {
    if (!this.requirePermission("maintenance")) {
      return;
    }
    if (this.demo) {
      return;
    }
    this.populatePLCInputs(deviceID);
    this.sendRuntimeRequest(buildPLCConnect(deviceID));
  }

  private sendPLCDisconnect(): void {
    if (!this.requirePermission("maintenance")) {
      return;
    }
    if (this.demo) {
      return;
    }
    this.sendRuntimeRequest(buildPLCDisconnect());
  }

  private sendPointsSnapshotGet(): void {
    if (!this.requirePermission("maintenance")) {
      return;
    }
    if (this.demo) {
      this.emitState();
      return;
    }
    this.sendRuntimeRequest(buildPointsSnapshotGet());
  }

  private sendRuntimeRequest(message: object): void {
    if (!this.canSendRuntime() || this.socket === null) {
      return;
    }
    this.socket.send(JSON.stringify(message));
  }

  private readPLCScanSettings(): { addressRange: string; port: number; unitID: number } | null {
    const addressRange = document.querySelector<HTMLInputElement>("#plcSubnetInput")!.value.trim();
    if (!isIPv4CIDR(addressRange)) {
      this.setPLCStatus("请输入有效的独立子网（IPv4 CIDR）");
      return null;
    }
    const transport = this.readPLCTransportSettings();
    return transport === null ? null : { addressRange, ...transport };
  }

  private readPLCEndpoint(): { host: string; port: number; unitID: number } | null {
    const host = document.querySelector<HTMLInputElement>("#plcHostInput")!.value.trim();
    if (!isIPv4Address(host)) {
      this.setPLCStatus("请输入有效的 PLC IP");
      return null;
    }
    const transport = this.readPLCTransportSettings();
    return transport === null ? null : { host, ...transport };
  }

  private readPLCTransportSettings(): { port: number; unitID: number } | null {
    const port = Number(document.querySelector<HTMLInputElement>("#plcPortInput")!.value);
    const unitID = Number(document.querySelector<HTMLInputElement>("#plcUnitInput")!.value);
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

  private populatePLCInputs(deviceID: string): void {
    let parsed: URL;
    try {
      parsed = new URL(deviceID);
    } catch {
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
    document.querySelector<HTMLInputElement>("#plcHostInput")!.value = parsed.hostname;
    document.querySelector<HTMLInputElement>("#plcPortInput")!.value = String(port);
    document.querySelector<HTMLInputElement>("#plcUnitInput")!.value = String(unitID);
  }

  private canSendRuntime(): boolean {
    return this.configured && (this.demo || this.socket?.readyState === WebSocket.OPEN);
  }

  private setPLCStatus(message: string): void {
    document.querySelector<HTMLElement>("#plc-status")!.textContent = message;
    if (this.isUserInputActive()) {
      this.deferredLiveState = true;
      return;
    }
    this.renderPLCReadOnly();
  }

  private isUserInputActive(): boolean {
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

  private publishLiveState(force = false): void {
    if (this.isUserInputActive() && !force) {
      this.deferredLiveState = true;
      return;
    }
    this.deferredLiveState = false;
    this.renderPLCReadOnly();
    this.emitState(force);
  }

  private flushDeferredLiveState(): boolean {
    if (!this.deferredLiveState || this.isUserInputActive()) {
      return false;
    }
    this.deferredLiveState = false;
    this.renderPLCReadOnly();
    this.emitState();
    return true;
  }

  private renderPLCReadOnly(): void {
    const connection = document.querySelector<HTMLElement>("#plc-connection-value");
    if (connection === null) {
      return;
    }
    connection.textContent = plcStateText(this.plcState);
  }

  private renderPLCCandidates(): void {
    const list = document.querySelector<HTMLElement>("#plc-candidates")!;
    const scan = document.querySelector<HTMLButtonElement>("#plc-scan-button")!;
    const save = document.querySelector<HTMLButtonElement>("#savePlcButton")!;
    const disconnect = document.querySelector<HTMLButtonElement>("#plc-disconnect-button")!;
    const snapshot = document.querySelector<HTMLButtonElement>("#snapshot-button")!;
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

  private emitState(force = false): void {
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

  private deferProductionPolicy(): void {
    if (this.demo) {
      return;
    }
    window.setTimeout(() => {
      const runtimeEnabled = this.canSendRuntime();
      document.querySelectorAll<HTMLButtonElement>(".control-button").forEach((button) => {
        const displayPath = button.dataset.pointAction;
        const configured = displayPath !== undefined && this.config.bindings.some((binding) =>
          binding.displayPath === displayPath && binding.state !== "pending" && binding.writePoint !== null && binding.action === "pulse"
        );
        const available = runtimeEnabled && configured;
        button.dataset.backendUnavailable = available ? "false" : "true";
      });
    }, 0);
  }
}

function formValue(form: HTMLFormElement, name: string): string {
  return String(new FormData(form).get(name) ?? "");
}

function plcStateText(state: PLCDevice["state"], deviceID?: string): string {
  const text: Record<PLCDevice["state"], string> = {
    unconfigured: "PLC 尚未配置",
    disconnected: "PLC 未连接",
    connecting: "正在连接 PLC",
    connected: "PLC 已连接",
    reconnecting: "PLC 正在重连",
    error: "PLC 连接错误"
  };
  return text[state] + (deviceID === undefined ? "" : "：" + deviceID);
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

export async function installHMIBackend(): Promise<LegacyBackend> {
  const demo = isDemoMode();
  const bridge = new AppleBridge(await loadConfiguration(demo), demo, demoAuthPreviewMode(), demoManualRole());
  await bridge.start();
  const backend = bridge.backend();
  window.HMIBackend = backend;
  return backend;
}
