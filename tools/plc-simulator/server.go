package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	functionReadHoldingRegisters byte = 0x03
	functionMaskWriteRegister    byte = 0x16

	exceptionIllegalFunction    byte = 0x01
	exceptionIllegalDataAddress byte = 0x02
	exceptionIllegalDataValue   byte = 0x03

	maxReadHoldingRegisters = 125
	maxMBAPLength           = 254
)

type registerStore struct {
	mu     sync.RWMutex
	values [1 << 16]uint16
}

func newRegisterStore(initial map[uint16]uint16) registerStore {
	store := registerStore{}
	for address, value := range initial {
		store.values[address] = value
	}
	return store
}

func (s *registerStore) read(address, count uint16) []uint16 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]uint16, count)
	copy(values, s.values[int(address):int(address)+int(count)])
	return values
}

func (s *registerStore) maskWrite(address, andMask, orMask uint16) uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.values[address]
	// Modbus FC22: Result = (Current AND And_Mask) OR (Or_Mask AND NOT And_Mask).
	next := (current & andMask) | (orMask &^ andMask)
	s.values[address] = next
	return next
}

type Server struct {
	unitID    byte
	registers registerStore
	trace     io.Writer
	traceMu   sync.Mutex
}

func NewServer(unitID byte, initial map[uint16]uint16, trace io.Writer) *Server {
	if trace == nil {
		trace = io.Discard
	}
	return &Server{
		unitID:    unitID,
		registers: newRegisterStore(initial),
		trace:     trace,
	}
}

type traceMasks struct {
	And uint16 `json:"and"`
	Or  uint16 `json:"or"`
}

type requestTrace struct {
	Time        string      `json:"time"`
	Peer        string      `json:"peer"`
	Transaction uint16      `json:"transaction"`
	Unit        byte        `json:"unit"`
	Function    byte        `json:"function"`
	Address     *uint16     `json:"address"`
	Masks       *traceMasks `json:"masks"`
	Result      string      `json:"result"`
}

func (s *Server) writeTrace(entry requestTrace) {
	s.traceMu.Lock()
	defer s.traceMu.Unlock()
	_ = json.NewEncoder(s.trace).Encode(entry)
}

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	var connections sync.WaitGroup
	var connectionMu sync.Mutex
	activeConnections := make(map[net.Conn]struct{})
	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			_ = listener.Close()
			connectionMu.Lock()
			for connection := range activeConnections {
				_ = connection.Close()
			}
			connectionMu.Unlock()
		})
	}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdown()
		case <-stopped:
		}
	}()
	defer func() {
		close(stopped)
		shutdown()
		connections.Wait()
	}()

	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept connection: %w", err)
		}
		connectionMu.Lock()
		if ctx.Err() != nil {
			connectionMu.Unlock()
			_ = connection.Close()
			return nil
		}
		activeConnections[connection] = struct{}{}
		connectionMu.Unlock()
		connections.Add(1)
		go func() {
			defer connections.Done()
			defer connection.Close()
			defer func() {
				connectionMu.Lock()
				delete(activeConnections, connection)
				connectionMu.Unlock()
			}()
			s.serveConnection(connection)
		}()
	}
}

func (s *Server) serveConnection(connection net.Conn) {
	for {
		request, err := readRequest(connection)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return
			}
			return
		}
		function := byte(0)
		if len(request.pdu) > 0 {
			function = request.pdu[0]
		}
		entry := requestTrace{
			Time:        time.Now().UTC().Format(time.RFC3339Nano),
			Peer:        connection.RemoteAddr().String(),
			Transaction: request.transaction,
			Unit:        request.unit,
			Function:    function,
		}
		if request.unit != s.unitID {
			entry.Result = "ignored_unit"
			s.writeTrace(entry)
			continue
		}

		response, address, masks, result := s.handlePDU(request.pdu)
		entry.Address = address
		entry.Masks = masks
		entry.Result = result
		if err := writeResponse(connection, request.transaction, request.unit, response); err != nil {
			entry.Result = "response_write_error"
		}
		s.writeTrace(entry)
		if entry.Result == "response_write_error" {
			return
		}
	}
}

