package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestFC03ReadsHoldingRegisters(t *testing.T) {
	address, stop := startTestServer(t, map[uint16]uint16{504: 0x1234, 505: 0xABCD})
	defer stop()

	response := exchange(t, address, 7, 1, []byte{functionReadHoldingRegisters, 0x01, 0xF8, 0x00, 0x02})
	if got, want := response, []byte{functionReadHoldingRegisters, 0x04, 0x12, 0x34, 0xAB, 0xCD}; !bytes.Equal(got, want) {
		t.Fatalf("FC03 response = % X, want % X", got, want)
	}
}

func TestFC22SetsAndClearsD504BitsWithoutChangingNeighbors(t *testing.T) {
	address, stop := startTestServer(t, map[uint16]uint16{504: 0xA001})
	defer stop()

	// D504.1 is bit 1 and D504.2 is bit 2. Bit 0 and the high bits are neighbors.
	setD504Bit1 := []byte{functionMaskWriteRegister, 0x01, 0xF8, 0xFF, 0xFD, 0x00, 0x02}
	if response := exchange(t, address, 1, 1, setD504Bit1); !bytes.Equal(response, setD504Bit1) {
		t.Fatalf("FC22 set D504.1 response = % X, want request echo % X", response, setD504Bit1)
	}
	setD504Bit2 := []byte{functionMaskWriteRegister, 0x01, 0xF8, 0xFF, 0xFB, 0x00, 0x04}
	if response := exchange(t, address, 2, 1, setD504Bit2); !bytes.Equal(response, setD504Bit2) {
		t.Fatalf("FC22 set D504.2 response = % X, want request echo % X", response, setD504Bit2)
	}
	clearD504Bit1 := []byte{functionMaskWriteRegister, 0x01, 0xF8, 0xFF, 0xFD, 0x00, 0x00}
	if response := exchange(t, address, 3, 1, clearD504Bit1); !bytes.Equal(response, clearD504Bit1) {
		t.Fatalf("FC22 clear D504.1 response = % X, want request echo % X", response, clearD504Bit1)
	}
	clearD504Bit2 := []byte{functionMaskWriteRegister, 0x01, 0xF8, 0xFF, 0xFB, 0x00, 0x00}
	if response := exchange(t, address, 4, 1, clearD504Bit2); !bytes.Equal(response, clearD504Bit2) {
		t.Fatalf("FC22 clear D504.2 response = % X, want request echo % X", response, clearD504Bit2)
	}

	response := exchange(t, address, 5, 1, []byte{functionReadHoldingRegisters, 0x01, 0xF8, 0x00, 0x01})
	if got, want := response, []byte{functionReadHoldingRegisters, 0x02, 0xA0, 0x01}; !bytes.Equal(got, want) {
		t.Fatalf("D504 after FC22 sequence = % X, want neighbors preserved as % X", got, want)
	}
}

func TestFC06FC16AndUnknownFunctionReturnIllegalFunction(t *testing.T) {
	address, stop := startTestServer(t, nil)
	defer stop()

	tests := []struct {
		name     string
		function byte
		request  []byte
	}{
		{name: "FC06", function: 0x06, request: []byte{0x06, 0x01, 0xF8, 0x00, 0x01}},
		{name: "FC16", function: 0x10, request: []byte{0x10, 0x01, 0xF8, 0x00, 0x01, 0x02, 0x00, 0x01}},
		{name: "unknown", function: 0x45, request: []byte{0x45}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := exchange(t, address, uint16(index+1), 1, test.request)
			want := []byte{test.function | 0x80, exceptionIllegalFunction}
			if !bytes.Equal(response, want) {
				t.Fatalf("%s response = % X, want % X", test.name, response, want)
			}
		})
	}
}

