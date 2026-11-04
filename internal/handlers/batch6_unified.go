package handlers

// Batch 6 — POST /v1/unified and POST /v1/video/generations, ported
// from AxonRouter src/app/api/v1/unified/route.ts and
// src/app/api/v1/video/generations/route.ts (both .ts-only routes).
//
// unified: validate the modality envelope ({error, code} 400s), then
// dispatch the ORIGINAL request (headers + body verbatim, "mode" field
// included — targets ignore it) to the modality's engine path on this
// same server, and wrap a JSON answer in {mode, provider, model,
// usage, correlation_id, output, data} with x-correlation-id /
// x-axonrouter-mode headers. Non-JSON answers pass through with an
// x-axonrouter-output-kind header. Axon additionally writes a
// request-detail record for the unified endpoint itself; the port does
// not — the modality sub-request is usage-logged by the gateway like
// any other /v1 call (deviation recorded in .changes/917).
//
// video/generations: Axon's route is a baseline — it deliberately does
// not generate. It validates and answers an "accepted" envelope. Both
// routes sit in Go's engine group, so client-key enforcement is the
// gateway-wide RequireApiKey (Axon self-checks only when keys are
// configured — global delta recorded in .changes/917).

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func setUnifiedCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
}

func writeUnifiedError(w http.ResponseWriter, status int, message, code string) {
	setUnifiedCORS(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	out, _ := json.Marshal(map[string]any{"error": message, "code": code})
	_, _ = w.Write(out)
}

type videoGenerationAccepted struct {
	Object  string `json:"object"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Prompt  string `json:"prompt"`
	Model   string `json:"model"`
}

type videoGenerationsResponse struct {
	Created int64                     `json:"created"`
	Data    []videoGenerationAccepted `json:"data"`
}

// HandleVideoGenerationsBaseline serves POST /video/generations
// (Axon: POST /v1/video/generations, singular "video").
func HandleVideoGenerationsBaseline(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(r.Body)
	if err != nil || !json.Valid(rawBody) {
		writeUnifiedError(w, http.StatusBadRequest, "Invalid JSON body", "invalid_json")
		return
	}
	var body map[string]any
	if err := json.Unmarshal(rawBody, &body); err != nil {
		writeUnifiedError(w, http.StatusBadRequest, "Invalid JSON body", "invalid_json")
		return
	}
	model, _ := body["model"].(string)
	if model == "" {
		writeUnifiedError(w, http.StatusBadRequest, "Missing required field: model", "missing_model")
		return
	}
	prompt, _ := body["prompt"].(string)
	if prompt == "" {
		writeUnifiedError(w, http.StatusBadRequest, "Missing required field: prompt", "missing_prompt")
		return
	}
	setUnifiedCORS(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	out, _ := json.Marshal(videoGenerationsResponse{
		Created: time.Now().Unix(),
		Data: []videoGenerationAccepted{{
			Object:  "video.generation",
			Status:  "accepted",
			Message: "Video generation baseline route accepted the request.",
			Prompt:  prompt,
			Model:   model,
		}},
	})
	_, _ = w.Write(out)
}

var unifiedModeTargets = map[string]string{
	"text":  "/v1/chat/completions",
	"image": "/v1/images/generations",
	"audio": "/v1/audio/speech",
	"video": "/v1/video/generations",
}

const base36Digits = "0123456789abcdefghijklmnopqrstuvwxyz"

func randomBase36(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("0", n)
	}
	for i, b := range buf {
		buf[i] = base36Digits[int(b)%len(base36Digits)]
	}
	return string(buf)
}

func newCorrelationID() string {
	return "rt_" + strconv.FormatInt(time.Now().UnixMilli(), 36) + "_" + randomBase36(8)
}

// HandleUnified serves POST /unified (Axon: POST /v1/unified).
func HandleUnified(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(r.Body)
	if err != nil || !json.Valid(rawBody) {
		writeUnifiedError(w, http.StatusBadRequest, "Invalid JSON body", "invalid_json")
		return
	}
	var body map[string]any
	if err := json.Unmarshal(rawBody, &body); err != nil {
		writeUnifiedError(w, http.StatusBadRequest, "Invalid JSON body", "invalid_json")
		return
	}

	mode := "text"
	if m, ok := body["mode"].(string); ok {
		mode = m
	}
	targetPath, ok := unifiedModeTargets[mode]
	if !ok {
		writeUnifiedError(w, http.StatusBadRequest, "Unsupported mode: "+mode, "unsupported_mode")
		return
	}
	model, _ := body["model"].(string)
	if model == "" {
		writeUnifiedError(w, http.StatusBadRequest, "Missing required field: model", "missing_model")
		return
	}
	switch mode {
	case "text":
		_, hasMessages := body["messages"].([]any)
		_, hasInput := body["input"].([]any)
		if !hasMessages && !hasInput {
			writeUnifiedError(w, http.StatusBadRequest, "Text mode requires messages or input", "missing_messages")
			return
		}
	case "image":
		if prompt, _ := body["prompt"].(string); prompt == "" {
			writeUnifiedError(w, http.StatusBadRequest, "Image mode requires prompt", "missing_prompt")
			return
		}
	case "audio":
		if input, _ := body["input"].(string); input == "" {
			writeUnifiedError(w, http.StatusBadRequest, "Audio mode requires input", "missing_input")
			return
		}
	case "video":
		if prompt, _ := body["prompt"].(string); prompt == "" {
			writeUnifiedError(w, http.StatusBadRequest, "Video mode requires prompt", "missing_prompt")
			return
		}
	}

	correlationID := newCorrelationID()
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	}
	targetURL := scheme + "://" + r.Host + targetPath

	subReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(rawBody))
	if err != nil {
		writeUnifiedError(w, http.StatusInternalServerError, "Internal Server Error", "internal_error")
		return
	}
	subReq.Header = r.Header.Clone()
	subReq.Header.Del("Content-Length")
	subReq.ContentLength = int64(len(rawBody))

	resp, err := http.DefaultClient.Do(subReq)
	if err != nil {
		writeUnifiedError(w, http.StatusInternalServerError, "Internal Server Error", "internal_error")
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeUnifiedError(w, http.StatusInternalServerError, "Internal Server Error", "internal_error")
		return
	}

	for k, values := range resp.Header {
		if http.CanonicalHeaderKey(k) == "Content-Length" {
			continue
		}
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	setUnifiedCORS(w)
	w.Header().Set("X-Correlation-Id", correlationID)
	w.Header().Set("X-Axonrouter-Mode", mode)

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		var payload map[string]any
		if err := json.Unmarshal(respBody, &payload); err == nil && payload != nil {
			provider := any(nil)
			if p, ok := payload["provider"].(string); ok && p != "" {
				provider = p
			} else if pm, ok := payload["model"].(string); ok && strings.Contains(pm, "/") {
				provider = pm[:strings.Index(pm, "/")]
			}
			modelOut := any(nil)
			if pm, ok := payload["model"].(string); ok && pm != "" {
				modelOut = pm
			} else {
				modelOut = model
			}
			envelope := map[string]any{
				"mode":           mode,
				"provider":       provider,
				"model":          modelOut,
				"usage":          payload["usage"],
				"correlation_id": correlationID,
				"output":         payload,
			}
			if data, present := payload["data"]; present {
				envelope["data"] = data
			}
			w.WriteHeader(resp.StatusCode)
			out, _ := json.Marshal(envelope)
			_, _ = w.Write(out)
			return
		}
		if err == nil && payload == nil {
			// JSON null / non-object: Axon answers the bare envelope.
			w.WriteHeader(resp.StatusCode)
			out, _ := json.Marshal(map[string]any{
				"mode":           mode,
				"correlation_id": correlationID,
				"output":         nil,
			})
			_, _ = w.Write(out)
			return
		}
	}

	// Non-JSON (or unparseable) answer: pass the body through with the
	// output-kind marker.
	outputKind := "binary"
	if mode == "audio" {
		outputKind = "binary-audio"
	}
	w.Header().Set("X-Axonrouter-Output-Kind", outputKind)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}
