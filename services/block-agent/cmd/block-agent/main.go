package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	agentapp "block.local/block-agent/internal/agent"
	"block.local/block-agent/internal/auth"
	"block.local/block-agent/internal/mqttv2"
	"block.local/block-agent/internal/sshbootstrap"
	"block.local/block-agent/internal/storage"
)

func main() {
	localHTTPAddress := flag.String("local-http-address", "127.0.0.1:8080", "loopback HTTP and WebSocket address")
	stateDatabase := flag.String("state-db", "data/block.db", "local SQLite database path")
	hmiStaticDirectory := flag.String("hmi-static-dir", "", "directory containing the local HMI static build")
	maintenanceHTTPSAddress := flag.String("maintenance-https-address", "", "maintenance HTTPS address, such as 0.0.0.0:8443")
	maintenanceTLSCertificate := flag.String("maintenance-tls-cert", "", "maintenance HTTPS certificate path")
	maintenanceTLSPrivateKey := flag.String("maintenance-tls-key", "", "maintenance HTTPS private key path")
	maintenanceSuperKeyHash := flag.String("maintenance-super-key-hash", "", "Argon2 hash of the maintenance super key")
	maintenanceAuthorizedKeys := flag.String("maintenance-authorized-keys", "", "managed SSH authorized_keys path")
	maintenanceDeviceID := flag.String("maintenance-device-id", "block-0001", "device ID used in generated SSH key filenames")
	maintenanceAdvertisedHost := flag.String("maintenance-advertised-host", "", "Block host or IP returned with the SSH key")
	mqttsV2Enabled := flag.Bool("mqtts-v2-enabled", false, "enable optional MQTTS v2 current-state and alarm-history sync")
	mqttsV2Endpoint := flag.String("mqtts-v2-endpoint", "", "MQTTS v2 broker endpoint, mqtts://HOST:8883")
	mqttsV2CA := flag.String("mqtts-v2-ca", "", "MQTTS v2 CA certificate path")
	mqttsV2ClientCert := flag.String("mqtts-v2-client-cert", "", "MQTTS v2 Block client certificate path")
	mqttsV2ClientKey := flag.String("mqtts-v2-client-key", "", "MQTTS v2 Block client private-key path")
	mqttsV2Principal := flag.String("mqtts-v2-principal", "", "MQTTS v2 mTLS principal and client certificate CN")
	mqttsV2SiteID := flag.String("mqtts-v2-site-id", "", "MQTTS v2 source site ID")
	mqttsV2BlockID := flag.String("mqtts-v2-block-id", "", "MQTTS v2 source Block ID")
	mqttsV2DeviceID := flag.String("mqtts-v2-device-id", "", "MQTTS v2 source device ID")
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
	authService, err := auth.NewService(store, time.Now, nil)
	if err != nil {
		log.Fatalf("initialize Block authentication: %v", err)
	}
	defer authService.Close()
	runtime, err := agentapp.NewLocalRuntimeWithOptions(*localHTTPAddress, time.Now, nil, hmi, authService, agentapp.RuntimeOptions{
		AlarmStore:      store,
		PLCEndpointPath: filepath.Join(filepath.Dir(*stateDatabase), "plc-endpoint.json"),
		MQTT: agentapp.MQTTOptions{
			Enabled: *mqttsV2Enabled,
			Connection: mqttv2.ConnectionConfig{
				Endpoint: *mqttsV2Endpoint, CAFile: *mqttsV2CA,
				ClientCertFile: *mqttsV2ClientCert, ClientKeyFile: *mqttsV2ClientKey,
				Principal: *mqttsV2Principal,
				Source:    mqttv2.Source{SiteID: *mqttsV2SiteID, BlockID: *mqttsV2BlockID, DeviceID: *mqttsV2DeviceID},
			},
		},
	})
	if err != nil {
		log.Fatalf("initialize Block local runtime: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var maintenance *http.Server
	if *maintenanceHTTPSAddress != "" {
		if *maintenanceTLSCertificate == "" || *maintenanceTLSPrivateKey == "" {
			log.Fatal("maintenance TLS certificate and private key are required when maintenance HTTPS is enabled")
		}
		handler, err := sshbootstrap.NewKeyHandler(sshbootstrap.KeyConfig{
			SuperKeyHash: *maintenanceSuperKeyHash, AuthorizedKeysPath: *maintenanceAuthorizedKeys,
			DeviceID: *maintenanceDeviceID, AdvertisedHost: *maintenanceAdvertisedHost,
		})
		if err != nil {
			log.Fatalf("initialize maintenance HTTPS: %v", err)
		}
		maintenance = &http.Server{
			Addr: *maintenanceHTTPSAddress, Handler: handler,
			ReadHeaderTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second,
			TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		}
		go func() {
			if err := sshbootstrap.ServeTLSOnly(maintenance, *maintenanceTLSCertificate, *maintenanceTLSPrivateKey); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("maintenance HTTPS stopped: %v", err)
				stop()
			}
		}()
		defer func() {
			shutdownContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = maintenance.Shutdown(shutdownContext)
		}()
	}
	log.Printf("block-agent listening on %s", *localHTTPAddress)
	if err := runtime.Run(ctx); err != nil {
		log.Fatalf("run Block local runtime: %v", err)
	}
}
