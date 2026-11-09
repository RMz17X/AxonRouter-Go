package executor

import (
	"encoding/json"
	"net/http"
)

// ForwardTokenHarbor handles requests to TokenHarbor (tokenharbor.ai).
// It ensures stream_options are properly set for SSE streaming and delegates
// to standard OpenAI transport. Hedging lives in the chat handler, which
// races hedged waves of TokenHarbor candidates under an admission budget
// (see hedging.go): the free-tier GPU queue is global per model and a
// single ticket waits silently, so the client rides the fastest of many
// tickets and the ~30K-account pool absorbs the spent attempts.
func ForwardTokenHarbor(w http.ResponseWriter, req *Request) error {
	if req.IsStream {
		req.Body = ensureTokenHarborStreamOptions(req.Body)
	}
	return ForwardOpenAI(w, req)
}

// ensureTokenHarborStreamOptions injects stream_options: { include_usage: true }
// if not already present, ensuring usage token accounting functions properly over SSE.
func ensureTokenHarborStreamOptions(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	if _, exists := raw["stream_options"]; !exists {
		raw["stream_options"] = map[string]any{
			"include_usage": true,
		}
		if modified, err := json.Marshal(raw); err == nil {
			return modified
		}
	}
	return body
}
