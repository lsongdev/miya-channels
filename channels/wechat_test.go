package channels

import "testing"

func TestWechatAttachmentNamePrefersPayloadName(t *testing.T) {
	got := attachmentName("file:///tmp/generated-name.bin", "report.pdf")
	if got != "report.pdf" {
		t.Fatalf("attachmentName = %q", got)
	}
}

func TestWechatAttachmentNameFromFileURL(t *testing.T) {
	got := attachmentName("file:///tmp/capture.jpg", "")
	if got != "capture.jpg" {
		t.Fatalf("attachmentName = %q", got)
	}
}

func TestWechatAttachmentNameFromHTTPURL(t *testing.T) {
	got := attachmentName("https://example.com/files/camera.mp4?token=abc", "")
	if got != "camera.mp4" {
		t.Fatalf("attachmentName = %q", got)
	}
}
