package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"
	"time"

	agentapp "block.local/block-agent/internal/agent"
)

func main() {
	localHTTPAddress := flag.String("local-http-address", "127.0.0.1:8080", "loopback HTTP and WebSocket address")
	flag.Parse()
	runtime, err := agentapp.NewLocalRuntime(*localHTTPAddress, time.Now)
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
