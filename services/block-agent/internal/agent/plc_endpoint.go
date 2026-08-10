package agent

import (
	"context"

	"block.local/block-agent/internal/storage"
)

func loadPLCEndpoint(store *storage.Store) (plcEndpoint, bool, error) {
	if store == nil {
		return plcEndpoint{}, false, nil
	}
	saved, found, err := store.LoadPLCEndpoint(context.Background())
	if err != nil || !found {
		return plcEndpoint{}, found, err
	}
	endpoint, err := parsePLCDeviceID((plcEndpoint{
		host: saved.Host, port: saved.Port, unitID: byte(saved.UnitID),
	}).DeviceID())
	if err != nil {
		return plcEndpoint{}, false, err
	}
	return endpoint, true, nil
}

func savePLCEndpoint(store *storage.Store, endpoint plcEndpoint) error {
	if store == nil {
		return nil
	}
	return store.SavePLCEndpoint(context.Background(), storage.PLCEndpoint{
		Host: endpoint.host, Port: endpoint.port, UnitID: int(endpoint.unitID),
	})
}
