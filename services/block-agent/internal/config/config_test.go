package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentIdentityUsesContractIdentifierGrammar(t *testing.T) {
	path := writeAgentConfig(t)
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

func writeAgentConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "block-agent.json")
	contents := `{
  "siteId":"site-lab",
  "blockId":"block-001",
  "deviceId":"device-001",
  "adapter":{"type":"disabled","ioSocket":"/run/block-plc/io/io.sock"},
  "databasePath":"/var/lib/block/block.db",
  "samplePeriod":"1s",
  "staleAfter":"5s",
  "commandTimeout":"2s"
}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
