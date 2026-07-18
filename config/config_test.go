package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDecodeChannelInstancesRejectsObject(t *testing.T) {
	if _, err := DecodeChannelInstances(json.RawMessage(`{"telegram":{"token":"secret"}}`)); err == nil {
		t.Fatal("expected channels object to be rejected")
	}
}

func TestUpdateChannelInstanceConfigPreservesArray(t *testing.T) {
	oldFile := ConfigFile
	ConfigFile = filepath.Join(t.TempDir(), "config.json")
	t.Cleanup(func() { ConfigFile = oldFile })
	initial := `{"agents":[],"channels":[
		{"id":"wx-personal","type":"wechat","config":{"token":"old"}},
		{"id":"wx-team","type":"wechat","config":{"token":"team"}}
	]}`
	if err := os.WriteFile(ConfigFile, []byte(initial), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := UpdateChannelInstanceConfig("wx-personal", json.RawMessage(`{"token":"new"}`)); err != nil {
		t.Fatalf("UpdateChannelInstanceConfig: %v", err)
	}
	data, err := os.ReadFile(ConfigFile)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var root struct {
		Channels []ChannelInstance `json:"channels"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	var personal, team map[string]string
	_ = json.Unmarshal(root.Channels[0].Config, &personal)
	_ = json.Unmarshal(root.Channels[1].Config, &team)
	if len(root.Channels) != 2 || personal["token"] != "new" || team["token"] != "team" {
		t.Fatalf("channels = %#v", root.Channels)
	}
}

func TestLoadGatewayConfigReadsChannelArrayAndSharedAgents(t *testing.T) {
	oldFile := ConfigFile
	ConfigFile = filepath.Join(t.TempDir(), "config.json")
	t.Cleanup(func() { ConfigFile = oldFile })
	data := `{
		"agents":[{"id":"miya","type":"builtin"}],
		"channels":[{"id":"tg-lab","type":"telegram","agent":"miya","config":{"token":"secret"}}]
	}`
	if err := os.WriteFile(ConfigFile, []byte(data), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, instances, err := LoadGatewayConfig()
	if err != nil {
		t.Fatalf("LoadGatewayConfig: %v", err)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].ID != "miya" || len(instances) != 1 || instances[0].ID != "tg-lab" {
		t.Fatalf("cfg agents = %#v, instances = %#v", cfg.Agents, instances)
	}
}

func TestDecodeChannelInstancesArrayAllowsSameType(t *testing.T) {
	instances, err := DecodeChannelInstances(json.RawMessage(`[
		{"id":"tg-personal","type":"telegram","agent":"miya","config":{"token":"one"}},
		{"id":"tg-lab","type":"telegram","agent":"research","delivery":{"visibility":"debug"},"config":{"token":"two"}}
	]`))
	if err != nil {
		t.Fatalf("DecodeChannelInstances: %v", err)
	}
	if len(instances) != 2 || instances[0].Type != instances[1].Type {
		t.Fatalf("instances = %#v", instances)
	}
	if instances[1].EffectiveVisibility() != VisibilityDebug {
		t.Fatalf("visibility = %q", instances[1].EffectiveVisibility())
	}
}

func TestDecodeChannelInstancesRejectsDuplicateID(t *testing.T) {
	_, err := DecodeChannelInstances(json.RawMessage(`[
		{"id":"tg","type":"telegram"},
		{"id":"tg","type":"telegram"}
	]`))
	if err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestDecodeChannelInstancesRejectsLegacyVisibilityField(t *testing.T) {
	_, err := DecodeChannelInstances(json.RawMessage(`[
		{"id":"tg","type":"telegram","visibility":"debug"}
	]`))
	if err == nil {
		t.Fatal("expected top-level visibility to be rejected")
	}
}
