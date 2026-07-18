package app

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/lsongdev/miya-agents/acp"
	miyaagent "github.com/lsongdev/miya-agents/agent"
	"github.com/lsongdev/miya-channels/channels"
	"github.com/lsongdev/miya-channels/config"
)

type EndpointAgentClientFactory func(*config.Config, config.AgentConfig) (*acp.Client, error)

type Options struct {
	Config         *config.Config
	Channels       []config.ChannelInstance
	NewAgentClient EndpointAgentClientFactory
	OnEvent        func(channels.ChannelEvent)
}

func Run(ctx context.Context, opts Options) error {
	cfg := opts.Config
	instances := opts.Channels
	if cfg == nil {
		var err error
		cfg, instances, err = config.LoadGatewayConfig()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
	}
	if instances == nil {
		var err error
		instances, err = config.ChannelInstances(cfg)
		if err != nil {
			return fmt.Errorf("load channel instances: %w", err)
		}
	}

	lock, err := acquireProcessLock(config.ChannelsLockFile)
	if err != nil {
		return err
	}
	defer lock.Release()

	cm, err := channels.NewChannelManager(instances, channels.ChannelOptions{Emit: opts.OnEvent})
	if err != nil {
		return err
	}
	if len(cm.ListChannels()) == 0 {
		return fmt.Errorf("no channels configured")
	}

	registry, err := newAgentRegistry(cfg, opts)
	if err != nil {
		return err
	}
	defer registry.Close()

	if err := cm.Start(ctx); err != nil {
		return fmt.Errorf("start channels: %w", err)
	}

	store := newRouteStore()
	bindings, err := store.Load()
	if err != nil {
		return fmt.Errorf("load route bindings: %w", err)
	} else if len(bindings) > 0 {
		log.Printf("[channels] loaded %d route bindings", len(bindings))
	}

	gateway := newGateway(cm, registry, store, bindings)
	registry.SetEventSink(gateway.DeliverAgentEvent)
	log.Printf("Listening for messages (agents: %v)...", registry.IDs())

	for {
		select {
		case <-ctx.Done():
			log.Println("Shutting down channels...")
			gateway.Persist()
			return nil
		case event := <-cm.Incoming:
			gateway.Submit(event)
		}
	}
}

func DefaultEndpointAgentClient(cfg *config.Config, agent config.AgentConfig) (*acp.Client, error) {
	switch agent.Type {
	case "builtin":
		return acp.DialInProcess(miyaagent.NewAgentManager(cfg)), nil
	case "stdio":
	default:
		return nil, fmt.Errorf("agent %q transport %q is not supported", agent.ID, agent.Type)
	}
	if agent.Command == "" {
		return nil, fmt.Errorf("agent %q command is required", agent.ID)
	}
	client, err := acp.DialStdio(agent.Command, agent.Args...)
	if err != nil {
		return nil, fmt.Errorf("start ACP agent %q: %w", agent.ID, err)
	}
	return client, nil
}

const promptTimeout = 2 * time.Minute
