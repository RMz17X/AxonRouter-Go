package media

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "encoding/json/v2"

	"axonrouter/proxy/internal/middleware"
)

// This file pins the OpenAI-parity contract of POST /v1/images/edits that the
// independent review of PR #5548 found broken (verdict REQUEST-CHANGES,
// 4 HIGH / 8 MEDIUM / 4 LOW). The endpoint shipped in v1.10.59 with the
// defects below; every case here is a regression guard against them coming
// back.
//
// H3  the OpenAI field set was dropped silently instead of being forwarded
// H4  the SDK image shapes (array, {image_url}, {url}) were rejected with 400
// H5  multipart bodies were forwarded with the wrong Content-Type
// H6  the JSON lane had no image cap while the multipart lane capped at 8
// H7  an oversized body answered 400 where the answer must be 413
// H8  the body limit rule did not cover /images/edits, so the 25 MB per-item
//     cap was dead configuration
// H9  multipart dispatch was case-sensitive, so "Multipart/Form-Data" fell
//     into the JSON lane and died on a JSON parse error
// H10 a non-raster image was coerced to image/png and forwarded
// H11 a mask was accepted and forwarded although no provider lane reads it
//
// The suite runs against a real PostgreSQL schema (see setupImageEditsEnv in
// image_edits_json_test.go). The handler resolves a provider, picks a
// connection row and reads the node registry, so a mocked store would prove
// nothing about the lane that actually serves the request.

// parityUpstream starts a fake upstream that records the forwarded body and
// the Content-Type the gateway used, then answers with a minimal payload.
func parityUpstream(t *testing.T, gotBody, gotContentType *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		*gotBody = buf.String()
		*gotContentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"AAAA"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestImageEditsParityForwardsUnknownFields covers H3: every field the client
// sends that is not an image reference must reach the upstream verbatim.
// Before the fix the handler rebuilt the body from a fixed struct, so
// input_fidelity, output_format, user and every other OpenAI field vanished.
func TestImageEditsParityForwardsUnknownFields(t *testing.T) {
	var gotBody, gotContentType string
	upstream := parityUpstream(t, &gotBody, &gotContentType)

	_, repo := setupImageEditsEnv(t)
	seedImageEditsConnection(t, repo, upstream.URL, "sk-parity-test")
	handler := imageEditsHandler(repo)

	body, err := json.Marshal(map[string]any{
		"prompt":            "make it blue",
		"model":             "openai/dall-e-2",
		"image":             "https://example.com/test.png",
		"input_fidelity":    "high",
		"output_format":     "png",
		"output_compression": 80,
		"user":              "user-123",
		"background":        "transparent",
		"quality":           "hd",
	})
	if err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	for _, field := range []string{
		`"input_fidelity":"high"`,
		`"output_format":"png"`,
		`"output_compression":80`,
		`"user":"user-123"`,
		`"background":"transparent"`,
		`"quality":"hd"`,
	} {
		if !strings.Contains(gotBody, field) {
			t.Errorf("upstream body dropped %s: %s", field, gotBody)
		}
	}
}

// TestImageEditsParityAcceptsSDKImageShapes covers H4: the OpenAI SDK sends
// the image as a string, as an array, or as an object. Before the fix a JSON
// array hit "cannot unmarshal JSON array into Go string" and an object hit
// the same class of error, so both answered 400.
func TestImageEditsParityAcceptsSDKImageShapes(t *testing.T) {
	cases := []struct {
		name  string
		image any
	}{
		{"string", "https://example.com/a.png"},
		{"array", []string{"https://example.com/a.png", "https://example.com/b.png"}},
		{"object_image_url", map[string]any{
			"image_url": map[string]any{"url": "https://example.com/a.png"},
		}},
		{"object_url", map[string]any{
			"url": "https://example.com/a.png",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody, gotContentType string
			upstream := parityUpstream(t, &gotBody, &gotContentType)

			_, repo := setupImageEditsEnv(t)
			seedImageEditsConnection(t, repo, upstream.URL, "sk-parity-test")
			handler := imageEditsHandler(repo)

			body, err := json.Marshal(map[string]any{
				"prompt": "make it blue",
				"model":  "openai/dall-e-2",
				"image":  tc.image,
			})
			if err != nil {
				t.Fatalf("encode request body: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.HandleImageEdits(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(gotBody, "https://example.com/a.png") {
				t.Errorf("upstream body lost the image reference: %s", gotBody)
			}
		})
	}
}

// TestImageEditsParityRejectsFileID pins the one image shape upstream
// refuses: a file_id reference has no bytes to forward, so the answer must be
// 400 with the upstream message instead of a forwarded broken body.
func TestImageEditsParityRejectsFileID(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	_, repo := setupImageEditsEnv(t)
	seedImageEditsConnection(t, repo, upstream.URL, "sk-parity-test")
	handler := imageEditsHandler(repo)

	body, err := json.Marshal(map[string]any{
		"prompt": "make it blue",
		"model":  "openai/dall-e-2",
		"image":  map[string]any{"file_id": "file-abc"},
	})
	if err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "file_id references are not supported") {
		t.Errorf("error body = %s, want the upstream file_id message", rec.Body.String())
	}
	if upstreamCalls != 0 {
		t.Errorf("upstream was called %d times, want 0", upstreamCalls)
	}
}

// TestImageEditsParityForwardsJSONContentType covers H5 for the JSON lane:
// the gateway must present the forwarded body as application/json.
func TestImageEditsParityForwardsJSONContentType(t *testing.T) {
	var gotBody, gotContentType string
	upstream := parityUpstream(t, &gotBody, &gotContentType)

	_, repo := setupImageEditsEnv(t)
	seedImageEditsConnection(t, repo, upstream.URL, "sk-parity-test")
	handler := imageEditsHandler(repo)

	body, err := json.Marshal(map[string]any{
		"prompt": "make it blue",
		"model":  "openai/dall-e-2",
		"image":  "https://example.com/test.png",
	})
	if err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Errorf("upstream Content-Type = %q, want application/json", gotContentType)
	}
}

