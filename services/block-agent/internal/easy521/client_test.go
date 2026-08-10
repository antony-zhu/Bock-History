package easy521

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{Endpoint: "127.0.0.1:502", UnitID: 1, ConnectTimeout: time.Second, RequestTimeout: time.Second}
}

func TestReadHoldingRegistersUsesFC03(t *testing.T) {
	clientConnection, serverConnection := net.Pipe()
	defer serverConnection.Close()
	client, err := NewWithDial(testConfig(), func(context.Context, string, string) (net.Conn, error) { return clientConnection, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		frame, err := readFrame(serverConnection)
		if err != nil {
			done <- err
			return
		}
		if frame[7] != functionReadHoldingRegisters || binary.BigEndian.Uint16(frame[8:10]) != 504 || binary.BigEndian.Uint16(frame[10:12]) != 1 {
			done <- io.ErrUnexpectedEOF
			return
		}
		done <- writeResponse(serverConnection, frame, []byte{functionReadHoldingRegisters, 2, 0xA5, 0xA5})
	}()

	values, err := client.ReadHoldingRegisters(context.Background(), 504, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0] != 0xA5A5 {
		t.Fatalf("values = %#v", values)
	}
}

func TestMaskWriteBitUsesFC22AndPreservesNeighborBits(t *testing.T) {
	clientConnection, serverConnection := net.Pipe()
	defer serverConnection.Close()
	client, err := NewWithDial(testConfig(), func(context.Context, string, string) (net.Conn, error) { return clientConnection, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	word := uint16(0xA5A5)
	done := make(chan error, 1)
	go func() {
		for range 2 {
			frame, err := readFrame(serverConnection)
			if err != nil {
				done <- err
				return
			}
			if frame[7] != functionMaskWriteRegister || binary.BigEndian.Uint16(frame[8:10]) != 504 || binary.BigEndian.Uint16(frame[10:12]) != 0xFFFD {
				done <- io.ErrUnexpectedEOF
				return
			}
			andMask := binary.BigEndian.Uint16(frame[10:12])
			orMask := binary.BigEndian.Uint16(frame[12:14])
			word = (word & andMask) | (orMask & ^andMask)
			if err := writeResponse(serverConnection, frame, frame[7:]); err != nil {
				done <- err
				return
			}
		}
		if word != 0xA5A5 {
			done <- io.ErrUnexpectedEOF
			return
		}
		done <- nil
	}()

	if err := client.MaskWriteBit(context.Background(), 504, 1, true); err != nil {
		t.Fatal(err)
	}
	if err := client.MaskWriteBit(context.Background(), 504, 1, false); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWriteSingleRegisterUsesFC06(t *testing.T) {
	clientConnection, serverConnection := net.Pipe()
	defer serverConnection.Close()
	client, err := NewWithDial(testConfig(), func(context.Context, string, string) (net.Conn, error) { return clientConnection, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		frame, err := readFrame(serverConnection)
		if err != nil {
			done <- err
			return
		}
		if frame[7] != functionWriteSingleRegister || binary.BigEndian.Uint16(frame[8:10]) != 820 || binary.BigEndian.Uint16(frame[10:12]) != 9 {
			done <- io.ErrUnexpectedEOF
			return
		}
		done <- writeResponse(serverConnection, frame, frame[7:])
	}()
	if err := client.WriteSingleRegister(context.Background(), 820, 9); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWriteMultipleRegistersUsesFC10(t *testing.T) {
	clientConnection, serverConnection := net.Pipe()
	defer serverConnection.Close()
	client, err := NewWithDial(testConfig(), func(context.Context, string, string) (net.Conn, error) { return clientConnection, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		frame, err := readFrame(serverConnection)
		if err != nil {
			done <- err
			return
		}
		if frame[7] != functionWriteMultipleRegs || binary.BigEndian.Uint16(frame[8:10]) != 800 ||
			binary.BigEndian.Uint16(frame[10:12]) != 2 || frame[12] != 4 ||
			binary.BigEndian.Uint16(frame[13:15]) != 0x0000 || binary.BigEndian.Uint16(frame[15:17]) != 0x4104 {
			done <- io.ErrUnexpectedEOF
			return
		}
		response := append([]byte{functionWriteMultipleRegs}, frame[8:12]...)
		done <- writeResponse(serverConnection, frame, response)
	}()
	if err := client.WriteMultipleRegisters(context.Background(), 800, []uint16{0x0000, 0x4104}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMaskWriteBitFallsBackFromFC22IllegalFunctionAndCachesCapability(t *testing.T) {
	unsupportedClientConnection, unsupportedServerConnection := net.Pipe()
	fallbackClientConnection, fallbackServerConnection := net.Pipe()
	defer unsupportedServerConnection.Close()
	defer fallbackServerConnection.Close()
	dials := 0
	client, err := NewWithDial(testConfig(), func(context.Context, string, string) (net.Conn, error) {
		switch dials {
		case 0:
			dials++
			return unsupportedClientConnection, nil
		case 1:
			dials++
			return fallbackClientConnection, nil
		default:
			return nil, errors.New("unexpected PLC dial")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	unsupportedDone := make(chan error, 1)
	go func() {
		frame, err := readFrame(unsupportedServerConnection)
		if err != nil {
			unsupportedDone <- err
			return
		}
		if frame[7] != functionMaskWriteRegister || binary.BigEndian.Uint16(frame[8:10]) != 504 ||
			binary.BigEndian.Uint16(frame[10:12]) != 0xEFFF || binary.BigEndian.Uint16(frame[12:14]) != 0x1000 {
			unsupportedDone <- io.ErrUnexpectedEOF
			return
		}
		unsupportedDone <- writeResponse(unsupportedServerConnection, frame, []byte{functionMaskWriteRegister | 0x80, exceptionIllegalFunction})
	}()

	fallbackDone := make(chan error, 1)
	go func() {
		frame, err := readFrame(fallbackServerConnection)
		if err != nil {
			fallbackDone <- err
			return
		}
		if frame[7] != functionReadHoldingRegisters || binary.BigEndian.Uint16(frame[8:10]) != 504 || binary.BigEndian.Uint16(frame[10:12]) != 1 {
			fallbackDone <- io.ErrUnexpectedEOF
			return
		}
		if err := writeResponse(fallbackServerConnection, frame, []byte{functionReadHoldingRegisters, 2, 0x00, 0x03}); err != nil {
			fallbackDone <- err
			return
		}

		frame, err = readFrame(fallbackServerConnection)
		if err != nil {
			fallbackDone <- err
			return
		}
		if frame[7] != functionWriteSingleRegister || binary.BigEndian.Uint16(frame[8:10]) != 504 || binary.BigEndian.Uint16(frame[10:12]) != 0x1003 {
			fallbackDone <- io.ErrUnexpectedEOF
			return
		}
		if err := writeResponse(fallbackServerConnection, frame, frame[7:]); err != nil {
			fallbackDone <- err
			return
		}

		frame, err = readFrame(fallbackServerConnection)
		if err != nil {
			fallbackDone <- err
			return
		}
		if frame[7] != functionReadHoldingRegisters || binary.BigEndian.Uint16(frame[8:10]) != 504 || binary.BigEndian.Uint16(frame[10:12]) != 1 {
			fallbackDone <- io.ErrUnexpectedEOF
			return
		}
		if err := writeResponse(fallbackServerConnection, frame, []byte{functionReadHoldingRegisters, 2, 0x10, 0x23}); err != nil {
			fallbackDone <- err
			return
		}

		frame, err = readFrame(fallbackServerConnection)
		if err != nil {
			fallbackDone <- err
			return
		}
		if frame[7] != functionWriteSingleRegister || binary.BigEndian.Uint16(frame[8:10]) != 504 || binary.BigEndian.Uint16(frame[10:12]) != 0x0023 {
			fallbackDone <- io.ErrUnexpectedEOF
			return
		}
		fallbackDone <- writeResponse(fallbackServerConnection, frame, frame[7:])
	}()
	if err := client.MaskWriteBit(context.Background(), 504, 12, true); err != nil {
		t.Fatal(err)
	}
	if err := client.MaskWriteBit(context.Background(), 504, 12, false); err != nil {
		t.Fatal(err)
	}
	if err := <-unsupportedDone; err != nil {
		t.Fatal(err)
	}
	if err := <-fallbackDone; err != nil {
		t.Fatal(err)
	}
	if dials != 2 {
		t.Fatalf("dials = %d, want 2", dials)
	}
}

func TestWriteMultipleRegistersFallsBackFromFC10IllegalFunctionAndCachesCapability(t *testing.T) {
	unsupportedClientConnection, unsupportedServerConnection := net.Pipe()
	fallbackClientConnection, fallbackServerConnection := net.Pipe()
	defer unsupportedServerConnection.Close()
	defer fallbackServerConnection.Close()
	dials := 0
	client, err := NewWithDial(testConfig(), func(context.Context, string, string) (net.Conn, error) {
		switch dials {
		case 0:
			dials++
			return unsupportedClientConnection, nil
		case 1:
			dials++
			return fallbackClientConnection, nil
		default:
			return nil, errors.New("unexpected PLC dial")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	unsupportedDone := make(chan error, 1)
	go func() {
		frame, err := readFrame(unsupportedServerConnection)
		if err != nil {
			unsupportedDone <- err
			return
		}
		if frame[7] != functionWriteMultipleRegs || binary.BigEndian.Uint16(frame[8:10]) != 1000 ||
			binary.BigEndian.Uint16(frame[10:12]) != 2 || frame[12] != 4 ||
			binary.BigEndian.Uint16(frame[13:15]) != 0x0000 || binary.BigEndian.Uint16(frame[15:17]) != 0x4104 {
			unsupportedDone <- io.ErrUnexpectedEOF
			return
		}
		unsupportedDone <- writeResponse(unsupportedServerConnection, frame, []byte{functionWriteMultipleRegs | 0x80, exceptionIllegalFunction})
	}()

	fallbackDone := make(chan error, 1)
	go func() {
		for index, expected := range []uint16{0x0000, 0x4104, 0x0012, 0x3456} {
			frame, err := readFrame(fallbackServerConnection)
			if err != nil {
				fallbackDone <- err
				return
			}
			if frame[7] != functionWriteSingleRegister || binary.BigEndian.Uint16(frame[8:10]) != uint16(1000+index%2) || binary.BigEndian.Uint16(frame[10:12]) != expected {
				fallbackDone <- io.ErrUnexpectedEOF
				return
			}
			if err := writeResponse(fallbackServerConnection, frame, frame[7:]); err != nil {
				fallbackDone <- err
				return
			}
		}
		fallbackDone <- nil
	}()
	if err := client.WriteMultipleRegisters(context.Background(), 1000, []uint16{0x0000, 0x4104}); err != nil {
		t.Fatal(err)
	}
	if err := client.WriteMultipleRegisters(context.Background(), 1000, []uint16{0x0012, 0x3456}); err != nil {
		t.Fatal(err)
	}
	if err := <-unsupportedDone; err != nil {
		t.Fatal(err)
	}
	if err := <-fallbackDone; err != nil {
		t.Fatal(err)
	}
	if dials != 2 {
		t.Fatalf("dials = %d, want 2", dials)
	}
}

func TestMaskWriteBitDoesNotFallbackAfterOtherException(t *testing.T) {
	clientConnection, serverConnection := net.Pipe()
	defer serverConnection.Close()
	dials := 0
	client, err := NewWithDial(testConfig(), func(context.Context, string, string) (net.Conn, error) {
		dials++
		return clientConnection, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		frame, err := readFrame(serverConnection)
		if err != nil {
			done <- err
			return
		}
		if frame[7] != functionMaskWriteRegister {
			done <- io.ErrUnexpectedEOF
			return
		}
		done <- writeResponse(serverConnection, frame, []byte{functionMaskWriteRegister | 0x80, 2})
	}()
	if err := client.MaskWriteBit(context.Background(), 504, 1, true); err == nil {
		t.Fatal("FC22 exception unexpectedly succeeded")
	} else if errors.Is(err, ErrTransportDisconnected) {
		t.Fatalf("Modbus exception was classified as a transport disconnect: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if dials != 1 {
		t.Fatalf("write fallback dialed again: dials=%d", dials)
	}
}

func TestWriteMultipleRegistersDoesNotFallbackAfterOtherException(t *testing.T) {
	clientConnection, serverConnection := net.Pipe()
	defer serverConnection.Close()
	dials := 0
	client, err := NewWithDial(testConfig(), func(context.Context, string, string) (net.Conn, error) {
		dials++
		return clientConnection, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		frame, err := readFrame(serverConnection)
		if err != nil {
			done <- err
			return
		}
		if frame[7] != functionWriteMultipleRegs {
			done <- io.ErrUnexpectedEOF
			return
		}
		done <- writeResponse(serverConnection, frame, []byte{functionWriteMultipleRegs | 0x80, 2})
	}()
	if err := client.WriteMultipleRegisters(context.Background(), 1000, []uint16{1, 2}); err == nil {
		t.Fatal("FC10 exception unexpectedly succeeded")
	} else if errors.Is(err, ErrTransportDisconnected) {
		t.Fatalf("Modbus exception was classified as a transport disconnect: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if dials != 1 {
		t.Fatalf("write fallback dialed again: dials=%d", dials)
	}
}

func TestMaskWriteBitDoesNotFallbackAfterTransportFailure(t *testing.T) {
	clientConnection, serverConnection := net.Pipe()
	defer serverConnection.Close()
	dials := 0
	client, err := NewWithDial(testConfig(), func(context.Context, string, string) (net.Conn, error) {
		dials++
		return clientConnection, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		frame, err := readFrame(serverConnection)
		if err == nil && frame[7] != functionMaskWriteRegister {
			err = io.ErrUnexpectedEOF
		}
		_ = serverConnection.Close()
		done <- err
	}()
	if err := client.MaskWriteBit(context.Background(), 504, 1, true); !errors.Is(err, ErrTransportDisconnected) {
		t.Fatalf("write error = %v, want transport disconnect", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if dials != 1 {
		t.Fatalf("write fallback dialed again: dials=%d", dials)
	}
}

func TestWriteMultipleRegistersDoesNotFallbackAfterTransportFailure(t *testing.T) {
	clientConnection, serverConnection := net.Pipe()
	defer serverConnection.Close()
	dials := 0
	client, err := NewWithDial(testConfig(), func(context.Context, string, string) (net.Conn, error) {
		dials++
		return clientConnection, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		frame, err := readFrame(serverConnection)
		if err == nil && frame[7] != functionWriteMultipleRegs {
			err = io.ErrUnexpectedEOF
		}
		_ = serverConnection.Close()
		done <- err
	}()
	if err := client.WriteMultipleRegisters(context.Background(), 1000, []uint16{1, 2}); !errors.Is(err, ErrTransportDisconnected) {
		t.Fatalf("write error = %v, want transport disconnect", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if dials != 1 {
		t.Fatalf("write fallback dialed again: dials=%d", dials)
	}
}

func TestReadTransportDisconnectIsClassified(t *testing.T) {
	clientConnection, serverConnection := net.Pipe()
	client, err := NewWithDial(testConfig(), func(context.Context, string, string) (net.Conn, error) { return clientConnection, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		_, err := readFrame(serverConnection)
		_ = serverConnection.Close()
		done <- err
	}()
	_, err = client.ReadHoldingRegisters(context.Background(), 504, 1)
	if !errors.Is(err, ErrTransportDisconnected) {
		t.Fatalf("read error = %v, want transport disconnect", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func readFrame(connection net.Conn) ([]byte, error) {
	header := make([]byte, 7)
	if _, err := io.ReadFull(connection, header); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint16(header[4:6])
	body := make([]byte, int(length)-1)
	if _, err := io.ReadFull(connection, body); err != nil {
		return nil, err
	}
	return append(header, body...), nil
}

func writeResponse(connection net.Conn, request []byte, pdu []byte) error {
	header := make([]byte, 7)
	copy(header[:2], request[:2])
	binary.BigEndian.PutUint16(header[4:6], uint16(len(pdu)+1))
	header[6] = request[6]
	_, err := connection.Write(append(header, pdu...))
	return err
}
