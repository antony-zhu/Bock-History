package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestStaticHMIUsesStatelessFrontendPermissions(t *testing.T) {
	index, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(index)
	for _, required := range []string{
		`id="hmi"`,
		`data-page="home"`,
		`data-page="manual"`,
		`data-page="data"`,
		`data-page="maintenance"`,
		`data-page="alarm"`,
		`data-page="history"`,
		`id="login-form"`,
		`id="initial-admin-form"`,
		`id="password-form"`,
		`id="session-policy-form"`,
		`id="plc-scan-button"`,
		`id="plc-disconnect-button"`,
		`id="snapshot-button"`,
		`data-maintenance-tab="production"`,
		`data-maintenance-tab="wifi"`,
		`data-maintenance-tab="plc"`,
		`data-maintenance-tab="accounts"`,
		`data-maintenance-panel="production"`,
		`data-maintenance-panel="wifi"`,
		`data-maintenance-panel="plc"`,
		`data-maintenance-panel="accounts"`,
		`/api/maintenance/production`,
		`/api/maintenance/connectivity`,
		`/api/maintenance/wifi/connect`,
		`id="operatorName"`,
		`assets/soft-keyboard.css?v=20260808.3`,
		`assets/soft-keyboard.js?v=20260808.3`,
		`import("./assets/hmi.mjs?v=20260808.1")`,
		`function requireFrontendPermission(permission)`,
		`window.BlockHMIReady.then(syncFrontendPermissions)`,
		`name === "maintenance" && !requireFrontendPermission("maintenance")`,
		`window.addEventListener("block-hmi-guest", () => {`,
		`switchPage("home")`,
	} {
		if !strings.Contains(page, required) {
			t.Fatalf("index is missing %q", required)
		}
	}
	if strings.Contains(page, "api-client.js") {
		t.Fatal("old REST polling client is still loaded by the HMI page")
	}
	for _, forbidden := range []string{
		`<span class="nav-en">`,
		`<div class="meta-en">Operator</div>`,
		`id="modeEn"`,
		`id="modeState"`,
		`远程联机`,
	} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("footer still contains removed secondary copy %q", forbidden)
		}
	}
	if !strings.Contains(page, `id="operatorName">登录</div>`) {
		t.Fatal("guest identity is not reduced to one Chinese login label")
	}
	if !regexp.MustCompile(`(?s)#hmi-footer \{.*?--footer-control-height: 102px;.*?grid-template-columns: 220px minmax\(0, 1fr\) 220px;`).MatchString(page) ||
		!regexp.MustCompile(`(?s)#hmi-footer \.nav-button,.*?#hmi-footer #operatorName,.*?#hmi-footer \.mode \{.*?height: var\(--footer-control-height\);.*?min-height: var\(--footer-control-height\);`).MatchString(page) ||
		!regexp.MustCompile(`(?s)#hmi-footer \.operator \{.*?height: var\(--footer-control-height\);`).MatchString(page) ||
		!regexp.MustCompile(`(?s)#hmi-footer #operatorName,.*?#hmi-footer \.mode \{.*?width: 168px;.*?border-radius: 18px;`).MatchString(page) {
		t.Fatal("footer end controls do not share the navigation visible height")
	}
	if !regexp.MustCompile(`(?s)#hmi-footer #operatorName::before \{.*?data:image/svg\+xml`).MatchString(page) ||
		!regexp.MustCompile(`(?s)#hmi-footer \.mode\.is-auto \{.*?color: #176b38;.*?background: #e8f7ec;`).MatchString(page) ||
		!regexp.MustCompile(`(?s)#hmi-footer \.mode\.is-manual \{.*?color: #8a6200;.*?background: #fff5d7;`).MatchString(page) {
		t.Fatal("footer identity and mode state visuals are incomplete")
	}
	if !regexp.MustCompile(`(?s)\.auth-sheet \{.*?max-height: min\(800px, calc\(100vh - 48px\)\);.*?padding: 60px 32px;.*?width: min\(500px, 100%\);`).MatchString(page) {
		t.Fatal("authentication sheet is not using the narrow, bounded layout")
	}
	if !regexp.MustCompile(`(?s)<div class="auth-sheet">.*?id="authLogin".*?id="authBootstrap"`).MatchString(page) {
		t.Fatal("login and bootstrap do not share the same authentication sheet")
	}
	if !regexp.MustCompile(`(?s)id="login-form".*?<div class="auth-field auth-actions">.*?<button type="submit">`).MatchString(page) ||
		!regexp.MustCompile(`(?s)id="initial-admin-form".*?<div class="auth-field auth-actions">.*?<button type="submit">`).MatchString(page) ||
		!regexp.MustCompile(`(?s)#authLogin \.auth-form \.auth-actions,.*?#authBootstrap \.auth-form \.auth-actions \{.*?justify-self: stretch;.*?width: 100%;`).MatchString(page) ||
		!regexp.MustCompile(`(?s)#authLogin \.auth-form \.auth-actions > button\[type="submit"\],.*?#authBootstrap \.auth-form \.auth-actions > button\[type="submit"\] \{.*?grid-column: 1 / -1;.*?justify-self: stretch;.*?width: 100%;`).MatchString(page) {
		t.Fatal("login and bootstrap submit buttons do not span the centered two-column form")
	}
	if !strings.Contains(page, `query.get("demo") !== "1" || query.get("__demoFrame") === "1"`) ||
		!strings.Contains(page, `new URL("demo-shell.html", window.location.href)`) {
		t.Fatal("demo entry does not route through the fixed viewport shell")
	}
	if !regexp.MustCompile(`(?s)\.page\[data-page="maintenance"\] \.settings-layout \{.*?overflow: hidden;`).MatchString(page) ||
		!regexp.MustCompile(`(?s)\.maintenance-panel \{.*?overflow-y: auto;.*?overscroll-behavior: contain;`).MatchString(page) {
		t.Fatal("maintenance panels do not have isolated local scrolling")
	}
	if regexp.MustCompile(`\.line-name::after|html\[data-backend-status="online"\] \.line-name::after`).MatchString(page) {
		t.Fatal("top backend-status badge is still rendered")
	}
	for _, removed := range []string{
		`选择一项本机配置`,
		`目标、换刀与装箱`,
		`本机无线连接`,
		`只读采集状态`,
		`本机账户与会话`,
	} {
		if strings.Contains(page, removed) {
			t.Fatalf("maintenance sidebar still contains removed copy %q", removed)
		}
	}
	if !strings.Contains(page, `<div class="settings-actions wifi-settings-actions"><button id="wifiRefreshButton" type="button">刷新状态</button><button id="saveWifiButton" type="submit">连接 Wi-Fi</button></div>`) ||
		strings.Index(page, `<div class="settings-actions wifi-settings-actions"`) > strings.Index(page, `<dl class="connection-facts" aria-label="Wi-Fi 与 BDM 当前状态">`) {
		t.Fatal("Wi-Fi actions are not rendered ahead of the connection facts")
	}
	wifiConnect := regexp.MustCompile(`(?s)async function connectWiFi\(\) \{.*?\n      \}\n\n      function switchMaintenanceTab`).FindString(page)
	if wifiConnect == "" ||
		!regexp.MustCompile(`(?s)try \{.*?await maintenanceRequest\("/api/maintenance/wifi/connect", "POST", \{ ssid, password \}\);.*?passwordInput\.value = "";\s*window\.HMISoftKeyboard\?\.clearInput\(passwordInput\);.*?return true;`).MatchString(wifiConnect) ||
		!regexp.MustCompile(`(?s)catch \(error\) \{.*?return false;.*?finally \{\s*wifiRequestInFlight = false;`).MatchString(wifiConnect) ||
		regexp.MustCompile(`(?s)finally \{.*?passwordInput\.value`).MatchString(wifiConnect) ||
		strings.Contains(wifiConnect, "localStorage") || strings.Contains(wifiConnect, "sessionStorage") || strings.Contains(wifiConnect, "console.") {
		t.Fatal("Wi-Fi connection no longer clears passwords only after success without persisting or logging them")
	}
	passwordInputs := regexp.MustCompile(`<input id="([^"]+)"[^>]*type="password"[^>]*>`).FindAllStringSubmatch(page, -1)
	passwordToggles := regexp.MustCompile(`<button[^>]*aria-controls="([^"]+)"[^>]*data-password-toggle`).FindAllStringSubmatch(page, -1)
	if len(passwordInputs) != 7 || len(passwordToggles) != len(passwordInputs) {
		t.Fatalf("password visibility controls do not cover every password input: inputs=%d toggles=%d", len(passwordInputs), len(passwordToggles))
	}
	passwordInputIDs := map[string]bool{}
	for _, match := range passwordInputs {
		if match[1] != "wifiPasswordInput" && !strings.Contains(match[0], `maxlength="4096"`) {
			t.Fatalf("password input %q does not enforce the v2 4096-character maximum", match[1])
		}
		passwordInputIDs[match[1]] = true
	}
	for _, match := range passwordToggles {
		if !passwordInputIDs[match[1]] {
			t.Fatalf("password visibility control targets unknown input %q", match[1])
		}
	}
	if !strings.Contains(page, `class="password-visibility-toggle" type="button"`) || !strings.Contains(page, `aria-label="显示密码"`) || !strings.Contains(page, `aria-pressed="false"`) {
		t.Fatal("password visibility controls are missing button semantics or accessible state")
	}
	for _, usernameInput := range []string{
		`id="login-username" name="username" maxlength="128"`,
		`id="initial-admin-username" name="username" value="admin" maxlength="128"`,
	} {
		if !strings.Contains(page, usernameInput) {
			t.Fatalf("username input does not enforce the v2 128-character maximum: %q", usernameInput)
		}
	}
	if !strings.Contains(page, `}, 650);`) || strings.Contains(page, `backend.updateSettings`) {
		t.Fatal("production settings do not use the dedicated 650 ms local Agent save path")
	}
	keyboardCSS, err := os.ReadFile("assets/soft-keyboard.css")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?s)#auth-panel\[data-keyboard-open="true"\] \{.*?--auth-sheet-top-gap:.*?padding-bottom: calc\(100vh - var\(--auth-keyboard-top`).Match(keyboardCSS) {
		t.Fatal("auth keyboard layout does not reserve a bounded area above the keyboard")
	}
	if !regexp.MustCompile(`(?s)#auth-panel\[data-keyboard-open="true"\] \.auth-form \.auth-field \{.*?grid-template-columns: 90px minmax\(0, 1fr\);`).Match(keyboardCSS) {
		t.Fatal("keyboard-open authentication form no longer keeps the two-column submit layout")
	}
	if !regexp.MustCompile(`(?s)\.page\[data-page="maintenance"\] \.wifi-settings-actions \{.*?display: grid;.*?grid-template-columns: repeat\(2, minmax\(0, 1fr\)\);`).Match(keyboardCSS) ||
		!regexp.MustCompile(`(?s)\.page\[data-page="maintenance"\] \.wifi-settings-actions button \{.*?min-height: 56px;.*?cursor: pointer;`).Match(keyboardCSS) {
		t.Fatal("Wi-Fi actions do not have a visible two-button layout")
	}
	if !regexp.MustCompile(`(?s)html\[data-soft-keyboard-open="true"\]\[data-soft-keyboard-layout="full"\] \[data-page="maintenance"\]:not\(\[hidden\]\) \.settings-layout \{.*?height: 332px;`).Match(keyboardCSS) ||
		!regexp.MustCompile(`(?s)html\[data-soft-keyboard-open="true"\]\[data-soft-keyboard-layout="full"\] #wifiSettingsPanel \{.*?scroll-padding: 20px 0 92px;`).Match(keyboardCSS) ||
		!regexp.MustCompile(`(?s)html\[data-soft-keyboard-open="true"\]\[data-soft-keyboard-layout="full"\] #wifiSettingsPanel \.maintenance-panel-head \{.*?display: none;`).Match(keyboardCSS) ||
		!regexp.MustCompile(`(?s)html\[data-soft-keyboard-open="true"\]\[data-soft-keyboard-layout="full"\] #wifiSettingsPanel \.wifi-settings-actions \{.*?margin-top: 10px;`).Match(keyboardCSS) {
		t.Fatal("full keyboard Wi-Fi layout does not reserve clickable space above the overlay")
	}
	if !regexp.MustCompile(`(?s)\.soft-keyboard-dock\[data-open-immediate="true"\] \{.*?transition: none;`).Match(keyboardCSS) ||
		!regexp.MustCompile(`(?s)#auth-panel\[data-keyboard-open="true"\] ~ #hmi \.soft-keyboard-dock \{.*?transition: none;`).Match(keyboardCSS) {
		t.Fatal("authentication keyboard entrance still animates after the sheet is visible")
	}

	bridge, err := os.ReadFile("assets/hmi.mts")
	if err != nil {
		t.Fatal(err)
	}
	source := string(bridge)
	for _, required := range []string{
		`private prepareGuestHMI(): void`,
		`private moveLocalAdministrationToMaintenance(): void`,
		`#accountSettingsPanel`,
		`private renderPLCReadOnly(): void`,
		`private bindPasswordVisibilityToggles(): void`,
		`private openAuthWithKeyboard(screen: AuthScreen, message = ""): void`,
		`keyboard?.init();`,
		`keyboard.open(input, { immediate: true });`,
		`input.focus({ preventScroll: true });`,
		`private requirePermission(permission: "operate" | "maintenance"): boolean`,
		`this.openSocket();`,
		`buildRuntimeConfigure(this.config.points)`,
		`private async authRequest(path: string, method: "GET" | "POST" | "PUT"`,
		`authRequest("/api/auth/initial-admin", "GET")`,
		`authRequest("/api/config/session", "GET")`,
		`authRequest("/api/auth/login", "POST"`,
		`authRequest("/api/auth/password", "POST"`,
		`authRequest("/api/config/session", "PUT"`,
		`await this.loadAuthenticationState();`,
		`this.refreshFrontendSession();`,
		`export function renewFrontendSession(session: FrontendSession | null, idleTimeoutSeconds: number, now = Date.now()): FrontendSession | null`,
		`if (session === null || !frontendSessionIsActive(session, now))`,
		`const renewed = renewFrontendSession(this.session, this.idleTimeoutSeconds);`,
		`private setAuthSubmitBusy(form: HTMLFormElement, busy: boolean): void`,
		`private publishLiveState(): void`,
		`window.addEventListener("hmi-soft-keyboard-statechange", () => this.flushDeferredLiveState());`,
		`this.becomeGuest();`,
		`new Event("block-hmi-guest")`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("HMI bridge is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		`/api/v1/`,
		`/api/v2/`,
		`/api/auth/status`,
		`/api/auth/activity`,
		`/api/auth/logout`,
		`localStorage`,
		`sessionStorage`,
		`crypto.subtle`,
		`passwordDigest`,
		`block-hmi-local-`,
		`event.code === 4401`,
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("HMI bridge still contains backend auth dependency %q", forbidden)
		}
	}
	authOpen := regexp.MustCompile(`(?s)private openAuthWithKeyboard\(screen: AuthScreen, message = ""\): void \{.*?\n  \}\n\n  private showLogin`).FindString(source)
	if authOpen == "" ||
		!regexp.MustCompile(`(?s)keyboard\?\.init\(\);.*?panel\.hidden = false;.*?panel\.setAttribute\("data-auth-mode", screen\);.*?keyboard\.open\(input, \{ immediate: true \}\);.*?input\.focus\(\{ preventScroll: true \}\);`).MatchString(authOpen) ||
		strings.Contains(authOpen, "requestAnimationFrame") || strings.Contains(authOpen, "setTimeout") {
		t.Fatal("authentication sheet and keyboard are not opened in one synchronous path")
	}
	if !regexp.MustCompile(`private sendCommand[\s\S]*?requirePermission\("operate"\)`).MatchString(source) {
		t.Fatal("HMI point commands are not protected by the frontend permission gate")
	}
	modeCommand := regexp.MustCompile(`(?s)private sendCommand\(command: string, payload: Record<string, unknown> = \{\}\): Promise<\{ state: LegacyState \}> \{.*?\n  \}\n\n  private acknowledgeAlarm`).FindString(source)
	if modeCommand == "" ||
		!strings.Contains(modeCommand, `command === "set_mode"`) ||
		!strings.Contains(modeCommand, `displayPath: "home.machine.enabled", action: "toggle"`) ||
		!strings.Contains(modeCommand, `buildPointCommand(pointID, operation.action, requestId)`) {
		t.Fatal("mode switching does not use the configured PLC toggle point")
	}
	if !regexp.MustCompile(`(?s)const enabled = this\.valueFor\("home\.machine\.enabled"\);.*?state\.mode = enabled === true \? "auto" : "manual";`).MatchString(source) {
		t.Fatal("mode display is not derived from the PLC enabled point")
	}
	if !strings.Contains(source, `document.querySelectorAll<HTMLButtonElement>(".control-button:not(.manual-entry-button)")`) ||
		!strings.Contains(source, `const available = runtimeEnabled && button === start;`) ||
		!strings.Contains(source, `mode.dataset.backendUnavailable = runtimeEnabled ? "false" : "true";`) ||
		strings.Contains(source, `mode.dataset.backendUnavailable = "true";`) {
		t.Fatal("production mode controls are not enabled only while the runtime is writable")
	}
	modeChange := regexp.MustCompile(`(?s)async function requestModeChange\(\) \{.*?\n      \}\n\n      async function handleAction`).FindString(page)
	if modeChange == "" ||
		!strings.Contains(modeChange, `if (!requireFrontendPermission("operate")) return false;`) ||
		!strings.Contains(modeChange, `state.mode === "auto" ? "manual" : "auto"`) ||
		!strings.Contains(modeChange, `backend.sendCommand("set_mode"`) ||
		!strings.Contains(modeChange, `runBackendMutation(`) {
		t.Fatal("mode toggle does not preserve the guest gate and both manual/auto directions")
	}
	if !regexp.MustCompile(`(?s)async function runBackendMutation\(factory, options = \{\}\) \{.*?showToast\(\s*backendErrorMessage\(mutationError\)`).MatchString(page) {
		t.Fatal("mode command failures are not surfaced through the HMI toast")
	}
	visibleCopy := regexp.MustCompile(`/api(?:/[a-z-]+)+`).ReplaceAllString(page+source, "")
	if regexp.MustCompile(`(?i)\bv2\b`).MatchString(visibleCopy) {
		t.Fatal("user-visible HMI copy still exposes a v2 label")
	}
	if !regexp.MustCompile(`private sendPLCScan[\s\S]*?requirePermission\("maintenance"\)`).MatchString(source) {
		t.Fatal("HMI PLC actions are not protected by the frontend maintenance gate")
	}
	bootstrap := regexp.MustCompile(`(?s)private async createInitialAdmin\(username: string, password: string, confirmPassword: string\): Promise<void> \{.*?\n  \}\n\n  private async changePassword`).FindString(source)
	if bootstrap == "" ||
		!strings.Contains(bootstrap, `if (this.initialAdminInFlight)`) ||
		!strings.Contains(bootstrap, `this.initialAdminInFlight = true;`) ||
		!strings.Contains(bootstrap, `this.beginSession(identity);`) ||
		!strings.Contains(bootstrap, `this.emitPageNotice("管理员创建成功");`) ||
		strings.Contains(bootstrap, `/api/auth/login`) {
		t.Fatal("initial-admin completion must be single-flight and enter the in-memory admin session without a second login")
	}
	if !regexp.MustCompile(`(?s)message\.type === "points\.changed".*?this\.publishLiveState\(\);`).MatchString(source) {
		t.Fatal("PLC updates still render through the full input path instead of coalescing while an input is active")
	}
	if !strings.Contains(page, `function flushDeferredServerRender()`) ||
		!regexp.MustCompile(`(?s)function applyServerState\(nextState, options = \{\}\).*?if \(inputInteractionActive\(\)\).*?deferredServerRender = true;.*?return true;`).MatchString(page) {
		t.Fatal("input interaction does not defer full-page state rendering")
	}
	for _, forbidden := range []string{`requestFullscreen`, `exitFullscreen`, `alert(`, `confirm(`, `prompt(`} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("HMI page still permits a native browser dialog or chrome surface: %q", forbidden)
		}
	}
	if !strings.Contains(source, `toggle.addEventListener("pointerdown", (event) => event.preventDefault())`) || !strings.Contains(source, `input.type = input.type === "password" ? "text" : "password";`) {
		t.Fatal("password visibility toggle does not preserve the current input target")
	}
	keyboardSource, err := os.ReadFile("assets/soft-keyboard.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(keyboardSource), `window.dispatchEvent(new window.Event("hmi-soft-keyboard-ready"))`) {
		t.Fatal("soft keyboard does not announce initialization completion")
	}
	keyboard := string(keyboardSource)
	for _, required := range []string{
		`"1 2 3 4 5 6 7 8 9 0 - = ."`,
		`"{shift} z x c v b n m . - {bksp}"`,
		`var shiftIcon = '<svg`,
		`var backspaceIcon = '<svg`,
		`value: "切换大小写"`,
		`value: "退格"`,
		`function isAuthenticationInput(input)`,
	} {
		if !strings.Contains(keyboard, required) {
			t.Fatalf("soft keyboard is missing %q", required)
		}
	}
	if !regexp.MustCompile(`function validateInput\(input, focusOnError\) \{[\s\S]*?isAuthenticationInput\(input\)`).MatchString(keyboard) {
		t.Fatal("authentication validation can still reserve keyboard error space")
	}
	if !strings.Contains(keyboard, `disableButtonHold: true`) ||
		!regexp.MustCompile(`function ensureKeyboard\(\) \{\s*if \(keyboard\) return true;`).MatchString(keyboard) ||
		!regexp.MustCompile(`function bindInput\(input\) \{\s*if \(input\.getAttribute\("data-soft-keyboard-bound"\) === "true"\) return;`).MatchString(keyboard) ||
		!regexp.MustCompile(`(?s)function clearInput\(input\) \{.*?if \(input === activeInput\) \{\s*originalValue = "";\s*committedValue = "";\s*\}.*?keyboard\.setInput\("", inputName\);.*?dispatchFieldEvent\(input, "input"\);`).MatchString(keyboard) {
		t.Fatal("soft keyboard does not bind once, suppress held-key repeats, and fully clear successful secret input")
	}
	for _, asset := range []string{
		"assets/demo-shell.mjs",
		"assets/hmi.mjs",
		"assets/machine-bin.png",
		"assets/soft-keyboard.css",
		"assets/soft-keyboard.js",
		"tools/maintenance-keyboard-layout-probe.mjs",
		"assets/vendor/simple-keyboard/index.css",
		"assets/vendor/simple-keyboard/index.js",
		"THIRD_PARTY_NOTICES.md",
	} {
		if _, err := os.Stat(asset); err != nil {
			t.Fatalf("required HMI asset %q is missing: %v", asset, err)
		}
	}
}

