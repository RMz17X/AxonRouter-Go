package media

// Batch 6 — POST /v1/task, ported from AxonRouter src/app/api/v1/task/
// route.js + src/sse/handlers/task.js + open-sse/handlers/taskProviders.
// Axon's task lane runs non-chat capability models outside the chat
// pipeline; its adapter registry has exactly one provider,
// cloudflare-ai (Cloudflare Workers AI), with per-model field specs.
// The port mirrors the handler's validation order, error bodies
// (buildErrorBody: {error:{message, type, code}} from ERROR_TYPES),
// credential rotation (no fallback on 400/404/413/422/499 per
// markAccountUnavailable; MAX_FALLBACK_ATTEMPTS = 10) and result shape
// {object:"task.result", model, result}. Provider base URL: Axon reads
// it from the provider's taskConfig constant, which carries no override
// for cloudflare-ai, so the adapter default applies.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
)

const cloudflareTaskBaseURL = "https://api.cloudflare.com/client/v4/accounts"
const maxTaskFallbackAttempts = 10

type taskModelSpec struct {
	required []string
	optional []string
}

var cloudflareTaskSpecs = map[string]taskModelSpec{
	"@cf/llava-hf/llava-1.5-7b-hf":          {required: []string{"image"}, optional: []string{"prompt"}},
	"@cf/microsoft/resnet-50":               {required: []string{"image"}},
	"@cf/huggingface/distilbert-sst-2-int8": {required: []string{"text"}},
	"@cf/ai4bharat/indictrans2-en-indic-1B": {required: []string{"text", "target_language"}},
	"@cf/meta/m2m100-1.2b":                  {required: []string{"text", "target_lang"}},
	"@cf/baai/bge-reranker-base":            {required: []string{"query", "contexts"}},
	"@cf/pipecat-ai/smart-turn-v2":          {required: []string{"audio"}},
}

// taskErrorTypes mirrors open-sse/config/errorConfig.js ERROR_TYPES.
var taskErrorTypes = map[int][2]string{
	400: {"invalid_request_error", "bad_request"},
	401: {"authentication_error", "invalid_api_key"},
	402: {"billing_error", "payment_required"},
	403: {"permission_error", "insufficient_quota"},
	404: {"invalid_request_error", "model_not_found"},
	406: {"invalid_request_error", "model_not_supported"},
	429: {"rate_limit_error", "rate_limit_exceeded"},
	500: {"server_error", "internal_server_error"},
	502: {"server_error", "bad_gateway"},
	503: {"server_error", "service_unavailable"},
	504: {"server_error", "gateway_timeout"},
}

// writeTaskError writes Axon's buildErrorBody shape with CORS wildcard,
// as errorResponse does.
func writeTaskError(w http.ResponseWriter, status int, message string) {
	errType, errCode := "invalid_request_error", ""
	if status >= 500 {
		errType, errCode = "server_error", "internal_server_error"
	}
	if pair, ok := taskErrorTypes[status]; ok {
		errType, errCode = pair[0], pair[1]
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	out, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"code":    errCode,
		},
	})
	_, _ = w.Write(out)
}

// taskShouldFallback mirrors markAccountUnavailable's client-error set.
func taskShouldFallback(status int) bool {
	switch status {
	case 400, 404, 413, 422, 499:
		return false
	}
	return true
}

