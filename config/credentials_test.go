package config

import (
	"errors"
	"strings"
	"testing"
)

func TestResolveCredentialReferencesIncludesChannelConfig(t *testing.T) {
	id := strings.Repeat("c", 64)
	input := []byte(`{"channels":[{"id":"feishu","type":"feishu","config":{"appSecret":"keyring://ruby/` + id + `"}}]}`)
	resolved, err := resolveCredentialReferencesWith(input, func(got string) (string, error) {
		if got != id {
			t.Fatalf("credential id = %q", got)
		}
		return "channel-secret", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(resolved), credentialReferencePrefix) || !strings.Contains(string(resolved), "channel-secret") {
		t.Fatalf("channel credential was not resolved: %s", resolved)
	}
}

func TestResolveCredentialReferencesFailsClosed(t *testing.T) {
	id := strings.Repeat("d", 64)
	_, err := resolveCredentialReferencesWith([]byte(`{"token":"keyring://ruby/`+id+`"}`), func(string) (string, error) { return "", errors.New("not found") })
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v", err)
	}
}
