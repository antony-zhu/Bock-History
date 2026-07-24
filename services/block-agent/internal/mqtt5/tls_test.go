package mqtt5

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testCA struct {
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	certFile    string
}

func TestTLSConfigEnforcesMutualIdentityValidityAndVersions(t *testing.T) {
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	ca := createTestCA(t, directory, now)
	principal := "blk-0123456789abcdef0123456789abcdef"
	clientCert, clientKey := issueCertificate(
		t, directory, ca, "client", principal, now.Add(-time.Hour), now.Add(time.Hour),
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil, nil,
	)
	config, address, err := TLSConfig(
		"mqtts://127.0.0.1:8883", ca.certFile, clientCert, clientKey, principal, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if address != "127.0.0.1:8883" || config.ServerName != "127.0.0.1" ||
		config.MinVersion != 0x0303 || config.MaxVersion != 0x0304 ||
		config.InsecureSkipVerify {
		t.Fatalf("TLS config/address = %#v / %q", config, address)
	}

	wrongCert, wrongKey := issueCertificate(
		t, directory, ca, "wrong-cn", "blk-ffffffffffffffffffffffffffffffff",
		now.Add(-time.Hour), now.Add(time.Hour),
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil, nil,
	)
	if _, _, err := TLSConfig(
		"mqtts://127.0.0.1:8883", ca.certFile, wrongCert, wrongKey, principal, now,
	); err == nil {
		t.Fatal("client certificate with wrong CN unexpectedly passed")
	}
	expiredCert, expiredKey := issueCertificate(
		t, directory, ca, "expired", principal, now.Add(-2*time.Hour), now.Add(-time.Hour),
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil, nil,
	)
	if _, _, err := TLSConfig(
		"mqtts://127.0.0.1:8883", ca.certFile, expiredCert, expiredKey, principal, now,
	); err == nil {
		t.Fatal("expired client certificate unexpectedly passed")
	}
	serverOnlyCert, serverOnlyKey := issueCertificate(
		t, directory, ca, "server-only-client", principal,
		now.Add(-time.Hour), now.Add(time.Hour),
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, nil, nil,
	)
	if _, _, err := TLSConfig(
		"mqtts://127.0.0.1:8883", ca.certFile,
		serverOnlyCert, serverOnlyKey, principal, now,
	); err == nil {
		t.Fatal("client certificate without clientAuth EKU unexpectedly passed")
	}
	if _, _, err := TLSConfig(
		"mqtt://127.0.0.1:1883", ca.certFile, clientCert, clientKey, principal, now,
	); err == nil {
		t.Fatal("plaintext MQTT endpoint unexpectedly passed")
	}
}

func TestTLSDialerSupportsSeparatedCAsAndRejectsInvalidServerTransport(t *testing.T) {
	now := time.Now().UTC()
	serverDirectory := t.TempDir()
	clientDirectory := t.TempDir()
	serverCA := createTestCA(t, serverDirectory, now)
	clientCA := createTestCA(t, clientDirectory, now)
	principal := "blk-0123456789abcdef0123456789abcdef"
	clientCertFile, clientKeyFile := issueCertificate(
		t, clientDirectory, clientCA, "client", principal, now.Add(-time.Hour), now.Add(time.Hour),
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil, nil,
	)
	serverCertFile, serverKeyFile := issueCertificate(
		t, serverDirectory, serverCA, "server", "bdm-broker", now.Add(-time.Hour), now.Add(time.Hour),
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, nil, []net.IP{net.ParseIP("127.0.0.1")},
	)
	clientConfig, _, err := TLSConfig(
		"mqtts://127.0.0.1:8883", serverCA.certFile, clientCertFile, clientKeyFile, principal, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	serverCertificate, err := tlsLoadPair(serverCertFile, serverKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	clientRoots := x509.NewCertPool()
	caContents, _ := os.ReadFile(clientCA.certFile)
	clientRoots.AppendCertsFromPEM(caContents)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		tlsConnection := tlsServer(connection, serverCertificate, clientRoots, 0x0303, 0x0304)
		err = tlsConnection.Handshake()
		_ = tlsConnection.Close()
		serverDone <- err
	}()
	connection, err := TLSDialer(listener.Addr().String(), clientConfig, time.Second)(context.Background())
	if err != nil {
		t.Fatalf("valid mutual TLS handshake: %v", err)
	}
	_ = connection.Close()
	if err := <-serverDone; err != nil {
		t.Fatalf("server mutual TLS handshake: %v", err)
	}
	_ = listener.Close()

	wrongHostConfig := clientConfig.Clone()
	wrongHostConfig.ServerName = "localhost"
	if err := dialOneTLSFailure(wrongHostConfig, serverCertificate, clientRoots, 0x0303, 0x0304); err == nil {
		t.Fatal("wrong server hostname unexpectedly passed")
	}
	if err := dialOneTLSFailure(clientConfig, serverCertificate, clientRoots, 0x0301, 0x0302); err == nil {
		t.Fatal("TLS 1.0/1.1-only server unexpectedly passed")
	}

	plainListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		connection, err := plainListener.Accept()
		if err == nil {
			_, _ = connection.Write([]byte{0x20, 0x03, 0x00, 0x00, 0x00})
			_ = connection.Close()
		}
	}()
	if connection, err := TLSDialer(
		plainListener.Addr().String(), clientConfig, time.Second,
	)(context.Background()); err == nil {
		_ = connection.Close()
		t.Fatal("plaintext broker unexpectedly passed TLS dial")
	}
	_ = plainListener.Close()
}

func createTestCA(t *testing.T, directory string, now time.Time) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "ca.crt")
	writePEM(t, path, "CERTIFICATE", der)
	return testCA{certificate: template, key: key, certFile: path}
}

