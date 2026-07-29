package sshbootstrap

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeIssuer struct{}

func (fakeIssuer) Issue(
	_ ED25519AuthorizedKey,
	principal string,
	requestID string,
	validAfter time.Time,
) (IssuedCertificate, error) {
	return IssuedCertificate{
		AuthorizedKey: "ssh-ed25519-cert-v01@openssh.com " + strings.Repeat("A", 96) + " " + requestID,
		ValidAfter:    validAfter,
		ValidBefore:   validAfter.Add(CertificateTTL),
	}, nil
}

type handlerFixture struct {
	handler    *Handler
	privateKey ed25519.PrivateKey
	now        time.Time
	request    CertificateRequest
}

func newHandlerFixture(t *testing.T) handlerFixture {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	config := validConfig(t)
	store, err := OpenNonceStore(config.NonceDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler, err := NewHandler(config, publicKey, store, fakeIssuer{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	handler.now = func() time.Time { return now }
	counter := 0
	handler.requestID = func() (string, error) {
		counter++
		return fmt.Sprintf("018f0000-0000-4000-8000-%012d", counter), nil
	}
	userKey := newUserKey(t)
	return handlerFixture{
		handler:    handler,
		privateKey: privateKey,
		now:        now,
		request: CertificateRequest{
			TargetNode:    "BLOCK",
			SiteID:        "site-lab",
			BlockID:       "block-001",
			DeviceID:      "device-001",
			AccessProfile: "debug",
			PublicKey:     userKey.Line,
		},
	}
}

func signedAuthorization(
	t *testing.T,
	privateKey ed25519.PrivateKey,
	body []byte,
	timestamp int64,
	nonceText string,
	method string,
	path string,
	identity SignedIdentity,
) string {
	t.Helper()
	nonce := base64.RawURLEncoding.EncodeToString([]byte(nonceText))
	token := SuperToken{
		KID:             "admin-lab-2026-01",
		Timestamp:       timestamp,
		TimestampString: fmt.Sprintf("%d", timestamp),
		Nonce:           nonce,
	}
	token.Signature = ed25519.Sign(privateKey, CanonicalBytes(token, method, path, body, identity))
	return fmt.Sprintf(
		"SuperToken v1 kid=%s,timestamp=%s,nonce=%s,signature=%s",
		token.KID,
		token.TimestampString,
		token.Nonce,
		base64.RawURLEncoding.EncodeToString(token.Signature),
	)
}

func performSignedRequest(
	t *testing.T,
	fixture handlerFixture,
	request CertificateRequest,
	timestamp int64,
	nonce string,
	signBody []byte,
	signMethod string,
	signPath string,
	sendBody []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	if sendBody == nil {
		var err error
		sendBody, err = json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
	}
	if signBody == nil {
		signBody = sendBody
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "https://bootstrap.test"+certificatePath, bytes.NewReader(sendBody))
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set(
		"Authorization",
		signedAuthorization(
			t,
			fixture.privateKey,
			signBody,
			timestamp,
			nonce,
			signMethod,
			signPath,
			request.Identity(),
		),
	)
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, httpRequest)
	return recorder
}

func decodeError(t *testing.T, recorder *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()
	var response ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestHandlerIssuesExactSecretFreeResponse(t *testing.T) {
	fixture := newHandlerFixture(t)
	recorder := performSignedRequest(
		t,
		fixture,
		fixture.request,
		fixture.now.Unix(),
		"0123456789abcdef-success",
		nil,
		http.MethodPost,
		certificatePath,
		nil,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
	var response CertificateResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Username != "debug" || response.Host != "192.168.1.104" || response.Port != 22 {
		t.Fatalf("unexpected route response: %#v", response)
	}
	validAfter, err := time.Parse(time.RFC3339, response.ValidAfter)
	if err != nil {
		t.Fatal(err)
	}
	validBefore, err := time.Parse(time.RFC3339, response.ValidBefore)
	if err != nil {
		t.Fatal(err)
	}
	if validBefore.Sub(validAfter) != CertificateTTL {
		t.Fatalf("response TTL = %s", validBefore.Sub(validAfter))
	}
	var keys map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 8 {
		t.Fatalf("success response field count = %d", len(keys))
	}
	lower := strings.ToLower(recorder.Body.String())
	for _, forbidden := range []string{"privatekey", "private_key", "password", "token", "secret"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("response contains secret-like text %q: %s", forbidden, recorder.Body)
		}
	}
}

func TestHandlerTimestampBoundaries(t *testing.T) {
	for _, offset := range []int64{-61, -60, 60, 61} {
		t.Run(fmt.Sprintf("%+d", offset), func(t *testing.T) {
			fixture := newHandlerFixture(t)
			recorder := performSignedRequest(
				t,
				fixture,
				fixture.request,
				fixture.now.Unix()+offset,
				fmt.Sprintf("0123456789abcdef-%+d", offset),
				nil,
				http.MethodPost,
				certificatePath,
				nil,
			)
			if offset == -60 || offset == 60 {
				if recorder.Code != http.StatusOK {
					t.Fatalf("boundary status=%d body=%s", recorder.Code, recorder.Body)
				}
				return
			}
			if recorder.Code != http.StatusUnauthorized || decodeError(t, recorder).Code != "TIMESTAMP_OUT_OF_WINDOW" {
				t.Fatalf("outside-window status=%d body=%s", recorder.Code, recorder.Body)
			}
		})
	}
}

func TestHandlerRejectsNonceReplay(t *testing.T) {
	fixture := newHandlerFixture(t)
	body, err := json.Marshal(fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	authorization := signedAuthorization(
		t,
		fixture.privateKey,
		body,
		fixture.now.Unix(),
		"0123456789abcdef-replay",
		http.MethodPost,
		certificatePath,
		fixture.request.Identity(),
	)
	for attempt := 1; attempt <= 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "https://bootstrap.test"+certificatePath, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", authorization)
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, request)
		if attempt == 1 && recorder.Code != http.StatusOK {
			t.Fatalf("first status=%d body=%s", recorder.Code, recorder.Body)
		}
		if attempt == 2 && (recorder.Code != http.StatusConflict || decodeError(t, recorder).Code != "NONCE_REPLAYED") {
			t.Fatalf("replay status=%d body=%s", recorder.Code, recorder.Body)
		}
	}
}

func TestHandlerRejectsTamperingAndCrossTarget(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(CertificateRequest) CertificateRequest
		signMethod string
		signPath   string
		tamperBody bool
		wantStatus int
		wantCode   string
	}{
		{name: "method", mutate: func(r CertificateRequest) CertificateRequest { return r }, signMethod: http.MethodGet, signPath: certificatePath, wantStatus: 401, wantCode: "AUTHORIZATION_INVALID"},
		{name: "path", mutate: func(r CertificateRequest) CertificateRequest { return r }, signMethod: http.MethodPost, signPath: "/v1/ssh/other", wantStatus: 401, wantCode: "AUTHORIZATION_INVALID"},
		{name: "body", mutate: func(r CertificateRequest) CertificateRequest { return r }, signMethod: http.MethodPost, signPath: certificatePath, tamperBody: true, wantStatus: 401, wantCode: "AUTHORIZATION_INVALID"},
		{name: "block", mutate: func(r CertificateRequest) CertificateRequest { r.BlockID = "block-002"; return r }, signMethod: http.MethodPost, signPath: certificatePath, wantStatus: 403, wantCode: "TARGET_MISMATCH"},
		{name: "device", mutate: func(r CertificateRequest) CertificateRequest { r.DeviceID = "device-002"; return r }, signMethod: http.MethodPost, signPath: certificatePath, wantStatus: 403, wantCode: "TARGET_MISMATCH"},
		{name: "node", mutate: func(r CertificateRequest) CertificateRequest { r.TargetNode = "BDM"; return r }, signMethod: http.MethodPost, signPath: certificatePath, wantStatus: 403, wantCode: "TARGET_MISMATCH"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHandlerFixture(t)
			request := test.mutate(fixture.request)
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			signBody := body
			sendBody := body
			if test.tamperBody {
				sendBody = append(append([]byte(nil), body...), ' ')
			}
			recorder := performSignedRequest(
				t,
				fixture,
				request,
				fixture.now.Unix(),
				"0123456789abcdef-"+test.name,
				signBody,
				test.signMethod,
				test.signPath,
				sendBody,
			)
			if recorder.Code != test.wantStatus || decodeError(t, recorder).Code != test.wantCode {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
			}
		})
	}
}

