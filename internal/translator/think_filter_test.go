package translator

import (
	"strings"
	"testing"
)

func TestExtractAndStripThink(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantThinking string
		wantContent  string
	}{
		{
			name:         "no think tag",
			input:        "Hello world",
			wantThinking: "",
			wantContent:  "Hello world",
		},
		{
			name:         "standard think block",
			input:        "<think>pondering 1+1</think>\n\n2",
			wantThinking: "pondering 1+1",
			wantContent:  "2",
		},
		{
			name:         "unclosed think tag",
			input:        "Prefix <think>pondering indefinitely",
			wantThinking: "pondering indefinitely",
			wantContent:  "Prefix ",
		},
		{
			name:         "multiple think blocks",
			input:        "<think>step 1</think>intermediate <think>step 2</think>final answer",
			wantThinking: "step 1\nstep 2",
			wantContent:  "intermediate final answer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotThinking, gotContent := ExtractAndStripThink(tt.input)
			if gotThinking != tt.wantThinking {
				t.Errorf("thinking = %q, want %q", gotThinking, tt.wantThinking)
			}
			if gotContent != tt.wantContent {
				t.Errorf("content = %q, want %q", gotContent, tt.wantContent)
			}
		})
	}
}

func TestThinkStreamFilter_Chunks(t *testing.T) {
	f := NewThinkStreamFilter()

	// Chunk 1: starts thinking
	th1, c1 := f.Feed("<think>I need to solve ")
	if th1 != "I need to solve " || c1 != "" {
		t.Errorf("chunk 1 mismatch: th=%q, c=%q", th1, c1)
	}
	if !f.InThinking() {
		t.Errorf("expected InThinking=true")
	}

	// Chunk 2: continues thinking
	th2, c2 := f.Feed("strawberry")
	if th2 != "strawberry" || c2 != "" {
		t.Errorf("chunk 2 mismatch: th=%q, c=%q", th2, c2)
	}

	// Chunk 3: closes thinking and starts content
	th3, c3 := f.Feed("</think>\n\nThere are 3 r's.")
	if th3 != "" || c3 != "There are 3 r's." {
		t.Errorf("chunk 3 mismatch: th=%q, c=%q", th3, c3)
	}
	if f.InThinking() {
		t.Errorf("expected InThinking=false")
	}

	// Chunk 4: normal content
	th4, c4 := f.Feed(" Done!")
	if th4 != "" || c4 != " Done!" {
		t.Errorf("chunk 4 mismatch: th=%q, c=%q", th4, c4)
	}
}

func TestThinkStreamFilter_TagSplitAcrossChunks(t *testing.T) {
	f := NewThinkStreamFilter()

	// Tag "<think>" split across chunks: "<th" then "ink>hello"
	th1, c1 := f.Feed("start <th")
	if th1 != "" || c1 != "start " {
		t.Errorf("chunk 1 mismatch: th=%q, c=%q", th1, c1)
	}

	th2, c2 := f.Feed("ink>my thought</th")
	if th2 != "my thought" || c2 != "" {
		t.Errorf("chunk 2 mismatch: th=%q, c=%q", th2, c2)
	}

	th3, c3 := f.Feed("ink>clean answer")
	if th3 != "" || c3 != "clean answer" {
		t.Errorf("chunk 3 mismatch: th=%q, c=%q", th3, c3)
	}
}

func TestThinkStreamFilter_FilterSSEPayload(t *testing.T) {
	f := NewThinkStreamFilter()

	// 1. Dahl MiniMax style: <think> in delta.content
	chunk1 := []byte(`{"choices":[{"index":0,"delta":{"role":"assistant","content":"<think>analyzing</think>\n\n2."}}]}`)
	patched1, changed1 := f.FilterSSEPayload(chunk1)
	if !changed1 {
		t.Fatalf("expected chunk 1 to be patched")
	}
	pStr1 := string(patched1)
	if !strings.Contains(pStr1, `"reasoning_content":"analyzing"`) {
		t.Errorf("reasoning_content missing in patched chunk 1: %s", pStr1)
	}
	if !strings.Contains(pStr1, `"content":"2."`) {
		t.Errorf("content incorrect in patched chunk 1: %s", pStr1)
	}
	if strings.Contains(pStr1, "<think>") || strings.Contains(pStr1, "</think>") {
		t.Errorf("leak detected: think tag preserved in payload: %s", pStr1)
	}

	// 2. Dahl DeepSeek style: "reasoning" alias
	f2 := NewThinkStreamFilter()
	chunk2 := []byte(`{"choices":[{"index":0,"delta":{"reasoning":"step 1"}}]}`)
	patched2, changed2 := f2.FilterSSEPayload(chunk2)
	if !changed2 {
		t.Fatalf("expected chunk 2 to be patched")
	}
	pStr2 := string(patched2)
	if !strings.Contains(pStr2, `"reasoning_content":"step 1"`) {
		t.Errorf("reasoning alias not lifted in chunk 2: %s", pStr2)
	}
}

func TestNormalizeOpenAIResponseJSON(t *testing.T) {
	// 1. MiniMax with <think> tag
	body1 := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"<think>thinking</think>\n\nHello"}}]}`)
	patched1, changed1 := NormalizeOpenAIResponseJSON(body1)
	if !changed1 {
		t.Fatalf("expected non-stream body 1 to be patched")
	}
	pStr1 := string(patched1)
	if !strings.Contains(pStr1, `"reasoning_content":"thinking"`) {
		t.Errorf("reasoning_content missing: %s", pStr1)
	}
	if !strings.Contains(pStr1, `"content":"Hello"`) {
		t.Errorf("content incorrect: %s", pStr1)
	}

	// 2. DeepSeek with reasoning alias
	body2 := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"Hello","reasoning":"thought"}}]}`)
	patched2, changed2 := NormalizeOpenAIResponseJSON(body2)
	if !changed2 {
		t.Fatalf("expected non-stream body 2 to be patched")
	}
	pStr2 := string(patched2)
	if !strings.Contains(pStr2, `"reasoning_content":"thought"`) {
		t.Errorf("reasoning_content missing from alias: %s", pStr2)
	}
}
