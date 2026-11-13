package chat

import (
	"io"
	"time"
)

// streamNoteWriter wraps an io.Writer and stamps each non-empty write with the
// current time via note, so every streamed chunk is timed for gap/rate metrics.
// A nil note is a no-op.
//
// It lives in its own file rather than in fallback.go: fallback.go is already
// far past the 700-line ceiling, and the wrapper is the whole of the
// "output generation rate" instrumentation that has to reach the executor
// lane (the executors write the captured response through an io.Writer, so
// the timing has to ride on the writer itself).
type streamNoteWriter struct {
	w    io.Writer
	note func(time.Time)
}

// Write forwards p to the wrapped writer and stamps the arrival time when the
// write produced bytes. The stamp happens after the write so a failed write
// does not widen the measured output window.
func (s *streamNoteWriter) Write(p []byte) (int, error) {
	if s == nil || s.w == nil {
		return 0, nil
	}
	n, err := s.w.Write(p)
	if n > 0 && s.note != nil {
		s.note(time.Now())
	}
	return n, err
}
