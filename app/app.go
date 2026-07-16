package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"os"
	"path/filepath"
	"strings"
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
	cwd       string
	loaded    bool
}

type sessionRecord struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type sessionStore struct {
	path string
}

type acpWorker struct {
	client      *acp.Client
	cm          *channels.ChannelManager
	sessions    map[string]*acpSession
	store       *sessionStore
	loadSession bool
	writers     map[acp.SessionID]channels.Writer
	routes      map[acp.SessionID]replyRoute
	mu          sync.Mutex
	requests    chan *promptRequest
}

type replyRoute struct {
	channel string
	target  string
}

type promptRequest struct {
	msg channels.IncomingMessage
}

func newSessionStore() *sessionStore {
	return &sessionStore{path: filepath.Join(config.ConfigPath, "channels", "sessions.json")}
}

func (s *sessionStore) Load() (map[string]*acpSession, error) {
	out := make(map[string]*acpSession)
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	var records map[string]sessionRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	for key, record := range records {
		if key == "" || record.SessionID == "" {
			continue
		}
		out[key] = &acpSession{
			sessionID: acp.SessionID(record.SessionID),
			cwd:       record.Cwd,
			loaded:    false,
		}
	}
	return out, nil
}

func (s *sessionStore) Save(sessions map[string]*acpSession) error {
	records := make(map[string]sessionRecord, len(sessions))
	now := time.Now().Format(time.RFC3339)
	for key, sess := range sessions {
		if key == "" || sess == nil || sess.sessionID == "" {
			continue
		}
		records[key] = sessionRecord{
			SessionID: string(sess.sessionID),
			Cwd:       sess.cwd,
			UpdatedAt: now,
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(s.path, data, 0600)
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

	lock, err := acquireProcessLock(config.ChannelsLockFile)
	if err != nil {
		return err
	}
	defer lock.Release()

	cm := channels.NewChannelManagerWithOptions(cfg, channels.ChannelOptions{
		Emit: opts.OnEvent,
	})
	if len(cm.ListChannels()) == 0 {
		return fmt.Errorf("no channels configured")
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

	if err := cm.Start(ctx); err != nil {
		return fmt.Errorf("start channels: %w", err)
	}

	store := newSessionStore()
	sessions, err := store.Load()
	if err != nil {
		log.Printf("[channels] failed to load session mappings: %v", err)
		sessions = make(map[string]*acpSession)
	} else if len(sessions) > 0 {
		log.Printf("[channels] loaded %d session mappings", len(sessions))
	}

	worker := &acpWorker{
		client:      client,
		cm:          cm,
		sessions:    sessions,
		store:       store,
		loadSession: initResp.AgentCapabilities.LoadSession,
		writers:     make(map[acp.SessionID]channels.Writer),
		routes:      make(map[acp.SessionID]replyRoute),
		requests:    make(chan *promptRequest, 32),
	}
	client.OnNotification(worker.handleNotification)

	go worker.run(ctx)

	log.Printf("Listening for messages (agent: %s)...", agentLabel)

	for {
		select {
		case <-ctx.Done():
			log.Println("Shutting down channels...")
			worker.persistSessions()
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

	w.setReply(session.sessionID, writer, replyRoute{channel: msg.From, target: msg.ReplyTo})
	defer w.clearReply(session.sessionID)

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
	var content acp.ContentBlock
	if err := json.Unmarshal(update.Content, &content); err != nil {
		return
	}
	switch content.Type {
	case "text":
		w.handleTextContent(raw.SessionID, content.Text)
	case "image", "audio", "resource", "resource_link":
		w.handleFileContent(raw.SessionID, content)
	}
}

func (w *acpWorker) handleTextContent(sessionID acp.SessionID, text string) {
	if text == "" {
		return
	}
	writer := w.writerFor(sessionID)
	if writer == nil {
		return
	}
	if err := writer.Write(text, false); err != nil {
		log.Printf("[ERROR] Failed to write chunk: %v", err)
	}
}

func (w *acpWorker) handleFileContent(sessionID acp.SessionID, content acp.ContentBlock) {
	delivery, ok, err := fileDelivery(content)
	if err != nil {
		log.Printf("[ERROR] Failed to prepare ACP file content: %v", err)
		return
	}
	if !ok {
		return
	}
	writer := w.writerFor(sessionID)
	if writer == nil {
		return
	}
	route, ok := w.routeFor(sessionID)
	if !ok {
		return
	}
	delivery.Channel = route.channel
	delivery.Target = route.target
	payload, err := json.Marshal(delivery)
	if err != nil {
		log.Printf("[ERROR] Failed to encode file payload: %v", err)
		return
	}
	if err := w.cm.SendFile(delivery.Channel, delivery.Target, delivery.Type, string(payload)); err != nil {
		log.Printf("[ERROR] Failed to send ACP file content: %v", err)
		_ = writer.Write(fmt.Sprintf("\n[Attachment: %s]\n", delivery.URL), false)
	}
}

type filePayload struct {
	Type    string `json:"type"`
	URL     string `json:"url"`
	Caption string `json:"caption,omitempty"`
	Name    string `json:"name,omitempty"`
	Mime    string `json:"mime,omitempty"`
	Channel string `json:"-"`
	Target  string `json:"-"`
}

func fileDelivery(content acp.ContentBlock) (filePayload, bool, error) {
	url := ""
	if content.URI != nil {
		url = *content.URI
	}
	if url == "" && content.Data != "" {
		path, err := writeInlineAttachment(content)
		if err != nil {
			return filePayload{}, false, err
		}
		url = "file://" + path
	}
	if url == "" {
		return filePayload{}, false, nil
	}

	mimeType := content.MimeType
	if mimeType == "" {
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(content.Name)))
	}
	typ := fileType(content.Type, mimeType)
	caption := ""
	if content.Title != nil {
		caption = *content.Title
	}
	if caption == "" && content.Description != nil {
		caption = *content.Description
	}
	if caption == "" {
		caption = content.Name
	}
	return filePayload{
		Type:    typ,
		URL:     url,
		Caption: caption,
		Name:    content.Name,
		Mime:    mimeType,
	}, true, nil
}

func writeInlineAttachment(content acp.ContentBlock) (string, error) {
	data, err := base64.StdEncoding.DecodeString(content.Data)
	if err != nil {
		return "", fmt.Errorf("decode inline attachment: %w", err)
	}
	name := content.Name
	if name == "" {
		name = "attachment" + extensionForMime(content.MimeType)
	}
	path := filepath.Join(os.TempDir(), "miya-channels", fmt.Sprintf("%d-%s", time.Now().UnixNano(), filepath.Base(name)))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", err
	}
	return path, nil
}

func extensionForMime(mimeType string) string {
	if mimeType == "" {
		return ""
	}
	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ""
}

func fileType(contentType, mimeType string) string {
	switch {
	case contentType == "image" || strings.HasPrefix(mimeType, "image/"):
		return "image"
	case strings.HasPrefix(mimeType, "video/"):
		return "video"
	case contentType == "audio" || strings.HasPrefix(mimeType, "audio/"):
		return "audio"
	default:
		return "file"
	}
}

func (w *acpWorker) setReply(sessionID acp.SessionID, writer channels.Writer, route replyRoute) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writers[sessionID] = writer
	w.routes[sessionID] = route
}

func (w *acpWorker) clearReply(sessionID acp.SessionID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.writers, sessionID)
	delete(w.routes, sessionID)
}

