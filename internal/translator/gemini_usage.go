package translator

// GeminiUsageMetadata is the usage block a Gemini-native upstream reports.
//
// Gemini counts the reasoning slice SEPARATE from the visible output:
// candidatesTokenCount is the output, while the reasoning lives in
// candidatesTokenDetails.reasoningTokens (or thoughtsTokenCount on the
// Antigravity flavour). The total therefore covers prompt + output +
// reasoning, not prompt + output.
type GeminiUsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount,omitempty"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
	CachedContentToken      int `json:"cachedContentToken,omitempty"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount,omitempty"`
	CandidatesTokenDetails  *struct {
		ReasoningTokens int `json:"reasoningTokens"`
	} `json:"candidatesTokenDetails,omitempty"`
}

// geminiUsage holds the token counts of a Gemini-native response, already
// resolved into the Chat Completions vocabulary.
type geminiUsage struct {
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
	CachedTokens     int
	TotalTokens      int
}

// geminiUsageFromMetadata resolves a Gemini usage block into the Chat
// Completions counters.
//
// The reasoning slice is kept APART from the output count, because that is how
// Gemini reports it: candidatesTokenCount counts the visible answer, the
// reasoning is billed on top of it.
//
// The total is computed as prompt + output + reasoning, which is the figure
// that tells a downstream parser the additive reading is the right one: a
// parser that sees total == prompt + completion + reasoning knows the
// reasoning slice is NOT inside the output count, while the OpenAI families
// report a total of prompt + completion with the reasoning nested inside it
// (decolua/9router #5634). Upstream derives the same total from
// promptTokenCount + candidatesTokenCount + thoughtsTokenCount
// (open-sse/translator/concerns/usage.js).
func geminiUsageFromMetadata(m *GeminiUsageMetadata) geminiUsage {
	if m == nil {
		return geminiUsage{}
	}
	prompt := m.PromptTokenCount
	output := m.CandidatesTokenCount
	cached := m.CachedContentTokenCount
	if cached == 0 {
		cached = m.CachedContentToken
	}
	reasoning := 0
	if m.CandidatesTokenDetails != nil {
		reasoning = m.CandidatesTokenDetails.ReasoningTokens
	}
	if reasoning == 0 {
		reasoning = m.ThoughtsTokenCount
	}
	// Fallback: derive the output count from the reported total when the
	// upstream omits it (upstream parity).
	if output == 0 && m.TotalTokenCount > 0 {
		output = m.TotalTokenCount - prompt - reasoning
		if output < 0 {
			output = 0
		}
	}
	return geminiUsage{
		PromptTokens:     prompt,
		CompletionTokens: output,
		ReasoningTokens:  reasoning,
		CachedTokens:     cached,
		TotalTokens:      prompt + output + reasoning,
	}
}

// openAIUsageMap renders the counters as the OpenAI Chat Completions usage
// block.
//
// total_tokens is only emitted when there is one to report: a literal zero
// beside a non-zero prompt and completion count contradicts them.
func (u geminiUsage) openAIUsageMap() map[string]any {
	usage := map[string]any{
		"prompt_tokens":     u.PromptTokens,
		"completion_tokens": u.CompletionTokens,
		"cached_tokens":     u.CachedTokens,
		"completion_tokens_details": map[string]any{
			"reasoning_tokens": u.ReasoningTokens,
		},
	}
	if u.TotalTokens > 0 {
		usage["total_tokens"] = u.TotalTokens
	}
	return usage
}

// toOpenAIUsage converts the resolved counters into the canonical usage
// record. The marker stays false: Gemini reports the reasoning slice apart
// from the output, so pricing has to charge it on top.
func (u geminiUsage) toOpenAIUsage() *OpenAIUsage {
	return &OpenAIUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CachedTokens:     u.CachedTokens,
		CompletionTokensDetails: &CompletionTokensDetails{
			ReasoningTokens: u.ReasoningTokens,
		},
	}
}
