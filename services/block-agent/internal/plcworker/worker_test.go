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

const pollTimingTolerance = 50 * time.Millisecond

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

func TestPreCancelledContextSkipsInitialPoll(t *testing.T) {
	adapter := newFakeAdapter(0)
	worker := newWorker(t, testConfig("pulse", runtimeconfig.DefaultPulseMs), adapter, func(map[string]pointstore.PointValue) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	worker.Run(ctx)
	if reads := adapter.readCalls(); len(reads) != 0 {
		t.Fatalf("pre-cancelled worker made FC03 reads: %#v", reads)
	}
	if err := <-worker.Ready(); err != context.Canceled {
		t.Fatalf("ready error = %v, want %v", err, context.Canceled)
	}
	select {
	case <-worker.Done():
	default:
		t.Fatal("pre-cancelled worker did not stop")
	}
}

func TestInitialPollIsImmediateAndSlowPollsWaitForCompletion(t *testing.T) {
	adapter := newFakeAdapter(0)
	adapter.readDelay = PollInterval + pollTimingTolerance
	readStarted := make(chan struct{}, 2)
	adapter.beforeRead = func(readCall) {
		readStarted <- struct{}{}
	}
	worker := newWorker(t, testConfig("pulse", runtimeconfig.DefaultPulseMs), adapter, func(map[string]pointstore.PointValue) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		waitDone(t, worker)
	}()

	go worker.Run(ctx)
	select {
	case <-readStarted:
	case <-time.After(PollInterval - 2*pollTimingTolerance):
		t.Fatal("initial poll did not start before the first scheduled interval")
	}

	select {
	case <-readStarted:
	case <-time.After(2*PollInterval + time.Second):
		t.Fatal("second poll did not start")
	}
	events := adapter.readEvents()
	if gap := events[1].startedAt.Sub(events[0].completedAt); gap < PollInterval-pollTimingTolerance {
		t.Fatalf("next poll began %s after the slow poll completed, want at least %s", gap, PollInterval-pollTimingTolerance)
	}
	if active := adapter.maxConcurrentReads(); active != 1 {
		t.Fatalf("maximum concurrent reads = %d, want 1", active)
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

func TestFloat32HighLowCompatibilityProfileReadsAndWritesFC10(t *testing.T) {
	adapter := newFakeAdapter(0)
	adapter.registers[820] = 0x4148 // float32 12.5, high-low word order.
	adapter.registers[821] = 0x0000
	published := make(chan map[string]pointstore.PointValue, 4)
	worker := newWorker(t, float32HighLowCompatibilityConfig(), adapter, func(values map[string]pointstore.PointValue) error {
		published <- values
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	initial := <-published
	if value, ok := initial["test.float32.high-low"]; !ok || value.Value != float64(12.5) {
		t.Fatalf("initial high-low float32 value = %#v", initial)
	}
	reads := adapter.readCalls()
	if len(reads) == 0 || reads[0].address != 820 || reads[0].quantity != 2 {
		t.Fatalf("high-low float32 FC03 span = %#v, want D820-D821", reads)
	}

	reply, rejected, accepted := worker.TrySubmit(Command{PointID: "test.float32.high-low", Action: "set", Value: float64(8.25)})
	if !accepted {
		t.Fatalf("numeric set was rejected: %+v", rejected)
	}
	if result := waitResult(t, reply); !result.Success || result.ActualValue != float64(8.25) {
		t.Fatalf("numeric result = %+v", result)
	}
	writes := adapter.registerWriteCalls()
	wantBits := math.Float32bits(8.25)
	wantWords := []uint16{uint16(wantBits >> 16), uint16(wantBits)}
	if len(writes) != 1 || writes[0].method != "fc10" || writes[0].address != 820 || !equalWords(writes[0].values, wantWords) {
		t.Fatalf("high-low FC10 numeric write = %#v, want %#v", writes, wantWords)
	}
}

func TestProductionDINTLowHighReadsAndWritesFC10(t *testing.T) {
	adapter := newFakeAdapter(0)
	adapter.registers[902], adapter.registers[903] = 0x614E, 0x00BC   // 12345678, low-high word order.
	adapter.registers[904], adapter.registers[905] = 0xFF9C, 0xFFFF   // -100, low-high word order.
	adapter.registers[1000], adapter.registers[1001] = 0x0091, 0x0000 // 145, low-high word order.
	published := make(chan map[string]pointstore.PointValue, 4)
	worker := newWorker(t, productionDINTConfig(), adapter, func(values map[string]pointstore.PointValue) error {
		published <- values
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	initial := <-published
	if initial["production.output.today"].Value != int32(12345678) || initial["production.quality.passed"].Value != int32(-100) || initial["maintenance.production.target"].Value != int32(145) {
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
	wantWords := []uint16{0x32EB, 0xF8A4}
	if len(writes) != 1 || writes[0].method != "fc10" || writes[0].address != 1000 || !equalWords(writes[0].values, wantWords) {
		t.Fatalf("DINT FC10 write = %#v, want %#v", writes, wantWords)
	}
}

func TestInt32HighLowCompatibilityProfileReadsAndWritesFC10(t *testing.T) {
	adapter := newFakeAdapter(0)
	adapter.registers[1010], adapter.registers[1011] = 0x0000, 0x0091 // 145, high-low word order.
	published := make(chan map[string]pointstore.PointValue, 4)
	worker := newWorker(t, int32HighLowCompatibilityConfig(), adapter, func(values map[string]pointstore.PointValue) error {
		published <- values
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	initial := <-published
	if value, ok := initial["test.int32.high-low"]; !ok || value.Value != int32(145) {
		t.Fatalf("initial high-low int32 value = %#v", initial)
	}

	reply, rejected, accepted := worker.TrySubmit(Command{PointID: "test.int32.high-low", Action: "set", Value: float64(-123456789)})
	if !accepted {
		t.Fatalf("high-low int32 set was rejected: %+v", rejected)
	}
	if result := waitResult(t, reply); !result.Success || result.ActualValue != int32(-123456789) {
		t.Fatalf("high-low int32 result = %+v", result)
	}
	writes := adapter.registerWriteCalls()
	wantWords := []uint16{0xF8A4, 0x32EB}
	if len(writes) != 1 || writes[0].method != "fc10" || writes[0].address != 1010 || !equalWords(writes[0].values, wantWords) {
		t.Fatalf("high-low int32 FC10 write = %#v, want %#v", writes, wantWords)
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

func TestPollIntervalRemainsFiveHundredMilliseconds(t *testing.T) {
	if PollInterval != 500*time.Millisecond {
		t.Fatalf("PollInterval = %s, want 500ms", PollInterval)
	}
}

func TestPulseUsesFC22SetWaitClearThenFreshRead(t *testing.T) {
	adapter := newFakeAdapter(0)
	published := make(chan map[string]pointstore.PointValue, 8)
	worker := newWorker(t, testConfig("pulse", runtimeconfig.DefaultPulseMs), adapter, func(values map[string]pointstore.PointValue) error {
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
	if elapsed := writes[1].at.Sub(writes[0].at); elapsed < time.Duration(runtimeconfig.DefaultPulseMs)*time.Millisecond-pollTimingTolerance {
		t.Fatalf("pulse stayed active for %s, want at least %dms", elapsed, runtimeconfig.DefaultPulseMs)
	}
	reads := adapter.readEvents()
	if len(reads) < 2 || reads[1].startedAt.Before(writes[1].at) {
		t.Fatalf("pulse confirmation read = %#v, want a full read after reset", reads)
	}
	if adapter.word(504)&0x0002 != 0 {
		t.Fatalf("pulse did not clear D504.1: %#x", adapter.word(504))
	}
}

func TestSuccessfulCommandConfirmationPrecedesReplyAndRestartsPollTimer(t *testing.T) {
	adapter := newFakeAdapter(0)
	confirmationStarted := make(chan struct{})
	releaseConfirmation := make(chan struct{})

	var readCountMu sync.Mutex
	readCount := 0
	adapter.beforeRead = func(readCall) {
		readCountMu.Lock()
		readCount++
		count := readCount
		readCountMu.Unlock()
		if count == 2 {
			close(confirmationStarted)
			<-releaseConfirmation
		}
	}

	worker := newWorker(t, testConfig("set", runtimeconfig.DefaultPulseMs), adapter, func(map[string]pointstore.PointValue) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		select {
		case <-releaseConfirmation:
		default:
			close(releaseConfirmation)
		}
		cancel()
		waitDone(t, worker)
	}()
	go worker.Run(ctx)
	waitFor(t, time.Second, func() bool { return len(adapter.readCalls()) == 1 })

	reply, rejected, accepted := worker.TrySubmit(Command{PointID: "command", Action: "set", Value: true})
	if !accepted {
		t.Fatalf("set was rejected: %+v", rejected)
	}
	select {
	case <-confirmationStarted:
	case <-time.After(time.Second):
		t.Fatal("successful set did not start its confirmation read")
	}
	select {
	case result := <-reply:
		t.Fatalf("set replied before confirmation read completed: %+v", result)
	case <-time.After(pollTimingTolerance):
	}

	close(releaseConfirmation)
	result := waitResult(t, reply)
	if !result.Success || result.ActualValue != true {
		t.Fatalf("set result = %+v", result)
	}

	select {
	case <-time.After(PollInterval / 2):
	}
	if reads := adapter.readCalls(); len(reads) != 2 {
		t.Fatalf("reads after command confirmation = %#v, want no extra immediate poll", reads)
	}

	waitFor(t, PollInterval+time.Second, func() bool { return len(adapter.readCalls()) >= 3 })
	events := adapter.readEvents()
	if gap := events[2].startedAt.Sub(events[1].completedAt); gap < PollInterval-pollTimingTolerance {
		t.Fatalf("ordinary poll began %s after command confirmation, want at least %s", gap, PollInterval-pollTimingTolerance)
	}
}

func TestFailedWriteRefreshesBeforeReplyAndRestartsPollTimer(t *testing.T) {
	adapter := newFakeAdapter(0)
	adapter.writeErr = context.DeadlineExceeded
	freshReadStarted := make(chan struct{})
	releaseFreshRead := make(chan struct{})

	var readCountMu sync.Mutex
	readCount := 0
	adapter.beforeRead = func(readCall) {
		readCountMu.Lock()
		readCount++
		count := readCount
		readCountMu.Unlock()
		if count == 2 {
			close(freshReadStarted)
			<-releaseFreshRead
		}
	}

	worker := newWorker(t, testConfig("set", runtimeconfig.DefaultPulseMs), adapter, func(map[string]pointstore.PointValue) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		select {
		case <-releaseFreshRead:
		default:
			close(releaseFreshRead)
		}
		cancel()
		waitDone(t, worker)
	}()
	go worker.Run(ctx)
	select {
	case err := <-worker.Ready():
		if err != nil {
			t.Fatalf("initial read failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("initial read did not finish")
	}

	reply, rejected, accepted := worker.TrySubmit(Command{PointID: "command", Action: "set", Value: true})
	if !accepted {
		t.Fatalf("set was rejected: %+v", rejected)
	}
	select {
	case <-freshReadStarted:
	case <-time.After(PollInterval - pollTimingTolerance):
		t.Fatal("failed write did not start its immediate fresh read")
	}
	select {
	case result := <-reply:
		t.Fatalf("failed write replied before fresh read completed: %+v", result)
	case <-time.After(pollTimingTolerance):
	}
	if writes := adapter.writeCalls(); len(writes) != 1 {
		t.Fatalf("failed write retried %d times", len(writes))
	}

	close(releaseFreshRead)
	result := waitResult(t, reply)
	if result.Success || result.Code != CodePLCWriteFailed {
		t.Fatalf("write failure result = %+v", result)
	}

	select {
	case <-time.After(PollInterval / 2):
	}
	if reads := adapter.readCalls(); len(reads) != 2 {
		t.Fatalf("reads after failed-write refresh = %#v, want no duplicate immediate poll", reads)
	}

	waitFor(t, PollInterval+time.Second, func() bool { return len(adapter.readCalls()) >= 3 })
	events := adapter.readEvents()
	if gap := events[2].startedAt.Sub(events[1].completedAt); gap < PollInterval-pollTimingTolerance {
		t.Fatalf("ordinary poll began %s after failed-write refresh, want at least %s", gap, PollInterval-pollTimingTolerance)
	}
}

func TestRejectedCommandsDoNotRefresh(t *testing.T) {
	adapter := newFakeAdapter(0)
	adapter.registers[820] = 7
	worker := newWorker(t, uint16Config(), adapter, func(map[string]pointstore.PointValue) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		waitDone(t, worker)
	}()
	go worker.Run(ctx)
	select {
	case err := <-worker.Ready():
		if err != nil {
			t.Fatalf("initial read failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("initial read did not finish")
	}

	missingReply, rejected, accepted := worker.TrySubmit(Command{PointID: "missing", Action: "set", Value: 1})
	if !accepted {
		t.Fatalf("missing point command was rejected: %+v", rejected)
	}
	missingResult := waitResult(t, missingReply)
	if missingResult.Success || missingResult.Code != CodePointNotFound {
		t.Fatalf("missing point result = %+v", missingResult)
	}
	if reads := adapter.readCalls(); len(reads) != 1 {
		t.Fatalf("invalid command triggered an extra poll: %#v", reads)
	}

	reply, rejected, accepted := worker.TrySubmit(Command{PointID: "manual.test.uint16", Action: "set", Value: 1.5})
	if !accepted {
		t.Fatalf("invalid numeric set was rejected: %+v", rejected)
	}
	result := waitResult(t, reply)
	if result.Success || result.Code != CodeInvalidRequest {
		t.Fatalf("numeric validation result = %+v", result)
	}
	if writes := adapter.registerWriteCalls(); len(writes) != 0 {
		t.Fatalf("invalid numeric set reached PLC: %#v", writes)
	}

	select {
	case <-time.After(PollInterval / 2):
	}
	if reads := adapter.readCalls(); len(reads) != 1 {
		t.Fatalf("numeric validation failure triggered an extra poll: %#v", reads)
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
	return runtimeconfig.Config{ScanIntervalMs: runtimeconfig.RequiredScanIntervalMs, Points: []runtimeconfig.PointDefinition{
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

func float32HighLowCompatibilityConfig() runtimeconfig.Config {
	return runtimeconfig.Config{ScanIntervalMs: runtimeconfig.RequiredScanIntervalMs, Points: []runtimeconfig.PointDefinition{{
		PointID: "test.float32.high-low", Address: "D820", Type: "float32", Access: "read_write",
		ReadPoint: "test.float32.high-low", WritePoint: "test.float32.high-low", WriteMethod: "fc10",
		RegisterCount: 2, WordOrder: "high-low", Write: &runtimeconfig.WriteDefinition{Mode: "set"},
	}}}
}

func productionDINTConfig() runtimeconfig.Config {
	return runtimeconfig.Config{ScanIntervalMs: runtimeconfig.RequiredScanIntervalMs, Points: []runtimeconfig.PointDefinition{
		{
			PointID: "maintenance.production.target", Address: "D1000", Type: "int32", Access: "read_write",
			ReadPoint: "maintenance.production.target", WritePoint: "maintenance.production.target", WriteMethod: "fc10",
			RegisterCount: 2, WordOrder: "low-high", Write: &runtimeconfig.WriteDefinition{Mode: "set"},
		},
		{PointID: "production.output.today", Address: "D902", Type: "int32", Access: "read", ReadPoint: "production.output.today", RegisterCount: 2, WordOrder: "low-high"},
		{PointID: "production.quality.passed", Address: "D904", Type: "int32", Access: "read", ReadPoint: "production.quality.passed", RegisterCount: 2, WordOrder: "low-high"},
	}}
}

func int32HighLowCompatibilityConfig() runtimeconfig.Config {
	return runtimeconfig.Config{ScanIntervalMs: runtimeconfig.RequiredScanIntervalMs, Points: []runtimeconfig.PointDefinition{{
		PointID: "test.int32.high-low", Address: "D1010", Type: "int32", Access: "read_write",
		ReadPoint: "test.int32.high-low", WritePoint: "test.int32.high-low", WriteMethod: "fc10",
		RegisterCount: 2, WordOrder: "high-low", Write: &runtimeconfig.WriteDefinition{Mode: "set"},
	}}}
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

type readEvent struct {
	call        readCall
	startedAt   time.Time
	completedAt time.Time
}

type writeCall struct {
	word  uint16
	bit   uint8
	value bool
	at    time.Time
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
	readDelay        time.Duration
	beforeRead       func(readCall)
	afterWrite       func(writeCall)
	readTimeline     []readEvent
	activeReads      int
	maxActiveReads   int
}

func newFakeAdapter(word504 uint16) *fakeAdapter {
	return &fakeAdapter{registers: map[uint16]uint16{504: word504}}
}

func (f *fakeAdapter) ReadHoldingRegisters(_ context.Context, address, quantity uint16) ([]uint16, error) {
	f.mu.Lock()
	call := readCall{address: address, quantity: quantity}
	f.reads = append(f.reads, call)
	eventIndex := len(f.readTimeline)
	f.readTimeline = append(f.readTimeline, readEvent{call: call, startedAt: time.Now()})
	f.activeReads++
	if f.activeReads > f.maxActiveReads {
		f.maxActiveReads = f.activeReads
	}
	beforeRead := f.beforeRead
	delay := f.readDelay
	f.mu.Unlock()

	if beforeRead != nil {
		beforeRead(call)
	}
	if delay > 0 {
		time.Sleep(delay)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.activeReads--
	f.readTimeline[eventIndex].completedAt = time.Now()
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
	call := writeCall{word: word, bit: bit, value: value, at: time.Now()}
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

func (f *fakeAdapter) readEvents() []readEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]readEvent(nil), f.readTimeline...)
}

func (f *fakeAdapter) maxConcurrentReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActiveReads
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
