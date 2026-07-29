package sshbootstrap

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"strings"
	"unicode/utf8"
)

func LoadAdministratorPublicKey(path string) (ed25519.PublicKey, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	block, rest := pem.Decode(contents)
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("administrator public key must contain one PEM block")
	}
	publicKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ed25519Key, ok := publicKey.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("administrator public key must be ED25519")
	}
	return ed25519Key, nil
}

type ED25519AuthorizedKey struct {
	Line string
	Blob []byte
}

func ParseED25519AuthorizedKey(value string) (ED25519AuthorizedKey, error) {
	if strings.ContainsAny(value, "\r\n") {
		return ED25519AuthorizedKey{}, errors.New("SSH public key must be one line")
	}
	if utf8.RuneCountInString(value) < 81 || utf8.RuneCountInString(value) > 1024 {
		return ED25519AuthorizedKey{}, errors.New("SSH public key length is invalid")
	}
	parts := strings.SplitN(value, " ", 3)
	if len(parts) < 2 || len(parts) > 3 || parts[0] != "ssh-ed25519" || parts[1] == "" {
		return ED25519AuthorizedKey{}, errors.New("SSH public key must be ED25519")
	}
	if len(parts) == 3 && (parts[2] == "" || strings.HasPrefix(parts[2], " ") || utf8.RuneCountInString(parts[2]) > 128) {
		return ED25519AuthorizedKey{}, errors.New("SSH public key comment is invalid")
	}
	blob, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return ED25519AuthorizedKey{}, errors.New("SSH public key encoding is invalid")
	}
	keyType, rest, ok := readSSHString(blob)
	if !ok || string(keyType) != "ssh-ed25519" {
		return ED25519AuthorizedKey{}, errors.New("SSH public key blob type is invalid")
	}
	keyBytes, rest, ok := readSSHString(rest)
	if !ok || len(keyBytes) != ed25519.PublicKeySize || len(rest) != 0 {
		return ED25519AuthorizedKey{}, errors.New("SSH public key blob is invalid")
	}
	return ED25519AuthorizedKey{Line: value, Blob: blob}, nil
}

func readSSHString(value []byte) (field []byte, rest []byte, ok bool) {
	if len(value) < 4 {
		return nil, nil, false
	}
	length := uint64(binary.BigEndian.Uint32(value[:4]))
	if length > uint64(len(value)-4) {
		return nil, nil, false
	}
	end := 4 + int(length)
	return value[4:end], value[end:], true
}

type SSHSigner struct {
	path       string
	publicLine string
}

func LoadSSHSigner(path string) (SSHSigner, error) {
	if path == "" {
		return SSHSigner{}, errors.New("SSH user CA private key path is required")
	}
	command := exec.Command("ssh-keygen", "-y", "-f", path)
	output, err := command.Output()
	if err != nil {
		return SSHSigner{}, errors.New("SSH user CA private key could not be loaded")
	}
	publicLine := strings.TrimSpace(string(output))
	if !strings.HasPrefix(publicLine, "ssh-ed25519 ") {
		return SSHSigner{}, errors.New("SSH user CA must be ED25519")
	}
	return SSHSigner{path: path, publicLine: publicLine}, nil
}

func ValidateSSHCAKeyPair(signer SSHSigner, publicKeyPath string) error {
	contents, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return err
	}
	configuredFields := strings.Fields(strings.TrimSpace(string(contents)))
	derivedFields := strings.Fields(signer.publicLine)
	if len(configuredFields) < 2 || len(derivedFields) < 2 ||
		configuredFields[0] != "ssh-ed25519" ||
		configuredFields[0] != derivedFields[0] ||
		configuredFields[1] != derivedFields[1] {
		return errors.New("SSH user CA public key does not match its private key")
	}
	return nil
}
