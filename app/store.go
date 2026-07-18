package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lsongdev/miya-agents/acp"
	"github.com/lsongdev/miya-channels/config"
)

type sessionState struct {
	SessionID acp.SessionID         `json:"sessionId"`
	Cwd       string                `json:"cwd,omitempty"`
	Loaded    bool                  `json:"-"`
	Modes     *acp.SessionModeState `json:"-"`
}

type routeBinding struct {
	ChannelID      string                   `json:"channelId"`
	ConversationID string                   `json:"conversationId"`
	SenderID       string                   `json:"senderId"`
	AgentID        string                   `json:"agentId"`
	Visibility     config.Visibility        `json:"visibility"`
	Sessions       map[string]*sessionState `json:"sessions,omitempty"`
	CreatedAt      time.Time                `json:"createdAt"`
	UpdatedAt      time.Time                `json:"updatedAt"`
}

type routeStore struct {
	path string
}

type routeStoreFile struct {
	Version  int                      `json:"version"`
	Bindings map[string]*routeBinding `json:"bindings"`
}

const routeStoreVersion = 2

func newRouteStore() *routeStore {
	return &routeStore{path: filepath.Join(config.ConfigPath, "channels", "sessions.json")}
}

func (s *routeStore) Load() (map[string]*routeBinding, error) {
	bindings := make(map[string]*routeBinding)
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return bindings, nil
		}
		return nil, err
	}
	var file routeStoreFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("decode route store: %w", err)
	}
	if file.Version != routeStoreVersion {
		return nil, fmt.Errorf("unsupported route store version %d", file.Version)
	}
	if file.Bindings == nil {
		file.Bindings = make(map[string]*routeBinding)
	}
	for _, binding := range file.Bindings {
		if binding == nil {
			continue
		}
		for _, session := range binding.Sessions {
			if session != nil {
				session.Loaded = false
			}
		}
	}
	return file.Bindings, nil
}

func (s *routeStore) Save(bindings map[string]*routeBinding) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(routeStoreFile{Version: routeStoreVersion, Bindings: bindings}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(s.path, data, 0600)
}
