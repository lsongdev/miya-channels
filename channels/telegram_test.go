package channels

import (
	"strings"
	"testing"
	"unicode/utf8"
)

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
