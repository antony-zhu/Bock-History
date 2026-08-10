// Package plcworker owns the one active PLC connection for a runtime session.
package plcworker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"block.local/block-agent/internal/easy521"
	"block.local/block-agent/internal/pointstore"
	"block.local/block-agent/internal/runtimeconfig"
)

const (
	CommandQueueCapacity = 64
	PollInterval         = time.Duration(runtimeconfig.RequiredScanIntervalMs) * time.Millisecond
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

// Adapter keeps BOOL writes on FC22 so shared D words never use a client-side
// read-modify-write fallback. FC06/FC10 are limited to explicitly configured
// numeric register spans.
type Adapter interface {
	ReadHoldingRegisters(context.Context, uint16, uint16) ([]uint16, error)
	MaskWriteBit(context.Context, uint16, uint8, bool) error
	WriteSingleRegister(context.Context, uint16, uint16) error
	WriteMultipleRegisters(context.Context, uint16, []uint16) error
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
	adapter      Adapter
	publish      func(map[string]pointstore.PointValue) error
	now          func() time.Time
	timeout      time.Duration
	points       map[string]pointPlan
	byWord       map[uint16][]pointPlan
	readable     map[string]struct{}
	batches      []readBatch
	last         map[string]pointstore.PointValue
	commands     chan commandRequest
	done         chan struct{}
	doneOnce     sync.Once
	ready        chan error
	readyOnce    sync.Once
	lifeMu       sync.Mutex
	stopping     bool
	stateMu      sync.Mutex
	disconnected bool
	onDisconnect func()
}

type pointPlan struct {
	definition runtimeconfig.PointDefinition
	address    pointAddress
}

type pointAddress struct {
	word  uint16
	bit   uint8
	count uint16
	bitIO bool
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

// New builds a session-only scan plan. It accepts documented D-word bit
// addresses plus the explicit simulator numeric profile (D-register spans
// with an exact type, count, and word order); no M-memory or inferred layouts
// are accepted.
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

	points, byWord, readable, batches, err := buildPlan(normalized)
	if err != nil {
		return nil, err
	}
	return &Worker{
		adapter: adapter, publish: publish, now: now, timeout: 2 * time.Second,
		points: points, byWord: byWord, readable: readable, batches: batches, last: make(map[string]pointstore.PointValue, len(readable)),
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

// SetDisconnectHandler receives only confirmed transport or explicit
// disconnect transitions. It does not implement reconnection or retries.
func (w *Worker) SetDisconnectHandler(handler func()) {
	w.stateMu.Lock()
	w.onDisconnect = handler
	w.stateMu.Unlock()
}

func (w *Worker) Disconnected() bool {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	return w.disconnected
}

// ConfirmDisconnected records a known disconnect and publishes a complete
// stale snapshot. Ordinary FC03 failures are errors until this explicit state
// transition occurs.
func (w *Worker) ConfirmDisconnected() error {
	values, changed := w.confirmDisconnected()
	if !changed {
		return nil
	}
	if err := w.publish(values); err != nil {
		return err
	}
	w.notifyDisconnected()
	return nil
}

// Run is the only place that calls the adapter. It does one initial scan, then
// starts the next 500 ms wait only after each complete scan ends. The single
// goroutine keeps slow scans and command confirmation scans from overlapping.
func (w *Worker) Run(ctx context.Context) {
	defer w.adapter.Close()
	defer w.doneOnce.Do(func() { close(w.done) })
	defer w.rejectQueued()
	defer w.stopAdmitting()
	defer w.readyOnce.Do(func() {
		w.ready <- context.Canceled
		close(w.ready)
	})

	if ctx.Err() != nil {
		return
	}

	// Configuration begins with the current PLC snapshot rather than waiting
	// for the first interval.
	_, err := w.readAll()
	w.readyOnce.Do(func() {
		w.ready <- err
		close(w.ready)
	})

	pollTimer := time.NewTimer(PollInterval)
	defer pollTimer.Stop()

	for {
		if ctx.Err() != nil {
			return
		}

		select {
		case request := <-w.commands:
			w.handleCommand(request, pollTimer)
			continue
		default:
		}

		select {
		case <-ctx.Done():
			return
		case request := <-w.commands:
			w.handleCommand(request, pollTimer)
		case <-pollTimer.C:
			// Prefer a command that became ready with this elapsed tick. Its
			// confirmation poll is the current snapshot, so an ordinary read
			// immediately before it would be redundant.
			select {
			case request := <-w.commands:
				w.handleCommand(request, pollTimer)
			default:
				_, _ = w.readAll()
				pollTimer.Reset(PollInterval)
			}
		}
	}
}

func (w *Worker) handleCommand(request commandRequest, pollTimer *time.Timer) {
	result := w.execute(request.command)
	if result.Code == CodePLCWriteFailed {
		// A timed out write may still have changed the PLC. Publish one current
		// complete snapshot before exposing that ambiguous failure to the HMI.
		_, _ = w.readAll()
	}
	// Successful commands finish their immediate full read inside execute.
	// Resetting here also discards an elapsed ordinary-poll tick, so that a
	// confirmation or failed-write read never causes a duplicate read after
	// the reply.
	resetPollTimer(pollTimer)
	w.reply(request, result)
}

func resetPollTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(PollInterval)
}

func (w *Worker) execute(command Command) Result {
	point, exists := w.points[command.PointID]
	if !exists {
		return failure(command.PointID, CodePointNotFound, "point is not configured")
	}
	if !allowsAction(point.definition, command.Action) {
		return failure(command.PointID, CodePointNotWritable, "point does not allow this action")
	}
	writePoint, exists := w.points[point.definition.WritePoint]
	if !exists {
		return failure(command.PointID, CodeInvalidRequest, "configured writePoint is unavailable")
	}
	var readPoint pointPlan
	hasReadPoint := point.definition.ReadPoint != ""
	if hasReadPoint {
		readPoint, exists = w.points[point.definition.ReadPoint]
		if !exists {
			return failure(command.PointID, CodeInvalidRequest, "configured readPoint is unavailable")
		}
	}

	if point.definition.Type != "bool" {
		if command.Action != "set" {
			return failure(command.PointID, CodeInvalidRequest, "numeric points only support set")
		}
		attempted, err := w.writeRegisters(writePoint, command.Value)
		if err != nil {
			if !attempted {
				return failure(command.PointID, CodeInvalidRequest, err.Error())
			}
			return failure(command.PointID, CodePLCWriteFailed, err.Error())
		}
		return w.confirmCommand(command.PointID, readPoint, hasReadPoint)
	}

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
		if !hasReadPoint {
			return failure(command.PointID, CodeInvalidRequest, "toggle requires a readable point")
		}
		values, err := w.readAll()
		if err != nil {
			return failure(command.PointID, CodePLCReadFailed, err.Error())
		}
		currentValue, returned := values[readPoint.definition.PointID]
		if !returned {
			return failure(command.PointID, CodePLCReadFailed, "configured readPoint was not returned by PLC")
		}
		current := currentValue.Value
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
	return w.confirmCommand(command.PointID, readPoint, hasReadPoint)
}

func (w *Worker) confirmCommand(pointID string, readPoint pointPlan, hasReadPoint bool) Result {
	values, err := w.readAll()
	if err != nil {
		return failure(pointID, CodePLCReadFailed, err.Error())
	}
	if !hasReadPoint {
		return Result{Success: true, PointID: pointID}
	}
	actual, exists := values[readPoint.definition.PointID]
	if !exists {
		return failure(pointID, CodePLCReadFailed, "configured readPoint was not returned by PLC")
	}
	return Result{Success: true, PointID: pointID, ActualValue: actual.Value}
}

func (w *Worker) writeBit(address pointAddress, value bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), w.timeout)
	defer cancel()
	return w.adapter.MaskWriteBit(ctx, address.word, address.bit, value)
}

