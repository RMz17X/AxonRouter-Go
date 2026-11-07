package executor

import (
	"bufio"
	"bytes"
	json "encoding/json/v2"
	"io"
	"strings"
)

// CodeBuddy-family SSE stream repair (fragment 977), porting the two
// normalizations upstream's shared passthrough applies
// (open-sse/utils/stream.js) because CodeBuddy's chunk shape violates
// the OpenAI streaming contract in two small ways:
//
//   - finish_reason arrives as "" on every chunk. OpenAI uses null
//     until the terminal chunk carries the real reason, and clients
//     that read "" as a finished choice misbehave — so an empty
//     finish_reason is rewritten to null. A chunk whose choices carry
//     a real reason is passed through untouched.
//   - The upstream can emit the [DONE] sentinel more than once; only
//     the first is forwarded, repeats are dropped.
//
// The wrapper sits on the family forward only (forwardCodebuddy), so
// no other provider's stream changes. It transforms line by line — the
// stream is never buffered whole (§4.A) — and lines it does not rewrite
// pass through byte-identical.
type codebuddySSENormalizer struct {
	lines    *bufio.Reader
	out      bytes.Buffer
	doneSeen bool
	eof      bool
	readErr  error
}

func newCodebuddySSENormalizer(src io.Reader) *codebuddySSENormalizer {
	return &codebuddySSENormalizer{lines: bufio.NewReader(src)}
}

// Read serves transformed bytes, pulling one upstream line at a time.
// A line is emitted only whole, so a slow upstream surfaces as a slow
// Read — the same contract the stall watchdog already relies on.
func (n *codebuddySSENormalizer) Read(p []byte) (int, error) {
	for n.out.Len() == 0 {
		if n.readErr != nil {
			return 0, n.readErr
		}
		if n.eof {
			return 0, io.EOF
		}
		line, err := n.lines.ReadString('\n')
		if err != nil {
			// A final unterminated line still counts; the error is
			// delivered after it.
			n.eof = true
			if err != io.EOF {
				n.readErr = err
			}
		}
		if line == "" {
			continue
		}
		n.out.WriteString(n.transformLine(line))
	}
	return n.out.Read(p)
}

// transformLine applies the two repairs to one SSE line.
func (n *codebuddySSENormalizer) transformLine(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return line
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if payload == "[DONE]" {
		if n.doneSeen {
			return ""
		}
		n.doneSeen = true
		return line
	}
	if !strings.HasPrefix(payload, "{") {
		return line
	}
	var chunk map[string]any
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return line
	}
	choices, ok := chunk["choices"].([]any)
	if !ok || len(choices) == 0 {
		return line
	}
	changed := false
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		if fr, ok := choice["finish_reason"].(string); ok && fr == "" {
			choice["finish_reason"] = nil
			changed = true
		}
	}
	if !changed {
		return line
	}
	repaired, err := json.Marshal(chunk)
	if err != nil {
		return line
	}
	return "data: " + string(repaired) + "\n"
}
