package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	agentsconfig "github.com/lsongdev/miya-agents/config"
)

type Config = agentsconfig.Config
type AgentConfig = agentsconfig.ACPAgentConfig

func AgentEndpoints(cfg *Config) ([]AgentConfig, error) {
	return agentsconfig.AgentEndpoints(cfg)
}

type Visibility string

const (
	VisibilitySimple  Visibility = "simple"
	VisibilityNormal  Visibility = "normal"
	VisibilityVerbose Visibility = "verbose"
	VisibilityDebug   Visibility = "debug"
)

type DeliveryConfig struct {
	Visibility      Visibility `json:"visibility,omitempty"`
	Streaming       *bool      `json:"streaming,omitempty"`
	EditIntervalMS  int        `json:"editIntervalMs,omitempty"`
	MaxMessageChars int        `json:"maxMessageChars,omitempty"`
	FinalOnly       bool       `json:"finalOnly,omitempty"`
}

type CommandConfig struct {
	AgentSwitch   bool     `json:"agentSwitch,omitempty"`
	AllowedAgents []string `json:"allowedAgents,omitempty"`
}

type ChannelInstance struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Enabled  *bool           `json:"enabled,omitempty"`
	Agent    string          `json:"agent,omitempty"`
	Config   json.RawMessage `json:"config,omitempty"`
	Delivery DeliveryConfig  `json:"delivery,omitempty"`
	Commands CommandConfig   `json:"commands,omitempty"`
}

func (c ChannelInstance) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

func (c ChannelInstance) EffectiveVisibility() Visibility {
	if c.Delivery.Visibility != "" {
		return c.Delivery.Visibility
	}
	return VisibilityNormal
}

var ConfigPath = agentsconfig.ConfigPath
var ConfigFile = agentsconfig.ConfigFile
var ChannelsLockFile = filepath.Join(ConfigPath, "miya-channels.lock")
var configWriteMu sync.Mutex

// LoadGatewayConfig decodes the channel-owned array separately from the
// shared agent configuration.
func LoadGatewayConfig() (*Config, []ChannelInstance, error) {
	data, err := os.ReadFile(ConfigFile)
	if err != nil {
		return nil, nil, err
	}
	data, err = resolveCredentialReferences(data)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve config credentials: %w", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, nil, err
	}
	instances, err := DecodeChannelInstances(root["channels"])
	if err != nil {
		return nil, nil, fmt.Errorf("decode channels: %w", err)
	}
	// Channel schema ownership stays in this package. Shared agent fields are
	// decoded independently after the channel array has been extracted.
	delete(root, "channels")
	sharedData, err := json.Marshal(root)
	if err != nil {
		return nil, nil, err
	}
	var cfg Config
	if err := json.Unmarshal(sharedData, &cfg); err != nil {
		return nil, nil, err
	}
	agentsconfig.Normalize(&cfg)
	return &cfg, instances, nil
}

func ChannelInstances(cfg *Config) ([]ChannelInstance, error) {
	if cfg == nil || cfg.Channels == nil {
		return nil, nil
	}
	raw, err := json.Marshal(cfg.Channels)
	if err != nil {
		return nil, err
	}
	return DecodeChannelInstances(raw)
}

func DecodeChannelInstances(raw json.RawMessage) ([]ChannelInstance, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var instances []ChannelInstance
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&instances); err != nil {
		return nil, fmt.Errorf("channels must be an array: %w", err)
	}
	return normalizeChannelInstances(instances)
}

func normalizeChannelInstances(instances []ChannelInstance) ([]ChannelInstance, error) {
	seen := make(map[string]struct{}, len(instances))
	result := make([]ChannelInstance, 0, len(instances))
	for i := range instances {
		instance := instances[i]
		if !instance.IsEnabled() {
			continue
		}
		if instance.ID == "" {
			return nil, fmt.Errorf("channel instance id is required")
		}
		if instance.Type == "" {
			return nil, fmt.Errorf("channel instance %q type is required", instance.ID)
		}
		if _, ok := seen[instance.ID]; ok {
			return nil, fmt.Errorf("duplicate channel instance id %q", instance.ID)
		}
		seen[instance.ID] = struct{}{}
		if len(instance.Config) == 0 {
			instance.Config = json.RawMessage(`{}`)
		}
		result = append(result, instance)
	}
	return result, nil
}

func UpdateChannelInstanceConfig(id string, rawConfig json.RawMessage) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	data, err := os.ReadFile(ConfigFile)
	if err != nil {
		return err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	channelsRaw := root["channels"]
	var instances []ChannelInstance
	if err := json.Unmarshal(channelsRaw, &instances); err != nil {
		return fmt.Errorf("channels must be an array: %w", err)
	}
	found := false
	for i := range instances {
		if instances[i].ID == id {
			instances[i].Config = rawConfig
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("channel instance %q not found", id)
	}
	root["channels"], err = json.Marshal(instances)
	if err != nil {
		return err
	}
	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	updated = append(updated, '\n')
	if err := os.MkdirAll(filepath.Dir(ConfigFile), 0755); err != nil {
		return err
	}
	return os.WriteFile(ConfigFile, updated, 0600)
}