func TestDemoShellUsesFixedIndustrialViewport(t *testing.T) {
	contents, err := os.ReadFile("demo-shell.html")
	if err != nil {
		t.Fatal(err)
	}
	shell := string(contents)
	for _, required := range []string{
		`id="demoFrame"`,
		`width="1920"`,
		`height="1080"`,
		`width: 1920px;`,
		`height: 1080px;`,
		`transform: scale(var(--demo-scale, 1));`,
		`id="demoInputBridge"`,
		`frame.contentWindow?.postMessage({ type: "block-hmi-demo-input", ...point }, window.location.origin)`,
		`demoFrameURL(window.location.href)`,
		`demoDisplayScale(window.innerWidth, window.innerHeight)`,
		`window.history.replaceState(null, "", demoVisibleURL(window.location.href).href)`,
	} {
		if !strings.Contains(shell, required) {
			t.Fatalf("demo shell is missing %q", required)
		}
	}
}

func TestManualPageKeepsDemoInteractionsSeparateFromPLCCommands(t *testing.T) {
	contents, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(contents)
	for _, required := range []string{
		`data-page="manual"`,
		`id="manualPageEntry"`,
		`id="manualReturnHome"`,
		`id="manualXPosition"`,
		`id="manualZPosition"`,
		`id="manualXLoad"`,
		`id="manualZLoad"`,
		`id="manualXSpeedInput"`,
		`id="manualZSpeedInput"`,
		`id="manualAdvancedMount"`,
		`function renderManualAdmin()`,
		`mount.replaceChildren();`,
		`权限来自当前产品设计：源点位表没有管理员角色列。`,
		`if (demoManualStart) switchPage("manual");`,
		`["home", "manual", "data", "alarm", "history"].includes(name)`,
		`.control-button:not(.manual-entry-button)`,
	} {
		if !strings.Contains(page, required) {
			t.Fatalf("manual page is missing %q", required)
		}
	}
	if strings.Contains(page, `id="manualPageEntry" type="button" data-action=`) ||
		!strings.Contains(page, `$("#manualPageEntry").addEventListener("click", () => switchPage("manual"));`) {
		t.Fatal("home manual entry no longer performs public-only page navigation")
	}
	markup := strings.Split(page, `<script src="assets/vendor/simple-keyboard/index.js"></script>`)[0]
	if strings.Contains(markup, `data-manual-admin=`) {
		t.Fatal("operator and guest markup still contains prebuilt administrator nodes")
	}
	manualHandler := regexp.MustCompile(`(?s)function handleManualAction\(button\) \{.*?\n      \}\n\n      function bindManualPage`).FindString(page)
	if manualHandler == "" ||
		!strings.Contains(manualHandler, `if (!requireFrontendPermission("operate")) return false;`) ||
		!strings.Contains(manualHandler, `当前页面未绑定现场写入`) ||
		strings.Contains(manualHandler, "sendCommand") ||
		strings.Contains(manualHandler, "point.command") ||
		strings.Contains(manualHandler, "WebSocket") {
		t.Fatal("manual actions are not an isolated local demo interaction")
	}
	bridge, err := os.ReadFile("assets/hmi.mts")
	if err != nil {
		t.Fatal(err)
	}
	source := string(bridge)
	for _, required := range []string{
		`export function demoManualRoleFromSearch(search: string): FrontendRole`,
		`role(): FrontendRole;`,
		`role: () => this.frontendRole()`,
		`if (this.demo && this.manualRole !== "GUEST")`,
		`private frontendRole(): FrontendRole`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("manual role bridge is missing %q", required)
		}
	}
}

