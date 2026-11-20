package media

import (
	"strings"
	"testing"
	"time"
)

func TestValidateMiniMaxImageBody(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		body    string
		wantErr bool
	}{
		{"defaults", "MiniMax-H3-Image", `{"prompt":"an orange cat"}`, false},
		{"highspeed explicit size", "MiniMax-H3-Image-Highspeed", `{"prompt":"a cat","n":2,"size":"2720x1536","response_format":"url","output_format":"webp"}`, false},
		{"aspect and area", "MiniMax-H3-Image", `{"prompt":"a cat","aspect_ratio":"16:9","area":"2K"}`, false},
		{"enhancement disabled with aspect", "MiniMax-H3-Image", `{"prompt":"a cat","disable_prompt_enhanced":true,"aspect_ratio":"16:9"}`, false},
		{"empty prompt", "MiniMax-H3-Image", `{"prompt":"   "}`, true},
		{"oversized prompt", "MiniMax-H3-Image", `{"prompt":"` + strings.Repeat("x", 32001) + `"}`, true},
		{"n too small", "MiniMax-H3-Image", `{"prompt":"a cat","n":0}`, true},
		{"n too large", "MiniMax-H3-Image", `{"prompt":"a cat","n":11}`, true},
		{"bad size", "MiniMax-H3-Image", `{"prompt":"a cat","size":"large"}`, true},
		{"size area too small", "MiniMax-H3-Image", `{"prompt":"a cat","size":"512x512"}`, true},
		{"size with aspect", "MiniMax-H3-Image", `{"prompt":"a cat","size":"1024x1024","aspect_ratio":"16:9"}`, true},
		{"bad aspect", "MiniMax-H3-Image", `{"prompt":"a cat","aspect_ratio":"4:1"}`, true},
		{"enhancement disabled without aspect", "MiniMax-H3-Image", `{"prompt":"a cat","disable_prompt_enhanced":true}`, true},
		{"bad area", "MiniMax-H3-Image", `{"prompt":"a cat","area":"8K"}`, true},
		{"bad response format", "MiniMax-H3-Image", `{"prompt":"a cat","response_format":"b64"}`, true},
		{"bad output format", "MiniMax-H3-Image", `{"prompt":"a cat","output_format":"gif"}`, true},
		{"wrong model", "MiniMax-M3", `{"prompt":"a cat"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMiniMaxImageBody([]byte(tt.body), tt.model)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateMiniMaxImageBody() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsMiniMaxH3ImageRequest(t *testing.T) {
	if !isMiniMaxH3ImageRequest("mmg", "MiniMax-H3-Image", "/v1/images/generations") {
		t.Error("mmg alias should gate H3-Image on the image endpoint")
	}
	if isMiniMaxH3ImageRequest("minimax", "MiniMax-H3-Image", "/v1/images/generations") {
		t.Error("API-key minimax must not inherit the mmg H3-Image lane")
	}
	if isMiniMaxH3ImageRequest("mmg", "MiniMax-H3", "/v1/images/generations") {
		t.Error("video model must not use the image timeout")
	}
	if isMiniMaxH3ImageRequest("mmg", "MiniMax-H3-Image", "/audio/speech") {
		t.Error("non-image endpoint must not use the image gate")
	}
}

func TestMiniMaxImageHeaderTimeout(t *testing.T) {
	if miniMaxImageHeaderTimeout < 10*time.Minute {
		t.Fatalf("header timeout = %s, want at least 10m", miniMaxImageHeaderTimeout)
	}
}
