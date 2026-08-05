package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web/*
var embeddedWebFiles embed.FS

type Application struct {
	server        *Server
	points        *PointManager
	modbusAddress string
}

func NewApplication(server *Server, points *PointManager, modbusAddress string) *Application {
	return &Application{
		server:        server,
		points:        points,
		modbusAddress: modbusAddress,
	}
}

func (a *Application) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/points", a.handlePoints)
	mux.HandleFunc("/api/points/", a.handlePoint)
	mux.HandleFunc("/api/events", a.handleEvents)

	webFiles, err := fs.Sub(embeddedWebFiles, "web")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServer(http.FS(webFiles)))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/api/") {
			writer.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(writer, request)
	})
}

func (a *Application) handleStatus(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		ModbusAddress     string `json:"modbusAddress"`
		UnitID            byte   `json:"unitId"`
		ActiveConnections int64  `json:"activeConnections"`
	}{
		ModbusAddress:     a.modbusAddress,
		UnitID:            a.server.unitID,
		ActiveConnections: a.server.ActiveConnections(),
	})
}

func (a *Application) handlePoints(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		writeJSON(writer, http.StatusOK, a.points.List())
	case http.MethodPost:
		var input PointInput
		if err := decodeJSON(request, &input); err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		point, err := a.points.Add(input)
		if err != nil {
			writePointError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, point)
	default:
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *Application) handlePoint(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/api/points/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(writer, request)
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "value" {
		a.handlePointValue(writer, request, id)
		return
	}
	if len(parts) != 1 {
		http.NotFound(writer, request)
		return
	}

	switch request.Method {
	case http.MethodPut:
		var input PointInput
		if err := decodeJSON(request, &input); err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		point, err := a.points.Update(id, input)
		if err != nil {
			writePointError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, point)
	case http.MethodDelete:
		if err := a.points.Delete(id); err != nil {
			writePointError(writer, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	default:
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *Application) handlePointValue(writer http.ResponseWriter, request *http.Request, id string) {
	if request.Method != http.MethodPut {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Value json.RawMessage `json:"value"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	point, err := a.points.SetValue(id, input.Value)
	if err != nil {
		writePointError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, point)
}

func (a *Application) handleEvents(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		http.Error(writer, "streaming is unavailable", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	writer.Header().Set("Connection", "keep-alive")
	events, unsubscribe := a.points.SubscribeEvents()
	defer unsubscribe()
	writer.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(writer, "event: ready\ndata: {\"type\":\"ready\"}\n\n"); err != nil {
		return
	}
	flusher.Flush()

	for {
		select {
		case <-request.Context().Done():
			return
		case event := <-events:
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(writer, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func decodeJSON(request *http.Request, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("request body must contain one JSON value")
	}
	return nil
}

func writePointError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errPointNotFound):
		writeAPIError(writer, http.StatusNotFound, err)
	case errors.Is(err, errPointNotWritable):
		writeAPIError(writer, http.StatusForbidden, err)
	default:
		var validation validationError
		if errors.As(err, &validation) {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		writeAPIError(writer, http.StatusInternalServerError, err)
	}
}

func writeAPIError(writer http.ResponseWriter, status int, err error) {
	http.Error(writer, err.Error(), status)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
