package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxSafeDecimal = uint64(9007199254740991)

var (
	identifierPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]{0,62})$`)
	principalPattern  = regexp.MustCompile(`^blk-[0-9a-f]{32}$`)
)

type Agent struct {
	SiteID              string       `json:"siteId"`
	BlockID             string       `json:"blockId"`
	DeviceID            string       `json:"deviceId"`
	Adapter             AgentAdapter `json:"adapter"`
	LocalAPISocket      string       `json:"localApiSocket"`
	LocalAPISocketGroup string       `json:"localApiSocketGroup"`
	DatabasePath        string       `json:"databasePath"`
	SamplePeriod        string       `json:"samplePeriod"`
	StaleAfter          string       `json:"staleAfter"`
	CommandTimeout      string       `json:"commandTimeout"`
	BDM                 BDM          `json:"bdm"`
}

type AgentAdapter struct {
	Type     string `json:"type"`
	IOSocket string `json:"ioSocket"`
}

type BDM struct {
	Enabled          bool   `json:"enabled"`
	Endpoint         string `json:"endpoint,omitempty"`
	Principal        string `json:"principal,omitempty"`
	CAFile           string `json:"caFile,omitempty"`
	ClientCertFile   string `json:"clientCertFile,omitempty"`
	ClientKeyFile    string `json:"clientKeyFile,omitempty"`
	SoftwareVersion  string `json:"softwareVersion,omitempty"`
	OSVersion        string `json:"osVersion,omitempty"`
	Architecture     string `json:"architecture,omitempty"`
	HardwareModel    string `json:"hardwareModel,omitempty"`
	StreamGeneration string `json:"streamGeneration,omitempty"`
}

type Simulator struct {
	IOSocket               string  `json:"ioSocket"`
	IOSocketGroup          string  `json:"ioSocketGroup"`
	ControlSocket          string  `json:"controlSocket"`
	ControlSocketGroup     string  `json:"controlSocketGroup"`
	StateFile              string  `json:"stateFile"`
	SamplePeriod           string  `json:"samplePeriod"`
	Seed                   int64   `json:"seed"`
	PassRate               float64 `json:"passRate"`
	FaultInjectionEnabled  bool    `json:"faultInjectionEnabled"`
	BinCapacities          []int   `json:"binCapacities"`
	InitialTarget          int     `json:"initialTarget"`
	InitialCycleSeconds    int     `json:"initialCycleSeconds"`
	InitialToolLimit       int     `json:"initialToolLimit"`
	InitialInspectInterval int     `json:"initialInspectInterval"`
}

func LoadAgent(path string) (Agent, error) {
	var value Agent
	if err := loadStrict(path, &value); err != nil {
		return Agent{}, err
	}
	for name, identifier := range map[string]string{
		"siteId": value.SiteID, "blockId": value.BlockID, "deviceId": value.DeviceID,
	} {
		if !identifierPattern.MatchString(identifier) {
			return Agent{}, fmt.Errorf("%s must match %s", name, identifierPattern)
		}
	}
	if value.Adapter.Type != "simulator" && value.Adapter.Type != "disabled" {
		return Agent{}, fmt.Errorf("adapter.type must be simulator or disabled, got %q", value.Adapter.Type)
	}
	if value.Adapter.Type == "simulator" {
		if err := requireAbsolute("adapter.ioSocket", value.Adapter.IOSocket); err != nil {
			return Agent{}, err
		}
	}
	if err := requireAbsolute("localApiSocket", value.LocalAPISocket); err != nil {
		return Agent{}, err
	}
	if err := requireGroup("localApiSocketGroup", value.LocalAPISocketGroup); err != nil {
		return Agent{}, err
	}
	if err := requireAbsolute("databasePath", value.DatabasePath); err != nil {
		return Agent{}, err
	}
	if _, _, _, err := value.Durations(); err != nil {
		return Agent{}, err
	}
	if err := value.BDM.Validate(); err != nil {
		return Agent{}, err
	}
	return value, nil
}

func (c Agent) Durations() (samplePeriod, staleAfter, commandTimeout time.Duration, err error) {
	if samplePeriod, err = positiveDuration("samplePeriod", c.SamplePeriod); err != nil {
		return 0, 0, 0, err
	}
	if staleAfter, err = positiveDuration("staleAfter", c.StaleAfter); err != nil {
		return 0, 0, 0, err
	}
	if commandTimeout, err = positiveDuration("commandTimeout", c.CommandTimeout); err != nil {
		return 0, 0, 0, err
	}
	if staleAfter < samplePeriod {
		return 0, 0, 0, errors.New("staleAfter must be greater than or equal to samplePeriod")
	}
	return samplePeriod, staleAfter, commandTimeout, nil
}

func (c BDM) Validate() error {
	if !c.Enabled {
		return nil
	}
	endpoint, err := url.Parse(c.Endpoint)
	if err != nil {
		return fmt.Errorf("bdm.endpoint is invalid: %w", err)
	}
	if endpoint.Scheme != "mqtts" || endpoint.Hostname() == "" || endpoint.Port() != "8883" ||
		endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("bdm.endpoint must be mqtts://HOST:8883 without credentials, path, query or fragment")
	}
	if !principalPattern.MatchString(c.Principal) {
		return errors.New("bdm.principal must be blk- followed by 32 lowercase hexadecimal characters")
	}
	for name, path := range map[string]string{
		"bdm.caFile": c.CAFile, "bdm.clientCertFile": c.ClientCertFile, "bdm.clientKeyFile": c.ClientKeyFile,
	} {
		if err := requireAbsolute(name, path); err != nil {
			return err
		}
	}
	for name, value := range map[string]string{
		"bdm.softwareVersion": c.SoftwareVersion,
		"bdm.osVersion":       c.OSVersion,
		"bdm.architecture":    c.Architecture,
		"bdm.hardwareModel":   c.HardwareModel,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required when bdm.enabled=true", name)
		}
	}
	generation, err := strconv.ParseUint(c.StreamGeneration, 10, 64)
	if err != nil || generation == 0 || generation > maxSafeDecimal ||
		strconv.FormatUint(generation, 10) != c.StreamGeneration {
		return fmt.Errorf("bdm.streamGeneration must be a canonical decimal string between 1 and %d", maxSafeDecimal)
	}
	return nil
}

func LoadSimulator(path string) (Simulator, error) {
	var value Simulator
	if err := loadStrict(path, &value); err != nil {
		return Simulator{}, err
	}
	if err := requireAbsolute("ioSocket", value.IOSocket); err != nil {
		return Simulator{}, err
	}
	if err := requireAbsolute("controlSocket", value.ControlSocket); err != nil {
		return Simulator{}, err
	}
	if err := requireGroup("ioSocketGroup", value.IOSocketGroup); err != nil {
		return Simulator{}, err
	}
	if err := requireGroup("controlSocketGroup", value.ControlSocketGroup); err != nil {
		return Simulator{}, err
	}
	if value.IOSocketGroup == value.ControlSocketGroup {
		return Simulator{}, errors.New("ioSocketGroup and controlSocketGroup must differ")
	}
	if value.IOSocket == value.ControlSocket {
		return Simulator{}, errors.New("ioSocket and controlSocket must differ")
	}
	if err := requireAbsolute("stateFile", value.StateFile); err != nil {
		return Simulator{}, err
	}
	if _, err := value.SampleDuration(); err != nil {
		return Simulator{}, err
	}
	if value.PassRate < 0 || value.PassRate > 1 {
		return Simulator{}, errors.New("passRate must be between 0 and 1")
	}
	if len(value.BinCapacities) != 3 {
		return Simulator{}, errors.New("binCapacities must contain exactly three capacities")
	}
	for index, capacity := range value.BinCapacities {
		if capacity < 1 {
			return Simulator{}, fmt.Errorf("binCapacities[%d] must be positive", index)
		}
	}
	if value.InitialTarget < 1 || value.InitialCycleSeconds < 1 || value.InitialToolLimit < 1 || value.InitialInspectInterval < 1 {
		return Simulator{}, errors.New("initial target, cycle, tool limit and inspect interval must be positive")
	}
	return value, nil
}

func (c Simulator) SampleDuration() (time.Duration, error) {
	return positiveDuration("samplePeriod", c.SamplePeriod)
}

func loadStrict(path string, target any) error {
	if err := requireAbsolute("config path", path); err != nil {
		return err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode config %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode config %s: multiple JSON values", path)
	}
	return nil
}

func positiveDuration(name, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return duration, nil
}

func requireAbsolute(name, value string) error {
	value = strings.TrimSpace(value)
	// Configuration is authored for the Ubuntu target but is also validated by
	// Windows-hosted CI. Accept native absolute paths and POSIX-rooted paths.
	if value == "" || (!filepath.IsAbs(value) && !strings.HasPrefix(filepath.ToSlash(value), "/")) {
		return fmt.Errorf("%s must be an absolute path", name)
	}
	return nil
}

func requireGroup(name, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '-' && character != '_' {
			return fmt.Errorf("%s must contain only lowercase letters, digits, hyphen or underscore", name)
		}
	}
	return nil
}
