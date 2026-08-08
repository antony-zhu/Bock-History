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

const authSubmitSelectors = [
  { form: "#login-form", actions: "#login-form .auth-actions", submit: "#login-form button[type=\"submit\"]" },
  { form: "#initial-admin-form", actions: "#initial-admin-form .auth-actions", submit: "#initial-admin-form button[type=\"submit\"]" }
];

export function inspectAuthSubmitLayout(documentRef = document, windowRef = window) {
  const sheetRect = documentRef.querySelector("#auth-panel .auth-sheet").getBoundingClientRect();
  return {
    viewportWidth: windowRef.innerWidth,
    viewportHeight: windowRef.innerHeight,
    forms: authSubmitSelectors.map(({ form, actions, submit }) => {
      const formRect = documentRef.querySelector(form).getBoundingClientRect();
      const actionsRect = documentRef.querySelector(actions).getBoundingClientRect();
      const submitRect = documentRef.querySelector(submit).getBoundingClientRect();
      return { form, formRect, actionsRect, submitRect, leftInset: formRect.left - sheetRect.left, rightInset: sheetRect.right - formRect.right };
    })
  };
}

export function assertAuthSubmitLayout(documentRef = document, windowRef = window) {
  const result = inspectAuthSubmitLayout(documentRef, windowRef);
  for (const layout of result.forms) {
    const { form, formRect, actionsRect, submitRect, leftInset, rightInset } = layout;
    if (Math.abs(leftInset - rightInset) > 0.5) throw new Error(form + " is not centered in the auth sheet");
    if (Math.abs(actionsRect.left - formRect.left) > 0.5 || Math.abs(actionsRect.right - formRect.right) > 0.5) {
      throw new Error(form + " action row does not span the form");
    }
    if (Math.abs(submitRect.left - actionsRect.left) > 0.5 || Math.abs(submitRect.right - actionsRect.right) > 0.5) {
      throw new Error(form + " submit does not span the label and input columns");
    }
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
