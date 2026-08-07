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
		`/api/v2/maintenance/production`,
		`/api/v2/maintenance/connectivity`,
		`/api/v2/maintenance/wifi/connect`,
		`id="operatorName"`,
		`assets/soft-keyboard.css?v=20260807.5`,
		`assets/soft-keyboard.js?v=20260807.5`,
		`import("./assets/hmi.mjs?v=20260807.5")`,
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
	if !strings.Contains(page, `query.get("demo") !== "1" || query.get("__demoFrame") === "1"`) ||
		!strings.Contains(page, `new URL("demo-shell.html", window.location.href)`) {
		t.Fatal("demo entry does not route through the fixed viewport shell")
	}
	if !regexp.MustCompile(`(?s)\.page\[data-page="maintenance"\] \.settings-layout \{.*?overflow: hidden;`).MatchString(page) ||
		!regexp.MustCompile(`(?s)\.maintenance-panel \{.*?overflow-y: auto;.*?overscroll-behavior: contain;`).MatchString(page) {
		t.Fatal("maintenance panels do not have isolated local scrolling")
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
		`authRequest("/api/v2/auth/initial-admin", "GET")`,
		`authRequest("/api/v2/config/session", "GET")`,
		`authRequest("/api/v2/auth/login", "POST"`,
		`authRequest("/api/v2/auth/password", "POST"`,
		`authRequest("/api/v2/config/session", "PUT"`,
		`await this.loadAuthenticationState();`,
		`this.refreshFrontendSession();`,
		`export function renewFrontendSession(session: FrontendSession | null, idleTimeoutSeconds: number, now = Date.now()): FrontendSession | null`,
		`if (session === null || !frontendSessionIsActive(session, now))`,
		`const renewed = renewFrontendSession(this.session, this.idleTimeoutSeconds);`,
		`this.becomeGuest();`,
		`new Event("block-hmi-guest")`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("HMI bridge is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		`/api/v2/auth/status`,
		`/api/v2/auth/activity`,
		`/api/v2/auth/logout`,
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
	if !regexp.MustCompile(`private sendPLCScan[\s\S]*?requirePermission\("maintenance"\)`).MatchString(source) {
		t.Fatal("HMI PLC actions are not protected by the frontend maintenance gate")
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
	for _, asset := range []string{
		"assets/demo-shell.mjs",
		"assets/hmi.mjs",
		"assets/machine-bin.png",
		"assets/soft-keyboard.css",
		"assets/soft-keyboard.js",
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
