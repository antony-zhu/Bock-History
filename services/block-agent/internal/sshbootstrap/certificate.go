package sshbootstrap

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const CertificateTTL = 300 * time.Second

type CertificateIssuer struct {
	ca SSHSigner
}

func NewCertificateIssuer(ca SSHSigner) (*CertificateIssuer, error) {
	if ca.path == "" {
		return nil, errors.New("SSH user CA signer is required")
	}
	return &CertificateIssuer{ca: ca}, nil
}

type IssuedCertificate struct {
	AuthorizedKey string
	ValidAfter    time.Time
	ValidBefore   time.Time
}

func (i *CertificateIssuer) Issue(
	publicKey ED25519AuthorizedKey,
	principal string,
	requestID string,
	validAfter time.Time,
) (IssuedCertificate, error) {
	if publicKey.Line == "" || len(publicKey.Blob) == 0 {
		return IssuedCertificate{}, fmt.Errorf("SSH public key must be ED25519")
	}
	if principal != "release" && principal != "debug" {
		return IssuedCertificate{}, fmt.Errorf("unsupported SSH principal")
	}
	if requestID == "" {
		return IssuedCertificate{}, fmt.Errorf("request ID is required")
	}

	validAfter = validAfter.UTC().Truncate(time.Second)
	validBefore := validAfter.Add(CertificateTTL)
	tempDirectory, err := os.MkdirTemp("", "ssh-bootstrap-cert-*")
	if err != nil {
		return IssuedCertificate{}, err
	}
	defer os.RemoveAll(tempDirectory)

	publicKeyPath := filepath.Join(tempDirectory, "client.pub")
	if err := os.WriteFile(publicKeyPath, []byte(publicKey.Line+"\n"), 0o600); err != nil {
		return IssuedCertificate{}, err
	}
	validity := validAfter.Format("20060102150405") + ":" + validBefore.Format("20060102150405")
	command := exec.Command(
		"ssh-keygen",
		"-q",
		"-s", i.ca.path,
		"-I", requestID,
		"-n", principal,
		"-V", validity,
		publicKeyPath,
	)
	command.Env = append(os.Environ(), "TZ=UTC0")
	if err := command.Run(); err != nil {
		return IssuedCertificate{}, errors.New("ssh-keygen could not issue the certificate")
	}
	certificate, err := os.ReadFile(strings.TrimSuffix(publicKeyPath, ".pub") + "-cert.pub")
	if err != nil {
		return IssuedCertificate{}, err
	}
	authorizedKey := strings.TrimSpace(string(certificate))
	if !strings.HasPrefix(authorizedKey, "ssh-ed25519-cert-v01@openssh.com ") {
		return IssuedCertificate{}, errors.New("ssh-keygen returned an unexpected certificate type")
	}

	return IssuedCertificate{
		AuthorizedKey: authorizedKey,
		ValidAfter:    validAfter,
		ValidBefore:   validBefore,
	}, nil
}
