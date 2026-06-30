package channels

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lsongdev/miya-channels/config"
	"github.com/lsongdev/wechatbot-go/wechatbot"
)

type WeChatChannel struct {
	bot      *wechatbot.WeChatBot
	cfg      *wechatbot.Config
	replyMap map[string]*wechatbot.ReplyMessage
}

func NewWeChatChannelFactory() ChannelFactory {
	return func(rawConfig json.RawMessage) (channel Channel, err error) {
		var cfg wechatbot.Config
		if err := json.Unmarshal(rawConfig, &cfg); err != nil {
			return nil, err
		}
		bot := wechatbot.NewBot(&cfg)
		bot.Login(context.Background(), false)
		channel = &WeChatChannel{
			bot:      bot,
			cfg:      &cfg,
			replyMap: make(map[string]*wechatbot.ReplyMessage),
		}
		return channel, nil
	}
}

func (w *WeChatChannel) SaveConfig() error {
	c, _ := config.LoadConfig()
	d, _ := json.Marshal(w.cfg)
	c.Channels["wechat"] = d
	return c.Save()
}

func (w *WeChatChannel) Receive(ctx context.Context) (chan IncomingMessage, error) {
	incoming := make(chan IncomingMessage, 100)
	go func() {
		defer close(incoming)
		w.bot.Start(ctx, func(message *wechatbot.Message) {
			w.replyMap[message.FromUserID] = w.bot.CreateReply(message)
			incoming <- IncomingMessage{
				From:    "wechat",
				Who:     message.FromUserID,
				ReplyTo: message.FromUserID,
				Content: message.Text(),
			}
			w.SaveConfig()
		})
	}()
	return incoming, nil
}

func (w *WeChatChannel) CreateReplyWriter(target string) Writer {
	replyMessage := w.replyMap[target]
	return &WeChatWriter{
		userID:       target,
		replyMessage: replyMessage,
		buffer:       "",
		first:        true,
	}
}

type WeChatWriter struct {
	userID       string
	buffer       string
	first        bool
	replyMessage *wechatbot.ReplyMessage
}

func (w *WeChatWriter) Write(s string, done bool) error {
	w.buffer += s

	if w.first {
		// Send typing status
		w.replyMessage.Typing(wechatbot.Typing)
		w.first = false
	}

	if done {
		// Send the complete message
		w.replyMessage.ReplyText(w.buffer)
		w.replyMessage.Typing(wechatbot.CancelTyping)
		w.buffer = ""
	}

	return nil
}

func (w *WeChatChannel) SendFile(target, typ, content string) error {
	var params struct {
		Type    string `json:"type"`
		URL     string `json:"url"`
		Caption string `json:"caption,omitempty"`
	}
	if err := json.Unmarshal([]byte(content), &params); err != nil {
		return err
	}

	// WeChat iLink bot doesn't have direct file upload API exposed
	// This would require additional implementation based on the CDN upload flow
	return fmt.Errorf("file upload not implemented for WeChat channel")
}
