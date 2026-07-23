package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDefaultAgentTimeoutIsEightSeconds(t *testing.T) {
	if defaultAgentTimeout != 8*time.Second {
		t.Fatalf("default Agent timeout = %s, want 8s", defaultAgentTimeout)
	}
}

func TestControllerErrorsKeepDistinctPublicCodes(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "revision", err: errRevisionConflict, wantStatus: http.StatusConflict, wantCode: "revision_conflict"},
		{name: "safety", err: errSafetyInterlock, wantStatus: http.StatusConflict, wantCode: "safety_interlock"},
		{name: "idempotency", err: errIdempotencyConflict, wantStatus: http.StatusConflict, wantCode: "idempotency_conflict"},
		{name: "alarm", err: errAlarmNotFound, wantStatus: http.StatusNotFound, wantCode: "alarm_not_found"},
		{name: "device", err: errDeviceUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: "device_unavailable"},
		{name: "quality", err: errBadQuality, wantStatus: http.StatusServiceUnavailable, wantCode: "bad_quality"},
		{name: "stale", err: errDataStale, wantStatus: http.StatusServiceUnavailable, wantCode: "data_stale"},
		{name: "unknown outcome", err: errOutcomeUnknown, wantStatus: http.StatusGatewayTimeout, wantCode: "command_outcome_unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeControllerError(response, errors.Join(errors.New("wrapped"), test.err))
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			var payload errorEnvelope
			decodeResponse(t, response.Result(), &payload)
			if payload.Error.Code != test.wantCode {
				t.Fatalf("code = %q, want %q", payload.Error.Code, test.wantCode)
			}
		})
	}
}

func TestBrowserErrorHandlingUsesCodesAndRefreshesOnlyRevisionConflict(t *testing.T) {
	contents, err := embeddedFiles.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	index := string(contents)
	for _, code := range []string{
		"revision_conflict",
		"safety_interlock",
		"idempotency_conflict",
		"alarm_not_found",
		"device_unavailable",
		"bad_quality",
		"data_stale",
		"command_outcome_unknown",
		`case "unknown"`,
	} {
		if !strings.Contains(index, code) {
			t.Fatalf("browser error handling does not distinguish %q", code)
		}
	}
	if !strings.Contains(index, `if (mutationError.code === "revision_conflict")`) {
		t.Fatal("revision_conflict does not trigger an explicit refresh")
	}
	for _, forbidden := range []string{
		`mutationError.status === 409`,
		`mutationError.code === "timeout") {
            await refreshBackendState`,
	} {
		if strings.Contains(index, forbidden) {
			t.Fatalf("non-revision errors can trigger refresh via %q", forbidden)
		}
	}
}

func TestInitialPageContainsNoFabricatedProductionOrAlarmData(t *testing.T) {
	contents, err := embeddedFiles.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	index := string(contents)
	for _, forbidden := range []string{
		`id="homeCycle">30</output>`,
		`id="homeOutput">30</output>`,
		`id="dataOee">92</output>`,
		`value="100" required`,
		"库位3定位异常",
		"库位2余量不足",
		"系统自检完成",
	} {
		if strings.Contains(index, forbidden) {
			t.Fatalf("initial page still contains fabricated value %q", forbidden)
		}
	}
	for _, required := range []string{
		`id="systemStatus">等待可信数据</span>`,
		`id="homeCycle">--</output>`,
		`target: 0`,
		`bins: []`,
		`alarms: []`,
		`history: []`,
		`let hasTrustedData = false`,
		`暂无可信设备数据`,
	} {
		if !strings.Contains(index, required) {
			t.Fatalf("initial no-data contract is missing %q", required)
		}
	}
}

func TestBrowserNormalizesExplicitNullCollectionsBeforeStateValidation(t *testing.T) {
	contents, err := embeddedFiles.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	index := string(contents)
	normalization := `if (normalized[key] === null) normalized[key] = [];`
	applyNormalization := `nextState = normalizeEmptyStateCollections(nextState);`
	validation := `if (!isCompleteServerState(nextState)) return false;`
	for _, required := range []string{normalization, applyNormalization, validation} {
		if !strings.Contains(index, required) {
			t.Fatalf("browser empty-collection recovery is missing %q", required)
		}
	}
	normalizationIndex := strings.Index(index, applyNormalization)
	validationIndex := strings.Index(index, validation)
	if normalizationIndex < 0 || validationIndex < 0 || normalizationIndex > validationIndex {
		t.Fatal("browser validates state before normalizing explicit null collections")
	}
}

func TestBrowserFetchTimeoutAbortsUnderlyingRequest(t *testing.T) {
	contents, err := embeddedFiles.ReadFile("assets/api-client.js")
	if err != nil {
		t.Fatal(err)
	}
	client := string(contents)
	for _, required := range []string{
		`: 12000;`,
		`new window.AbortController()`,
		`controller.abort()`,
		`signal: controller.signal`,
		`error.name === "AbortError"`,
	} {
		if !strings.Contains(client, required) {
			t.Fatalf("browser timeout contract is missing %q", required)
		}
	}
	if strings.Contains(client, "Promise.race") {
		t.Fatal("timeout still races without canceling the underlying fetch")
	}
}
