package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lsongdev/miya-agents/acp"
	"github.com/lsongdev/miya/channels"
	"github.com/lsongdev/miya/config"
)

type promptResult struct {
	text string
	err  error
}

type acpSession struct {
	sessionID acp.SessionID
}

type acpWorker struct {
	client   *acp.Client
	sessions map[string]*acpSession
	mu       sync.Mutex
	requests chan *promptRequest
}

type promptRequest struct {
	msg    channels.IncomingMessage
	cm     *channels.ChannelManager
	result chan promptResult
}

func main() {
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	client, err := acp.DialStdio(cfg.ACP.Command, cfg.ACP.Args...)
	if err != nil {
		log.Fatalf("Failed to start ACP client: %v", err)
	}
	defer client.Close()

	initResp, err := client.Initialize(&acp.InitializeRequest{
		ProtocolVersion:    1,
		ClientCapabilities: acp.DefaultClientCapabilities(),
		ClientInfo: &acp.Implementation{
			Name:    "miya-channels",
			Version: "1.0.0",
		},
	})
	if err != nil {
		log.Fatalf("ACP initialize failed: %v", err)
	}
	log.Printf("Connected to ACP agent: %s v%s", initResp.AgentInfo.Name, initResp.AgentInfo.Version)

	cm := channels.NewChannelManager(cfg)
	if len(cm.ListChannels()) == 0 {
		log.Fatal("No channels configured")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cm.Start(ctx); err != nil {
		log.Fatalf("Failed to start channels: %v", err)
	}

	worker := &acpWorker{
		client:   client,
		sessions: make(map[string]*acpSession),
		requests: make(chan *promptRequest, 32),
	}

	go worker.run(ctx)

	log.Printf("Listening for messages (command: %s)...", cfg.ACP.Command)

	for {
		select {
		case <-ctx.Done():
			log.Println("Shutting down...")
			for key, s := range worker.sessions {
				client.CloseSession(&acp.CloseSessionRequest{SessionID: s.sessionID})
				log.Printf("Closed session %s: %s", key, s.sessionID)
			}
			time.Sleep(500 * time.Millisecond)
			log.Println("Bye!")
			return

		case msg := <-cm.Incoming:
			req := &promptRequest{
				msg:    msg,
				cm:     cm,
				result: make(chan promptResult, 1),
			}
			worker.requests <- req
		}
	}
}

func (w *acpWorker) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-w.requests:
			w.handle(req)
		}
	}
}

func (w *acpWorker) handle(req *promptRequest) {
	msg := req.msg
	cm := req.cm

	log.Printf("[DEBUG] handleMessage: from=%s who=%s content=%q", msg.From, msg.Who, msg.Content)

	writer, err := cm.CreateReplyWriter(msg.From, msg.ReplyTo)
	if err != nil {
		log.Printf("Error creating writer for %s/%s: %v", msg.From, msg.ReplyTo, err)
		return
	}

	session, err := w.getOrCreateSession(msg.From, msg.Who)
	if err != nil {
		log.Printf("[DEBUG] getOrCreateSession error: %v", err)
		if err := writer.Write(fmt.Sprintf("Session error: %v", err), true); err != nil {
			log.Printf("[ERROR] Failed to write session error to channel: %v", err)
		}
		return
	}

	var contentBuilder strings.Builder
	w.client.OnNotification(func(method string, params json.RawMessage) {
		if method != "session/update" {
			return
		}
		var raw struct {
			SessionID string          `json:"sessionId"`
			Update    json.RawMessage `json:"update"`
		}
		if err := json.Unmarshal(params, &raw); err != nil {
			return
		}
		var update struct {
			SessionUpdate string          `json:"sessionUpdate"`
			Content       json.RawMessage `json:"content,omitempty"`
		}
		if err := json.Unmarshal(raw.Update, &update); err != nil {
			return
		}
		if update.SessionUpdate != "agent_message_chunk" {
			return
		}
		var content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(update.Content, &content); err != nil {
			return
		}
		if content.Type == "text" && content.Text != "" {
			contentBuilder.WriteString(content.Text)
		}
	})

	log.Printf("[DEBUG] Sending Prompt (session=%s)...", session.sessionID)
	promptResp, err := w.client.Prompt(&acp.PromptRequest{
		SessionID: session.sessionID,
		Prompt: []acp.ContentBlock{
			{Type: "text", Text: msg.Content},
		},
	})
	if err != nil {
		log.Printf("[DEBUG] Prompt error: %v", err)
		if err := writer.Write(fmt.Sprintf("Prompt error: %v", err), true); err != nil {
			log.Printf("[ERROR] Failed to write prompt error to channel: %v", err)
		}
		return
	}
	log.Printf("[DEBUG] Prompt response: stopReason=%s, text length=%d", promptResp.StopReason, contentBuilder.Len())

	text := contentBuilder.String()
	if text != "" {
		preview := text
		if len(preview) > 200 {
			preview = preview[:200]
		}
		log.Printf("[DEBUG] Writing response to channel: text length=%d, preview=%q", len(text), preview)
		if err := writer.Write(text, true); err != nil {
			log.Printf("[ERROR] Failed to write response to channel: %v", err)
		}
	} else {
		log.Printf("[DEBUG] No content received from ACP")
		if err := writer.Write("", true); err != nil {
			log.Printf("[ERROR] Failed to write empty response to channel: %v", err)
		}
	}
}

func (w *acpWorker) getOrCreateSession(channel, who string) (*acpSession, error) {
	key := channel + ":" + who
	w.mu.Lock()
	defer w.mu.Unlock()

	if s, ok := w.sessions[key]; ok {
		log.Printf("[DEBUG] Reusing session %s for %s", s.sessionID, key)
		return s, nil
	}

	cwd, _ := os.Getwd()
	log.Printf("[DEBUG] Creating new session for %s", key)
	sessResp, err := w.client.NewSession(&acp.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		return nil, err
	}

	s := &acpSession{sessionID: sessResp.SessionID}
	w.sessions[key] = s
	log.Printf("[DEBUG] New session %s for %s", s.sessionID, key)
	return s, nil
}
