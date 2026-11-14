package executor

import (
	"testing"
)

func TestDeepAIIslandHash(t *testing.T) {
	// Verify island hash calculation is non-empty and stable
	h1 := deepaiIslandHash("test-string-input")
	h2 := deepaiIslandHash("test-string-input")
	if h1 == "" {
		t.Fatalf("expected non-empty hash, got empty string")
	}
	if h1 != h2 {
		t.Fatalf("expected stable hash, got %q != %q", h1, h2)
	}

	key := deepaiGenerateKey(deepaiUA, "123456789012")
	if key == "" {
		t.Fatalf("expected non-empty key, got empty string")
	}
}

func TestDeepAIMapModel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"chat-standard", "chat_standard"},
		{"chat-genius", "chat_genius"},
		{"super-genius", "super_genius"},
		{"standard", "standard"},
		{"unknown-model", "chat_standard"},
	}

	for _, tt := range tests {
		got := deepaiMapModel(tt.input)
		if got != tt.expected {
			t.Errorf("deepaiMapModel(%q) = %q, expected %q", tt.input, got, tt.expected)
		}
	}
}
