package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"
	"time"

	"block.local/block-agent/internal/config"
	"block.local/block-agent/internal/plcsim"
)

func main() {
	configPath := flag.String("config", "", "absolute path to the simulator JSON configuration")
	flag.Parse()
	if *configPath == "" {
		log.Fatal("-config is required")
	}
	cfg, err := config.LoadSimulator(*configPath)
	if err != nil {
		log.Fatalf("load simulator config: %v", err)
	}
	period, _ := cfg.SampleDuration()
	engine, err := plcsim.Open(cfg, time.Now)
	if err != nil {
		log.Fatalf("open simulator state: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		ticker := time.NewTicker(period)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := engine.Tick(); err != nil {
					log.Printf("simulator sample failed: %v", err)
				}
			}
		}
	}()
	server := plcsim.NewServer(engine, cfg.IOSocket, cfg.IOSocketGroup, cfg.ControlSocket, cfg.ControlSocketGroup)
	log.Printf("PLC simulator serving Unix sockets %s and %s", cfg.IOSocket, cfg.ControlSocket)
	if err := server.Serve(ctx); err != nil {
		log.Fatalf("serve simulator: %v", err)
	}
}
