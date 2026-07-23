package main

import (
	"context"
	"crypto/tls"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultBasePath     = "/block-apple-style"
	defaultAgentTimeout = 8 * time.Second
)

//go:embed index.html assets/*
var embeddedFiles embed.FS

func main() {
	serverConfig, err := loadServerConfig()
	if err != nil {
		log.Fatalf("load HMI server config: %v", err)
	}

	handler, err := newHandler()
	if err != nil {
		log.Fatalf("prepare HMI server: %v", err)
	}

	server := newHMIServer(serverConfig.Addr, handler)

	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, syscall.SIGINT, syscall.SIGTERM)

	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("Block HMI HTTPS server listening on %s", serverConfig.Addr)
		serverErrors <- server.ListenAndServeTLS(serverConfig.CertFile, serverConfig.KeyFile)
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

type hmiServerConfig struct {
	Addr     string
	CertFile string
	KeyFile  string
}

func newHMIServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		WriteTimeout:      30 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			MaxVersion: tls.VersionTLS13,
		},
	}
}

func loadServerConfig() (hmiServerConfig, error) {
	value := hmiServerConfig{
		Addr:     strings.TrimSpace(os.Getenv("BLOCK_HMI_ADDR")),
		CertFile: strings.TrimSpace(os.Getenv("BLOCK_HMI_TLS_CERT")),
		KeyFile:  strings.TrimSpace(os.Getenv("BLOCK_HMI_TLS_KEY")),
	}
	if value.Addr == "" {
		value.Addr = "127.0.0.1:8443"
	}
	if value.Addr != "127.0.0.1:8443" {
		return hmiServerConfig{}, errors.New("BLOCK_HMI_ADDR must be exactly 127.0.0.1:8443")
	}
	if value.CertFile == "" || !filepath.IsAbs(value.CertFile) {
		return hmiServerConfig{}, errors.New("BLOCK_HMI_TLS_CERT must be an absolute path")
	}
	if value.KeyFile == "" || !filepath.IsAbs(value.KeyFile) {
		return hmiServerConfig{}, errors.New("BLOCK_HMI_TLS_KEY must be an absolute path")
	}
	return value, nil
}

func newHandler() (http.Handler, error) {
	basePath := os.Getenv("BLOCK_HMI_BASE_PATH")
	if basePath == "" {
		basePath = defaultBasePath
	}
	timeout := defaultAgentTimeout
	var err error
	if raw := strings.TrimSpace(os.Getenv("BLOCK_HMI_AGENT_TIMEOUT")); raw != "" {
		timeout, err = time.ParseDuration(raw)
		if err != nil || timeout <= 0 {
			return nil, errors.New("BLOCK_HMI_AGENT_TIMEOUT must be a positive duration")
		}
	}
	controller, err := newAgentController(strings.TrimSpace(os.Getenv("BLOCK_HMI_AGENT_SOCKET")), timeout)
	if err != nil {
		return nil, fmt.Errorf("prepare HMI controller: %w", err)
	}
	sourceContext, sourceCancel := context.WithTimeout(context.Background(), timeout)
	defer sourceCancel()
	source, err := controller.SourceInfo(sourceContext)
	if err != nil {
		controller.Close()
		return nil, fmt.Errorf("verify block-agent data source: %w", err)
	}
	return newHandlerWithOptions(controller, basePath, source.Simulation)
}

func newHandlerWithController(controller HMIController, basePath string) (http.Handler, error) {
	return newHandlerWithOptions(controller, basePath, false)
}

func newHandlerWithOptions(controller HMIController, basePath string, simulation bool) (http.Handler, error) {
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
		if isIndexPath(r.URL.Path, basePath) {
			serveIndex(w, r, simulation)
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}

func isIndexPath(path, basePath string) bool {
	return path == "/" || path == "/index.html" ||
		(basePath != "" && (path == basePath+"/" || path == basePath+"/index.html"))
}

func serveIndex(w http.ResponseWriter, r *http.Request, simulation bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	contents, err := embeddedFiles.ReadFile("index.html")
	if err != nil {
		http.Error(w, "index unavailable", http.StatusInternalServerError)
		return
	}
	contents = []byte(strings.ReplaceAll(string(contents), "__BLOCK_HMI_SOURCE_SIMULATION__", strconv.FormatBool(simulation)))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(contents)
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
