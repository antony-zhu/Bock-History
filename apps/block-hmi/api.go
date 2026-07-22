package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxJSONBody = 64 << 10

type apiServer struct {
	controller HMIController
}

type settingsRequest struct {
	Target           *int    `json:"target"`
	ToolLimit        *int    `json:"toolLimit"`
	InspectInterval  *int    `json:"inspectInterval"`
	ExpectedRevision *uint64 `json:"expectedRevision,omitempty"`
	Operator         string  `json:"operator,omitempty"`
}

type commandRequest struct {
	Command          string  `json:"command"`
	Mode             string  `json:"mode,omitempty"`
	Paused           *bool   `json:"paused,omitempty"`
	ExpectedRevision *uint64 `json:"expectedRevision,omitempty"`
	Operator         string  `json:"operator,omitempty"`
}

type acknowledgeRequest struct {
	ExpectedRevision *uint64 `json:"expectedRevision,omitempty"`
	Operator         string  `json:"operator,omitempty"`
}

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func newAPIHandler(controller HMIController) http.Handler {
	server := &apiServer{controller: controller}
	return http.HandlerFunc(server.serveHTTP)
}

func (a *apiServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	switch {
	case r.URL.Path == "/api/v1/state":
		a.handleState(w, r)
	case r.URL.Path == "/api/v1/settings":
		a.handleSettings(w, r)
	case r.URL.Path == "/api/v1/commands":
		a.handleCommand(w, r)
	case r.URL.Path == "/api/v1/audit":
		a.handleAudit(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v1/alarms/") && strings.HasSuffix(r.URL.Path, "/ack"):
		a.handleAlarmAcknowledgement(w, r)
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "接口不存在", nil)
	}
}

func (a *apiServer) handleState(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	state, err := a.controller.State(r.Context())
	if err != nil {
		writeControllerError(w, err)
		return
	}
	setStateETag(w, state)
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"state": state})
}

func (a *apiServer) handleSettings(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPut) {
		return
	}
	var request settingsRequest
	if !decodeRequiredJSON(w, r, &request) {
		return
	}
	fields := validateSettingsRequest(request)
	if len(fields) > 0 {
		writeAPIError(w, http.StatusUnprocessableEntity, "validation_error", "维护参数不符合要求", fields)
		return
	}
	operator, ok := mutationOperator(w, r, request.Operator)
	if !ok {
		return
	}
	expected, ok := resolveExpectedRevision(w, r, request.ExpectedRevision)
	if !ok {
		return
	}
	state, message, err := a.controller.UpdateSettings(r.Context(), Parameters{
		Target: *request.Target, ToolLimit: *request.ToolLimit, InspectInterval: *request.InspectInterval,
	}, mutationMeta(r, operator), expected)
	if err != nil {
		writeControllerError(w, err)
		return
	}
	setStateETag(w, state)
	writeJSON(w, http.StatusOK, map[string]interface{}{"state": state, "message": message})
}

func (a *apiServer) handleCommand(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}
	var request commandRequest
	if !decodeRequiredJSON(w, r, &request) {
		return
	}
	fields := validateCommandRequest(request)
	if len(fields) > 0 {
		writeAPIError(w, http.StatusUnprocessableEntity, "validation_error", "设备命令不符合要求", fields)
		return
	}
	operator, ok := mutationOperator(w, r, request.Operator)
	if !ok {
		return
	}
	expected, ok := resolveExpectedRevision(w, r, request.ExpectedRevision)
	if !ok {
		return
	}
	state, message, err := a.controller.ExecuteCommand(r.Context(), DeviceCommand{
		Name: request.Command, Mode: request.Mode, Paused: request.Paused,
	}, mutationMeta(r, operator), expected)
	if err != nil {
		writeControllerError(w, err)
		return
	}
	setStateETag(w, state)
	writeJSON(w, http.StatusOK, map[string]interface{}{"state": state, "message": message})
}

func (a *apiServer) handleAlarmAcknowledgement(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}
	const prefix = "/api/v1/alarms/"
	value := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/ack")
	if value == "" || strings.Contains(value, "/") {
		writeAPIError(w, http.StatusNotFound, "not_found", "报警不存在", nil)
		return
	}
	alarmID, err := strconv.ParseUint(value, 10, 64)
	if err != nil || alarmID == 0 {
		writeAPIError(w, http.StatusUnprocessableEntity, "validation_error", "报警编号必须是正整数", map[string]string{"alarmId": "必须是正整数"})
		return
	}
	var request acknowledgeRequest
	if !decodeOptionalJSON(w, r, &request) {
		return
	}
	operator, ok := mutationOperator(w, r, request.Operator)
	if !ok {
		return
	}
	expected, ok := resolveExpectedRevision(w, r, request.ExpectedRevision)
	if !ok {
		return
	}
	state, message, err := a.controller.AcknowledgeAlarm(r.Context(), alarmID, mutationMeta(r, operator), expected)
	if err != nil {
		writeControllerError(w, err)
		return
	}
	setStateETag(w, state)
	writeJSON(w, http.StatusOK, map[string]interface{}{"state": state, "message": message})
}

