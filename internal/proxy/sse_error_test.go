package proxy

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
)

func errorFrame(t *testing.T, stream string) (sseErrorPayload, bool) {
	t.Helper()
	for _, line := range strings.Split(stream, "\n") {
		payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
		if !ok {
			continue
		}
		var frame sseErrorPayload
		if err := json.Unmarshal([]byte(strings.TrimSpace(payload)), &frame); err == nil && frame.Error.Message != "" {
			return frame, true
		}
	}
	return sseErrorPayload{}, false
}

// Issue #57: a stream that dies after HTTP 200 used to be closed silently, so
// clients kept the truncated text and reported success. The abort must arrive
// as a parseable in-band error frame instead (upstream decolua/9router 93001213).
func TestSSECopy_AbortEmitsInBandErrorFrame(t *testing.T) {
	const partial = "data: {\"choices\":[{\"delta\":{\"content\":\"the answer is\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" forty-two and\"}}]}\n\n"

	tests := []struct {
		name        string
		cause       error
		wantMessage string
		wantCode    string
	}{
		{
			name:        "socket died mid-turn reports upstream connection lost",
			cause:       errors.New("connection reset by peer"),
			wantMessage: "upstream connection lost",
			wantCode:    "upstream_error",
		},
		{
			name:        "stall watchdog reports a timeout, not a lost socket",
			cause:       ErrStreamStall,
			wantMessage: "stream stall timeout",
			wantCode:    "gateway_timeout",
		},
		{
			name:        "context cancellation is not a gateway failure",
			cause:       context.Canceled,
			wantMessage: "upstream request aborted",
			wantCode:    "request_aborted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &mockResponseWriter{}
			upstream := io.MultiReader(strings.NewReader(partial), iotest.ErrReader(tt.cause))
			if err := SSECopy(rec, upstream, rec, nil); err == nil {
				t.Fatal("expected the read error to be reported")
			}

			out := rec.String()
			if !strings.Contains(out, `"content":" forty-two and"`) {
				t.Fatalf("content streamed before the abort must survive, got %q", out)
			}
			frame, ok := errorFrame(t, out)
			if !ok {
				t.Fatalf("no in-band error frame in %q", out)
			}
			if frame.Error.Message != tt.wantMessage {
				t.Errorf("message = %q, want %q", frame.Error.Message, tt.wantMessage)
			}
			if frame.Error.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", frame.Error.Code, tt.wantCode)
			}
			// The client must not be told the turn finished.
			if strings.Contains(out, "finish_reason") {
				t.Errorf("abort fabricated a finish_reason: %q", out)
			}
			if !strings.HasSuffix(out, sseDoneFrame) {
				t.Errorf("stream must still close with [DONE], got %q", out)
			}
		})
	}
}

// TestBuildStreamErrorBytes_ClaudeUsesOverloadedError pins the Claude-lane
// error type. A mid-stream abort carries no HTTP status, and Claude Code only
// retries a streamed error event whose type is overloaded_error — the type
// Anthropic itself uses for transient failures. With server_error the run ends
// instead of retrying, so every upstream cut killed the turn.
//
// Upstream decolua/9router #5609.
func TestBuildStreamErrorBytes_ClaudeUsesOverloadedError(t *testing.T) {
	frame := string(BuildStreamErrorBytes(http.StatusBadGateway, "upstream connection lost", SSEFormatClaude))

	if !strings.HasPrefix(frame, "event: error\ndata: ") {
		t.Fatalf("Claude frame shape changed: %q", frame)
	}
	payload := strings.TrimPrefix(frame, "event: error\ndata: ")
	var decoded struct {
		Type  string         `json:"type"`
		Error sseErrorDetail `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(payload)), &decoded); err != nil {
		t.Fatalf("Claude frame is not JSON: %v (frame=%q)", err, frame)
	}
	if decoded.Type != "error" {
		t.Errorf("frame type = %q, want %q", decoded.Type, "error")
	}
	if decoded.Error.Type != "overloaded_error" {
		t.Errorf("error type = %q, want %q — Claude Code only retries this one",
			decoded.Error.Type, "overloaded_error")
	}
	// The message and the code stay: the type is the only thing that changes.
	if decoded.Error.Message != "upstream connection lost" {
		t.Errorf("message = %q, want %q", decoded.Error.Message, "upstream connection lost")
	}
	if decoded.Error.Code != "upstream_error" {
		t.Errorf("code = %q, want %q", decoded.Error.Code, "upstream_error")
	}
}

// TestBuildStreamErrorBytes_OpenAIKeepsServerError guards the other lanes: the
// overloaded_error type is a Claude-client convention and must not leak into
// the OpenAI frame, whose clients read server_error.
func TestBuildStreamErrorBytes_OpenAIKeepsServerError(t *testing.T) {
	frame := string(BuildStreamErrorBytes(http.StatusBadGateway, "upstream connection lost", SSEFormatOpenAI))

	if strings.Contains(frame, "overloaded_error") {
		t.Errorf("the OpenAI frame must keep server_error, got %q", frame)
	}
	parsed, ok := errorFrame(t, frame)
	if !ok {
		t.Fatalf("no in-band error frame in %q", frame)
	}
	if parsed.Error.Type != "server_error" {
		t.Errorf("error type = %q, want %q", parsed.Error.Type, "server_error")
	}
	if parsed.Error.Code != "upstream_error" {
		t.Errorf("code = %q, want %q", parsed.Error.Code, "upstream_error")
	}
	if !strings.HasSuffix(frame, sseDoneFrame) {
		t.Errorf("the OpenAI frame must still close with [DONE], got %q", frame)
	}
}
