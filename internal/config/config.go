// Package config defines the runtime configuration for csar-botverify.
package config

import (
	"fmt"
	"reflect"

	"github.com/ledatu/csar-core/configutil"
	"gopkg.in/yaml.v3"
)

// Config is the top-level YAML schema for csar-botverify.
type Config struct {
	Service ServiceSection       `yaml:"service"`
	TLS     configutil.TLSSection `yaml:"tls"`
	Tracing TracingSection        `yaml:"tracing"`
	Custom  Custom                `yaml:"custom"`
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
	RouterBaseURL     string           `yaml:"router_base_url"`
	STSEndpoint       string           `yaml:"sts_endpoint"`
	Audience          string           `yaml:"audience"`
	ServiceName       string           `yaml:"service_name"`
	AssertionAudience string           `yaml:"assertion_audience"`
	JWT               JWTConfig        `yaml:"jwt"`
	RouterTLS         RouterTLSConfig  `yaml:"router_tls"`
	Providers         []ProviderConfig `yaml:"providers"`
}

// ProviderConfig describes a single bot provider (Telegram, VK, etc.)
type ProviderConfig struct {
	Name          string `yaml:"name"`
	BotToken      string `yaml:"bot_token"`
	WebhookSecret string `yaml:"webhook_secret"`
	WebhookURL    string `yaml:"webhook_url"`
}

// JWTConfig points to the key pair used for STS assertions.
type JWTConfig struct {
	PrivateKeyFile string `yaml:"private_key_file"`
	PublicKeyFile  string `yaml:"public_key_file"`
}

// RouterTLSConfig provides the CA for verifying the router's TLS cert.
type RouterTLSConfig struct {
	CAFile string `yaml:"ca_file"`
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
	if err := cfg.TLS.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	return &cfg, nil
}
