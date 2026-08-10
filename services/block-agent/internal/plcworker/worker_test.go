package plcworker

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"block.local/block-agent/internal/easy521"
	"block.local/block-agent/internal/pointstore"
	"block.local/block-agent/internal/runtimeconfig"
)

func TestInitialPollBatchesD504BitsIntoOneFC03(t *testing.T) {
	adapter := newFakeAdapter(0x0006) // D504.1 and D504.2 are both set.
	published := make(chan map[string]pointstore.PointValue, 2)
	worker := newWorker(t, testConfig("pulse", 100), adapter, func(values map[string]pointstore.PointValue) error {
		published <- values
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	go worker.Run(ctx)
	values := <-published
	cancel()
	waitDone(t, worker)

	if values["command"].Value != true || values["feedback"].Value != true {
		t.Fatalf("initial values = %#v", values)
	}
	reads := adapter.readCalls()
	if len(reads) == 0 || reads[0].address != 504 || reads[0].quantity != 1 {
		t.Fatalf("FC03 batches = %#v, want D504 once", reads)
	}
}

func TestFloat32ProfileReadsD800SpanAndWritesFC10(t *testing.T) {
	adapter := newFakeAdapter(0)
	adapter.registers[800] = 0x0000
	adapter.registers[801] = 0x4148 // float32 12.5, low-high word order.
	published := make(chan map[string]pointstore.PointValue, 4)
	worker := newWorker(t, float32Config(), adapter, func(values map[string]pointstore.PointValue) error {
		published <- values
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	initial := <-published
	if value, ok := initial["manual.motion.x.jog.speed.parameter"]; !ok || value.Value != float64(12.5) {
		t.Fatalf("initial float32 value = %#v", initial)
	}
	reads := adapter.readCalls()
	if len(reads) == 0 || reads[0].address != 800 || reads[0].quantity != 2 {
		t.Fatalf("float32 FC03 span = %#v, want D800-D801", reads)
	}

	reply, rejected, accepted := worker.TrySubmit(Command{PointID: "manual.motion.x.jog.speed.parameter", Action: "set", Value: float64(8.25)})
	if !accepted {
		t.Fatalf("numeric set was rejected: %+v", rejected)
	}
	result := waitResult(t, reply)
	if !result.Success || result.ActualValue != float64(8.25) {
		t.Fatalf("numeric result = %+v", result)
	}
	writes := adapter.registerWriteCalls()
	wantBits := math.Float32bits(8.25)
	wantWords := []uint16{uint16(wantBits), uint16(wantBits >> 16)}
	if len(writes) != 1 || writes[0].method != "fc10" || writes[0].address != 800 || !equalWords(writes[0].values, wantWords) {
		t.Fatalf("FC10 numeric write = %#v, want %#v", writes, wantWords)
	}
}

func TestFloat32HighLowProfileReadsD522SpanAndWritesFC10(t *testing.T) {
	adapter := newFakeAdapter(0)
	adapter.registers[522] = 0x4148 // float32 12.5, high-low word order.
	adapter.registers[523] = 0x0000
	published := make(chan map[string]pointstore.PointValue, 4)
	worker := newWorker(t, float32HighLowConfig(), adapter, func(values map[string]pointstore.PointValue) error {
		published <- values
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	initial := <-published
	if value, ok := initial["home.speed.automatic"]; !ok || value.Value != float64(12.5) {
		t.Fatalf("initial high-low float32 value = %#v", initial)
	}
	reads := adapter.readCalls()
	if len(reads) == 0 || reads[0].address != 522 || reads[0].quantity != 2 {
		t.Fatalf("high-low float32 FC03 span = %#v, want D522-D523", reads)
	}

	reply, rejected, accepted := worker.TrySubmit(Command{PointID: "home.speed.automatic", Action: "set", Value: float64(8.25)})
	if !accepted {
		t.Fatalf("numeric set was rejected: %+v", rejected)
	}
	if result := waitResult(t, reply); !result.Success || result.ActualValue != float64(8.25) {
		t.Fatalf("numeric result = %+v", result)
	}
	writes := adapter.registerWriteCalls()
	wantBits := math.Float32bits(8.25)
	wantWords := []uint16{uint16(wantBits >> 16), uint16(wantBits)}
	if len(writes) != 1 || writes[0].method != "fc10" || writes[0].address != 522 || !equalWords(writes[0].values, wantWords) {
		t.Fatalf("high-low FC10 numeric write = %#v, want %#v", writes, wantWords)
	}
}

func TestProductionDINTHighLowReadsAndWritesFC10(t *testing.T) {
	adapter := newFakeAdapter(0)
	adapter.registers[902], adapter.registers[903] = 0x0000, 0x04D2   // 1234
	adapter.registers[904], adapter.registers[905] = 0xFFFF, 0xFF9C   // -100
	adapter.registers[1000], adapter.registers[1001] = 0x075B, 0xCD15 // 123456789
	published := make(chan map[string]pointstore.PointValue, 4)
	worker := newWorker(t, productionDINTConfig(), adapter, func(values map[string]pointstore.PointValue) error {
		published <- values
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	initial := <-published
	if initial["production.output.today"].Value != int32(1234) || initial["production.quality.passed"].Value != int32(-100) || initial["maintenance.production.target"].Value != int32(123456789) {
		t.Fatalf("initial DINT values = %#v", initial)
	}

	reply, rejected, accepted := worker.TrySubmit(Command{PointID: "maintenance.production.target", Action: "set", Value: float64(-123456789)})
	if !accepted {
		t.Fatalf("DINT set was rejected: %+v", rejected)
	}
	if result := waitResult(t, reply); !result.Success || result.ActualValue != int32(-123456789) {
		t.Fatalf("DINT result = %+v", result)
	}
	writes := adapter.registerWriteCalls()
	wantWords := []uint16{0xF8A4, 0x32EB}
	if len(writes) != 1 || writes[0].method != "fc10" || writes[0].address != 1000 || !equalWords(writes[0].values, wantWords) {
		t.Fatalf("DINT FC10 write = %#v, want %#v", writes, wantWords)
	}
}

func TestUint16ProfileWritesFC06(t *testing.T) {
	adapter := newFakeAdapter(0)
	adapter.registers[820] = 7
	worker := newWorker(t, uint16Config(), adapter, func(map[string]pointstore.PointValue) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	waitFor(t, time.Second, func() bool { return len(adapter.readCalls()) > 0 })

	reply, rejected, accepted := worker.TrySubmit(Command{PointID: "manual.test.uint16", Action: "set", Value: float64(9)})
	if !accepted {
		t.Fatalf("numeric set was rejected: %+v", rejected)
	}
	if result := waitResult(t, reply); !result.Success || result.ActualValue != uint16(9) {
		t.Fatalf("numeric result = %+v", result)
	}
	writes := adapter.registerWriteCalls()
	if len(writes) != 1 || writes[0].method != "fc06" || writes[0].address != 820 || !equalWords(writes[0].values, []uint16{9}) {
		t.Fatalf("FC06 numeric write = %#v", writes)
	}
}

func TestInt16ProfileReadsAndWritesFC06(t *testing.T) {
	adapter := newFakeAdapter(0)
	adapter.registers[520] = 0xFF8F // -113
	worker := newWorker(t, int16Config(), adapter, func(map[string]pointstore.PointValue) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	waitFor(t, time.Second, func() bool { return len(adapter.readCalls()) > 0 })

	reply, rejected, accepted := worker.TrySubmit(Command{PointID: "maintenance.frame.height", Action: "set", Value: float64(113)})
	if !accepted {
		t.Fatalf("int16 set was rejected: %+v", rejected)
	}
	if result := waitResult(t, reply); !result.Success || result.ActualValue != int16(113) {
		t.Fatalf("int16 result = %+v", result)
	}
	writes := adapter.registerWriteCalls()
	if len(writes) != 1 || writes[0].method != "fc06" || writes[0].address != 520 || !equalWords(writes[0].values, []uint16{113}) {
		t.Fatalf("int16 FC06 write = %#v", writes)
	}
}

func TestWriteOnlyPulseUsesDefault100msFC22(t *testing.T) {
	adapter := newFakeAdapter(0)
	config, err := runtimeconfig.Normalize(runtimeconfig.Config{ScanIntervalMs: runtimeconfig.RequiredScanIntervalMs, Points: []runtimeconfig.PointDefinition{{
		PointID: "manual.motion.x.relative.trigger.action", Address: "D550.3", Type: "bool", Access: "write",
		WritePoint: "manual.motion.x.relative.trigger.action", WriteMethod: "maskWrite",
		Write: &runtimeconfig.WriteDefinition{Mode: "pulse", ActiveValue: true, DefaultValue: false},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if config.Points[0].Write.PulseMs != runtimeconfig.DefaultPulseMs {
		t.Fatalf("pulse default = %d", config.Points[0].Write.PulseMs)
	}
	worker := newWorker(t, config, adapter, func(map[string]pointstore.PointValue) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)

	reply, rejected, accepted := worker.TrySubmit(Command{PointID: "manual.motion.x.relative.trigger.action", Action: "pulse"})
	if !accepted {
		t.Fatalf("pulse was rejected: %+v", rejected)
	}
	if result := waitResult(t, reply); !result.Success || result.ActualValue != nil {
		t.Fatalf("write-only pulse result = %+v", result)
	}
	writes := adapter.writeCalls()
	if len(writes) != 2 || writes[0].word != 550 || writes[0].bit != 3 || !writes[0].value || writes[1].value {
		t.Fatalf("write-only FC22 pulse = %#v", writes)
	}
}

func TestPollIntervalRemainsFiftyMilliseconds(t *testing.T) {
	if PollInterval != 50*time.Millisecond {
		t.Fatalf("PollInterval = %s, want 50ms", PollInterval)
	}
}

func TestPulseUsesFC22SetWaitClearThenFreshRead(t *testing.T) {
	adapter := newFakeAdapter(0)
	published := make(chan map[string]pointstore.PointValue, 8)
	worker := newWorker(t, testConfig("pulse", 20), adapter, func(values map[string]pointstore.PointValue) error {
		published <- values
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	<-published // initial scan

	reply, rejected, accepted := worker.TrySubmit(Command{PointID: "command", Action: "pulse"})
	if !accepted {
		t.Fatalf("pulse was rejected: %+v", rejected)
	}
	result := waitResult(t, reply)
	if !result.Success || result.ActualValue != false {
		t.Fatalf("pulse result = %+v", result)
	}

	writes := adapter.writeCalls()
	if len(writes) != 2 || !writes[0].value || writes[1].value || writes[0].word != 504 || writes[0].bit != 1 {
		t.Fatalf("FC22 pulse sequence = %#v", writes)
	}
	if adapter.word(504)&0x0002 != 0 {
		t.Fatalf("pulse did not clear D504.1: %#x", adapter.word(504))
	}
}

func TestPulseCompletesClearAfterSessionCancellation(t *testing.T) {
	adapter := newFakeAdapter(0)
	activeWritten := make(chan struct{})
	adapter.afterWrite = func(call writeCall) {
		if call.value {
			close(activeWritten)
		}
	}
	worker := newWorker(t, testConfig("pulse", 25), adapter, func(map[string]pointstore.PointValue) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	go worker.Run(ctx)
	waitFor(t, time.Second, func() bool { return len(adapter.readCalls()) > 0 })

	reply, rejected, accepted := worker.TrySubmit(Command{PointID: "command", Action: "pulse"})
	if !accepted {
		t.Fatalf("pulse was rejected: %+v", rejected)
	}
	<-activeWritten
	cancel()
	result := waitResult(t, reply)
	if !result.Success {
		t.Fatalf("pulse did not complete after cancellation: %+v", result)
	}
	waitDone(t, worker)
	writes := adapter.writeCalls()
	if len(writes) != 2 || !writes[0].value || writes[1].value {
		t.Fatalf("cancelled pulse did not clear: %#v", writes)
	}
}

func TestToggleReadsLatestFeedbackThenWritesOneFC22(t *testing.T) {
	adapter := newFakeAdapter(0)
	worker := newWorker(t, testConfig("toggle", 100), adapter, func(map[string]pointstore.PointValue) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	waitFor(t, time.Second, func() bool { return len(adapter.readCalls()) > 0 })

	reply, rejected, accepted := worker.TrySubmit(Command{PointID: "command", Action: "toggle"})
	if !accepted {
		t.Fatalf("toggle was rejected: %+v", rejected)
	}
	result := waitResult(t, reply)
	if !result.Success || result.ActualValue != true {
		t.Fatalf("toggle result = %+v", result)
	}
	writes := adapter.writeCalls()
	if len(writes) != 1 || !writes[0].value {
		t.Fatalf("toggle write = %#v", writes)
	}
}

func TestWriteDoesNotRetry(t *testing.T) {
	adapter := newFakeAdapter(0)
	adapter.writeErr = errors.New("timeout")
	worker := newWorker(t, testConfig("set", 100), adapter, func(map[string]pointstore.PointValue) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	waitFor(t, time.Second, func() bool { return len(adapter.readCalls()) > 0 })

	reply, rejected, accepted := worker.TrySubmit(Command{PointID: "command", Action: "set", Value: true})
	if !accepted {
		t.Fatalf("set was rejected: %+v", rejected)
	}
	result := waitResult(t, reply)
	if result.Success || result.Code != CodePLCWriteFailed {
		t.Fatalf("write failure result = %+v", result)
	}
	if writes := adapter.writeCalls(); len(writes) != 1 {
		t.Fatalf("failed write retried %d times", len(writes))
	}
}

func TestQueueRejectsTheSixtyFifthCommand(t *testing.T) {
	worker := newWorker(t, testConfig("set", 100), newFakeAdapter(0), func(map[string]pointstore.PointValue) error { return nil })
	for index := 0; index < CommandQueueCapacity; index++ {
		_, rejected, accepted := worker.TrySubmit(Command{PointID: "command", Action: "set", Value: true})
		if !accepted {
			t.Fatalf("command %d rejected before queue was full: %+v", index, rejected)
		}
	}
	_, rejected, accepted := worker.TrySubmit(Command{PointID: "command", Action: "set", Value: true})
	if accepted || rejected.Code != CodeBusy {
		t.Fatalf("sixty-fifth command = accepted:%v result:%+v", accepted, rejected)
	}
}

func TestSingleReadFailurePublishesErrorWithoutInventingFalse(t *testing.T) {
	adapter := newFakeAdapter(0)
	adapter.readErr = errors.New("temporary FC03 failure")
	published := make(chan map[string]pointstore.PointValue, 1)
	worker := newWorker(t, testConfig("pulse", 100), adapter, func(values map[string]pointstore.PointValue) error {
		published <- values
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	values := <-published
	if value := values["feedback"]; value.Value != nil || value.Quality != "error" || value.AlarmActive != nil {
		t.Fatalf("single read failure value = %#v", value)
	}
}

func TestConfirmedTransportDisconnectPublishesStaleAndNotifies(t *testing.T) {
	adapter := newFakeAdapter(0)
	adapter.readErr = easy521.ErrTransportDisconnected
	published := make(chan map[string]pointstore.PointValue, 2)
	disconnected := make(chan struct{}, 1)
	worker := newWorker(t, testConfig("pulse", 100), adapter, func(values map[string]pointstore.PointValue) error {
		published <- values
		return nil
	})
	worker.SetDisconnectHandler(func() { disconnected <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	go worker.Run(ctx)
	values := <-published
	cancel()
	waitDone(t, worker)

	if value := values["feedback"]; value.Value != nil || value.Quality != "stale" || value.AlarmActive != nil {
		t.Fatalf("transport disconnect value = %#v", value)
	}
	select {
	case <-disconnected:
	default:
		t.Fatal("transport disconnect did not notify")
	}
}

func newWorker(t *testing.T, config runtimeconfig.Config, adapter *fakeAdapter, publish func(map[string]pointstore.PointValue) error) *Worker {
	t.Helper()
	worker, err := New(config, adapter, publish, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func testConfig(mode string, pulseMs int) runtimeconfig.Config {
	return runtimeconfig.Config{ScanIntervalMs: 50, Points: []runtimeconfig.PointDefinition{
		{
			PointID: "command", Address: "D504.1", Type: "bool", Access: "read_write",
			ReadPoint: "feedback", WritePoint: "command", WriteMethod: "maskWrite",
			Write: &runtimeconfig.WriteDefinition{Mode: mode, ActiveValue: true, DefaultValue: false, PulseMs: pulseMs},
		},
		{PointID: "feedback", Address: "D504.1", Type: "bool", Access: "read", ReadPoint: "feedback"},
	}}
}

func float32Config() runtimeconfig.Config {
	return runtimeconfig.Config{ScanIntervalMs: runtimeconfig.RequiredScanIntervalMs, Points: []runtimeconfig.PointDefinition{{
		PointID: "manual.motion.x.jog.speed.parameter", Address: "D800", Type: "float32", Access: "read_write",
		ReadPoint: "manual.motion.x.jog.speed.parameter", WritePoint: "manual.motion.x.jog.speed.parameter", WriteMethod: "fc10",
		RegisterCount: 2, WordOrder: "low-high", Write: &runtimeconfig.WriteDefinition{Mode: "set"},
	}}}
}

func float32HighLowConfig() runtimeconfig.Config {
	return runtimeconfig.Config{ScanIntervalMs: runtimeconfig.RequiredScanIntervalMs, Points: []runtimeconfig.PointDefinition{{
		PointID: "home.speed.automatic", Address: "D522", Type: "float32", Access: "read_write",
		ReadPoint: "home.speed.automatic", WritePoint: "home.speed.automatic", WriteMethod: "fc10",
		RegisterCount: 2, WordOrder: "high-low", Write: &runtimeconfig.WriteDefinition{Mode: "set"},
	}}}
}

func productionDINTConfig() runtimeconfig.Config {
	return runtimeconfig.Config{ScanIntervalMs: runtimeconfig.RequiredScanIntervalMs, Points: []runtimeconfig.PointDefinition{
		{
			PointID: "maintenance.production.target", Address: "D1000", Type: "int32", Access: "read_write",
			ReadPoint: "maintenance.production.target", WritePoint: "maintenance.production.target", WriteMethod: "fc10",
			RegisterCount: 2, WordOrder: "high-low", Write: &runtimeconfig.WriteDefinition{Mode: "set"},
		},
		{PointID: "production.output.today", Address: "D902", Type: "int32", Access: "read", ReadPoint: "production.output.today", RegisterCount: 2, WordOrder: "high-low"},
		{PointID: "production.quality.passed", Address: "D904", Type: "int32", Access: "read", ReadPoint: "production.quality.passed", RegisterCount: 2, WordOrder: "high-low"},
	}}
}

func uint16Config() runtimeconfig.Config {
	return runtimeconfig.Config{ScanIntervalMs: runtimeconfig.RequiredScanIntervalMs, Points: []runtimeconfig.PointDefinition{{
		PointID: "manual.test.uint16", Address: "D820", Type: "uint16", Access: "read_write",
		ReadPoint: "manual.test.uint16", WritePoint: "manual.test.uint16", WriteMethod: "fc06",
		RegisterCount: 1, Write: &runtimeconfig.WriteDefinition{Mode: "set"},
	}}}
}

func int16Config() runtimeconfig.Config {
	return runtimeconfig.Config{ScanIntervalMs: runtimeconfig.RequiredScanIntervalMs, Points: []runtimeconfig.PointDefinition{{
		PointID: "maintenance.frame.height", Address: "D520", Type: "int16", Access: "read_write",
		ReadPoint: "maintenance.frame.height", WritePoint: "maintenance.frame.height", WriteMethod: "fc06",
		RegisterCount: 1, Write: &runtimeconfig.WriteDefinition{Mode: "set"},
	}}}
}

func equalWords(left, right []uint16) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func waitResult(t *testing.T, reply <-chan Result) Result {
	t.Helper()
	select {
	case result := <-reply:
		return result
	case <-time.After(time.Second):
		t.Fatal("PLC command did not return")
		return Result{}
	}
}

func waitDone(t *testing.T, worker *Worker) {
	t.Helper()
	select {
	case <-worker.Done():
	case <-time.After(time.Second):
		t.Fatal("PLC worker did not stop")
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

type readCall struct {
	address  uint16
	quantity uint16
}

type writeCall struct {
	word  uint16
	bit   uint8
	value bool
}

type registerWriteCall struct {
	method  string
	address uint16
	values  []uint16
}

type fakeAdapter struct {
	mu               sync.Mutex
	registers        map[uint16]uint16
	reads            []readCall
	writes           []writeCall
	registersWritten []registerWriteCall
	readErr          error
	writeErr         error
	afterWrite       func(writeCall)
}

func newFakeAdapter(word504 uint16) *fakeAdapter {
	return &fakeAdapter{registers: map[uint16]uint16{504: word504}}
}

func (f *fakeAdapter) ReadHoldingRegisters(_ context.Context, address, quantity uint16) ([]uint16, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, readCall{address: address, quantity: quantity})
	if f.readErr != nil {
		return nil, f.readErr
	}
	values := make([]uint16, quantity)
	for index := range values {
		values[index] = f.registers[address+uint16(index)]
	}
	return values, nil
}

func (f *fakeAdapter) MaskWriteBit(_ context.Context, word uint16, bit uint8, value bool) error {
	f.mu.Lock()
	call := writeCall{word: word, bit: bit, value: value}
	f.writes = append(f.writes, call)
	if f.writeErr == nil {
		mask := uint16(1) << bit
		if value {
			f.registers[word] |= mask
		} else {
			f.registers[word] &^= mask
		}
	}
	afterWrite := f.afterWrite
	err := f.writeErr
	f.mu.Unlock()
	if afterWrite != nil {
		afterWrite(call)
	}
	return err
}

func (f *fakeAdapter) WriteSingleRegister(_ context.Context, address uint16, value uint16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := registerWriteCall{method: "fc06", address: address, values: []uint16{value}}
	f.registersWritten = append(f.registersWritten, call)
	if f.writeErr == nil {
		f.registers[address] = value
	}
	return f.writeErr
}

func (f *fakeAdapter) WriteMultipleRegisters(_ context.Context, address uint16, values []uint16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	copyValues := append([]uint16(nil), values...)
	f.registersWritten = append(f.registersWritten, registerWriteCall{method: "fc10", address: address, values: copyValues})
	if f.writeErr == nil {
		for index, value := range values {
			f.registers[address+uint16(index)] = value
		}
	}
	return f.writeErr
}

func (f *fakeAdapter) Close() {}

func (f *fakeAdapter) readCalls() []readCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]readCall(nil), f.reads...)
}

func (f *fakeAdapter) writeCalls() []writeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]writeCall(nil), f.writes...)
}

func (f *fakeAdapter) registerWriteCalls() []registerWriteCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]registerWriteCall, len(f.registersWritten))
	for index, call := range f.registersWritten {
		result[index] = registerWriteCall{method: call.method, address: call.address, values: append([]uint16(nil), call.values...)}
	}
	return result
}

func (f *fakeAdapter) word(address uint16) uint16 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.registers[address]
}