// TestImageEditsParityCapsJSONImageCount covers H6: the JSON lane must apply
// the same cap as the multipart lane. Before the fix the JSON lane had no
// cap at all while the multipart lane stopped at 8.
func TestImageEditsParityCapsJSONImageCount(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	_, repo := setupImageEditsEnv(t)
	seedImageEditsConnection(t, repo, upstream.URL, "sk-parity-test")
	handler := imageEditsHandler(repo)

	images := make([]string, 0, 17)
	for i := range 17 {
		images = append(images, fmt.Sprintf("https://example.com/%d.png", i))
	}
	body, err := json.Marshal(map[string]any{
		"prompt": "make it blue",
		"model":  "openai/dall-e-2",
		"image":  images,
	})
	if err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "At most 16 images are allowed") {
		t.Errorf("error body = %s, want the 16-image cap message", rec.Body.String())
	}
	if upstreamCalls != 0 {
		t.Errorf("upstream was called %d times, want 0", upstreamCalls)
	}
}

// TestImageEditsParityRejectsOversizedBody covers H7: a body over the limit
// must answer 413, not 400. The gateway has middleware.WriteBodyReadError
// for exactly this, but the JSON lane never called it, so an oversized body
// was reported as a malformed request.
//
// The limit is installed the way middleware.MaxBodyByPath does it (a
// http.MaxBytesReader plus the limit in the context) but with a small ceiling
// so the test allocates kilobytes instead of the 128MB production limit.
func TestImageEditsParityRejectsOversizedBody(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	_, repo := setupImageEditsEnv(t)
	seedImageEditsConnection(t, repo, upstream.URL, "sk-parity-test")
	handler := imageEditsHandler(repo)

	const limit = 512
	body, err := json.Marshal(map[string]any{
		"prompt": "make it blue",
		"model":  "openai/dall-e-2",
		"image":  "https://example.com/test.png",
		"user":   strings.Repeat("x", limit*2),
	})
	if err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.WithBodyLimit(req.Context(), limit))
	req.Body = http.MaxBytesReader(nil, req.Body, limit)
	rec := httptest.NewRecorder()
	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "request body too large") {
		t.Errorf("error body = %s, want the 413 ceiling message", rec.Body.String())
	}
	if upstreamCalls != 0 {
		t.Errorf("upstream was called %d times, want 0", upstreamCalls)
	}
}

// TestImageEditsParityAcceptsMixedCaseMultipart covers H9: the multipart
// dispatch must be case-insensitive. Before the fix a "Multipart/Form-Data"
// request fell into the JSON lane and died on "invalid character '-'".
func TestImageEditsParityAcceptsMixedCaseMultipart(t *testing.T) {
	var gotBody, gotContentType string
	upstream := parityUpstream(t, &gotBody, &gotContentType)

	_, repo := setupImageEditsEnv(t)
	seedImageEditsConnection(t, repo, upstream.URL, "sk-parity-test")
	handler := imageEditsHandler(repo)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("prompt", "make it blue")
	_ = mw.WriteField("model", "openai/dall-e-2")
	fw, err := mw.CreateFormFile("image", "a.png")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	_, _ = fw.Write([]byte("\x89PNG\r\n\x1a\nfake png bytes"))
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(buf.Bytes()))
	// Deliberately mixed case: mime.ParseMediaType lowercases the type.
	req.Header.Set("Content-Type", "Multipart/Form-Data; boundary="+mw.Boundary())
	rec := httptest.NewRecorder()
	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Errorf("upstream Content-Type = %q, want application/json (H5)", gotContentType)
	}
}

// TestImageEditsParityRejectsUnsupportedImageFormat covers H10: a non-raster
// payload must be refused. Before the fix any upload was coerced to image/png
// and forwarded, so a text file was sent to the provider as a PNG.
func TestImageEditsParityRejectsUnsupportedImageFormat(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	_, repo := setupImageEditsEnv(t)
	seedImageEditsConnection(t, repo, upstream.URL, "sk-parity-test")
	handler := imageEditsHandler(repo)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("prompt", "make it blue")
	_ = mw.WriteField("model", "openai/dall-e-2")
	fw, err := mw.CreateFormFile("image", "a.png")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	_, _ = fw.Write([]byte("this is plain text, not an image"))
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+mw.Boundary())
	rec := httptest.NewRecorder()
	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Unsupported image format") {
		t.Errorf("error body = %s, want the unsupported-format message", rec.Body.String())
	}
	if upstreamCalls != 0 {
		t.Errorf("upstream was called %d times, want 0", upstreamCalls)
	}
}

// TestImageEditsParityRejectsMask covers H11: no provider lane reads a mask,
// so accepting it and forwarding it advertises a feature that does not work.
// The answer must be 400 with the upstream message.
func TestImageEditsParityRejectsMask(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	_, repo := setupImageEditsEnv(t)
	seedImageEditsConnection(t, repo, upstream.URL, "sk-parity-test")
	handler := imageEditsHandler(repo)

	body, err := json.Marshal(map[string]any{
		"prompt": "make it blue",
		"model":  "openai/dall-e-2",
		"image":  "https://example.com/test.png",
		"mask":   "https://example.com/mask.png",
	})
	if err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "mask is not supported") {
		t.Errorf("error body = %s, want the upstream mask message", rec.Body.String())
	}
	if upstreamCalls != 0 {
		t.Errorf("upstream was called %d times, want 0", upstreamCalls)
	}
}
