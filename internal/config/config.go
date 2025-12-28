package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type ServerConfig struct {
	Name             string `yaml:"name"`
	Type             string `yaml:"type"`
	MinecraftVersion string `yaml:"minecraft_version"`
	Port             int    `yaml:"port"`
	MaxPlayers       int    `yaml:"max_players"`
	AllocatedRAM     int    `yaml:"ram"`
	AutoStart        bool   `yaml:"auto_start"`
}

type Config struct {
	LicenseKey   string         `yaml:"license_key"`
	CloudURL     string         `yaml:"cloud_url"`
	SFTPRelayURL string         `yaml:"sftp_relay_url"`
	DataDir      string         `yaml:"data_dir"`
	ConfigDir    string         `yaml:"config_dir"`
	Servers      []ServerConfig `yaml:"servers"`

	// Runtime fields (not from config file)
	InstanceUUID string `yaml:"-"`
	APIToken     string `yaml:"-"`
}

func Load(configPath string) (*Config, error) {
	cfg := &Config{
		CloudURL:     "https://redstonecore.net",
		SFTPRelayURL: "wss://redstonecore.net/relay/sftp",
		DataDir:      "/data",
		ConfigDir:    "/config",
		Servers:      []ServerConfig{},
	}

	// Check environment variables first
	if licenseKey := os.Getenv("RSC_LICENSE_KEY"); licenseKey != "" {
		cfg.LicenseKey = licenseKey
	}
	if cloudURL := os.Getenv("RSC_CLOUD_URL"); cloudURL != "" {
		cfg.CloudURL = cloudURL
	}
	if dataDir := os.Getenv("RSC_DATA_DIR"); dataDir != "" {
		cfg.DataDir = dataDir
	}
	if configDir := os.Getenv("RSC_CONFIG_DIR"); configDir != "" {
		cfg.ConfigDir = configDir
	}
	if sftpRelayURL := os.Getenv("RSC_SFTP_RELAY_URL"); sftpRelayURL != "" {
		cfg.SFTPRelayURL = sftpRelayURL
	}

	// Try to load from config file
	if configPath == "" {
		configPath = filepath.Join(cfg.ConfigDir, "config.yaml")
	}

	if data, err := os.ReadFile(configPath); err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}

	// Validate required fields
	if cfg.LicenseKey == "" {
		return nil, fmt.Errorf("license_key is required (set RSC_LICENSE_KEY env or in config.yaml)")
	}

	// Ensure directories exist
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	return cfg, nil
}

func (c *Config) SaveCredentials() error {
	credPath := filepath.Join(c.ConfigDir, ".credentials")
	data := fmt.Sprintf("instance_uuid=%s\napi_token=%s\n", c.InstanceUUID, c.APIToken)
	return os.WriteFile(credPath, []byte(data), 0600)
}

func (c *Config) LoadCredentials() error {
	credPath := filepath.Join(c.ConfigDir, ".credentials")
	data, err := os.ReadFile(credPath)
	if err != nil {
		return err
	}

	lines := string(data)
	for _, line := range splitLines(lines) {
		if len(line) == 0 {
			continue
		}
		parts := splitFirst(line, '=')
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "instance_uuid":
			c.InstanceUUID = parts[1]
		case "api_token":
			c.APIToken = parts[1]
		}
	}

	return nil
}

func (c *Config) IsRegistered() bool {
	return c.InstanceUUID != "" && c.APIToken != ""
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func splitFirst(s string, sep byte) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}
