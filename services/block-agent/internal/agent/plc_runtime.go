package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"block.local/block-agent/internal/easy521"
	"block.local/block-agent/internal/plcworker"
	"block.local/block-agent/internal/pointstore"
	"block.local/block-agent/internal/runtimeconfig"
)

func (r *Runtime) handlePLCScan(client *wsClient, raw []byte) {
	var request struct {
		ProtocolVersion string `json:"protocolVersion"`
		Type            string `json:"type"`
		Timestamp       string `json:"timestamp"`
		RequestID       string `json:"requestId"`
		AddressRange    string `json:"addressRange"`
	}
	if err := decodeAllowed(raw, &request, "protocolVersion", "type", "timestamp", "requestId", "addressRange"); err != nil || request.Type != "plc.scan" || request.AddressRange == "" {
		message := "plc.scan requires addressRange"
		if err != nil {
			message = err.Error()
		}
		client.enqueue(errorEnvelope(r.now, request.RequestID, "INVALID_REQUEST", message), false)
		return
	}
	if err := validateRequestEnvelope(request.ProtocolVersion, request.Timestamp); err != nil {
		client.enqueue(errorEnvelope(r.now, request.RequestID, "INVALID_REQUEST", err.Error()), false)
		return
	}
	ctx, cancel, selected, started := r.beginScan()
	if !started {
		client.enqueue(errorEnvelope(r.now, request.RequestID, "BUSY", "a PLC scan is already in progress"), false)
		return
	}
	go func() {
		defer r.endScan(cancel)
		devices, err := scanRange(ctx, request.AddressRange, selected)
		if errors.Is(err, context.Canceled) {
			return
		}
		if err != nil {
			client.enqueue(errorEnvelope(r.now, request.RequestID, "INVALID_REQUEST", err.Error()), false)
			return
		}
		client.enqueue(plcScanResultEnvelope(r.now, request.RequestID, devices), false)
	}()
}

func (r *Runtime) handlePLCConnect(client *wsClient, raw []byte) {
	var request struct {
		ProtocolVersion string `json:"protocolVersion"`
		Type            string `json:"type"`
		Timestamp       string `json:"timestamp"`
		RequestID       string `json:"requestId"`
		DeviceID        string `json:"deviceId"`
	}
	if err := decodeAllowed(raw, &request, "protocolVersion", "type", "timestamp", "requestId", "deviceId"); err != nil || request.Type != "plc.connect" || request.DeviceID == "" {
		message := "plc.connect requires deviceId"
		if err != nil {
			message = err.Error()
		}
		client.enqueue(errorEnvelope(r.now, request.RequestID, "INVALID_REQUEST", message), false)
		return
	}
	if err := validateRequestEnvelope(request.ProtocolVersion, request.Timestamp); err != nil {
		client.enqueue(errorEnvelope(r.now, request.RequestID, "INVALID_REQUEST", err.Error()), false)
		return
	}
	endpoint, err := parsePLCDeviceID(request.DeviceID)
	if err != nil {
		client.enqueue(errorEnvelope(r.now, request.RequestID, "PLC_NOT_FOUND", err.Error()), false)
		return
	}
	r.connectPLC(client, request.RequestID, endpoint, true)
}

func (r *Runtime) connectPLC(client *wsClient, requestID string, endpoint plcEndpoint, persistOnSuccess bool) {
	deviceID := endpoint.DeviceID()
	r.mu.Lock()
	session := r.session
	if r.owner != client || session == nil {
		r.mu.Unlock()
		client.enqueue(errorEnvelope(r.now, requestID, "PLC_NOT_CONNECTED", "runtime is not configured"), false)
		return
	}
	oldWorker, oldCancel := detachWorkerLocked(session)
	r.mu.Unlock()
	stopWorker(oldWorker, oldCancel)
	r.store.ClearValues()

	var worker *plcworker.Worker
	publish := func(values map[string]pointstore.PointValue) error {
		return r.updateFromWorker(session, worker, values)
	}
	worker, err := r.newPLCWorker(session.config, endpoint, publish)
	if err != nil {
		client.enqueue(plcConnectResultEnvelope(r.now, requestID, deviceID, false, "", "INTERNAL_ERROR", err.Error()), false)
		return
	}
	worker.SetDisconnectHandler(func() { r.notifyPLCDisconnected(session, worker) })
	workerContext, workerCancel := context.WithCancel(context.Background())
	r.mu.Lock()
	if r.owner != client || r.session != session {
		r.mu.Unlock()
		workerCancel()
		go worker.Run(workerContext)
		return
	}
	session.worker = worker
	session.cancel = workerCancel
	session.deviceID = deviceID
	session.disconnecting = false
	session.broadcasts = false
	client.enqueue(plcConnectionEnvelope(r.now, deviceID, "connecting"), false)
	r.mu.Unlock()
	go worker.Run(workerContext)
	go r.finishPLCConnect(client, session, worker, requestID, deviceID, endpoint, persistOnSuccess)
}

