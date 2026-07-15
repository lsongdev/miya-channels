package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	agentsconfig "github.com/lsongdev/miya-agents/config"
)

type Config = agentsconfig.Config
type AgentConfig = agentsconfig.ACPAgentConfig

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

func DefaultAgent(c *Config) (*AgentConfig, error) {
	for i := range c.Agents {
		agent := &c.Agents[i]
		if agent.Type == "" || agent.Type == "stdio" {
			if agent.Command == "" {
				continue
			}
			return agent, nil
		}
	}
	return nil, fmt.Errorf("no stdio ACP agent configured")
}

func Save(c *Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigFile, data, 0644)
}
