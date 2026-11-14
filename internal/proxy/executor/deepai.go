package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	json "encoding/json/v2"

	"axonrouter/proxy/internal/proxy"
)

const (
	deepaiEndpoint = "https://api.deepai.org/hacking_is_a_serious_crime"
	deepaiSalt     = "hackers_become_a_little_stinkier_every_time_they_hack"
	deepaiUA       = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.6778.85 Safari/537.36"
)

var deepaiTable = [64]int32{
	-680876936, -389564586, 606105819, -1044525330, -176418897, 1200080426, -1473231341, -45705983,
	1770035416, -1958414417, -42063, -1990404162, 1804603682, -40341101, -1502002290, 1236535329,
	-165796510, -1069501632, 643717713, -373897302, -701558691, 38016083, -660478335, -405537848,
	568446438, -1019803690, -187363961, 1163531501, -1444681467, -51403784, 1735328473, -1926607734,
	-378558, -2022574463, 1839030562, -35309556, -1530992060, 1272893353, -155497632, -1094730640,
	681279174, -358537222, -722521979, 76029189, -640364487, -421815835, 530742520, -995338651,
	-198630844, 1126891415, -1416354905, -57434055, 1700485571, -1894986606, -1051523, -2054922799,
	1873313359, -30611744, -1560198380, 1309151649, -145523070, -1120210379, 718787259, -343485551,
}

var deepaiShifts = [16]uint{7, 12, 17, 22, 5, 9, 14, 20, 4, 11, 16, 23, 6, 10, 15, 21}

func deepaiIslandHash(text string) string {
	data := append([]byte(text), 0x80)
	n := len(data) - 1
	c := ((n / 4 + 2) | 15) - 1
	hSize := c + 16
	if hSize < 64 {
		hSize = 64
	}
	h := make([]uint32, hSize)
	h[c] = uint32(8 * n)
	for k := n; k >= 0; k-- {
		h[k>>2] |= uint32(data[k]) << (8 * (k & 3))
	}

	g := [4]uint32{1732584193, 4023233417, ^uint32(1732584193), ^uint32(4023233417)}
	for b := 0; b < c; b += 16 {
		state := g
		l := 0
		for l < 64 {
			d := int32(state[1])
			e := int32(state[2])
			f := int32(state[3])
			q := l >> 4
			var fn int32
			switch q {
			case 0:
				fn = (d & e) | (^d & f)
			case 1:
				fn = (f & d) | (^f & e)
			case 2:
				fn = d ^ e ^ f
			default:
				fn = e ^ (d | ^f)
			}
			midx := [4]int{l, 5*l + 1, 3*l + 5, 7 * l}[q]
			wi := b | (midx & 15)
			var hval int32
			if wi < len(h) {
				hval = int32(h[wi])
			}
			temp := state[0] + uint32(fn) + uint32(deepaiTable[l]) + uint32(hval)
			sh := deepaiShifts[4*q+(l&3)]
			l++
			rot := (temp << sh) | (temp >> (32 - sh))
			state = [4]uint32{uint32(f), uint32(d) + rot, uint32(d), uint32(e)}
		}
		for idx := 3; idx >= 0; idx-- {
			g[idx] += state[idx]
		}
	}

	var out [32]byte
	for l := 0; l < 32; l++ {
		word := int32(g[l>>3])
		nibble := (word >> uint((4*(1^l))&31)) & 15
		out[l] = "0123456789abcdef"[nibble]
	}
	for i, j := 0, 31; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out[:])
}

func deepaiGenerateKey(ua, randStr string) string {
	inner := deepaiIslandHash(ua + randStr + deepaiSalt)
	mid := deepaiIslandHash(ua + inner)
	outer := deepaiIslandHash(ua + mid)
	return fmt.Sprintf("tryit-%s-%s", randStr, outer)
}

func deepaiRandomStr() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(100000000000))
	return n.String()
}

func deepaiMapModel(raw string) string {
	clean := strings.ToLower(raw)
	if idx := strings.Index(clean, "/"); idx != -1 {
		clean = clean[idx+1:]
	}
	clean = strings.ReplaceAll(clean, "-", "_")
	switch clean {
	case "standard":
		return "standard"
	case "chat_standard":
		return "chat_standard"
	case "chat_genius":
		return "chat_genius"
	case "super_genius":
		return "super_genius"
	default:
		return "chat_standard"
	}
}

