package channels

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lsongdev/miya-channels/config"
	"github.com/lsongdev/wechatbot-go/wechatbot"
)

func TestWechatAttachmentNamePrefersPayloadName(t *testing.T) {
	got := attachmentName("file:///tmp/generated-name.bin", "report.pdf")
	if got != "report.pdf" {
		t.Fatalf("attachmentName = %q", got)
	}
}

func TestWechatAttachmentNameFromFileURL(t *testing.T) {
	got := attachmentName("file:///tmp/capture.jpg", "")
	if got != "capture.jpg" {
		t.Fatalf("attachmentName = %q", got)
	}
}

func TestWechatAttachmentNameFromHTTPURL(t *testing.T) {
	got := attachmentName("https://example.com/files/camera.mp4?token=abc", "")
	if got != "camera.mp4" {
		t.Fatalf("attachmentName = %q", got)
	}
}

func TestWechatSaveConfigWritesObject(t *testing.T) {
	oldPath := config.ConfigPath
	oldFile := config.ConfigFile
	temp := t.TempDir()
	config.ConfigPath = temp
	config.ConfigFile = filepath.Join(temp, "config.json")
	t.Cleanup(func() {
		config.ConfigPath = oldPath
		config.ConfigFile = oldFile
	})

	channel := &WeChatChannel{
		cfg: &wechatbot.Config{
			BaseURL:    "https://example.com/base",
			CDNBaseURL: "https://example.com/cdn",
			Token:      "wechat-token",
			UpdatesBuf: "cursor",
		},
	}
	if err := channel.SaveConfig(); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	data, err := os.ReadFile(config.ConfigFile)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var raw struct {
		Channels map[string]any `json:"channels"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal saved config: %v", err)
	}
	wechat, ok := raw.Channels["wechat"].(map[string]any)
	if !ok {
		t.Fatalf("wechat config = %#v", raw.Channels["wechat"])
	}
	if wechat["token"] != "wechat-token" || wechat["updates_buf"] != "cursor" {
		t.Fatalf("wechat config = %#v", wechat)
	}
}
