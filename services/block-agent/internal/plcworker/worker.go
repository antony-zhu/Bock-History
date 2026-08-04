// Package plcworker owns the one active PLC connection for a runtime session.
package plcworker

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"block.local/block-agent/internal/pointstore"
	"block.local/block-agent/internal/runtimeconfig"
)

const (
	CommandQueueCapacity = 64
	PollInterval         = 50 * time.Millisecond
)

const (
	CodeBusy                 = "BUSY"
	CodeServiceStopping      = "SERVICE_STOPPING"
	CodePointNotFound        = "POINT_NOT_FOUND"
	CodePointNotWritable     = "POINT_NOT_WRITABLE"
	CodeInvalidRequest       = "INVALID_REQUEST"
	CodePLCWriteFailed       = "PLC_WRITE_FAILED"
	CodePLCReadFailed        = "PLC_READ_FAILED"
	CodePointStateUnexpected = "POINT_STATE_UNEXPECTED"
)

// Adapter is deliberately narrow. Its only write is a one-bit FC22 mask
// write, so a shared D word never has a full-register write or a client-side
// read-modify-write fallback.
type Adapter interface {
	ReadHoldingRegisters(context.Context, uint16, uint16) ([]uint16, error)
	MaskWriteBit(context.Context, uint16, uint8, bool) error
	Close()
}

// Command is already validated by the WS router before it reaches the worker.
// The worker still validates it at this boundary so callers cannot bypass the
// configured point table.
type Command struct {
	PointID string
	Action  string
	Value   any
}

type Result struct {
	Success     bool
	PointID     string
	ActualValue any
	Code        string
	Message     string
}

// Factory lets the runtime create one fresh worker/adapter for each HMI
// session. It is an injection point for the concrete Easy521 client and for
// the local PLC simulator tests.
type Factory func(runtimeconfig.Config, func(map[string]pointstore.PointValue) error) (*Worker, error)

type Worker struct {
	adapter   Adapter
	publish   func(map[string]pointstore.PointValue) error
	now       func() time.Time
	timeout   time.Duration
	points    map[string]pointPlan
	byWord    map[uint16][]pointPlan
	batches   []readBatch
	last      map[string]pointstore.PointValue
	commands  chan commandRequest
	done      chan struct{}
	doneOnce  sync.Once
	ready     chan error
	readyOnce sync.Once
	lifeMu    sync.Mutex
	stopping  bool
}

type pointPlan struct {
	definition runtimeconfig.PointDefinition
	address    bitAddress
}

type bitAddress struct {
	word uint16
	bit  uint8
}

type readBatch struct {
	start uint16
	words []uint16
}

type commandRequest struct {
	command Command
	reply   chan Result
}

// New builds a session-only scan plan. The current Easy521 implementation is
// intentionally restricted to documented candidate D-word bit addresses
// (D504.1, D504.2, ...); M-memory mapping and non-bit data layouts have not
// yet been verified and therefore are not guessed here.
func New(config runtimeconfig.Config, adapter Adapter, publish func(map[string]pointstore.PointValue) error, now func() time.Time) (*Worker, error) {
	normalized, err := runtimeconfig.Normalize(config)
	if err != nil {
		return nil, err
	}
	if adapter == nil {
		return nil, errors.New("PLC adapter is required")
	}
	if publish == nil {
		return nil, errors.New("point publish callback is required")
	}
	if now == nil {
		now = time.Now
	}

	points, byWord, batches, err := buildPlan(normalized)
	if err != nil {
		return nil, err
	}
	return &Worker{
		adapter: adapter, publish: publish, now: now, timeout: 2 * time.Second,
		points: points, byWord: byWord, batches: batches, last: make(map[string]pointstore.PointValue, len(points)),
		commands: make(chan commandRequest, CommandQueueCapacity), done: make(chan struct{}), ready: make(chan error, 1),
	}, nil
}

