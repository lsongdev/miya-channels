package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lsongdev/telegram-go/telegram"
	"github.com/lsongdev/telegram-go/tgmd"
)

const telegramTextLimit = 3900
const telegramStartupAttempts = 3
const telegramStreamUpdateInterval = time.Second

type telegramConfig struct {
	telegram.Config
	AllowFrom []string `json:"allow_from"`
}

type TelegramChannel struct {
	bot *telegram.TelegramBot
}

func NewTelegramChannelFactory() ChannelFactory {
	return func(config json.RawMessage, _ ChannelOptions) (Channel, error) {
		var cfg telegramConfig
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
		bot := telegram.NewBot(&cfg.Config)
		me, err := telegramGetMeWithRetry(bot)
		if err != nil {
			return nil, fmt.Errorf("telegram getMe: %w", err)
		}
		log.Printf("[INFO] Telegram connected: %s(@%s)", me.FirstName, me.UserName)
		channel := &TelegramChannel{
			bot: bot,
		}
		return channel, nil
	}
}

func (t *TelegramChannel) Receive(ctx context.Context) (chan IncomingEvent, error) {
	incoming := make(chan IncomingEvent, 100)
	// Start polling
	go t.bot.StartPolling(ctx, func(update *telegram.Update, err error) {
		if err != nil {
			log.Printf("[WARN] Telegram polling error: %v", err)
			return
		}
		if update.Message == nil {
			return
		}
		if update.Message.Chat == nil {
			return
		}

		conversationID := fmt.Sprintf("%d", update.Message.Chat.ID)
		senderID := conversationID
		if update.Message.From != nil {
			senderID = fmt.Sprintf("%d", update.Message.From.ID)
		}
		text := update.Message.Text
		if text == "" && update.Message.Caption != nil {
			text = *update.Message.Caption
		}
		raw, _ := json.Marshal(update)
		incoming <- IncomingEvent{
			ConversationID: conversationID,
			SenderID:       senderID,
			MessageID:      fmt.Sprintf("%d", update.Message.MessageID),
			ReplyTo:        conversationID,
			Text:           text,
			Raw:            raw,
		}
	})
	return incoming, nil
}

func telegramGetMeWithRetry(bot *telegram.TelegramBot) (*telegram.User, error) {
	var lastErr error
	for attempt := 1; attempt <= telegramStartupAttempts; attempt++ {
		me, err := bot.GetMe()
		if err == nil {
			return me, nil
		}
		lastErr = err
		if attempt < telegramStartupAttempts {
			backoff := time.Duration(attempt) * time.Second
			log.Printf("[WARN] Telegram getMe failed (attempt %d/%d): %v; retrying in %s", attempt, telegramStartupAttempts, err, backoff)
			time.Sleep(backoff)
		}
	}
	return nil, lastErr
}

func (t *TelegramChannel) CreateReplyWriter(target string) Writer {
	return &TelegramWriter{
		bot:    t.bot,
		chatID: target,
	}
}

type TelegramWriter struct {
	bot            *telegram.TelegramBot
	chatID         string
	messageID      int64
	buffer         string
	published      string
	publishedHTML  bool
	lastUpdate     time.Time
	updateTimer    *time.Timer
	updateInterval time.Duration
	generation     uint64
	mu             sync.Mutex

	editTextOverride func(text, fallbackText string) (bool, error)
}

func (w *TelegramWriter) sendText(text string, parseMode string) (int64, error) {
	msg, err := w.bot.SendMessage(&telegram.MessageRequest{
		ChatID:    w.chatID,
		Text:      text,
		ParseMode: parseMode,
	})
	if err != nil {
		log.Printf("[ERROR] SendMessage: %v", err)
		return 0, err
	}
	return msg.MessageID, nil
}

