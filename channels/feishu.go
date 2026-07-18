package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/lsongdev/feishu-go/feishu"
)

type FeishuChannel struct {
	client *feishu.Client
}

func NewFeishuChannelFactory() ChannelFactory {
	return func(config json.RawMessage, _ ChannelOptions) (Channel, error) {
		var cfg feishu.Config
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
		cfg.WSConfig = *feishu.DefaultWSConfig()
		client := feishu.NewClient(&cfg)
		resp, err := client.GetTenantAccessTokenInternal()
		if err != nil {
			return nil, err
		}
		client.SetAccessToken(resp.TenantAccessToken)
		channel := &FeishuChannel{
			client: client,
		}
		return channel, nil
	}
}

func (f *FeishuChannel) Receive(ctx context.Context) (incoming chan IncomingEvent, err error) {
	incoming = make(chan IncomingEvent, 100)
	// Start listening for messages
	f.client.Start(ctx)
	go func() {
		defer close(incoming)
		for event := range f.client.IncomingMessage {
			if event.Header.EventType == feishu.EVENT_MESSAGE_RECEIVE {
				// log.Println(event.Header.EventType, string(event.Event))
				var msgReceive feishu.MessageReceiveEvent
				err = json.Unmarshal(event.Event, &msgReceive)
				content := ""
				if msgReceive.Message.Content != "" {
					var contentMap map[string]string
					if err := json.Unmarshal([]byte(msgReceive.Message.Content), &contentMap); err == nil {
						content = contentMap["text"]
					}
				}
				senderID := msgReceive.Sender.SenderID.UserID
				if senderID == "" {
					senderID = msgReceive.Sender.SenderID.OpenID
				}
				raw, _ := json.Marshal(msgReceive)
				incoming <- IncomingEvent{
					ConversationID: msgReceive.Message.ChatID,
					SenderID:       senderID,
					MessageID:      msgReceive.Message.MessageID,
					ReplyTo:        msgReceive.Message.MessageID,
					Text:           content,
					Raw:            raw,
				}
			}
		}
	}()
	return incoming, nil
}

func (f *FeishuChannel) CreateReplyWriter(target string) Writer {
	return &FeishuWriter{
		client:    f.client,
		messageID: target,
		first:     true,
	}
}

type FeishuWriter struct {
	client    *feishu.Client
	messageID string
	buffer    string
	first     bool
}

func (w *FeishuWriter) Write(s string, done bool) error {
	w.buffer += s

	if w.first {
		_, err := w.client.AddReaction(w.messageID, feishu.EMOJI_Typing)
		if err != nil {
			return err
		}
		w.first = false
	}
	if done {
		message := feishu.NewMarkdownMessage("", w.buffer)
		_, err := w.client.SendReplyMessage(w.messageID, &message)
		if err != nil {
			return err
		}
		w.buffer = ""
	}
	return nil
}

func (c *FeishuChannel) SendFile(target, typ, content string) error {
	var params struct {
		Type    string `json:"type"`
		URL     string `json:"url"`
		Caption string `json:"caption,omitempty"`
	}
	if err := json.Unmarshal([]byte(content), &params); err != nil {
		return err
	}
	switch typ {
	case "image":
		filename, _ := strings.CutPrefix(params.URL, "file://")
		f, err := os.Open(filename)
		if err != nil {
			return err
		}
		defer f.Close()
		resp, err := c.client.UploadImage("message", f)
		if err != nil {
			return err
		}
		imageMessage := feishu.NewImageMessage(resp.Data.ImageKey)
		imageMessage.ReceiveIdType = "user_id"
		_, err = c.client.SendReplyMessage(target, &imageMessage)
		return err
	default:
		return fmt.Errorf("unsupported type: %s", typ)
	}
}
