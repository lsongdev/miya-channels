package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lsongdev/miya-agents/acp"
	"github.com/lsongdev/miya-channels/channels"
	"github.com/lsongdev/miya-channels/config"
)

func TestFileDeliveryImageURI(t *testing.T) {
	uri := "file:///tmp/capture.jpg"
	payload, ok, err := fileDelivery(acp.ContentBlock{
		Type:     "image",
		MimeType: "image/jpeg",
		URI:      &uri,
		Name:     "capture.jpg",
	})
	if err != nil {
		t.Fatalf("fileDelivery: %v", err)
	}
	if !ok {
		t.Fatal("expected file delivery")
	}
	if payload.Type != "image" || payload.URL != uri || payload.Caption != "capture.jpg" {
		t.Fatalf("payload = %#v", payload)
	}
}

type commandTestChannel struct{}

func (commandTestChannel) Receive(context.Context) (chan channels.IncomingEvent, error) {
	return make(chan channels.IncomingEvent), nil
}
func (commandTestChannel) CreateReplyWriter(string) channels.Writer { return &recordingWriter{} }
func (commandTestChannel) SendFile(string, string, string) error    { return nil }

func TestAgentCommandSwitchesBindingAndKeepsAgentSessions(t *testing.T) {
	manager, err := channels.NewChannelManager(nil, channels.ChannelOptions{})
	if err != nil {
		t.Fatalf("NewChannelManager: %v", err)
	}
	manager.RegisterInstance(config.ChannelInstance{
		ID: "tg-lab", Type: "telegram",
		Commands: config.CommandConfig{AgentSwitch: true, AllowedAgents: []string{"miya", "research"}},
	}, commandTestChannel{})
	registry := &agentRegistry{
		agents: map[string]*agentRuntime{"miya": {id: "miya"}, "research": {id: "research"}},
		order:  []string{"miya", "research"},
	}
	gateway := newGateway(manager, registry, nil, make(map[string]*routeBinding))
	binding := &routeBinding{
		AgentID: "miya",
		Sessions: map[string]*sessionState{
			"miya": {SessionID: "miya-session"},
		},
	}
	writer := &recordingWriter{}
	gateway.agentCommand(channels.IncomingEvent{ChannelID: "tg-lab"}, binding, writer, []string{"research"})
	if binding.AgentID != "research" {
		t.Fatalf("agent = %q", binding.AgentID)
	}
	if binding.Sessions["miya"].SessionID != "miya-session" {
		t.Fatalf("sessions = %#v", binding.Sessions)
	}
}

func TestFileDeliveryVideoResource(t *testing.T) {
	uri := "file:///tmp/camera.mp4"
	payload, ok, err := fileDelivery(acp.ContentBlock{
		Type:     "resource_link",
		MimeType: "video/mp4",
		URI:      &uri,
		Name:     "camera.mp4",
	})
	if err != nil {
		t.Fatalf("fileDelivery: %v", err)
	}
	if !ok {
		t.Fatal("expected file delivery")
	}
	if payload.Type != "video" {
		t.Fatalf("type = %q", payload.Type)
	}
}

func TestFileDeliveryInlineDataWritesTempFile(t *testing.T) {
	payload, ok, err := fileDelivery(acp.ContentBlock{
		Type:     "resource",
		MimeType: "application/pdf",
		Name:     "report.pdf",
		Data:     base64.StdEncoding.EncodeToString([]byte("pdf")),
	})
	if err != nil {
		t.Fatalf("fileDelivery: %v", err)
	}
	if !ok {
		t.Fatal("expected file delivery")
	}
	if payload.Type != "file" || !strings.HasPrefix(payload.URL, "file://") {
		t.Fatalf("payload = %#v", payload)
	}
	path := strings.TrimPrefix(payload.URL, "file://")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp attachment: %v", err)
	}
	if string(data) != "pdf" {
		t.Fatalf("temp attachment = %q", data)
	}
}