func TestHandlerRejectsUnsupportedKeyAndRootProfile(t *testing.T) {
	fixture := newHandlerFixture(t)
	for name, mutate := range map[string]func(CertificateRequest) CertificateRequest{
		"rsa": func(request CertificateRequest) CertificateRequest {
			request.PublicKey = "ssh-rsa " + strings.Repeat("A", 100)
			return request
		},
		"root": func(request CertificateRequest) CertificateRequest {
			request.AccessProfile = "root"
			return request
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := mutate(fixture.request)
			recorder := performSignedRequest(
				t,
				fixture,
				request,
				fixture.now.Unix(),
				"0123456789abcdef-"+name,
				nil,
				http.MethodPost,
				certificatePath,
				nil,
			)
			wantCode := "UNSUPPORTED_PUBLIC_KEY"
			if name == "root" {
				wantCode = "INVALID_REQUEST"
			}
			if recorder.Code != http.StatusBadRequest || decodeError(t, recorder).Code != wantCode {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
			}
		})
	}
}

func generateServerCertificate(
	t *testing.T,
	notBefore time.Time,
	notAfter time.Time,
) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "bootstrap.test"},
		DNSNames:     []string{"bootstrap.test"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	certificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, parsed
}

func startTLSOnlyServer(t *testing.T, certificate tls.Certificate) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})}
	tlsListener := &tlsHandshakeListener{
		Listener: listener,
		config: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{certificate},
		},
	}
	go func() { _ = server.Serve(tlsListener) }()
	return listener.Addr().String(), func() { _ = server.Close() }
}

