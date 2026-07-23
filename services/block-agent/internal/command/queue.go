package command

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"block.local/block-agent/internal/adapter"
	"block.local/block-agent/internal/plccontract"
	"block.local/block-agent/internal/state"
	"block.local/block-agent/internal/storage"
)

type Meta struct {
	Operator  string
	RequestID string
}

type Outcome struct {
	Result  plccontract.CommandResult
	State   *state.Model
	Message string
}

type request struct {
	command  plccontract.Command
	meta     Meta
	response chan response
}

type response struct {
	outcome Outcome
	err     error
}

type Queue struct {
	device            *adapter.Coordinator
	store             *storage.Store
	timeout           time.Duration
	staleAfter        time.Duration
	now               func() time.Time
	checkAvailability func(context.Context) (string, string)
	requests          chan request
	stop              chan struct{}
	done              chan struct{}
	fatal             chan error
	stopOnce          sync.Once
}

func New(device *adapter.Coordinator, store *storage.Store, timeout, staleAfter time.Duration, now func() time.Time) *Queue {
	if now == nil {
		now = time.Now
	}
	queue := &Queue{
		device: device, store: store, timeout: timeout, staleAfter: staleAfter, now: now,
		requests: make(chan request), stop: make(chan struct{}), done: make(chan struct{}), fatal: make(chan error, 1),
	}
	queue.checkAvailability = queue.commandAvailability
	go queue.run()
	return queue
}

func (q *Queue) Close() {
	q.stopOnce.Do(func() { close(q.stop) })
	<-q.done
}

func (q *Queue) Errors() <-chan error {
	return q.fatal
}

// Submit uses the caller context only for queue admission and response
// delivery. Once admitted, device execution and terminal persistence continue
// under a separate bounded internal context even if the HTTP client leaves.
func (q *Queue) Submit(ctx context.Context, command plccontract.Command, meta Meta) (Outcome, error) {
	responseChannel := make(chan response, 1)
	item := request{command: plccontract.NormalizeCommand(command), meta: meta, response: responseChannel}
	select {
	case <-ctx.Done():
		return Outcome{}, ctx.Err()
	case <-q.stop:
		return Outcome{}, errors.New("command queue is stopped")
	case <-q.done:
		return Outcome{}, errors.New("command queue terminated")
	case q.requests <- item:
	}
	select {
	case <-ctx.Done():
		return Outcome{}, ctx.Err()
	case <-q.done:
		select {
		case value := <-responseChannel:
			return value.outcome, value.err
		default:
			return Outcome{}, errors.New("command queue terminated")
		}
	case value := <-responseChannel:
		return value.outcome, value.err
	}
}

func (q *Queue) run() {
	defer close(q.done)
	for {
		select {
		case <-q.stop:
			return
		case item := <-q.requests:
			outcome, err, fatal := q.process(item.command, item.meta)
			item.response <- response{outcome: outcome, err: err}
			if fatal {
				select {
				case q.fatal <- err:
				default:
				}
				return
			}
		}
	}
}

