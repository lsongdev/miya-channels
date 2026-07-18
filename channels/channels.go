package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/lsongdev/miya-channels/config"
)

type Writer interface {
	Write(s string, done bool) error
}

type Channel interface {
	Receive(context.Context) (chan IncomingEvent, error)
	CreateReplyWriter(target string) Writer
	SendFile(target, typ, content string) error
}

type Attachment struct {
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	URL      string `json:"url,omitempty"`
	Data     []byte `json:"data,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

type IncomingEvent struct {
	ChannelID      string          `json:"channelId"`
	ChannelType    string          `json:"channelType"`
	ConversationID string          `json:"conversationId"`
	SenderID       string          `json:"senderId"`
	MessageID      string          `json:"messageId,omitempty"`
	ReplyTo        string          `json:"replyTo,omitempty"`
	Text           string          `json:"text,omitempty"`
	Attachments    []Attachment    `json:"attachments,omitempty"`
	Raw            json.RawMessage `json:"raw,omitempty"`
	ReceivedAt     time.Time       `json:"receivedAt"`
}

func (e IncomingEvent) SourceKey() string {
	return fmt.Sprintf("%s:conversation:%s:sender:%s", e.ChannelID, e.ConversationID, e.SenderID)
}

type DeliveryItem struct {
	Kind      string      `json:"kind"`
	Text      string      `json:"text,omitempty"`
	File      *Attachment `json:"file,omitempty"`
	Format    string      `json:"format,omitempty"`
	Final     bool        `json:"final,omitempty"`
	Sensitive bool        `json:"sensitive,omitempty"`
}

type ChannelEvent struct {
	Channel     string `json:"channel"` // channel instance ID
	ChannelType string `json:"channelType,omitempty"`
	Type        string `json:"type"`
	Status      string `json:"status,omitempty"`
	QRCode      string `json:"qrcode,omitempty"`
	QRCodeURL   string `json:"qrcodeUrl,omitempty"`
	QRCodeImage string `json:"qrcodeImage,omitempty"`
	Error       string `json:"error,omitempty"`
}

type ChannelOptions struct {
	Instance config.ChannelInstance
	Emit     func(ChannelEvent)
}

func (opts ChannelOptions) emit(event ChannelEvent) {
	if opts.Emit != nil {
		opts.Emit(event)
	}
}

type ChannelFactory func(config json.RawMessage, opts ChannelOptions) (Channel, error)

type ChannelManager struct {
	channels  map[string]Channel
	instances map[string]config.ChannelInstance
	Incoming  chan IncomingEvent
	options   ChannelOptions
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

func NewChannelManager(instances []config.ChannelInstance, opts ChannelOptions) (*ChannelManager, error) {
	manager := &ChannelManager{
		channels:  make(map[string]Channel),
		instances: make(map[string]config.ChannelInstance),
		Incoming:  make(chan IncomingEvent, 100),
		options:   opts,
	}
	for _, instance := range instances {
		factory, ok := factories[instance.Type]
		if !ok {
			return nil, fmt.Errorf("channel factory not found: %s", instance.Type)
		}
		instanceOpts := opts
		instanceOpts.Instance = instance
		baseEmit := opts.Emit
		instanceOpts.Emit = func(event ChannelEvent) {
			event.Channel = instance.ID
			event.ChannelType = instance.Type
			if baseEmit != nil {
				baseEmit(event)
			}
		}
		channel, err := factory(instance.Config, instanceOpts)
		if err != nil {
			return nil, fmt.Errorf("create channel %s (%s): %w", instance.ID, instance.Type, err)
		}
		manager.RegisterInstance(instance, channel)
	}
	return manager, nil
}

func (cm *ChannelManager) RegisterInstance(instance config.ChannelInstance, channel Channel) {
	name := instance.ID
	cm.channels[name] = channel
	cm.instances[name] = instance
	log.Println("channel register", name)
}

func (cm *ChannelManager) Start(ctx context.Context) (err error) {
	// Start receiving messages
	for id, ch := range cm.channels {
		incoming, err := ch.Receive(ctx)
		if err != nil {
			return err
		}
		instance := cm.instances[id]
		go func() {
			for msg := range incoming {
				msg.ChannelID = instance.ID
				msg.ChannelType = instance.Type
				if msg.ReceivedAt.IsZero() {
					msg.ReceivedAt = time.Now()
				}
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
	sort.Strings(names)
	return names
}

func (cm *ChannelManager) Instance(id string) (config.ChannelInstance, bool) {
	instance, ok := cm.instances[id]
	return instance, ok
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
