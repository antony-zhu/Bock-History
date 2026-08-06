(function (window, document) {
  "use strict";

  var root = document.documentElement;
  var storageKey = "hmi-soft-keyboard-mode";
  var defaultFootText = "取消会恢复原值，完成后保留输入";
  var config = window.HMISoftKeyboardConfig || {};
  var enabled = config.enabled !== false && root.getAttribute("data-soft-keyboard-enabled") !== "false";
  var dock = null;
  var host = null;
  var fieldName = null;
  var preview = null;
  var closeButton = null;
  var foot = null;
  var toggle = null;
  var toggleState = null;
  var validationLine = null;
  var keyboard = null;
  var inputs = [];
  var inputObserver = null;
  var mode = "native";
  var available = false;
  var isOpen = false;
  var activeInput = null;
  var activeInputName = "";
  var activeLayout = "numeric";
  var originalValue = "";
  var committedValue = "";
  var activeFootText = defaultFootText;
  var hideTimer = 0;
  var inputSequence = 0;
  var initializing = true;
  var initialized = false;
  var handlingSoftKey = false;

  var layouts = {
    numeric: [
      "7 8 9 {bksp}",
      "4 5 6 {clear}",
      "1 2 3 {next}",
      "{cancel} 0 00 {done}"
    ],
    "default": [
      "q w e r t y u i o p {bksp}",
      "a s d f g h j k l @",
      "{shift} z x c v b n m . - {shift}",
      "{symbols} {cancel} _ {space} {next} {done}"
    ],
    shift: [
      "Q W E R T Y U I O P {bksp}",
      "A S D F G H J K L @",
      "{shift} Z X C V B N M . - {shift}",
      "{symbols} {cancel} _ {space} {next} {done}"
    ],
    symbols: [
      "1 2 3 4 5 6 7 8 9 0 {bksp}",
      "! @ # $ % ^ & * ( )",
      ". , : ; / ? + = _ -",
      "{abc} {cancel} {space} {next} {done}"
    ]
  };

  var baseDisplay = {
    "{bksp}": "退格",
    "{clear}": "清空",
    "{next}": "下一项",
    "{cancel}": "取消",
    "{done}": "完成",
    "{shift}": "大写",
    "{symbols}": "123#+=",
    "{abc}": "ABC",
    "{space}": "空格"
  };

  function toArray(list) {
    return Array.prototype.slice.call(list || []);
  }

  function createEvent(name, detail) {
    var event;
    if (typeof window.CustomEvent === "function") {
      event = new window.CustomEvent(name, { bubbles: true, detail: detail || {} });
    } else {
      event = document.createEvent("CustomEvent");
      event.initCustomEvent(name, true, false, detail || {});
    }
    return event;
  }

  function dispatchFieldEvent(input, name) {
    var event;
    if (typeof window.Event === "function") {
      event = new window.Event(name, { bubbles: true });
    } else {
      event = document.createEvent("Event");
      event.initEvent(name, true, false);
    }
    input.dispatchEvent(event);
  }

  function safeReadMode() {
    var match = window.location.search.match(/(?:^|[?&])keyboard=(soft|native)(?:&|$)/i);
    if (match) return match[1].toLowerCase();
    try {
      var saved = window.localStorage.getItem(storageKey);
      if (saved === "soft" || saved === "native") return saved;
    } catch (error) {
      return config.defaultMode === "native" ? "native" : "soft";
    }
    return config.defaultMode === "native" ? "native" : "soft";
  }

  function safeStoreMode(nextMode) {
    try {
      window.localStorage.setItem(storageKey, nextMode);
    } catch (error) {
      return;
    }
  }

  function keyboardConstructor() {
    if (!window.SimpleKeyboard) return null;
    return window.SimpleKeyboard.default || window.SimpleKeyboard.SimpleKeyboard || null;
  }

  function copyDisplay(doneLabel) {
    var display = {};
    Object.keys(baseDisplay).forEach(function (key) {
      display[key] = baseDisplay[key];
    });
    display["{done}"] = doneLabel || "完成";
    return display;
  }

  function prepareRenderedButtons() {
    toArray(host.querySelectorAll("button")).forEach(function (button) {
      button.setAttribute("type", "button");
    });
  }

  function ensureKeyboard() {
    if (keyboard) return true;
    var Keyboard = keyboardConstructor();
    if (!Keyboard || !host) return false;

    try {
      keyboard = new Keyboard(".hmi-simple-keyboard", {
        theme: "hg-theme-default hmi-keyboard hmi-simple-keyboard",
        layout: layouts,
        layoutName: "numeric",
        display: copyDisplay("完成"),
        mergeDisplay: true,
        useButtonTag: true,
        preventMouseDownDefault: true,
        preventMouseUpDefault: true,
        onChange: handleKeyboardChange,
        onKeyPress: handleKeyPress,
        onRender: prepareRenderedButtons,
        buttonTheme: [
          {
            class: "hg-function-key",
            buttons: "{bksp} {clear} {next} {cancel} {done} {shift} {symbols} {abc} {space}"
          },
          { class: "hg-button-done", buttons: "{done}" },
          { class: "hg-button-cancel", buttons: "{cancel}" }
        ]
      });
      return true;
    } catch (error) {
      keyboard = null;
      return false;
    }
  }

  function getInputName(input) {
    var name = input.getAttribute("data-soft-input-name") || input.id || input.name;
    if (!name) {
      inputSequence += 1;
      name = "softInput" + inputSequence;
    }
    input.setAttribute("data-soft-input-name", name);
    return name;
  }

  function getFieldLabel(input) {
    var explicit = input.getAttribute("data-soft-label");
    if (explicit) return explicit;
    if (input.id) {
      var label = document.querySelector('label[for="' + input.id.replace(/"/g, "\\\"") + '"]');
      if (label) return label.textContent.replace(/[：:]\s*$/, "").trim();
    }
    return input.name || "当前字段";
  }

  function getLayout(input) {
    return input.getAttribute("data-soft-keyboard") === "full" ? "full" : "numeric";
  }

  function getMaxLength(input) {
    var explicit = parseInt(input.getAttribute("maxlength"), 10);
    if (Number.isFinite(explicit) && explicit > 0) return explicit;
    if (getLayout(input) === "numeric") return 15;
    return 200;
  }

  function updateFoot(message, isError) {
    if (!foot) return;
    foot.textContent = message || defaultFootText;
    foot.classList.toggle("is-error", Boolean(isError));
  }

  function updatePreview(value) {
    if (!preview || !activeInput) return;
    if (activeInput.type === "password") {
      preview.textContent = "内容已隐藏";
      return;
    }
    preview.textContent = String(value || "") || "等待输入";
  }

  function clearError(input) {
    if (input) input.removeAttribute("aria-invalid");
    if (validationLine) validationLine.textContent = "";
    updateFoot(activeFootText, false);
  }

  function setError(input, message) {
    if (input) input.setAttribute("aria-invalid", "true");
    if (validationLine) validationLine.textContent = message;
    updateFoot(message, true);
  }

  function validationMessage(input) {
    var label = getFieldLabel(input);
    var value = String(input.value || "").trim();
    var layout = getLayout(input);

    if (layout === "numeric") {
      if (!value && !input.required) return "";
      var hasMin = input.hasAttribute("min");
      var hasMax = input.hasAttribute("max");
      var min = hasMin ? Number(input.getAttribute("min")) : NaN;
      var max = hasMax ? Number(input.getAttribute("max")) : NaN;
      if (!/^\d+$/.test(value)) return label + "请输入整数";
      if (value.length > 1 && value.charAt(0) === "0") return label + "不能以 0 开头";
      var numericValue = Number(value);
      if (hasMin && Number.isFinite(min) && numericValue < min) return label + "不能小于 " + min;
      if (hasMax && Number.isFinite(max) && numericValue > max) return label + "不能大于 " + max;
      return "";
    }

    if (input.required && !value) return "请输入" + label;
    if (value.length > getMaxLength(input)) return label + "不能超过 " + getMaxLength(input) + " 个字符";
    return "";
  }

  function validateInput(input, focusOnError) {
    var message = validationMessage(input);
    if (!message) {
      clearError(input);
      return true;
    }
    setError(input, message);
    if (focusOnError) {
      input.focus();
      if (mode === "soft") openForInput(input);
    }
    return false;
  }

  function syncActiveValue(value, emitInput) {
    if (!activeInput) return;
    var nextValue = String(value == null ? "" : value);
    var maxLength = getMaxLength(activeInput);
    if (activeLayout === "numeric") nextValue = nextValue.replace(/\D/g, "");
    if (nextValue.length > maxLength) nextValue = nextValue.slice(0, maxLength);
    activeInput.value = nextValue;
    updatePreview(nextValue);
    activeInput.removeAttribute("aria-invalid");
    if (validationLine) validationLine.textContent = "";
    updateFoot(activeFootText, false);
    if (emitInput) dispatchFieldEvent(activeInput, "input");
    if (keyboard && keyboard.getInput(activeInputName) !== nextValue) {
      keyboard.setInput(nextValue, activeInputName);
    }
  }

  function handleKeyboardChange(value) {
    syncActiveValue(value, true);
  }

  function setKeyboardValue(value, emitInput) {
    if (!activeInput || !keyboard) return;
    keyboard.setInput(String(value), activeInputName);
    if (typeof keyboard.setCaretPosition === "function") {
      keyboard.setCaretPosition(String(value).length);
    }
    syncActiveValue(value, emitInput);
  }

  function switchFullLayout(layoutName) {
    if (!keyboard || activeLayout !== "full") return;
    keyboard.setOptions({ layoutName: layoutName });
  }

  function submitOwningForm(input) {
    var form = input && input.form;
    if (!form || input.getAttribute("data-soft-submit") !== "true") return;
    var submit = form.querySelector('[type="submit"]');
    if (submit && typeof submit.click === "function") {
      submit.click();
      return;
    }
    var event = document.createEvent("Event");
    event.initEvent("submit", true, true);
    form.dispatchEvent(event);
  }

  function handleKeyPressAction(button) {
    if (!activeInput) return;
    if (button === "{shift}") {
      var currentLayout = keyboard.options.layoutName;
      switchFullLayout(currentLayout === "shift" ? "default" : "shift");
      return;
    }
    if (button === "{symbols}") {
      switchFullLayout("symbols");
      return;
    }
    if (button === "{abc}") {
      switchFullLayout("default");
      return;
    }
    if (button === "{clear}") {
      setKeyboardValue("", true);
      return;
    }
    if (button === "{cancel}") {
      window.setTimeout(function () {
        closeKeyboard("cancel");
      }, 0);
      return;
    }
    if (button === "{next}") {
      window.setTimeout(function () {
        moveToNextInput();
      }, 0);
      return;
    }
    if (button === "{done}") {
      window.setTimeout(function () {
        var completedInput = activeInput;
        if (closeKeyboard("commit")) submitOwningForm(completedInput);
      }, 0);
    }
  }

  function handleKeyPress(button) {
    handlingSoftKey = true;
    try {
      handleKeyPressAction(button);
    } finally {
      handlingSoftKey = false;
    }
  }

  function configureKeyboard(input) {
    var layout = getLayout(input);
    var doneLabel = input.getAttribute("data-soft-done-label") || "完成";
    activeLayout = layout;
    dock.setAttribute("data-layout", layout);
    keyboard.setOptions({
      inputName: activeInputName,
      layoutName: layout === "numeric" ? "numeric" : "default",
      maxLength: getMaxLength(input),
      display: copyDisplay(doneLabel)
    });
    keyboard.setInput(input.value || "", activeInputName);
    if (typeof keyboard.setCaretPosition === "function") {
      keyboard.setCaretPosition(String(input.value || "").length);
    }
  }

  function getFootText(input) {
    var explicit = input.getAttribute("data-soft-foot");
    if (explicit) return explicit;
    if (input.type === "password") return "密码内容不会在键盘面板中显示";
    if (input.form && input.form.id === "settingsForm") {
      return "取消会恢复原值，完成后仍需保存参数";
    }
    return defaultFootText;
  }

  function showDock() {
    window.clearTimeout(hideTimer);
    hideTimer = 0;
    dock.hidden = false;
    window.requestAnimationFrame(function () {
      dock.classList.add("is-open");
    });
  }

  function hideDock() {
    dock.classList.remove("is-open");
    window.clearTimeout(hideTimer);
    hideTimer = window.setTimeout(function () {
      if (!isOpen) dock.hidden = true;
      hideTimer = 0;
    }, 190);
  }

  function openForInput(input) {
    if (!enabled || mode !== "soft" || !available || !input) return false;
    if (isOpen && activeInput === input) return true;
    var previousInput = activeInput;
    var previousInputName = activeInputName;
    if (isOpen && previousInput && previousInput !== input && !commitActive()) {
      previousInput.focus();
      return false;
    }
    if (isOpen && previousInput && previousInput !== input) {
      previousInput.setAttribute("aria-expanded", "false");
      clearKeyboardCache(previousInputName);
    }

    activeInput = input;
    activeInputName = getInputName(input);
    originalValue = input.value || "";
    committedValue = originalValue;
    activeFootText = getFootText(input);
    clearError(input);
    configureKeyboard(input);
    fieldName.textContent = getFieldLabel(input);
    updatePreview(input.value || "");
    input.setAttribute("aria-expanded", "true");
    input.setAttribute("aria-controls", dock.id);
    isOpen = true;
    root.setAttribute("data-soft-keyboard-open", "true");
    root.setAttribute("data-soft-keyboard-layout", activeLayout);
    showDock();
    return true;
  }

  function commitActive() {
    if (!activeInput) return true;
    if (!validateInput(activeInput, false)) return false;
    if (activeInput.value !== committedValue) {
      dispatchFieldEvent(activeInput, "change");
      committedValue = activeInput.value;
    }
    return true;
  }

  function clearKeyboardCache(inputName) {
    if (!keyboard || !inputName) return;
    if (handlingSoftKey) {
      window.setTimeout(function () {
        clearKeyboardCache(inputName);
      }, 0);
      return;
    }
    keyboard.setInput("", inputName);
  }

  function closeKeyboard(action) {
    if (!isOpen) return true;
    var input = activeInput;
    if (action === "cancel" && input) {
      input.value = originalValue;
      if (keyboard) keyboard.setInput(originalValue, activeInputName);
      dispatchFieldEvent(input, "input");
      clearError(input);
    } else if (action === "commit" && !commitActive()) {
      input.focus();
      return false;
    }

    if (input) input.setAttribute("aria-expanded", "false");
    clearKeyboardCache(activeInputName);
    isOpen = false;
    activeInput = null;
    activeInputName = "";
    originalValue = "";
    committedValue = "";
    activeFootText = defaultFootText;
    root.removeAttribute("data-soft-keyboard-open");
    root.removeAttribute("data-soft-keyboard-layout");
    hideDock();
    return true;
  }

  function isUsableInput(input) {
    if (!input || input.disabled || input.type === "hidden") return false;
    if (input.hidden || input.getAttribute("aria-hidden") === "true") return false;
    var style = window.getComputedStyle(input);
    if (style.display === "none" || style.visibility === "hidden") return false;
    return input.getClientRects().length > 0;
  }

  function isKeyboardCandidate(input) {
    if (!input || input.nodeType !== 1 || input.disabled || input.readOnly || input.hidden) return false;
    var tag = String(input.tagName || "").toLowerCase();
    if (tag === "textarea") return true;
    if (tag !== "input") return false;
    var type = String(input.getAttribute("type") || "text").toLowerCase();
    return ["hidden", "button", "submit", "reset", "checkbox", "radio", "file", "image"].indexOf(type) === -1;
  }

  function ensureKeyboardAttribute(input) {
    if (input.hasAttribute("data-soft-keyboard")) return;
    var type = String(input.getAttribute("type") || "text").toLowerCase();
    var inputMode = String(input.getAttribute("inputmode") || "").toLowerCase();
    var numeric = type === "number" || inputMode === "numeric" || inputMode === "decimal" || inputMode === "tel";
    input.setAttribute("data-soft-keyboard", numeric ? "numeric" : "full");
  }

  function findNextInput(input) {
    if (!input) return null;
    var scope = input.form || document;
    var candidates = toArray(scope.querySelectorAll("[data-soft-keyboard]"));
    var index = candidates.indexOf(input);
    for (var nextIndex = index + 1; nextIndex < candidates.length; nextIndex += 1) {
      if (isUsableInput(candidates[nextIndex])) return candidates[nextIndex];
    }
    return null;
  }

  function findPreviousInput(input) {
    if (!input) return null;
    var scope = input.form || document;
    var candidates = toArray(scope.querySelectorAll("[data-soft-keyboard]"));
    var index = candidates.indexOf(input);
    for (var previousIndex = index - 1; previousIndex >= 0; previousIndex -= 1) {
      if (isUsableInput(candidates[previousIndex])) return candidates[previousIndex];
    }
    return null;
  }

  function findPreviousFocusable(input) {
    if (!input) return null;
    var selector = 'a[href], button, input, select, textarea, [tabindex]';
    var candidates = toArray(document.querySelectorAll(selector));
    var index = candidates.indexOf(input);
    for (var previousIndex = index - 1; previousIndex >= 0; previousIndex -= 1) {
      var candidate = candidates[previousIndex];
      if (candidate.getAttribute("tabindex") === "-1") continue;
      if (isUsableInput(candidate)) return candidate;
    }
    return null;
  }

  function moveToNextInput() {
    if (!activeInput || !commitActive()) return false;
    var next = findNextInput(activeInput);
    if (!next) {
      var form = activeInput.form;
      var submit = form && form.querySelector('[type="submit"]');
      var closed = closeKeyboard("commit");
      if (closed && submit) submit.focus();
      return closed;
    }
    next.focus();
    return openForInput(next);
  }

  function moveToPreviousInput() {
    if (!activeInput || !commitActive()) return false;
    var previous = findPreviousInput(activeInput);
    if (!previous) return false;
    previous.focus();
    return openForInput(previous);
  }

  function restoreNativeInputs() {
    inputs.forEach(function (input) {
      if (input.getAttribute("data-soft-original-readonly") === "false") input.readOnly = false;
    });
  }

  function applySoftReadonly() {
    inputs.forEach(function (input) {
      input.readOnly = true;
    });
  }

  function updateModeControl() {
    if (!toggle) return;
    var soft = mode === "soft" && available;
    toggle.setAttribute("aria-pressed", soft ? "true" : "false");
    toggle.setAttribute(
      "aria-label",
      soft ? "软键盘已开启，点击切换为系统键盘" : "当前使用系统或实体键盘，点击开启软键盘"
    );
    if (toggleState) {
      toggleState.textContent = available
        ? (soft ? "触控输入已开启" : "系统 / 实体键盘")
        : "软键盘组件不可用";
    }
    toggle.disabled = !available;
  }

  function setMode(nextMode, persist) {
    var requested = nextMode === "soft" ? "soft" : "native";
    mode = requested === "soft" && available && enabled ? "soft" : "native";
    if (mode === "soft") {
      applySoftReadonly();
    } else {
      closeKeyboard("keep");
      restoreNativeInputs();
    }
    root.setAttribute("data-keyboard-mode", mode);
    updateModeControl();
    if (persist) safeStoreMode(mode);
    document.dispatchEvent(createEvent("hmi-soft-keyboard-modechange", {
      mode: mode,
      available: available,
      initial: initializing
    }));
    return mode;
  }

  function bindInput(input) {
    if (input.getAttribute("data-soft-keyboard-bound") === "true") return;
    input.setAttribute("data-soft-keyboard-bound", "true");
    input.setAttribute("data-soft-original-readonly", input.readOnly ? "true" : "false");
    input.setAttribute("aria-expanded", "false");
    input.addEventListener("focus", function () {
      if (mode === "soft") openForInput(input);
    });
    input.addEventListener("click", function () {
      if (mode === "soft") openForInput(input);
    });
    input.addEventListener("input", function () {
      input.removeAttribute("aria-invalid");
      if (validationLine) validationLine.textContent = "";
    });
  }

  function refresh() {
    var nextInputs = [];
    toArray(document.querySelectorAll("input, textarea")).forEach(function (input) {
      var bound = input.getAttribute("data-soft-keyboard-bound") === "true";
      if (!bound && !isKeyboardCandidate(input)) return;
      if (!bound) {
        ensureKeyboardAttribute(input);
        bindInput(input);
      }
      nextInputs.push(input);
    });
    inputs = nextInputs;
    if (mode === "soft") applySoftReadonly();
    return inputs.length;
  }

  function hasKeyboardCandidate(node) {
    if (!node || node.nodeType !== 1) return false;
    if (String(node.tagName || "").toLowerCase() === "input" || String(node.tagName || "").toLowerCase() === "textarea") return true;
    return typeof node.querySelector === "function" && node.querySelector("input, textarea") !== null;
  }

  function observeInputs() {
    if (!window.MutationObserver || inputObserver || !document.body) return;
    inputObserver = new window.MutationObserver(function (records) {
      var needsRefresh = records.some(function (record) {
        if (record.type === "childList") {
          return toArray(record.addedNodes).some(hasKeyboardCandidate);
        }
        return hasKeyboardCandidate(record.target);
      });
      if (needsRefresh) refresh();
    });
    inputObserver.observe(document.body, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: ["disabled", "hidden", "type", "inputmode"]
    });
  }

  function validateForm(form) {
    if (!form) return true;
    if (isOpen && activeInput && form.contains(activeInput) && !commitActive()) return false;
    var fields = toArray(form.querySelectorAll("[data-soft-keyboard]")).filter(isUsableInput);
    for (var index = 0; index < fields.length; index += 1) {
      if (!validateInput(fields[index], true)) return false;
    }
    return true;
  }

  function appendPhysicalCharacter(character) {
    if (!activeInput || !keyboard) return;
    var value = keyboard.getInput(activeInputName) || "";
    setKeyboardValue(value + character, true);
  }

  function removePhysicalCharacter() {
    if (!activeInput || !keyboard) return;
    var value = keyboard.getInput(activeInputName) || "";
    setKeyboardValue(value.slice(0, -1), true);
  }

  function handlePhysicalKeyboard(event) {
    if (!isOpen || !activeInput) return;
    if (event.key === "Escape") {
      event.preventDefault();
      closeKeyboard("cancel");
      return;
    }
    if (document.activeElement !== activeInput) return;
    if (event.key === "Tab") {
      if (event.shiftKey && !findPreviousInput(activeInput)) {
        var previousFocusable = findPreviousFocusable(activeInput);
        if (!closeKeyboard("commit")) {
          event.preventDefault();
        } else if (previousFocusable) {
          event.preventDefault();
          previousFocusable.focus();
        }
        return;
      }
      event.preventDefault();
      if (event.shiftKey) {
        moveToPreviousInput();
      } else {
        moveToNextInput();
      }
      return;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      if (activeLayout === "numeric" || findNextInput(activeInput)) {
        moveToNextInput();
      } else {
        handleKeyPress("{done}");
      }
      return;
    }
    if (event.key === "Backspace") {
      event.preventDefault();
      removePhysicalCharacter();
      return;
    }
    if (event.ctrlKey || event.altKey || event.metaKey || event.key.length !== 1) return;
    if (activeLayout === "numeric" && !/^\d$/.test(event.key)) return;
    event.preventDefault();
    appendPhysicalCharacter(event.key);
  }

  function init() {
    if (initialized) {
      refresh();
      if (!available && enabled) {
        available = ensureKeyboard();
        setMode(safeReadMode(), false);
      }
      return available;
    }
    dock = document.getElementById("softKeyboardDock");
    host = document.getElementById("softKeyboardHost");
    fieldName = document.getElementById("softKeyboardFieldName");
    preview = document.getElementById("softKeyboardPreview");
    closeButton = document.getElementById("softKeyboardClose");
    foot = document.getElementById("softKeyboardFoot");
    toggle = document.getElementById("softKeyboardToggle");
    toggleState = document.getElementById("softKeyboardToggleState");
    validationLine = document.getElementById("settingsValidation");

    if (!dock || !host) {
      root.setAttribute("data-keyboard-mode", "native");
      return false;
    }

    initialized = true;
    if (!enabled && toggle) toggle.hidden = true;
    available = enabled && ensureKeyboard();
    refresh();
    setMode(safeReadMode(), false);
    initializing = false;
    observeInputs();

    if (toggle) {
      toggle.addEventListener("click", function () {
        setMode(mode === "soft" ? "native" : "soft", true);
      });
    }
    if (closeButton) {
      closeButton.addEventListener("click", function () {
        closeKeyboard("commit");
      });
    }
    dock.addEventListener("mousedown", function (event) {
      event.preventDefault();
    });
    document.addEventListener("keydown", handlePhysicalKeyboard, true);
    return available;
  }

  window.HMISoftKeyboard = {
    init: init,
    refresh: refresh,
    open: openForInput,
    close: closeKeyboard,
    commitActive: commitActive,
    validateInput: validateInput,
    validateForm: validateForm,
    setMode: setMode,
    getMode: function () { return mode; },
    isOpen: function () { return isOpen; },
    isAvailable: function () { return available; }
  };

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})(window, document);