func (w *Worker) writeRegisters(point pointPlan, value any) (bool, error) {
	words, err := numericWords(point.definition, value)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), w.timeout)
	defer cancel()
	switch point.definition.WriteMethod {
	case "fc06":
		return true, w.adapter.WriteSingleRegister(ctx, point.address.word, words[0])
	case "fc10":
		return true, w.adapter.WriteMultipleRegisters(ctx, point.address.word, words)
	default:
		return false, fmt.Errorf("unsupported numeric write method %q", point.definition.WriteMethod)
	}
}

func numericWords(definition runtimeconfig.PointDefinition, value any) ([]uint16, error) {
	if err := runtimeconfig.ValidateValue(definition.Type, value); err != nil {
		return nil, err
	}
	number, ok := numericFloat64(value)
	if !ok {
		return nil, errors.New("numeric point requires a number")
	}
	switch definition.Type {
	case "int16":
		return []uint16{uint16(int16(number))}, nil
	case "uint16":
		return []uint16{uint16(number)}, nil
	case "int32":
		if definition.WordOrder != "high-low" {
			return nil, fmt.Errorf("unsupported int32 word order %q", definition.WordOrder)
		}
		bits := uint32(int32(number))
		return []uint16{uint16(bits >> 16), uint16(bits)}, nil
	case "float32":
		bits := math.Float32bits(float32(number))
		low, high := uint16(bits), uint16(bits>>16)
		if definition.WordOrder == "low-high" {
			return []uint16{low, high}, nil
		}
		if definition.WordOrder == "high-low" {
			return []uint16{high, low}, nil
		}
		return nil, fmt.Errorf("unsupported float32 word order %q", definition.WordOrder)
	default:
		return nil, fmt.Errorf("unsupported numeric point type %q", definition.Type)
	}
}

