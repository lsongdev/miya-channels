package app

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/lsongdev/miya-agents/acp"
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
