package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type runtimeTLSMaterial struct {
	certificate     tls.Certificate
	certificatePath string
	privateKeyPath  string
	ca              *x509.Certificate
}

func TestRuntimeTLSUsesTrustedCertificateAndRejectsInvalidClients(t *testing.T) {
	material := newRuntimeTLSMaterial(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	address, cancel, done := startRuntimeTLSFromFiles(t, material)
	defer stopRuntime(t, cancel, done)

	url := "https://" + address + "/healthz"
	response, err := strictRuntimeTLSClient(material.ca, "127.0.0.1", tls.VersionTLS13).Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("trusted TLS health status = %d", response.StatusCode)
	}

	for name, client := range map[string]*http.Client{
		"wrong-ca":       strictRuntimeTLSClient(nil, "127.0.0.1", tls.VersionTLS13),
		"wrong-hostname": strictRuntimeTLSClient(material.ca, "wrong.invalid", tls.VersionTLS13),
		"tls-1.1": {
			Timeout: 3 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{
				RootCAs:    runtimeTLSRoots(material.ca),
				ServerName: "127.0.0.1",
				MinVersion: tls.VersionTLS11,
				MaxVersion: tls.VersionTLS11,
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			response, err := client.Get(url)
			if err == nil {
				_ = response.Body.Close()
				t.Fatal("invalid TLS client unexpectedly reached the runtime")
			}
		})
	}
}

func TestRuntimeTLSRejectsExpiredCertificateAndPlainHTTP(t *testing.T) {
	material := newRuntimeTLSMaterial(t, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	runtime, err := NewLocalRuntime("127.0.0.1:0", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	address, cancel, done := startRuntimeTLS(t, runtime, material)
	defer stopRuntime(t, cancel, done)

	if response, err := strictRuntimeTLSClient(material.ca, "127.0.0.1", tls.VersionTLS13).Get("https://" + address + "/healthz"); err == nil {
		_ = response.Body.Close()
		t.Fatal("expired runtime certificate unexpectedly succeeded")
	}

	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(connection, "GET /healthz HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(connection)
	if err != nil {
		if networkError, ok := err.(net.Error); !ok || !networkError.Timeout() {
			t.Fatal(err)
		}
	}
	if bytes.Contains(response, []byte("HTTP/")) || bytes.Contains(response, []byte("Location:")) {
		t.Fatalf("plaintext HTTP received a business response or redirect: %q", response)
	}
}

func TestRuntimeTLSRequiresCertificateAndKeepsOfflineDefaults(t *testing.T) {
	runtime, err := NewLocalRuntime("127.0.0.1:0", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.mqtt.Enabled || runtime.wifiBackend != nil {
		t.Fatal("default local TLS runtime unexpectedly depends on BDM or Wi-Fi")
	}
	if err := runtime.RunTLS(context.Background(), "", ""); err == nil {
		t.Fatal("runtime accepted missing local TLS certificate material")
	}
}

func startRuntimeTLS(t *testing.T, runtime *Runtime, material runtimeTLSMaterial) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.serveTLSListener(ctx, listener, material.certificate) }()
	address := listener.Addr().String()
	return address, cancel, done
}

func startRuntimeTLSFromFiles(t *testing.T, material runtimeTLSMaterial) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewLocalRuntime(address, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.RunTLS(ctx, material.certificatePath, material.privateKeyPath) }()
	waitFor(t, time.Second, func() bool {
		response, err := strictRuntimeTLSClient(material.ca, "127.0.0.1", tls.VersionTLS13).Get("https://" + address + "/healthz")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusOK
	})
	return address, cancel, done
}

func strictRuntimeTLSClient(certificate *x509.Certificate, serverName string, maxVersion uint16) *http.Client {
	return &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:    runtimeTLSRoots(certificate),
			ServerName: serverName,
			MinVersion: tls.VersionTLS12,
			MaxVersion: maxVersion,
		}},
	}
}

func runtimeTLSRoots(certificate *x509.Certificate) *x509.CertPool {
	roots := x509.NewCertPool()
	if certificate != nil {
		roots.AddCert(certificate)
	}
	return roots
}

func newRuntimeTLSMaterial(t *testing.T, notBefore, notAfter time.Time) runtimeTLSMaterial {
	t.Helper()
	caPrivateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caNow := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Block local test CA"},
		NotBefore:             caNow.Add(-time.Hour),
		NotAfter:              caNow.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caPrivateKey.PublicKey, caPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	certificateTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, certificateTemplate, ca, &privateKey.PublicKey, caPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER})
	certificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certificatePath := filepath.Join(directory, "local.crt")
	privateKeyPath := filepath.Join(directory, "local.key")
	if err := os.WriteFile(certificatePath, certificatePEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateKeyPath, privateKeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return runtimeTLSMaterial{
		certificate: certificate, certificatePath: certificatePath, privateKeyPath: privateKeyPath, ca: ca,
	}
}
