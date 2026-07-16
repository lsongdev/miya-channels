package config

import (
	"fmt"
	"path/filepath"

	agentsconfig "github.com/lsongdev/miya-agents/config"
)

type Config = agentsconfig.Config
type AgentConfig = agentsconfig.ACPAgentConfig

var ConfigPath = agentsconfig.ConfigPath
var ConfigFile = agentsconfig.ConfigFile
var ChannelsLockFile = filepath.Join(ConfigPath, "miya-channels.lock")

func LoadConfig() (cfg *Config, err error) {
	return agentsconfig.LoadConfigFromFile(ConfigFile)
}

func DefaultAgent(c *Config) (*AgentConfig, error) {
	for i := range c.Agents {
		agent := &c.Agents[i]
		if !agent.IsEnabled() {
			continue
		}
		if agent.Type == "" || agent.Type == "stdio" || agent.Type == "builtin" || agent.Type == "inprocess" {
			if agent.Command == "" && agent.Type != "builtin" && agent.Type != "inprocess" {
				continue
			}
			return agent, nil
		}
	}
	return nil, fmt.Errorf("no enabled ACP agent configured")
}

func Save(c *Config) error {
	return agentsconfig.SaveConfigToFile(ConfigFile, c)
}
