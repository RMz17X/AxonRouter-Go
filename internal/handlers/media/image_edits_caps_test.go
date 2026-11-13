package media

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleImageEdits_MultipartCapsEnforced(t *testing.T) {
	_, repo := setupImageEditsEnv(t)
	handler := imageEditsHandler(repo)

	// 1. More than MediaMaxItems (8) images should be rejected with 413
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("prompt", "test multiple images")
	for i := 0; i < 9; i++ {
		fw, err := mw.CreateFormFile("image", "test.png")
		if err != nil {
			t.Fatalf("create form file %d: %v", i, err)
		}
		_, _ = fw.Write([]byte("fake image data"))
	}
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()

	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status 413 for > 8 images, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "at most 8") {
		t.Errorf("expected error to mention limit of 8 items, got: %s", rec.Body.String())
	}

	// 2. A file exceeding MediaMaxItemBytes (25MB) should be rejected
	var buf2 bytes.Buffer
	mw2 := multipart.NewWriter(&buf2)
	_ = mw2.WriteField("prompt", "test oversized image")
	fw2, err := mw2.CreateFormFile("image", "large.png")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	// Write dummy data with an inflated multipart header Size or check fileToDataURI directly
	_, _ = fw2.Write([]byte("some data"))
	_ = mw2.Close()

	// Direct test on fileToDataURI with oversized FileHeader
	fh := &multipart.FileHeader{
		Filename: "large.png",
		Size:     MediaMaxItemBytes + 1024,
	}
	_, err = fileToDataURI(fh)
	if err == nil {
		t.Fatal("expected error for file exceeding MediaMaxItemBytes, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Errorf("expected error to mention exceeds maximum, got: %v", err)
	}
}
