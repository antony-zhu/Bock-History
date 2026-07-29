package main

import (
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"block.local/block-agent/internal/sshbootstrap"
)

var version = "development"

func main() {
	configPath := flag.String("config", "", "absolute path to ssh-bootstrapd JSON config")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if *configPath == "" {
		log.Fatal("-config is required")
	}
	config, err := sshbootstrap.LoadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	adminKey, err := sshbootstrap.LoadAdministratorPublicKey(config.AdministratorPublicKeyPath)
	if err != nil {
		log.Fatal("administrator verifier public key could not be loaded")
	}
	caSigner, err := sshbootstrap.LoadSSHSigner(config.SSHUserCAPrivateKeyPath)
	if err != nil {
		log.Fatal(err)
	}
	if err := sshbootstrap.ValidateSSHCAKeyPair(caSigner, config.SSHUserCAPublicKeyPath); err != nil {
		log.Fatal(err)
	}
	issuer, err := sshbootstrap.NewCertificateIssuer(caSigner)
	if err != nil {
		log.Fatal(err)
	}
	nonces, err := sshbootstrap.OpenNonceStore(config.NonceDatabasePath)
	if err != nil {
		log.Fatal("nonce database could not be opened")
	}
	defer nonces.Close()
	handler, err := sshbootstrap.NewHandler(config, adminKey, nonces, issuer)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{
		Addr:      config.ListenAddress,
		Handler:   handler,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	log.Printf("ssh-bootstrapd version=%s target=BLOCK listen=%s", version, config.ListenAddress)
	if err := sshbootstrap.ServeTLSOnly(server, config.TLSCertificatePath, config.TLSPrivateKeyPath); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func init() {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ldate | log.Ltime | log.LUTC)
}
