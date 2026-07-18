package app

import (
	"fmt"
	"log"
	"sort"
	"sync"

	"github.com/lsongdev/miya-agents/acp"
	"github.com/lsongdev/miya-channels/config"
)

type agentRuntime struct {
	id          string
	client      *acp.Client
	loadSession bool
}

type agentRegistry struct {
	mu        sync.RWMutex
	agents    map[string]*agentRuntime
	order     []string
	eventSink func(string, AgentEvent)
}

func newAgentRegistry(cfg *config.Config, opts Options) (*agentRegistry, error) {
	registry := &agentRegistry{agents: make(map[string]*agentRuntime)}
	factory := opts.NewAgentClient
	if factory == nil {
		factory = DefaultEndpointAgentClient
	}
	for _, endpoint := range cfg.Agents {
		if !endpoint.IsEnabled() {
			continue
		}
		if endpoint.ID == "" {
			registry.Close()
			return nil, fmt.Errorf("enabled ACP agent id is required")
		}
		client, err := factory(cfg, endpoint)
		if err != nil {
			registry.Close()
			return nil, err
		}
		if err := registry.add(endpoint.ID, client); err != nil {
			client.Close()
			registry.Close()
			return nil, err
		}
	}
	if len(registry.agents) == 0 {
		return nil, fmt.Errorf("no enabled ACP agent configured")
	}
	return registry, nil
}

func (r *agentRegistry) add(id string, client *acp.Client) error {
	if _, exists := r.agents[id]; exists {
		return fmt.Errorf("duplicate ACP agent id %q", id)
	}
	initResp, err := client.Initialize(&acp.InitializeRequest{
		ProtocolVersion:    1,
		ClientCapabilities: acp.DefaultClientCapabilities(),
		ClientInfo:         &acp.Implementation{Name: "miya-channels", Version: "1.0.0"},
	})
	if err != nil {
		return fmt.Errorf("initialize ACP agent %q: %w", id, err)
	}
	if err := client.SendNotification("notifications/initialized", struct{}{}); err != nil {
		return fmt.Errorf("notify ACP agent %q initialized: %w", id, err)
	}
	runtime := &agentRuntime{id: id, client: client, loadSession: initResp.AgentCapabilities.LoadSession}
	r.agents[id] = runtime
	r.order = append(r.order, id)
	client.OnNotification(acp.NewNotificationHandler(&agentNotificationReceiver{agentID: id, registry: r}))
	if initResp.AgentInfo != nil {
		log.Printf("Connected to ACP agent %s: %s v%s", id, initResp.AgentInfo.Name, initResp.AgentInfo.Version)
	}
	return nil
}

func (r *agentRegistry) SetEventSink(sink func(string, AgentEvent)) {
	r.mu.Lock()
	r.eventSink = sink
	r.mu.Unlock()
}

func (r *agentRegistry) emit(agentID string, event AgentEvent) {
	r.mu.RLock()
	sink := r.eventSink
	r.mu.RUnlock()
	if sink != nil {
		sink(agentID, event)
	}
}

func (r *agentRegistry) Get(id string) (*agentRuntime, bool) {
	runtime, ok := r.agents[id]
	return runtime, ok
}

func (r *agentRegistry) DefaultID() string {
	if len(r.order) == 0 {
		return ""
	}
	return r.order[0]
}

func (r *agentRegistry) IDs() []string {
	ids := append([]string(nil), r.order...)
	sort.Strings(ids)
	return ids
}

func (r *agentRegistry) Close() {
	for _, runtime := range r.agents {
		_ = runtime.client.Close()
	}
}

type agentNotificationReceiver struct {
	acp.DefaultNotificationReceiver
	agentID  string
	registry *agentRegistry
}

func (r *agentNotificationReceiver) SessionUpdate(notification *acp.SessionNotification) {
	event, ok := normalizeAgentEvent(notification)
	if ok {
		r.registry.emit(r.agentID, event)
	}
}
