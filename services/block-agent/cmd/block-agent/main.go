package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"
	"time"

	agentapp "block.local/block-agent/internal/agent"
	"block.local/block-agent/internal/config"
)

func main() {
	configPath := flag.String("config", "", "absolute path to the block-agent JSON configuration")
	flag.Parse()
	if *configPath == "" {
		log.Fatal("-config is required")
	}
	cfg, err := config.LoadAgent(*configPath)
	if err != nil {
		log.Fatalf("load block-agent config: %v", err)
	}
	runtime, err := agentapp.Open(cfg, time.Now)
	if err != nil {
		log.Fatalf("initialize block-agent: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("block-agent %s/%s/%s using %s adapter and Unix socket %s", cfg.SiteID, cfg.BlockID, cfg.DeviceID, cfg.Adapter.Type, cfg.LocalAPISocket)
	if err := runtime.Run(ctx); err != nil {
		log.Fatalf("run block-agent: %v", err)
	}
}
