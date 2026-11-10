package executor

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	json "encoding/json/v2"
)

func TestDeepSeekExtractFilesFromContent(t *testing.T) {
	ctx := context.Background()

	// 1. OpenAI image_url (data URL)
	samplePNG := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	openAIBlock := `[
		{"type": "text", "text": "What is in this image?"},
		{"type": "image_url", "image_url": {"url": "data:image/png;base64,` + samplePNG + `"}}
	]`
	files, err := deepSeekExtractFilesFromContent(ctx, jsontext.Value(openAIBlock))
	if err != nil {
		t.Fatalf("extract files from openai block failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].MIME != "image/png" {
		t.Errorf("expected mime image/png, got %s", files[0].MIME)
	}
	if len(files[0].Data) == 0 {
		t.Errorf("expected non-empty data")
	}

	// 2. Claude image block
	claudeBlock := `[
		{"type": "image", "source": {"type": "base64", "media_type": "image/jpeg", "data": "` + samplePNG + `"}}
	]`
	files, err = deepSeekExtractFilesFromContent(ctx, jsontext.Value(claudeBlock))
	if err != nil {
		t.Fatalf("extract files from claude block failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].MIME != "image/jpeg" {
		t.Errorf("expected mime image/jpeg, got %s", files[0].MIME)
	}

	// 3. Document file block
	docData := base64.StdEncoding.EncodeToString([]byte("hello deepseek document"))
	fileBlock := `[
		{"type": "file", "filename": "test.txt", "file_data": "` + docData + `"}
	]`
	files, err = deepSeekExtractFilesFromContent(ctx, jsontext.Value(fileBlock))
	if err != nil {
		t.Fatalf("extract files from doc block failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Name != "test.txt" {
		t.Errorf("expected filename test.txt, got %s", files[0].Name)
	}
	if files[0].MIME != "text/plain" {
		t.Errorf("expected mime text/plain, got %s", files[0].MIME)
	}
	if string(files[0].Data) != "hello deepseek document" {
		t.Errorf("expected data 'hello deepseek document', got %q", string(files[0].Data))
	}
}

func TestDeepSeekFileCache(t *testing.T) {
	cache := &deepSeekFileCache{items: make(map[string]deepSeekFileCacheEntry)}

	hash := "fakehash123"
	fileID := "file-abc-999"

	if _, ok := cache.get(hash); ok {
		t.Fatalf("expected cache miss")
	}

	cache.set(hash, fileID, 1*time.Hour)
	got, ok := cache.get(hash)
	if !ok || got != fileID {
		t.Fatalf("expected cached fileID %q, got %q (ok=%v)", fileID, got, ok)
	}

	// Expired entry
	cache.set(hash, fileID, -1*time.Second)
	if _, ok := cache.get(hash); ok {
		t.Fatalf("expected expired entry to be treated as miss")
	}
}

func TestDeepSeekUploadFileWithMockServer(t *testing.T) {
	ctx := context.Background()

	var uploadCalled bool
	var fetchCalled bool
	var powCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat/create_pow_challenge":
			powCalled = true
			reqBytes, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(reqBytes, &req)
			if req["target_path"] != deepSeekUploadFilePath {
				t.Errorf("expected target_path %q, got %v", deepSeekUploadFilePath, req["target_path"])
			}
			// Return a minimal challenge with difficulty 1 so solving is instant
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"code": 0,
				"msg": "",
				"data": {
					"biz_code": 0,
					"biz_msg": "",
					"biz_data": {
						"challenge": {
							"algorithm": "DeepSeekHashV1",
							"challenge": "1813da791ddac0da3a225e5878ff3d3ea07577e048f8e9575b82a274b71e3810",
							"salt": "vector",
							"difficulty": 10,
							"expire_at": 42,
							"signature": "testsign",
							"target_path": "/api/v0/file/upload_file"
						}
					}
				}
			}`))
		case "/file/upload_file":
			uploadCalled = true
			if r.Header.Get("X-Ds-Pow-Response") == "" {
				t.Errorf("expected X-Ds-Pow-Response header")
			}
			if r.Header.Get("X-File-Size") == "" {
				t.Errorf("expected X-File-Size header")
			}
			mr, err := r.MultipartReader()
			if err != nil {
				t.Fatalf("multipart reader failed: %v", err)
			}
			part, err := mr.NextPart()
			if err != nil {
				t.Fatalf("next part failed: %v", err)
			}
			if part.FormName() != "file" {
				t.Errorf("expected form name file, got %s", part.FormName())
			}
			data, _ := io.ReadAll(part)
			if string(data) != "test file content" {
				t.Errorf("expected file content 'test file content', got %q", string(data))
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"code": 0,
				"msg": "",
				"data": {
					"biz_code": 0,
					"biz_data": {
						"id": "file-xyz-12345",
						"status": "PENDING"
					}
				}
			}`))
		case "/file/fetch_files":
			fetchCalled = true
			if r.URL.Query().Get("file_ids") != "file-xyz-12345" {
				t.Errorf("unexpected file_ids query: %s", r.URL.Query().Get("file_ids"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"code": 0,
				"msg": "",
				"data": {
					"biz_code": 0,
					"biz_data": {
						"files": [
							{
								"id": "file-xyz-12345",
								"status": "SUCCESS"
							}
						]
					}
				}
			}`))
		default:
			t.Logf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	turn := newDeepSeekTurn(server.Client(), server.URL+"/chat/completion", deepSeekStealth{})
	turn.endpoints = deepSeekEndpointsFor(server.URL + "/chat/completion")

	testFile := deepSeekFile{
		Data: []byte("test file content"),
		MIME: "text/plain",
		Name: "test.txt",
	}

	fileID, err := turn.uploadFile(ctx, "mock-access-token", "mock-session", testFile)
	if err != nil {
		t.Fatalf("uploadFile failed: %v", err)
	}
	if fileID != "file-xyz-12345" {
		t.Errorf("expected fileID file-xyz-12345, got %s", fileID)
	}

	if !powCalled {
		t.Errorf("expected PoW challenge to be requested")
	}
	if !uploadCalled {
		t.Errorf("expected upload_file to be called")
	}
	if !fetchCalled {
		t.Errorf("expected fetch_files to be polled")
	}

	// Second upload of identical content should hit cache and not call HTTP
	powCalled, uploadCalled, fetchCalled = false, false, false
	cachedID, err := turn.uploadFile(ctx, "mock-access-token", "mock-session", testFile)
	if err != nil {
		t.Fatalf("cached uploadFile failed: %v", err)
	}
	if cachedID != "file-xyz-12345" {
		t.Errorf("expected cached fileID file-xyz-12345, got %s", cachedID)
	}
	if powCalled || uploadCalled || fetchCalled {
		t.Errorf("expected cache hit to skip network calls")
	}
}

func TestDeepSeekSafeNameRe(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"image.png", "image.png"},
		{"file with spaces.pdf", "file_with_spaces.pdf"},
		{"evil/path;name.bin", "evil_path_name.bin"},
		{"sample-123_4.txt", "sample-123_4.txt"},
	}

	for _, c := range cases {
		got := deepSeekSafeNameRe.ReplaceAllString(c.input, "_")
		if got != c.expected {
			t.Errorf("sanitize %q: got %q, want %q", c.input, got, c.expected)
		}
	}
}
