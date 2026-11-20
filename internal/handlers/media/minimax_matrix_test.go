package media

import (
	"encoding/json"
	"testing"
)

func TestMiniMaxMatrixBizError(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantMsg    string
		wantOK     bool
	}{
		{"success envelope", `{"code":0,"message":"ok","task_id":"t1"}`, 0, "", false},
		{"balance refusal", `{"code":402,"message":"remaining M Plan weekly limit"}`, 402, "remaining M Plan weekly limit", true},
		{"base_resp refusal", `{"code":0,"message":"","base_resp":{"status_code":402,"status_message":"no credit"}}`, 402, "no credit", true},
		{"rate limit", `{"code":429,"message":"slow down"}`, 429, "slow down", true},
		{"unknown code", `{"code":500,"message":"upstream exploded"}`, 502, "upstream exploded", true},
		{"no envelope", `{"task_id":"t1"}`, 0, "", false},
		{"task submitted message", `{"code":0,"message":"video task submitted; call the query tool with this task_id and model","task_id":"t1"}`, 0, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, msg, ok := miniMaxMatrixBizError([]byte(tt.body))
			if ok != tt.wantOK || status != tt.wantStatus || msg != tt.wantMsg {
				t.Fatalf("miniMaxMatrixBizError() = (%d, %q, %v), want (%d, %q, %v)", status, msg, ok, tt.wantStatus, tt.wantMsg, tt.wantOK)
			}
		})
	}
}

func TestBuildMiniMaxMatrixImageBody(t *testing.T) {
	raw := []byte(`{"model":"MiniMax-H3-Image","prompt":"a cat","n":2,"size":"1024x768"}`)
	out, err := buildMiniMaxMatrixImageBody(raw)
	if err != nil {
		t.Fatalf("buildMiniMaxMatrixImageBody: %v", err)
	}
	var req miniMaxMatrixImageRequest
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(req.Requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(req.Requests))
	}
	for i, item := range req.Requests {
		if item.Prompt != "a cat" || item.AspectRatio != "4:3" || item.Resolution != miniMaxMatrixResolution1K {
			t.Errorf("request %d = %+v", i, item)
		}
		if want := "slot_" + string(rune('0'+i)) + ".png"; item.OutputFile != want {
			t.Errorf("output_file = %q, want %q", item.OutputFile, want)
		}
	}
}

func TestTranslateMiniMaxMatrixImageResult(t *testing.T) {
	status, body := translateMiniMaxMatrixImageResult([]byte(`{"code":0,"message":"ok","success_items":[{"output_url":"https://cdn.example/a.png","size":"1024x1024","revised_prompt":"a fluffy cat"}],"failed_items":[]}`))
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	var result miniMaxMatrixImageResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Data) != 1 || result.Data[0].URL != "https://cdn.example/a.png" || result.Data[0].RevisedPrompt != "a fluffy cat" {
		t.Fatalf("result = %+v", result)
	}
	if result.Created == 0 {
		t.Error("created must be set")
	}
}

func TestMiniMaxMatrixVideoModel(t *testing.T) {
	tests := map[string]string{
		"MiniMax-H3":         miniMaxMatrixH3Model,
		"MiniMax-H3-Max":     miniMaxMatrixH3Model,
		"MiniMax-Hailuo-2.3": miniMaxMatrixHailuoModel,
	}
	for in, want := range tests {
		if got := miniMaxMatrixVideoModel(in); got != want {
			t.Errorf("miniMaxMatrixVideoModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTranslateMiniMaxMatrixImageResultBizError(t *testing.T) {
	status, body := translateMiniMaxMatrixImageResult([]byte(`{"code":402,"message":"insufficient credits"}`))
	if status != 402 {
		t.Fatalf("status = %d, want 402", status)
	}
	if !json.Valid(body) {
		t.Fatalf("invalid JSON error body: %s", body)
	}
}

func TestMiniMaxMatrixVideoResolutionPromotion(t *testing.T) {
	tests := map[string]string{
		"480P": "768P",
		"768P": "768P",
		"2K":   "2K",
	}
	for in, want := range tests {
		if got := miniMaxMatrixVideoResolution(in); got != want {
			t.Errorf("miniMaxMatrixVideoResolution(%q) = %q, want %q", in, got, want)
		}
	}
}
