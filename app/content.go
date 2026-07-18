package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lsongdev/miya-agents/acp"
	"github.com/lsongdev/miya-channels/channels"
)

func eventContentBlocks(event channels.IncomingEvent) []acp.ContentBlock {
	blocks := make([]acp.ContentBlock, 0, 1+len(event.Attachments))
	if event.Text != "" {
		blocks = append(blocks, acp.ContentBlock{Type: "text", Text: event.Text})
	}
	for _, attachment := range event.Attachments {
		block := acp.ContentBlock{Type: attachmentContentType(attachment), Name: attachment.Name, MimeType: attachment.MimeType}
		if attachment.URL != "" {
			url := attachment.URL
			block.URI = &url
		}
		if len(attachment.Data) > 0 {
			block.Data = base64.StdEncoding.EncodeToString(attachment.Data)
		}
		if attachment.Size > 0 {
			size := int(attachment.Size)
			block.Size = &size
		}
		blocks = append(blocks, block)
	}
	return blocks
}

func attachmentContentType(attachment channels.Attachment) string {
	if attachment.Type == "image" || attachment.Type == "audio" {
		return attachment.Type
	}
	return "resource"
}

func contentAttachment(content acp.ContentBlock) (channels.Attachment, bool) {
	if content.Type != "image" && content.Type != "audio" && content.Type != "resource" && content.Type != "resource_link" {
		return channels.Attachment{}, false
	}
	attachment := channels.Attachment{
		Type:     fileType(content.Type, content.MimeType),
		Name:     content.Name,
		MimeType: content.MimeType,
	}
	if content.URI != nil {
		attachment.URL = *content.URI
	}
	if content.Data != "" {
		data, err := base64.StdEncoding.DecodeString(content.Data)
		if err != nil {
			return channels.Attachment{}, false
		}
		attachment.Data = data
		attachment.Size = int64(len(data))
	}
	if content.Size != nil && attachment.Size == 0 {
		attachment.Size = int64(*content.Size)
	}
	return attachment, attachment.URL != "" || len(attachment.Data) > 0
}

type filePayload struct {
	Type    string `json:"type"`
	URL     string `json:"url"`
	Caption string `json:"caption,omitempty"`
	Name    string `json:"name,omitempty"`
	Mime    string `json:"mime,omitempty"`
}

func attachmentPayload(attachment channels.Attachment) (filePayload, error) {
	url := attachment.URL
	if url == "" && len(attachment.Data) > 0 {
		path, err := writeInlineAttachment(attachment)
		if err != nil {
			return filePayload{}, err
		}
		url = "file://" + path
	}
	if url == "" {
		return filePayload{}, fmt.Errorf("attachment has no URL or data")
	}
	mimeType := attachment.MimeType
	if mimeType == "" {
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(attachment.Name)))
	}
	return filePayload{Type: attachment.Type, URL: url, Caption: attachment.Name, Name: attachment.Name, Mime: mimeType}, nil
}

func fileDelivery(content acp.ContentBlock) (filePayload, bool, error) {
	attachment, ok := contentAttachment(content)
	if !ok {
		return filePayload{}, false, nil
	}
	payload, err := attachmentPayload(attachment)
	if err != nil {
		return filePayload{}, false, err
	}
	if content.Title != nil {
		payload.Caption = *content.Title
	} else if content.Description != nil {
		payload.Caption = *content.Description
	}
	return payload, true, nil
}

func writeInlineAttachment(attachment channels.Attachment) (string, error) {
	name := attachment.Name
	if name == "" {
		name = "attachment" + extensionForMime(attachment.MimeType)
	}
	path := filepath.Join(os.TempDir(), "miya-channels", fmt.Sprintf("%d-%s", time.Now().UnixNano(), filepath.Base(name)))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, attachment.Data, 0600); err != nil {
		return "", err
	}
	return path, nil
}

func extensionForMime(mimeType string) string {
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

func encodeFilePayload(payload filePayload) (string, error) {
	data, err := json.Marshal(payload)
	return string(data), err
}
