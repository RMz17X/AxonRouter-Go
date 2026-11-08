package oauth

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func init() {
	Register("perch", RefreshPerch)
	Register("pc", RefreshPerch)
}

const (
	perchDefaultSupabaseURL = "https://zlfuvsfjtgsdtqcaykia.supabase.co"
	perchDefaultAnonKey     = "sb_publishable_w4R_pNkUpygBFIljZCoOlA_67ACg0FO"
)

// RefreshPerch refreshes a Perch AI account access token via Supabase Auth.
func RefreshPerch(ctx context.Context, p *Params) (*TokenResult, error) {
	rt := strings.TrimSpace(p.RefreshToken)
	if rt == "" {
		return nil, fmt.Errorf("perch: no refresh token stored")
	}

	tokenURL := perchDefaultSupabaseURL + "/auth/v1/token?grant_type=refresh_token"
	bodyBytes, err := json.Marshal(map[string]string{
		"refresh_token": rt,
	})
	if err != nil {
		return nil, fmt.Errorf("perch: marshal refresh payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("perch: build refresh request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", perchDefaultAnonKey)
	req.Header.Set("Authorization", "Bearer "+perchDefaultAnonKey)

	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perch: refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("perch: read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("perch: refresh returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("perch: parse refresh response: %w", err)
	}

	if parsed.AccessToken == "" {
		return nil, fmt.Errorf("perch: refresh response missing access_token")
	}

	newRT := parsed.RefreshToken
	if newRT == "" {
		newRT = rt
	}

	return &TokenResult{
		AccessToken:  parsed.AccessToken,
		RefreshToken: newRT,
		ExpiresIn:    parsed.ExpiresIn,
		APIKey:       parsed.AccessToken,
	}, nil
}
