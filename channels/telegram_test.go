package channels

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTelegramWriterPublishesBufferedTextAfterInterval(t *testing.T) {
	updates := make(chan string, 1)
	w := &TelegramWriter{
		messageID:      1,
		buffer:         "hello",
		published:      "hello",
		lastUpdate:     time.Now(),
		updateInterval: 20 * time.Millisecond,
		editTextOverride: func(_, fallbackText string) (bool, error) {
			updates <- fallbackText
			return true, nil
		},
	}

	if err := w.Write(" world", false); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	select {
	case got := <-updates:
		if got != "hello world" {
			t.Fatalf("published text = %q", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("buffered update was not published")
	}

	if err := w.Write("", true); err != nil {
		t.Fatalf("final Write() error = %v", err)
	}
	select {
	case got := <-updates:
		t.Fatalf("final flush repeated unchanged edit: %q", got)
	default:
	}
}

func TestTelegramWriterFinalWriteCancelsPendingUpdate(t *testing.T) {
	var updates []string
	w := &TelegramWriter{
		messageID:      1,
		buffer:         "hello",
		published:      "hello",
		lastUpdate:     time.Now(),
		updateInterval: time.Hour,
		editTextOverride: func(_, fallbackText string) (bool, error) {
			updates = append(updates, fallbackText)
			return true, nil
		},
	}

	if err := w.Write(" world", false); err != nil {
		t.Fatalf("stream Write() error = %v", err)
	}
	if err := w.Write("!", true); err != nil {
		t.Fatalf("final Write() error = %v", err)
	}
	if len(updates) != 1 || updates[0] != "hello world!" {
		t.Fatalf("updates = %#v", updates)
	}
	if w.updateTimer != nil {
		t.Fatal("pending update timer was not cleared")
	}
}

func TestTelegramWriterFinalizesInitialPlainTextAsHTML(t *testing.T) {
	var edits int
	w := &TelegramWriter{
		messageID: 1,
		buffer:    "**hello**",
		published: "**hello**",
		editTextOverride: func(_, _ string) (bool, error) {
			edits++
			return true, nil
		},
	}

	if err := w.Write("", true); err != nil {
		t.Fatalf("final Write() error = %v", err)
	}
	if edits != 1 {
		t.Fatalf("HTML finalization edits = %d", edits)
	}
}

func TestSplitTelegramMarkdownLimitsRenderedHTMLByRunes(t *testing.T) {
	input := strings.Repeat("你", telegramTextLimit+10)
	parts := splitTelegramMarkdown(input, telegramTextLimit)
	if len(parts) != 2 {
		t.Fatalf("parts = %d", len(parts))
	}
	for _, part := range parts {
		if got := renderedTelegramLen(part); got > telegramTextLimit {
			t.Fatalf("rendered part rune count = %d", got)
		}
	}
	if strings.Join(parts, "") != input {
		t.Fatalf("split changed content")
	}
}

func TestSplitTelegramMarkdownPrefersParagraphBoundary(t *testing.T) {
	first := strings.Repeat("a", 80)
	second := strings.Repeat("b", 80)
	parts := splitTelegramMarkdown(first+"\n\n"+second, 100)
	if len(parts) != 2 {
		t.Fatalf("parts = %d", len(parts))
	}
	if strings.TrimSpace(parts[0]) != first {
		t.Fatalf("first part = %q", parts[0])
	}
	if strings.TrimSpace(parts[1]) != second {
		t.Fatalf("second part = %q", parts[1])
	}
}

func TestSplitTelegramMarkdownClosesOpenFence(t *testing.T) {
	input := "```go\n" + strings.Repeat("fmt.Println(\"hello\")\n", 500) + "```\n"
	parts := splitTelegramMarkdown(input, 500)
	if len(parts) < 2 {
		t.Fatalf("expected multiple parts, got %d", len(parts))
	}
	for i, part := range parts {
		if strings.Count(part, "```")%2 != 0 {
			t.Fatalf("part %d has unbalanced fence: %q", i, part)
		}
		if got := renderedTelegramLen(part); got > 500 {
			t.Fatalf("part %d rendered length = %d", i, got)
		}
	}
}

func TestTelegramPreviewAddsEllipsisForLongText(t *testing.T) {
	input := strings.Repeat("x", telegramTextLimit+1)
	preview := telegramPreview(input)
	if renderedTelegramLen(preview) > telegramTextLimit {
		t.Fatalf("preview too long: %d", utf8.RuneCountInString(preview))
	}
	if !strings.Contains(preview, "…") {
		t.Fatalf("preview missing ellipsis")
	}
}

func TestFenceMarkerPreservesLongFence(t *testing.T) {
	if got := fenceMarker("````text"); got != "````" {
		t.Fatalf("fenceMarker() = %q", got)
	}
}
