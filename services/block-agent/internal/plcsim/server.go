package plcsim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"block.local/block-agent/internal/plccontract"
	"block.local/block-agent/internal/socketperm"
)

type Server struct {
	engine             *Engine
	ioSocket           string
	ioSocketGroup      string
	controlSocket      string
	controlSocketGroup string
	ioServer           *http.Server
	controlServer      *http.Server
}

func NewServer(engine *Engine, ioSocket, ioSocketGroup, controlSocket, controlSocketGroup string) *Server {
	return &Server{
		engine: engine, ioSocket: ioSocket, ioSocketGroup: ioSocketGroup,
		controlSocket: controlSocket, controlSocketGroup: controlSocketGroup,
	}
}

func (s *Server) Serve(ctx context.Context) error {
	ioListener, err := socketperm.Listen(s.ioSocket, s.ioSocketGroup)
	if err != nil {
		return fmt.Errorf("listen simulator IO socket: %w", err)
	}
	controlListener, err := socketperm.Listen(s.controlSocket, s.controlSocketGroup)
	if err != nil {
		_ = ioListener.Close()
		_ = os.Remove(s.ioSocket)
		return fmt.Errorf("listen simulator control socket: %w", err)
	}
	defer func() {
		_ = ioListener.Close()
		_ = controlListener.Close()
		_ = os.Remove(s.ioSocket)
		_ = os.Remove(s.controlSocket)
	}()
	_ = os.Chmod(s.ioSocket, 0o660)
	_ = os.Chmod(s.controlSocket, 0o660)

	s.ioServer = &http.Server{Handler: s.ioHandler(), ReadHeaderTimeout: 2 * time.Second}
	s.controlServer = &http.Server{Handler: s.controlHandler(), ReadHeaderTimeout: 2 * time.Second}
	errorsChannel := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		errorsChannel <- s.ioServer.Serve(ioListener)
	}()
	go func() {
		defer wait.Done()
		errorsChannel <- s.controlServer.Serve(controlListener)
	}()

	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.ioServer.Shutdown(shutdownContext)
		_ = s.controlServer.Shutdown(shutdownContext)
		wait.Wait()
		return nil
	case err := <-errorsChannel:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) ioHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		if !s.delay(r.Context()) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"snapshot": s.engine.Snapshot()})
	})
	mux.HandleFunc("/v1/commands", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}
		var command plccontract.Command
		if err := decodeStrict(r, &command); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_command", err.Error())
			return
		}
		result, err := s.engine.ApplyCommand(command)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "simulator_failure", err.Error())
			return
		}
		delay, appliedTimeout := s.engine.ResponseBehavior()
		if delay > 0 && !waitContext(r.Context(), delay) {
			return
		}
		if appliedTimeout && result.Status == plccontract.CommandExecuted {
			<-r.Context().Done()
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": result})
	})
	return mux
}

func (s *Server) controlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/faults", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}
		var request plccontract.FaultRequest
		if err := decodeStrict(r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_fault", err.Error())
			return
		}
		snapshot, err := s.engine.SetFault(request)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "fault_rejected", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"snapshot": snapshot})
	})
	mux.HandleFunc("/v1/restart", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}
		snapshot, err := s.engine.RestartSession()
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "restart_rejected", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"snapshot": snapshot})
	})
	return mux
}

func (s *Server) delay(ctx context.Context) bool {
	delay, _ := s.engine.ResponseBehavior()
	return delay <= 0 || waitContext(ctx, delay)
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func decodeStrict(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("only one JSON value is allowed")
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, plccontract.ErrorEnvelope{Error: plccontract.ErrorDetail{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
