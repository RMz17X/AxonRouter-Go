package executor

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"axonrouter/proxy/internal/log"
	fhttp "github.com/bogdanfinn/fhttp"
)

// GeminiWebImageResult carries one generated image asset.
type GeminiWebImageResult struct {
	URL string
	B64 string
}

// geminiWebImagenURLRe matches image URLs emitted by Gemini Web (Imagen 3).
// Covers googleusercontent.com and gstatic.com image content endpoints.
var geminiWebImagenURLRe = regexp.MustCompile(`https?://[^\s"'<>\)]*(?:googleusercontent\.com|gstatic\.com)[^\s"'<>\)]+`)

// GenerateGeminiWebImage runs a turn on Gemini Web with an image generation prompt,
// extracts the resulting Imagen 3 image URLs, downloads the asset using the session's
// TLS client and authenticated cookies, and returns the base64-encoded images.
func GenerateGeminiWebImage(ctx context.Context, cookie, prompt string, n int, inputImage ...string) ([]GeminiWebImageResult, error) {
	cookie = strings.TrimSpace(cookie)
	session := geminiWebSessionForCredential(cookie)
	if session == nil {
		return nil, fmt.Errorf("gemini-web: failed to initialize session for image generation")
	}
	return GenerateGeminiWebImageWithSession(ctx, session, prompt, n, inputImage...)
}

// GenerateGeminiWebImageWithSession executes image generation on a prepared session.
func GenerateGeminiWebImageWithSession(ctx context.Context, session *geminiWebSession, prompt string, n int, inputImage ...string) ([]GeminiWebImageResult, error) {
	if session == nil {
		return nil, fmt.Errorf("gemini-web: nil session")
	}

	if n < 1 {
		n = 1
	}

	// Optional input image for image editing
	var attachments []geminiWebAttachment
	if len(inputImage) > 0 && strings.TrimSpace(inputImage[0]) != "" {
		imgSrc := geminiWebImageSource{URL: strings.TrimSpace(inputImage[0])}
		resolvedImg, err := geminiWebResolveImage(ctx, imgSrc)
		if err == nil && resolvedImg != nil {
			if att, err := session.uploadImage(ctx, *resolvedImg); err == nil {
				attachments = append(attachments, att)
			}
		}
	}

	// Enrich the prompt so Gemini Web's conversational model reliably triggers
	// the Imagen 3 tool rather than describing an image in text.
	genPrompt := strings.TrimSpace(prompt)
	lower := strings.ToLower(genPrompt)
	if len(attachments) > 0 {
		if !strings.HasPrefix(lower, "edit") && !strings.HasPrefix(lower, "modify") && !strings.HasPrefix(lower, "generate") {
			genPrompt = "Edit this image: " + genPrompt
		}
	} else if !strings.HasPrefix(lower, "generate an image") &&
		!strings.HasPrefix(lower, "create an image") &&
		!strings.HasPrefix(lower, "draw ") {
		genPrompt = "Generate an image of " + genPrompt
	}

	var accumulatedText string
	var lastRPCError *geminiWebRPCError

	err := session.streamTurn(ctx, genPrompt, 1, nil, attachments, func(line *geminiWebLine) error {
		if line == nil {
			return nil
		}
		if line.RPCError != nil {
			lastRPCError = line.RPCError
		}
		for _, frame := range line.Frames {
			if frame.Text != "" {
				accumulatedText = frame.Text
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("gemini-web image generation turn failed: %w", err)
	}
	if lastRPCError != nil {
		return nil, fmt.Errorf("gemini-web upstream RPC error: %s (status %d)", lastRPCError.Message, lastRPCError.Status)
	}

	// Extract candidate image URLs from markdown or text output
	rawMatches := geminiWebImagenURLRe.FindAllString(accumulatedText, -1)
	if len(rawMatches) == 0 {
		preview := strings.TrimSpace(accumulatedText)
		if len(preview) > 200 {
			preview = preview[:200] + "…"
		}
		return nil, fmt.Errorf("gemini-web did not return a generated image asset (response: %s)", preview)
	}

	// Deduplicate and clean URLs (strip markdown closing brackets/parentheses)
	seen := make(map[string]struct{})
	cleanURLs := make([]string, 0, len(rawMatches))
	for _, m := range rawMatches {
		cleaned := strings.TrimRight(m, ")]\"',.")
		if _, exists := seen[cleaned]; exists || cleaned == "" {
			continue
		}
		seen[cleaned] = struct{}{}
		cleanURLs = append(cleanURLs, cleaned)
		if len(cleanURLs) >= n {
			break
		}
	}

	results := make([]GeminiWebImageResult, 0, len(cleanURLs))
	for _, imgURL := range cleanURLs {
		imgBytes, err := session.downloadGeneratedImage(ctx, imgURL)
		if err != nil {
			log.Warn("gemini-web", "could not download image asset", "url", imgURL, "error", err)
			continue
		}
		b64 := base64.StdEncoding.EncodeToString(imgBytes)
		results = append(results, GeminiWebImageResult{
			URL: imgURL,
			B64: b64,
		})
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("gemini-web: found %d image URL(s) but failed to download any asset", len(cleanURLs))
	}

	return results, nil
}

// downloadGeneratedImage downloads an image using the authenticated stealth client,
// falling back to a standard HTTP GET if necessary.
func (s *geminiWebSession) downloadGeneratedImage(ctx context.Context, imgURL string) ([]byte, error) {
	headers := map[string]string{
		"referer": "https://gemini.google.com/",
		"accept":  "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8",
	}

	// Try via stealth client with shared cookies if stealth client is available
	if s.stealth != nil {
		resp, err := s.do(ctx, http.MethodGet, imgURL, nil, "", headers)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == fhttp.StatusOK {
				return io.ReadAll(io.LimitReader(resp.Body, geminiWebMaxImageBytes))
			}
		}
	}

	// Fallback to standard HTTP GET with cookie header
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imgURL, nil)
	if err != nil {
		return nil, fmt.Errorf("gemini-web build download request: %w", err)
	}
	req.Header.Set("User-Agent", geminiWebUserAgent)
	req.Header.Set("Referer", "https://gemini.google.com/")
	if s.cookie != "" {
		req.Header.Set("Cookie", s.cookie)
	}

	fallbackResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini-web download fallback error: %w", err)
	}
	defer fallbackResp.Body.Close()

	if fallbackResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini-web image download HTTP status %d", fallbackResp.StatusCode)
	}

	return io.ReadAll(io.LimitReader(fallbackResp.Body, geminiWebMaxImageBytes))
}
