package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/middleware"
	"axonrouter/proxy/internal/models"
	"github.com/google/uuid"
)

// playground.go serves the dashboard Playground: an operator chat surface
// that runs through the REAL serving path (HandleChatCompletions) instead of
// a parallel implementation, so proxy binding, guardrails, the stall gate
// and usage logging all apply exactly as they do to client traffic.
//
// Two things make that possible:
//
//   - Caller identity: the dashboard session has no API key, and the usage
//     ledger names its caller from the authenticated key on the context. The
//     playground therefore lends its traffic a real apiKeys row named
//     "playground" (playgroundPrincipal), minted once and reused. The row
//     carries no allowlist, which the access gate reads as unrestricted —
//     the same posture as any key minted before per-key model policy.
//
//   - Prompt decision: a playground turn may carry a session system prompt
//     or a chosen Persona preset, or explicitly decline the model's standing
//     Persona configuration. That decision rides the request context as a
//     playgroundPromptOverride and applyJailbreak honors it instead of the
//     standing resolver (see jailbreak.go), so injection mechanics are never
//     forked and the two prompt sources can never stack.

const playgroundKeyName = "playground"

// playgroundPromptContextKey carries the playground prompt decision.
type playgroundPromptContextKey struct{}

// playgroundPromptOverride is the prompt decision for one playground turn.
// A nil doc (or a doc without content) means "inject nothing" — which, by
// the override's mere presence, also suppresses the standing configuration.
type playgroundPromptOverride struct {
	doc *db.JailbreakDoc
}

func withPlaygroundPrompt(ctx context.Context, ov *playgroundPromptOverride) context.Context {
	return context.WithValue(ctx, playgroundPromptContextKey{}, ov)
}

func playgroundPromptFromContext(ctx context.Context) (*playgroundPromptOverride, bool) {
	if ctx == nil {
		return nil, false
	}
	ov, ok := ctx.Value(playgroundPromptContextKey{}).(*playgroundPromptOverride)
	return ov, ok
}

// playgroundChatRequest is the playground's own wire shape. It is translated
// into an ordinary OpenAI chat body before dispatch; nothing playground-
// specific reaches the serving path except the two context values above.
type playgroundChatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	SystemPrompt         string   `json:"systemPrompt"`
	PresetID             string   `json:"presetId"`
	ApplyStandingPersona *bool    `json:"applyStandingPersona"`
	Temperature          *float64 `json:"temperature"`
	MaxTokens            *int     `json:"maxTokens"`
	Stream               *bool    `json:"stream"`
}

// playgroundOverride resolves the request's prompt precedence: a session
// prompt wins, then a chosen preset, then — only when the operator turned
// the standing configuration off — a suppression-only override. A nil
// override means "no playground decision", and the standing configuration
// applies exactly as it would for real traffic.
func (h *ChatHandler) playgroundOverride(req *playgroundChatRequest) (*playgroundPromptOverride, error) {
	if strings.TrimSpace(req.SystemPrompt) != "" {
		return &playgroundPromptOverride{doc: &db.JailbreakDoc{
			Prompt:   req.SystemPrompt,
			Mode:     "single",
			Position: "prepend",
		}}, nil
	}
	if req.PresetID != "" {
		if h.Repo == nil {
			return nil, errors.New("preset store unavailable")
		}
		doc, err := h.Repo.GetJailbreakPreset(req.PresetID)
		if err != nil || doc == nil {
			return nil, fmt.Errorf("preset %q not found", req.PresetID)
		}
		return &playgroundPromptOverride{doc: doc}, nil
	}
	if req.ApplyStandingPersona != nil && !*req.ApplyStandingPersona {
		return &playgroundPromptOverride{}, nil
	}
	return nil, nil
}

// playgroundPrincipal loads the apiKeys row named "playground", minting it
// on first use, and caches it on the handler. The returned principal is
// placed on the request context under middleware.ApiKeyContextKey — the
// exact slot the auth middleware fills — so usage rows, the model-access
// gate and the guardrail key scope all see an ordinary caller.
func (h *ChatHandler) playgroundPrincipal() (*models.APIKey, error) {
	h.playgroundKeyMu.Lock()
	defer h.playgroundKeyMu.Unlock()
	if h.playgroundKey != nil {
		return h.playgroundKey, nil
	}
	if h.Repo == nil {
		return nil, errors.New("playground requires a repository")
	}
	keys, err := h.Repo.GetApiKeys()
	if err != nil {
		return nil, fmt.Errorf("playground key lookup: %w", err)
	}
	for _, k := range keys {
		if k == nil || k.Name == nil || *k.Name != playgroundKeyName {
			continue
		}
		if k.IsActive != 1 {
			return nil, errors.New("the playground API key is disabled")
		}
		h.playgroundKey = k
		return k, nil
	}
	id := uuid.New().String()
	secret := "sk-" + strings.ReplaceAll(uuid.New().String(), "-", "")
	if err := h.Repo.CreateApiKey(id, secret, playgroundKeyName, ""); err != nil {
		return nil, fmt.Errorf("playground key create: %w", err)
	}
	name := playgroundKeyName
	key := &models.APIKey{
		ID:        id,
		Key:       secret,
		Name:      &name,
		IsActive:  1,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	h.playgroundKey = key
	return key, nil
}

// HandlePlaygroundChat serves POST /api/playground/chat (dashboard session
// group). It translates the playground body into an OpenAI chat body and
// hands it to HandleChatCompletions on a clone of the caller's request, so
// the response — streamed SSE included — is the serving path's own, and a
// client abort cancels the same context the serving path already honors.
func (h *ChatHandler) HandlePlaygroundChat(w http.ResponseWriter, r *http.Request) {
	var req playgroundChatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing model")
		return
	}
	if len(req.Messages) == 0 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing messages")
		return
	}
	override, err := h.playgroundOverride(&req)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	principal, err := h.playgroundPrincipal()
	if err != nil {
		log.Error("playground", "caller identity unavailable", "error", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "playground identity unavailable")
		return
	}

	stream := true
	if req.Stream != nil {
		stream = *req.Stream
	}
	outBody := map[string]any{
		"model":  req.Model,
		"stream": stream,
	}
	messages := make([]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, map[string]any{"role": m.Role, "content": m.Content})
	}
	outBody["messages"] = messages
	if req.Temperature != nil {
		outBody["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		outBody["max_tokens"] = *req.MaxTokens
	}
	raw, err := json.Marshal(outBody)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}

	ctx := context.WithValue(r.Context(), middleware.ApiKeyContextKey, principal)
	if override != nil {
		ctx = withPlaygroundPrompt(ctx, override)
	}
	serveReq := r.Clone(ctx)
	serveReq.Body = io.NopCloser(bytes.NewReader(raw))
	serveReq.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(raw)), nil
	}
	serveReq.ContentLength = int64(len(raw))
	serveReq.Header = r.Header.Clone()
	serveReq.Header.Set("Content-Type", "application/json")
	serveReq.Header.Del("Content-Length")

	// The serving path keeps its request id in logs and usage details but
	// does not echo it to /v1 clients; the playground is an operator surface,
	// so surface it — the reply's meta line and the Usage trail join on it.
	if rid := middleware.GetRequestIDFromContext(ctx); rid != "" {
		w.Header().Set("X-Request-ID", rid)
	}
	h.HandleChatCompletions(w, serveReq)
}