type request struct {
	transaction uint16
	unit        byte
	pdu         []byte
}

func readRequest(reader io.Reader) (request, error) {
	var header [6]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return request{}, err
	}
	if protocolID := binary.BigEndian.Uint16(header[2:4]); protocolID != 0 {
		return request{}, fmt.Errorf("unsupported protocol ID %d", protocolID)
	}
	length := binary.BigEndian.Uint16(header[4:6])
	if length < 2 || length > maxMBAPLength {
		return request{}, fmt.Errorf("invalid MBAP length %d", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(reader, body); err != nil {
		return request{}, err
	}
	return request{
		transaction: binary.BigEndian.Uint16(header[0:2]),
		unit:        body[0],
		pdu:         body[1:],
	}, nil
}

func (s *Server) handlePDU(pdu []byte) ([]byte, *uint16, *traceMasks, string) {
	if len(pdu) == 0 {
		return nil, nil, nil, "malformed_request"
	}
	switch pdu[0] {
	case functionReadHoldingRegisters:
		return s.handleReadHoldingRegisters(pdu)
	case functionMaskWriteRegister:
		return s.handleMaskWriteRegister(pdu)
	default:
		return exceptionResponse(pdu[0], exceptionIllegalFunction), nil, nil, "exception_illegal_function"
	}
}

func (s *Server) handleReadHoldingRegisters(pdu []byte) ([]byte, *uint16, *traceMasks, string) {
	if len(pdu) != 5 {
		return exceptionResponse(functionReadHoldingRegisters, exceptionIllegalDataValue), nil, nil, "exception_illegal_data_value"
	}
	address := binary.BigEndian.Uint16(pdu[1:3])
	count := binary.BigEndian.Uint16(pdu[3:5])
	if count == 0 || count > maxReadHoldingRegisters {
		return exceptionResponse(functionReadHoldingRegisters, exceptionIllegalDataValue), &address, nil, "exception_illegal_data_value"
	}
	if uint32(address)+uint32(count) > 1<<16 {
		return exceptionResponse(functionReadHoldingRegisters, exceptionIllegalDataAddress), &address, nil, "exception_illegal_data_address"
	}
	values := s.registers.read(address, count)
	response := make([]byte, 2+len(values)*2)
	response[0] = functionReadHoldingRegisters
	response[1] = byte(len(values) * 2)
	for index, value := range values {
		binary.BigEndian.PutUint16(response[2+index*2:], value)
	}
	return response, &address, nil, "ok"
}

func (s *Server) handleMaskWriteRegister(pdu []byte) ([]byte, *uint16, *traceMasks, string) {
	if len(pdu) != 7 {
		return exceptionResponse(functionMaskWriteRegister, exceptionIllegalDataValue), nil, nil, "exception_illegal_data_value"
	}
	address := binary.BigEndian.Uint16(pdu[1:3])
	andMask := binary.BigEndian.Uint16(pdu[3:5])
	orMask := binary.BigEndian.Uint16(pdu[5:7])
	s.registers.maskWrite(address, andMask, orMask)
	response := append([]byte(nil), pdu...)
	return response, &address, &traceMasks{And: andMask, Or: orMask}, "ok"
}

func exceptionResponse(function, code byte) []byte {
	return []byte{function | 0x80, code}
}

func writeResponse(writer io.Writer, transaction uint16, unit byte, pdu []byte) error {
	var header [6]byte
	binary.BigEndian.PutUint16(header[0:2], transaction)
	binary.BigEndian.PutUint16(header[2:4], 0)
	binary.BigEndian.PutUint16(header[4:6], uint16(len(pdu)+1))
	if err := writeAll(writer, header[:]); err != nil {
		return err
	}
	if err := writeAll(writer, []byte{unit}); err != nil {
		return err
	}
	return writeAll(writer, pdu)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		data = data[written:]
	}
	return nil
}
