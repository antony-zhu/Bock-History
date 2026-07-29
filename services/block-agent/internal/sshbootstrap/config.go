package sshbootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
)

var (
	identifierPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	kidPattern            = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	hostPattern           = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,253}$`)
	fingerprintPattern    = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
	absolutePathFieldList = []struct {
		name  string
		value func(Config) string
	}{
		{name: "administratorPublicKeyPath", value: func(c Config) string { return c.AdministratorPublicKeyPath }},
		{name: "tlsCertificatePath", value: func(c Config) string { return c.TLSCertificatePath }},
		{name: "tlsPrivateKeyPath", value: func(c Config) string { return c.TLSPrivateKeyPath }},
		{name: "sshUserCaPrivateKeyPath", value: func(c Config) string { return c.SSHUserCAPrivateKeyPath }},
		{name: "sshUserCaPublicKeyPath", value: func(c Config) string { return c.SSHUserCAPublicKeyPath }},
		{name: "nonceDatabasePath", value: func(c Config) string { return c.NonceDatabasePath }},
	}
)

type Config struct {
	ListenAddress              string `json:"listenAddress"`
	TargetNode                 string `json:"targetNode"`
	SiteID                     string `json:"siteId"`
	BlockID                    string `json:"blockId"`
	DeviceID                   string `json:"deviceId"`
	AdvertisedHost             string `json:"advertisedHost"`
	SSHPort                    int    `json:"sshPort"`
	AdministratorKID           string `json:"administratorKid"`
	AdministratorPublicKeyPath string `json:"administratorPublicKeyPath"`
	TLSCertificatePath         string `json:"tlsCertificatePath"`
	TLSPrivateKeyPath          string `json:"tlsPrivateKeyPath"`
	SSHUserCAPrivateKeyPath    string `json:"sshUserCaPrivateKeyPath"`
	SSHUserCAPublicKeyPath     string `json:"sshUserCaPublicKeyPath"`
	SSHHostKeyFingerprint      string `json:"sshHostKeyFingerprint"`
	NonceDatabasePath          string `json:"nonceDatabasePath"`
	ReleaseUsername            string `json:"releaseUsername"`
	DebugUsername              string `json:"debugUsername"`
}

func LoadConfig(path string) (Config, error) {
	if !filepath.IsAbs(path) {
		return Config{}, errors.New("config path must be absolute")
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Config{}, err
	}
	if config.ListenAddress == "" {
		config.ListenAddress = ":9443"
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var value any
	err := decoder.Decode(&value)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("config contains multiple JSON values")
	}
	return err
}

func (c Config) Validate() error {
	_, port, err := net.SplitHostPort(c.ListenAddress)
	if err != nil || port == "" {
		return errors.New("listenAddress must include a TCP port")
	}
	if c.TargetNode != "BLOCK" {
		return errors.New("targetNode must be BLOCK in the Block deployment")
	}
	for name, value := range map[string]string{
		"siteId":   c.SiteID,
		"blockId":  c.BlockID,
		"deviceId": c.DeviceID,
	} {
		if !identifierPattern.MatchString(value) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	if !hostPattern.MatchString(c.AdvertisedHost) {
		return errors.New("advertisedHost is invalid")
	}
	if c.SSHPort < 1 || c.SSHPort > 65535 {
		return errors.New("sshPort is invalid")
	}
	if !kidPattern.MatchString(c.AdministratorKID) {
		return errors.New("administratorKid is invalid")
	}
	for _, field := range absolutePathFieldList {
		if !filepath.IsAbs(field.value(c)) {
			return fmt.Errorf("%s must be an absolute path", field.name)
		}
	}
	if !fingerprintPattern.MatchString(c.SSHHostKeyFingerprint) {
		return errors.New("sshHostKeyFingerprint is invalid")
	}
	if c.ReleaseUsername != "release" || c.DebugUsername != "debug" {
		return errors.New("releaseUsername and debugUsername must match their non-root v1 principals")
	}
	return nil
}

func (c Config) Username(accessProfile string) (string, bool) {
	switch accessProfile {
	case "release":
		return c.ReleaseUsername, true
	case "debug":
		return c.DebugUsername, true
	default:
		return "", false
	}
}