func (q *Queue) process(command plccontract.Command, meta Meta) (Outcome, error, bool) {
	operationContext, operationCancel := context.WithTimeout(context.Background(), q.operationBudget())
	defer operationCancel()

	record, err := q.store.BeginCommand(operationContext, command, storage.CommandMeta{
		Operator: meta.Operator, RequestID: meta.RequestID,
	})
	if err != nil {
		if errors.Is(err, storage.ErrIdempotencyConflict) {
			result := plccontract.CommandResult{
				CommandID: command.CommandID, Name: command.Name, Status: plccontract.CommandRejected,
				Code: plccontract.ResultCodeIdempotencyConflict, Reason: err.Error(),
			}
			return Outcome{Result: result, Message: resultMessage(result)}, err, false
		}
		return Outcome{}, fmt.Errorf("begin command persistence: %w", err), true
	}
	if record.Exists {
		outcome := Outcome{Result: record.Result, Message: resultMessage(record.Result)}
		if snapshot, err := q.store.LoadSnapshot(operationContext); err == nil {
			stateCopy := snapshot.State
			outcome.State = &stateCopy
		}
		return outcome, nil, false
	}

	var (
		outcome Outcome
		runErr  error
		fatal   bool
	)
	q.device.Do(func(device adapter.Adapter) {
		if code, reason := q.checkAvailability(operationContext); code != "" {
			result := plccontract.CommandResult{
				CommandID: command.CommandID, Name: command.Name,
				Status: plccontract.CommandRejected, Code: code, Reason: reason,
			}
			outcome, runErr, fatal = q.persistOutcome(operationContext, command, meta, result, nil)
			return
		}

		commandContext, cancel := context.WithTimeout(operationContext, q.timeout)
		result, executeErr := device.Execute(commandContext, command)
		cancel()
		result = validatedResult(command, result, executeErr)
		ensureResultCode(&result)

		var readback *plccontract.Snapshot
		if result.Status == plccontract.CommandExecuted {
			readContext, readCancel := context.WithTimeout(operationContext, q.timeout)
			value, readErr := device.Read(readContext)
			readCancel()
			var confirmationErr error
			if readErr == nil {
				confirmationErr = confirm(command, result, value)
			}
			switch {
			case readErr != nil:
				result.Status = plccontract.CommandUnknown
				result.Code = plccontract.ResultCodeOutcomeUnknown
				result.Reason = "command response was received but readback failed: " + readErr.Error()
			case confirmationErr != nil:
				result.Status = plccontract.CommandFailed
				result.Code = plccontract.ResultCodeReadbackFailed
				result.Reason = "readback confirmation failed: " + confirmationErr.Error()
			default:
				readback = &value
			}
		}
		outcome, runErr, fatal = q.persistOutcome(operationContext, command, meta, result, readback)
	})
	return outcome, runErr, fatal
}

func (q *Queue) commandAvailability(ctx context.Context) (string, string) {
	record, err := q.store.LoadSnapshot(ctx)
	if err != nil {
		return storage.AvailabilityBackendUnavailable, "persisted device state is unavailable"
	}
	available, code := q.store.SourceAvailability()
	if !available {
		if code == "" {
			code = storage.AvailabilityBackendUnavailable
		}
		return code, availabilityReason(code)
	}
	if fresh, code := storage.Freshness(record, q.now().UTC(), q.staleAfter); !fresh {
		q.store.SetSourceUnavailable(code)
		return code, availabilityReason(code)
	}
	return "", ""
}

func availabilityReason(code string) string {
	switch code {
	case storage.AvailabilityDeviceUnavailable:
		return "device transport is unavailable"
	case storage.AvailabilityBadQuality:
		return "device data quality is not GOOD"
	case storage.AvailabilityDataStale:
		return "device data is stale"
	default:
		return "Block persistence backend is unavailable"
	}
}

func validatedResult(command plccontract.Command, result plccontract.CommandResult, executeErr error) plccontract.CommandResult {
	if executeErr != nil {
		return unknownResult(command, executeErr.Error())
	}
	if result.CommandID != command.CommandID {
		return unknownResult(command, fmt.Sprintf("invalid command response: commandId %q does not match request", result.CommandID))
	}
	if result.Name != command.Name {
		return unknownResult(command, fmt.Sprintf("invalid command response: name %q does not match request", result.Name))
	}
	switch result.Status {
	case plccontract.CommandExecuted, plccontract.CommandRejected, plccontract.CommandFailed, plccontract.CommandUnknown:
		return result
	default:
		return unknownResult(command, fmt.Sprintf("invalid command response: status %q is not terminal", result.Status))
	}
}

func unknownResult(command plccontract.Command, reason string) plccontract.CommandResult {
	return plccontract.CommandResult{
		CommandID: command.CommandID, Name: command.Name, Status: plccontract.CommandUnknown,
		Code: plccontract.ResultCodeOutcomeUnknown, Reason: reason,
	}
}