// TrySubmit admits one user action without blocking. A full 64-entry FIFO is
// rejected immediately; the reply channel is buffered so a disconnected WS
// client can never block the sole PLC goroutine.
func (w *Worker) TrySubmit(command Command) (<-chan Result, Result, bool) {
	reply := make(chan Result, 1)
	request := commandRequest{command: command, reply: reply}
	w.lifeMu.Lock()
	defer w.lifeMu.Unlock()
	if w.stopping {
		return nil, failure(command.PointID, CodeServiceStopping, "PLC worker is stopping"), false
	}
	select {
	case w.commands <- request:
		return reply, Result{}, true
	default:
		return nil, failure(command.PointID, CodeBusy, "PLC command queue is full"), false
	}
}

func (w *Worker) Done() <-chan struct{} {
	return w.done
}

// Ready reports the result of the one initial PLC read. It lets the runtime
// enqueue the first complete snapshot before it enables ordinary changes.
func (w *Worker) Ready() <-chan error {
	return w.ready
}

// Run is the only place that calls the adapter. It does one initial scan, then
// coalesces 50 ms ticks in pollPending while always giving an already queued
// external command priority over the next ordinary poll.
func (w *Worker) Run(ctx context.Context) {
	defer w.adapter.Close()
	defer w.doneOnce.Do(func() { close(w.done) })
	defer w.rejectQueued()
	defer w.stopAdmitting()
	defer w.readyOnce.Do(func() {
		w.ready <- context.Canceled
		close(w.ready)
	})

	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()
	pollPending := true // configuration begins with the current PLC snapshot.
	initial := true

	for {
		if ctx.Err() != nil {
			return
		}

		select {
		case request := <-w.commands:
			w.reply(request, w.execute(request.command))
			// A command always gets a fresh poll opportunity, including a failed
			// write whose actual output is then left to PLC feedback.
			pollPending = true
			continue
		default:
		}

		if pollPending {
			_, err := w.readAll()
			if initial {
				w.readyOnce.Do(func() {
					w.ready <- err
					close(w.ready)
				})
				initial = false
			}
			pollPending = false
			continue
		}

		select {
		case <-ctx.Done():
			return
		case request := <-w.commands:
			w.reply(request, w.execute(request.command))
			pollPending = true
		case <-ticker.C:
			// Multiple ticks collapse to this single boolean rather than a
			// historical backlog of PLC reads.
			pollPending = true
		}
	}
}