func TestConcurrentConnectionsKeepAllMaskWrites(t *testing.T) {
	address, stop := startTestServer(t, nil)
	defer stop()

	var workers sync.WaitGroup
	for bit := 0; bit < 16; bit++ {
		workers.Add(1)
		go func(bit int) {
			defer workers.Done()
			connection, err := net.Dial("tcp", address)
			if err != nil {
				t.Errorf("dial bit %d: %v", bit, err)
				return
			}
			defer connection.Close()
			mask := uint16(1 << bit)
			request := make([]byte, 7)
			request[0] = functionMaskWriteRegister
			binary.BigEndian.PutUint16(request[1:3], 504)
			binary.BigEndian.PutUint16(request[3:5], ^mask)
			binary.BigEndian.PutUint16(request[5:7], mask)
			for round := 0; round < 10; round++ {
				if response := exchangeOnConnection(t, connection, uint16(bit*10+round+1), 1, request); !bytes.Equal(response, request) {
					t.Errorf("bit %d response = % X, want % X", bit, response, request)
					return
				}
			}
		}(bit)
	}
	workers.Wait()

	response := exchange(t, address, 999, 1, []byte{functionReadHoldingRegisters, 0x01, 0xF8, 0x00, 0x01})
	if got, want := response, []byte{functionReadHoldingRegisters, 0x02, 0xFF, 0xFF}; !bytes.Equal(got, want) {
		t.Fatalf("concurrent FC22 writes produced % X, want % X", got, want)
	}
}

func TestJSONLTraceContainsRequestFields(t *testing.T) {
	var trace bytes.Buffer
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := NewServer(1, map[uint16]uint16{504: 1}, &trace)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	response := exchange(t, listener.Addr().String(), 1, 1, []byte{functionReadHoldingRegisters, 0x01, 0xF8, 0x00, 0x01})
	if !bytes.Equal(response, []byte{functionReadHoldingRegisters, 0x02, 0x00, 0x01}) {
		t.Fatalf("FC03 response = % X", response)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var entry requestTrace
	if err := json.Unmarshal(bytes.TrimSpace(trace.Bytes()), &entry); err != nil {
		t.Fatalf("trace is not one JSON line: %q: %v", trace.String(), err)
	}
	if entry.Time == "" || entry.Peer == "" || entry.Transaction != 1 || entry.Unit != 1 || entry.Function != functionReadHoldingRegisters || entry.Address == nil || *entry.Address != 504 || entry.Masks != nil || entry.Result != "ok" {
		t.Fatalf("unexpected trace: %+v", entry)
	}
}

func startTestServer(t *testing.T, initial map[uint16]uint16) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := NewServer(1, initial, io.Discard)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	return listener.Addr().String(), func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func exchange(t *testing.T, address string, transaction uint16, unit byte, pdu []byte) []byte {
	t.Helper()
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	return exchangeOnConnection(t, connection, transaction, unit, pdu)
}

func exchangeOnConnection(t *testing.T, connection net.Conn, transaction uint16, unit byte, pdu []byte) []byte {
	t.Helper()
	if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	request := make([]byte, 7+len(pdu))
	binary.BigEndian.PutUint16(request[0:2], transaction)
	binary.BigEndian.PutUint16(request[2:4], 0)
	binary.BigEndian.PutUint16(request[4:6], uint16(1+len(pdu)))
	request[6] = unit
	copy(request[7:], pdu)
	if _, err := connection.Write(request); err != nil {
		t.Fatal(err)
	}
	var header [6]byte
	if _, err := io.ReadFull(connection, header[:]); err != nil {
		t.Fatal(err)
	}
	if got := binary.BigEndian.Uint16(header[0:2]); got != transaction {
		t.Fatalf("transaction = %d, want %d", got, transaction)
	}
	if got := binary.BigEndian.Uint16(header[2:4]); got != 0 {
		t.Fatalf("protocol ID = %d, want 0", got)
	}
	length := binary.BigEndian.Uint16(header[4:6])
	body := make([]byte, length)
	if _, err := io.ReadFull(connection, body); err != nil {
		t.Fatal(err)
	}
	if len(body) < 2 || body[0] != unit {
		t.Fatalf("invalid response unit/body: % X", body)
	}
	return body[1:]
}