func TestHomeRendersOnlyTwoHorizontalBins(t *testing.T) {
	contents, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(contents)
	if !regexp.MustCompile(`(?s)\.bin-row \{.*?grid-template-columns:\s*repeat\(2,\s*minmax\(0,\s*1fr\)\);.*?align-items:\s*stretch;`).MatchString(page) {
		t.Fatal("home bins are not a two-column equal-height grid")
	}
	bins := regexp.MustCompile(`<div class="bin [^"]+" data-bin="([0-9]+)">`).FindAllStringSubmatch(page, -1)
	if len(bins) != 2 || bins[0][1] != "0" || bins[1][1] != "1" {
		t.Fatalf("home must render only bins 0 and 1, got %#v", bins)
	}
	renderBins := regexp.MustCompile(`(?s)function renderBins\(\) \{.*?\n      \}`).FindString(page)
	if renderBins == "" || !strings.Contains(renderBins, `state.bins[index]`) {
		t.Fatal("home bin rendering no longer preserves the existing state binding")
	}
}

func TestDemoInputBridgeOnlyRunsInDemoFrame(t *testing.T) {
	contents, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(contents)
	for _, required := range []string{
		`get("__demoFrame") !== "1"`,
		`event.source !== window.parent`,
		`document.elementFromPoint(x, y)`,
		`target.matches(".hg-button")`,
	} {
		if !strings.Contains(page, required) {
			t.Fatalf("demo input bridge is missing %q", required)
		}
	}
}

