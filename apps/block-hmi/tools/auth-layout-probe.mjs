const visualSelectors = ["#hmi", "#hmi-topbar", "#hmi-pages", "#hmi-footer"];
export const authKeyboardSafeGap = 16;

export function rectangleIntersectionArea(first, second) {
  const width = Math.max(0, Math.min(first.right, second.right) - Math.max(first.left, second.left));
  const height = Math.max(0, Math.min(first.bottom, second.bottom) - Math.max(first.top, second.top));
  return width * height;
}

export function captureHMIVisualState(documentRef = document, windowRef = window) {
  return Object.fromEntries(visualSelectors.map((selector) => {
    const element = documentRef.querySelector(selector);
    const style = windowRef.getComputedStyle(element);
    return [selector, {
      backgroundColor: style.backgroundColor,
      filter: style.filter,
      opacity: style.opacity,
      backdropFilter: style.backdropFilter
    }];
  }));
}

export function inspectAuthKeyboardLayout(documentRef = document, windowRef = window) {
  const authPanel = documentRef.querySelector("#auth-panel");
  const sheet = documentRef.querySelector("#auth-panel .auth-sheet");
  const dock = documentRef.querySelector("#softKeyboardDock");
  const authStyle = windowRef.getComputedStyle(authPanel);
  const sheetStyle = windowRef.getComputedStyle(sheet);
  const keyboardOpen = authPanel.getAttribute("data-keyboard-open") === "true";
  const dockVisible = !dock.hidden && dock.getClientRects().length > 0;
  const sheetRect = sheet.getBoundingClientRect();
  const dockRect = dock.getBoundingClientRect();
  return {
    keyboardOpen,
    dockVisible,
    intersectionArea: dockVisible ? rectangleIntersectionArea(sheetRect, dockRect) : 0,
    sheetTopGap: sheetRect.top,
    sheetKeyboardGap: dockVisible ? dockRect.top - sheetRect.bottom : null,
    sheetBottom: sheetRect.bottom,
    viewportHeight: windowRef.innerHeight,
    authPointerEvents: authStyle.pointerEvents,
    authBackgroundColor: authStyle.backgroundColor,
    authFilter: authStyle.filter,
    authOpacity: authStyle.opacity,
    authOverflow: authStyle.overflow,
    sheetPointerEvents: sheetStyle.pointerEvents,
    sheetOverflowY: sheetStyle.overflowY,
    hmi: captureHMIVisualState(documentRef, windowRef)
  };
}

export function assertAuthKeyboardLayout(documentRef = document, windowRef = window) {
  const result = inspectAuthKeyboardLayout(documentRef, windowRef);
  if (result.authPointerEvents !== "none") throw new Error("auth host captures pointer events");
  if (result.sheetPointerEvents !== "auto") throw new Error("auth sheet is not interactive");
  if (result.authBackgroundColor !== "rgba(0, 0, 0, 0)") throw new Error("auth host has a visible background");
  if (result.authFilter !== "none" || result.authOpacity !== "1") throw new Error("auth host visually alters the HMI");
  if (result.keyboardOpen && result.dockVisible && result.intersectionArea !== 0) {
    throw new Error("auth sheet overlaps the soft keyboard");
  }
  if (result.keyboardOpen && result.dockVisible) {
    if (result.sheetTopGap < authKeyboardSafeGap) throw new Error("auth sheet is above the safe top margin");
    if (result.sheetKeyboardGap < authKeyboardSafeGap) throw new Error("auth sheet is too close to the soft keyboard");
    if (result.sheetBottom > result.viewportHeight) throw new Error("auth sheet extends below the viewport");
    if (result.authOverflow !== "hidden") throw new Error("auth host may scroll outside the sheet");
    if (result.sheetOverflowY !== "auto") throw new Error("auth sheet does not own its vertical scroll");
  }
  return result;
}

export function assertHMIVisualStateMatches(expected, actual) {
  for (const selector of visualSelectors) {
    const baseline = expected[selector];
    const current = actual[selector];
    if (!baseline || !current) throw new Error("missing visual state for " + selector);
    for (const property of ["backgroundColor", "filter", "opacity", "backdropFilter"]) {
      if (baseline[property] !== current[property]) {
        throw new Error(selector + " changed " + property + " from " + baseline[property] + " to " + current[property]);
      }
    }
  }
}
