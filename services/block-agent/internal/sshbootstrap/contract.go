package sshbootstrap

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var publicKeyPattern = regexp.MustCompile(`^ssh-ed25519 [A-Za-z0-9+/]+={0,2}(?: [^\r\n]{1,128})?$`)

var ErrUnsupportedPublicKey = errors.New("unsupported SSH public key")

type CertificateRequest struct {
	TargetNode    string `json:"targetNode"`
	SiteID        string `json:"siteId"`
	BlockID       string `json:"blockId"`
	DeviceID      string `json:"deviceId"`
	AccessProfile string `json:"accessProfile"`
	PublicKey     string `json:"publicKey"`
}

func DecodeCertificateRequest(body []byte) (CertificateRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request CertificateRequest
	if err := decoder.Decode(&request); err != nil {
		return CertificateRequest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return CertificateRequest{}, errors.New("request contains multiple JSON values")
		}
		return CertificateRequest{}, err
	}
	if err := request.Validate(); err != nil {
		return CertificateRequest{}, err
	}
	return request, nil
}

func (r CertificateRequest) Validate() error {
	if r.TargetNode != "BLOCK" && r.TargetNode != "BDM" {
		return errors.New("targetNode is invalid")
	}
	for _, value := range []string{r.SiteID, r.BlockID, r.DeviceID} {
		if !identifierPattern.MatchString(value) {
			return errors.New("request identity is invalid")
		}
	}
	if r.AccessProfile != "release" && r.AccessProfile != "debug" {
		return errors.New("accessProfile is invalid")
	}
	if r.PublicKey == "" {
		return errors.New("publicKey is required")
	}
	if utf8.RuneCountInString(r.PublicKey) < 81 ||
		utf8.RuneCountInString(r.PublicKey) > 1024 ||
		!publicKeyPattern.MatchString(r.PublicKey) {
		return ErrUnsupportedPublicKey
	}
	return nil
}

func (r CertificateRequest) Identity() SignedIdentity {
	return SignedIdentity{SiteID: r.SiteID, BlockID: r.BlockID, DeviceID: r.DeviceID}
}

type CertificateResponse struct {
	Certificate        string `json:"certificate"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Username           string `json:"username"`
	HostKeyFingerprint string `json:"hostKeyFingerprint"`
	ValidAfter         string `json:"validAfter"`
	ValidBefore        string `json:"validBefore"`
	RequestID          string `json:"requestId"`
}

func NewCertificateResponse(
	config Config,
	certificate IssuedCertificate,
	username string,
	requestID string,
) CertificateResponse {
	return CertificateResponse{
		Certificate:        strings.TrimSpace(certificate.AuthorizedKey),
		Host:               config.AdvertisedHost,
		Port:               config.SSHPort,
		Username:           username,
		HostKeyFingerprint: config.SSHHostKeyFingerprint,
		ValidAfter:         certificate.ValidAfter.UTC().Format(time.RFC3339),
		ValidBefore:        certificate.ValidBefore.UTC().Format(time.RFC3339),
		RequestID:          requestID,
	}
}

type ErrorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
}
