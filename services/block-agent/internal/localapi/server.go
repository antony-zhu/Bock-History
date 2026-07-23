package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"block.local/block-agent/internal/command"
	"block.local/block-agent/internal/plccontract"
	"block.local/block-agent/internal/socketperm"
	"block.local/block-agent/internal/storage"
)

const maxBody = 64 << 10

type Server struct {
	socket      string
	socketGroup string
	sourceKind  string
	store       *storage.Store
	queue       *command.Queue
	staleAfter  time.Duration
	now         func() time.Time
	server      *http.Server
}

type availabilityError struct {
	code string
}

func (e availabilityError) Error() string { return e.code }

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
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func New(socket, socketGroup, sourceKind string, store *storage.Store, queue *command.Queue, staleAfter time.Duration, now func() time.Time) *Server {
	if now == nil {
		now = time.Now
	}
	return &Server{
		socket: socket, socketGroup: socketGroup, sourceKind: sourceKind,
		store: store, queue: queue, staleAfter: staleAfter, now: now,
	}
}

func (s *Server) Serve(ctx context.Context) error {
	listener, err := socketperm.Listen(s.socket, s.socketGroup)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(s.socket)
	}()
	_ = os.Chmod(s.socket, 0o660)
	s.server = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second}
	errorsChannel := make(chan error, 1)
	go func() { errorsChannel <- s.server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdownContext)
		return nil
	case err := <-errorsChannel:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/internal/v1/source", s.source)
	mux.HandleFunc("/api/v1/state", s.state)
	mux.HandleFunc("/api/v1/settings", s.settings)
	mux.HandleFunc("/api/v1/commands", s.commands)
	mux.HandleFunc("/api/v1/audit", s.audit)
	mux.HandleFunc("/api/v1/alarms/", s.alarms)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Block-Source-Kind", s.sourceKind)
		w.Header().Set("X-Block-Simulation", strconv.FormatBool(s.sourceKind == "simulator"))
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) source(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持", nil)
		return
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schemaVersion": "block-local-private/v1",
		"source":        map[string]any{"kind": s.sourceKind, "simulation": s.sourceKind == "simulator"},
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持", nil)
		return
	}
	record, err := s.freshSnapshot(r.Context())
	if err != nil {
		s.writeAvailabilityError(w, err, false)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf("\"rev-%d\"", record.State.Revision))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": record.State})
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持", nil)
		return
	}
	var request settingsRequest
	if err := decodeRequired(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "malformed_json", "请求 JSON 格式错误或包含未知字段", nil)
		return
	}
	fields := map[string]string{}
	validateRange(fields, "target", request.Target, 1, 9999)
	validateRange(fields, "toolLimit", request.ToolLimit, 1, 99999)
	validateRange(fields, "inspectInterval", request.InspectInterval, 1, 9999)
	if len(fields) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "维护参数不符合要求", fields)
		return
	}
	s.submit(w, r, plccontract.Command{
		Name: "update_settings", ExpectedControlRevision: request.ExpectedRevision,
		Settings: &plccontract.Settings{Target: *request.Target, ToolLimit: *request.ToolLimit, InspectInterval: *request.InspectInterval},
	}, request.Operator)
}

func (s *Server) commands(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持", nil)
		return
	}
	var request commandRequest
	if err := decodeRequired(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "malformed_json", "请求 JSON 格式错误或包含未知字段", nil)
		return
	}
	if !validCommand(request) {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "设备命令不符合要求", nil)
		return
	}
	s.submit(w, r, plccontract.Command{
		Name: request.Command, ExpectedControlRevision: request.ExpectedRevision,
		Mode: request.Mode, Paused: request.Paused,
	}, request.Operator)
}

func (s *Server) alarms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/ack") {
		writeError(w, http.StatusNotFound, "not_found", "接口不存在", nil)
		return
	}
	value := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/alarms/"), "/ack")
	alarmID, err := strconv.ParseUint(value, 10, 64)
	if err != nil || alarmID == 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "报警编号必须是正整数", nil)
		return
	}
	var request acknowledgeRequest
	if r.ContentLength > 0 {
		if err := decodeRequired(r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "malformed_json", "请求 JSON 格式错误或包含未知字段", nil)
			return
		}
	}
	s.submit(w, r, plccontract.Command{
		Name: "acknowledge_alarm", AlarmID: strconv.FormatUint(alarmID, 10),
		ExpectedControlRevision: request.ExpectedRevision,
	}, request.Operator)
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持", nil)
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusUnprocessableEntity, "validation_error", "分页参数不符合要求", nil)
			return
		}
		limit = parsed
	}
	var before *uint64
	if raw := r.URL.Query().Get("beforeId"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 {
			writeError(w, http.StatusUnprocessableEntity, "validation_error", "分页参数不符合要求", nil)
			return
		}
		before = &parsed
	}
	page, err := s.store.Audit(r.Context(), limit, before)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "backend_unavailable", "后台暂时不可用", nil)
		return
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": page.Items, "nextBeforeId": page.NextBeforeID})
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request, deviceCommand plccontract.Command, bodyOperator string) {
	commandID := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if commandID == "" || len(commandID) > 128 {
		writeError(w, http.StatusBadRequest, "malformed_json", "Idempotency-Key 必须包含 1 到 128 个字符", nil)
		return
	}
	operator := strings.TrimSpace(r.Header.Get("X-Operator"))
	if operator == "" {
		operator = strings.TrimSpace(bodyOperator)
	}
	if operator == "" {
		writeError(w, http.StatusBadRequest, "operator_required", "写操作必须提供操作员", nil)
		return
	}
	deviceCommand.CommandID = commandID
	outcome, err := s.queue.Submit(r.Context(), deviceCommand, command.Meta{
		Operator: operator, RequestID: strings.TrimSpace(r.Header.Get("X-Request-ID")),
	})
	if err != nil {
		if errors.Is(err, storage.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, plccontract.ResultCodeIdempotencyConflict, "commandId 已绑定到不同命令内容", nil)
			return
		}
		writeError(w, http.StatusServiceUnavailable, "backend_unavailable", "后台暂时不可用", nil)
		return
	}
	if outcome.Result.Status != plccontract.CommandExecuted || outcome.State == nil {
		s.writeCommandError(w, outcome.Result, outcome.Message)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf("\"rev-%d\"", outcome.State.Revision))
	writeJSON(w, http.StatusOK, map[string]any{"state": *outcome.State, "message": outcome.Message})
}

