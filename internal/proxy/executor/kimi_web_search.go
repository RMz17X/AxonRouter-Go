package executor

import (
	"bufio"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"

	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/proxy"
)

// Kimi Web as a search engine, served on POST /v1/search by the
// media handler. A use_search completion's stream carries a
// "search_plus" event family whose "get_res" messages are the
// structured result list — title, url, snippet, date, site name,
// favicon — captured live from kimi.com on 2026-11-11 (the written
// spec never documented them; the chat classifier ignores them).
// Search is a media service, not a chat model: there is deliberately
// no kimi-search entry in kimiWebModelMap.

// KimiWebSearchResult is one structured source from a search turn.
type KimiWebSearchResult struct {
	Title       string
	URL         string
	Snippet     string
	PublishedAt string
	SiteName    string
	FaviconURL  string
}

// KimiWebSearchOutcome is one search turn's yield: the sources in
// the order upstream ranked them, plus the synthesized answer.
type KimiWebSearchOutcome struct {
	Results []KimiWebSearchResult
	Answer  string
}

// kimiWebSearchEvent decodes one data: line of a search turn. The
// chat event struct has no "msg" — search payloads live there.
type kimiWebSearchEvent struct {
	Event string `json:"event"`
	Text  string `json:"text"`
	View  string `json:"view"`
	Msg   *struct {
		Type     string `json:"type"`
		Title    string `json:"title"`
		URL      string `json:"url"`
		Snippet  string `json:"snippet"`
		Date     string `json:"date"`
		SiteName string `json:"site_name"`
		Icon     string `json:"icon"`
	} `json:"msg"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ExecuteKimiWebSearch runs one use_search turn for the query on the
// request's connection and collects its structured results. The
// plumbing mirrors ForwardKimiWeb: lazy token exchange, a throwaway
// chat, best-effort cleanup.
func ExecuteKimiWebSearch(ctx context.Context, req *Request, query string, maxResults int) (*KimiWebSearchOutcome, error) {
	cred, err := kimiWebResolveCred(req)
	if err != nil {
		return nil, err
	}
	doer, err := kimiWebTransportFactory(req)
	if err != nil {
		return nil, proxy.UpstreamFailure(http.StatusServiceUnavailable, err.Error())
	}
	// Snapshot the upstream this turn talks to. The throwaway-chat
	// cleanup below runs on its own goroutine after the turn returned,
	// so it must not read the package global: the test suite swaps
	// that global at a fake site and restores it in a cleanup, which
	// raced with the goroutine.
	kimiBaseURL := kimiWebBaseURL

	callCtx, cancel := context.WithTimeout(ctx, kimiWebTurnTimeout)
	defer cancel()

	accessToken, err := kimiWebAccessToken(callCtx, doer, req, cred)
	if err != nil {
		return nil, err
	}
	chatID, err := kimiWebCreateChat(callCtx, doer, cred, accessToken)
	if err != nil {
		return nil, err
	}
	defer func() { go kimiWebDeleteChat(doer, cred, kimiBaseURL, accessToken, chatID) }()

	payload, err := json.Marshal(map[string]any{
		"messages":   []map[string]any{{"role": "user", "content": query}},
		"refs":       []any{},
		"use_search": true,
		"model":      "kimi",
	})
	if err != nil {
		return nil, proxy.UpstreamFailure(http.StatusInternalServerError, "kimi-web: marshal search payload: "+err.Error())
	}

	log.Info("kimi-web", "search query", "query", query)
	target := fmt.Sprintf("%s/api/chat/%s/completion/stream", kimiWebBaseURL, chatID)
	response, err := doer.do(callCtx, http.MethodPost, target, kimiWebHeaders(cred, accessToken), payload)
	if err != nil {
		return nil, proxy.UpstreamFailure(http.StatusBadGateway, "kimi-web: search connection failed: "+err.Error())
	}
	defer func() { _ = response.Body.Close() }()
	response.Body = log.ProgressReader(callCtx, response.Body)

	if response.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, kimiWebStatusFailure(response.StatusCode, string(snippet))
	}
	return kimiWebCollectSearch(response.Body, maxResults)
}

// kimiWebCollectSearch drains a search turn's stream: search_plus
// get_res messages become results (deduped by URL, arrival order is
// the upstream rank), cmpl deltas become the answer, an error event
// fails the turn so the caller can fail over to another account.
func kimiWebCollectSearch(r io.Reader, maxResults int) (*KimiWebSearchOutcome, error) {
	out := &KimiWebSearchOutcome{}
	seen := make(map[string]bool)
	var answer strings.Builder
	var streamErr string

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev kimiWebSearchEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		if ev.Error != nil && ev.Error.Message != "" {
			streamErr = ev.Error.Message
			break
		}
		switch ev.Event {
		case "search_plus":
			if ev.Msg == nil || ev.Msg.Type != "get_res" || ev.Msg.URL == "" || seen[ev.Msg.URL] {
				continue
			}
			seen[ev.Msg.URL] = true
			out.Results = append(out.Results, KimiWebSearchResult{
				Title:       ev.Msg.Title,
				URL:         ev.Msg.URL,
				Snippet:     ev.Msg.Snippet,
				PublishedAt: ev.Msg.Date,
				SiteName:    ev.Msg.SiteName,
				FaviconURL:  ev.Msg.Icon,
			})
		case "cmpl":
			if ev.View != "think" && ev.View != "thinking" {
				answer.WriteString(ev.Text)
			}
		case "error":
			streamErr = ev.Text
			if streamErr == "" {
				streamErr = "upstream error event"
			}
			goto collected
		case "done":
			// Turn over; fall through to the cap below.
			goto collected
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, proxy.UpstreamFailure(http.StatusBadGateway, "kimi-web: search stream: "+err.Error())
	}
collected:
	if streamErr != "" {
		return nil, proxy.UpstreamFailure(http.StatusBadGateway, "kimi-web: "+streamErr)
	}
	if maxResults > 0 && len(out.Results) > maxResults {
		out.Results = out.Results[:maxResults]
	}
	out.Answer = answer.String()
	return out, nil
}
