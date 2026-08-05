type PointType = "bool" | "int16" | "uint16";

interface Point {
  id: string;
  name: string;
  type: PointType;
  description: string;
  address: string;
  writable: boolean;
  value: boolean | number;
  updatedAt: string;
  source: string;
}

interface Status {
  modbusAddress: string;
  unitId: number;
  activeConnections: number;
}

const pointsBody = document.querySelector<HTMLTableSectionElement>("#points-body")!;
const emptyPoints = document.querySelector<HTMLParagraphElement>("#empty-points")!;
const message = document.querySelector<HTMLParagraphElement>("#message")!;
const editor = document.querySelector<HTMLElement>("#editor")!;
const form = document.querySelector<HTMLFormElement>("#point-form")!;
const editorTitle = document.querySelector<HTMLElement>("#editor-title")!;
const nameInput = document.querySelector<HTMLInputElement>("#point-name")!;
const typeInput = document.querySelector<HTMLSelectElement>("#point-type")!;
const addressInput = document.querySelector<HTMLInputElement>("#point-address")!;
const descriptionInput = document.querySelector<HTMLInputElement>("#point-description")!;
const writableInput = document.querySelector<HTMLInputElement>("#point-writable")!;

let points: Point[] = [];
let editingID: string | null = null;

async function request<T>(path: string, method = "GET", body?: unknown): Promise<T> {
  const response = await fetch(path, {
    method,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
    cache: "no-store",
  });
  if (!response.ok) {
    throw new Error((await response.text()).trim() || `请求失败 (${response.status})`);
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return response.json() as Promise<T>;
}

function setMessage(text = "", success = false): void {
  message.textContent = text;
  message.classList.toggle("success", success);
}

function sourceText(source: string): string {
  return ({ modbus: "机器 Modbus", ui: "页面写入", definition: "点位定义", initial: "初始" } as Record<string, string>)[source] ?? source;
}

function formatTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString("zh-CN", { hour12: false });
}

function element<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (text !== undefined) {
    node.textContent = text;
  }
  return node;
}

function appendCell(row: HTMLTableRowElement, content: string): HTMLTableCellElement {
  const cell = element("td", content);
  row.append(cell);
  return cell;
}

function renderPoints(): void {
  pointsBody.replaceChildren();
  emptyPoints.hidden = points.length !== 0;
  for (const point of points) {
    const row = element("tr");
    appendCell(row, point.name);
    appendCell(row, point.address);
    appendCell(row, point.type);

    const valueCell = element("td");
    if (!point.writable) {
      valueCell.textContent = String(point.value);
    } else if (point.type === "bool") {
      const toggle = element("input") as HTMLInputElement;
      toggle.type = "checkbox";
      toggle.checked = point.value === true;
      toggle.setAttribute("aria-label", `${point.name} 当前值`);
      toggle.addEventListener("change", () => writeValue(point, toggle.checked));
      valueCell.append(toggle);
    } else {
      const control = element("div");
      control.className = "value-editor";
      const input = element("input") as HTMLInputElement;
      input.type = "number";
      input.step = "1";
      input.value = String(point.value);
      input.min = point.type === "int16" ? "-32768" : "0";
      input.max = point.type === "int16" ? "32767" : "65535";
      const write = element("button", "写入");
      write.type = "button";
      write.addEventListener("click", () => writeValue(point, Number(input.value)));
      control.append(input, write);
      valueCell.append(control);
    }
    row.append(valueCell);
    appendCell(row, point.writable ? "是" : "否");
    appendCell(row, point.description || "—");
    const timeCell = appendCell(row, "");
    timeCell.className = "timestamp";
    timeCell.textContent = `${formatTime(point.updatedAt)}\n${sourceText(point.source)}`;

    const actions = element("td");
    actions.className = "point-actions";
    const edit = element("button", "编辑");
    edit.type = "button";
    edit.addEventListener("click", () => showEditor(point));
    const remove = element("button", "删除");
    remove.type = "button";
    remove.className = "danger";
    remove.addEventListener("click", () => deletePoint(point));
    actions.append(edit, remove);
    row.append(actions);
    pointsBody.append(row);
  }
}

async function loadPoints(): Promise<void> {
  points = await request<Point[]>("/api/points");
  renderPoints();
}

async function loadStatus(): Promise<void> {
  const status = await request<Status>("/api/status");
  document.querySelector<HTMLElement>("#modbus-address")!.textContent = `${status.modbusAddress}（Unit ${status.unitId}）`;
  document.querySelector<HTMLElement>("#active-connections")!.textContent = String(status.activeConnections);
}

async function refresh(showErrors = true): Promise<void> {
  try {
    await Promise.all([loadPoints(), loadStatus()]);
  } catch (error) {
    if (showErrors) {
      setMessage(error instanceof Error ? error.message : "无法连接本机模拟器");
    }
  }
}

async function writeValue(point: Point, value: boolean | number): Promise<void> {
  try {
    await request<Point>(`/api/points/${encodeURIComponent(point.id)}/value`, "PUT", { value });
    setMessage(`${point.name} 已写入`, true);
    await refresh(false);
  } catch (error) {
    setMessage(error instanceof Error ? error.message : "写入失败");
    await refresh(false);
  }
}

function showEditor(point?: Point): void {
  editingID = point?.id ?? null;
  editor.hidden = false;
  editorTitle.textContent = point ? `编辑点位：${point.name}` : "添加点位";
  nameInput.value = point?.name ?? "";
  typeInput.value = point?.type ?? "bool";
  addressInput.value = point?.address ?? "";
  descriptionInput.value = point?.description ?? "";
  writableInput.checked = point?.writable ?? false;
  nameInput.focus();
}

function hideEditor(): void {
  editor.hidden = true;
  editingID = null;
  form.reset();
}

async function deletePoint(point: Point): Promise<void> {
  if (!window.confirm(`确定删除点位“${point.name}”吗？寄存器当前值不会被清空。`)) {
    return;
  }
  try {
    await request<void>(`/api/points/${encodeURIComponent(point.id)}`, "DELETE");
    setMessage(`${point.name} 已删除`, true);
    await refresh(false);
  } catch (error) {
    setMessage(error instanceof Error ? error.message : "删除失败");
  }
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const input = {
    name: nameInput.value,
    type: typeInput.value as PointType,
    address: addressInput.value,
    description: descriptionInput.value,
    writable: writableInput.checked,
  };
  try {
    if (editingID === null) {
      await request<Point>("/api/points", "POST", input);
      setMessage("点位已添加", true);
    } else {
      await request<Point>(`/api/points/${encodeURIComponent(editingID)}`, "PUT", input);
      setMessage("点位已更新", true);
    }
    hideEditor();
    await refresh(false);
  } catch (error) {
    setMessage(error instanceof Error ? error.message : "保存失败");
  }
});

document.querySelector<HTMLButtonElement>("#add-point")!.addEventListener("click", () => showEditor());
document.querySelector<HTMLButtonElement>("#cancel-edit")!.addEventListener("click", hideEditor);

const events = new EventSource("/api/events");
events.addEventListener("ready", () => { void refresh(false); });
events.onmessage = () => { void refresh(false); };

void refresh();
window.setInterval(() => { void loadStatus().catch(() => undefined); }, 2000);
