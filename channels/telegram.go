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

const maxMsgLen = 4000

func (w *TelegramWriter) sendChunkHTML(chunkHTML string, isFirst bool) error {
	if isFirst && w.messageID != 0 {
		_, err := w.bot.EditMessageText(&telegram.EditMessageTextRequest{
			ChatID:    w.chatID,
			MessageID: w.messageID,
			Text:      chunkHTML,
			ParseMode: "HTML",
		})
		return err
	}
	_, err := w.bot.SendMessage(&telegram.MessageRequest{
		ChatID: w.chatID,
		Text:   chunkHTML,
	})
	return err
}

func (w *TelegramWriter) splitAndSend(text string, isFirstChunkEdit bool) error {
	runes := []rune(text)
	for i := 0; i < len(runes); {
		lo, hi := i+1, len(runes)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			chunkHTML := tgmd.Convert(string(runes[i:mid]))
			if len(chunkHTML) <= maxMsgLen {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		chunk := string(runes[i:lo])
		chunkHTML := tgmd.Convert(chunk)
		if err := w.sendChunkHTML(chunkHTML, isFirstChunkEdit && i == 0); err != nil {
			return err
		}
		i = lo
	}
	return nil
}

func (w *TelegramWriter) Write(s string, done bool) error {
	w.buffer += s

	if w.messageID == 0 {
		w.bot.SendChatAction(&telegram.ChatAction{
			ChatID: w.chatID,
			Action: "typing",
		})

		if done {
			html := tgmd.Convert(w.buffer)
			if len(html) >= maxMsgLen {
				err := w.splitAndSend(w.buffer, false)
				w.buffer = ""
				w.messageID = 0
				return err
			}
		}

		msg, err := w.bot.SendMessage(&telegram.MessageRequest{
			ChatID: w.chatID,
			Text:   w.buffer,
		})
		if err != nil {
			return err
		}
		w.messageID = msg.MessageID
		if !done {
			return nil
		}
	}

	html := tgmd.Convert(w.buffer)
	if !done && len(w.buffer)%20 == 0 && len(html) < maxMsgLen {
		_, err := w.bot.EditMessageText(&telegram.EditMessageTextRequest{
			ChatID:    w.chatID,
			MessageID: w.messageID,
			Text:      html,
			ParseMode: "HTML",
		})
		if err != nil {
			return err
		}
	}

	if done {
		html := tgmd.Convert(w.buffer)
		if len(html) >= maxMsgLen {
			err := w.splitAndSend(w.buffer, true)
			if err != nil {
				return err
			}
		} else {
			_, err := w.bot.EditMessageText(&telegram.EditMessageTextRequest{
				ChatID:    w.chatID,
				MessageID: w.messageID,
				Text:      html,
				ParseMode: "HTML",
			})
			if err != nil {
				return err
			}
		}
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
