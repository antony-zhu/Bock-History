package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"block.local/block-agent/internal/plccontract"
)

type Simulator struct {
	socket    string
	client    *http.Client
	transport *http.Transport
}

func NewSimulator(socket string, timeout time.Duration) *Simulator {
	transport := &http.Transport{
		Proxy:              nil,
		DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "unix", socket)
		},
	}
	return &Simulator{socket: socket, transport: transport, client: &http.Client{Transport: transport, Timeout: timeout}}
}

func (s *Simulator) Read(ctx context.Context) (plccontract.Snapshot, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/snapshot", nil)
	if err != nil {
		return plccontract.Snapshot{}, fmt.Errorf("%w: build snapshot request: %v", ErrBadData, err)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return plccontract.Snapshot{}, fmt.Errorf("%w: read simulator snapshot via %s: %v", ErrUnavailable, s.socket, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		protocolErr := decodeProtocolError(response)
		if response.StatusCode >= http.StatusInternalServerError {
			return plccontract.Snapshot{}, fmt.Errorf("%w: %v", ErrUnavailable, protocolErr)
		}
		return plccontract.Snapshot{}, fmt.Errorf("%w: %v", ErrBadData, protocolErr)
	}
	var envelope struct {
		Snapshot plccontract.Snapshot `json:"snapshot"`
	}
	if err := decodeLimited(response.Body, &envelope); err != nil {
		return plccontract.Snapshot{}, fmt.Errorf("%w: decode simulator snapshot: %v", ErrBadData, err)
	}
	if err := ValidateSnapshot(envelope.Snapshot); err != nil {
		return plccontract.Snapshot{}, err
	}
	return envelope.Snapshot, nil
}

func (s *Simulator) Execute(ctx context.Context, command plccontract.Command) (plccontract.CommandResult, error) {
	contents, err := json.Marshal(command)
	if err != nil {
		return plccontract.CommandResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/commands", bytes.NewReader(contents))
	if err != nil {
		return plccontract.CommandResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return plccontract.CommandResult{}, fmt.Errorf("%w: simulator command timed out", ErrOutcomeUnknown)
		}
		return plccontract.CommandResult{}, fmt.Errorf("%w: simulator command transport failed: %v", ErrOutcomeUnknown, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return plccontract.CommandResult{}, decodeProtocolError(response)
	}
	var envelope struct {
		Result plccontract.CommandResult `json:"result"`
	}
	if err := decodeLimited(response.Body, &envelope); err != nil {
		return plccontract.CommandResult{}, fmt.Errorf("%w: decode simulator command response: %v", ErrOutcomeUnknown, err)
	}
	return envelope.Result, nil
}

func (s *Simulator) Close() {
	s.transport.CloseIdleConnections()
}

func decodeProtocolError(response *http.Response) error {
	var envelope plccontract.ErrorEnvelope
	if err := decodeLimited(response.Body, &envelope); err != nil {
		return fmt.Errorf("simulator returned HTTP %d", response.StatusCode)
	}
	return fmt.Errorf("simulator %s: %s", envelope.Error.Code, envelope.Error.Message)
}

func decodeLimited(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("only one JSON value is allowed")
	}
	return nil
}
