package app

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lsongdev/miya-agents/acp"
	"github.com/lsongdev/miya-channels/channels"
	"github.com/lsongdev/miya-channels/config"
)

type sessionRouteKey struct {
	agentID   string
	sessionID acp.SessionID
}

type activeDelivery struct {
	writer     channels.Writer
	channelID  string
	target     string
	visibility config.Visibility
}

type activePrompt struct {
	agentID   string
	sessionID acp.SessionID
}

type Gateway struct {
	channels *channels.ChannelManager
	agents   *agentRegistry
	store    *routeStore

	mu         sync.Mutex
	bindings   map[string]*routeBinding
	routes     map[sessionRouteKey]activeDelivery
	active     map[string]activePrompt
	sourceLock map[string]*sync.Mutex
}

func newGateway(cm *channels.ChannelManager, agents *agentRegistry, store *routeStore, bindings map[string]*routeBinding) *Gateway {
	return &Gateway{
		channels:   cm,
		agents:     agents,
		store:      store,
		bindings:   bindings,
		routes:     make(map[sessionRouteKey]activeDelivery),
		active:     make(map[string]activePrompt),
		sourceLock: make(map[string]*sync.Mutex),
	}
}

func (g *Gateway) Submit(event channels.IncomingEvent) {
	if commandName(event.Text) == "stop" {
		go g.stop(event)
		return
	}
	go func() {
		lock := g.lockForSource(event.SourceKey())
		lock.Lock()
		defer lock.Unlock()
		g.handle(event)
	}()
}

func (g *Gateway) lockForSource(source string) *sync.Mutex {
	g.mu.Lock()
	defer g.mu.Unlock()
	lock := g.sourceLock[source]
	if lock == nil {
		lock = &sync.Mutex{}
		g.sourceLock[source] = lock
	}
	return lock
}

func (g *Gateway) handle(event channels.IncomingEvent) {
	rawWriter, err := g.channels.CreateReplyWriter(event.ChannelID, event.ReplyTo)
	if err != nil {
		log.Printf("[channels] create writer for %s: %v", event.SourceKey(), err)
		return
	}
	instance, _ := g.channels.Instance(event.ChannelID)
	writer := newPolicyWriter(rawWriter, instance.Delivery)
	binding, err := g.resolveBinding(event)
	if err != nil {
		_ = writer.Write("Route error: "+err.Error(), true)
		return
	}
	if handled := g.handleCommand(event, binding, writer); handled {
		return
	}
	if len(eventContentBlocks(event)) == 0 {
		_ = writer.Write("Message has no supported content.", true)
		return
	}
	if err := g.prompt(event, binding, writer); err != nil {
		log.Printf("[channels] prompt failed for %s: %v", event.SourceKey(), err)
		_ = writer.Write("Prompt error: "+err.Error(), true)
	}
}

