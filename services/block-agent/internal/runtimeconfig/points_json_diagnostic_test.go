package runtimeconfig

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPublishedPointsJSONNormalizes(t *testing.T) {
	contents, err := os.ReadFile("../../../../apps/block-hmi/assets/points.json")
	if err != nil {
		t.Fatal(err)
	}
	var value Config
	if err := json.Unmarshal(contents, &value); err != nil {
		t.Fatal(err)
	}
	if _, err := Normalize(value); err != nil {
		t.Fatal(err)
	}
}
