package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHMIStateMarshalsNilCollectionsAsEmptyArrays(t *testing.T) {
	contents, err := json.Marshal(HMIState{})
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

func TestStateEndpointReturnsEmptyArraysForNoActiveItems(t *testing.T) {
	controller, err := newMemoryController("", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	controller.state = HMIState{
		Revision:  1,
		UpdatedAt: time.Unix(1, 0).UTC(),
		Mode:      "auto",
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	newAPIHandler(controller).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("state status = %d, want 200", response.Code)
	}
	var payload struct {
		State map[string]json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"bins", "alarms", "history"} {
		if got := string(payload.State[field]); got != "[]" {
			t.Fatalf("%s = %s, want []", field, got)
		}
	}
}
