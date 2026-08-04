package easy521

import (
	"context"
	"encoding/binary"
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

func TestMaskWriteBitDoesNotRetryAfterException(t *testing.T) {
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
		done <- writeResponse(serverConnection, frame, []byte{functionMaskWriteRegister | 0x80, 2})
	}()
	if err := client.MaskWriteBit(context.Background(), 504, 1, true); err == nil {
		t.Fatal("FC22 exception unexpectedly succeeded")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if dials != 1 {
		t.Fatalf("writes were retried: dials=%d", dials)
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
