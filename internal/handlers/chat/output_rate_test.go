package chat

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

// The output generation rate (tok/s) and the max inter-chunk gap are the two
// numbers an operator reads when a stream feels slow: a low rate means the
// upstream is generating slowly, a wide gap means the stream stalled between
// two chunks. Both are derived from the streamed chunks themselves, so the
// instrumentation has to be proven on the two paths that produce them —
// the writer wrapper the executor lane uses, and the direct notes the
// forwarder stamps.
//
// These cases are deliberately storage-free: they assert the measurement, not
// the persistence around it.

// TestStreamNoteWriterStampsEveryChunk proves the wrapper the executor lane
// installs actually stamps a chunk: the note fires once per non-empty write,
// with the write's own bytes forwarded unchanged.
func TestStreamNoteWriterStampsEveryChunk(t *testing.T) {
	var buf bytes.Buffer
	var stamps []time.Time

	w := &streamNoteWriter{
		w:    &buf,
		note: func(now time.Time) { stamps = append(stamps, now) },
	}

	n, err := w.Write([]byte("first"))
	if err != nil || n != 5 {
		t.Fatalf("write = (%d, %v), want (5, nil)", n, err)
	}
	if len(stamps) != 1 {
		t.Fatalf("stamps after one write = %d, want 1", len(stamps))
	}
	if buf.String() != "first" {
		t.Errorf("buffer = %q, want %q", buf.String(), "first")
	}

	if _, err := w.Write([]byte("second")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if len(stamps) != 2 {
		t.Fatalf("stamps after two writes = %d, want 2", len(stamps))
	}
	if buf.String() != "firstsecond" {
		t.Errorf("buffer = %q, want %q", buf.String(), "firstsecond")
	}
}

// TestStreamNoteWriterNilGuards proves the wrapper is inert rather than
// panicking when it is used without a target or without a note. Both are
// reachable: the executor request carries a nil buffer when no capture is
// wanted, and the note is nil on a metrics-free path.
func TestStreamNoteWriterNilGuards(t *testing.T) {
	noTarget := &streamNoteWriter{}
	if n, err := noTarget.Write([]byte("x")); err != nil || n != 0 {
		t.Errorf("write with no target = (%d, %v), want (0, nil)", n, err)
	}

	var buf bytes.Buffer
	noNote := &streamNoteWriter{w: &buf}
	if n, err := noNote.Write([]byte("x")); err != nil || n != 1 {
		t.Errorf("write with no note = (%d, %v), want (1, nil)", n, err)
	}
	if buf.String() != "x" {
		t.Errorf("buffer = %q, want %q", buf.String(), "x")
	}
}

// TestStreamNoteWriterForwardsToWrappedWriter proves the wrapper is
// transparent: what it writes is what the wrapped writer receives, byte for
// byte. The wrapper sits between the executor and the capture buffer, so a
// lossy wrapper would corrupt the response content the usage log records.
func TestStreamNoteWriterForwardsToWrappedWriter(t *testing.T) {
	payload := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"
	var buf bytes.Buffer
	w := &streamNoteWriter{w: &buf, note: func(time.Time) {}}

	n, err := w.Write([]byte(payload))
	if err != nil || n != len(payload) {
		t.Fatalf("write = (%d, %v), want (%d, nil)", n, err, len(payload))
	}
	if buf.String() != payload {
		t.Errorf("buffer = %q, want %q", buf.String(), payload)
	}
}

// TestStreamNoteWriterNotesRealTime proves the stamp is the wall clock, not a
// counter: two writes separated by a sleep must show a gap of at least that
// sleep. This is what makes the max-gap figure meaningful.
func TestStreamNoteWriterNotesRealTime(t *testing.T) {
	var buf bytes.Buffer
	var stamps []time.Time
	w := &streamNoteWriter{
		w:    &buf,
		note: func(now time.Time) { stamps = append(stamps, now) },
	}

	_, _ = w.Write([]byte("a"))
	time.Sleep(20 * time.Millisecond)
	_, _ = w.Write([]byte("b"))

	if len(stamps) != 2 {
		t.Fatalf("stamps = %d, want 2", len(stamps))
	}
	gap := stamps[1].Sub(stamps[0])
	if gap < 15*time.Millisecond {
		t.Errorf("gap between stamps = %v, want at least ~20ms", gap)
	}
}

// TestStreamMetricsReportsRateAndGap is the end-to-end measurement: a stream
// that produced tokens over a known window must report both the rate and the
// widest gap between two chunks.
func TestStreamMetricsReportsRateAndGap(t *testing.T) {
	m := &streamMetrics{}
	m.SetOutputTokens(100)

	start := time.Now()
	m.NoteOutput(start)
	m.NoteOutput(start.Add(500 * time.Millisecond))
	m.NoteOutput(start.Add(1500 * time.Millisecond))

	if got := m.ElapsedMs(); got != 1500 {
		t.Errorf("ElapsedMs = %d, want 1500", got)
	}
	if got := m.MaxGap(); got != 1000 {
		t.Errorf("MaxGap = %d, want 1000", got)
	}
	// 100 tokens over 1.5 s = ~66.7 tok/s.
	rate := m.TokPerSec()
	if rate < 66 || rate > 68 {
		t.Errorf("TokPerSec = %v, want ~66.7 (100 tok / 1.5 s)", rate)
	}
}

// TestStreamMetricsConcurrentAccessIsSafe proves the metrics survive the
// concurrent write the stream lane actually produces: chunks land on the
// writer goroutine while the usage log reads the figures on another. The
// race detector turns this into a hard failure if the mutex is missing.
func TestStreamMetricsConcurrentAccessIsSafe(t *testing.T) {
	m := &streamMetrics{}
	m.SetOutputTokens(10)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			m.NoteOutput(time.Now())
		}
	}()
	for i := 0; i < 100; i++ {
		_ = m.TokPerSec()
		_ = m.MaxGap()
		_ = m.ElapsedMs()
		m.SetOutputTokens(i)
	}
	<-done
}

// TestStreamMetricsZeroWhenNothingStreamed proves the figures read zero when
// no chunk ever arrived, so a non-streaming turn does not log a rate of
// infinity or a gap of the whole request.
func TestStreamMetricsZeroWhenNothingStreamed(t *testing.T) {
	m := &streamMetrics{}
	if m.TokPerSec() != 0 {
		t.Errorf("TokPerSec = %v, want 0", m.TokPerSec())
	}
	if m.MaxGap() != 0 {
		t.Errorf("MaxGap = %d, want 0", m.MaxGap())
	}
	if m.ElapsedMs() != 0 {
		t.Errorf("ElapsedMs = %d, want 0", m.ElapsedMs())
	}
}

// TestStreamNoteWriterKeepsCaptureContentIntact proves the wrapper does not
// disturb the captured response content: the usage log reads that buffer to
// estimate tokens and to record the response, so a wrapper that dropped or
// reordered bytes would corrupt both.
func TestStreamNoteWriterKeepsCaptureContentIntact(t *testing.T) {
	var buf bytes.Buffer
	w := &streamNoteWriter{w: &buf, note: func(time.Time) {}}

	chunks := []string{
		"data: {\"id\":\"1\"}\n\n",
		"data: {\"id\":\"2\"}\n\n",
		"data: [DONE]\n\n",
	}
	for _, c := range chunks {
		if _, err := io.WriteString(w, c); err != nil {
			t.Fatalf("write %q: %v", c, err)
		}
	}

	want := strings.Join(chunks, "")
	if buf.String() != want {
		t.Errorf("captured content = %q, want %q", buf.String(), want)
	}
}
