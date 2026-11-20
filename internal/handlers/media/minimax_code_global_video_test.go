package media

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"axonrouter/proxy/internal/handlers/shared"
	"axonrouter/proxy/internal/providers"
)

func TestValidateMiniMaxVideoBody(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		body    string
		wantErr bool
	}{
		{"prompt shorthand", "MiniMax-H3", `{"prompt":"a cat","duration":6,"resolution":"768P"}`, false},
		{"first frame", "MiniMax-H3-Max", `{"content":[{"type":"text","text":"walk"},{"type":"image_url","role":"first_frame","image_url":{"url":"https://cdn.example/first.png"}}],"duration":10,"resolution":"768P"}`, false},
		{"lite rejects 2K", "MiniMax-H3-Lite", `{"prompt":"a cat","duration":6,"resolution":"2K"}`, true},
		{"H3 rejects 480P", "MiniMax-H3", `{"prompt":"a cat","duration":6,"resolution":"480P"}`, true},
		{"highspeed rejects 2K", "MiniMax-H3-Highspeed", `{"prompt":"a cat","duration":6,"resolution":"2K"}`, true},
		{"max rejects 2K", "MiniMax-H3-Max", `{"prompt":"a cat","duration":6,"resolution":"2K"}`, true},
		{"duration below minimum", "MiniMax-H3", `{"prompt":"a cat","duration":3,"resolution":"768P"}`, true},
		{"max requires five seconds", "MiniMax-H3-Max", `{"prompt":"a cat","duration":4,"resolution":"768P"}`, true},
		{"frame and reference mixed", "MiniMax-H3", `{"content":[{"type":"text","text":"walk"},{"type":"image_url","role":"first_frame","image_url":{"url":"https://cdn.example/a.png"}},{"type":"image_url","role":"reference_image","image_url":{"url":"https://cdn.example/b.png"}}],"duration":6,"resolution":"768P"}`, true},
		{"too many reference images", "MiniMax-H3", `{"content":[{"type":"text","text":"walk"},` + strings.Repeat(`{"type":"image_url","role":"reference_image","image_url":{"url":"https://cdn.example/a.png"}},`, 10) + `{"type":"image_url","role":"reference_image","image_url":{"url":"https://cdn.example/a.png"}}],"duration":6,"resolution":"768P"}`, true},
		{"no prompt", "MiniMax-H3", `{"duration":6,"resolution":"768P"}`, true},
		{"two text items", "MiniMax-H3", `{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"duration":6,"resolution":"768P"}`, true},
		{"private media url", "MiniMax-H3", `{"content":[{"type":"text","text":"walk"},{"type":"image_url","role":"first_frame","image_url":{"url":"http://127.0.0.1/a.png"}}],"duration":6,"resolution":"768P"}`, true},
		{"oversized prompt", "MiniMax-H3", `{"prompt":"` + strings.Repeat("x", 7001) + `","duration":6,"resolution":"768P"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := validateMiniMaxVideoBody([]byte(tt.body), tt.model)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateMiniMaxVideoBody() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			var req miniMaxCreateRequest
			if err := json.Unmarshal(out, &req); err != nil {
				t.Fatalf("unmarshal translated body: %v", err)
			}
			if req.Model != tt.model {
				t.Errorf("model = %q, want %q", req.Model, tt.model)
			}
			if req.GenerateAudio == nil || !*req.GenerateAudio {
				t.Errorf("generate_audio should default to true")
			}
		})
	}
}

func TestValidateMiniMaxVideoBodyForcesRoutedModel(t *testing.T) {
	out, err := validateMiniMaxVideoBody([]byte(`{"model":"MiniMax-M3","prompt":"a cat","duration":6,"resolution":"768P"}`), "MiniMax-H3")
	if err != nil {
		t.Fatalf("validateMiniMaxVideoBody: %v", err)
	}
	var req miniMaxCreateRequest
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Model != "MiniMax-H3" {
		t.Fatalf("model = %q, want MiniMax-H3", req.Model)
	}
}

func TestMiniMaxVideoURLs(t *testing.T) {
	cfg := providers.ProviderConfig{VideoURL: "https://api.minimax.io/v2/video_generation"}
	conn := &shared.ConnectionData{BaseURL: "https://agent.minimax.io/mavis/api/v1/llm/v1/messages"}
	if got := miniMaxVideoCreateURL(cfg, conn); got != cfg.VideoURL {
		t.Errorf("chat baseUrl must not redirect video: got %q", got)
	}
	q, err := miniMaxVideoQueryURL(cfg, conn, "task/123")
	if err != nil {
		t.Fatalf("miniMaxVideoQueryURL: %v", err)
	}
	if want := "https://api.minimax.io/v2/query/video_generation/task%2F123"; q != want {
		t.Errorf("query URL = %q, want %q", q, want)
	}
	conn.BaseURL = "http://mock.local/v2/video_generation"
	if got := miniMaxVideoCreateURL(cfg, conn); got != conn.BaseURL {
		t.Errorf("explicit /v2 override ignored: got %q", got)
	}
}

func TestParseMiniMaxVideoPoll(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		terminal    bool
		wantURL     string
		wantErrTest bool
	}{
		{"nested succeeded", `{"task":{"task_id":"t1","status":"succeeded","content":{"url":"https://cdn.example/a.mp4"}}}`, true, "https://cdn.example/a.mp4", false},
		{"flat succeeded", `{"task_id":"t1","status":"succeeded","content":{"url":"https://cdn.example/b.mp4"}}`, true, "https://cdn.example/b.mp4", false},
		{"running", `{"task_id":"t1","status":"running"}`, false, "", false},
		{"failed", `{"task_id":"t1","status":"failed","error":{"message":"render failed"}}`, true, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseMiniMaxVideoPoll([]byte(tt.body))
			if (err != nil) != tt.wantErrTest {
				t.Fatalf("parseMiniMaxVideoPoll() error = %v", err)
			}
			if p.terminal() != tt.terminal {
				t.Errorf("terminal() = %v, want %v", p.terminal(), tt.terminal)
			}
			if p.Content.URL != tt.wantURL {
				t.Errorf("content URL = %q, want %q", p.Content.URL, tt.wantURL)
			}
		})
	}
}

func TestMiniMaxVideoCreatePollContent(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/video_generation":
			if r.Method != http.MethodPost {
				t.Errorf("create method = %s", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Errorf("create auth = %q", r.Header.Get("Authorization"))
			}
			body, _ := io.ReadAll(r.Body)
			var req miniMaxCreateRequest
			if err := json.Unmarshal(body, &req); err != nil || req.Model != "MiniMax-H3" {
				t.Errorf("create body = %s (%v)", body, err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"task_id":"task-1"}`))
		case "/v2/query/video_generation/task-1":
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Errorf("poll auth = %q", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"task":{"task_id":"task-1","status":"succeeded","content":{"url":"` + srv.URL + `/download"}}}`))
		case "/download":
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("content download leaked credential: %q", got)
			}
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("mp4-bytes"))
		default:
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	h := &MediaHandler{}
	cfg := providers.ProviderConfig{VideoURL: srv.URL + "/v2/video_generation"}
	connData := &shared.ConnectionData{APIKey: "test-token"}
	ctx := context.Background()

	body, err := validateMiniMaxVideoBody([]byte(`{"prompt":"a cat","duration":6,"resolution":"768P"}`), "MiniMax-H3")
	if err != nil {
		t.Fatalf("validate body: %v", err)
	}
	created := h.videoFetch(ctx, srv.Client(), "minimax-code-global", miniMaxVideoCreateURL(cfg, connData), "test-token", cfg,
		videoProxyParams{Body: body, ContentType: "application/json"})
	if created.ErrText != "" {
		t.Fatalf("create failed: %s", created.ErrText)
	}
	if !strings.Contains(string(created.Body), "task-1") {
		t.Fatalf("create response = %s", created.Body)
	}

	queryURL, err := miniMaxVideoQueryURL(cfg, connData, "task-1")
	if err != nil {
		t.Fatalf("query URL: %v", err)
	}
	polled := h.videoFetch(ctx, srv.Client(), "minimax-code-global", queryURL, "test-token", cfg,
		videoProxyParams{RequestID: "task-1"})
	if polled.ErrText != "" {
		t.Fatalf("poll failed: %s", polled.ErrText)
	}
	task, err := parseMiniMaxVideoPoll(polled.Body)
	if err != nil || !task.terminal() {
		t.Fatalf("poll state = %s (%v)", polled.Body, err)
	}

	downloaded := h.videoFetch(ctx, srv.Client(), "minimax-code-global", task.Content.URL, "test-token", cfg,
		videoProxyParams{RequestID: "task-1", Content: true, SkipAuth: true})
	if downloaded.ErrText != "" {
		t.Fatalf("download failed: %s", downloaded.ErrText)
	}
	if downloaded.Stream == nil {
		t.Fatal("content download did not return a stream")
	}
	got, err := io.ReadAll(downloaded.Stream)
	if err != nil {
		t.Fatalf("read download: %v", err)
	}
	_ = downloaded.Stream.Close()
	if string(got) != "mp4-bytes" {
		t.Errorf("downloaded body = %q", got)
	}
}

func TestWriteVideoUpstreamErrorForwardsRetryAfter(t *testing.T) {
	w := httptest.NewRecorder()
	writeVideoUpstreamError(w, &videoUpstreamResponse{Status: 429, ErrText: "quota", RetryAfter: "60"})
	if got := w.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q", got)
	}
	if w.Code != 429 {
		t.Errorf("status = %d", w.Code)
	}
}
