package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"time"

	"github.com/lsongdev/miya-channels/config"
	"github.com/lsongdev/wechatbot-go/wechatbot"
)

type WeChatChannel struct {
	bot      *wechatbot.WeChatBot
	cfg      *wechatbot.Config
	replyMap map[string]*wechatbot.ReplyMessage
	options  ChannelOptions
}

func NewWeChatChannelFactory() ChannelFactory {
	return func(rawConfig json.RawMessage, opts ChannelOptions) (channel Channel, err error) {
		var cfg wechatbot.Config
		if err := json.Unmarshal(rawConfig, &cfg); err != nil {
			return nil, err
		}
		if cfg.Token == "" {
			return nil, fmt.Errorf("wechat token missing; complete WeChat login before starting the channel service")
		}
		bot := wechatbot.NewBot(&cfg)
		return &WeChatChannel{
			bot:      bot,
			cfg:      &cfg,
			replyMap: make(map[string]*wechatbot.ReplyMessage),
			options:  opts,
		}, nil
	}
}

func (w *WeChatChannel) emit(status string, qrcode *wechatbot.QRCodeResponse, err error) {
	if w.options.Emit == nil {
		if qrcode != nil && status == "qrcode" {
			log.Println("WeChat QRCode:", qrcode.QRCodeImgContent)
		} else if err != nil {
			log.Printf("WeChat login %s: %v", status, err)
		} else if status != "" {
			log.Println("WeChat login:", status)
		}
	}
	event := ChannelEvent{
		Channel: "wechat",
		Type:    "login",
		Status:  status,
	}
	if qrcode != nil {
		event.QRCode = qrcode.QRCode
		event.QRCodeURL = qrcode.QRCodeImgContent
		event.QRCodeImage = qrcodeImage(qrcode.QRCodeImgContent)
	}
	if err != nil {
		event.Error = err.Error()
	}
	w.options.emit(event)
}

func qrcodeImage(content string) string {
	if content == "" {
		return ""
	}
	return "https://m.maoyan.com/qr?text=" + url.QueryEscape(content)
}

func LoginWeChat(ctx context.Context, rawConfig json.RawMessage, opts ChannelOptions) (*wechatbot.Config, error) {
	var cfg wechatbot.Config
	if err := json.Unmarshal(rawConfig, &cfg); err != nil {
		return nil, err
	}
	login := &WeChatChannel{
		bot:     wechatbot.NewBot(&cfg),
		cfg:     &cfg,
		options: opts,
	}
	if err := login.login(ctx, true); err != nil {
		return nil, err
	}
	return login.cfg, nil
}

func (w *WeChatChannel) login(ctx context.Context, _ bool) error {
	qrcode, err := w.bot.GetBotQRCode()
	if err != nil {
		w.emit("error", nil, err)
		return err
	}
	w.emit("qrcode", qrcode, nil)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	lastStatus := ""
	for {
		select {
		case <-ctx.Done():
			err := ctx.Err()
			w.emit("cancelled", qrcode, err)
			return err
		case <-ticker.C:
		}

		resp, err := w.bot.GetQRCodeStatus(qrcode.QRCode)
		if err != nil {
			w.emit("error", qrcode, err)
			return err
		}
		if resp.Status != "" && resp.Status != lastStatus {
			w.emit(resp.Status, qrcode, nil)
			lastStatus = resp.Status
		}
		switch resp.Status {
		case "wait", "scaned":
			continue
		case "confirmed":
			w.cfg.Token = resp.BotToken
			if resp.BaseURL != "" {
				w.cfg.BaseURL = resp.BaseURL
			}
			w.bot.Token = resp.BotToken
			return nil
		case "expired":
			err := fmt.Errorf("qrcode expired")
			w.emit("expired", qrcode, err)
			return err
		default:
			err := fmt.Errorf("qrcode %s", resp.Status)
			w.emit("error", qrcode, err)
			return err
		}
	}
}

func (w *WeChatChannel) SaveConfig() error {
	c, err := config.LoadConfig()
	if err != nil || c == nil {
		c = &config.Config{}
	}
	if c.Channels == nil {
		c.Channels = map[string]any{}
	}
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
