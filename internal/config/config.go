// Package config defines the runtime configuration for csar-botverify.
package config

// Custom holds service-specific configuration parsed from the aurumskynet-core
// config.Custom YAML node.
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