func (w *acpWorker) writerFor(sessionID acp.SessionID) channels.Writer {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writers[sessionID]
}

func (w *acpWorker) routeFor(sessionID acp.SessionID) (replyRoute, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	route, ok := w.routes[sessionID]
	return route, ok
}

func (w *acpWorker) persistSessionsLocked() {
	if w.store == nil {
		return
	}
	if err := w.store.Save(w.sessions); err != nil {
		log.Printf("[channels] failed to persist session mappings: %v", err)
	}
}

func (w *acpWorker) persistSessions() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.persistSessionsLocked()
}

func (w *acpWorker) closeSession(key string) {
	w.mu.Lock()
	s, ok := w.sessions[key]
	if ok {
		delete(w.sessions, key)
		w.persistSessionsLocked()
	}
	w.mu.Unlock()

	if !ok {
		log.Printf("[DEBUG] No session to close for %s", key)
		return
	}
	if _, err := w.client.CloseSession(&acp.CloseSessionRequest{SessionID: s.sessionID}); err != nil {
		log.Printf("[WARN] Failed to close session %s for %s: %v", s.sessionID, key, err)
	}
	log.Printf("[DEBUG] Closed session %s for %s", s.sessionID, key)
}

func (w *acpWorker) createSession(key, channel, who string) (*acpSession, error) {
	cwd := defaultSessionCwd()
	log.Printf("[DEBUG] Creating new session for %s", key)
	sessResp, err := w.client.NewSession(&acp.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		return nil, err
	}
	s := &acpSession{sessionID: sessResp.SessionID, cwd: cwd, loaded: true}

	w.mu.Lock()
	w.sessions[key] = s
	w.persistSessionsLocked()
	w.mu.Unlock()

	log.Printf("[DEBUG] New session %s for %s", s.sessionID, key)
	return s, nil
}

func (w *acpWorker) getOrCreateSession(channel, who string) (*acpSession, error) {
	key := channel + ":" + who
	w.mu.Lock()
	s, ok := w.sessions[key]
	w.mu.Unlock()

	if !ok {
		return w.createSession(key, channel, who)
	}

	if !s.loaded {
		if !w.loadSession {
			log.Printf("[DEBUG] Dropping persisted session %s for %s because agent does not support loadSession", s.sessionID, key)
			w.mu.Lock()
			delete(w.sessions, key)
			w.persistSessionsLocked()
			w.mu.Unlock()
			return w.createSession(key, channel, who)
		}
		cwd := s.cwd
		if cwd == "" {
			cwd = defaultSessionCwd()
		}
		log.Printf("[DEBUG] Loading persisted session %s for %s", s.sessionID, key)
		if _, err := w.client.LoadSession(&acp.LoadSessionRequest{
			SessionID:  s.sessionID,
			Cwd:        cwd,
			McpServers: []acp.McpServer{},
		}); err != nil {
			log.Printf("[DEBUG] Failed to load persisted session %s for %s: %v", s.sessionID, key, err)
			w.mu.Lock()
			delete(w.sessions, key)
			w.persistSessionsLocked()
			w.mu.Unlock()
			return w.createSession(key, channel, who)
		}
		w.mu.Lock()
		if current, ok := w.sessions[key]; ok && current.sessionID == s.sessionID {
			current.cwd = cwd
			current.loaded = true
			s = current
			w.persistSessionsLocked()
		}
		w.mu.Unlock()
	}

	log.Printf("[DEBUG] Reusing session %s for %s", s.sessionID, key)
	return s, nil
}

func defaultSessionCwd() string {
	workspace := filepath.Join(config.ConfigPath, "workspace")
	if err := os.MkdirAll(workspace, 0755); err == nil {
		return workspace
	}
	cwd, err := os.Getwd()
	if err == nil && cwd != "" {
		return cwd
	}
	return "."
}
