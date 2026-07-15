package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lsongdev/miya-agents/acp"
	miyaagent "github.com/lsongdev/miya-agents/agent"
	"github.com/lsongdev/miya-channels/channels"
	"github.com/lsongdev/miya-channels/config"
)

type AgentClientFactory func(*config.Config) (*acp.Client, string, error)

type Options struct {
	Config    *config.Config
	NewClient AgentClientFactory
	OnEvent   func(channels.ChannelEvent)
}

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

func Run(ctx context.Context, opts Options) error {
	cfg := opts.Config
	if cfg == nil {
		var err error
		cfg, err = config.LoadConfig()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
	}

	newClient := opts.NewClient
	if newClient == nil {
		newClient = DefaultAgentClient
	}
	client, agentLabel, err := newClient(cfg)
	if err != nil {
		return err
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
		return fmt.Errorf("ACP initialize: %w", err)
	}
	if err := client.SendNotification("notifications/initialized", struct{}{}); err != nil {
		return fmt.Errorf("ACP initialized notification: %w", err)
	}
	if initResp.AgentInfo != nil {
		log.Printf("Connected to ACP agent: %s v%s", initResp.AgentInfo.Name, initResp.AgentInfo.Version)
	}

	cm := channels.NewChannelManagerWithOptions(cfg, channels.ChannelOptions{
		Emit: opts.OnEvent,
	})
	if len(cm.ListChannels()) == 0 {
		return fmt.Errorf("no channels configured")
	}

	if err := cm.Start(ctx); err != nil {
		return fmt.Errorf("start channels: %w", err)
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

	log.Printf("Listening for messages (agent: %s)...", agentLabel)

	for {
		select {
		case <-ctx.Done():
			log.Println("Shutting down channels...")
			for key, s := range worker.sessions {
				_, _ = client.CloseSession(&acp.CloseSessionRequest{SessionID: s.sessionID})
				log.Printf("Closed session %s: %s", key, s.sessionID)
			}
			time.Sleep(500 * time.Millisecond)
			return nil

		case msg := <-cm.Incoming:
			worker.requests <- &promptRequest{msg: msg}
		}
	}
}

func DefaultAgentClient(cfg *config.Config) (*acp.Client, string, error) {
	agentConfig, err := config.DefaultAgent(cfg)
	if err != nil {
		return nil, "", err
	}
	if isBuiltinAgent(*agentConfig) {
		return acp.DialInProcess(miyaagent.NewAgentManager(cfg)), agentConfig.ID, nil
	}
	client, err := acp.DialStdio(agentConfig.Command, agentConfig.Args...)
	if err != nil {
		return nil, "", fmt.Errorf("start ACP client: %w", err)
	}
	return client, agentConfig.ID, nil
}

func isBuiltinAgent(agent config.AgentConfig) bool {
	if agent.Type == "builtin" || agent.Type == "inprocess" {
		return true
	}
	command := filepath.Base(agent.Command)
	if command != "miya" && command != "miya-agent" && command != "miya-agents" {
		return false
	}
	if len(agent.Args) == 0 {
		return true
	}
	return len(agent.Args) == 1 && agent.Args[0] == "acp"
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
		_ = writer.Write("Session stopped.", true)
		return

	case msg.Content == "/new":
		w.closeSession(key)
		session, err := w.createSession(key, msg.From, msg.Who)
		if err != nil {
			_ = writer.Write(fmt.Sprintf("Failed to create session: %v", err), true)
			return
		}
		_ = writer.Write(fmt.Sprintf("New session created: %s", session.sessionID), true)
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
		_, _ = w.client.CloseSession(&acp.CloseSessionRequest{SessionID: s.sessionID})
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
