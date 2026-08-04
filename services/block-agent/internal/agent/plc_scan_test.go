package agent

import "testing"

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
	if _, err := scanAddresses("192.168.0.0/16"); err == nil {
		t.Fatal("oversized scan range was accepted")
	}
}
