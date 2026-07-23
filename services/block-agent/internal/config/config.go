package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
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
}

type AgentAdapter struct {
	Type     string `json:"type"`
	IOSocket string `json:"ioSocket"`
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
	if strings.TrimSpace(value.SiteID) == "" || strings.TrimSpace(value.BlockID) == "" || strings.TrimSpace(value.DeviceID) == "" {
		return Agent{}, errors.New("siteId, blockId and deviceId are required")
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
	if strings.TrimSpace(value) == "" || !filepath.IsAbs(value) {
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