func decodePointValue(point pointPlan, words []uint16) (any, error) {
	if len(words) != int(point.address.count) {
		return nil, errors.New("PLC returned an incomplete register span")
	}
	if point.address.bitIO {
		return words[0]&(uint16(1)<<point.address.bit) != 0, nil
	}
	switch point.definition.Type {
	case "int16":
		return int16(words[0]), nil
	case "uint16":
		return words[0], nil
	case "int32":
		if point.definition.WordOrder != "high-low" {
			return nil, fmt.Errorf("unsupported int32 word order %q", point.definition.WordOrder)
		}
		return int32(uint32(words[0])<<16 | uint32(words[1])), nil
	case "float32":
		var bits uint32
		if point.definition.WordOrder == "low-high" {
			bits = uint32(words[1])<<16 | uint32(words[0])
		} else if point.definition.WordOrder == "high-low" {
			bits = uint32(words[0])<<16 | uint32(words[1])
		} else {
			return nil, fmt.Errorf("unsupported float32 word order %q", point.definition.WordOrder)
		}
		return float64(math.Float32frombits(bits)), nil
	default:
		return nil, fmt.Errorf("unsupported numeric point type %q", point.definition.Type)
	}
}

func numericFloat64(value any) (float64, bool) {
	switch number := value.(type) {
	case int:
		return float64(number), true
	case int8:
		return float64(number), true
	case int16:
		return float64(number), true
	case int32:
		return float64(number), true
	case int64:
		return float64(number), true
	case uint:
		return float64(number), true
	case uint8:
		return float64(number), true
	case uint16:
		return float64(number), true
	case uint32:
		return float64(number), true
	case uint64:
		return float64(number), true
	case float32:
		return float64(number), true
	case float64:
		return number, true
	default:
		return 0, false
	}
}

// readAll batches configured readable D words. D504.1 and D504.2 therefore
// share one FC03 request for D504 instead of racing separate per-bit reads;
// a float32 span such as D800-D801 stays in one FC03 request.
func (w *Worker) readAll() (map[string]pointstore.PointValue, error) {
	values := make(map[string]pointstore.PointValue, len(w.readable))
	var readErr error
	confirmedDisconnect := false
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
			if errors.Is(err, easy521.ErrTransportDisconnected) && w.markDisconnected() {
				confirmedDisconnect = true
			}
			w.failureValues(values, batch)
			continue
		}
		w.goodValues(values, batch, registers)
	}
	if readErr == nil {
		w.markConnected()
	} else if w.Disconnected() {
		values = w.staleValues()
	}
	if err := w.publish(values); err != nil && readErr == nil {
		readErr = err
	}
	if confirmedDisconnect {
		w.notifyDisconnected()
	}
	return values, readErr
}