// ForwardDeepAI routes a chat completion natively to deepai.org.
func ForwardDeepAI(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	client := req.Client
	if client == nil {
		client = http.DefaultClient
	}

	var parsed struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
		Stream bool `json:"stream"`
	}

	if err := json.Unmarshal(req.Body, &parsed); err != nil {
		return fmt.Errorf("deepai: unmarshal request body: %w", err)
	}

	deepaiModel := deepaiMapModel(parsed.Model)

	// Format messages as simple [{role, content}]
	type simpleMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var chatHistory []simpleMsg
	for _, m := range parsed.Messages {
		contentStr := ""
		switch v := m.Content.(type) {
		case string:
			contentStr = v
		default:
			b, _ := json.Marshal(v)
			contentStr = string(b)
		}
		chatHistory = append(chatHistory, simpleMsg{
			Role:    m.Role,
			Content: contentStr,
		})
	}

	historyBytes, err := json.Marshal(chatHistory)
	if err != nil {
		return fmt.Errorf("deepai: marshal chatHistory: %w", err)
	}

	// Prepare multipart form
	var bodyBuf bytes.Buffer
	mw := multipart.NewWriter(&bodyBuf)

	_ = mw.WriteField("chat_style", "chat")
	_ = mw.WriteField("chatHistory", string(historyBytes))
	_ = mw.WriteField("model", deepaiModel)
	if err := mw.Close(); err != nil {
		return fmt.Errorf("deepai: close multipart writer: %w", err)
	}

	randStr := deepaiRandomStr()
	apiKey := deepaiGenerateKey(deepaiUA, randStr)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, deepaiEndpoint, &bodyBuf)
	if err != nil {
		return fmt.Errorf("deepai: create upstream request: %w", err)
	}

	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	httpReq.Header.Set("User-Agent", deepaiUA)
	httpReq.Header.Set("api-key", apiKey)
	httpReq.Header.Set("Accept", "*/*")

	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("deepai: upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return &proxy.UpstreamError{
			StatusCode: resp.StatusCode,
			Body:       respBytes,
			Header:     resp.Header,
		}
	}

	now := time.Now().Unix()
	id := fmt.Sprintf("chatcmpl-deepai-%d", now)

	if parsed.Stream {
		if flusher, ok := w.(http.Flusher); ok {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()

			buf := make([]byte, 128)
			for {
				n, readErr := resp.Body.Read(buf)
				if n > 0 {
					chunkText := string(buf[:n])
					chunkObj := map[string]any{
						"id":      id,
						"object":  "chat.completion.chunk",
						"created": now,
						"model":   parsed.Model,
						"choices": []any{
							map[string]any{
								"index": 0,
								"delta": map[string]any{
									"content": chunkText,
								},
								"finish_reason": nil,
							},
						},
					}
					chunkJSON, _ := json.Marshal(chunkObj)
					fmt.Fprintf(w, "data: %s\n\n", chunkJSON)
					flusher.Flush()
				}
				if readErr != nil {
					break
				}
			}

			// Final DONE frame
			doneObj := map[string]any{
				"id":      id,
				"object":  "chat.completion.chunk",
				"created": now,
				"model":   parsed.Model,
				"choices": []any{
					map[string]any{
						"index":         0,
						"delta":         map[string]any{},
						"finish_reason": "stop",
					},
				},
			}
			doneJSON, _ := json.Marshal(doneObj)
			fmt.Fprintf(w, "data: %s\n\n", doneJSON)
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			return nil
		}
	}

	// Non-streaming response
	fullTextBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("deepai: read upstream response: %w", err)
	}

	fullText := string(fullTextBytes)
	outObj := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": now,
		"model":   parsed.Model,
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": fullText,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     len(historyBytes) / 4,
			"completion_tokens": len(fullText) / 4,
			"total_tokens":      (len(historyBytes) + len(fullText)) / 4,
		},
	}

	outJSON, err := json.Marshal(outObj)
	if err != nil {
		return fmt.Errorf("deepai: marshal completion response: %w", err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(outJSON)
	return nil
}
