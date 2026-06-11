package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lsongdev/miya-agents/acp"
	"github.com/lsongdev/miya/channels"
	"github.com/lsongdev/miya/config"
)

func main() {
	acpURL := flag.String("acp-url", os.Getenv("ACP_URL"), "ACP server URL")
	agentName := flag.String("agent", os.Getenv("ACP_AGENT"), "Default agent name")
	flag.Parse()

	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	acpURLStr := *acpURL
	if acpURLStr == "" && cfg.ACP != nil {
		acpURLStr = cfg.ACP.URL
	}
	if acpURLStr == "" {
		log.Fatal("ACP URL is required (--acp-url, ACP_URL env, or acp.url in config)")
	}

	agentNameStr := *agentName
	if agentNameStr == "" && cfg.ACP != nil {
		agentNameStr = cfg.ACP.AgentName
	}
	if agentNameStr == "" {
		log.Fatal("Agent name is required (--agent, ACP_AGENT env, or acp.agentName in config)")
	}

	acpClient := acp.NewClient(acpURLStr)

	cm := channels.NewChannelManager(cfg)
	if len(cm.ListChannels()) == 0 {
		log.Fatal("No channels configured")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cm.Start(ctx); err != nil {
		log.Fatalf("Failed to start channels: %v", err)
	}

	log.Printf("Listening for messages (agent: %s, acp: %s)...", agentNameStr, acpURLStr)

	for {
		select {
		case <-ctx.Done():
			log.Println("Shutting down...")
			time.Sleep(500 * time.Millisecond)
			log.Println("Bye!")
			return

		case msg := <-cm.Incoming:
			go handleMessage(ctx, acpClient, cm, msg, agentNameStr)
		}
	}
}

func handleMessage(ctx context.Context, client *acp.Client, cm *channels.ChannelManager, msg channels.IncomingMessage, agentName string) {
	input := []acp.Message{{
		Role: "user",
		Parts: []acp.MessagePart{{
			ContentType: "text/plain",
			Content:     msg.Content,
		}},
	}}

	writer, err := cm.CreateReplyWriter(msg.From, msg.ReplyTo)
	if err != nil {
		log.Printf("Error creating writer for %s/%s: %v", msg.From, msg.ReplyTo, err)
		return
	}

	events, err := client.RunStream(ctx, agentName, input)
	if err != nil {
		writer.Write(fmt.Sprintf("Error: %v", err), true)
		return
	}

	for evt := range events {
		switch evt.Type {
		case "update":
			if m, ok := evt.Data.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					writer.Write(text, false)
				}
			}

		case "message.part":
			var part acp.MessagePart
			if b, _ := json.Marshal(evt.Data); json.Unmarshal(b, &part) == nil {
				writer.Write(part.Content, false)
			}

		case "message.completed":
			var m acp.Message
			if b, _ := json.Marshal(evt.Data); json.Unmarshal(b, &m) == nil {
				for _, p := range m.Parts {
					writer.Write(p.Content, false)
				}
			}

		case "run.completed":
			var run acp.Run
			if b, _ := json.Marshal(evt.Data); json.Unmarshal(b, &run) == nil {
				for _, m := range run.Output {
					for _, p := range m.Parts {
						writer.Write(p.Content, false)
					}
				}
			}
			writer.Write("", true)
			return

		case "run.failed":
			if s, ok := evt.Data.(string); ok {
				writer.Write(fmt.Sprintf("Error: %s", s), false)
			}
			var run acp.Run
			if b, _ := json.Marshal(evt.Data); json.Unmarshal(b, &run) == nil && run.Error != nil {
				writer.Write(fmt.Sprintf("Error: %s", run.Error.Message), false)
			}
			writer.Write("", true)
			return

		case "error":
			var errResp acp.RunError
			if b, _ := json.Marshal(evt.Data); json.Unmarshal(b, &errResp) == nil {
				writer.Write(fmt.Sprintf("Error: %s", errResp.Message), false)
			}
			writer.Write("", true)
			return
		}
	}

	log.Printf("ACP stream ended without completion for: %s", msg.Content)
	writer.Write("", true)
}
