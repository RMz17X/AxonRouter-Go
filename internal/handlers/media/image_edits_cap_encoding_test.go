package media

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	json "encoding/json/v2"
)

// TestImageEditsParityCapIndependentOfEncoding covers H6: the image-count
// cap must answer the same way whichever way the client spells the image.
//
// The JSON lane capped the count at 16 (the upstream MAX_IMAGES) while the
// global media guard capped it at MediaMaxItems (8) — but the global guard
// only measures media that carries bytes, so a URL image was measured as
// zero items and a data-URI image as one. The consequence was that the same
// nine-image request was forwarded as URLs and refused as data URIs, and the
// multipart lane refused at 8 while the JSON lane accepted 16.
//
// The count is now one number for the endpoint, and it is enforced before the
// encoding can change the answer.
func TestImageEditsParityCapIndependentOfEncoding(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	_, repo := setupImageEditsEnv(t)
	seedImageEditsConnection(t, repo, upstream.URL, "sk-parity-test")
	handler := imageEditsHandler(repo)

	const dataURI = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

	post := func(t *testing.T, refs []string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"prompt": "make it blue",
			"model":  "openai/dall-e-2",
			"image":  refs,
		})
		if err != nil {
			t.Fatalf("encode request body: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.HandleImageEdits(rec, req)
		return rec
	}

	urls := func(n int) []string {
		out := make([]string, 0, n)
		for i := range n {
			out = append(out, fmt.Sprintf("https://example.com/%d.png", i))
		}
		return out
	}
	duris := func(n int) []string {
		out := make([]string, 0, n)
		for range n {
			out = append(out, dataURI)
		}
		return out
	}

	// Under the ceiling: both encodings are accepted, and the answer does not
	// depend on how the image was spelled.
	for _, n := range []int{8, 9, maxImageEdits} {
		urlRec := post(t, urls(n))
		dataRec := post(t, duris(n))

		if urlRec.Code != http.StatusOK {
			t.Errorf("%d URL images: status = %d, want 200 (body: %s)", n, urlRec.Code, urlRec.Body.String())
		}
		if dataRec.Code != http.StatusOK {
			t.Errorf("%d data-URI images: status = %d, want 200 (body: %s)", n, dataRec.Code, dataRec.Body.String())
		}
	}

	// Over the ceiling: both encodings are refused the same way, with the
	// same message, and neither reaches the provider.
	before := upstreamCalls
	urlRec := post(t, urls(maxImageEdits+1))
	dataRec := post(t, duris(maxImageEdits+1))

	wantMsg := fmt.Sprintf("At most %d images are allowed", maxImageEdits)
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"URL images":      urlRec,
		"data-URI images": dataRec,
	} {
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s over the cap: status = %d, want 400 (body: %s)", name, rec.Code, rec.Body.String())
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte(wantMsg)) {
			t.Errorf("%s over the cap: body = %s, want %q", name, rec.Body.String(), wantMsg)
		}
	}
	if upstreamCalls != before {
		t.Errorf("upstream was called %d times for over-cap requests, want 0", upstreamCalls-before)
	}
}
