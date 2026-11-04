package dashboard

import (
	stdjson "encoding/json"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/models"
)

// ---------------------------------------------------------------------------
// CLI credential files
// ---------------------------------------------------------------------------

func connExpiresIn(data map[string]any) int {
	switch v := data["expiresIn"].(type) {
	case float64:
		if v > 0 {
			return int(v)
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	return 3600
}

func connectionTokenPair(c *models.ProviderConnection) (accessToken, apiKey string, data map[string]any) {
	data = connDataMap(c)
	return dataStr(data, "accessToken"), dataStr(data, "apiKey"), data
}

func antigravityTokenPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token"), nil
}

func writeAntigravityTokenFile(c *models.ProviderConnection) bool {
	accessToken, apiKey, data := connectionTokenPair(c)
	token := accessToken
	if token == "" {
		token = apiKey
	}
	if token == "" {
		return false
	}
	path, err := antigravityTokenPath()
	if err != nil {
		return false
	}
	payload := map[string]any{
		"token": map[string]any{
			"access_token":  token,
			"token_type":    "Bearer",
			"refresh_token": dataStr(data, "refreshToken"),
			"expiry":        time.Now().Add(time.Duration(connExpiresIn(data)) * time.Second).UTC().Format("2006-01-02T15:04:05.000Z"),
		},
		"auth_method": "consumer",
	}
	encoded, err := stdjson.MarshalIndent(payload, "", "  ")
	if err != nil {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false
	}
	return os.WriteFile(path, encoded, 0o600) == nil
}

func readAntigravityFileToken() (string, bool) {
	path, err := antigravityTokenPath()
	if err != nil {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var parsed struct {
		Token struct {
			AccessToken string `json:"access_token"`
			APIKey      string `json:"apiKey"`
		} `json:"token"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return "", false
	}
	token := parsed.Token.AccessToken
	if token == "" {
		token = parsed.Token.APIKey
	}
	return token, token != ""
}

func codexAuthPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "auth.json"), nil
}

func writeCodexAuthFile(c *models.ProviderConnection) bool {
	accessToken, apiKey, data := connectionTokenPair(c)
	token := accessToken
	if token == "" {
		token = apiKey
	}
	if token == "" {
		return false
	}
	path, err := codexAuthPath()
	if err != nil {
		return false
	}
	auth := map[string]any{}
	if raw, readErr := os.ReadFile(path); readErr == nil {
		_ = json.Unmarshal(raw, &auth)
		if auth == nil {
			auth = map[string]any{}
		}
	}
	auth["OPENAI_API_KEY"] = token
	if accessToken != "" {
		auth["tokens"] = map[string]any{
			"access_token":  accessToken,
			"refresh_token": dataStr(data, "refreshToken"),
			"id_token":      dataStr(data, "idToken"),
			"expires_in":    connExpiresIn(data),
			"token_type":    "Bearer",
		}
	} else {
		auth["tokens"] = map[string]any{
			"access_token": token,
			"token_type":   "Bearer",
		}
	}
	encoded, err := stdjson.MarshalIndent(auth, "", "  ")
	if err != nil {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false
	}
	return os.WriteFile(path, encoded, 0o644) == nil
}

func readCodexFileToken() (string, bool) {
	path, err := codexAuthPath()
	if err != nil {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var parsed struct {
		OpenAIAPIKey string `json:"OPENAI_API_KEY"`
		Tokens       struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return "", false
	}
	token := parsed.OpenAIAPIKey
	if token == "" {
		token = parsed.Tokens.AccessToken
	}
	return token, token != ""
}

func writeAutoSwitchTokenFile(provider string, c *models.ProviderConnection) bool {
	if provider == codexProviderID {
		return writeCodexAuthFile(c)
	}
	return writeAntigravityTokenFile(c)
}

func readAutoSwitchFileToken(provider string) (string, bool) {
	if provider == codexProviderID {
		return readCodexFileToken()
	}
	return readAntigravityFileToken()
}
