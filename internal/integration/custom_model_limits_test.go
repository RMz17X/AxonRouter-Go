//go:build integration

package integration

import (
	"net/http"
	"testing"

	"axonrouter/proxy/internal/auth"
)

// Issue #90 smoke: a custom model on an OpenAI-compatible provider node
// declares its real limits, and the discovery surfaces that carry custom
// models report those numbers instead of the substring table's guess.
// /v1/models/info is the one surface that does not: batch 10 made it
// Axon's static-catalogue lookup, which has never served custom models,
// so the info leg below pins Axon's 404 document instead of the declared
// numbers the pre-batch-10 Go document used to answer with. Runs through
// the real router, real middleware stack, and a real HTTP listener.
func TestCustomModelDeclaredLimitsReachEveryDiscoverySurface(t *testing.T) {
	env := newEnv(t)

	if _, err := env.Repo.CreateProviderNode("node-nara", "openai-compatible", "Nara AI",
		`{"prefix":"nara","apiType":"openai-compatible"}`); err != nil {
		t.Fatalf("create provider node: %v", err)
	}
	priority := 1
	if err := env.Repo.CreateProviderConnectionFull("conn-nara", "node-nara", "apikey", "Nara",
		&priority, `{"apiKey":"sk-nara","providerSpecificData":{"prefix":"nara"}}`); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	declared := `{"providerAlias":"node-nara","id":"my-custom-model","type":"llm","name":"my-custom-model","contextWindow":1000000,"maxOutput":32000}`
	if err := env.Repo.SetKV("customModels", "node-nara|my-custom-model|llm", declared); err != nil {
		t.Fatalf("save declared custom model: %v", err)
	}
	plain := `{"providerAlias":"node-nara","id":"plain-model","type":"llm","name":"plain-model"}`
	if err := env.Repo.SetKV("customModels", "node-nara|plain-model|llm", plain); err != nil {
		t.Fatalf("save plain custom model: %v", err)
	}

	type modelEntry struct {
		ID                  string `json:"id"`
		ContextLength       int    `json:"context_length"`
		MaxCompletionTokens int    `json:"max_completion_tokens"`
	}
	listed := env.Get(t, "/v1/models")
	if listed.Status != http.StatusOK {
		t.Fatalf("/v1/models status = %d: %s", listed.Status, truncate(listed.Body))
	}
	var catalog struct {
		Data []modelEntry `json:"data"`
	}
	listed.Decode(t, &catalog)

	byID := make(map[string]modelEntry, len(catalog.Data))
	for _, entry := range catalog.Data {
		byID[entry.ID] = entry
	}

	declaredEntry, ok := byID["nara/my-custom-model"]
	if !ok {
		t.Fatalf("nara/my-custom-model absent from /v1/models; got %d entries", len(catalog.Data))
	}
	if declaredEntry.ContextLength != 1000000 || declaredEntry.MaxCompletionTokens != 32000 {
		t.Errorf("/v1/models = (%d, %d), want the declared (1000000, 32000)",
			declaredEntry.ContextLength, declaredEntry.MaxCompletionTokens)
	}

	// A row saved before these fields existed keeps the table's answer.
	plainEntry, ok := byID["nara/plain-model"]
	if !ok {
		t.Fatalf("nara/plain-model absent from /v1/models; got %d entries", len(catalog.Data))
	}
	if plainEntry.ContextLength != 128000 || plainEntry.MaxCompletionTokens != 4096 {
		t.Errorf("undeclared model = (%d, %d), want the table fallback (128000, 4096)",
			plainEntry.ContextLength, plainEntry.MaxCompletionTokens)
	}

	// /v1/models/info answers from Axon's static catalogue only (batch
	// 10): a custom model is not in it, so the surface returns Axon's
	// 404 document verbatim rather than the declared limits.
	info := env.Get(t, "/v1/models/info?id=nara/my-custom-model")
	if info.Status != http.StatusNotFound {
		t.Fatalf("/v1/models/info status = %d, want Axon's 404 for a custom model: %s", info.Status, truncate(info.Body))
	}
	var infoBody struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	info.Decode(t, &infoBody)
	if infoBody.Error.Message != "Model not found: nara/my-custom-model" || infoBody.Error.Type != "not_found" {
		t.Errorf("/v1/models/info error = %+v, want Axon's not_found document for the custom model", infoBody.Error)
	}

	caps := env.Get(t, "/api/models/caps?provider=nara", WithHeader(auth.CLITokenHeader, auth.CLIToken()))
	if caps.Status != http.StatusOK {
		t.Fatalf("/api/models/caps status = %d: %s", caps.Status, truncate(caps.Body))
	}
	var capsBody struct {
		Caps map[string]struct {
			ContextWindow int `json:"contextWindow"`
			MaxOutput     int `json:"maxOutput"`
		} `json:"caps"`
	}
	caps.Decode(t, &capsBody)
	entry, ok := capsBody.Caps["my-custom-model"]
	if !ok {
		t.Fatalf("my-custom-model absent from /api/models/caps; got %d entries", len(capsBody.Caps))
	}
	if entry.ContextWindow != 1000000 || entry.MaxOutput != 32000 {
		t.Errorf("/api/models/caps = (%d, %d), want (1000000, 32000)",
			entry.ContextWindow, entry.MaxOutput)
	}
}
