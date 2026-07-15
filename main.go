package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/lsongdev/miya-agents/acp"
	"github.com/lsongdev/miya-channels/channels"
	"github.com/lsongdev/miya-channels/config"
)

type acpSession struct {
	sessionID acp.SessionID
}

type acpWorker struct {
	client   *acp.Client
	cm       *channels.ChannelManager
	sessions map[string]*acpSession
	writers  map[acp.SessionID]channels.Writer
	mu       sync.Mutex
	requests chan *promptRequest
}

type promptRequest struct {
	msg channels.IncomingMessage
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
		cm:       cm,
		sessions: make(map[string]*acpSession),
		writers:  make(map[acp.SessionID]channels.Writer),
		requests: make(chan *promptRequest, 32),
	}
	client.OnNotification(worker.handleNotification)

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
			req := &promptRequest{msg: msg}
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
	cm := w.cm

	log.Printf("[DEBUG] handleMessage: from=%s who=%s content=%q", msg.From, msg.Who, msg.Content)

	writer, err := cm.CreateReplyWriter(msg.From, msg.ReplyTo)
	if err != nil {
		log.Printf("Error creating writer for %s/%s: %v", msg.From, msg.ReplyTo, err)
		return
	}

	key := msg.From + ":" + msg.Who

	switch {
	case msg.Content == "/stop":
		w.closeSession(key)
		writer.Write("Session stopped.", true)
		return

	case msg.Content == "/new":
		w.closeSession(key)
		session, err := w.createSession(key, msg.From, msg.Who)
		if err != nil {
			writer.Write(fmt.Sprintf("Failed to create session: %v", err), true)
			return
		}
		writer.Write(fmt.Sprintf("New session created: %s", session.sessionID), true)
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

	w.setWriter(session.sessionID, writer)
	defer w.clearWriter(session.sessionID)

	log.Printf("[DEBUG] Sending Prompt (session=%s)...", session.sessionID)
	_, err = w.client.Prompt(&acp.PromptRequest{
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
	if err := writer.Write("", true); err != nil {
		log.Printf("[ERROR] Failed to finalize write: %v", err)
	}
}

func (w *acpWorker) handleNotification(method string, params json.RawMessage) {
	if method != "session/update" {
		log.Println("[DEBUG] Ignoring notification", method, "params:", string(params))
		return
	}
	var raw struct {
		SessionID acp.SessionID   `json:"sessionId"`
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
	if content.Type != "text" || content.Text == "" {
		return
	}
	writer := w.writerFor(raw.SessionID)
	if writer == nil {
		return
	}
	if err := writer.Write(content.Text, false); err != nil {
		log.Printf("[ERROR] Failed to write chunk: %v", err)
	}
}

func (w *acpWorker) setWriter(sessionID acp.SessionID, writer channels.Writer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writers[sessionID] = writer
}

func (w *acpWorker) clearWriter(sessionID acp.SessionID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.writers, sessionID)
}

func (w *acpWorker) writerFor(sessionID acp.SessionID) channels.Writer {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writers[sessionID]
}

func (w *acpWorker) closeSession(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s, ok := w.sessions[key]; ok {
		w.client.CloseSession(&acp.CloseSessionRequest{SessionID: s.sessionID})
		log.Printf("[DEBUG] Closed session %s for %s", s.sessionID, key)
		delete(w.sessions, key)
	} else {
		log.Printf("[DEBUG] No session to close for %s", key)
	}
}

func (w *acpWorker) createSession(key, channel, who string) (*acpSession, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
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
