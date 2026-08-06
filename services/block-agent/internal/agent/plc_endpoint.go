package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type storedPLCEndpoint struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	UnitID byte   `json:"unitId"`
}

func loadPLCEndpoint(path string) (plcEndpoint, bool, error) {
	if path == "" {
		return plcEndpoint{}, false, nil
	}
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return plcEndpoint{}, false, nil
	}
	if err != nil {
		return plcEndpoint{}, false, err
	}
	var stored storedPLCEndpoint
	if err := json.Unmarshal(contents, &stored); err != nil {
		return plcEndpoint{}, false, err
	}
	endpoint, err := parsePLCDeviceID((plcEndpoint{host: stored.Host, port: stored.Port, unitID: stored.UnitID}).DeviceID())
	if err != nil {
		return plcEndpoint{}, false, err
	}
	return endpoint, true, nil
}

func savePLCEndpoint(path string, endpoint plcEndpoint) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".plc-endpoint-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := json.NewEncoder(temporary).Encode(storedPLCEndpoint{Host: endpoint.host, Port: endpoint.port, UnitID: endpoint.unitID}); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

func clearPLCEndpoint(path string) error {
	if path == "" {
		return nil
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
