package executor

import (
	"bytes"
	"context"

	"axonrouter/proxy/internal/proxy"
	"axonrouter/proxy/internal/translator"
)

// maxUsageFrame caps how much of an unterminated SSE frame is held back
// while waiting for the rest of that frame. A usage frame is a single JSON
// payload; anything longer is not a frame this capture is going to parse.
const maxUsageFrame = 1 << 20

// usageCapture reads the usage block out of an upstream SSE stream.
//
// The raw byte stream has to be reassembled into complete frames first: a
// stream arrives in arbitrary read-sized blocks, so the frame that carries
// the usage block is routinely split across two of them. Parsing each block
// on its own loses the usage of every frame that straddles a boundary, and
// the loss is silent — the turn is billed as if the upstream reported
// nothing, so a cache hit is charged at the full input rate and the
// cached-token discount never applies (upstream decolua/9router #5616).
//
// The assembler therefore holds the tail of an unterminated frame and only
// parses a frame once its terminator has arrived.
type usageCapture struct {
	ctx  context.Context
	tail []byte
}

// newUsageCapture returns a capture bound to the request context.
func newUsageCapture(ctx context.Context) *usageCapture {
	return &usageCapture{ctx: ctx}
}

// feed consumes a raw block of the upstream stream and records the usage of
// every complete frame it completes.
func (u *usageCapture) feed(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	data := chunk
	if len(u.tail) > 0 {
		combined := make([]byte, 0, len(u.tail)+len(chunk))
		combined = append(combined, u.tail...)
		combined = append(combined, chunk...)
		u.tail = u.tail[:0]
		data = combined
	}
	for {
		idx, sepLen := sseFrameBoundary(data)
		if idx < 0 {
			break
		}
		frame := data[:idx+sepLen]
		data = data[idx+sepLen:]
		captureUsageFromSSEChunk(u.ctx, frame)
	}
	// The remainder is an incomplete frame: hold it for the next block. The
	// bound keeps a stream that never terminates a frame from growing the
	// buffer without limit.
	if len(data) > maxUsageFrame {
		data = data[len(data)-maxUsageFrame:]
	}
	u.tail = append(u.tail, data...)
}

// flush parses whatever is left after the stream ended. A truncated frame
// still carries its usage block in the common case — the upstream closed
// the connection right after writing it — so it is worth a look.
func (u *usageCapture) flush() {
	if len(u.tail) == 0 {
		return
	}
	captureUsageFromSSEChunk(u.ctx, u.tail)
	u.tail = u.tail[:0]
}

// sseFrameBoundary finds the end of the first complete SSE frame in data.
//
// It returns the index of the frame's last content byte and the length of
// its terminator. Both the LF and the CRLF spelling of the blank-line
// separator are recognised, because upstreams differ on the line ending
// they emit.
func sseFrameBoundary(data []byte) (int, int) {
	if idx := bytes.Index(data, []byte("\n\n")); idx >= 0 {
		return idx, 2
	}
	if idx := bytes.Index(data, []byte("\r\n\r\n")); idx >= 0 {
		return idx, 4
	}
	return -1, 0
}

// captureUsageFromSSEChunk records the usage block carried by one complete
// SSE frame.
//
// A frame without a usage block leaves the recorded usage alone: the
// upstream reported nothing new, and an estimate must not overwrite the
// real numbers an earlier frame carried (upstream decolua/9router #5616).
func captureUsageFromSSEChunk(ctx context.Context, chunk []byte) {
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) > maxUsageFrame {
		trimmed = trimmed[:maxUsageFrame]
	}
	if !bytes.Contains(trimmed, []byte("data:")) {
		return
	}
	_ = proxy.ScanStream(bytes.NewReader(trimmed), func(payload []byte) {
		if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
			return
		}
		usage := translator.ParseResponseUsage(payload)
		if usage == nil {
			usage = translator.ParseResponsesUsage(payload)
		}
		if usage == nil {
			return
		}
		// The capture sees the raw bytes, before the wire patcher runs: an
		// upstream reporting prompt_tokens: 0 gets the same estimate
		// substituted here, so the recorded usage matches what the client
		// was sent.
		estimated := false
		if usage.PromptTokens == 0 {
			if est := translator.PromptEstimateFromContext(ctx); est > 0 {
				usage.PromptTokens = est
				estimated = true
			}
		}
		// A frame carrying a usage block merges into what the earlier
		// frames already reported instead of replacing it: the finish
		// chunk and the usage-only trailer are two frames of the same
		// turn, and the trailer holds the real counters. A frame whose
		// numbers are an estimate never overwrites real ones (upstream
		// decolua/9router #5616).
		previous := translator.PeekUsage(ctx)
		if previous != nil {
			usage = mergeUsage(previous, usage, estimated)
		}
		translator.SetUsage(ctx, usage)
	})
}

// mergeUsage folds a newer usage block into the one already recorded.
//
// The newer block wins per field, but only where it actually reported a
// value: a field left at zero means "not reported", not "reported zero". An
// estimated value never displaces a real one — an estimate standing in for
// a missing prompt count must not erase the cached-token count a later
// frame actually reported (upstream decolua/9router #5616).
func mergeUsage(previous, next *translator.OpenAIUsage, nextEstimated bool) *translator.OpenAIUsage {
	if nextEstimated {
		// Only the prompt count of an estimated block is a guess; the rest
		// of it came off the wire.
		if previous.PromptTokens > 0 {
			next.PromptTokens = previous.PromptTokens
		}
	} else if next.PromptTokens == 0 {
		next.PromptTokens = previous.PromptTokens
	}
	if next.CompletionTokens == 0 {
		next.CompletionTokens = previous.CompletionTokens
	}
	if previous.GetCachedTokens() > next.GetCachedTokens() {
		next.CachedTokens = previous.GetCachedTokens()
		next.PromptTokensDetails = previous.PromptTokensDetails
	}
	if previous.CacheCreationInputTokens > next.CacheCreationInputTokens {
		next.CacheCreationInputTokens = previous.CacheCreationInputTokens
	}
	if next.CompletionTokensDetails == nil {
		next.CompletionTokensDetails = previous.CompletionTokensDetails
	}
	if next.TotalTokens == 0 {
		next.TotalTokens = previous.TotalTokens
	}
	if next.TotalTokens == 0 && next.PromptTokens > 0 {
		next.TotalTokens = next.PromptTokens + next.CompletionTokens
	}
	return next
}
