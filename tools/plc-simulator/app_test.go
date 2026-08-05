package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPointCRUDValidationAndJSONReload(t *testing.T) {
	server := NewServer(1, map[uint16]uint16{504: 0xA001, 600: 42}, io.Discard)
	file := filepath.Join(t.TempDir(), "points.json")
	points, err := NewPointManager(file, server)
	if err != nil {
		t.Fatal(err)
	}

	first, err := points.Add(PointInput{Name: "允许启动", Type: pointTypeBool, Address: "D504.1", Writable: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.Address != "D504.1" {
		t.Fatalf("created point = %+v", first)
	}
	if _, err := points.Add(PointInput{Name: "允许启动", Type: pointTypeBool, Address: "D504.2"}); !isValidationError(err) {
		t.Fatalf("duplicate name error = %v, want validation error", err)
	}
	if _, err := points.Add(PointInput{Name: "重复位置", Type: pointTypeBool, Address: "D504.1"}); !isValidationError(err) {
		t.Fatalf("duplicate type/address error = %v, want validation error", err)
	}
	for _, input := range []PointInput{
		{Name: "缺少位", Type: pointTypeBool, Address: "D504"},
		{Name: "整字带位", Type: pointTypeInt16, Address: "D504.1"},
		{Name: "超范围", Type: pointTypeUint16, Address: "D65536"},
		{Name: "", Type: pointTypeUint16, Address: "D504"},
	} {
		if _, err := points.Add(input); !isValidationError(err) {
			t.Fatalf("input %+v error = %v, want validation error", input, err)
		}
	}

	before := server.ReadRegister(504)
	updated, err := points.Update(first.ID, PointInput{Name: "允许启动（已移动）", Type: pointTypeBool, Address: "D505.1", Writable: true})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Address != "D505.1" || server.ReadRegister(504) != before {
		t.Fatalf("update changed the register: point=%+v D504=%#04x want %#04x", updated, server.ReadRegister(504), before)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var persisted []Point
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || persisted[0].ID != first.ID {
		t.Fatalf("persisted definitions = %+v", persisted)
	}

	external := []Point{{
		ID:       "external-point",
		Name:     "外部重载",
		Type:     pointTypeUint16,
		Address:  "D600",
		Writable: false,
	}}
	externalData, err := json.Marshal(external)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, externalData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := points.Reload(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := points.Get("external-point")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := reloaded.Value, uint16(42); got != want {
		t.Fatalf("reloaded point value = %v (%T), want %v", got, got, want)
	}
}

func TestHTTPPointCRUDAndValidation(t *testing.T) {
	_, _, httpServer := newTestApplication(t, nil)
	defer httpServer.Close()

	createdResponse := apiRequest(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/points", PointInput{
		Name: "运行标志", Type: pointTypeBool, Address: "D504.0", Description: "运行中", Writable: true,
	})
	if createdResponse.StatusCode != http.StatusCreated {
		t.Fatalf("POST status = %d, want %d", createdResponse.StatusCode, http.StatusCreated)
	}
	if got := createdResponse.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("POST Cache-Control = %q, want no-store", got)
	}
	created := decodeResponse[PointView](t, createdResponse)
	if created.ID == "" || created.Name != "运行标志" {
		t.Fatalf("created point = %+v", created)
	}

	duplicate := apiRequest(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/points", PointInput{
		Name: "运行标志", Type: pointTypeBool, Address: "D504.1",
	})
	if duplicate.StatusCode != http.StatusBadRequest {
		duplicate.Body.Close()
		t.Fatalf("duplicate status = %d, want %d", duplicate.StatusCode, http.StatusBadRequest)
	}
	duplicate.Body.Close()

	updatedResponse := apiRequest(t, httpServer.Client(), http.MethodPut, httpServer.URL+"/api/points/"+created.ID, PointInput{
		Name: "运行标志", Type: pointTypeBool, Address: "D504.0", Description: "已编辑", Writable: false,
	})
	if updatedResponse.StatusCode != http.StatusOK {
		updatedResponse.Body.Close()
		t.Fatalf("PUT status = %d, want %d", updatedResponse.StatusCode, http.StatusOK)
	}
	updated := decodeResponse[PointView](t, updatedResponse)
	if updated.Description != "已编辑" || updated.Writable {
		t.Fatalf("updated point = %+v", updated)
	}

	listResponse := apiRequest(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/points", nil)
	list := decodeResponse[[]PointView](t, listResponse)
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("GET points = %+v", list)
	}

	statusResponse := apiRequest(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/status", nil)
	status := decodeResponse[struct {
		ModbusAddress string `json:"modbusAddress"`
		UnitID        byte   `json:"unitId"`
	}](t, statusResponse)
	if status.ModbusAddress == "" || status.UnitID != 1 {
		t.Fatalf("status = %+v", status)
	}

	deleteResponse := apiRequest(t, httpServer.Client(), http.MethodDelete, httpServer.URL+"/api/points/"+created.ID, nil)
	if deleteResponse.StatusCode != http.StatusNoContent {
		deleteResponse.Body.Close()
		t.Fatalf("DELETE status = %d, want %d", deleteResponse.StatusCode, http.StatusNoContent)
	}
	deleteResponse.Body.Close()
}

func TestEmbeddedManagementPageIsServed(t *testing.T) {
	_, _, httpServer := newTestApplication(t, nil)
	defer httpServer.Close()

	pageResponse := apiRequest(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/", nil)
	page, err := io.ReadAll(pageResponse.Body)
	pageResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if pageResponse.StatusCode != http.StatusOK || !bytes.Contains(page, []byte("PLC 模拟器")) {
		t.Fatalf("management page status=%d body=%q", pageResponse.StatusCode, page)
	}

	scriptResponse := apiRequest(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/app.js", nil)
	script, err := io.ReadAll(scriptResponse.Body)
	scriptResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if scriptResponse.StatusCode != http.StatusOK || !bytes.Contains(script, []byte("EventSource")) {
		t.Fatalf("management script status=%d", scriptResponse.StatusCode)
	}
}

func TestPageWritesEnforceWritableAndPreserveBoolNeighbors(t *testing.T) {
	server, points, httpServer := newTestApplication(t, map[uint16]uint16{504: 0xA001})
	defer httpServer.Close()

	boolPoint, err := points.Add(PointInput{Name: "D504.1", Type: pointTypeBool, Address: "D504.1", Writable: true})
	if err != nil {
		t.Fatal(err)
	}
	intPoint, err := points.Add(PointInput{Name: "有符号", Type: pointTypeInt16, Address: "D505", Writable: true})
	if err != nil {
		t.Fatal(err)
	}
	uintPoint, err := points.Add(PointInput{Name: "无符号", Type: pointTypeUint16, Address: "D506", Writable: true})
	if err != nil {
		t.Fatal(err)
	}
	readonly, err := points.Add(PointInput{Name: "只读", Type: pointTypeBool, Address: "D504.2", Writable: false})
	if err != nil {
		t.Fatal(err)
	}

	putPointValue(t, httpServer, boolPoint.ID, true, http.StatusOK)
	if got, want := server.ReadRegister(504), uint16(0xA003); got != want {
		t.Fatalf("set bool result = %#04x, want %#04x", got, want)
	}
	putPointValue(t, httpServer, boolPoint.ID, false, http.StatusOK)
	if got, want := server.ReadRegister(504), uint16(0xA001); got != want {
		t.Fatalf("clear bool result = %#04x, want %#04x", got, want)
	}
	putPointValue(t, httpServer, intPoint.ID, -2, http.StatusOK)
	putPointValue(t, httpServer, uintPoint.ID, 65535, http.StatusOK)
	if got, want := server.ReadRegister(505), uint16(0xFFFE); got != want {
		t.Fatalf("int16 register = %#04x, want %#04x", got, want)
	}
	if got, want := server.ReadRegister(506), uint16(0xFFFF); got != want {
		t.Fatalf("uint16 register = %#04x, want %#04x", got, want)
	}
	putPointValue(t, httpServer, readonly.ID, true, http.StatusForbidden)
}

func TestFC22ChangesMappedPointsThroughSSEAndAPI(t *testing.T) {
	server, points, httpServer := newTestApplication(t, nil)
	defer httpServer.Close()
	for _, input := range []PointInput{
		{Name: "位一", Type: pointTypeBool, Address: "D504.1"},
		{Name: "位二", Type: pointTypeBool, Address: "D504.2"},
		{Name: "整字", Type: pointTypeUint16, Address: "D504"},
	} {
		if _, err := points.Add(input); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" {
		t.Fatalf("SSE content type = %q", response.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(response.Body)
	if data := readSSEData(t, reader); data != `{"type":"ready"}` {
		t.Fatalf("ready data = %q", data)
	}

	pdu := []byte{functionMaskWriteRegister, 0x01, 0xF8, 0xFF, 0xFD, 0x00, 0x02}
	if got, _, _, result := server.handlePDU(pdu); result != "ok" || !bytes.Equal(got, pdu) {
		t.Fatalf("FC22 result=%q response=% X", result, got)
	}
	var event simulatorEvent
	if err := json.Unmarshal([]byte(readSSEData(t, reader)), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "values" || len(event.Points) != 3 {
		t.Fatalf("SSE event = %+v", event)
	}
	values := make(map[string]any)
	for _, point := range event.Points {
		if point.Source != "modbus" {
			t.Fatalf("SSE source for %s = %q, want modbus", point.Name, point.Source)
		}
		values[point.Name] = point.Value
	}
	if values["位一"] != true || values["位二"] != false || values["整字"] != float64(2) {
		t.Fatalf("SSE values = %#v", values)
	}

	apiResponse := apiRequest(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/points", nil)
	apiPoints := decodeResponse[[]PointView](t, apiResponse)
	if len(apiPoints) != 3 {
		t.Fatalf("API points = %+v", apiPoints)
	}
	for _, point := range apiPoints {
		if point.Source != "modbus" {
			t.Fatalf("API source for %s = %q, want modbus", point.Name, point.Source)
		}
	}
}

func TestConcurrentModbusAndPageWrites(t *testing.T) {
	server := NewServer(1, nil, io.Discard)
	points, err := NewPointManager(filepath.Join(t.TempDir(), "points.json"), server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := points.Add(PointInput{Name: "页面位", Type: pointTypeBool, Address: "D504.1", Writable: true}); err != nil {
		t.Fatal(err)
	}
	word, err := points.Add(PointInput{Name: "页面整字", Type: pointTypeUint16, Address: "D504", Writable: true})
	if err != nil {
		t.Fatal(err)
	}

	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for round := 0; round < 100; round++ {
				if worker%2 == 0 {
					_, _, _, result := server.handlePDU([]byte{functionMaskWriteRegister, 0x01, 0xF8, 0xFF, 0xFD, 0x00, byte((round % 2) * 2)})
					if result != "ok" {
						t.Errorf("FC22 result = %q", result)
					}
				} else if _, err := points.SetValue(word.ID, json.RawMessage(strconv.Itoa(worker*100+round))); err != nil {
					t.Errorf("page write: %v", err)
				}
				_ = points.List()
			}
		}(worker)
	}
	workers.Wait()
	if len(points.List()) != 2 {
		t.Fatalf("point count changed during concurrent writes")
	}
}

func newTestApplication(t *testing.T, initial map[uint16]uint16) (*Server, *PointManager, *httptest.Server) {
	t.Helper()
	server := NewServer(1, initial, io.Discard)
	points, err := NewPointManager(filepath.Join(t.TempDir(), "points.json"), server)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(NewApplication(server, points, "127.0.0.1:1502").Handler())
	return server, points, httpServer
}

func apiRequest(t *testing.T, client *http.Client, method, url string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeResponse[T any](t *testing.T, response *http.Response) T {
	t.Helper()
	defer response.Body.Close()
	var value T
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func putPointValue(t *testing.T, httpServer *httptest.Server, id string, value any, wantStatus int) {
	t.Helper()
	response := apiRequest(t, httpServer.Client(), http.MethodPut, httpServer.URL+"/api/points/"+id+"/value", map[string]any{"value": value})
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("PUT value status = %d, want %d: %s", response.StatusCode, wantStatus, body)
	}
}

func readSSEData(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	deadline := time.After(2 * time.Second)
	data := ""
	for {
		result := make(chan struct {
			line string
			err  error
		}, 1)
		go func() {
			line, err := reader.ReadString('\n')
			result <- struct {
				line string
				err  error
			}{line: line, err: err}
		}()
		select {
		case read := <-result:
			if read.err != nil {
				t.Fatal(read.err)
			}
			line := strings.TrimRight(read.line, "\r\n")
			if line == "" {
				return data
			}
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
		case <-deadline:
			t.Fatal("timed out waiting for SSE data")
		}
	}
}

func isValidationError(err error) bool {
	var validation validationError
	return errors.As(err, &validation)
}