func TestRouteStoreRoundTrip(t *testing.T) {
	store := &routeStore{path: filepath.Join(t.TempDir(), "channels", "sessions.json")}
	input := map[string]*routeBinding{
		"wechat:conversation:user-1:sender:user-1": {
			ChannelID: "wechat",
			AgentID:   "miya",
			Sessions: map[string]*sessionState{
				"miya": {SessionID: acp.SessionID("sess-1"), Cwd: "/tmp/work", Loaded: true},
			},
		},
	}
	if err := store.Save(input); err != nil {
		t.Fatalf("save sessions: %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load sessions: %v", err)
	}
	binding := loaded["wechat:conversation:user-1:sender:user-1"]
	if binding == nil {
		t.Fatal("missing loaded binding")
	}
	sess := binding.Sessions["miya"]
	if sess == nil {
		t.Fatal("missing loaded session")
	}
	if sess.SessionID != "sess-1" || sess.Cwd != "/tmp/work" {
		t.Fatalf("loaded session = %#v", sess)
	}
	if sess.Loaded {
		t.Fatal("persisted sessions must start unloaded so the worker can LoadSession")
	}
}

func TestRouteStoreRejectsLegacyFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	if err := os.WriteFile(path, []byte(`{"telegram:user-1":{"sessionId":"legacy-session","cwd":"/tmp/work"}}`), 0600); err != nil {
		t.Fatalf("write legacy store: %v", err)
	}
	store := &routeStore{path: path}
	if _, err := store.Load(); err == nil {
		t.Fatal("expected legacy route store to be rejected")
	}
}

func TestParseCommandSupportsTelegramBotSuffix(t *testing.T) {
	name, args := parseCommand(" /agent@miya_bot research ")
	if name != "agent" || len(args) != 1 || args[0] != "research" {
		t.Fatalf("parseCommand = %q, %#v", name, args)
	}
}

func TestRenderAgentEventHidesThoughtExceptDebug(t *testing.T) {
	event := AgentEvent{Type: AgentEventThoughtDelta, Text: "private reasoning"}
	if items := renderAgentEvent(event, "normal"); len(items) != 0 {
		t.Fatalf("normal items = %#v", items)
	}
	items := renderAgentEvent(event, "debug")
	if len(items) != 1 || !items[0].Sensitive {
		t.Fatalf("debug items = %#v", items)
	}
}

func TestEventContentBlocksIncludesAttachments(t *testing.T) {
	event := channels.IncomingEvent{
		Text: "inspect",
		Attachments: []channels.Attachment{{
			Type: "image", Name: "capture.png", MimeType: "image/png", Data: []byte("png"), Size: 3,
		}},
	}
	blocks := eventContentBlocks(event)
	if len(blocks) != 2 || blocks[0].Text != "inspect" || blocks[1].Type != "image" {
		t.Fatalf("blocks = %#v", blocks)
	}
	if blocks[1].Data != base64.StdEncoding.EncodeToString([]byte("png")) {
		t.Fatalf("image data = %q", blocks[1].Data)
	}
}

func TestMissingSessionError(t *testing.T) {
	if !isMissingSessionError(fmt.Errorf("ACP: session not found")) {
		t.Fatal("expected missing session error")
	}
	if isMissingSessionError(fmt.Errorf("network unavailable")) {
		t.Fatal("unexpected missing session match")
	}
}

func TestDefaultEndpointAgentClientRejectsInprocessAlias(t *testing.T) {
	_, err := DefaultEndpointAgentClient(nil, config.AgentConfig{ID: "old", Type: "inprocess"})
	if err == nil {
		t.Fatal("expected inprocess agent type to be rejected")
	}
}

type recordingWriter struct {
	writes []string
	done   []bool
}

func (w *recordingWriter) Write(text string, done bool) error {
	w.writes = append(w.writes, text)
	w.done = append(w.done, done)
	return nil
}

func TestPolicyWriterFinalOnlyCoalescesChunks(t *testing.T) {
	inner := &recordingWriter{}
	writer := newPolicyWriter(inner, config.DeliveryConfig{FinalOnly: true})
	_ = writer.Write("hello ", false)
	_ = writer.Write("world", false)
	_ = writer.Write("", true)
	if len(inner.writes) != 1 || inner.writes[0] != "hello world" || !inner.done[0] {
		t.Fatalf("writes = %#v done = %#v", inner.writes, inner.done)
	}
}
