package agent

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPublishedPointsJSONConfiguresRuntime(t *testing.T) {
	contents, err := os.ReadFile("../../../../apps/block-hmi/assets/points.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Points json.RawMessage `json:"points"`
	}
	if err := json.Unmarshal(contents, &file); err != nil {
		t.Fatal(err)
	}
	runtime, address, cancel, done := startRuntime(t)
	defer stopRuntime(t, cancel, done)
	connection := dial(t, address)
	defer connection.Close()
	send(t, connection, map[string]any{
		"type": "runtime.configure", "scanIntervalMs": 500, "points": file.Points,
	})
	message := receive(t, connection)
	if message["type"] != "runtime.configured" {
		t.Fatalf("published runtime.configure response = %#v", message)
	}
	if !runtime.Store().Configured() {
		t.Fatal("published points.json did not configure the runtime")
	}
}
