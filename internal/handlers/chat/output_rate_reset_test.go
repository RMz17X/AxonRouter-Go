package chat

import (
	"testing"
	"time"
)

// The two regressions below are the review findings on the output-generation
// instrumentation (review-batch-a-20261113.md, findings M1 and M2).
//
// M1: when a /v1/responses client is served by a Gemini model, the chunk
// reaches the capture buffer twice — once by the Gemini lane and once by the
// Responses bridge that translates it. The buffer then holds every chunk
// twice, so the logged response body is doubled and the memory it holds is
// doubled for no reason.
//
// M2: the reactive OAuth refresh retries the request in place with the same
// metrics value. Without a reset the second attempt inherits the first
// attempt's buffer, token count and gap stamps, so the reported rate blends
// two attempts and the reported max gap swallows the refresh pause.

// TestStreamMetricsResetClearsEveryAttempt proves the reset drops the state
// the first attempt left behind: the captured body, the token count, the
// first/last token stamps and the widest gap.
func TestStreamMetricsResetClearsEveryAttempt(t *testing.T) {
	m := &streamMetrics{}
	m.SetOutputTokens(42)
	m.ResponseBuf.Write([]byte("first attempt body"))
	start := time.Now()
	m.NoteOutput(start)
	m.NoteOutput(start.Add(3 * time.Second))

	if m.OutputTokenCount() != 42 {
		t.Fatalf("precondition: token count = %d, want 42", m.OutputTokenCount())
	}
	if m.MaxGap() != 3000 {
		t.Fatalf("precondition: max gap = %d, want 3000", m.MaxGap())
	}

	m.Reset()

	if got := m.ResponseBuf.Len(); got != 0 {
		t.Errorf("after Reset: buffer length = %d, want 0", got)
	}
	if got := m.OutputTokenCount(); got != 0 {
		t.Errorf("after Reset: token count = %d, want 0", got)
	}
	if got := m.ElapsedMs(); got != 0 {
		t.Errorf("after Reset: elapsed = %d, want 0", got)
	}
	if got := m.MaxGap(); got != 0 {
		t.Errorf("after Reset: max gap = %d, want 0", got)
	}
	if got := m.TokPerSec(); got != 0 {
		t.Errorf("after Reset: tok/s = %v, want 0", got)
	}
}

// TestStreamMetricsResetIsNilSafe proves a nil metrics value is a no-op, so
// the reset can be called on the retry path without a nil check at the call
// site.
func TestStreamMetricsResetIsNilSafe(t *testing.T) {
	var m *streamMetrics
	m.Reset()
	m.SetOutputTokens(1)
	m.NoteOutput(time.Now())
	if m.TokPerSec() != 0 {
		t.Errorf("nil metrics tok/s = %v, want 0", m.TokPerSec())
	}
}

// TestStreamNoteWriterDoesNotDoubleCapture proves the writer the executor
// lane installs captures a chunk exactly once: the buffer holds one copy and
// the output stamp fires once, so the Gemini lane can leave the capture to
// the bridge without duplicating anything.
func TestStreamNoteWriterDoesNotDoubleCapture(t *testing.T) {
	m := &streamMetrics{}
	w := &streamNoteWriter{w: &m.ResponseBuf, note: m.NoteOutput}

	payload := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := m.ResponseBuf.Len(); got != len(payload) {
		t.Errorf("buffer length = %d, want %d (chunk captured twice?)", got, len(payload))
	}
	if got := string(m.ResponseBuf.Bytes()); got != string(payload) {
		t.Errorf("buffer content = %q, want %q", got, string(payload))
	}
	if m.ElapsedMs() != 0 {
		t.Errorf("elapsed = %d, want 0 for a single chunk", m.ElapsedMs())
	}
}