func (a *apiServer) handleAudit(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			writeAPIError(w, http.StatusUnprocessableEntity, "validation_error", "分页参数不符合要求", map[string]string{"limit": "必须是 1 到 200 的整数"})
			return
		}
		limit = parsed
	}
	var beforeID *uint64
	if value := r.URL.Query().Get("beforeId"); value != "" {
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil || parsed == 0 {
			writeAPIError(w, http.StatusUnprocessableEntity, "validation_error", "分页参数不符合要求", map[string]string{"beforeId": "必须是正整数"})
			return
		}
		beforeID = &parsed
	}
	page, err := a.controller.Audit(r.Context(), limit, beforeID)
	if err != nil {
		writeControllerError(w, err)
		return
	}
	response := struct {
		Items        []AuditEntry `json:"items"`
		NextBeforeID *uint64      `json:"nextBeforeId,omitempty"`
	}{Items: page.Items, NextBeforeID: page.NextBeforeID}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func validateSettingsRequest(request settingsRequest) map[string]string {
	fields := make(map[string]string)
	validateIntegerRange(fields, "target", request.Target, 1, 9999)
	validateIntegerRange(fields, "toolLimit", request.ToolLimit, 1, 99999)
	validateIntegerRange(fields, "inspectInterval", request.InspectInterval, 1, 9999)
	return fields
}

func validateIntegerRange(fields map[string]string, name string, value *int, minimum, maximum int) {
	if value == nil {
		fields[name] = "不能为空"
		return
	}
	if *value < minimum || *value > maximum {
		fields[name] = fmt.Sprintf("必须是 %d 到 %d 的整数", minimum, maximum)
	}
}

func validateCommandRequest(request commandRequest) map[string]string {
	fields := make(map[string]string)
	switch request.Command {
	case "start", "pause", "reset", "clear_bins", "inspect":
	case "set_mode":
		if request.Mode != "auto" && request.Mode != "manual" {
			fields["mode"] = "必须是 auto 或 manual"
		}
	case "set_single_paused", "set_frame_paused":
		if request.Paused == nil {
			fields["paused"] = "不能为空"
		}
	default:
		fields["command"] = "不支持的设备命令"
	}
	return fields
}

func mutationOperator(w http.ResponseWriter, r *http.Request, bodyValue string) (string, bool) {
	operator := strings.TrimSpace(r.Header.Get("X-Operator"))
	if operator == "" {
		operator = strings.TrimSpace(bodyValue)
	}
	if operator == "" {
		writeAPIError(w, http.StatusBadRequest, "operator_required", "修改数据或操作设备时必须提供操作员", map[string]string{"operator": "请通过 X-Operator 请求头或 operator 字段提供"})
		return "", false
	}
	if utf8.RuneCountInString(operator) > 64 {
		writeAPIError(w, http.StatusUnprocessableEntity, "validation_error", "操作员标识不符合要求", map[string]string{"operator": "不能超过 64 个字符"})
		return "", false
	}
	for _, char := range operator {
		if unicode.IsControl(char) {
			writeAPIError(w, http.StatusUnprocessableEntity, "validation_error", "操作员标识不符合要求", map[string]string{"operator": "不能包含控制字符"})
			return "", false
		}
	}
	return operator, true
}

func mutationMeta(r *http.Request, operator string) MutationMeta {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if len(requestID) > 128 {
		requestID = requestID[:128]
	}
	return MutationMeta{Operator: operator, RequestID: requestID}
}

func resolveExpectedRevision(w http.ResponseWriter, r *http.Request, bodyValue *uint64) (*uint64, bool) {
	header := strings.TrimSpace(r.Header.Get("If-Match"))
	if header == "" {
		return bodyValue, true
	}
	header = strings.Trim(header, "\"")
	header = strings.TrimPrefix(header, "rev-")
	value, err := strconv.ParseUint(header, 10, 64)
	if err != nil || value == 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_revision", "If-Match 必须使用响应中的 ETag", nil)
		return nil, false
	}
	if bodyValue != nil && *bodyValue != value {
		writeAPIError(w, http.StatusBadRequest, "invalid_revision", "If-Match 与 expectedRevision 不一致", nil)
		return nil, false
	}
	return &value, true
}

func decodeRequiredJSON(w http.ResponseWriter, r *http.Request, target interface{}) bool {
	if r.Body == nil || r.ContentLength == 0 {
		writeAPIError(w, http.StatusBadRequest, "malformed_json", "请求体必须是 JSON 对象", nil)
		return false
	}
	return decodeJSON(w, r, target)
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, target interface{}) bool {
	if r.Body == nil || r.ContentLength == 0 {
		return true
	}
	return decodeJSON(w, r, target)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target interface{}) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type 必须是 application/json", nil)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "请求体不能超过 64 KiB", nil)
		} else {
			writeAPIError(w, http.StatusBadRequest, "malformed_json", "请求 JSON 格式错误或包含未知字段", nil)
		}
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, "malformed_json", "请求体只能包含一个 JSON 对象", nil)
		return false
	}
	return true
}

func allowMethods(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, method := range methods {
		if r.Method == method {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持", nil)
	return false
}

func writeControllerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errRevisionConflict):
		writeAPIError(w, http.StatusConflict, "revision_conflict", "数据已被其他操作更新，请刷新后重试", nil)
	case errors.Is(err, errAlarmNotFound):
		writeAPIError(w, http.StatusNotFound, "alarm_not_found", "报警不存在", nil)
	case errors.Is(err, errUnknownCommand):
		writeAPIError(w, http.StatusUnprocessableEntity, "validation_error", "设备命令不符合要求", nil)
	default:
		writeAPIError(w, http.StatusServiceUnavailable, "backend_unavailable", "后台暂时不可用，请稍后重试", nil)
	}
}

func setStateETag(w http.ResponseWriter, state HMIState) {
	w.Header().Set("ETag", fmt.Sprintf("\"rev-%d\"", state.Revision))
}

func writeAPIError(w http.ResponseWriter, status int, code, message string, fields map[string]string) {
	writeJSON(w, status, errorEnvelope{Error: apiError{Code: code, Message: message, Fields: fields}})
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