func (s *Server) freshSnapshot(ctx context.Context) (storage.SnapshotRecord, error) {
	available, healthCode := s.store.SourceAvailability()
	record, err := s.store.LoadSnapshot(ctx)
	if err != nil {
		return storage.SnapshotRecord{}, availabilityError{code: storage.AvailabilityBackendUnavailable}
	}
	if !available {
		return storage.SnapshotRecord{}, availabilityError{code: healthCode}
	}
	if fresh, code := storage.Freshness(record, s.now().UTC(), s.staleAfter); !fresh {
		return storage.SnapshotRecord{}, availabilityError{code: code}
	}
	return record, nil
}

func (s *Server) writeAvailabilityError(w http.ResponseWriter, err error, write bool) {
	code := storage.AvailabilityBackendUnavailable
	var unavailable availabilityError
	if errors.As(err, &unavailable) && unavailable.code != "" {
		code = unavailable.code
	}
	message := map[string]string{
		storage.AvailabilityDeviceUnavailable:  "设备连接中断",
		storage.AvailabilityBadQuality:         "设备数据质量不可用",
		storage.AvailabilityDataStale:          "设备数据已过期",
		storage.AvailabilityBackendUnavailable: "后台暂时不可用",
	}[code]
	if write {
		message += "，写操作已禁用"
	}
	writeError(w, http.StatusServiceUnavailable, code, message, nil)
}

func (s *Server) writeCommandError(w http.ResponseWriter, result plccontract.CommandResult, fallback string) {
	message := result.Reason
	if message == "" {
		message = fallback
	}
	switch result.Code {
	case plccontract.ResultCodeRevisionConflict:
		writeError(w, http.StatusConflict, result.Code, "数据已被其他操作更新，请刷新后重试", nil)
	case plccontract.ResultCodeIdempotencyConflict:
		writeError(w, http.StatusConflict, result.Code, "commandId 已绑定到不同命令内容", nil)
	case plccontract.ResultCodeSafetyInterlock:
		writeError(w, http.StatusConflict, result.Code, "安全联锁拒绝启动，请检查急停和安全门", nil)
	case plccontract.ResultCodeAlarmNotFound:
		writeError(w, http.StatusNotFound, result.Code, "报警不存在或已清除", nil)
	case storage.AvailabilityDeviceUnavailable:
		writeError(w, http.StatusServiceUnavailable, result.Code, "设备连接不可用", nil)
	case storage.AvailabilityBadQuality:
		writeError(w, http.StatusServiceUnavailable, result.Code, "设备数据质量不可用，写操作已禁用", nil)
	case storage.AvailabilityDataStale:
		writeError(w, http.StatusServiceUnavailable, result.Code, "设备数据已过期，写操作已禁用", nil)
	case storage.AvailabilityBackendUnavailable:
		writeError(w, http.StatusServiceUnavailable, result.Code, "后台暂时不可用，写操作已禁用", nil)
	case plccontract.ResultCodeCommandRejected:
		writeError(w, http.StatusUnprocessableEntity, result.Code, message, nil)
	case plccontract.ResultCodeCommandFailed, plccontract.ResultCodeReadbackFailed:
		writeError(w, http.StatusBadGateway, result.Code, message, nil)
	case plccontract.ResultCodeOutcomeUnknown:
		writeError(w, http.StatusGatewayTimeout, result.Code, "命令结果未知，禁止自动重试", nil)
	default:
		writeError(w, http.StatusServiceUnavailable, "backend_unavailable", message, nil)
	}
}

func validCommand(request commandRequest) bool {
	switch request.Command {
	case "start", "pause", "reset", "clear_bins", "inspect":
		return true
	case "set_mode":
		return request.Mode == "auto" || request.Mode == "manual"
	case "set_single_paused", "set_frame_paused":
		return request.Paused != nil
	default:
		return false
	}
}

func validateRange(fields map[string]string, name string, value *int, min, max int) {
	if value == nil || *value < min || *value > max {
		fields[name] = fmt.Sprintf("必须是 %d 到 %d 的整数", min, max)
	}
}

func decodeRequired(r *http.Request, target any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return errors.New("JSON body required")
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("only one JSON value is allowed")
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, code, message string, fields map[string]string) {
	writeJSON(w, status, errorEnvelope{Error: errorDetail{Code: code, Message: message, Fields: fields}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
