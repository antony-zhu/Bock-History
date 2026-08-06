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

type LegacyBin = {
  type: string;
  label: string;
};

type LegacyAlarm = {
  id: number;
  level: "danger" | "warning" | "info";
  code: string;
  text: string;
  time: string;
  acknowledged: boolean;
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
  running: boolean;
  mode: "auto" | "manual";
  singlePaused: boolean;
  framePaused: boolean;
  target: number;
  output: number;
  cycle: number;
  oee: number;
  inspected: number;
  passed: number;
  ng: number;
  pending: number;
  blank: number;
  finished: number;
  toolLimit: number;
  inspectInterval: number;
  bins: LegacyBin[];
  alarms: LegacyAlarm[];
  history: LegacyHistory[];
};

type LegacyBackend = {
  APIError: typeof HMIAPIError;
  getState(): Promise<{ state: LegacyState }>;
  sendCommand(command: string, payload?: Record<string, unknown>, context?: unknown): Promise<{ state: LegacyState }>;
  acknowledgeAlarm(alarmID: number, context?: unknown): Promise<{ state: LegacyState }>;
  getAudit(options?: unknown): Promise<{ events: LegacyHistory[] }>;
};

type SoftKeyboardMode = "soft" | "native";

type HMISoftKeyboard = {
  close(action?: "cancel" | "commit" | "keep"): boolean;
  getMode(): SoftKeyboardMode;
  open(input: HTMLInputElement | HTMLTextAreaElement): boolean;
  setMode(mode: SoftKeyboardMode, persist?: boolean): SoftKeyboardMode;
  setPinned(pinned: boolean): void;
};

