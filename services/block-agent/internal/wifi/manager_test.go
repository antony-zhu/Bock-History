package wifi

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
)

type recordingRunner struct {
	t        *testing.T
	password string
	path     string
	mode     os.FileMode
}

func (r *recordingRunner) Run(_ context.Context, command string, args ...string) ([]byte, error) {
	for _, arg := range append([]string{command}, args...) {
		if strings.Contains(arg, r.password) {
			r.t.Fatal("password leaked to command arguments")
		}
	}
	if len(args) >= 3 && args[0] == "connection" && args[1] == "load" {
		r.path = args[2]
		info, err := os.Stat(r.path)
		if err != nil {
			r.t.Fatal(err)
		}
		r.mode = info.Mode().Perm()
		contents, err := os.ReadFile(r.path)
		if err != nil {
			r.t.Fatal(err)
		}
		if !strings.Contains(string(contents), "psk="+r.password) {
			r.t.Fatal("password was not written to protected keyfile")
		}
	}
	if len(args) > 0 && args[0] == "-t" {
		return []byte("GENERAL.STATE:100 (connected)\nGENERAL.CONNECTION:block-wifi-Factory\nIP4.ADDRESS[1]:192.168.1.104/24\n"), nil
	}
	return nil, nil
}

func TestNetworkManagerUsesProtectedKeyfile(t *testing.T) {
	runner := &recordingRunner{t: t, password: "factory-secret"}
	backend := NewNetworkManager(runner, t.TempDir())
	status, err := backend.Apply(context.Background(), "wlan0", "Factory", runner.password)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && runner.mode != 0o600 {
		t.Fatalf("keyfile mode = %o", runner.mode)
	}
	if _, err := os.Stat(runner.path); !os.IsNotExist(err) {
		t.Fatalf("temporary keyfile was not removed: %v", err)
	}
	if status.State != "connected" || status.SSID == nil || *status.SSID != "Factory" {
		t.Fatalf("status = %#v", status)
	}
}