func (g *Gateway) resolveBinding(event channels.IncomingEvent) (*routeBinding, error) {
	source := event.SourceKey()
	g.mu.Lock()
	defer g.mu.Unlock()
	if binding := g.bindings[source]; binding != nil {
		if binding.AgentID == "" {
			binding.AgentID = g.agents.DefaultID()
			if session := binding.Sessions[""]; session != nil {
				delete(binding.Sessions, "")
				binding.Sessions[binding.AgentID] = session
			}
		}
		return binding, nil
	}
	instance, ok := g.channels.Instance(event.ChannelID)
	if !ok {
		return nil, fmt.Errorf("channel instance %q not found", event.ChannelID)
	}
	agentID := instance.Agent
	if agentID == "" {
		agentID = g.agents.DefaultID()
	}
	if _, ok := g.agents.Get(agentID); !ok {
		return nil, fmt.Errorf("agent %q not found", agentID)
	}
	now := time.Now()
	binding := &routeBinding{
		ChannelID:      event.ChannelID,
		ConversationID: event.ConversationID,
		SenderID:       event.SenderID,
		AgentID:        agentID,
		Visibility:     instance.EffectiveVisibility(),
		Sessions:       make(map[string]*sessionState),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	g.bindings[source] = binding
	g.persistLocked()
	return binding, nil
}

func (g *Gateway) prompt(event channels.IncomingEvent, binding *routeBinding, writer channels.Writer) error {
	runtime, ok := g.agents.Get(binding.AgentID)
	if !ok {
		return fmt.Errorf("agent %q not found", binding.AgentID)
	}
	session, err := g.getOrCreateSession(event.SourceKey(), binding, runtime)
	if err != nil {
		return err
	}

	if err := g.runPrompt(event, binding, runtime, session, writer); err != nil {
		if !isMissingSessionError(err) {
			return err
		}
		g.deleteSession(event.SourceKey(), binding.AgentID)
		session, createErr := g.createSession(event.SourceKey(), binding, runtime)
		if createErr != nil {
			return fmt.Errorf("replace invalid session: %w", createErr)
		}
		return g.runPrompt(event, binding, runtime, session, writer)
	}
	return nil
}

func (g *Gateway) runPrompt(event channels.IncomingEvent, binding *routeBinding, runtime *agentRuntime, session *sessionState, writer channels.Writer) error {
	source := event.SourceKey()
	key := sessionRouteKey{agentID: runtime.id, sessionID: session.SessionID}
	g.mu.Lock()
	g.routes[key] = activeDelivery{writer: writer, channelID: event.ChannelID, target: event.ReplyTo, visibility: binding.Visibility}
	g.active[source] = activePrompt{agentID: runtime.id, sessionID: session.SessionID}
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.routes, key)
		delete(g.active, source)
		g.mu.Unlock()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := runtime.client.Prompt(&acp.PromptRequest{SessionID: session.SessionID, Prompt: eventContentBlocks(event)})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return err
		}
		return writer.Write("", true)
	case <-time.After(promptTimeout):
		_ = runtime.client.CancelSession(session.SessionID)
		return fmt.Errorf("prompt timed out after %s", promptTimeout)
	}
}

func (g *Gateway) stop(event channels.IncomingEvent) {
	rawWriter, err := g.channels.CreateReplyWriter(event.ChannelID, event.ReplyTo)
	if err != nil {
		return
	}
	instance, _ := g.channels.Instance(event.ChannelID)
	writer := newPolicyWriter(rawWriter, instance.Delivery)
	g.mu.Lock()
	active, ok := g.active[event.SourceKey()]
	g.mu.Unlock()
	if !ok {
		_ = writer.Write("No active prompt.", true)
		return
	}
	runtime, ok := g.agents.Get(active.agentID)
	if !ok {
		_ = writer.Write("Agent is no longer available.", true)
		return
	}
	if err := runtime.client.CancelSession(active.sessionID); err != nil {
		_ = writer.Write("Failed to stop prompt: "+err.Error(), true)
		return
	}
	_ = writer.Write("Prompt stopped.", true)
}

func (g *Gateway) DeliverAgentEvent(agentID string, event AgentEvent) {
	key := sessionRouteKey{agentID: agentID, sessionID: event.SessionID}
	g.mu.Lock()
	delivery, ok := g.routes[key]
	g.mu.Unlock()
	if !ok {
		return
	}
	for _, item := range renderAgentEvent(event, delivery.visibility) {
		switch item.Kind {
		case "text", "status":
			if err := delivery.writer.Write(item.Text, item.Final); err != nil {
				log.Printf("[channels] deliver %s event: %v", item.Kind, err)
			}
		case "file":
			if item.File == nil {
				continue
			}
			payload, err := attachmentPayload(*item.File)
			if err != nil {
				log.Printf("[channels] prepare attachment: %v", err)
				continue
			}
			encoded, err := encodeFilePayload(payload)
			if err == nil {
				err = g.channels.SendFile(delivery.channelID, delivery.target, payload.Type, encoded)
			}
			if err != nil {
				log.Printf("[channels] deliver attachment: %v", err)
				_ = delivery.writer.Write(fmt.Sprintf("\n[Attachment: %s]\n", payload.URL), false)
			}
		}
	}
}

