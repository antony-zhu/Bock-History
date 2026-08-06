// Package wifi provides the narrow NetworkManager boundary used by local HMI maintenance.
package wifi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var ErrUnavailable = errors.New("NetworkManager boundary is unavailable")

type Status struct {
	State     string  `json:"state"`
	Interface string  `json:"interface"`
	SSID      *string `json:"ssid"`
	IPv4      *string `json:"ipv4"`
}

type Backend interface {
	Status(context.Context, string) (Status, error)
	Apply(context.Context, string, string, string) (Status, error)
}

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, command, args...).CombinedOutput()
}

// NetworkManager uses nmcli only as a parameterized D-Bus/Polkit client. The
// password is written to a mode-0600 temporary keyfile rather than process argv.
type NetworkManager struct {
	runner  Runner
	tempDir string
}

func NewNetworkManager(runner Runner, tempDir string) *NetworkManager {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &NetworkManager{runner: runner, tempDir: tempDir}
}

func (n *NetworkManager) Status(ctx context.Context, iface string) (Status, error) {
	output, err := n.runner.Run(ctx, "nmcli", "-t", "-f", "GENERAL.STATE,GENERAL.CONNECTION,IP4.ADDRESS", "device", "show", iface)
	if err != nil {
		return Status{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	status := Status{State: "disconnected", Interface: iface}
	for _, line := range strings.Split(string(output), "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch key {
		case "GENERAL.STATE":
			if strings.HasPrefix(value, "100") {
				status.State = "connected"
			}
		case "GENERAL.CONNECTION":
			if value != "" && value != "--" {
				ssid := strings.TrimPrefix(value, "block-wifi-")
				status.SSID = &ssid
			}
		case "IP4.ADDRESS[1]":
			if host, _, ok := strings.Cut(value, "/"); ok {
				value = host
			}
			if value != "" {
				ipv4 := value
				status.IPv4 = &ipv4
			}
		}
	}
	return status, nil
}

func (n *NetworkManager) Apply(ctx context.Context, iface, ssid, password string) (Status, error) {
	if err := os.MkdirAll(n.tempDir, 0o700); err != nil {
		return Status{}, err
	}
	file, err := os.CreateTemp(n.tempDir, "block-wifi-*.nmconnection")
	if err != nil {
		return Status{}, err
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return Status{}, err
	}
	profileID := "block-wifi-" + stripLineBreaks(ssid)
	profile := "[connection]\nid=" + escapeKeyfile(profileID) + "\ntype=wifi\ninterface-name=" + escapeKeyfile(iface) +
		"\n[wifi]\nmode=infrastructure\nssid=" + escapeKeyfile(ssid) +
		"\n[wifi-security]\nkey-mgmt=wpa-psk\npsk=" + escapeKeyfile(password) +
		"\n[ipv4]\nmethod=auto\n[ipv6]\nmethod=auto\n"
	if _, err := file.WriteString(profile); err != nil {
		_ = file.Close()
		return Status{}, err
	}
	if err := file.Close(); err != nil {
		return Status{}, err
	}
	if _, err := n.runner.Run(ctx, "nmcli", "connection", "load", filepath.Clean(path)); err != nil {
		return Status{}, fmt.Errorf("%w: load Wi-Fi profile", ErrUnavailable)
	}
	if _, err := n.runner.Run(ctx, "nmcli", "connection", "up", "id", profileID, "ifname", iface); err != nil {
		return Status{}, fmt.Errorf("%w: activate Wi-Fi profile", ErrUnavailable)
	}
	return n.Status(ctx, iface)
}

func escapeKeyfile(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	return stripLineBreaks(value)
}

func stripLineBreaks(value string) string {
	value = strings.ReplaceAll(value, "\n", "")
	return strings.ReplaceAll(value, "\r", "")
}
