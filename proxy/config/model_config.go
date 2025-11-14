package config

import (
	"errors"
	"runtime"
	"slices"
	"strings"
)

type ModelConfig struct {
	Cmd           string   `yaml:"cmd"`
	CmdStop       string   `yaml:"cmdStop"`
	Proxy         string   `yaml:"proxy"`
	Aliases       []string `yaml:"aliases"`
	Env           []string `yaml:"env"`
	CheckEndpoint string   `yaml:"checkEndpoint"`
	UnloadAfter   int      `yaml:"ttl"`
	Unlisted      bool     `yaml:"unlisted"`
	UseModelName  string   `yaml:"useModelName"`

	// HTTP-based sleep/wake configuration
	SleepEndpoint string `yaml:"sleepEndpoint"`
	SleepMethod   string `yaml:"sleepMethod"`
	SleepBody     string `yaml:"sleepBody"`
	SleepTimeout  int    `yaml:"sleepTimeout"`

	WakeEndpoint string `yaml:"wakeEndpoint"`
	WakeMethod   string `yaml:"wakeMethod"`
	WakeBody     string `yaml:"wakeBody"`
	WakeTimeout  int    `yaml:"wakeTimeout"`

	// #179 for /v1/models
	Name        string `yaml:"name"`
	Description string `yaml:"description"`

	// Limit concurrency of HTTP requests to process
	ConcurrencyLimit int `yaml:"concurrencyLimit"`

	// Model filters see issue #174
	Filters ModelFilters `yaml:"filters"`

	// Macros: see #264
	// Model level macros take precedence over the global macros
	Macros MacroList `yaml:"macros"`

	// Metadata: see #264
	// Arbitrary metadata that can be exposed through the API
	Metadata map[string]any `yaml:"metadata"`

	// override global setting
	SendLoadingState *bool `yaml:"sendLoadingState"`
}

func (m *ModelConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawModelConfig ModelConfig
	defaults := rawModelConfig{
		Cmd:              "",
		CmdStop:          "",
		Proxy:            "http://localhost:${PORT}",
		Aliases:          []string{},
		Env:              []string{},
		CheckEndpoint:    "/health",
		UnloadAfter:      0,
		Unlisted:         false,
		UseModelName:     "",
		ConcurrencyLimit: 0,
		Name:             "",
		Description:      "",
		SleepMethod:      "",
		WakeMethod:       "",
		SleepTimeout:     0,
		WakeTimeout:      0,
	}

	// the default cmdStop to taskkill /f /t /pid ${PID}
	if runtime.GOOS == "windows" {
		defaults.CmdStop = "taskkill /f /t /pid ${PID}"
	}

	if err := unmarshal(&defaults); err != nil {
		return err
	}

	*m = ModelConfig(defaults)

	// Validation: if one endpoint is set, both must be set
	if (m.SleepEndpoint != "" && m.WakeEndpoint == "") {
		return errors.New("wakeEndpoint required when sleepEndpoint is configured")
	}
	if (m.WakeEndpoint != "" && m.SleepEndpoint == "") {
		return errors.New("sleepEndpoint required when wakeEndpoint is configured")
	}

	// Set default methods if endpoints are configured but methods are empty
	if m.SleepEndpoint != "" && m.SleepMethod == "" {
		m.SleepMethod = "POST"
	}
	if m.WakeEndpoint != "" && m.WakeMethod == "" {
		m.WakeMethod = "POST"
	}

	// Validate HTTP methods
	validMethods := map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true}
	if m.SleepMethod != "" && !validMethods[strings.ToUpper(m.SleepMethod)] {
		return errors.New("invalid sleepMethod: " + m.SleepMethod + " (must be GET, POST, PUT, or PATCH)")
	}
	if m.WakeMethod != "" && !validMethods[strings.ToUpper(m.WakeMethod)] {
		return errors.New("invalid wakeMethod: " + m.WakeMethod + " (must be GET, POST, PUT, or PATCH)")
	}

	// Normalize methods to uppercase
	if m.SleepMethod != "" {
		m.SleepMethod = strings.ToUpper(m.SleepMethod)
	}
	if m.WakeMethod != "" {
		m.WakeMethod = strings.ToUpper(m.WakeMethod)
	}

	return nil
}

func (m *ModelConfig) SanitizedCommand() ([]string, error) {
	return SanitizeCommand(m.Cmd)
}

// ModelFilters see issue #174
type ModelFilters struct {
	StripParams string `yaml:"stripParams"`
}

func (m *ModelFilters) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawModelFilters ModelFilters
	defaults := rawModelFilters{
		StripParams: "",
	}

	if err := unmarshal(&defaults); err != nil {
		return err
	}

	// Try to unmarshal with the old field name for backwards compatibility
	if defaults.StripParams == "" {
		var legacy struct {
			StripParams string `yaml:"strip_params"`
		}
		if legacyErr := unmarshal(&legacy); legacyErr != nil {
			return errors.New("failed to unmarshal legacy filters.strip_params: " + legacyErr.Error())
		}
		defaults.StripParams = legacy.StripParams
	}

	*m = ModelFilters(defaults)
	return nil
}

func (f ModelFilters) SanitizedStripParams() ([]string, error) {
	if f.StripParams == "" {
		return nil, nil
	}

	params := strings.Split(f.StripParams, ",")
	cleaned := make([]string, 0, len(params))
	seen := make(map[string]bool)

	for _, param := range params {
		trimmed := strings.TrimSpace(param)
		if trimmed == "model" || trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		cleaned = append(cleaned, trimmed)
	}

	// sort cleaned
	slices.Sort(cleaned)
	return cleaned, nil
}
