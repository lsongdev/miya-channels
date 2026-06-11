package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"

	"github.com/lsongdev/telegram-go/telegram"
	"github.com/lsongdev/telegram-go/tgmd"
)

type telegramConfig struct {
	telegram.Config
	AllowFrom []string `json:"allow_from"`
}

type TelegramChannel struct {
	bot *telegram.TelegramBot
}

func NewTelegramChannelFactory() ChannelFactory {
	return func(config json.RawMessage) (Channel, error) {
		var cfg telegramConfig
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
		bot := telegram.NewBot(&cfg.Config)
		me, err := bot.GetMe()
		if err != nil {
			return nil, err
		}
		log.Printf("Telegram: %s(@%s)", me.FirstName, me.UserName)
		channel := &TelegramChannel{
			bot: bot,
		}
		return channel, nil
	}
}

func (t *TelegramChannel) Receive(ctx context.Context) (chan IncomingMessage, error) {
	incoming := make(chan IncomingMessage, 100)
	// Start polling
	go t.bot.StartPolling(ctx, func(update *telegram.Update, err error) {
		if err != nil {
			return
		}
		if update.Message == nil {
			return
		}

		incoming <- IncomingMessage{
			From:    "telegram",
			Who:     fmt.Sprintf("%d", update.Message.Chat.ID),
			ReplyTo: fmt.Sprintf("%d", update.Message.Chat.ID),
			Content: update.Message.Text,
		}
	})
	return incoming, nil
}

func (t *TelegramChannel) CreateReplyWriter(target string) Writer {
	return &TelegramWriter{
		bot:    t.bot,
		chatID: target,
	}
}

type TelegramWriter struct {
	bot       *telegram.TelegramBot
	chatID    string
	messageID int64
	buffer    string
}

func (w *TelegramWriter) Write(s string, done bool) error {
	w.buffer += s
	if w.messageID == 0 {
		// First write - send typing action and create initial message
		w.bot.SendChatAction(&telegram.ChatAction{
			ChatID: w.chatID,
			Action: "typing",
		})
		msg, err := w.bot.SendMessage(&telegram.MessageRequest{
			ChatID: w.chatID,
			Text:   w.buffer,
		})
		if err != nil {
			return err
		}
		w.messageID = msg.MessageID
	}

	// Edit message periodically
	if len(w.buffer) > 0 && (done || len(w.buffer)%20 == 0) {
		_, err := w.bot.EditMessageText(&telegram.EditMessageTextRequest{
			ChatID:    w.chatID,
			MessageID: w.messageID,
			Text:      tgmd.Convert(w.buffer),
			ParseMode: "HTML",
		})
		if err != nil {
			return err
		}
	}
	if done {
		w.buffer = ""
		w.messageID = 0
	}

	return nil
}

func (t *TelegramChannel) SendFile(target, typ, content string) error {
	chatID, _ := strconv.Atoi(target)
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
		p := telegram.PhotoRequest{
			ChatID:  chatID,
			Photo:   params.URL,
			Caption: params.Caption,
		}
		_, err := t.bot.SendPhoto(&p)
		return err
	case "video":
		v := telegram.VideoRequest{
			ChatID:  chatID,
			Video:   params.URL,
			Caption: params.Caption,
		}
		_, err := t.bot.SendVideo(&v)
		return err
	default:
		return fmt.Errorf("unsupported type: %s", typ)
	}
}
