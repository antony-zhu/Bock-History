// Package easy521 contains the narrow Modbus TCP operations approved for the
// current Easy521 path: FC03 register reads and FC22 single-bit mask writes.
package easy521

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	functionReadHoldingRegisters byte = 0x03
	functionMaskWriteRegister    byte = 0x16
	maxReadRegisters                  = 125
)

var ErrTransportDisconnected = errors.New("PLC transport disconnected")

type Config struct {
	Endpoint       string
	UnitID         byte
	ConnectTimeout time.Duration
	RequestTimeout time.Duration
}

type DialFunc func(context.Context, string, string) (net.Conn, error)

// Client is deliberately used by exactly one PLCWorker goroutine. It keeps
// one active Modbus TCP connection and exposes no FC06/FC16/full-word write.
type Client struct {
	cfg    Config
	dial   DialFunc
	conn   net.Conn
	nextID uint16
}

func New(config Config) (*Client, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: config.ConnectTimeout}
	return NewWithDial(config, dialer.DialContext)
}

func NewWithDial(config Config, dial DialFunc) (*Client, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if dial == nil {
		return nil, errors.New("Modbus dial function is required")
	}
	return &Client{cfg: config, dial: dial, nextID: 1}, nil
}

func (c *Client) Close() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

// ReadHoldingRegisters performs one FC03 request. The caller is responsible
// for batching consecutive D words into this request.
func (c *Client) ReadHoldingRegisters(ctx context.Context, address, quantity uint16) ([]uint16, error) {
	if quantity == 0 || quantity > maxReadRegisters || uint32(address)+uint32(quantity) > 1<<16 {
		return nil, fmt.Errorf("invalid FC03 register range address=%d quantity=%d", address, quantity)
	}
	request := make([]byte, 5)
	request[0] = functionReadHoldingRegisters
	binary.BigEndian.PutUint16(request[1:3], address)
	binary.BigEndian.PutUint16(request[3:5], quantity)
	response, err := c.exchange(ctx, request)
	if err != nil {
		return nil, err
	}
	if len(response) != 2+int(quantity)*2 || response[0] != functionReadHoldingRegisters || int(response[1]) != int(quantity)*2 {
		c.Close()
		return nil, errors.New("invalid Modbus FC03 response")
	}
	values := make([]uint16, quantity)
	for index := range values {
		values[index] = binary.BigEndian.Uint16(response[2+index*2 : 4+index*2])
	}
	return values, nil
}

// MaskWriteBit updates exactly one bit with FC22. It does not read or write a
// full register and it never retries a write after a failed exchange.
func (c *Client) MaskWriteBit(ctx context.Context, address uint16, bit uint8, target bool) error {
	if bit > 15 {
		return fmt.Errorf("PLC bit %d is outside 0..15", bit)
	}
	mask := uint16(1) << bit
	andMask := ^mask
	orMask := uint16(0)
	if target {
		orMask = mask
	}
	request := make([]byte, 7)
	request[0] = functionMaskWriteRegister
	binary.BigEndian.PutUint16(request[1:3], address)
	binary.BigEndian.PutUint16(request[3:5], andMask)
	binary.BigEndian.PutUint16(request[5:7], orMask)
	response, err := c.exchange(ctx, request)
	if err != nil {
		return err
	}
	if !bytes.Equal(response, request) {
		c.Close()
		return errors.New("Modbus FC22 response does not echo request")
	}
	return nil
}

func (c *Client) exchange(ctx context.Context, pdu []byte) ([]byte, error) {
	if err := c.ensureConnection(ctx); err != nil {
		return nil, err
	}
	c.nextID++
	if c.nextID == 0 {
		c.nextID = 1
	}
	transactionID := c.nextID
	frame := make([]byte, 7+len(pdu))
	binary.BigEndian.PutUint16(frame[0:2], transactionID)
	binary.BigEndian.PutUint16(frame[4:6], uint16(len(pdu)+1))
	frame[6] = c.cfg.UnitID
	copy(frame[7:], pdu)

	deadline := time.Now().Add(c.cfg.RequestTimeout)
	if candidate, ok := ctx.Deadline(); ok && candidate.Before(deadline) {
		deadline = candidate
	}
	_ = c.conn.SetDeadline(deadline)
	if _, err := c.conn.Write(frame); err != nil {
		c.Close()
		return nil, transportError(err)
	}

	header := make([]byte, 7)
	if _, err := io.ReadFull(c.conn, header); err != nil {
		c.Close()
		return nil, transportError(err)
	}
	length := binary.BigEndian.Uint16(header[4:6])
	if binary.BigEndian.Uint16(header[0:2]) != transactionID || binary.BigEndian.Uint16(header[2:4]) != 0 ||
		header[6] != c.cfg.UnitID || length < 2 || length > 260 {
		c.Close()
		return nil, errors.New("invalid Modbus TCP MBAP response")
	}
	response := make([]byte, int(length)-1)
	if _, err := io.ReadFull(c.conn, response); err != nil {
		c.Close()
		return nil, transportError(err)
	}
	if response[0] == pdu[0]|0x80 {
		c.Close()
		if len(response) != 2 {
			return nil, errors.New("invalid Modbus exception response")
		}
		return nil, fmt.Errorf("Modbus exception 0x%02x", response[1])
	}
	if response[0] != pdu[0] {
		c.Close()
		return nil, errors.New("Modbus function code mismatch")
	}
	return response, nil
}

func (c *Client) ensureConnection(ctx context.Context) error {
	if c.conn != nil {
		return nil
	}
	connection, err := c.dial(ctx, "tcp", c.cfg.Endpoint)
	if err != nil {
		return transportError(err)
	}
	c.conn = connection
	return nil
}

func transportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrTransportDisconnected, err)
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return fmt.Errorf("%w: %w", ErrTransportDisconnected, err)
	}
	return err
}

func validateConfig(config Config) error {
	if config.Endpoint == "" {
		return errors.New("Modbus endpoint is required")
	}
	if config.ConnectTimeout <= 0 || config.RequestTimeout <= 0 {
		return errors.New("Modbus timeouts must be positive")
	}
	return nil
}