func (r *Runtime) handlePLCDisconnect(client *wsClient, raw []byte) {
	var request struct {
		ProtocolVersion string `json:"protocolVersion"`
		Type            string `json:"type"`
		Timestamp       string `json:"timestamp"`
		RequestID       string `json:"requestId"`
	}
	if err := decodeAllowed(raw, &request, "protocolVersion", "type", "timestamp", "requestId"); err != nil || request.Type != "plc.disconnect" {
		message := "plc.disconnect request is invalid"
		if err != nil {
			message = err.Error()
		}
		client.enqueue(errorEnvelope(r.now, request.RequestID, "INVALID_REQUEST", message), false)
		return
	}
	if err := validateRequestEnvelope(request.ProtocolVersion, request.Timestamp); err != nil {
		client.enqueue(errorEnvelope(r.now, request.RequestID, "INVALID_REQUEST", err.Error()), false)
		return
	}
	r.mu.Lock()
	session := r.session
	if r.owner != client || session == nil {
		r.mu.Unlock()
		if err := clearPLCEndpoint(r.plcEndpointPath); err != nil {
			client.enqueue(errorEnvelope(r.now, request.RequestID, "PLC_ENDPOINT_CLEAR_FAILED", err.Error()), false)
			return
		}
		client.enqueue(plcDisconnectResultEnvelope(r.now, request.RequestID, true), false)
		return
	}
	worker, cancel := session.worker, session.cancel
	session.disconnecting = true
	r.mu.Unlock()
	stopWorker(worker, cancel)
	if worker != nil {
		_ = worker.ConfirmDisconnected()
	}
	r.mu.Lock()
	if r.session == session && session.worker == worker {
		detachWorkerLocked(session)
	}
	r.mu.Unlock()
	if err := clearPLCEndpoint(r.plcEndpointPath); err != nil {
		client.enqueue(errorEnvelope(r.now, request.RequestID, "PLC_ENDPOINT_CLEAR_FAILED", err.Error()), false)
		return
	}
	client.enqueue(plcDisconnectResultEnvelope(r.now, request.RequestID, true), false)
}

func (r *Runtime) finishPLCConnect(client *wsClient, session *runtimeSession, worker *plcworker.Worker, requestID, deviceID string, endpoint plcEndpoint, persistOnSuccess bool) {
	readyErr, ok := <-worker.Ready()
	if !ok {
		readyErr = context.Canceled
	}
	r.mu.Lock()
	if r.owner != client || r.session != session || session.worker != worker || session.disconnecting {
		r.mu.Unlock()
		return
	}
	persistErr := error(nil)
	if readyErr == nil && persistOnSuccess {
		persistErr = savePLCEndpoint(r.plcEndpointPath, endpoint)
	}
	state := "connected"
	if readyErr != nil {
		state = "error"
		if worker.Disconnected() {
			state = "disconnected"
		}
	}
	if state != "disconnected" {
		client.enqueue(plcConnectionEnvelope(r.now, deviceID, state), false)
	}
	if readyErr != nil {
		client.enqueue(plcConnectResultEnvelope(r.now, requestID, deviceID, false, state, "PLC_READ_FAILED", "initial PLC read failed"), false)
	} else if persistErr != nil {
		client.enqueue(plcConnectResultEnvelope(r.now, requestID, deviceID, false, state, "PLC_ENDPOINT_SAVE_FAILED", persistErr.Error()), false)
	} else {
		client.enqueue(plcConnectResultEnvelope(r.now, requestID, deviceID, true, state, "", ""), false)
	}
	client.enqueue(snapshotEnvelope(r.now, r.store.Snapshot()), false)
	session.broadcasts = true
	r.mu.Unlock()
}

func (r *Runtime) notifyPLCDisconnected(session *runtimeSession, worker *plcworker.Worker) {
	r.mu.Lock()
	if r.session != session || session.worker != worker || r.owner == nil {
		r.mu.Unlock()
		return
	}
	client, deviceID := r.owner, session.deviceID
	r.mu.Unlock()
	if deviceID != "" {
		client.enqueue(plcConnectionEnvelope(r.now, deviceID, "disconnected"), false)
	}
}

func (r *Runtime) newPLCWorker(config runtimeconfig.Config, endpoint plcEndpoint, publish func(map[string]pointstore.PointValue) error) (*plcworker.Worker, error) {
	if r.factory != nil {
		return r.factory(config, publish)
	}
	adapter, err := easy521.New(easy521.Config{
		Endpoint:       endpoint.String(),
		UnitID:         endpoint.unitID,
		ConnectTimeout: 2 * time.Second,
		RequestTimeout: 2 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return plcworker.New(config, adapter, publish, r.now)
}

func (r *Runtime) beginScan() (context.Context, context.CancelFunc, string, bool) {
	r.scanMu.Lock()
	defer r.scanMu.Unlock()
	if r.scanCancel != nil {
		return nil, nil, "", false
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.scanCancel = cancel
	r.mu.Lock()
	selected := ""
	if r.session != nil {
		selected = r.session.deviceID
	}
	r.mu.Unlock()
	return ctx, cancel, selected, true
}

func (r *Runtime) endScan(cancel context.CancelFunc) {
	r.scanMu.Lock()
	if r.scanCancel != nil {
		r.scanCancel = nil
	}
	r.scanMu.Unlock()
	cancel()
}

func (r *Runtime) cancelScan() {
	r.scanMu.Lock()
	cancel := r.scanCancel
	r.scanCancel = nil
	r.scanMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func detachWorkerLocked(session *runtimeSession) (*plcworker.Worker, context.CancelFunc) {
	if session == nil {
		return nil, nil
	}
	worker, cancel := session.worker, session.cancel
	session.worker = nil
	session.cancel = nil
	session.deviceID = ""
	session.disconnecting = false
	session.broadcasts = false
	return worker, cancel
}

func stopWorker(worker *plcworker.Worker, cancel context.CancelFunc) {
	if cancel != nil {
		cancel()
	}
	if worker != nil {
		<-worker.Done()
	}
}

func validateRequestEnvelope(protocol, timestamp string) error {
	if protocol != protocolVersion {
		return fmt.Errorf("protocolVersion must be %s", protocolVersion)
	}
	if _, err := time.Parse(time.RFC3339, timestamp); err != nil {
		return errors.New("timestamp must be UTC RFC3339")
	}
	return nil
}
