package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	json "encoding/json/v2"

	"axonrouter/proxy/internal/proxy"
)

// deepSeekUploadFilePath is DeepSeek Web's multipart file upload endpoint path.
const deepSeekUploadFilePath = "/api/v0/file/upload_file"

// deepSeekMaxFileBytes bounds one file/image attachment (20 MB).
const deepSeekMaxFileBytes = 20 << 20

var deepSeekFileHTTPClient = sync.OnceValue(func() *http.Client {
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: proxy.NewSafeTransport(),
	}
})

// deepSeekSafeNameRe strips characters that would corrupt headers or paths.
var deepSeekSafeNameRe = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// deepSeekFile represents one file/image to be referenced via ref_file_ids.
type deepSeekFile struct {
	Data []byte
	MIME string
	Name string
}

// deepSeekFileCacheEntry stores an uploaded file_id with an expiration timestamp.
type deepSeekFileCacheEntry struct {
	fileID    string
	expiresAt time.Time
}

// deepSeekFileCache dedupes file uploads by SHA256 of the content.
// In agentic workflows, the same image or document is frequently repeated
// across multiple turn requests. Caching avoids duplicate uploads and PoW solves.
type deepSeekFileCache struct {
	mu    sync.Mutex
	items map[string]deepSeekFileCacheEntry
}

var globalDeepSeekFileCache = &deepSeekFileCache{
	items: make(map[string]deepSeekFileCacheEntry),
}

func (c *deepSeekFileCache) get(hash string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[hash]
	if !ok {
		return "", false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.items, hash)
		return "", false
	}
	return entry.fileID, true
}

func (c *deepSeekFileCache) set(hash, fileID string, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Periodic cleanup if cache grows
	if len(c.items) > 500 {
		now := time.Now()
		for k, v := range c.items {
			if now.After(v.expiresAt) {
				delete(c.items, k)
			}
		}
	}
	c.items[hash] = deepSeekFileCacheEntry{
		fileID:    fileID,
		expiresAt: time.Now().Add(ttl),
	}
}

// deepSeekCollectFiles extracts all images and document files from messages.
// It handles OpenAI image_url, Claude image blocks, and raw file objects.
func deepSeekCollectFiles(ctx context.Context, messages []deepSeekMessage) ([]deepSeekFile, error) {
	var files []deepSeekFile
	for _, msg := range messages {
		extracted, err := deepSeekExtractFilesFromContent(ctx, msg.Content)
		if err != nil {
			return nil, err
		}
		files = append(files, extracted...)
	}
	return files, nil
}

func deepSeekExtractFilesFromContent(ctx context.Context, content jsontext.Value) ([]deepSeekFile, error) {
	if len(content) == 0 {
		return nil, nil
	}
	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		return nil, nil
	}
	var blocks []map[string]any
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, nil
	}

	var files []deepSeekFile
	for _, block := range blocks {
		typ, _ := block["type"].(string)
		switch typ {
		case "image_url":
			f, err := deepSeekResolveFromImageURL(ctx, block["image_url"])
			if err != nil {
				return nil, err
			}
			if f != nil {
				files = append(files, *f)
			}
		case "input_image":
			f, err := deepSeekResolveFromImageURL(ctx, block["image_url"])
			if err != nil {
				return nil, err
			}
			if f == nil {
				f, err = deepSeekResolveFromImageURL(ctx, block["url"])
				if err != nil {
					return nil, err
				}
			}
			if f != nil {
				files = append(files, *f)
			}
		case "image":
			f, err := deepSeekResolveFromClaudeImage(ctx, block["source"])
			if err != nil {
				return nil, err
			}
			if f != nil {
				files = append(files, *f)
			}
		case "file":
			f, err := deepSeekResolveFromFileBlock(ctx, block)
			if err != nil {
				return nil, err
			}
			if f != nil {
				files = append(files, *f)
			}
		}
	}
	return files, nil
}

func deepSeekResolveFromImageURL(ctx context.Context, val any) (*deepSeekFile, error) {
	var rawURL string
	switch v := val.(type) {
	case string:
		rawURL = v
	case map[string]any:
		rawURL, _ = v["url"].(string)
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, nil
	}
	return deepSeekResolveRawResource(ctx, rawURL, "image.png")
}