func (g *Gateway) getOrCreateSession(source string, binding *routeBinding, runtime *agentRuntime) (*sessionState, error) {
	g.mu.Lock()
	session := binding.Sessions[runtime.id]
	g.mu.Unlock()
	if session == nil {
		return g.createSession(source, binding, runtime)
	}
	if session.Loaded {
		return session, nil
	}
	if !runtime.loadSession {
		g.deleteSession(source, runtime.id)
		return g.createSession(source, binding, runtime)
	}
	cwd := session.Cwd
	if cwd == "" {
		cwd = defaultSessionCwd()
	}
	resp, err := runtime.client.LoadSession(&acp.LoadSessionRequest{SessionID: session.SessionID, Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil {
		g.deleteSession(source, runtime.id)
		return g.createSession(source, binding, runtime)
	}
	g.mu.Lock()
	session.Cwd = cwd
	session.Loaded = true
	session.Modes = resp.Modes
	binding.UpdatedAt = time.Now()
	g.persistLocked()
	g.mu.Unlock()
	return session, nil
}

func (g *Gateway) createSession(source string, binding *routeBinding, runtime *agentRuntime) (*sessionState, error) {
	cwd := defaultSessionCwd()
	resp, err := runtime.client.NewSession(newSessionRequest(runtime, cwd))
	if err != nil {
		return nil, err
	}
	session := &sessionState{SessionID: resp.SessionID, Cwd: cwd, Loaded: true, Modes: resp.Modes}
	g.mu.Lock()
	if binding.Sessions == nil {
		binding.Sessions = make(map[string]*sessionState)
	}
	binding.Sessions[runtime.id] = session
	binding.UpdatedAt = time.Now()
	g.bindings[source] = binding
	g.persistLocked()
	g.mu.Unlock()
	return session, nil
}

func newSessionRequest(runtime *agentRuntime, cwd string) *acp.NewSessionRequest {
	req := &acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}}
	if runtime != nil && runtime.profile != "" {
		req.Meta = acp.Meta{acp.MiyaProfileMetaKey: runtime.profile}
	}
	return req
}

func (g *Gateway) deleteSession(source, agentID string) *sessionState {
	g.mu.Lock()
	defer g.mu.Unlock()
	binding := g.bindings[source]
	if binding == nil {
		return nil
	}
	session := binding.Sessions[agentID]
	delete(binding.Sessions, agentID)
	binding.UpdatedAt = time.Now()
	g.persistLocked()
	return session
}

func (g *Gateway) newSession(source string, binding *routeBinding) (*sessionState, error) {
	runtime, ok := g.agents.Get(binding.AgentID)
	if !ok {
		return nil, fmt.Errorf("agent %q not found", binding.AgentID)
	}
	old := g.deleteSession(source, binding.AgentID)
	if old != nil {
		if _, err := runtime.client.CloseSession(&acp.CloseSessionRequest{SessionID: old.SessionID}); err != nil {
			log.Printf("[channels] close old session %s: %v", old.SessionID, err)
		}
	}
	return g.createSession(source, binding, runtime)
}

func (g *Gateway) handleCommand(event channels.IncomingEvent, binding *routeBinding, writer channels.Writer) bool {
	name, args := parseCommand(event.Text)
	switch name {
	case "":
		return false
	case "new":
		session, err := g.newSession(event.SourceKey(), binding)
		if err != nil {
			_ = writer.Write("Failed to create session: "+err.Error(), true)
		} else {
			_ = writer.Write("New session created: "+string(session.SessionID), true)
		}
		return true
	case "reset":
		_ = writer.Write("Reset is not supported yet. Use /new to start a new session.", true)
		return true
	case "help":
		_ = writer.Write("Commands: /new, /reset, /stop, /agent, /mode, /detail, /help", true)
		return true
	case "agent":
		g.agentCommand(event, binding, writer, args)
		return true
	case "detail":
		g.detailCommand(binding, writer, args)
		return true
	case "mode":
		g.modeCommand(event, binding, writer, args)
		return true
	default:
		return false
	}
}

