package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ACPConfig contains ACP server connection settings.
type ACPConfig struct {
	URL       string `json:"url" yaml:"url"`
	AgentName string `json:"agentName" yaml:"agentName"`
}

// Config is the root configuration structure.
type Config struct {
	ACP      *ACPConfig                 `json:"acp,omitempty" yaml:"acp,omitempty"`
	Channels map[string]json.RawMessage `json:"channels,omitempty" yaml:"channels,omitempty"`
}

var ConfigPath = filepath.Join(os.Getenv("HOME"), ".miya")
var ConfigFile = filepath.Join(ConfigPath, "config.json")

func LoadConfig() (cfg *Config, err error) {
	if _, err := os.Stat(ConfigFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("config file not found: %s", ConfigFile)
	}
	f, err := os.Open(ConfigFile)
	if err != nil {
		return
	}
	defer f.Close()
	if err = json.NewDecoder(f).Decode(&cfg); err != nil {
		return
	}
	return
}

func (c *Config) Save() error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// log.Println(string(data))
	return os.WriteFile(ConfigFile, data, 0644)
}
