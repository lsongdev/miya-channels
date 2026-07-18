package channels

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lsongdev/wecom-go/wecom"
)

type WeComChannel struct {
	bot *wecom.WeComBot
}

func NewWeComChannelFactory() ChannelFactory {
	return func(c json.RawMessage, _ ChannelOptions) (ch Channel, err error) {
		var cfg wecom.WeComBotConfig
		err = json.Unmarshal(c, &cfg)
		ch = &WeComChannel{
			bot: wecom.NewWeComBot(&cfg),
		}
		return
	}
}

type WeComReplyWriter struct {
	bot    *wecom.WeComBot
	buffer string
	reply  (func(content string, done bool) (*wecom.WeComBotResponse, error))
}

// Write implements [agent.Writer].
func (w *WeComReplyWriter) Write(s string, done bool) (err error) {
	w.buffer += s
	_, err = w.reply(w.buffer, done)
	return
}

// CreateReplyWriter implements [Channel].
func (w *WeComChannel) CreateReplyWriter(target string) Writer {
	reply := w.bot.CreateReplyStream(target)
	return &WeComReplyWriter{
		bot:   w.bot,
		reply: reply,
	}
}

// Receive implements [Channel].
func (w *WeComChannel) Receive(ctx context.Context) (incoming chan IncomingEvent, err error) {
	incoming = make(chan IncomingEvent, 10)
	go w.bot.Start(ctx)
	go func() {
		defer close(incoming)
		for msg := range w.bot.IncomingMessage {
			var event wecom.WeComBotEvent
			json.Unmarshal(msg.Body, &event)
			if msg.Command == "aibot_msg_callback" {
				raw, _ := json.Marshal(event)
				incoming <- IncomingEvent{
					ConversationID: event.ChatID,
					SenderID:       event.From.UserID,
					MessageID:      event.MessageID,
					ReplyTo:        msg.RequestID(),
					Text:           event.Text.Content,
					Raw:            raw,
				}
			}
		}
	}()
	return
}

// SendFile implements [Channel].
func (w *WeComChannel) SendFile(target string, typ string, content string) error {
	return fmt.Errorf("file upload not implemented for WeCom channel")
}
