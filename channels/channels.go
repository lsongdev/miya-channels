package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/lsongdev/miya-channels/config"
)

type Writer interface {
	Write(s string, done bool) error
}

type Channel interface {
	Receive(context.Context) (chan IncomingMessage, error)
	CreateReplyWriter(target string) Writer
	SendFile(target, typ, content string) error
}

type IncomingMessage struct {
	From    string // channel name
	Who     string // user ID
	ReplyTo string
	Content string
}

type ChannelFactory func(config json.RawMessage) (Channel, error)

type ChannelManager struct {
	config   *config.Config
	channels map[string]Channel
	Incoming chan IncomingMessage
}

var factories map[string]ChannelFactory

func init() {
	// Create channel factories
	factories = map[string]ChannelFactory{
		"telegram": NewTelegramChannelFactory(),
		"feishu":   NewFeishuChannelFactory(),
		"wecom":    NewWeComChannelFactory(),
		"wechat":   NewWeChatChannelFactory(),
	}
}

func NewChannelManager(cfg *config.Config) (manager *ChannelManager) {
	manager = &ChannelManager{
		channels: make(map[string]Channel),
		Incoming: make(chan IncomingMessage, 100),
	}
	for name, channelConfig := range cfg.Channels {
		factory, ok := factories[name]
		if !ok {
			log.Printf("Channel factory not found: %s", name)
			continue
		}
		channel, err := factory(channelConfig)
		if err != nil {
			log.Printf("Error creating channel %s: %v", name, err)
			continue
		}
		manager.Register(name, channel)
	}
	return manager
}

func (cm *ChannelManager) Register(name string, channel Channel) {
	cm.channels[name] = channel
	log.Println("channel register", name)
}

func (cm *ChannelManager) Start(ctx context.Context) (err error) {
	// Start receiving messages
	for _, ch := range cm.channels {
		incoming, err := ch.Receive(ctx)
		if err != nil {
			return err
		}
		go func() {
			for msg := range incoming {
				cm.Incoming <- msg
			}
		}()
	}
	return
}

func (cm *ChannelManager) GetChannel(name string) (Channel, bool) {
	channel, ok := cm.channels[name]
	return channel, ok
}

func (cm *ChannelManager) ListChannels() []string {
	names := make([]string, 0, len(cm.channels))
	for name := range cm.channels {
		names = append(names, name)
	}
	return names
}

func (cm *ChannelManager) CreateReplyWriter(channelName, target string) (Writer, error) {
	channel, ok := cm.channels[channelName]
	if !ok {
		return nil, fmt.Errorf("channel not found: %s", channelName)
	}
	return channel.CreateReplyWriter(target), nil
}

func (cm *ChannelManager) Send(channelName, target, content string) error {
	channel, ok := cm.channels[channelName]
	if !ok {
		return fmt.Errorf("channel not found: %s", channelName)
	}
	writer := channel.CreateReplyWriter(target)
	return writer.Write(content, true)
}

func (cm *ChannelManager) SendFile(channelName, target, typ, content string) error {
	channel, ok := cm.channels[channelName]
	if !ok {
		return fmt.Errorf("channel not found: %s", channelName)
	}
	return channel.SendFile(target, typ, content)
}
