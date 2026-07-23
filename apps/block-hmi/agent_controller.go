package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type agentController struct {
	socket             string
	client             *http.Client
	transport          *http.Transport
	expectedSource     string
	expectedSimulation bool
}

type agentErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func newAgentController(socket string, timeout time.Duration) (*agentController, error) {
	if socket == "" || !filepath.IsAbs(socket) {
		return nil, errors.New("BLOCK_HMI_AGENT_SOCKET must be an absolute Unix socket path")
	}
	if timeout <= 0 {
		return nil, errors.New("Agent timeout must be positive")
	}
	transport := &http.Transport{
		Proxy:              nil,
		DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "unix", socket)
		},
	}
	return &agentController{
		socket:    socket,
		transport: transport,
		client:    &http.Client{Transport: transport, Timeout: timeout},
	}, nil
}

func (c *agentController) Close() {
	c.transport.CloseIdleConnections()
}

func (c *agentController) SourceInfo(ctx context.Context) (SourceInfo, error) {
	var response struct {
		SchemaVersion string `json:"schemaVersion"`
		Source        struct {
			Kind       string `json:"kind"`
			Simulation bool   `json:"simulation"`
		} `json:"source"`
	}
	headers, err := c.doWithHeaders(ctx, http.MethodGet, "/internal/v1/source", nil, MutationMeta{}, &response)
	if err != nil {
		return SourceInfo{}, err
	}
	if response.SchemaVersion != "block-local-private/v1" ||
		(response.Source.Kind != "simulator" && response.Source.Kind != "disabled") ||
		response.Source.Simulation != (response.Source.Kind == "simulator") {
		return SourceInfo{}, errors.New("block-agent returned invalid private source metadata")
	}
	sourceHeaders := headers.Values("X-Block-Source-Kind")
	simulationHeaders := headers.Values("X-Block-Simulation")
	if len(sourceHeaders) != 1 || len(simulationHeaders) != 1 ||
		strings.TrimSpace(sourceHeaders[0]) == "" || strings.TrimSpace(simulationHeaders[0]) == "" ||
		sourceHeaders[0] != response.Source.Kind ||
		simulationHeaders[0] != strconv.FormatBool(response.Source.Simulation) {
		return SourceInfo{}, errors.New("block-agent source metadata body and headers are incomplete or inconsistent")
	}
	c.expectedSource = response.Source.Kind
	c.expectedSimulation = response.Source.Simulation
	return SourceInfo{Kind: response.Source.Kind, Simulation: response.Source.Simulation}, nil
}

func (c *agentController) State(ctx context.Context) (HMIState, error) {
	var response struct {
		State HMIState `json:"state"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/state", nil, MutationMeta{}, &response); err != nil {
		return HMIState{}, err
	}
	return response.State, nil
}

func (c *agentController) UpdateSettings(ctx context.Context, parameters Parameters, meta MutationMeta, expected *uint64) (HMIState, string, error) {
	body := map[string]any{
		"target":          parameters.Target,
		"toolLimit":       parameters.ToolLimit,
		"inspectInterval": parameters.InspectInterval,
	}
	if expected != nil {
		body["expectedRevision"] = *expected
	}
	return c.mutate(ctx, http.MethodPut, "/api/v1/settings", body, meta)
}

func (c *agentController) ExecuteCommand(ctx context.Context, command DeviceCommand, meta MutationMeta, expected *uint64) (HMIState, string, error) {
	body := map[string]any{"command": command.Name}
	if command.Mode != "" {
		body["mode"] = command.Mode
	}
	if command.Paused != nil {
		body["paused"] = *command.Paused
	}
	if expected != nil {
		body["expectedRevision"] = *expected
	}
	return c.mutate(ctx, http.MethodPost, "/api/v1/commands", body, meta)
}

func (c *agentController) AcknowledgeAlarm(ctx context.Context, alarmID uint64, meta MutationMeta, expected *uint64) (HMIState, string, error) {
	body := map[string]any{}
	if expected != nil {
		body["expectedRevision"] = *expected
	}
	return c.mutate(ctx, http.MethodPost, "/api/v1/alarms/"+strconv.FormatUint(alarmID, 10)+"/ack", body, meta)
}

func (c *agentController) Audit(ctx context.Context, limit int, beforeID *uint64) (AuditPage, error) {
	values := url.Values{"limit": []string{strconv.Itoa(limit)}}
	if beforeID != nil {
		values.Set("beforeId", strconv.FormatUint(*beforeID, 10))
	}
	var response struct {
		Items        []AuditEntry `json:"items"`
		NextBeforeID *uint64      `json:"nextBeforeId,omitempty"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/audit?"+values.Encode(), nil, MutationMeta{}, &response); err != nil {
		return AuditPage{}, err
	}
	return AuditPage{Items: response.Items, NextBeforeID: response.NextBeforeID}, nil
}

