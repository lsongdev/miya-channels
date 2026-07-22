package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	keyring "github.com/zalando/go-keyring"
)

const (
	credentialReferencePrefix = "keyring://ruby/"
	credentialServiceName     = "Ruby Desktop"
)

func resolveCredentialReferences(data []byte) ([]byte, error) {
	return resolveCredentialReferencesWith(data, func(id string) (string, error) {
		return keyring.Get(credentialServiceName, id)
	})
}

func resolveCredentialReferencesWith(data []byte, get func(string) (string, error)) ([]byte, error) {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	resolved, err := resolveCredentialValue(root, get)
	if err != nil {
		return nil, err
	}
	return json.Marshal(resolved)
}

func resolveCredentialValue(value any, get func(string) (string, error)) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			resolved, err := resolveCredentialValue(child, get)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			typed[key] = resolved
		}
		return typed, nil
	case []any:
		for index, child := range typed {
			resolved, err := resolveCredentialValue(child, get)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", index, err)
			}
			typed[index] = resolved
		}
		return typed, nil
	case string:
		if !strings.HasPrefix(typed, credentialReferencePrefix) {
			return typed, nil
		}
		id := strings.TrimPrefix(typed, credentialReferencePrefix)
		if len(id) != sha256.Size*2 {
			return nil, fmt.Errorf("invalid Ruby credential reference")
		}
		if _, err := hex.DecodeString(id); err != nil {
			return nil, fmt.Errorf("invalid Ruby credential reference: %w", err)
		}
		secret, err := get(id)
		if err != nil {
			return nil, fmt.Errorf("read system credential %q: %w", id, err)
		}
		return secret, nil
	default:
		return value, nil
	}
}
