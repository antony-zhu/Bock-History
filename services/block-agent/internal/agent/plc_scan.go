package agent

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"

	"block.local/block-agent/internal/easy521"
)

const (
	defaultPLCScanPort = 502
	defaultPLCUnitID   = 1
	maxScanCandidates  = 256
	maxScanWorkers     = 16
)

type plcEndpoint struct {
	host   string
	port   int
	unitID byte
}

type plcDevice struct {
	DeviceID string         `json:"deviceId"`
	Name     string         `json:"name"`
	Address  string         `json:"address"`
	State    string         `json:"state"`
	Selected bool           `json:"selected"`
	Metadata map[string]any `json:"metadata"`
}

type plcProbe func(context.Context, netip.Addr, int, byte, string) (plcDevice, bool)

func scanRange(ctx context.Context, addressRange string, port int, unitID byte, selectedDeviceID string) ([]plcDevice, error) {
	return scanRangeWithProbe(ctx, addressRange, port, unitID, selectedDeviceID, probePLC)
}

func scanRangeWithProbe(ctx context.Context, addressRange string, port int, unitID byte, selectedDeviceID string, probe plcProbe) ([]plcDevice, error) {
	addresses, err := scanAddresses(addressRange)
	if err != nil {
		return nil, err
	}
	jobs := make(chan netip.Addr)
	results := make(chan plcDevice)
	workerCount := maxScanWorkers
	if len(addresses) < workerCount {
		workerCount = len(addresses)
	}
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for address := range jobs {
				device, found := probe(ctx, address, port, unitID, selectedDeviceID)
				if !found {
					continue
				}
				select {
				case results <- device:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, address := range addresses {
			select {
			case jobs <- address:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	devices := make([]plcDevice, 0)
	for device := range results {
		devices = append(devices, device)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(devices, func(left, right int) bool { return devices[left].Address < devices[right].Address })
	return devices, nil
}

func scanAddresses(raw string) ([]netip.Addr, error) {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil || !prefix.Addr().Is4() {
		return nil, errorsInvalidAddressRange(raw)
	}
	prefix = prefix.Masked()
	count := uint64(1) << uint(32-prefix.Bits())
	if count > maxScanCandidates {
		return nil, fmt.Errorf("addressRange %q contains more than %d addresses", raw, maxScanCandidates)
	}
	baseBytes := prefix.Addr().As4()
	base := binary.BigEndian.Uint32(baseBytes[:])
	addresses := make([]netip.Addr, 0, count)
	for offset := uint64(0); offset < count; offset++ {
		if count > 2 && (offset == 0 || offset == count-1) {
			continue
		}
		value := base + uint32(offset)
		var bytes [4]byte
		binary.BigEndian.PutUint32(bytes[:], value)
		addresses = append(addresses, netip.AddrFrom4(bytes))
	}
	return addresses, nil
}

func errorsInvalidAddressRange(raw string) error {
	return fmt.Errorf("addressRange %q must be an IPv4 CIDR", raw)
}

func probePLC(ctx context.Context, address netip.Addr, port int, unitID byte, selectedDeviceID string) (plcDevice, bool) {
	endpoint := plcEndpoint{host: address.String(), port: port, unitID: unitID}
	client, err := easy521.New(easy521.Config{
		Endpoint:       endpoint.String(),
		UnitID:         endpoint.unitID,
		ConnectTimeout: 300 * time.Millisecond,
		RequestTimeout: 300 * time.Millisecond,
	})
	if err != nil {
		return plcDevice{}, false
	}
	defer client.Close()
	probeContext, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	if _, err := client.ReadHoldingRegisters(probeContext, 0, 1); err != nil {
		return plcDevice{}, false
	}
	deviceID := endpoint.DeviceID()
	return plcDevice{
		DeviceID: deviceID,
		Name:     "Easy521 " + endpoint.host,
		Address:  endpoint.host,
		State:    "disconnected",
		Selected: deviceID == selectedDeviceID,
		Metadata: map[string]any{"protocol": "modbus-tcp", "port": endpoint.port, "unitId": endpoint.unitID},
	}, true
}

func (e plcEndpoint) String() string {
	return net.JoinHostPort(e.host, strconv.Itoa(e.port))
}

func (e plcEndpoint) DeviceID() string {
	return fmt.Sprintf("easy521://%s?unitId=%d", e.String(), e.unitID)
}

func parsePLCDeviceID(raw string) (plcEndpoint, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "easy521" || parsed.User != nil || parsed.Path != "" || parsed.Fragment != "" {
		return plcEndpoint{}, errorsInvalidDeviceID()
	}
	if parsed.Hostname() == "" || net.ParseIP(parsed.Hostname()) == nil {
		return plcEndpoint{}, errorsInvalidDeviceID()
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return plcEndpoint{}, errorsInvalidDeviceID()
	}
	query := parsed.Query()
	if len(query) != 1 || len(query["unitId"]) != 1 {
		return plcEndpoint{}, errorsInvalidDeviceID()
	}
	unitID, err := strconv.Atoi(query.Get("unitId"))
	if err != nil || unitID < 1 || unitID > 247 {
		return plcEndpoint{}, errorsInvalidDeviceID()
	}
	return plcEndpoint{host: parsed.Hostname(), port: port, unitID: byte(unitID)}, nil
}

func errorsInvalidDeviceID() error { return fmt.Errorf("deviceId is not an Easy521 scan result") }
