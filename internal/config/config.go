// Package config defines the runtime configuration for csar-botverify.
package config

import (
	"fmt"
	"reflect"

	"github.com/ledatu/csar-core/configutil"
	"github.com/ledatu/csar-core/redisutil"
	"github.com/ledatu/csar-core/stsclient"
	"gopkg.in/yaml.v3"
)

// Config is the top-level YAML schema for csar-botverify.
type Config struct {
	Service     ServiceSection              `yaml:"service"`
	TLS         configutil.TLSSection       `yaml:"tls"`
	Tracing     TracingSection              `yaml:"tracing"`
	ServiceAuth stsclient.ServiceAuthConfig `yaml:"service_auth"`
	HealthPort  int                         `yaml:"health_port"`
	Custom      Custom                      `yaml:"custom"`
}

// ServiceSection identifies the service.
type ServiceSection struct {
	Name string `yaml:"name"`
	Port int    `yaml:"port"`
}

// TracingSection configures OpenTelemetry tracing.
type TracingSection struct {
	Endpoint   string  `yaml:"endpoint"`
	SampleRate float64 `yaml:"sample_rate"`
}

// Custom holds service-specific configuration.
type Custom struct {
	PollingActive bool             `yaml:"-"`
	Redis         redisutil.Config `yaml:"redis"`

	Providers []ProviderConfig `yaml:"providers"`
}

// ProviderConfig describes a single bot provider (Telegram, VK, etc.)
type ProviderConfig struct {
	Mode    string `yaml:"mode"`
	Network string `yaml:"network"`

	Name          string `yaml:"name"`
	BotToken      string `yaml:"bot_token"`
	WebhookSecret string `yaml:"webhook_secret"`
	WebhookURL    string `yaml:"webhook_url"`
}

// LoadFromBytes parses raw YAML bytes into a Config, expanding environment
// variables and validating required fields.
func LoadFromBytes(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	configutil.ExpandEnvInStruct(reflect.ValueOf(&cfg))

	if cfg.Service.Name == "" {
		return nil, fmt.Errorf("config: service.name is required")
	}
	if cfg.Service.Port == 0 {
		return nil, fmt.Errorf("config: service.port is required")
	}
	if cfg.HealthPort < 0 {
		return nil, fmt.Errorf("config: health_port must be positive")
	}
	if cfg.HealthPort > 0 && cfg.HealthPort == cfg.Service.Port {
		return nil, fmt.Errorf("config: health_port must differ from service.port")
	}
	if err := cfg.TLS.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := cfg.ServiceAuth.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	seen := make(map[string]bool)
	for i := range cfg.Custom.Providers {
		p := &cfg.Custom.Providers[i]
		if seen[p.Name] {
			return nil, fmt.Errorf("config: duplicate bot provider")
		}
		seen[p.Name] = true
		if p.Mode == "" {
			p.Mode = "webhook"
		}
		if p.Network == "" {
			p.Network = "tcp"
		}
		if p.Mode != "webhook" && p.Mode != "polling" && p.Mode != "polling_standby" {
			return nil, fmt.Errorf("config: invalid bot mode")
		}
		if p.Network != "tcp" && p.Network != "tcp4" && p.Network != "tcp6" {
			return nil, fmt.Errorf("config: invalid bot network")
		}
		if p.Name == "telegram" && p.BotToken == "" {
			return nil, fmt.Errorf("config: Telegram token is required")
		}
		if p.Mode != "webhook" && p.Name != "telegram" {
			return nil, fmt.Errorf("config: polling is supported only for Telegram")
		}
		if p.Mode == "polling" {
			cfg.Custom.PollingActive = true
		}
	}
	if cfg.Custom.PollingActive && (!seen["telegram"] || cfg.Custom.Redis.Addr == "") {
		return nil, fmt.Errorf("config: active polling requires Telegram and Redis")
	}
	return &cfg, nil
}