func (w *Worker) goodValues(values map[string]pointstore.PointValue, batch readBatch, registers []uint16) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	for index, word := range batch.words {
		for _, point := range w.pointsAt(word) {
			end := index + int(point.address.count)
			if end > len(registers) {
				continue
			}
			value, err := decodePointValue(point, registers[index:end])
			if err != nil {
				continue
			}
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

func (w *Worker) markDisconnected() bool {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.disconnected {
		return false
	}
	w.disconnected = true
	return true
}

func (w *Worker) markConnected() {
	w.stateMu.Lock()
	w.disconnected = false
	w.stateMu.Unlock()
}

func (w *Worker) confirmDisconnected() (map[string]pointstore.PointValue, bool) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.disconnected {
		return nil, false
	}
	w.disconnected = true
	return w.staleValuesLocked(), true
}

func (w *Worker) staleValues() map[string]pointstore.PointValue {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	return w.staleValuesLocked()
}

func (w *Worker) staleValuesLocked() map[string]pointstore.PointValue {
	now := w.now().UTC()
	values := make(map[string]pointstore.PointValue, len(w.readable))
	for pointID := range w.readable {
		item, exists := w.last[pointID]
		if !exists {
			item = pointstore.PointValue{}
		}
		item.Quality = "stale"
		item.UpdatedAt = now
		values[pointID] = item
		w.last[pointID] = cloneValue(item)
	}
	return values
}

func (w *Worker) notifyDisconnected() {
	w.stateMu.Lock()
	handler := w.onDisconnect
	w.stateMu.Unlock()
	if handler != nil {
		handler()
	}
}

func (w *Worker) failureValues(values map[string]pointstore.PointValue, batch readBatch) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	quality := "error"
	if w.disconnected {
		quality = "stale"
	}
	for _, word := range batch.words {
		for _, point := range w.pointsAt(word) {
			item, exists := w.last[point.definition.PointID]
			if !exists {
				item = pointstore.PointValue{}
			}
			item.Quality = quality
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

func buildPlan(config runtimeconfig.Config) (map[string]pointPlan, map[uint16][]pointPlan, map[string]struct{}, []readBatch, error) {
	points := make(map[string]pointPlan, len(config.Points))
	byWord := make(map[uint16][]pointPlan)
	readable := make(map[string]struct{}, len(config.Points))
	readPlans := make([]pointPlan, 0, len(config.Points))
	for _, definition := range config.Points {
		address, err := planAddress(definition)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("point %q: %w", definition.PointID, err)
		}
		point := pointPlan{definition: definition, address: address}
		points[definition.PointID] = point
		if definition.Access != "write" {
			byWord[address.word] = append(byWord[address.word], point)
			readable[definition.PointID] = struct{}{}
			readPlans = append(readPlans, point)
		}
	}
	for _, point := range points {
		if point.definition.Access == "read" {
			continue
		}
		writePoint, exists := points[point.definition.WritePoint]
		if !exists || writePoint.definition.Type != point.definition.Type || writePoint.address.bitIO != point.address.bitIO {
			return nil, nil, nil, nil, fmt.Errorf("point %q: writePoint is not a supported Easy521 %s point", point.definition.PointID, point.definition.Type)
		}
	}

	sort.Slice(readPlans, func(left, right int) bool {
		if readPlans[left].address.word == readPlans[right].address.word {
			return readPlans[left].address.count < readPlans[right].address.count
		}
		return readPlans[left].address.word < readPlans[right].address.word
	})
	batches := make([]readBatch, 0, len(readPlans))
	if len(readPlans) == 0 {
		return points, byWord, readable, batches, nil
	}
	batchStart := readPlans[0].address.word
	batchEnd := addressEnd(readPlans[0].address)
	for _, point := range readPlans[1:] {
		pointStart := point.address.word
		pointEnd := addressEnd(point.address)
		contiguous := uint32(pointStart) <= uint32(batchEnd)+1
		fits := uint32(maxWord(batchEnd, pointEnd))-uint32(batchStart)+1 <= 125
		if !contiguous || !fits {
			batches = append(batches, newReadBatch(batchStart, batchEnd))
			batchStart, batchEnd = pointStart, pointEnd
			continue
		}
		batchEnd = maxWord(batchEnd, pointEnd)
	}
	batches = append(batches, newReadBatch(batchStart, batchEnd))
	return points, byWord, readable, batches, nil
}

func planAddress(definition runtimeconfig.PointDefinition) (pointAddress, error) {
	if definition.Type == "bool" {
		address, err := parseBitAddress(definition.Address)
		if err != nil {
			return pointAddress{}, err
		}
		if definition.Access != "read" && definition.WriteMethod != "maskWrite" {
			return pointAddress{}, errors.New("Easy521 bit writes require writeMethod maskWrite")
		}
		return pointAddress{word: address.word, bit: address.bit, count: 1, bitIO: true}, nil
	}
	if definition.Type != "int16" && definition.Type != "uint16" && definition.Type != "int32" && definition.Type != "float32" {
		return pointAddress{}, fmt.Errorf("Easy521 FC03 numeric path does not support type %q", definition.Type)
	}
	address, err := parseRegisterAddress(definition.Address)
	if err != nil {
		return pointAddress{}, err
	}
	if uint32(address)+uint32(definition.RegisterCount) > 1<<16 {
		return pointAddress{}, fmt.Errorf("register span D%d..D%d is outside the PLC address range", address, uint32(address)+uint32(definition.RegisterCount)-1)
	}
	if definition.Access != "read" && definition.WriteMethod != "fc06" && definition.WriteMethod != "fc10" {
		return pointAddress{}, fmt.Errorf("numeric writes require writeMethod fc06 or fc10")
	}
	return pointAddress{word: address, count: uint16(definition.RegisterCount)}, nil
}

func addressEnd(address pointAddress) uint16 {
	return address.word + address.count - 1
}

func maxWord(left, right uint16) uint16 {
	if left > right {
		return left
	}
	return right
}

func newReadBatch(start, end uint16) readBatch {
	words := make([]uint16, int(end-start)+1)
	for index := range words {
		words[index] = start + uint16(index)
	}
	return readBatch{start: start, words: words}
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

func parseRegisterAddress(value string) (uint16, error) {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "D") || len(trimmed) == 1 || strings.Contains(trimmed, ".") {
		return 0, fmt.Errorf("address %q must be a D register such as D800", value)
	}
	word, err := strconv.ParseUint(trimmed[1:], 10, 16)
	if err != nil {
		return 0, fmt.Errorf("address %q has an invalid D register", value)
	}
	return uint16(word), nil
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
