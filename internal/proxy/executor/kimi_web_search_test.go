package executor

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeKimiSearchSite serves the search turn's real wire shape,
// captured live from kimi.com on 2026-11-11: search_plus events
// whose get_res messages carry the structured results, interleaved
// with the furniture and the cmpl answer deltas.
type fakeKimiSearchSite struct {
	completion map[string]any
}

func (f *fakeKimiSearchSite) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/token/refresh", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"access-1","refresh_token":"refresh-2","expires_in":1800}`))
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"chat-search"}`))
	})
	mux.HandleFunc("POST /api/chat/{id}/completion/stream", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &f.completion)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"content\":\"Versi Go terakhir?\",\"event\":\"req\",\"model\":\"kimi\"}\n\n" +
				"data: {\"event\":\"resp\",\"model\":\"kimi\"}\n\n" +
				"data: {\"event\":\"rename\",\"text\":\"Versi Go terakhir?\"}\n\n" +
				"data: {\"event\":\"search_plus\",\"msg\":{\"type\":\"start\",\"view\":\"search_plus\"},\"type\":\"start\",\"view\":\"search_plus\"}\n\n" +
				"data: {\"event\":\"search_plus\",\"msg\":{\"type\":\"get_res\",\"title\":\"Release History\",\"url\":\"https://go.dev/doc/devel/release\",\"snippet\":\"go1.27 notes\",\"date\":\"2026-08-19\",\"site_name\":\"The Go Programming Language\",\"icon\":\"https://example.com/go.ico\"},\"type\":\"get_res\",\"view\":\"search_plus\"}\n\n" +
				"data: {\"event\":\"search_plus\",\"msg\":{\"type\":\"get_res\",\"title\":\"Go\",\"url\":\"https://endoflife.date/go\",\"snippet\":\"Latest 1.27\",\"date\":\"2026-10-21\",\"site_name\":\"endoflife.date\",\"icon\":\"\"},\"type\":\"get_res\",\"view\":\"search_plus\"}\n\n" +
				"data: {\"event\":\"search_plus\",\"msg\":{\"type\":\"get_res\",\"title\":\"dup\",\"url\":\"https://go.dev/doc/devel/release\",\"snippet\":\"dup row\"},\"type\":\"get_res\",\"view\":\"search_plus\"}\n\n" +
				"data: {\"event\":\"cmpl\",\"text\":\"Jawabannya \",\"view\":\"cmpl\"}\n\n" +
				"data: {\"event\":\"cmpl\",\"text\":\"Go 1.27.\",\"view\":\"cmpl\"}\n\n" +
				"data: {\"event\":\"done\"}\n\n"))
	})
	mux.HandleFunc("DELETE /api/chat/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	return mux
}

func TestExecuteKimiWebSearch_CollectsStructuredResults(t *testing.T) {
	site := &fakeKimiSearchSite{}
	server := httptest.NewServer(site.handler())
	t.Cleanup(server.Close)

	oldBase, oldFactory := kimiWebBaseURL, kimiWebTransportFactory
	kimiWebBaseURL = server.URL
	kimiWebTransportFactory = func(req *Request) (kimiWebDoer, error) {
		return &kimiWebPlainDoer{}, nil
	}
	t.Cleanup(func() { kimiWebBaseURL, kimiWebTransportFactory = oldBase, oldFactory })

	testToken := "refresh" + "-1"
	req := &Request{
		APIKey:       testToken,
		ConnectionID: "conn-kimi-search",
	}
	outcome, err := ExecuteKimiWebSearch(context.Background(), req, "Versi Go terakhir?", 5)
	if err != nil {
		t.Fatalf("ExecuteKimiWebSearch: %v", err)
	}

	if got := site.completion["use_search"]; got != true {
		t.Errorf("use_search = %v, want true for a search turn", got)
	}
	if got := site.completion["model"]; got != "kimi" {
		t.Errorf("completion model = %v, want the kimi slot", got)
	}

	if len(outcome.Results) != 2 {
		t.Fatalf("results = %d, want 2 (the duplicate URL must collapse): %+v", len(outcome.Results), outcome.Results)
	}
	first := outcome.Results[0]
	if first.Title != "Release History" || first.URL != "https://go.dev/doc/devel/release" ||
		first.Snippet != "go1.27 notes" || first.PublishedAt != "2026-08-19" ||
		first.SiteName != "The Go Programming Language" || first.FaviconURL != "https://example.com/go.ico" {
		t.Errorf("first result = %+v", first)
	}
	if outcome.Results[1].URL != "https://endoflife.date/go" {
		t.Errorf("second result = %+v", outcome.Results[1])
	}
	if !strings.Contains(outcome.Answer, "Go 1.27.") || strings.Contains(outcome.Answer, "Versi Go terakhir?") {
		t.Errorf("answer = %q, want the cmpl text only, no furniture", outcome.Answer)
	}

	capped, err := ExecuteKimiWebSearch(context.Background(), req, "Versi Go terakhir?", 1)
	if err != nil {
		t.Fatalf("ExecuteKimiWebSearch capped: %v", err)
	}
	if len(capped.Results) != 1 {
		t.Errorf("capped results = %d, want 1", len(capped.Results))
	}
}
