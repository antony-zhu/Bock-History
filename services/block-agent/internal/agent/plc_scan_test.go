package agent

import (
	"context"
	"net/netip"
	"testing"
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
