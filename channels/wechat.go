package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lsongdev/miya-channels/config"
	"github.com/lsongdev/wechatbot-go/wechatbot"
)

type WeChatChannel struct {
	bot        *wechatbot.WeChatBot
	cfg        *wechatbot.Config
	instanceID string
	replyMap   map[string]*wechatbot.ReplyMessage
	replyMu    sync.RWMutex
	options    ChannelOptions
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
			bot:        bot,
			cfg:        &cfg,
			instanceID: opts.Instance.ID,
			replyMap:   make(map[string]*wechatbot.ReplyMessage),
			options:    opts,
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
	raw, err := json.Marshal(map[string]any{
		"base_url":     w.cfg.BaseURL,
		"cdn_base_url": w.cfg.CDNBaseURL,
		"token":        w.cfg.Token,
		"updates_buf":  w.cfg.UpdatesBuf,
	})
	if err != nil {
		return err
	}
	if w.instanceID == "" {
		return fmt.Errorf("wechat channel instance id is required")
	}
	return config.UpdateChannelInstanceConfig(w.instanceID, raw)
}

func (w *WeChatChannel) Receive(ctx context.Context) (chan IncomingEvent, error) {
	incoming := make(chan IncomingEvent, 100)
	go func() {
		defer close(incoming)
		w.bot.Start(ctx, func(message *wechatbot.Message) {
			replyTo := fmt.Sprintf("%d", message.MessageID)
			if message.MessageID == 0 {
				replyTo = message.FromUserID
			}
			w.replyMu.Lock()
			w.replyMap[replyTo] = w.bot.CreateReply(message)
			w.replyMu.Unlock()
			attachments, attachmentErr := w.attachments(message)
			text := message.Text()
			if attachmentErr != nil {
				text += fmt.Sprintf("\n[Attachment error: %v]", attachmentErr)
			}
			conversationID := message.SessionID
			if message.GroupID != "" {
				conversationID = message.GroupID
			}
			if conversationID == "" {
				conversationID = message.FromUserID
			}
			raw, _ := json.Marshal(message)
			incoming <- IncomingEvent{
				ConversationID: conversationID,
				SenderID:       message.FromUserID,
				MessageID:      fmt.Sprintf("%d", message.MessageID),
				ReplyTo:        replyTo,
				Text:           text,
				Attachments:    attachments,
				Raw:            raw,
			}
			w.SaveConfig()
		})
	}()
	return incoming, nil
}

func (w *WeChatChannel) attachments(message *wechatbot.Message) ([]Attachment, error) {
	attachments := make([]Attachment, 0)
	for _, item := range message.ItemList {
		var attachment Attachment
		var media *wechatbot.CDNMedia
		switch {
		case item.ImageItem != nil:
			attachment = Attachment{Type: "image", Name: "image.jpg", MimeType: "image/jpeg", Size: item.ImageItem.MidSize}
			media = item.ImageItem.Media
		case item.VoiceItem != nil:
			attachment = Attachment{Type: "audio", Name: "voice.silk", MimeType: "audio/silk"}
			media = item.VoiceItem.Media
		case item.FileItem != nil:
			attachment = Attachment{Type: "file", Name: item.FileItem.FileName}
			media = item.FileItem.Media
		case item.VideoItem != nil:
			attachment = Attachment{Type: "video", Name: "video.mp4", MimeType: "video/mp4", Size: item.VideoItem.VideoSize}
			media = item.VideoItem.Media
		default:
			continue
		}
		if media == nil {
			continue
		}
		data, err := w.bot.DownloadMedia(media)
		if err != nil {
			return attachments, err
		}
		attachment.Data = data
		if attachment.Size == 0 {
			attachment.Size = int64(len(data))
		}
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

func (w *WeChatChannel) CreateReplyWriter(target string) Writer {
	w.replyMu.RLock()
	replyMessage := w.replyMap[target]
	w.replyMu.RUnlock()
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
	if w.replyMessage == nil {
		return fmt.Errorf("wechat reply target not found: %s", w.userID)
	}
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
		Name    string `json:"name,omitempty"`
		Mime    string `json:"mime,omitempty"`
	}
	if err := json.Unmarshal([]byte(content), &params); err != nil {
		return err
	}

	w.replyMu.RLock()
	replyMessage := w.replyMap[target]
	w.replyMu.RUnlock()
	if replyMessage == nil {
		return fmt.Errorf("wechat reply target not found: %s", target)
	}

	fileName, data, err := loadWechatAttachment(params.URL, params.Name)
	if err != nil {
		return err
	}
	switch typ {
	case "image":
		_, err = replyMessage.ReplyImage(fileName, data)
	case "video":
		_, err = replyMessage.ReplyVideo(fileName, data, nil)
	case "audio", "file":
		_, err = replyMessage.ReplyFile(fileName, data)
	default:
		err = fmt.Errorf("unsupported type: %s", typ)
	}
	return err
}

func loadWechatAttachment(rawURL, name string) (string, []byte, error) {
	if rawURL == "" {
		return "", nil, fmt.Errorf("attachment url missing")
	}
	fileName := attachmentName(rawURL, name)
	if path, ok := strings.CutPrefix(rawURL, "file://"); ok {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", nil, err
		}
		return fileName, data, nil
	}
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		resp, err := http.Get(rawURL)
		if err != nil {
			return "", nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return "", nil, fmt.Errorf("download attachment: HTTP %d", resp.StatusCode)
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", nil, err
		}
		return fileName, data, nil
	}
	return "", nil, fmt.Errorf("unsupported attachment url: %s", rawURL)
}

func attachmentName(rawURL, name string) string {
	if name != "" {
		return filepath.Base(name)
	}
	if path, ok := strings.CutPrefix(rawURL, "file://"); ok {
		return filepath.Base(path)
	}
	if u, err := url.Parse(rawURL); err == nil && u.Path != "" {
		return filepath.Base(u.Path)
	}
	return "attachment"
}