func (q *Queue) persistOutcome(
	ctx context.Context,
	command plccontract.Command,
	meta Meta,
	result plccontract.CommandResult,
	readback *plccontract.Snapshot,
) (Outcome, error, bool) {
	ensureResultCode(&result)
	message := resultMessage(result)
	audit := storage.AuditInput{
		Operator: meta.Operator, Action: "command." + command.Name, Message: message,
		RequestID: meta.RequestID,
		Details: map[string]any{
			"commandId": command.CommandID, "status": result.Status, "code": result.Code,
			"simulationOnly": result.SimulationOnly, "fingerprint": plccontract.CommandFingerprint(command),
		},
	}
	operation := storage.OperationInput{}
	if result.Status == plccontract.CommandExecuted {
		operation = storage.OperationInput{Level: "info", Code: "0001", Text: message, At: q.now().UTC()}
	}

	var persisted *storage.SnapshotRecord
	var err error
	for {
		persisted, err = q.store.CompleteCommand(ctx, result, readback, q.now().UTC(), q.staleAfter, operation, audit)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return Outcome{}, fmt.Errorf("complete command persistence: %w", err), true
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Outcome{}, fmt.Errorf("complete command persistence: %w", err), true
		case <-timer.C:
		}
	}
	var responseState *state.Model
	if persisted != nil {
		stateCopy := persisted.State
		responseState = &stateCopy
	}
	return Outcome{Result: result, State: responseState, Message: message}, nil, false
}

func (q *Queue) operationBudget() time.Duration {
	budget := 3*q.timeout + time.Second
	if budget < 2*time.Second {
		return 2 * time.Second
	}
	return budget
}

func ensureResultCode(result *plccontract.CommandResult) {
	if result.Code != "" {
		return
	}
	switch result.Status {
	case plccontract.CommandRejected:
		result.Code = plccontract.ResultCodeCommandRejected
	case plccontract.CommandFailed:
		result.Code = plccontract.ResultCodeCommandFailed
	case plccontract.CommandUnknown, plccontract.CommandPending:
		result.Code = plccontract.ResultCodeOutcomeUnknown
	}
}

func confirm(command plccontract.Command, result plccontract.CommandResult, readback plccontract.Snapshot) error {
	points := readback.Points
	if result.ControlRevision == 0 || points.ControlRevision != result.ControlRevision {
		return fmt.Errorf("control revision is %d, expected %d", points.ControlRevision, result.ControlRevision)
	}
	switch command.Name {
	case "start":
		if !points.Running {
			return errors.New("running did not become true")
		}
	case "pause":
		if points.Running {
			return errors.New("running did not become false")
		}
	case "reset":
		if points.Running || points.Output != 0 || points.ToolUsed != 0 {
			return errors.New("reset state does not match")
		}
	case "set_mode":
		if points.Mode != command.Mode {
			return errors.New("mode does not match")
		}
	case "set_single_paused":
		if command.Paused == nil || points.SinglePaused != *command.Paused {
			return errors.New("single pause state does not match")
		}
	case "set_frame_paused":
		if command.Paused == nil || points.FramePaused != *command.Paused {
			return errors.New("frame pause state does not match")
		}
	case "clear_bins":
		for _, bin := range points.Bins {
			if bin.Quantity != 0 {
				return errors.New("bins were not cleared")
			}
		}
	case "update_settings":
		if command.Settings == nil || points.Target != command.Settings.Target || points.ToolLimit != command.Settings.ToolLimit || points.InspectInterval != command.Settings.InspectInterval {
			return errors.New("settings do not match")
		}
	case "acknowledge_alarm":
		for _, item := range points.Alarms {
			if item.AlarmID == command.AlarmID && item.Active && item.Acknowledged {
				return nil
			}
		}
		return errors.New("alarm acknowledgement was not observed")
	case "inspect":
		// The control revision and simulator invariant are authoritative.
	default:
		return errors.New("unsupported command confirmation")
	}
	return nil
}

func resultMessage(result plccontract.CommandResult) string {
	if result.Status != plccontract.CommandExecuted {
		if result.Reason != "" {
			return fmt.Sprintf("%s: %s", result.Status, result.Reason)
		}
		return string(result.Status)
	}
	switch result.Name {
	case "start":
		return "设备已启动"
	case "pause":
		return "设备已暂停"
	case "reset":
		return "设备复位完成"
	case "set_mode":
		return "设备模式已更新"
	case "set_single_paused":
		return "单件循环状态已更新"
	case "set_frame_paused":
		return "单框循环状态已更新"
	case "inspect":
		return "抽检命令已执行"
	case "clear_bins":
		return "清空料仓命令已执行（仅模拟）"
	case "update_settings":
		return "维护参数已保存（仅模拟）"
	case "acknowledge_alarm":
		return "报警已确认（仅模拟）"
	default:
		return "命令已执行"
	}
}