func (w *TelegramWriter) editText(text, fallbackText string) (bool, error) {
	if w.editTextOverride != nil {
		return w.editTextOverride(text, fallbackText)
	}
	_, err := w.bot.EditMessageText(&telegram.EditMessageTextRequest{
		ChatID:    w.chatID,
		MessageID: w.messageID,
		Text:      text,
		ParseMode: "HTML",
	})
	if err != nil {
		log.Printf("[ERROR] EditMessageText: %v", err)
		msgID, err2 := w.sendText(fallbackText, "")
		if err2 != nil {
			log.Printf("[ERROR] SendMessage (fallback): %v", err2)
			return false, err2
		}
		w.messageID = msgID
		return false, nil
	}
	return true, nil
}

func (w *TelegramWriter) Write(s string, done bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if done && s == "" && w.messageID == 0 {
		return nil
	}
	w.buffer += s

	if w.messageID == 0 {
		w.bot.SendChatAction(&telegram.ChatAction{
			ChatID: w.chatID,
			Action: "typing",
		})

		preview := telegramPreview(w.buffer)
		msgID, err := w.sendText(preview, "")
		if err != nil {
			log.Printf("[ERROR] SendMessage (initial): %v", err)
			return err
		}
		w.messageID = msgID
		w.published = preview
		w.publishedHTML = false
		w.lastUpdate = time.Now()
		if done {
			return w.flushLocked()
		}
		return nil
	}

	if done {
		return w.flushLocked()
	}

	return w.scheduleUpdateLocked()
}

func (w *TelegramWriter) scheduleUpdateLocked() error {
	if w.updateTimer != nil {
		return nil
	}
	interval := w.updateInterval
	if interval <= 0 {
		interval = telegramStreamUpdateInterval
	}
	delay := time.Until(w.lastUpdate.Add(interval))
	if delay <= 0 {
		return w.publishPreviewLocked()
	}
	generation := w.generation
	w.updateTimer = time.AfterFunc(delay, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.updateTimer = nil
		if generation != w.generation || w.messageID == 0 {
			return
		}
		if err := w.publishPreviewLocked(); err != nil {
			log.Printf("[ERROR] Telegram stream update: %v", err)
		}
	})
	return nil
}

func (w *TelegramWriter) publishPreviewLocked() error {
	preview := telegramPreview(w.buffer)
	if preview == w.published {
		return nil
	}
	publishedHTML, err := w.editText(tgmd.Convert(preview), preview)
	if err != nil {
		return err
	}
	w.published = preview
	w.publishedHTML = publishedHTML
	w.lastUpdate = time.Now()
	return nil
}

func (w *TelegramWriter) flushLocked() error {
	if w.updateTimer != nil {
		w.updateTimer.Stop()
		w.updateTimer = nil
	}
	w.generation++

	parts := splitTelegramMarkdown(w.buffer, telegramTextLimit)
	if len(parts) == 0 {
		w.resetLocked()
		return nil
	}

	first := tgmd.Convert(parts[0])
	if w.messageID != 0 {
		if w.published != parts[0] || !w.publishedHTML {
			publishedHTML, err := w.editText(first, parts[0])
			if err != nil {
				return err
			}
			w.published = parts[0]
			w.publishedHTML = publishedHTML
		}
	} else {
		msgID, err := w.sendText(first, "HTML")
		if err != nil {
			return err
		}
		w.messageID = msgID
		w.published = parts[0]
		w.publishedHTML = true
	}

	for _, part := range parts[1:] {
		html := tgmd.Convert(part)
		if _, err := w.sendText(html, "HTML"); err != nil {
			return err
		}
	}

	w.resetLocked()
	return nil
}

func (w *TelegramWriter) resetLocked() {
	w.buffer = ""
	w.published = ""
	w.publishedHTML = false
	w.messageID = 0
	w.lastUpdate = time.Time{}
}

func telegramPreview(s string) string {
	parts := splitTelegramMarkdown(s, telegramTextLimit-16)
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.TrimRight(parts[0], "\n") + "\n\n…"
}

