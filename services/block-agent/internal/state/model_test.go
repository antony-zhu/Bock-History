package state

import (
	"encoding/json"
	"testing"
	"time"

	"block.local/block-agent/internal/plccontract"
)

func TestFromPLCInitializesEmptyCollections(t *testing.T) {
	model, _ := FromPLC(plccontract.Snapshot{
		SchemaVersion:  plccontract.SchemaVersion,
		SampleSequence: 1,
		GeneratedAt:    time.Unix(1, 0).UTC(),
		Quality:        plccontract.QualityGood,
		Points: plccontract.Points{
			ControlRevision: 1,
		},
	}, Model{})

	if model.Bins == nil || model.Alarms == nil || model.History == nil {
		t.Fatalf("empty collections must be non-nil: bins=%v alarms=%v history=%v",
			model.Bins, model.Alarms, model.History)
	}
	contents, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(contents, &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"bins", "alarms", "history"} {
		if got := string(payload[field]); got != "[]" {
			t.Fatalf("%s = %s, want []", field, got)
		}
	}
}
