package channels

import (
	"context"
	"testing"
	"time"

	"github.com/lsongdev/miya-channels/config"
)

func TestIncomingEventSourceKeySeparatesConversationAndSender(t *testing.T) {
	event := IncomingEvent{ChannelID: "tg-lab", ConversationID: "group-1", SenderID: "user-2"}
	if got := event.SourceKey(); got != "tg-lab:conversation:group-1:sender:user-2" {
		t.Fatalf("SourceKey = %q", got)
	}
}

type testChannel struct {
	incoming chan IncomingEvent
}

func (c *testChannel) Receive(context.Context) (chan IncomingEvent, error) { return c.incoming, nil }
func (c *testChannel) CreateReplyWriter(string) Writer                     { return testWriter{} }
func (c *testChannel) SendFile(string, string, string) error               { return nil }

type testWriter struct{}

func (testWriter) Write(string, bool) error { return nil }

func TestChannelManagerEnrichesEventWithInstance(t *testing.T) {
	manager, err := NewChannelManager(nil, ChannelOptions{})
	if err != nil {
		t.Fatalf("NewChannelManager: %v", err)
	}
	incoming := make(chan IncomingEvent, 1)
	manager.RegisterInstance(config.ChannelInstance{ID: "tg-lab", Type: "telegram"}, &testChannel{incoming: incoming})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	incoming <- IncomingEvent{ConversationID: "chat", SenderID: "user"}
	select {
	case event := <-manager.Incoming:
		if event.ChannelID != "tg-lab" || event.ChannelType != "telegram" || event.ReceivedAt.IsZero() {
			t.Fatalf("event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestChannelManagerRejectsUnknownType(t *testing.T) {
	_, err := NewChannelManager([]config.ChannelInstance{{ID: "unknown-1", Type: "unknown"}}, ChannelOptions{})
	if err == nil {
		t.Fatal("expected unknown channel type error")
	}
}
