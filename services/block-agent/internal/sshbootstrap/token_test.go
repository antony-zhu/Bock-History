package sshbootstrap

import (
	"crypto"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type superTokenVector struct {
	KID                    string `json:"kid"`
	Timestamp              string `json:"timestamp"`
	Nonce                  string `json:"nonce"`
	Method                 string `json:"method"`
	Path                   string `json:"path"`
	BodyUTF8               string `json:"bodyUtf8"`
	SiteID                 string `json:"siteId"`
	BlockID                string `json:"blockId"`
	DeviceID               string `json:"deviceId"`
	CanonicalUTF8          string `json:"canonicalUtf8"`
	AdministratorPublicPEM string `json:"administratorPublicKeyPem"`
	SignatureBase64URL     string `json:"signatureBase64Url"`
	Authorization          string `json:"authorization"`
}

func loadVector(t *testing.T) superTokenVector {
	t.Helper()
	path := filepath.Join(
		"..", "..", "..", "..", "..",
		"Common", "contracts", "ssh-bootstrap", "v1", "vectors", "supertoken-v1.json",
	)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var vector superTokenVector
	if err := json.Unmarshal(contents, &vector); err != nil {
		t.Fatal(err)
	}
	return vector
}

func vectorPublicKey(t *testing.T, vector superTokenVector) ed25519.PublicKey {
	t.Helper()
	block, _ := pem.Decode([]byte(vector.AdministratorPublicPEM))
	if block == nil {
		t.Fatal("vector public key PEM did not decode")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, ok := key.(ed25519.PublicKey)
	if !ok {
		t.Fatal("vector public key is not ED25519")
	}
	return publicKey
}

func TestSuperTokenVector(t *testing.T) {
	vector := loadVector(t)
	token, err := ParseSuperToken(vector.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	canonical := CanonicalBytes(
		token,
		vector.Method,
		vector.Path,
		[]byte(vector.BodyUTF8),
		SignedIdentity{
			SiteID:   vector.SiteID,
			BlockID:  vector.BlockID,
			DeviceID: vector.DeviceID,
		},
	)
	if string(canonical) != vector.CanonicalUTF8 {
		t.Fatalf("canonical bytes mismatch\nwant: %q\ngot:  %q", vector.CanonicalUTF8, canonical)
	}
	if !VerifySuperToken(
		vectorPublicKey(t, vector),
		token,
		vector.Method,
		vector.Path,
		[]byte(vector.BodyUTF8),
		SignedIdentity{SiteID: vector.SiteID, BlockID: vector.BlockID, DeviceID: vector.DeviceID},
	) {
		t.Fatal("contract vector signature did not verify")
	}
}

func TestSuperTokenRejectsChangedSignedValues(t *testing.T) {
	vector := loadVector(t)
	token, err := ParseSuperToken(vector.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := vectorPublicKey(t, vector)
	identity := SignedIdentity{SiteID: vector.SiteID, BlockID: vector.BlockID, DeviceID: vector.DeviceID}

	tests := []struct {
		name     string
		method   string
		path     string
		body     []byte
		identity SignedIdentity
	}{
		{name: "method", method: "GET", path: vector.Path, body: []byte(vector.BodyUTF8), identity: identity},
		{name: "path", method: vector.Method, path: "/v1/ssh/other", body: []byte(vector.BodyUTF8), identity: identity},
		{name: "body", method: vector.Method, path: vector.Path, body: append([]byte(vector.BodyUTF8), ' '), identity: identity},
		{name: "site", method: vector.Method, path: vector.Path, body: []byte(vector.BodyUTF8), identity: SignedIdentity{SiteID: "site-other", BlockID: vector.BlockID, DeviceID: vector.DeviceID}},
		{name: "block", method: vector.Method, path: vector.Path, body: []byte(vector.BodyUTF8), identity: SignedIdentity{SiteID: vector.SiteID, BlockID: "block-other", DeviceID: vector.DeviceID}},
		{name: "device", method: vector.Method, path: vector.Path, body: []byte(vector.BodyUTF8), identity: SignedIdentity{SiteID: vector.SiteID, BlockID: vector.BlockID, DeviceID: "device-other"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if VerifySuperToken(publicKey, token, test.method, test.path, test.body, test.identity) {
				t.Fatal("signature verified after signed value changed")
			}
		})
	}
}

func TestParseSuperTokenStrictSyntax(t *testing.T) {
	vector := loadVector(t)
	tests := []string{
		"",
		strings.Replace(vector.Authorization, "SuperToken", "supertoken", 1),
		strings.Replace(vector.Authorization, "kid=", "unknown=x,kid=", 1),
		strings.Replace(vector.Authorization, "timestamp="+vector.Timestamp, "timestamp=0"+vector.Timestamp, 1),
		strings.Replace(vector.Authorization, "nonce="+vector.Nonce, "nonce=YWJj", 1),
		strings.Replace(vector.Authorization, "signature="+vector.SignatureBase64URL, "signature=YWJj", 1),
		vector.Authorization + ",kid=duplicate",
	}
	for _, value := range tests {
		if _, err := ParseSuperToken(value); err == nil {
			t.Fatalf("invalid token unexpectedly accepted: %q", value)
		}
	}
}

func TestLoadAdministratorPublicKey(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "admin.pub")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadAdministratorPublicKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Equal(crypto.PublicKey(publicKey)) {
		t.Fatal("loaded administrator public key differs")
	}
}

func TestNewRequestID(t *testing.T) {
	requestID, err := NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(requestID, "-")
	if len(parts) != 5 || len(requestID) != 36 {
		t.Fatalf("request ID is not UUID-shaped: %q", requestID)
	}
	if parts[2][0] != '4' {
		t.Fatalf("request ID is not UUIDv4: %q", requestID)
	}
	decoded, err := base64.RawURLEncoding.DecodeString("MDEyMzQ1Njc4OWFiY2RlZg")
	if err != nil || len(decoded) != 16 {
		t.Fatal("test nonce is invalid")
	}
}