func (c *agentController) mutate(ctx context.Context, method, path string, body map[string]any, meta MutationMeta) (HMIState, string, error) {
	var response struct {
		State   HMIState `json:"state"`
		Message string   `json:"message"`
	}
	if err := c.do(ctx, method, path, body, meta, &response); err != nil {
		return HMIState{}, "", err
	}
	return response.State, response.Message, nil
}

func (c *agentController) do(ctx context.Context, method, path string, body any, meta MutationMeta, target any) error {
	_, err := c.doWithHeaders(ctx, method, path, body, meta, target)
	return err
}

func (c *agentController) doWithHeaders(ctx context.Context, method, path string, body any, meta MutationMeta, target any) (http.Header, error) {
	var reader io.Reader
	if body != nil {
		contents, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(contents)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if meta.Operator != "" {
		request.Header.Set("X-Operator", meta.Operator)
	}
	if meta.RequestID != "" {
		request.Header.Set("X-Request-ID", meta.RequestID)
	}
	if meta.CommandID != "" {
		request.Header.Set("Idempotency-Key", meta.CommandID)
	}
	response, err := c.client.Do(request)
	if err != nil {
		if outcomeErr := ambiguousMutationError(ctx, method, err); outcomeErr != nil {
			return nil, outcomeErr
		}
		return nil, fmt.Errorf("connect block-agent via %s: %w", c.socket, err)
	}
	defer response.Body.Close()
	if c.expectedSource != "" {
		kind := response.Header.Get("X-Block-Source-Kind")
		simulation := response.Header.Get("X-Block-Simulation")
		if kind != c.expectedSource || simulation != strconv.FormatBool(c.expectedSimulation) {
			return response.Header, errSourceMismatch
		}
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope agentErrorEnvelope
		if err := decoder.Decode(&envelope); err != nil {
			if outcomeErr := ambiguousMutationError(ctx, method, err); outcomeErr != nil {
				return response.Header, outcomeErr
			}
			return response.Header, fmt.Errorf("block-agent returned HTTP %d", response.StatusCode)
		}
		switch envelope.Error.Code {
		case "revision_conflict":
			return response.Header, errRevisionConflict
		case "alarm_not_found":
			return response.Header, errAlarmNotFound
		case "validation_error", "command_rejected":
			return response.Header, errUnknownCommand
		case "idempotency_conflict":
			return response.Header, errIdempotencyConflict
		case "safety_interlock":
			return response.Header, errSafetyInterlock
		case "device_unavailable":
			return response.Header, errDeviceUnavailable
		case "bad_quality":
			return response.Header, errBadQuality
		case "data_stale":
			return response.Header, errDataStale
		case "command_failed", "readback_failed":
			return response.Header, errCommandFailed
		case "command_outcome_unknown":
			return response.Header, errOutcomeUnknown
		default:
			return response.Header, fmt.Errorf("block-agent unavailable: %s", envelope.Error.Message)
		}
	}
	if target == nil || response.StatusCode == http.StatusNoContent {
		return response.Header, nil
	}
	if err := decoder.Decode(target); err != nil {
		if outcomeErr := ambiguousMutationError(ctx, method, err); outcomeErr != nil {
			return response.Header, outcomeErr
		}
		return response.Header, fmt.Errorf("decode block-agent response: %w", err)
	}
	return response.Header, nil
}

func ambiguousMutationError(ctx context.Context, method string, err error) error {
	if method == http.MethodGet || method == http.MethodHead {
		return nil
	}
	var networkError net.Error
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		ctx.Err() != nil ||
		(errors.As(err, &networkError) && networkError.Timeout()) {
		return fmt.Errorf("%w: Agent request canceled or timed out after dispatch: %v", errOutcomeUnknown, err)
	}
	return nil
}