func (w *Worker) execute(command Command) Result {
	point, exists := w.points[command.PointID]
	if !exists {
		return failure(command.PointID, CodePointNotFound, "point is not configured")
	}
	if !allowsAction(point.definition, command.Action) {
		return failure(command.PointID, CodePointNotWritable, "point does not allow this action")
	}
	writePoint := w.points[point.definition.WritePoint]
	readPoint := w.points[point.definition.ReadPoint]

	var target bool
	switch command.Action {
	case "set":
		value, ok := command.Value.(bool)
		if !ok {
			return failure(command.PointID, CodeInvalidRequest, "Easy521 bit set requires a boolean value")
		}
		target = value
		if err := w.writeBit(writePoint.address, target); err != nil {
			return failure(command.PointID, CodePLCWriteFailed, err.Error())
		}
	case "pulse":
		active, ok := point.definition.Write.ActiveValue.(bool)
		if !ok {
			return failure(command.PointID, CodeInvalidRequest, "pulse activeValue must be boolean")
		}
		if err := w.writeBit(writePoint.address, active); err != nil {
			return failure(command.PointID, CodePLCWriteFailed, err.Error())
		}
		// Once active was written the clear is mandatory, even when the HMI
		// session was cancelled while this serial operation was in progress.
		timer := time.NewTimer(time.Duration(point.definition.Write.PulseMs) * time.Millisecond)
		<-timer.C
		defaultValue, ok := point.definition.Write.DefaultValue.(bool)
		if !ok {
			return failure(command.PointID, CodeInvalidRequest, "pulse defaultValue must be boolean")
		}
		if err := w.writeBit(writePoint.address, defaultValue); err != nil {
			return failure(command.PointID, CodePLCWriteFailed, err.Error())
		}
	case "press":
		active, ok := point.definition.Write.ActiveValue.(bool)
		if !ok {
			return failure(command.PointID, CodeInvalidRequest, "press activeValue must be boolean")
		}
		if err := w.writeBit(writePoint.address, active); err != nil {
			return failure(command.PointID, CodePLCWriteFailed, err.Error())
		}
	case "release":
		defaultValue, ok := point.definition.Write.DefaultValue.(bool)
		if !ok {
			return failure(command.PointID, CodeInvalidRequest, "release defaultValue must be boolean")
		}
		if err := w.writeBit(writePoint.address, defaultValue); err != nil {
			return failure(command.PointID, CodePLCWriteFailed, err.Error())
		}
	case "toggle":
		values, err := w.readAll()
		if err != nil {
			return failure(command.PointID, CodePLCReadFailed, err.Error())
		}
		current := values[readPoint.definition.PointID].Value
		active := point.definition.Write.ActiveValue
		defaultValue := point.definition.Write.DefaultValue
		switch {
		case reflect.DeepEqual(current, active):
			target = boolValue(defaultValue)
		case reflect.DeepEqual(current, defaultValue):
			target = boolValue(active)
		default:
			return failure(command.PointID, CodePointStateUnexpected, "PLC feedback is neither activeValue nor defaultValue")
		}
		if err := w.writeBit(writePoint.address, target); err != nil {
			return failure(command.PointID, CodePLCWriteFailed, err.Error())
		}
	default:
		return failure(command.PointID, CodeInvalidRequest, "unsupported point action")
	}

	values, err := w.readAll()
	if err != nil {
		return failure(command.PointID, CodePLCReadFailed, err.Error())
	}
	actual, exists := values[readPoint.definition.PointID]
	if !exists {
		return failure(command.PointID, CodePLCReadFailed, "configured readPoint was not returned by PLC")
	}
	return Result{Success: true, PointID: command.PointID, ActualValue: actual.Value}
}

func (w *Worker) writeBit(address bitAddress, value bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), w.timeout)
	defer cancel()
	return w.adapter.MaskWriteBit(ctx, address.word, address.bit, value)
}

// readAll batches all configured D words. D504.1 and D504.2 therefore share
// one FC03 request for D504 instead of racing separate per-bit reads.
func (w *Worker) readAll() (map[string]pointstore.PointValue, error) {
	values := make(map[string]pointstore.PointValue, len(w.points))
	var readErr error
	for _, batch := range w.batches {
		ctx, cancel := context.WithTimeout(context.Background(), w.timeout)
		registers, err := w.adapter.ReadHoldingRegisters(ctx, batch.start, uint16(len(batch.words)))
		cancel()
		if err != nil || len(registers) != len(batch.words) {
			if err == nil {
				err = errors.New("PLC returned an incomplete FC03 response")
			}
			if readErr == nil {
				readErr = err
			}
			w.failureValues(values, batch)
			continue
		}
		w.goodValues(values, batch, registers)
	}
	if err := w.publish(values); err != nil && readErr == nil {
		readErr = err
	}
	return values, readErr
}

func (w *Worker) goodValues(values map[string]pointstore.PointValue, batch readBatch, registers []uint16) {
	for index, word := range batch.words {
		for _, point := range w.pointsAt(word) {
			value := registers[index]&(uint16(1)<<point.address.bit) != 0
			item := pointstore.PointValue{Value: value, Quality: "good", UpdatedAt: w.now().UTC()}
			if point.definition.Alarm != nil {
				active := reflect.DeepEqual(value, point.definition.Alarm.AlarmValue)
				item.AlarmActive = &active
			}
			values[point.definition.PointID] = item
			w.last[point.definition.PointID] = cloneValue(item)
		}
	}
}

func (w *Worker) failureValues(values map[string]pointstore.PointValue, batch readBatch) {
	for _, word := range batch.words {
		for _, point := range w.pointsAt(word) {
			item, exists := w.last[point.definition.PointID]
			if !exists {
				item = pointstore.PointValue{}
			}
			item.Quality = "stale"
			item.UpdatedAt = w.now().UTC()
			values[point.definition.PointID] = item
			w.last[point.definition.PointID] = cloneValue(item)
		}
	}
}

