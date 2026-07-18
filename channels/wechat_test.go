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

func TestWechatSaveConfigUpdatesInstance(t *testing.T) {
	oldPath := config.ConfigPath
	oldFile := config.ConfigFile
	temp := t.TempDir()
	config.ConfigPath = temp
	config.ConfigFile = filepath.Join(temp, "config.json")
	t.Cleanup(func() {
		config.ConfigPath = oldPath
		config.ConfigFile = oldFile
	})
	initial := `{"channels":[{"id":"wx-personal","type":"wechat","config":{"token":"old"}}]}`
	if err := os.WriteFile(config.ConfigFile, []byte(initial), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	channel := &WeChatChannel{
		instanceID: "wx-personal",
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
		Channels []config.ChannelInstance `json:"channels"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal saved config: %v", err)
	}
	if len(raw.Channels) != 1 {
		t.Fatalf("channels = %#v", raw.Channels)
	}
	var wechat map[string]any
	if err := json.Unmarshal(raw.Channels[0].Config, &wechat); err != nil {
		t.Fatalf("wechat config: %v", err)
	}
	if wechat["token"] != "wechat-token" || wechat["updates_buf"] != "cursor" {
		t.Fatalf("wechat config = %#v", wechat)
	}
}
