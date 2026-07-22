package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const defaultBasePath = "/block-apple-style"

//go:embed index.html assets/*
var embeddedFiles embed.FS

func main() {
	addr := os.Getenv("BLOCK_HMI_ADDR")
	if addr == "" {
		addr = "0.0.0.0:8080"
	}

	handler, err := newHandler()
	if err != nil {
		log.Fatalf("prepare HMI server: %v", err)
	}

	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, syscall.SIGINT, syscall.SIGTERM)

	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("Block HMI Go server listening on %s", addr)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case sig := <-shutdownSignals:
		log.Printf("received %s; shutting down", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Fatalf("graceful shutdown: %v", err)
		}
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve HMI: %v", err)
		}
	}
}

func newHandler() (http.Handler, error) {
	basePath := os.Getenv("BLOCK_HMI_BASE_PATH")
	if basePath == "" {
		basePath = defaultBasePath
	}
	controller, err := newMemoryController(os.Getenv("BLOCK_HMI_DATA_FILE"), time.Now)
	if err != nil {
		return nil, fmt.Errorf("prepare HMI controller: %w", err)
	}
	return newHandlerWithController(controller, basePath)
}

func newHandlerWithController(controller HMIController, basePath string) (http.Handler, error) {
	if controller == nil {
		return nil, errors.New("HMI controller is required")
	}
	content, err := fs.Sub(embeddedFiles, ".")
	if err != nil {
		return nil, fmt.Errorf("open embedded filesystem: %w", err)
	}
	basePath, err = normalizeBasePath(basePath)
	if err != nil {
		return nil, err
	}

	files := http.FileServer(http.FS(content))
	api := newAPIHandler(controller)
	mux := http.NewServeMux()

	registerHealth := func(path string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write([]byte("ok\n"))
			}
		})
	}

	registerHealth("/healthz")
	mux.Handle("/api/v1", api)
	mux.Handle("/api/v1/", api)

	if basePath != "" {
		registerHealth(basePath + "/healthz")
		mux.Handle(basePath+"/api/v1", http.StripPrefix(basePath, api))
		mux.Handle(basePath+"/api/v1/", http.StripPrefix(basePath, api))
		mux.HandleFunc(basePath, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, basePath+"/", http.StatusTemporaryRedirect)
		})
		mux.Handle(basePath+"/", http.StripPrefix(basePath, files))
	}
	// Root serving remains available for a reverse proxy that strips the public
	// base path before forwarding to this process.
	mux.Handle("/", files)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.URL.Path != "/healthz" && r.URL.Path != basePath+"/healthz" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		mux.ServeHTTP(w, r)
	}), nil
}

func normalizeBasePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "/" {
		return "", nil
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	value = strings.TrimRight(value, "/")
	if strings.Contains(value, "//") || strings.ContainsAny(value, "?#") {
		return "", fmt.Errorf("invalid BLOCK_HMI_BASE_PATH %q", value)
	}
	for _, segment := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("invalid BLOCK_HMI_BASE_PATH %q", value)
		}
	}
	return value, nil
}
