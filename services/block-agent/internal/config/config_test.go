package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBDMDisabledRequiresNoNetworkConfiguration(t *testing.T) {
	path := writeAgentConfig(t, `"bdm":{"enabled":false}`)
	value, err := LoadAgent(path)
	if err != nil {
		t.Fatalf("disabled BDM config: %v", err)
	}
	if value.BDM.Enabled {
		t.Fatal("BDM unexpectedly enabled")
	}
}

func TestBDMEnabledAcceptsOnlyStrictMQTTSConfiguration(t *testing.T) {
	certificateRoot := filepath.Join(t.TempDir(), "certs")
	valid := `"bdm":{` +
		`"enabled":true,` +
		`"endpoint":"mqtts://192.168.1.105:8883",` +
		`"principal":"blk-0123456789abcdef0123456789abcdef",` +
		`"caFile":` + quote(filepath.Join(certificateRoot, "ca.crt")) + `,` +
		`"clientCertFile":` + quote(filepath.Join(certificateRoot, "client.crt")) + `,` +
		`"clientKeyFile":` + quote(filepath.Join(certificateRoot, "client.key")) + `,` +
		`"softwareVersion":"0.1.0",` +
		`"osVersion":"Ubuntu 18.04.5 LTS",` +
		`"architecture":"arm64",` +
		`"hardwareModel":"rk3566",` +
		`"streamGeneration":"1"` +
		`}`
	if _, err := LoadAgent(writeAgentConfig(t, valid)); err != nil {
		t.Fatalf("valid MQTTS config: %v", err)
	}

	tests := []struct {
		name    string
		replace string
		with    string
	}{
		{name: "plaintext MQTT", replace: "mqtts://", with: "mqtt://"},
		{name: "WebSocket", replace: "mqtts://", with: "wss://"},
		{name: "plaintext port", replace: ":8883", with: ":1883"},
		{name: "URL credentials", replace: "mqtts://192", with: "mqtts://user@192"},
		{name: "URL path", replace: ":8883\"", with: ":8883/mqtt\""},
		{name: "derived principal", replace: "blk-0123456789abcdef0123456789abcdef", with: "block-site-lab-block-001"},
		{name: "relative CA", replace: quote(filepath.Join(certificateRoot, "ca.crt")), with: `"ca.crt"`},
		{name: "zero generation", replace: `"streamGeneration":"1"`, with: `"streamGeneration":"0"`},
		{name: "noncanonical generation", replace: `"streamGeneration":"1"`, with: `"streamGeneration":"01"`},
		{name: "future generation overflow", replace: `"streamGeneration":"1"`, with: `"streamGeneration":"9007199254740992"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := strings.Replace(valid, test.replace, test.with, 1)
			if _, err := LoadAgent(writeAgentConfig(t, candidate)); err == nil {
				t.Fatal("invalid BDM configuration unexpectedly passed")
			}
		})
	}
}

func TestAgentIdentityUsesContractIdentifierGrammar(t *testing.T) {
	path := writeAgentConfig(t, `"bdm":{"enabled":false}`)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	invalid := strings.Replace(string(contents), `"siteId":"site-lab"`, `"siteId":"Site Lab"`, 1)
	if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgent(path); err == nil {
		t.Fatal("invalid site identity unexpectedly passed")
	}
}

func writeAgentConfig(t *testing.T, bdm string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "block-agent.json")
	contents := `{
  "siteId":"site-lab",
  "blockId":"block-001",
  "deviceId":"device-001",
  "adapter":{"type":"disabled","ioSocket":"/run/block-plc/io/io.sock"},
  "localApiSocket":"/run/block-agent/api/block-agent.sock",
  "localApiSocketGroup":"block-hmi-api",
  "databasePath":"/var/lib/block/block.db",
  "samplePeriod":"1s",
  "staleAfter":"5s",
  "commandTimeout":"2s",
  ` + bdm + `
}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func quote(value string) string {
	return `"` + strings.ReplaceAll(value, `\`, `\\`) + `"`
}