type HMIFrontendAuth = {
  hasPermission(permission: "operate" | "maintenance"): boolean;
  requirePermission(permission: "operate" | "maintenance"): boolean;
  permissions(): FrontendPermissions;
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

export function buildPLCConnect(deviceID: string, id = requestID(), timestamp = new Date().toISOString()): object {
  return request("plc.connect", { deviceId: deviceID }, id, timestamp);
}

export function buildPLCDisconnect(id = requestID(), timestamp = new Date().toISOString()): object {
  return request("plc.disconnect", {}, id, timestamp);
}

export function buildPointCommand(
  pointID: string,
  action: "pulse" | "press" | "release" | "toggle",
  id = requestID(),
  timestamp = new Date().toISOString()
): object {
  return request("point.command", { pointId: pointID, action }, id, timestamp);
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
  if (config.scanIntervalMs !== 50 || !Array.isArray(config.points) || !Array.isArray(config.bindings) || !Array.isArray(config.layout)) {
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

async function loadConfiguration(demo: boolean): Promise<PageConfiguration> {
  if (demo) {
    return demoConfiguration();
  }
  const response = await fetch(new URL("./points.json", import.meta.url), { cache: "no-store" });
  if (!response.ok) {
    throw new Error("无法读取 points.json");
  }
  return configurationFrom(await response.json());
}

function websocketURL(): string {
  const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
  return scheme + "//" + window.location.host + "/ws";
}

function isDemoMode(): boolean {
  return new URLSearchParams(window.location.search).get("demo") === "1";
}

type DemoAuthPreview = "login" | "bootstrap" | null;
type AuthScreen = Exclude<DemoAuthPreview, null>;
type StorageReader = () => Pick<Storage, "getItem" | "setItem" | "removeItem"> | null | undefined;
type FrontendPermissions = { operate: boolean; maintenance: boolean };
type LocalAdministrator = {
  username: string;
  passwordHash: string;
  permissions: FrontendPermissions;
};
type LocalSession = {
  username: string;
  permissions: FrontendPermissions;
  lastActivity: number;
  expiresAt: number;
};
type LocalSettings = { idleTimeoutSeconds: number };

export const localAdminStorageKey = "block-hmi-local-admin-v1";
export const localSessionStorageKey = "block-hmi-local-session-v1";
export const localSettingsStorageKey = "block-hmi-local-settings-v1";
export const defaultIdleTimeoutSeconds = 300;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function validPermissions(value: unknown): value is FrontendPermissions {
  return isRecord(value) && typeof value.operate === "boolean" && typeof value.maintenance === "boolean";
}

export function localAdministratorFrom(value: unknown): LocalAdministrator | null {
  if (!isRecord(value) ||
    typeof value.username !== "string" || value.username.trim() === "" ||
    !/^[a-f0-9]{64}$/.test(String(value.passwordHash)) ||
    !validPermissions(value.permissions)) {
    return null;
  }
  return {
    username: value.username,
    passwordHash: String(value.passwordHash),
    permissions: { ...value.permissions }
  };
}

export function localSessionFrom(value: unknown): LocalSession | null {
  if (!isRecord(value) || typeof value.username !== "string" || !validPermissions(value.permissions) ||
    !Number.isFinite(value.lastActivity) || !Number.isFinite(value.expiresAt)) {
    return null;
  }
  return {
    username: value.username,
    permissions: { ...value.permissions },
    lastActivity: Number(value.lastActivity),
    expiresAt: Number(value.expiresAt)
  };
}

export function readLocalAdministrator(storage: StorageReader): LocalAdministrator | null {
  try {
    const raw = storage()?.getItem(localAdminStorageKey);
    return raw === null || raw === undefined ? null : localAdministratorFrom(JSON.parse(raw));
  } catch {
    return null;
  }
}

export function readLocalSettings(storage: StorageReader): LocalSettings {
  try {
    const raw = storage()?.getItem(localSettingsStorageKey);
    const value = raw === null || raw === undefined ? null : JSON.parse(raw);
    if (isRecord(value) && Number.isInteger(value.idleTimeoutSeconds) && Number(value.idleTimeoutSeconds) >= 60) {
      return { idleTimeoutSeconds: Number(value.idleTimeoutSeconds) };
    }
  } catch {
    // Browser storage can be unavailable in hardened kiosk profiles.
  }
  return { idleTimeoutSeconds: defaultIdleTimeoutSeconds };
}

export function localSessionIsActive(session: LocalSession | null, now = Date.now()): boolean {
  return session !== null && session.expiresAt > now;
}

export async function passwordDigest(password: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(password));
  return Array.from(new Uint8Array(digest), (value) => value.toString(16).padStart(2, "0")).join("");
}

export function demoAuthPreviewFromSearch(search: string): DemoAuthPreview {
  const query = new URLSearchParams(search);
  if (query.get("demo") !== "1") {
    return null;
  }
  const auth = query.get("auth");
  return auth === "login" || auth === "bootstrap" ? auth : null;
}

export function demoAuthScreenForPreview(preview: DemoAuthPreview, storage: StorageReader): DemoAuthPreview {
  if (preview !== "login") {
    return preview;
  }
  return readLocalAdministrator(storage) === null ? "bootstrap" : "login";
}

function demoAuthPreviewMode(): DemoAuthPreview {
  return demoAuthScreenForPreview(
    demoAuthPreviewFromSearch(window.location.search),
    () => window.localStorage
  );
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

type PendingStartCommand = {
  requestID: string;
  timeout: ReturnType<typeof setTimeout>;
  resolve: () => void;
  reject: (error: HMIAPIError) => void;
};

export const startCommandResultTimeoutMilliseconds = 5000;

// The V2 HMI currently exposes exactly one real operation: the Start pulse.
// This keeps its one request/result pair explicit rather than introducing a command queue.
export class StartCommandReceipt {
  private pending: PendingStartCommand | null = null;

  constructor(private readonly timeoutMilliseconds = startCommandResultTimeoutMilliseconds) {}

  waitFor(requestID: string): Promise<void> {
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
  private session: LocalSession | null = null;
  private configured = false;
  private reconnectDelay = 1000;
  private reconnectTimer: number | null = null;
  private sessionExpiryTimer: number | null = null;
  private lastActivityAt = Number.NEGATIVE_INFINITY;
  private revision = 0;
  private plcState: PLCDevice["state"] = "disconnected";
  private lastPLCSampleAt: string | null = null;
  private lastPLCError = "";
  private readonly values = new Map<string, PointValue>();
  private readonly plcDevices: PLCDevice[] = [];
  private readonly pendingStartCommand = new StartCommandReceipt();
  private demoState = initialDemoState();
  private authKeyboardOriginalMode: SoftKeyboardMode | null = null;

  constructor(
    private readonly config: PageConfiguration,
    private readonly demo: boolean,
    private readonly authPreview: DemoAuthPreview
  ) {}

  start(): void {
    window.HMIFrontendAuth = {
      hasPermission: (permission) => this.hasPermission(permission),
      requirePermission: (permission) => this.requirePermission(permission),
      permissions: () => ({
        operate: this.hasPermission("operate"),
        maintenance: this.hasPermission("maintenance")
      })
    };
    window.addEventListener("hmi-soft-keyboard-ready", () => this.openFocusedAuthenticationKeyboard(), { once: true });
    this.moveLocalAdministrationToMaintenance();
    this.bindAuthForms();
    this.bindPasswordVisibilityToggles();
    this.bindAccountControls();
    this.bindPLCControls();
    this.bindActivityReporting();
    this.prepareGuestHMI();
    this.restoreLocalSession();
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
      this.showAuthentication(this.authPreview);
    }
  }

  backend(): LegacyBackend {
    return {
      APIError: HMIAPIError,
      getState: () => this.getState(),
      sendCommand: (command, payload) => this.sendCommand(command, payload),
      acknowledgeAlarm: (alarmID) => this.acknowledgeAlarm(alarmID),
      getAudit: () => Promise.resolve({ events: cloneState(this.currentState()).history })
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
    this.authNotice().textContent = message;
    const maintenanceNotice = document.querySelector<HTMLElement>("#local-admin-notice");
    if (maintenanceNotice !== null) {
      maintenanceNotice.textContent = message;
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

  private showAuthentication(screen: AuthScreen, message = ""): void {
    this.endAuthenticationKeyboard();
    const panel = this.authPanel();
    panel.hidden = false;
    panel.setAttribute("aria-busy", "false");
    panel.setAttribute("data-auth-mode", screen);
    this.loginSection().hidden = screen !== "login";
    this.bootstrapSection().hidden = screen !== "bootstrap";
    this.setAuthNotice(message);
    this.openAuthenticationKeyboard(screen);
  }

  private showLogin(message = ""): void {
    this.showAuthentication("login", message);
  }

  private showBootstrap(message = ""): void {
    this.showAuthentication("bootstrap", message);
  }

  private openAuthenticationKeyboard(screen: AuthScreen): void {
    const form = document.querySelector<HTMLFormElement>(screen === "bootstrap" ? "#initial-admin-form" : "#login-form")!;
    const input = form.querySelector<HTMLInputElement>("[data-soft-keyboard]")!;
    window.requestAnimationFrame(() => {
      if (this.authPanel().hidden || this.authPanel().getAttribute("data-auth-mode") !== screen) {
        return;
      }
      input.focus();
      this.openAuthenticationKeyboardInput(input);
    });
  }

  private openFocusedAuthenticationKeyboard(): void {
    const panel = this.authPanel();
    const screen = panel.getAttribute("data-auth-mode");
    if (panel.hidden || (screen !== "login" && screen !== "bootstrap")) {
      return;
    }
    const form = document.querySelector<HTMLFormElement>(screen === "bootstrap" ? "#initial-admin-form" : "#login-form")!;
    const input = form.querySelector<HTMLInputElement>("[data-soft-keyboard]")!;
    if (document.activeElement === input) {
      this.openAuthenticationKeyboardInput(input);
    }
  }

  private openAuthenticationKeyboardInput(input: HTMLInputElement): void {
    const keyboard = window.HMISoftKeyboard;
    if (keyboard === undefined) {
      return;
    }
    if (this.authKeyboardOriginalMode === null) {
      this.authKeyboardOriginalMode = keyboard.getMode();
    }
    keyboard.setMode("soft", false);
    keyboard.setPinned(true);
    keyboard.open(input);
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

    const policy = document.querySelector<HTMLFormElement>("#session-policy-form")!;
    policy.addEventListener("submit", (event) => {
      event.preventDefault();
      void this.saveSessionPolicy(Number(formValue(policy, "idleTimeoutSeconds")));
    });
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
      idleTimeout.value = String(readLocalSettings(() => window.localStorage).idleTimeoutSeconds);
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
      const now = performance.now();
      if (now - this.lastActivityAt < 500) {
        return;
      }
      this.lastActivityAt = now;
      this.refreshLocalSession();
    };
    document.addEventListener("pointerdown", report, { passive: true });
    document.addEventListener("touchstart", report, { passive: true });
    document.addEventListener("keydown", report);
  }

  private async login(username: string, password: string): Promise<void> {
    const account = readLocalAdministrator(() => window.localStorage);
    if (account === null || account.username !== username.trim() || account.passwordHash !== await passwordDigest(password)) {
      this.setAuthNotice("用户名或密码不正确");
      return;
    }
    this.beginSession(account);
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
    try {
      const existing = readLocalAdministrator(() => window.localStorage);
      if (existing !== null && this.authPreview !== "bootstrap") {
        this.showLogin("本机管理员已存在，请登录。");
        return;
      }
      const account: LocalAdministrator = {
        username: normalizedUsername,
        passwordHash: await passwordDigest(password),
        permissions: { operate: true, maintenance: true }
      };
      window.localStorage.setItem(localAdminStorageKey, JSON.stringify(account));
      this.beginSession(account);
    } catch {
      this.setAuthNotice("无法保存本机管理员");
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
    const account = readLocalAdministrator(() => window.localStorage);
    if (account === null || !this.signedIn || account.passwordHash !== await passwordDigest(currentPassword)) {
      this.setAuthNotice("当前密码不正确");
      return;
    }
    try {
      account.passwordHash = await passwordDigest(newPassword);
      window.localStorage.setItem(localAdminStorageKey, JSON.stringify(account));
      this.setAuthNotice("密码已修改");
    } catch {
      this.setAuthNotice("无法保存本地密码");
    }
  }

  private async saveSessionPolicy(idleTimeoutSeconds: number): Promise<void> {
    if (!this.requirePermission("maintenance")) {
      return;
    }
    if (!Number.isInteger(idleTimeoutSeconds) || idleTimeoutSeconds < 60) {
      this.setAuthNotice("不活动退出时长至少为 60 秒");
      return;
    }
    try {
      window.localStorage.setItem(localSettingsStorageKey, JSON.stringify({ idleTimeoutSeconds }));
      this.refreshLocalSession();
      this.setAuthNotice("会话时长已保存");
    } catch {
      this.setAuthNotice("无法保存会话时长");
    }
  }

  private beginSession(account: LocalAdministrator): void {
    this.endAuthenticationKeyboard();
    this.signedIn = true;
    const now = Date.now();
    const timeoutMilliseconds = readLocalSettings(() => window.localStorage).idleTimeoutSeconds * 1000;
    this.session = { username: account.username, permissions: { ...account.permissions }, lastActivity: now, expiresAt: now + timeoutMilliseconds };
    this.writeSession();
    this.scheduleSessionExpiry();
    this.authPanel().hidden = true;
    this.setAuthNotice("");
    this.updateAccountControl();
    this.emitPermissionChange();
    this.renderPLCCandidates();
    this.emitState();
  }

  private logout(): void {
    this.endAuthenticationKeyboard();
    this.pendingStartCommand.cancel("已退出登录，启动结果未知", 401, "unauthenticated");
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
    try {
      window.sessionStorage.removeItem(localSessionStorageKey);
    } catch {
      // Session storage is optional for the frontend gate.
    }
    this.authPanel().hidden = true;
    if (document.querySelector<HTMLElement>("[data-page=\"maintenance\"]")?.hidden === false) {
      window.dispatchEvent(new Event("block-hmi-guest"));
    }
    this.updateAccountControl();
    this.emitPermissionChange();
    this.renderPLCCandidates();
    this.emitState();
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
      this.pendingStartCommand.cancel("本机服务连接中断，启动结果未知", 503, "network_error");
      this.socket = null;
      this.configured = false;
      clearTransientRuntime(this.values, this.plcDevices);
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
    this.pendingStartCommand.cancel("本机服务连接已关闭，启动结果未知", 503, "network_error");
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

  private restoreLocalSession(): void {
    let session: LocalSession | null = null;
    try {
      const raw = window.sessionStorage.getItem(localSessionStorageKey);
      session = raw === null ? null : localSessionFrom(JSON.parse(raw));
    } catch {
      session = null;
    }
    const account = readLocalAdministrator(() => window.localStorage);
    if (account === null || session === null || !localSessionIsActive(session) || session.username !== account.username ||
      session.permissions.operate !== account.permissions.operate || session.permissions.maintenance !== account.permissions.maintenance) {
      this.becomeGuest();
      return;
    }
    this.signedIn = true;
    this.session = session;
    this.scheduleSessionExpiry();
    this.updateAccountControl();
    this.emitPermissionChange();
  }

  private writeSession(): void {
    if (this.session === null) {
      return;
    }
    try {
      window.sessionStorage.setItem(localSessionStorageKey, JSON.stringify(this.session));
    } catch {
      // A kiosk without session storage still has the current in-memory session.
    }
  }

  private refreshLocalSession(): void {
    if (this.session === null) {
      return;
    }
    const now = Date.now();
    this.session.lastActivity = now;
    this.session.expiresAt = now + readLocalSettings(() => window.localStorage).idleTimeoutSeconds * 1000;
    this.writeSession();
    this.scheduleSessionExpiry();
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
      if (!localSessionIsActive(this.session)) {
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
    this.showAuthentication(readLocalAdministrator(() => window.localStorage) === null ? "bootstrap" : "login");
  }

  private hasPermission(permission: "operate" | "maintenance"): boolean {
    return this.signedIn && this.session !== null && this.session.permissions[permission];
  }

  private requirePermission(permission: "operate" | "maintenance"): boolean {
    if (this.hasPermission(permission)) {
      return true;
    }
    this.showAuthentication(readLocalAdministrator(() => window.localStorage) === null ? "bootstrap" : "login");
    return false;
  }

  private updateAccountControl(): void {
    const operator = document.querySelector<HTMLElement>("#operatorName")!;
    const label = operator.parentElement?.querySelector<HTMLElement>(".meta-cn") ?? null;
    operator.textContent = this.signedIn && this.session !== null ? this.session.username : "登录";
    operator.setAttribute("aria-label", this.signedIn ? "点击退出本机管理员" : "点击登录本机管理员");
    if (label !== null) {
      label.textContent = this.signedIn ? "管理员" : "登录";
    }
  }

  private emitPermissionChange(): void {
    window.dispatchEvent(new CustomEvent("block-hmi-auth-changed", {
      detail: {
        signedIn: this.signedIn,
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
      this.lastPLCSampleAt = latestPointTime(message.values) ?? new Date().toISOString();
      this.setPLCStatus("已接收 PLC 当前状态");
      this.emitState();
      return;
    }
    if (message.type === "points.changed" && message.values !== undefined) {
      applyAbsoluteValues(this.values, message.values);
      this.revision += 1;
      this.lastPLCSampleAt = latestPointTime(message.values) ?? new Date().toISOString();
      this.renderPLCReadOnly();
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
      if (message.state === "error") {
        this.lastPLCError = plcStateText(message.state, message.deviceId);
      }
      this.setPLCStatus(plcStateText(message.state, message.deviceId));
      this.renderPLCCandidates();
      return;
    }
    if (message.success === false) {
      this.lastPLCError = message.error?.message ?? errorText(message.error?.code);
      this.renderPLCReadOnly();
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

  private valueFor(displayPath: string): Scalar | undefined {
    const binding = this.config.bindings.find((item) => item.displayPath === displayPath);
    if (binding === undefined) {
      return undefined;
    }
    return this.values.get(binding.readPoint)?.value;
  }

  private sendCommand(command: string, payload: Record<string, unknown> = {}): Promise<{ state: LegacyState }> {
    if (!this.requirePermission("operate")) {
      return Promise.reject(new HMIAPIError("请登录管理员后执行现场操作", 403, "permission_denied"));
    }
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
      this.socket!.send(JSON.stringify(buildPointCommand(binding.writePoint, binding.action, requestId)));
    } catch {
      this.pendingStartCommand.cancel("启动命令未发送，结果未知", 503, "network_error");
    }
    return confirmation.then(() => ({ state: cloneState(this.currentState()) }));
  }

  private acknowledgeAlarm(alarmID: number): Promise<{ state: LegacyState }> {
    if (!this.requirePermission("operate")) {
      return Promise.reject(new HMIAPIError("请登录管理员后确认报警", 403, "permission_denied"));
    }
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
      state.inspected += 1;
      state.passed += 1;
    } else if (command === "clear_bins") {
      state.bins = state.bins.map(() => ({ type: "empty", label: "已清空" }));
    } else if (command === "set_single_paused") {
      state.singlePaused = Boolean(payload.paused);
    } else if (command === "set_frame_paused") {
      state.framePaused = Boolean(payload.paused);
    } else if (command === "set_mode") {
      state.mode = payload.mode === "manual" ? "manual" : "auto";
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
    this.sendRuntimeRequest(buildPLCScan());
  }

  private sendPLCConnect(deviceID: string): void {
    if (!this.requirePermission("maintenance")) {
      return;
    }
    if (this.demo) {
      return;
    }
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

  private canSendRuntime(): boolean {
    return this.configured && (this.demo || this.socket?.readyState === WebSocket.OPEN);
  }

  private setPLCStatus(message: string): void {
    document.querySelector<HTMLElement>("#plc-status")!.textContent = message;
    this.renderPLCReadOnly();
  }

  private renderPLCReadOnly(): void {
    const connection = document.querySelector<HTMLElement>("#plc-connection-value");
    const sample = document.querySelector<HTMLElement>("#plc-last-sample");
    const error = document.querySelector<HTMLElement>("#plc-last-error");
    const pointCount = document.querySelector<HTMLElement>("#plc-point-count");
    const livePoints = document.querySelector<HTMLElement>("#plc-live-points");
    if (connection === null || sample === null || error === null || pointCount === null || livePoints === null) {
      return;
    }
    connection.textContent = plcStateText(this.plcState);
    sample.textContent = this.lastPLCSampleAt ?? "—";
    error.textContent = this.lastPLCError || "—";
    pointCount.textContent = String(this.config.points.length || this.values.size);
    const entries = [...this.values.entries()].slice(0, 50);
    livePoints.textContent = entries.length === 0
      ? "等待 PLC 实时点值"
      : entries.map(([pointID, point]) => pointID + ": " + String(point.value) + " · " + point.quality + " · " + point.updatedAt).join("\n");
  }

  private renderPLCCandidates(): void {
    const list = document.querySelector<HTMLElement>("#plc-candidates")!;
    const scan = document.querySelector<HTMLButtonElement>("#plc-scan-button")!;
    const disconnect = document.querySelector<HTMLButtonElement>("#plc-disconnect-button")!;
    const snapshot = document.querySelector<HTMLButtonElement>("#snapshot-button")!;
    const active = this.canSendRuntime();
    scan.disabled = this.signedIn && !active;
    snapshot.disabled = this.signedIn && !active;
    disconnect.disabled = this.signedIn && (!active || this.plcState === "disconnected" || this.plcState === "unconfigured");
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
      connect.disabled = this.signedIn && (!active || device.state === "connected" || device.state === "connecting");
      connect.addEventListener("click", () => this.sendPLCConnect(device.deviceId));
      item.append(detail, document.createTextNode(" "), connect);
      list.append(item);
    }
  }

  private emitState(): void {
    window.dispatchEvent(new Event("block-hmi-state"));
  }

  private deferProductionPolicy(): void {
    if (this.demo) {
      return;
    }
    window.setTimeout(() => {
      const start = document.querySelector<HTMLButtonElement>('[data-action="start"]');
      const startEnabled = this.canSendRuntime();
      document.querySelectorAll<HTMLButtonElement>(".control-button").forEach((button) => {
        button.dataset.backendUnavailable = button !== start || !startEnabled ? "true" : "false";
      });
      const mode = document.querySelector<HTMLButtonElement>("#modeToggle");
      if (mode !== null) {
        mode.dataset.backendUnavailable = "true";
      }
      document.querySelectorAll<HTMLButtonElement>(".ack-button").forEach((button) => {
        button.dataset.backendUnavailable = "true";
      });
      const gap = document.querySelector<HTMLElement>("#v2-data-gap");
      if (gap !== null) {
        gap.hidden = false;
      }
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
  const bridge = new AppleBridge(await loadConfiguration(demo), demo, demoAuthPreviewMode());
  bridge.start();
  const backend = bridge.backend();
  window.HMIBackend = backend;
  return backend;
}
