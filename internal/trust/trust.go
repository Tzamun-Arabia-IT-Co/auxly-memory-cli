package trust

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	LevelAuto            = "auto"
	LevelRequireApproval = "require_approval"
	LevelReadOnly        = "read_only"
)

// ProviderConfig holds trust configuration for a single provider.
type ProviderConfig struct {
	TrustLevel string `yaml:"trust_level"`
}

// Config holds the full trust.yaml configuration.
type Config struct {
	Default   string                    `yaml:"default"`
	Providers map[string]ProviderConfig `yaml:"providers"`
}

// Load reads and parses trust.yaml from the memory root.
func Load(memoryRoot string) (*Config, error) {
	path := filepath.Join(memoryRoot, "trust.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{Default: LevelRequireApproval, Providers: make(map[string]ProviderConfig)}, nil
		}
		return nil, fmt.Errorf("failed to read trust.yaml: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse trust.yaml: %w", err)
	}
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]ProviderConfig)
	}
	return &cfg, nil
}

// Save writes the trust config back to trust.yaml.
func (c *Config) Save(memoryRoot string) error {
	path := filepath.Join(memoryRoot, "trust.yaml")
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal trust config: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// GetTrustLevel returns the trust level for a given provider.
func (c *Config) GetTrustLevel(provider string) string {
	if p, ok := c.Providers[provider]; ok {
		return p.TrustLevel
	}
	return c.Default
}

// SetTrustLevel updates the trust level for a provider.
func (c *Config) SetTrustLevel(provider, level string) error {
	if level != LevelAuto && level != LevelRequireApproval && level != LevelReadOnly {
		return fmt.Errorf("invalid trust level: %s (must be auto, require_approval, or read_only)", level)
	}
	c.Providers[provider] = ProviderConfig{TrustLevel: level}
	return nil
}