func tlsClient(certificate *x509.Certificate, serverName string, maxVersion uint16) *http.Client {
	roots := x509.NewCertPool()
	if certificate != nil {
		roots.AddCert(certificate)
	}
	return &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:    roots,
			ServerName: serverName,
			MinVersion: tls.VersionTLS12,
			MaxVersion: maxVersion,
		}},
	}
}

func TestTLSOnlyCorrectCertificateAndNegativeCases(t *testing.T) {
	now := time.Now()
	certificate, parsed := generateServerCertificate(t, now.Add(-time.Hour), now.Add(time.Hour))
	address, closeServer := startTLSOnlyServer(t, certificate)
	defer closeServer()
	url := "https://" + address + "/"

	response, err := tlsClient(parsed, "bootstrap.test", tls.VersionTLS13).Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("correct TLS status = %d", response.StatusCode)
	}
	for name, client := range map[string]*http.Client{
		"wrong-ca":   tlsClient(nil, "bootstrap.test", tls.VersionTLS13),
		"wrong-host": tlsClient(parsed, "wrong.test", tls.VersionTLS13),
		"tls-1.1": {
			Timeout: 3 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{
				RootCAs:    x509.NewCertPool(),
				ServerName: "bootstrap.test",
				MaxVersion: tls.VersionTLS11,
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			response, err := client.Get(url)
			if err == nil {
				_ = response.Body.Close()
				t.Fatal("invalid TLS connection unexpectedly succeeded")
			}
		})
	}
}

func TestTLSOnlyRejectsExpiredCertificateAndPlainHTTP(t *testing.T) {
	now := time.Now()
	expired, parsed := generateServerCertificate(t, now.Add(-2*time.Hour), now.Add(-time.Hour))
	address, closeServer := startTLSOnlyServer(t, expired)
	defer closeServer()
	if response, err := tlsClient(parsed, "bootstrap.test", tls.VersionTLS13).Get("https://" + address + "/"); err == nil {
		_ = response.Body.Close()
		t.Fatal("expired TLS certificate unexpectedly succeeded")
	}

	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(connection, "GET /v1/ssh/cert HTTP/1.1\r\nHost: bootstrap.test\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(connection)
	if err != nil {
		if networkError, ok := err.(net.Error); !ok || !networkError.Timeout() {
			t.Fatal(err)
		}
	}
	if bytes.Contains(response, []byte("HTTP/")) || bytes.Contains(response, []byte("Location:")) {
		t.Fatalf("plain HTTP received a response or redirect: %q", response)
	}
}

func TestHandlerDoesNotDependOnBDMOrWiFi(t *testing.T) {
	fixture := newHandlerFixture(t)
	if _, ok := any(fixture.handler).(interface{ ConnectBDM(context.Context) error }); ok {
		t.Fatal("SSH bootstrap handler unexpectedly exposes a BDM dependency")
	}
	recorder := performSignedRequest(
		t,
		fixture,
		fixture.request,
		fixture.now.Unix(),
		"0123456789abcdef-offline",
		nil,
		http.MethodPost,
		certificatePath,
		nil,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("local-only issue status=%d body=%s", recorder.Code, recorder.Body)
	}
}
