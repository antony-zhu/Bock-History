package plcworker

import (
	"context"
	"errors"
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

type fakeAdapter struct {
	mu         sync.Mutex
	registers  map[uint16]uint16
	reads      []readCall
	writes     []writeCall
	readErr    error
	writeErr   error
	afterWrite func(writeCall)
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

func (f *fakeAdapter) word(address uint16) uint16 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.registers[address]
}