func deepSeekResolveFromClaudeImage(ctx context.Context, sourceVal any) (*deepSeekFile, error) {
	source, ok := sourceVal.(map[string]any)
	if !ok {
		return nil, nil
	}
	data, _ := source["data"].(string)
	if data != "" {
		mime, _ := source["media_type"].(string)
		if mime == "" {
			mime = "image/png"
		}
		rawBytes, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, fmt.Errorf("deepseek-web: decode claude base64 image: %w", err)
		}
		return &deepSeekFile{
			Data: rawBytes,
			MIME: mime,
			Name: "image.png",
		}, nil
	}
	rawURL, _ := source["url"].(string)
	if rawURL != "" {
		return deepSeekResolveRawResource(ctx, rawURL, "image.png")
	}
	return nil, nil
}

func deepSeekResolveFromFileBlock(ctx context.Context, block map[string]any) (*deepSeekFile, error) {
	filename, _ := block["filename"].(string)
	if filename == "" {
		filename = "file.bin"
	}
	fileData, _ := block["file_data"].(string)
	if fileData != "" {
		// Can be base64 or Data URI
		if strings.HasPrefix(fileData, "data:") {
			return deepSeekResolveRawResource(ctx, fileData, filename)
		}
		decoded, err := base64.StdEncoding.DecodeString(fileData)
		if err != nil {
			return nil, fmt.Errorf("deepseek-web: decode file_data: %w", err)
		}
		mime := "application/octet-stream"
		if ext := filepath.Ext(filename); ext != "" {
			switch strings.ToLower(ext) {
			case ".txt":
				mime = "text/plain"
			case ".pdf":
				mime = "application/pdf"
			case ".png":
				mime = "image/png"
			case ".jpg", ".jpeg":
				mime = "image/jpeg"
			case ".webp":
				mime = "image/webp"
			}
		}
		return &deepSeekFile{
			Data: decoded,
			MIME: mime,
			Name: filename,
		}, nil
	}
	fileURL, _ := block["url"].(string)
	if fileURL != "" {
		return deepSeekResolveRawResource(ctx, fileURL, filename)
	}
	return nil, nil
}

func deepSeekResolveRawResource(ctx context.Context, raw, fallbackName string) (*deepSeekFile, error) {
	if strings.HasPrefix(raw, "data:") {
		parts := strings.SplitN(raw[5:], ",", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("deepseek-web: invalid data URL")
		}
		header, encoded := parts[0], parts[1]
		mime := "application/octet-stream"
		isBase64 := false
		for _, token := range strings.Split(header, ";") {
			token = strings.TrimSpace(token)
			if token == "base64" {
				isBase64 = true
			} else if strings.Contains(token, "/") {
				mime = token
			}
		}
		var data []byte
		var err error
		if isBase64 {
			data, err = base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return nil, fmt.Errorf("deepseek-web: decode data URL base64: %w", err)
			}
		} else {
			unescaped, err := url.QueryUnescape(encoded)
			if err != nil {
				return nil, fmt.Errorf("deepseek-web: unescape data URL: %w", err)
			}
			data = []byte(unescaped)
		}
		if len(data) > deepSeekMaxFileBytes {
			return nil, fmt.Errorf("deepseek-web: file exceeds maximum allowed size (%d bytes)", deepSeekMaxFileBytes)
		}
		name := fallbackName
		if name == "" {
			name = "file.bin"
		}
		return &deepSeekFile{
			Data: data,
			MIME: mime,
			Name: name,
		}, nil
	}

	// Remote URL: validate scheme and SSRF safety
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("deepseek-web: unparseable file URL: %s", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("deepseek-web: unsupported URL scheme: %s", parsed.Scheme)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	// Use safe HTTP transport to prevent SSRF
	resp, err := deepSeekFileHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("deepseek-web: fetch remote resource: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("deepseek-web: remote resource returned HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, deepSeekMaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("deepseek-web: read remote resource: %w", err)
	}
	if len(data) > deepSeekMaxFileBytes {
		return nil, fmt.Errorf("deepseek-web: remote file exceeds maximum allowed size (%d bytes)", deepSeekMaxFileBytes)
	}

	mime := resp.Header.Get("Content-Type")
	if idx := strings.Index(mime, ";"); idx != -1 {
		mime = strings.TrimSpace(mime[:idx])
	}
	if mime == "" {
		mime = "application/octet-stream"
	}

	name := fallbackName
	if base := filepath.Base(parsed.Path); base != "" && base != "." && base != "/" {
		name = base
	}

	return &deepSeekFile{
		Data: data,
		MIME: mime,
		Name: name,
	}, nil
}