func (w *Worker) pointsAt(word uint16) []pointPlan {
	return w.byWord[word]
}

func (w *Worker) reply(request commandRequest, result Result) {
	request.reply <- result
}

func (w *Worker) rejectQueued() {
	for {
		select {
		case request := <-w.commands:
			w.reply(request, failure(request.command.PointID, CodeServiceStopping, "PLC worker is stopping"))
		default:
			return
		}
	}
}

func (w *Worker) stopAdmitting() {
	w.lifeMu.Lock()
	w.stopping = true
	w.lifeMu.Unlock()
}

func buildPlan(config runtimeconfig.Config) (map[string]pointPlan, map[uint16][]pointPlan, []readBatch, error) {
	points := make(map[string]pointPlan, len(config.Points))
	byWord := make(map[uint16][]pointPlan)
	words := make(map[uint16]struct{})
	for _, definition := range config.Points {
		if definition.Type != "bool" {
			return nil, nil, nil, fmt.Errorf("point %q: Easy521 FC03/FC22 path currently supports only bool D-word bits", definition.PointID)
		}
		address, err := parseBitAddress(definition.Address)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("point %q: %w", definition.PointID, err)
		}
		if definition.Access != "read" && definition.WriteMethod != "maskWrite" {
			return nil, nil, nil, fmt.Errorf("point %q: Easy521 bit writes require writeMethod maskWrite", definition.PointID)
		}
		point := pointPlan{definition: definition, address: address}
		points[definition.PointID] = point
		byWord[address.word] = append(byWord[address.word], point)
		words[address.word] = struct{}{}
	}
	for _, point := range points {
		if point.definition.Access == "read" {
			continue
		}
		writePoint, exists := points[point.definition.WritePoint]
		if !exists || writePoint.definition.Type != "bool" {
			return nil, nil, nil, fmt.Errorf("point %q: writePoint is not a supported Easy521 bit", point.definition.PointID)
		}
	}

	ordered := make([]int, 0, len(words))
	for word := range words {
		ordered = append(ordered, int(word))
	}
	sort.Ints(ordered)
	batches := make([]readBatch, 0, len(ordered))
	for _, rawWord := range ordered {
		word := uint16(rawWord)
		if len(batches) == 0 || word != batches[len(batches)-1].words[len(batches[len(batches)-1].words)-1]+1 || len(batches[len(batches)-1].words) == 125 {
			batches = append(batches, readBatch{start: word})
		}
		last := len(batches) - 1
		batches[last].words = append(batches[last].words, word)
	}
	return points, byWord, batches, nil
}

func parseBitAddress(value string) (bitAddress, error) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "D") || len(parts[0]) == 1 {
		return bitAddress{}, fmt.Errorf("address %q must be a candidate D-word bit such as D504.1", value)
	}
	word, err := strconv.ParseUint(parts[0][1:], 10, 16)
	if err != nil {
		return bitAddress{}, fmt.Errorf("address %q has an invalid D word", value)
	}
	bit, err := strconv.ParseUint(parts[1], 10, 8)
	if err != nil || bit > 15 {
		return bitAddress{}, fmt.Errorf("address %q has an invalid bit", value)
	}
	return bitAddress{word: uint16(word), bit: uint8(bit)}, nil
}

func allowsAction(definition runtimeconfig.PointDefinition, action string) bool {
	if definition.Access == "read" || definition.Write == nil {
		return false
	}
	switch definition.Write.Mode {
	case "set":
		return action == "set"
	case "pulse":
		return action == "pulse"
	case "momentary":
		return action == "press" || action == "release"
	case "toggle":
		return action == "toggle"
	default:
		return false
	}
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func failure(pointID, code, message string) Result {
	return Result{PointID: pointID, Code: code, Message: message}
}

func cloneValue(value pointstore.PointValue) pointstore.PointValue {
	copyValue := value
	if value.AlarmActive != nil {
		alarm := *value.AlarmActive
		copyValue.AlarmActive = &alarm
	}
	return copyValue
}
