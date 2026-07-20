package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lsongdev/miya-agents/acp"
	"github.com/lsongdev/miya-channels/channels"
	"github.com/lsongdev/miya-channels/config"
)

type AgentEventType string

const (
	AgentEventMessageDelta AgentEventType = "message_delta"
	AgentEventThoughtDelta AgentEventType = "thought_delta"
	AgentEventToolStart    AgentEventType = "tool_start"
	AgentEventToolUpdate   AgentEventType = "tool_update"
	AgentEventPlan         AgentEventType = "plan"
	AgentEventUsage        AgentEventType = "usage"
)

type AgentEvent struct {
	SessionID  acp.SessionID
	Type       AgentEventType
	Text       string
	Content    *acp.ContentBlock
	Tool       *acp.ToolCall
	ToolUpdate *acp.ToolCallUpdate
	Plan       *acp.Plan
	Usage      *acp.UsageUpdate
}

func normalizeAgentEvent(notification *acp.SessionNotification) (AgentEvent, bool) {
	update := notification.Update
	event := AgentEvent{SessionID: notification.SessionID}
	switch update.SessionUpdate {
	case "agent_message_chunk":
		event.Type = AgentEventMessageDelta
		event.Content = &update.Content
		event.Text = update.Content.Text
	case "agent_thought_chunk":
		event.Type = AgentEventThoughtDelta
		event.Content = &update.Content
		event.Text = update.Thought
		if event.Text == "" {
			event.Text = update.Content.Text
		}
	case "tool_call":
		event.Type = AgentEventToolStart
		event.Tool = update.ToolCall
	case "tool_call_update":
		event.Type = AgentEventToolUpdate
		event.ToolUpdate = update.ToolCallUpdate
	case "plan":
		event.Type = AgentEventPlan
		event.Plan = update.Plan
	case "usage_update":
		event.Type = AgentEventUsage
		event.Usage = update.Usage
	default:
		return AgentEvent{}, false
	}
	return event, true
}

func renderAgentEvent(event AgentEvent, visibility config.Visibility) []channels.DeliveryItem {
	switch event.Type {
	case AgentEventMessageDelta:
		if event.Content == nil {
			return nil
		}
		if event.Content.Type == "text" && event.Text != "" {
			return []channels.DeliveryItem{{Kind: "text", Text: event.Text, Format: "markdown"}}
		}
		if attachment, ok := contentAttachment(*event.Content); ok {
			return []channels.DeliveryItem{{Kind: "file", File: &attachment}}
		}
	case AgentEventThoughtDelta:
		if visibility == config.VisibilityDebug && event.Text != "" {
			return []channels.DeliveryItem{{Kind: "status", Text: "Thought: " + event.Text, Sensitive: true}}
		}
	case AgentEventToolStart:
		if visibilityRank(visibility) >= visibilityRank(config.VisibilityNormal) && event.Tool != nil {
			text := "Using tool"
			if event.Tool.Title != "" {
				text += ": " + event.Tool.Title
			}
			if command := toolCommand(event.Tool); command != "" {
				text += "\n" + command
			}
			if visibility == config.VisibilityDebug && len(event.Tool.RawInput) > 0 {
				text += "\n" + string(event.Tool.RawInput)
			}
			return []channels.DeliveryItem{{Kind: "status", Text: statusCodeBlock(text), Sensitive: visibility == config.VisibilityDebug}}
		}
	case AgentEventToolUpdate:
		if visibilityRank(visibility) >= visibilityRank(config.VisibilityVerbose) && event.ToolUpdate != nil {
			parts := []string{"Tool update"}
			if event.ToolUpdate.Title != nil && *event.ToolUpdate.Title != "" {
				parts[0] += ": " + *event.ToolUpdate.Title
			}
			if event.ToolUpdate.Status != nil {
				parts = append(parts, string(*event.ToolUpdate.Status))
			}
			if visibility == config.VisibilityDebug && len(event.ToolUpdate.RawOutput) > 0 {
				parts = append(parts, string(event.ToolUpdate.RawOutput))
			}
			return []channels.DeliveryItem{{Kind: "status", Text: statusCodeBlock(strings.Join(parts, " - ")), Sensitive: visibility == config.VisibilityDebug}}
		}
	case AgentEventPlan:
		if visibilityRank(visibility) >= visibilityRank(config.VisibilityVerbose) && event.Plan != nil {
			lines := make([]string, 0, len(event.Plan.Entries)+1)
			lines = append(lines, "Plan:")
			for _, entry := range event.Plan.Entries {
				lines = append(lines, fmt.Sprintf("- [%s] %s", entry.Status, entry.Content))
			}
			return []channels.DeliveryItem{{Kind: "status", Text: strings.Join(lines, "\n") + "\n"}}
		}
	case AgentEventUsage:
		if visibility == config.VisibilityDebug && event.Usage != nil {
			return []channels.DeliveryItem{{Kind: "status", Text: fmt.Sprintf("Usage: %d/%d\n", event.Usage.Used, event.Usage.Size)}}
		}
	}
	return nil
}

func toolCommand(tool *acp.ToolCall) string {
	if tool == nil {
		return ""
	}
	title := strings.ToLower(strings.TrimSpace(tool.Title))
	if title != "exec" && tool.Kind != acp.ToolKindExecute {
		return ""
	}
	var input struct {
		Command string `json:"command"`
		Cmd     string `json:"cmd"`
	}
	if err := json.Unmarshal(tool.RawInput, &input); err != nil {
		return ""
	}
	if command := strings.TrimSpace(input.Command); command != "" {
		return command
	}
	return strings.TrimSpace(input.Cmd)
}

func statusCodeBlock(text string) string {
	text = strings.TrimSpace(text)
	fence := "```"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	return "\n" + fence + "text\n" + text + "\n" + fence + "\n\n"
}

func visibilityRank(visibility config.Visibility) int {
	switch visibility {
	case config.VisibilitySimple:
		return 0
	case config.VisibilityNormal, "":
		return 1
	case config.VisibilityVerbose:
		return 2
	case config.VisibilityDebug:
		return 3
	default:
		return 1
	}
}
