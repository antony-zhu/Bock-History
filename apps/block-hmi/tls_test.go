package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestTLSValidAndInvalidPeersAndPlaintextRejection(t *testing.T) {
	now := time.Now().UTC()
	caCertificate, caKey, caPEM := makeCA(t, now)
	validCertificate := makeServerCertificate(t, caCertificate, caKey, now.Add(-time.Hour), now.Add(time.Hour))
	handler := newTestHandler(t, "")
	address, stop := startTLSServer(t, handler, validCertificate)
	defer stop()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("append test CA")
	}

	validClient := tlsHTTPClient(roots, "", tls.VersionTLS12, 0)
	response, err := validClient.Get("https://" + address + "/healthz")
	if err != nil {
		t.Fatalf("trusted TLS request failed: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("trusted TLS status = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	wrongCAClient := tlsHTTPClient(x509.NewCertPool(), "", tls.VersionTLS12, 0)
	if _, err := wrongCAClient.Get("https://" + address + "/healthz"); err == nil {
		t.Fatal("wrong CA was accepted")
	}
	wrongHostClient := tlsHTTPClient(roots, "wrong.invalid", tls.VersionTLS12, 0)
	if _, err := wrongHostClient.Get("https://" + address + "/healthz"); err == nil {
		t.Fatal("wrong hostname was accepted")
	}
	legacyClient := tlsHTTPClient(roots, "", tls.VersionTLS10, tls.VersionTLS11)
	if _, err := legacyClient.Get("https://" + address + "/healthz"); err == nil {
		t.Fatal("TLS 1.0/1.1 client was accepted")
	}

	plainClient := &http.Client{Timeout: time.Second}
	plainResponse, plainErr := plainClient.Get("http://" + address + "/healthz")
	if plainErr == nil {
		defer plainResponse.Body.Close()
		if plainResponse.StatusCode >= 200 && plainResponse.StatusCode < 400 {
			t.Fatalf("plaintext received business/redirect response %d", plainResponse.StatusCode)
		}
		if plainResponse.Header.Get("Location") != "" {
			t.Fatalf("plaintext was redirected to %q", plainResponse.Header.Get("Location"))
		}
	}

	expiredCertificate := makeServerCertificate(t, caCertificate, caKey, now.Add(-2*time.Hour), now.Add(-time.Hour))
	expiredAddress, stopExpired := startTLSServer(t, handler, expiredCertificate)
	defer stopExpired()
	if _, err := validClient.Get("https://" + expiredAddress + "/healthz"); err == nil {
		t.Fatal("expired certificate was accepted")
	}
}

func TestServerAddressIsFailClosedToLoopback8443(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("BLOCK_HMI_TLS_CERT", filepath.Join(directory, "server.crt"))
	t.Setenv("BLOCK_HMI_TLS_KEY", filepath.Join(directory, "server.key"))
	t.Setenv("BLOCK_HMI_ADDR", "127.0.0.1:8443")
	if _, err := loadServerConfig(); err != nil {
		t.Fatalf("valid server config: %v", err)
	}
	for _, address := range []string{"0.0.0.0:8443", "127.0.0.1:80", "127.0.0.1:8080", "127.0.0.1:8081", "localhost:8443"} {
		t.Setenv("BLOCK_HMI_ADDR", address)
		if _, err := loadServerConfig(); err == nil {
			t.Fatalf("unsafe address %q was accepted", address)
		}
	}
}

func makeCA(t *testing.T, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "BLK-SIM-001 Test CA"},
		NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func makeServerCertificate(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(notAfter.UnixNano()), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: notBefore, NotAfter: notAfter,
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage:    x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func startTLSServer(t *testing.T, handler http.Handler, certificate tls.Certificate) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tlsListener := tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	server := &http.Server{Handler: handler}
	done := make(chan struct{})
	go func() {
		_ = server.Serve(tlsListener)
		close(done)
	}()
	return listener.Addr().String(), func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
		<-done
	}
}

func tlsHTTPClient(roots *x509.CertPool, serverName string, minVersion, maxVersion uint16) *http.Client {
	return &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs: roots, ServerName: serverName, MinVersion: minVersion, MaxVersion: maxVersion,
		}},
	}
}
