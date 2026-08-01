package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/usewhale/whale/internal/securefs"
)

var deepSeekAPIKeyPattern = regexp.MustCompile(`^sk-[A-Za-z0-9_-]{16,}$`)

type Credentials struct {
	DeepSeekAPIKey string `json:"deepseek_api_key,omitempty"`
	MiniMaxAPIKey  string `json:"minimax_api_key,omitempty"`
}

func credentialsPath(dataDir string) string {
	return filepath.Join(dataDir, "credentials.json")
}

func LoadCredentials(dataDir string) (Credentials, error) {
	path := credentialsPath(dataDir)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Credentials{}, nil
		}
		return Credentials{}, fmt.Errorf("read credentials: %w", err)
	}
	var creds Credentials
	if err := json.Unmarshal(b, &creds); err != nil {
		return Credentials{}, fmt.Errorf("unmarshal credentials: %w", err)
	}
	return creds, nil
}

func SaveCredentials(dataDir string, creds Credentials) error {
	b, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	path := credentialsPath(dataDir)
	if err := securefs.WritePrivateFile(path, b); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}

func ValidateDeepSeekAPIKey(key string) error {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return fmt.Errorf("empty key")
	}
	if !deepSeekAPIKeyPattern.MatchString(trimmed) {
		return fmt.Errorf("invalid DeepSeek API key format")
	}
	return nil
}

func LoadDeepSeekAPIKey(dataDir string) (string, error) {
	if v := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")); v != "" {
		return v, nil
	}
	creds, err := LoadCredentials(dataDir)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(creds.DeepSeekAPIKey), nil
}

func LoadMiniMaxAPIKey(dataDir string) (string, error) {
	if v := strings.TrimSpace(os.Getenv("MINIMAX_API_KEY")); v != "" {
		return v, nil
	}
	creds, err := LoadCredentials(dataDir)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(creds.MiniMaxAPIKey), nil
}