// HandleTask serves POST /v1/task (relative "/task" registration).
func (h *MediaHandler) HandleTask(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		writeTaskError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if !json.Valid(rawBody) {
		writeTaskError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	var body map[string]any
	if err := json.Unmarshal(rawBody, &body); err != nil {
		writeTaskError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	modelStr, _ := body["model"].(string)
	if modelStr == "" {
		writeTaskError(w, http.StatusBadRequest, "Missing model")
		return
	}
	slash := strings.Index(modelStr, "/")
	if slash < 0 {
		writeTaskError(w, http.StatusBadRequest, "Invalid model format")
		return
	}
	provider, model := modelStr[:slash], modelStr[slash+1:]
	if provider != "cloudflare-ai" {
		writeTaskError(w, http.StatusBadRequest, fmt.Sprintf("Provider '%s' does not serve task models", provider))
		return
	}
	spec, ok := cloudflareTaskSpecs[model]
	if !ok {
		writeTaskError(w, http.StatusBadRequest, fmt.Sprintf("Model '%s' is not a task model", model))
		return
	}
	taskBody, buildErr := buildTaskBody(spec, body)
	if buildErr != "" {
		writeTaskError(w, http.StatusBadRequest, buildErr)
		return
	}
	taskJSON, err := json.Marshal(taskBody)
	if err != nil {
		writeTaskError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var conns []struct {
		data string
	}
	if h.Repo != nil {
		dbConns, cerr := h.Repo.GetProviderConnections("cloudflare-ai", true)
		if cerr == nil {
			for _, c := range dbConns {
				if c != nil {
					conns = append(conns, struct{ data string }{data: c.Data})
				}
			}
		}
	}
	if len(conns) == 0 {
		writeTaskError(w, http.StatusBadRequest, "No credentials for provider: cloudflare-ai")
		return
	}

	lastStatus := 0
	lastError := ""
	attempts := 0
	allRateLimited := true
	for _, conn := range conns {
		if attempts >= maxTaskFallbackAttempts {
			writeTaskError(w, statusOr(lastStatus, http.StatusServiceUnavailable),
				fmt.Sprintf("Max fallback attempts (%d) reached: %s", maxTaskFallbackAttempts, lastError))
			return
		}
		attempts++

		var connData struct {
			APIKey               string         `json:"apiKey"`
			AccessToken          string         `json:"accessToken"`
			AccountID            string         `json:"accountId"`
			ProviderSpecificData map[string]any `json:"providerSpecificData"`
		}
		if conn.data != "" {
			_ = json.Unmarshal([]byte(conn.data), &connData)
		}
		apiKey := connData.APIKey
		if apiKey == "" {
			apiKey = connData.AccessToken
		}
		if apiKey == "" {
			lastStatus, lastError = http.StatusUnauthorized, "cloudflare-ai API key required"
			allRateLimited = false
			continue
		}
		accountID := ""
		if v, ok := connData.ProviderSpecificData["accountId"].(string); ok {
			accountID = v
		}
		if accountID == "" {
			accountID = connData.AccountID
		}
		if accountID == "" {
			// 400 is in the no-fallback set: Axon surfaces it directly.
			writeTaskError(w, http.StatusBadRequest, "cloudflare-ai requires accountId in providerSpecificData")
			return
		}

		upstreamURL := cloudflareTaskBaseURL + "/" + accountID + "/ai/run/" + model
		result, failStatus, failMessage := h.runCloudflareTask(r, upstreamURL, apiKey, taskJSON)
		if failMessage == "" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.WriteHeader(http.StatusOK)
			out, _ := json.Marshal(map[string]any{
				"object": "task.result",
				"model":  model,
				"result": result,
			})
			_, _ = w.Write(out)
			return
		}
		lastStatus, lastError = failStatus, failMessage
		if failStatus != http.StatusTooManyRequests {
			allRateLimited = false
		}
		if !taskShouldFallback(failStatus) {
			break
		}
	}

	if allRateLimited && attempts > 0 && lastStatus != 0 {
		// Axon's unavailableResponse for the all-rate-limited case.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(lastStatus)
		out, _ := json.Marshal(map[string]any{
			"error": map[string]any{
				"message":  fmt.Sprintf("[cloudflare-ai/%s] %s", model, lastError),
				"type":     "service_unavailable",
				"code":     "SERVICE_UNAVAILABLE",
				"provider": "cloudflare-ai",
				"model":    model,
			},
		})
		_, _ = w.Write(out)
		return
	}
	if lastError == "" {
		lastError = "All accounts unavailable"
	}
	writeTaskError(w, statusOr(lastStatus, http.StatusServiceUnavailable), lastError)
}

func statusOr(status, fallback int) int {
	if status == 0 {
		return fallback
	}
	return status
}

// runCloudflareTask performs one adapter attempt. On success it
// returns the result value with an empty fail pair; on failure a
// (status, message) pair in Axon's adapter fail() terms.
func (h *MediaHandler) runCloudflareTask(r *http.Request, upstreamURL, apiKey string, taskJSON []byte) (any, int, string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(taskJSON))
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Sprintf("Cloudflare task request failed: %s", err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Sprintf("Cloudflare task request failed: %s", err.Error())
	}
	defer resp.Body.Close()
	rawResp, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, cloudflareErrorMessage(rawResp, resp.StatusCode)
	}
	var parsed map[string]any
	if err := json.Unmarshal(rawResp, &parsed); err != nil {
		return nil, http.StatusBadGateway, "Cloudflare task returned an empty result"
	}
	if success, ok := parsed["success"].(bool); ok && !success {
		msg := cloudflareErrorsMessage(parsed)
		if msg == "" {
			msg = "Cloudflare task returned success:false"
		}
		return nil, http.StatusBadGateway, msg
	}
	result, has := parsed["result"]
	if !has || result == nil {
		// Axon falls back to the whole body when result is absent, and
		// fails only when that is empty too.
		if len(parsed) == 0 {
			return nil, http.StatusBadGateway, "Cloudflare task returned an empty result"
		}
		return parsed, 0, ""
	}
	return result, 0, ""
}

