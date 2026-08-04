package main

import (
	"context"
	"flag"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	agentapp "block.local/block-agent/internal/agent"
	"block.local/block-agent/internal/auth"
	"block.local/block-agent/internal/storage"
)

func main() {
	localHTTPAddress := flag.String("local-http-address", "127.0.0.1:8080", "loopback HTTP and WebSocket address")
	stateDatabase := flag.String("state-db", "data/block.db", "local SQLite database path")
	hmiStaticDirectory := flag.String("hmi-static-dir", "", "directory containing the local HMI static build")
	flag.Parse()
	var hmi fs.FS
	if *hmiStaticDirectory != "" {
		info, err := os.Stat(*hmiStaticDirectory)
		if err != nil {
			log.Fatalf("open HMI static directory: %v", err)
		}
		if !info.IsDir() {
			log.Fatalf("open HMI static directory: %s is not a directory", *hmiStaticDirectory)
		}
		hmi = os.DirFS(*hmiStaticDirectory)
	}
	store, err := storage.Open(*stateDatabase, time.Now)
	if err != nil {
		log.Fatalf("open Block local database: %v", err)
	}
	defer store.Close()
	var runtime *agentapp.Runtime
	authService, err := auth.NewService(store, time.Now, func(auth.Session) {
		if runtime != nil {
			runtime.StopSession()
		}
	})
	if err != nil {
		log.Fatalf("initialize Block authentication: %v", err)
	}
	defer authService.Close()
	runtime, err = agentapp.NewLocalRuntimeWithServices(*localHTTPAddress, time.Now, nil, hmi, authService)
	if err != nil {
		log.Fatalf("initialize Block local runtime: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("block-agent listening on %s", *localHTTPAddress)
	if err := runtime.Run(ctx); err != nil {
		log.Fatalf("run Block local runtime: %v", err)
	}
}
