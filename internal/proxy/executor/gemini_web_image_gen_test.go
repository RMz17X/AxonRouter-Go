package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeminiWebImagenURLRe(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantURL string
	}{
		{
			name:    "markdown image format",
			input:   "Here is your image:\n![cute robot](https://lh3.googleusercontent.com/gg/image_generation_content/s12345/robot.png)\nHope you like it!",
			wantURL: "https://lh3.googleusercontent.com/gg/image_generation_content/s12345/robot.png",
		},
		{
			name:    "direct url with trailing punctuation",
			input:   "Check this: https://lh3.googleusercontent.com/image_generation_content/abc-123.jpg, nice right?",
			wantURL: "https://lh3.googleusercontent.com/image_generation_content/abc-123.jpg",
		},
		{
			name:    "gstatic cdn format",
			input:   `{"image": "https://encrypted-tbn0.gstatic.com/images?q=tbn:ANd9GcR123"}`,
			wantURL: "https://encrypted-tbn0.gstatic.com/images?q=tbn:ANd9GcR123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := geminiWebImagenURLRe.FindAllString(tt.input, -1)
			if len(matches) == 0 {
				t.Fatalf("no match found in input: %s", tt.input)
			}
			cleaned := strings.TrimRight(matches[0], ")]\"',.")
			if cleaned != tt.wantURL {
				t.Errorf("got %q, want %q", cleaned, tt.wantURL)
			}
		})
	}
}

func TestGenerateGeminiWebImage_Mock(t *testing.T) {
	const fakeImageBytes = "GIF89aFAKEIMAGEBYTES"
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/gif")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fakeImageBytes))
	}))
	defer imageServer.Close()

	// Build a fake gemini.google.com upstream
	fake := newGeminiWebFakeUpstream(t)
	fake.at = "csrf-token-123"
	imageMarkdown := "![Generated Robot](" + imageServer.URL + "/googleusercontent.com/image_generation_content/12345)"
	fake.streamBody = ")]}'\n\n" + geminiWebFrameLine(imageMarkdown, true, "cf41b0e0dd7d53e5") + "\n"

	server := httptest.NewServer(fake)
	defer server.Close()

	cookie := "__Secure-1PSID=test-sid; __Secure-1PSIDTS=test-ts"
	endpoints := geminiWebEndpoints{
		origin: server.URL,
		app:    server.URL + "/app",
		stream: server.URL + geminiWebStreamPath,
		batch:  server.URL + geminiWebBatchPath,
	}

	stealth, _ := newGeminiWebStealthClient(nil, endpoints.origin, cookie)
	session := &geminiWebSession{
		endpoints: endpoints,
		cookie:    cookie,
		stealth:   stealth,
	}

	ctx := context.Background()
	results, err := GenerateGeminiWebImageWithSession(ctx, session, "origami robot", 1)
	if err != nil {
		t.Fatalf("GenerateGeminiWebImage error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].B64 == "" {
		t.Error("expected non-empty B64 in result")
	}
}