// cloudflareErrorMessage extracts errors[0].message || error.message ||
// message from an upstream error body, falling back to the raw body
// (sliced) and finally to a synthesised message.
func cloudflareErrorMessage(raw []byte, status int) string {
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if msg := cloudflareErrorsMessage(parsed); msg != "" {
			return msg
		}
		if e, ok := parsed["error"].(map[string]any); ok {
			if msg, ok := e["message"].(string); ok && msg != "" {
				return msg
			}
		}
		if msg, ok := parsed["message"].(string); ok && msg != "" {
			return msg
		}
	}
	text := strings.TrimSpace(string(raw))
	if text != "" {
		if len(text) > 300 {
			text = text[:300]
		}
		return text
	}
	return fmt.Sprintf("Cloudflare task error (%d)", status)
}

func cloudflareErrorsMessage(parsed map[string]any) string {
	errs, ok := parsed["errors"].([]any)
	if !ok || len(errs) == 0 {
		return ""
	}
	if first, ok := errs[0].(map[string]any); ok {
		if msg, ok := first["message"].(string); ok {
			return msg
		}
	}
	return ""
}

// buildTaskBody validates and converts the request fields per model
// spec, mirroring the adapter's buildTaskBody: required fields must be
// present and non-empty; image becomes a byte array; audio stays a
// base64 string with any data-URI prefix stripped; optional fields are
// forwarded only when present and non-empty.
func buildTaskBody(spec taskModelSpec, body map[string]any) (map[string]any, string) {
	out := make(map[string]any)
	convert := func(field string, value any) (any, string) {
		switch field {
		case "image":
			return convertTaskImage(value)
		case "audio":
			if s, ok := value.(string); ok {
				if i := strings.Index(s, ","); i >= 0 && strings.HasPrefix(s, "data:") {
					return s[i+1:], ""
				}
				return s, ""
			}
			return value, ""
		}
		return value, ""
	}
	for _, field := range spec.required {
		value, present := body[field]
		if !present || value == nil || value == "" {
			return nil, fmt.Sprintf("Missing required field: %s", field)
		}
		converted, errStr := convert(field, value)
		if errStr != "" {
			return nil, errStr
		}
		out[field] = converted
	}
	for _, field := range spec.optional {
		value, present := body[field]
		if !present || value == nil || value == "" {
			continue
		}
		converted, errStr := convert(field, value)
		if errStr != "" {
			return nil, errStr
		}
		out[field] = converted
	}
	return out, ""
}

// convertTaskImage turns a base64 string or an int array into the byte
// array Cloudflare expects.
func convertTaskImage(value any) (any, string) {
	switch v := value.(type) {
	case []any:
		bytesOut := make([]int, 0, len(v))
		for _, item := range v {
			f, ok := item.(float64)
			if !ok || f != math.Trunc(f) || f < 0 || f > 255 {
				return nil, "image array must contain integers 0-255"
			}
			bytesOut = append(bytesOut, int(f))
		}
		return bytesOut, ""
	case string:
		s := v
		if i := strings.Index(s, "base64,"); i >= 0 {
			s = s[i+len("base64,"):]
		}
		decoded, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(s)
			if err != nil {
				return nil, "image is not valid base64"
			}
		}
		bytesOut := make([]int, 0, len(decoded))
		for _, b := range decoded {
			bytesOut = append(bytesOut, int(b))
		}
		return bytesOut, ""
	}
	return nil, "image must be a base64 string or an int array"
}
