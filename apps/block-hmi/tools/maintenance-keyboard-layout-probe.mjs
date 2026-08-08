function rectangleIntersectionArea(first, second) {
  const width = Math.max(0, Math.min(first.right, second.right) - Math.max(first.left, second.left));
  const height = Math.max(0, Math.min(first.bottom, second.bottom) - Math.max(first.top, second.top));
  return width * height;
}

function isVisible(element) {
  return Boolean(element && !element.hidden && element.getClientRects().length > 0);
}

export function inspectMaintenanceFullKeyboardLayout(documentRef = document, windowRef = window) {
  const panel = documentRef.querySelector("#wifiSettingsPanel");
  const dock = documentRef.querySelector("#softKeyboardDock");
  const actions = documentRef.querySelector(".wifi-settings-actions");
  const controls = [
    { name: "Wi-Fi 名称", element: documentRef.querySelector("#wifiSsidInput") },
    { name: "Wi-Fi 密码", element: documentRef.querySelector("#wifiPasswordInput") },
    { name: "刷新状态", element: documentRef.querySelector("#wifiRefreshButton") },
    { name: "连接 Wi-Fi", element: documentRef.querySelector("#saveWifiButton") }
  ];
  const panelStyle = windowRef.getComputedStyle(panel);
  const dockStyle = windowRef.getComputedStyle(dock);
  const actionStyle = windowRef.getComputedStyle(actions);
  const dockRect = dock.getBoundingClientRect();
  const actionRect = actions.getBoundingClientRect();

  return {
    keyboardOpen: documentRef.documentElement.getAttribute("data-soft-keyboard-open") === "true",
    keyboardLayout: documentRef.documentElement.getAttribute("data-soft-keyboard-layout"),
    viewportWidth: windowRef.innerWidth,
    viewportHeight: windowRef.innerHeight,
    panelRect: panel.getBoundingClientRect(),
    panelOverflowY: panelStyle.overflowY,
    panelScrollable: panel.scrollHeight > panel.clientHeight,
    dockRect,
    dockVisible: isVisible(dock),
    dockPointerEvents: dockStyle.pointerEvents,
    actionRect,
    actionPointerEvents: actionStyle.pointerEvents,
    actionDockIntersection: rectangleIntersectionArea(actionRect, dockRect),
    controls: controls.map(({ name, element }) => ({
      name,
      visible: isVisible(element),
      pointerEvents: windowRef.getComputedStyle(element).pointerEvents,
      rect: element.getBoundingClientRect(),
      dockIntersection: rectangleIntersectionArea(element.getBoundingClientRect(), dockRect)
    }))
  };
}

export function assertMaintenanceFullKeyboardLayout(documentRef = document, windowRef = window) {
  const result = inspectMaintenanceFullKeyboardLayout(documentRef, windowRef);
  if (!result.keyboardOpen || result.keyboardLayout !== "full") {
    throw new Error("maintenance keyboard layout probe requires an open full keyboard");
  }
  if (!result.dockVisible || result.dockPointerEvents !== "auto") {
    throw new Error("full keyboard dock is not an interactive visible overlay");
  }
  if (result.panelOverflowY !== "auto" || !result.panelScrollable) {
    throw new Error("Wi-Fi panel does not keep its own scrollable region");
  }
  if (result.panelRect.bottom > result.dockRect.top - 16) {
    throw new Error("Wi-Fi panel reaches the full keyboard overlay");
  }
  if (result.actionPointerEvents !== "auto" || result.actionRect.bottom > result.dockRect.top || result.actionDockIntersection !== 0) {
    throw new Error("Wi-Fi actions overlap the full keyboard overlay");
  }
  for (const control of result.controls) {
    if (!control.visible || control.pointerEvents !== "auto") {
      throw new Error(control.name + " is not visible and clickable above the full keyboard");
    }
    if (control.rect.bottom > result.dockRect.top || control.dockIntersection !== 0) {
      throw new Error(control.name + " overlaps the full keyboard overlay");
    }
  }
  return result;
}
