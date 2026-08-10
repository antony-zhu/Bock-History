package agent

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestScanAddressRangeAndDeviceIDAreSessionOnlyAndParseable(t *testing.T) {
	addresses, err := scanAddresses("192.168.10.0/30")
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 2 || addresses[0].String() != "192.168.10.1" || addresses[1].String() != "192.168.10.2" {
		t.Fatalf("scan addresses = %#v", addresses)
	}
	endpoint, err := parsePLCDeviceID("easy521://192.168.10.1:1502?unitId=1")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.host != "192.168.10.1" || endpoint.port != 1502 || endpoint.unitID != 1 {
		t.Fatalf("parsed endpoint = %+v", endpoint)
	}
	if _, err := parsePLCDeviceID("easy521://plc.local:502?unitId=1"); err == nil {
		t.Fatal("non-IP deviceId was accepted")
	}
	if _, err := parsePLCDeviceID("easy521://192.168.10.1:502?unitId=0"); err == nil {
		t.Fatal("unit ID below the V1 range was accepted")
	}
	if _, err := parsePLCDeviceID("easy521://192.168.10.1:502?unitId=248"); err == nil {
		t.Fatal("unit ID above the V1 range was accepted")
	}
	if _, err := scanAddresses("192.168.0.0/16"); err == nil {
		t.Fatal("oversized scan range was accepted")
	}
}

func TestScanRangePassesRequestedPortAndUnitIDToProbe(t *testing.T) {
	var gotAddress netip.Addr
	var gotPort int
	var gotUnitID byte
	devices, err := scanRangeWithProbe(context.Background(), "192.168.10.1/32", 1502, 7, "", func(_ context.Context, address netip.Addr, port int, unitID byte, _ string) (plcDevice, bool) {
		gotAddress, gotPort, gotUnitID = address, port, unitID
		endpoint := plcEndpoint{host: address.String(), port: port, unitID: unitID}
		return plcDevice{DeviceID: endpoint.DeviceID(), Address: address.String()}, true
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAddress.String() != "192.168.10.1" || gotPort != 1502 || gotUnitID != 7 {
		t.Fatalf("probe settings = address=%s port=%d unitId=%d", gotAddress, gotPort, gotUnitID)
	}
	if len(devices) != 1 || devices[0].DeviceID != "easy521://192.168.10.1:1502?unitId=7" {
		t.Fatalf("scan devices = %#v", devices)
	}
}

func TestProbePLCReadsConfirmedD504(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serverDone := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer connection.Close()

		header := make([]byte, 7)
		if _, err := io.ReadFull(connection, header); err != nil {
			serverDone <- err
			return
		}
		pdu := make([]byte, int(binary.BigEndian.Uint16(header[4:6]))-1)
		if _, err := io.ReadFull(connection, pdu); err != nil {
			serverDone <- err
			return
		}
		if header[6] != 7 || len(pdu) != 5 || pdu[0] != 3 || binary.BigEndian.Uint16(pdu[1:3]) != plcProbeRegister || binary.BigEndian.Uint16(pdu[3:5]) != 1 {
			serverDone <- fmt.Errorf("probe unit=%d PDU=%x", header[6], pdu)
			return
		}

		response := []byte{3, 2, 0, 0}
		frame := make([]byte, 7+len(response))
		copy(frame[:4], header[:4])
		binary.BigEndian.PutUint16(frame[4:6], uint16(len(response)+1))
		frame[6] = header[6]
		copy(frame[7:], response)
		_, err = connection.Write(frame)
		serverDone <- err
	}()

	address := netip.MustParseAddr("127.0.0.1")
	port := listener.Addr().(*net.TCPAddr).Port
	probeContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	device, found := probePLC(probeContext, address, port, 7, "")
	expected := (plcEndpoint{host: address.String(), port: port, unitID: 7}).DeviceID()
	if !found || device.DeviceID != expected {
		t.Fatalf("probe result = %#v, found=%t", device, found)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