func issueCertificate(
	t *testing.T,
	directory string,
	ca testCA,
	name, commonName string,
	notBefore, notAfter time.Time,
	extended []x509.ExtKeyUsage,
	dnsNames []string,
	ipAddresses []net.IP,
) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: commonName},
		NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: extended,
		DNSNames: dnsNames, IPAddresses: ipAddresses,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.certificate, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(directory, name+".crt")
	keyPath := filepath.Join(directory, name+".key")
	writePEM(t, certPath, "CERTIFICATE", der)
	writePEM(t, keyPath, "PRIVATE KEY", keyDER)
	return certPath, keyPath
}

func writePEM(t *testing.T, path, blockType string, contents []byte) {
	t.Helper()
	block := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: contents})
	if err := os.WriteFile(path, block, 0o600); err != nil {
		t.Fatal(err)
	}
}

func tlsLoadPair(certFile, keyFile string) (tls.Certificate, error) {
	return tls.LoadX509KeyPair(certFile, keyFile)
}

func tlsServer(
	connection net.Conn,
	certificate tls.Certificate,
	clientRoots *x509.CertPool,
	minimum, maximum uint16,
) *tls.Conn {
	return tls.Server(connection, &tls.Config{
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert, ClientCAs: clientRoots,
		MinVersion: minimum, MaxVersion: maximum,
	})
}

func dialOneTLSFailure(
	clientConfig *tls.Config,
	serverCertificate tls.Certificate,
	clientRoots *x509.CertPool,
	minimum, maximum uint16,
) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		server := tlsServer(connection, serverCertificate, clientRoots, minimum, maximum)
		serverDone <- server.Handshake()
		_ = server.Close()
	}()
	connection, clientErr := TLSDialer(listener.Addr().String(), clientConfig, time.Second)(context.Background())
	if connection != nil {
		_ = connection.Close()
	}
	serverErr := <-serverDone
	if clientErr == nil && serverErr == nil {
		return nil
	}
	return errors.Join(clientErr, serverErr)
}
