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
		{"hailuo 6s", "MiniMax-Hailuo-2.3", `{"prompt":"a cat","duration":6,"resolution":"768P"}`, false},
		{"hailuo rejects 2K", "MiniMax-Hailuo-2.3", `{"prompt":"a cat","duration":6,"resolution":"2K"}`, true},
		{"hailuo rejects 5s", "MiniMax-Hailuo-2.3", `{"prompt":"a cat","duration":5,"resolution":"768P"}`, true},
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
			var req miniMaxMatrixVideoRequest
			if err := json.Unmarshal(out, &req); err != nil {
				t.Fatalf("unmarshal translated body: %v", err)
			}
			if want := miniMaxMatrixVideoModel(tt.model); req.Model != want {
				t.Errorf("model = %q, want %q", req.Model, want)
			}
			if req.Prompt == "" {
				t.Error("prompt must be present in the Matrix body")
			}
			if req.Duration == 0 {
				t.Error("duration must be present in the Matrix body")
			}
		})
	}
}

func TestValidateMiniMaxVideoBodyForcesRoutedModel(t *testing.T) {
	out, err := validateMiniMaxVideoBody([]byte(`{"model":"MiniMax-M3","prompt":"a cat","duration":6,"resolution":"768P"}`), "MiniMax-H3")
	if err != nil {
		t.Fatalf("validateMiniMaxVideoBody: %v", err)
	}
	var req miniMaxMatrixVideoRequest
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Model != miniMaxMatrixH3Model {
		t.Fatalf("model = %q, want %q", req.Model, miniMaxMatrixH3Model)
	}
}

func TestMiniMaxVideoURLs(t *testing.T) {
	cfg := providers.ProviderConfig{BaseURL: "https://agent.minimax.io/mavis/api/v1/llm/v1/messages"}
	conn := &shared.ConnectionData{BaseURL: "https://agent.minimax.io/mavis/api/v1/llm/v1/messages"}
	c, err := miniMaxVideoCreateURL(cfg, conn)
	if err != nil {
		t.Fatalf("miniMaxVideoCreateURL: %v", err)
	}
	if want := "https://agent.minimax.io" + miniMaxMatrixPrefix + miniMaxMatrixVideoSubmit; c != want {
		t.Errorf("create URL = %q, want %q", c, want)
	}
	q, err := miniMaxVideoQueryURL(cfg, conn, "task/123")
	if err != nil {
		t.Fatalf("miniMaxVideoQueryURL: %v", err)
	}
	if want := "https://agent.minimax.io" + miniMaxMatrixPrefix + miniMaxMatrixVideoQueryTool; q != want {
		t.Errorf("query URL = %q, want %q", q, want)
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
		{"matrix succeeded", `{"code":0,"message":"ok","task_id":"t1","status":"succeeded","video_url":"https://cdn.example/c.mp4"}`, true, "https://cdn.example/c.mp4", false},
		{"matrix running", `{"code":0,"status":"running","task_id":"t1"}`, false, "", false},
		{"matrix biz error", `{"code":402,"message":"remaining M Plan weekly limit"}`, true, "", false},
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
			if p.contentURL() != tt.wantURL {
				t.Errorf("content URL = %q, want %q", p.contentURL(), tt.wantURL)
			}
		})
	}
}

func TestMiniMaxVideoCreatePollContent(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case miniMaxMatrixPrefix + miniMaxMatrixVideoSubmit:
			if r.Method != http.MethodPost {
				t.Errorf("create method = %s", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Errorf("create auth = %q", r.Header.Get("Authorization"))
			}
			body, _ := io.ReadAll(r.Body)
			var req miniMaxMatrixVideoRequest
			if err := json.Unmarshal(body, &req); err != nil || req.Model != miniMaxMatrixH3Model || req.Prompt != "a cat" {
				t.Errorf("create body = %s (%v)", body, err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","task_id":"task-1"}`))
		case miniMaxMatrixPrefix + miniMaxMatrixVideoQueryTool:
			if r.Method != http.MethodPost {
				t.Errorf("poll method = %s", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Errorf("poll auth = %q", r.Header.Get("Authorization"))
			}
			body, _ := io.ReadAll(r.Body)
			var q miniMaxMatrixVideoQuery
			if err := json.Unmarshal(body, &q); err != nil || q.TaskID != "task-1" {
				t.Errorf("poll body = %s (%v)", body, err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","status":"succeeded","video_url":"` + srv.URL + `/download"}`))
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
	cfg := providers.ProviderConfig{BaseURL: srv.URL}
	connData := &shared.ConnectionData{BaseURL: srv.URL, APIKey: "test-token"}
	ctx := context.Background()

	body, err := validateMiniMaxVideoBody([]byte(`{"prompt":"a cat","duration":6,"resolution":"768P"}`), "MiniMax-H3")
	if err != nil {
		t.Fatalf("validate body: %v", err)
	}
	createURL, err := miniMaxVideoCreateURL(cfg, connData)
	if err != nil {
		t.Fatalf("create URL: %v", err)
	}
	created := h.videoFetch(ctx, srv.Client(), "minimax-code-global", createURL, "test-token", cfg,
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
	queryBody, err := buildMiniMaxMatrixVideoQuery("MiniMax-H3", "task-1")
	if err != nil {
		t.Fatalf("query body: %v", err)
	}
	polled := h.videoFetch(ctx, srv.Client(), "minimax-code-global", queryURL, "test-token", cfg,
		videoProxyParams{RequestID: "task-1", QueryBody: queryBody, ContentType: "application/json"})
	if polled.ErrText != "" {
		t.Fatalf("poll failed: %s", polled.ErrText)
	}
	task, err := parseMiniMaxVideoPoll(polled.Body)
	if err != nil || !task.terminal() {
		t.Fatalf("poll state = %s (%v)", polled.Body, err)
	}

	downloaded := h.videoFetch(ctx, srv.Client(), "minimax-code-global", task.contentURL(), "test-token", cfg,
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