// uploadFile uploads one file/image to DeepSeek Web (/api/v0/file/upload_file),
// solving a PoW challenge for the upload path and polling until parsing succeeds.
func (t deepSeekTurn) uploadFile(ctx context.Context, accessToken, chatSessionID string, file deepSeekFile) (string, error) {
	// 1. Check deduplication cache
	h := sha256.Sum256(file.Data)
	hashKey := hex.EncodeToString(h[:])
	if fileID, found := globalDeepSeekFileCache.get(hashKey); found {
		return fileID, nil
	}

	// 2. Solve PoW for /api/v0/file/upload_file
	powAnswer, err := t.requestPowForTarget(ctx, accessToken, chatSessionID, deepSeekUploadFilePath)
	if err != nil {
		return "", fmt.Errorf("pow for upload: %w", err)
	}

	// 3. Construct multipart body
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	safeName := deepSeekSafeNameRe.ReplaceAllString(file.Name, "_")
	if safeName == "" {
		safeName = "file.bin"
	}

	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, safeName))
	partHeader.Set("Content-Type", file.MIME)

	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return "", fmt.Errorf("create multipart: %w", err)
	}
	if _, err := part.Write(file.Data); err != nil {
		return "", fmt.Errorf("write multipart: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close multipart: %w", err)
	}

	// 4. Send upload request
	uploadURL := t.endpoints.uploadFile
	headers := t.webHeaders(map[string]string{
		"content-type":      writer.FormDataContentType(),
		"x-file-size":       strconv.Itoa(len(file.Data)),
		"x-ds-pow-response": powAnswer,
		"authorization":     "Bearer " + accessToken,
		"origin":            t.origin,
		"referer":           deepSeekChatPageURL(t.origin, chatSessionID),
	})

	for name, value := range t.attestationHeaders(ctx) {
		headers[name] = value
	}

	resp, err := t.send(ctx, http.MethodPost, uploadURL, body.Bytes(), headers)
	if err != nil {
		return "", fmt.Errorf("upload post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("upload returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read upload response: %w", err)
	}

	var envelope deepSeekEnvelope
	if err := json.Unmarshal(respBytes, &envelope); err != nil {
		return "", fmt.Errorf("unmarshal upload response: %w", err)
	}
	if envelope.Code != 0 {
		return "", fmt.Errorf("upload returned code %d: %s", envelope.Code, envelope.Msg)
	}

	var holder struct {
		BizData struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"biz_data"`
	}
	if err := json.Unmarshal(envelope.Data, &holder); err != nil {
		return "", fmt.Errorf("unmarshal upload biz_data: %w", err)
	}

	fileID := holder.BizData.ID
	if fileID == "" {
		return "", fmt.Errorf("upload returned empty file id")
	}

	status := holder.BizData.Status
	// 5. Poll fetch_files if pending / parsing
	deadline := time.Now().Add(30 * time.Second)
	for (status == "PENDING" || status == "PARSING") && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}

		fetchURL := t.endpoints.fetchFiles + "?file_ids=" + url.QueryEscape(fileID)
		fetchHeaders := t.webHeaders(map[string]string{
			"authorization": "Bearer " + accessToken,
			"origin":        t.origin,
			"referer":       deepSeekChatPageURL(t.origin, chatSessionID),
		})
		fetchResp, err := t.send(ctx, http.MethodGet, fetchURL, nil, fetchHeaders)
		if err != nil {
			break
		}
		fBytes, _ := io.ReadAll(fetchResp.Body)
		fetchResp.Body.Close()

		var fEnv deepSeekEnvelope
		if err := json.Unmarshal(fBytes, &fEnv); err == nil && fEnv.Code == 0 {
			var fBiz struct {
				BizData struct {
					Files []struct {
						ID     string `json:"id"`
						Status string `json:"status"`
					} `json:"files"`
				} `json:"biz_data"`
			}
			if err := json.Unmarshal(fEnv.Data, &fBiz); err == nil {
				for _, f := range fBiz.BizData.Files {
					if f.ID == fileID {
						status = f.Status
						break
					}
				}
			}
		}
	}

	// Cache successful upload (2-hour TTL)
	globalDeepSeekFileCache.set(hashKey, fileID, 2*time.Hour)
	return fileID, nil
}