func TestPointsJSONKeepsDisplayBindingsOutOfRuntimePoints(t *testing.T) {
	contents, err := os.ReadFile("assets/points.json")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		ScanIntervalMs int              `json:"scanIntervalMs"`
		Points         []map[string]any `json:"points"`
		Bindings       []struct {
			DisplayPath string `json:"displayPath"`
			Description string `json:"description"`
		} `json:"bindings"`
	}
	if err := json.Unmarshal(contents, &config); err != nil {
		t.Fatal(err)
	}
	if config.ScanIntervalMs != 50 || len(config.Points) == 0 || len(config.Bindings) == 0 {
		t.Fatalf("incomplete points.json: %+v", config)
	}
	for _, point := range config.Points {
		if point["writeMethod"] != nil && point["writeMethod"] != "maskWrite" {
			t.Fatalf("point has unexpected write method: %+v", point)
		}
		if _, ok := point["displayPath"]; ok {
			t.Fatalf("runtime point leaked displayPath: %+v", point)
		}
		if _, ok := point["description"]; ok {
			t.Fatalf("runtime point leaked description: %+v", point)
		}
	}
	for _, binding := range config.Bindings {
		if !isEnglishDisplayPath(binding.DisplayPath) {
			t.Fatalf("display path is not an English dotted path: %q", binding.DisplayPath)
		}
		if binding.Description == "" {
			t.Fatalf("binding description is empty for %q", binding.DisplayPath)
		}
	}
}

func isEnglishDisplayPath(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, letter := range part {
			if letter < 'a' || letter > 'z' {
				return false
			}
		}
	}
	return true
}