func (g *Gateway) modeCommand(event channels.IncomingEvent, binding *routeBinding, writer channels.Writer, args []string) {
	runtime, ok := g.agents.Get(binding.AgentID)
	if !ok {
		_ = writer.Write("Agent is not available: "+binding.AgentID, true)
		return
	}
	session, err := g.getOrCreateSession(event.SourceKey(), binding, runtime)
	if err != nil {
		_ = writer.Write("Session error: "+err.Error(), true)
		return
	}
	if len(args) == 0 {
		if session.Modes == nil {
			_ = writer.Write("This agent does not expose session modes.", true)
			return
		}
		names := make([]string, 0, len(session.Modes.AvailableModes))
		for _, mode := range session.Modes.AvailableModes {
			names = append(names, fmt.Sprintf("%s (%s)", mode.Name, mode.ID))
		}
		_ = writer.Write(fmt.Sprintf("Current mode: %s\nAvailable modes: %s", session.Modes.CurrentModeID, strings.Join(names, ", ")), true)
		return
	}
	modeID := acp.SessionModeID(args[0])
	if session.Modes != nil && !hasMode(session.Modes.AvailableModes, modeID) {
		_ = writer.Write("Mode is not available: "+args[0], true)
		return
	}
	if _, err := runtime.client.SetSessionMode(&acp.SetSessionModeRequest{SessionID: session.SessionID, ModeID: modeID}); err != nil {
		_ = writer.Write("Failed to switch mode: "+err.Error(), true)
		return
	}
	if session.Modes != nil {
		session.Modes.CurrentModeID = modeID
	}
	_ = writer.Write("Switched mode: "+args[0], true)
}

func hasMode(modes []acp.SessionMode, id acp.SessionModeID) bool {
	for _, mode := range modes {
		if mode.ID == id {
			return true
		}
	}
	return false
}

func (g *Gateway) agentCommand(event channels.IncomingEvent, binding *routeBinding, writer channels.Writer, args []string) {
	if len(args) == 0 {
		_ = writer.Write(fmt.Sprintf("Current agent: %s\nAvailable agents: %s", binding.AgentID, strings.Join(g.agents.IDs(), ", ")), true)
		return
	}
	instance, _ := g.channels.Instance(event.ChannelID)
	if !instance.Commands.AgentSwitch {
		_ = writer.Write("Agent switching is disabled for this channel.", true)
		return
	}
	agentID := args[0]
	if _, ok := g.agents.Get(agentID); !ok || !agentAllowed(instance.Commands.AllowedAgents, agentID) {
		_ = writer.Write("Agent is not available: "+agentID, true)
		return
	}
	g.mu.Lock()
	binding.AgentID = agentID
	binding.UpdatedAt = time.Now()
	g.persistLocked()
	g.mu.Unlock()
	_ = writer.Write("Switched to agent: "+agentID, true)
}

func (g *Gateway) detailCommand(binding *routeBinding, writer channels.Writer, args []string) {
	if len(args) == 0 {
		_ = writer.Write("Current detail level: "+string(binding.Visibility), true)
		return
	}
	visibility := config.Visibility(args[0])
	if visibility != config.VisibilitySimple && visibility != config.VisibilityNormal && visibility != config.VisibilityVerbose && visibility != config.VisibilityDebug {
		_ = writer.Write("Detail level must be simple, normal, verbose, or debug.", true)
		return
	}
	g.mu.Lock()
	binding.Visibility = visibility
	binding.UpdatedAt = time.Now()
	g.persistLocked()
	g.mu.Unlock()
	_ = writer.Write("Detail level: "+string(visibility), true)
}

func (g *Gateway) Persist() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.persistLocked()
}

func (g *Gateway) persistLocked() {
	if g.store != nil {
		if err := g.store.Save(g.bindings); err != nil {
			log.Printf("[channels] persist route bindings: %v", err)
		}
	}
}

func parseCommand(text string) (string, []string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", nil
	}
	name := strings.TrimPrefix(fields[0], "/")
	if before, _, ok := strings.Cut(name, "@"); ok {
		name = before
	}
	return strings.ToLower(name), fields[1:]
}

func commandName(text string) string {
	name, _ := parseCommand(text)
	return name
}

func agentAllowed(allowed []string, id string) bool {
	if len(allowed) == 0 {
		return true
	}
	sorted := appendSorted(allowed)
	index := sort.SearchStrings(sorted, id)
	return index < len(sorted) && sorted[index] == id
}

func appendSorted(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func isMissingSessionError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "session") && (strings.Contains(message, "not found") || strings.Contains(message, "unknown") || strings.Contains(message, "does not exist"))
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
