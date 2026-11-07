//go:build sqlite_legacy

package oauth

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"axonrouter/proxy/internal/db"
)

// authorizeForTest runs the authorize handler and returns the minted
// state plus the PKCE challenge carried in the authorization URL.
func authorizeForTest(t *testing.T, handler *OAuthHandler) (string, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/antigravity/authorize", nil)
	rec := httptest.NewRecorder()
	handler.HandleAntigravityAuthorize(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize status = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to unmarshal authorize JSON: %v", err)
	}
	authURL, err := url.Parse(res.URL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	return res.State, authURL.Query().Get("code_challenge")
}

func TestHandleAntigravityAuthorize(t *testing.T) {
	handler := NewOAuthHandler(nil)
	// The legacy ?redirect_uri= parameter rides along to prove it is no
	// longer honored (fragment 982): the URL must still pin agy's page.
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/oauth/antigravity/authorize?state=custom_state_123&redirect_uri=http%3A%2F%2Flocalhost%3A20130%2Fcallback",
		nil,
	)
	rec := httptest.NewRecorder()
	handler.HandleAntigravityAuthorize(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var res struct {
		URL         string `json:"url"`
		RedirectURI string `json:"redirectUri"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	authURL, err := url.Parse(res.URL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	if authURL.Host != "accounts.google.com" {
		t.Errorf("auth URL host = %q", authURL.Host)
	}
	if got := authURL.Query().Get("redirect_uri"); got != "https://antigravity.google/oauth-callback" {
		t.Errorf("redirect_uri = %q", got)
	}
	if res.RedirectURI != "https://antigravity.google/oauth-callback" {
		t.Errorf("response redirectUri = %q", res.RedirectURI)
	}
	if authURL.Query().Get("state") != "custom_state_123" {
		t.Errorf("state = %q", authURL.Query().Get("state"))
	}
	if authURL.Query().Get("access_type") != "offline" {
		t.Errorf("access_type = %q", authURL.Query().Get("access_type"))
	}
	if authURL.Query().Get("prompt") != "consent" {
		t.Errorf("prompt = %q", authURL.Query().Get("prompt"))
	}
	if authURL.Query().Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q", authURL.Query().Get("code_challenge_method"))
	}
	if authURL.Query().Get("code_challenge") == "" {
		t.Errorf("code_challenge missing")
	}
	expectedScopes := []string{
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/userinfo.profile",
		"https://www.googleapis.com/auth/cclog",
		"https://www.googleapis.com/auth/experimentsandconfigs",
		"https://www.googleapis.com/auth/aicode",
		"openid",
	}
	if got := authURL.Query().Get("scope"); got != strings.Join(expectedScopes, " ") {
		t.Errorf("scope = %q", got)
	}

	reqRedirect := httptest.NewRequest(http.MethodGet, "/api/oauth/antigravity/authorize?redirect=true", nil)
	recRedirect := httptest.NewRecorder()
	handler.HandleAntigravityAuthorize(recRedirect, reqRedirect)
	if recRedirect.Code != http.StatusFound {
		t.Errorf("expected 302 redirect, got %d", recRedirect.Code)
	}
}

func TestHandleAntigravityExchangeErrors(t *testing.T) {
	handler := NewOAuthHandler(nil)

	tests := []struct {
		name         string
		method       string
		body         string
		expectedCode int
	}{
		{name: "method must be post", method: http.MethodGet, expectedCode: http.StatusMethodNotAllowed},
		{name: "missing code", method: http.MethodPost, body: `{"state":"state-123"}`, expectedCode: http.StatusBadRequest},
		{name: "missing state", method: http.MethodPost, body: `{"code":"code-123"}`, expectedCode: http.StatusBadRequest},
		{name: "unknown state", method: http.MethodPost, body: `{"code":"code-123","state":"never-minted"}`, expectedCode: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, "/api/oauth/antigravity/exchange", strings.NewReader(test.body))
			rec := httptest.NewRecorder()
			handler.HandleAntigravityExchange(rec, req)
			if rec.Code != test.expectedCode {
				t.Fatalf("status = %d, expected %d: %s", rec.Code, test.expectedCode, rec.Body.String())
			}
		})
	}
}

func TestHandleAntigravityExchangeSuccess(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	// Set from the authorize response before each exchange: the mock
	// token endpoint only accepts the verifier whose S256 hash equals it.
	var expectedChallenge string

	mockGoogleServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		if got := r.Form.Get("redirect_uri"); got != "https://antigravity.google/oauth-callback" {
			http.Error(w, "redirect mismatch", http.StatusBadRequest)
			return
		}
		// The live endpoint requires the embedded client secret even with
		// PKCE (fragment 983): the mock mirrors that requirement.
		if r.Form.Get("client_secret") == "" {
			http.Error(w, "client_secret is required", http.StatusBadRequest)
			return
		}
		if verifier := r.Form.Get("code_verifier"); verifier == "" || sha256Base64(verifier) != expectedChallenge {
			http.Error(w, "pkce verification failed", http.StatusBadRequest)
			return
		}
		if got := r.Form.Get("code"); got != "valid_google_code_123" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}

		// Simple mock JWT id_token with claims: {"email":"antigravity-user@gmail.com"}
		// Header: {"alg":"none"} -> eyJhbGciOiJub25lIn0
		// Payload: {"email":"antigravity-user@gmail.com"} -> eyJlbWFpbCI6ImFudGlncmF2aXR5LXVzZXJAZ21haWwuY29tIn0
		mockIDToken := "eyJhbGciOiJub25lIn0.eyJlbWFpbCI6ImFudGlncmF2aXR5LXVzZXJAZ21haWwuY29tIn0."

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"access_token": "ya29.mock_access_token_xyz",
			"refresh_token": "1//mock_refresh_token_uvw",
			"expires_in": 3600,
			"token_type": "Bearer",
			"id_token": "` + mockIDToken + `",
			"scope": "https://www.googleapis.com/auth/cloud-platform"
		}`))
	}))
	defer mockGoogleServer.Close()

	oldTokenURL := googleOAuthTokenURL
	googleOAuthTokenURL = mockGoogleServer.URL + "/token"
	defer func() { googleOAuthTokenURL = oldTokenURL }()

	handler := NewOAuthHandler(repo)
	state, challenge := authorizeForTest(t, handler)
	expectedChallenge = challenge
	body := `{"code":"valid_google_code_123","state":"` + state + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/antigravity/exchange", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleAntigravityExchange(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var res struct {
		Success    bool `json:"success"`
		Connection struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			Email    string `json:"email"`
		} `json:"connection"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}
	if !res.Success {
		t.Error("expected success response")
	}
	if res.Connection.Provider != "antigravity" {
		t.Errorf("expected provider 'antigravity', got %q", res.Connection.Provider)
	}
	if res.Connection.Email != "antigravity-user@gmail.com" {
		t.Errorf("expected email 'antigravity-user@gmail.com', got %q", res.Connection.Email)
	}
	connID := res.Connection.ID
	if connID == "" {
		t.Fatalf("missing connection id in response: %s", rec.Body.String())
	}

	// Verify DB record
	var provider, authType, name, data string
	var isActive int
	err := database.QueryRow(
		"SELECT provider, authType, name, isActive, data FROM providerConnections WHERE id = ?",
		connID,
	).Scan(&provider, &authType, &name, &isActive, &data)
	if err != nil {
		t.Fatalf("failed to query providerConnections: %v", err)
	}

	if provider != "antigravity" {
		t.Errorf("expected provider 'antigravity', got %s", provider)
	}
	if authType != "oauth" {
		t.Errorf("expected authType 'oauth', got %s", authType)
	}
	if !strings.Contains(name, "antigravity-user@gmail.com") {
		t.Errorf("expected name to contain email, got %s", name)
	}
	if isActive != 1 {
		t.Errorf("expected isActive 1, got %d", isActive)
	}

	var dataMap map[string]any
	if err := json.Unmarshal([]byte(data), &dataMap); err != nil {
		t.Fatalf("failed to unmarshal stored data: %v", err)
	}
	if dataMap["accessToken"] != "ya29.mock_access_token_xyz" {
		t.Errorf("expected accessToken, got %v", dataMap["accessToken"])
	}
	if dataMap["refreshToken"] != "1//mock_refresh_token_uvw" {
		t.Errorf("expected refreshToken, got %v", dataMap["refreshToken"])
	}
	if dataMap["email"] != "antigravity-user@gmail.com" {
		t.Errorf("expected email 'antigravity-user@gmail.com', got %v", dataMap["email"])
	}
	if dataMap["projectId"] != "aicode-consumers" {
		t.Errorf("expected projectId 'aicode-consumers', got %v", dataMap["projectId"])
	}

	// Update the existing connection with a custom project ID
	dataMap["projectId"] = "my-custom-project"
	customDataBytes, _ := json.Marshal(dataMap)
	_, err = database.Exec("UPDATE providerConnections SET data = ? WHERE id = ?", string(customDataBytes), connID)
	if err != nil {
		t.Fatalf("failed to update DB with custom projectId: %v", err)
	}

	// Exchange again for the same account — custom project ID should be
	// preserved. Verifiers are single-use (fragment 982), so the second
	// login round mints a fresh state/verifier pair first.
	state2, challenge2 := authorizeForTest(t, handler)
	expectedChallenge = challenge2
	body2 := `{"code":"valid_google_code_123","state":"` + state2 + `"}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/oauth/antigravity/exchange", strings.NewReader(body2))
	rec2 := httptest.NewRecorder()
	handler.HandleAntigravityExchange(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 on second exchange, got %d: %s", rec2.Code, rec2.Body.String())
	}

	err = database.QueryRow("SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&data)
	if err != nil {
		t.Fatalf("failed to re-query providerConnections: %v", err)
	}
	var dataMap2 map[string]any
	if err := json.Unmarshal([]byte(data), &dataMap2); err != nil {
		t.Fatalf("failed to unmarshal updated data: %v", err)
	}
	if dataMap2["projectId"] != "my-custom-project" {
		t.Errorf("expected custom projectId 'my-custom-project' to be preserved, got %v", dataMap2["projectId"])
	}
}
