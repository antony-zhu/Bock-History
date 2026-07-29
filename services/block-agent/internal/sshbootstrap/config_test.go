package sshbootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func validConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	return Config{
		ListenAddress:              ":9443",
		TargetNode:                 "BLOCK",
		SiteID:                     "site-lab",
		BlockID:                    "block-001",
		DeviceID:                   "device-001",
		AdvertisedHost:             "192.168.1.104",
		SSHPort:                    22,
		AdministratorKID:           "admin-lab-2026-01",
		AdministratorPublicKeyPath: filepath.Join(root, "admin.pem"),
		TLSCertificatePath:         filepath.Join(root, "tls.crt"),
		TLSPrivateKeyPath:          filepath.Join(root, "tls.key"),
		SSHUserCAPrivateKeyPath:    filepath.Join(root, "ssh-user-ca"),
		SSHUserCAPublicKeyPath:     filepath.Join(root, "ssh-user-ca.pub"),
		SSHHostKeyFingerprint:      "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		NonceDatabasePath:          filepath.Join(root, "nonces.db"),
		ReleaseUsername:            "release",
		DebugUsername:              "debug",
	}
}

func TestLoadConfigDefaultsToHTTPSPort9443AndRejectsUnknownFields(t *testing.T) {
	config := validConfig(t)
	config.ListenAddress = ""
	contents, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ListenAddress != ":9443" {
		t.Fatalf("listenAddress = %q", loaded.ListenAddress)
	}

	var values map[string]any
	if err := json.Unmarshal(contents, &values); err != nil {
		t.Fatal(err)
	}
	values["unexpected"] = true
	contents, err = json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("unknown config field unexpectedly accepted")
	}
}

func TestConfigRejectsNonBlockTargetAndRootUsername(t *testing.T) {
	config := validConfig(t)
	config.TargetNode = "BDM"
	if err := config.Validate(); err == nil {
		t.Fatal("BDM target unexpectedly accepted by Block service")
	}
	config = validConfig(t)
	config.DebugUsername = "root"
	if err := config.Validate(); err == nil {
		t.Fatal("root username unexpectedly accepted")
	}
}