func splitTelegramMarkdown(s string, limit int) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if limit <= 0 {
		limit = telegramTextLimit
	}

	var parts []string
	var current strings.Builder
	var openFence string

	flush := func() {
		if strings.TrimSpace(current.String()) == "" {
			current.Reset()
			return
		}
		parts = append(parts, closeOpenFence(current.String(), openFence))
		current.Reset()
		if openFence != "" {
			current.WriteString(openFence)
			current.WriteString("\n")
		}
	}

	for _, line := range splitLinesAfter(s) {
		candidate := current.String() + line
		nextFence := updateFenceState(openFence, line)
		if current.Len() > 0 && renderedTelegramLen(closeOpenFence(candidate, nextFence)) > limit {
			flush()
		}
		if current.Len() == 0 && renderedTelegramLen(closeOpenFence(line, updateFenceState(openFence, line))) > limit {
			longParts, finalFence := splitLongMarkdownLine(line, openFence, limit)
			parts = append(parts, longParts...)
			openFence = finalFence
			if openFence != "" {
				current.WriteString(openFence)
				current.WriteString("\n")
			}
			continue
		}
		current.WriteString(line)
		openFence = updateFenceState(openFence, line)
	}
	flush()
	return parts
}

func splitLongMarkdownLine(line, openFence string, limit int) ([]string, string) {
	var parts []string
	remaining := line
	fence := openFence
	for remaining != "" {
		prefix, rest := splitRenderedPrefix(remaining, fence, limit)
		if prefix == "" {
			prefix, rest = splitAtRuneLimit(remaining, 1)
		}
		nextFence := updateFenceState(fence, prefix)
		parts = append(parts, closeOpenFence(prefix, nextFence))
		remaining = rest
		fence = nextFence
	}
	return parts, fence
}

func splitRenderedPrefix(s, openFence string, limit int) (string, string) {
	runes := []rune(s)
	best := 0
	for i := 1; i <= len(runes); i++ {
		prefix := string(runes[:i])
		if renderedTelegramLen(closeOpenFence(prefix, updateFenceState(openFence, prefix))) > limit {
			break
		}
		best = i
	}
	if best == 0 {
		return "", s
	}
	return string(runes[:best]), string(runes[best:])
}

func renderedTelegramLen(markdown string) int {
	return utf8.RuneCountInString(tgmd.Convert(markdown))
}

func closeOpenFence(s, openFence string) string {
	if openFence == "" {
		return s
	}
	closing := fenceMarker(openFence)
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s + closing + "\n"
}

func updateFenceState(openFence, text string) string {
	for _, line := range splitLinesAfter(text) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			if openFence == "" {
				openFence = trimmed
			} else if strings.HasPrefix(trimmed, fenceMarker(openFence)) {
				openFence = ""
			}
		}
	}
	return openFence
}

func fenceMarker(openFence string) string {
	trimmed := strings.TrimSpace(openFence)
	if trimmed == "" {
		return ""
	}
	marker := trimmed[0]
	length := 0
	for length < len(trimmed) && trimmed[length] == marker {
		length++
	}
	if length < 3 || (marker != '`' && marker != '~') {
		return "```"
	}
	return trimmed[:length]
}

func splitLinesAfter(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	return lines
}

func splitAtRuneLimit(s string, limit int) (string, string) {
	if limit <= 0 {
		return "", s
	}
	count := 0
	for i := range s {
		if count == limit {
			return s[:i], s[i:]
		}
		count++
	}
	return s, ""
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
	case "audio":
		a := telegram.AudioRequest{
			ChatID:  chatID,
			Audio:   params.URL,
			Caption: params.Caption,
		}
		_, err := t.bot.SendAudio(&a)
		return err
	case "file":
		d := telegram.DocumentRequest{
			ChatID:   chatID,
			Document: params.URL,
			Caption:  params.Caption,
		}
		_, err := t.bot.SendDocument(&d)
		return err
	default:
		return fmt.Errorf("unsupported type: %s", typ)
	}
}
