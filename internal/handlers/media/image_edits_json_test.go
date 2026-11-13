package media

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "encoding/json/v2"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/db/dbtest"
	"axonrouter/proxy/internal/handlers/chat"
)

// The JSON lane of POST /v1/images/edits is the OpenAI-compatible shape: a
// client posts {"prompt":…, "image":…} as JSON instead of a multipart form.
// It is the only shape an OpenAI SDK sends when the caller hands it a URL or a
// base64 string rather than a file handle, so a gateway that only answers the
// multipart form fails those clients with a 400 they cannot act on.
//
// The suite lives in the untagged build (unlike the sqlite_legacy-quarantined
// media suites) because it runs against a real PostgreSQL schema: the handler
// resolves a provider, picks a connection row and reads the node registry, so
// a mocked store would prove nothing about the lane that actually serves the
// request. The database is provisioned by internal/db/dbtest, which skips the
// test when no scratch database is configured and fails hard when one is
// configured but unreachable.

// setupImageEditsEnv provisions a throwaway PostgreSQL schema with the real
// core schema and returns a repo over it plus the cleanup.
func setupImageEditsEnv(t *testing.T) (*sql.DB, *db.Repo) {
	t.Helper()
	// The usage writer runs asynchronously in production; inline it here so the
	// row lands before the schema is dropped at cleanup. Left asynchronous, the
	// write outlives the schema and the log fills with "database is closed"
	// errors that say nothing about the lane under test.
	chat.SetUsageWritesSynchronousForTest(true)
	t.Cleanup(func() { chat.SetUsageWritesSynchronousForTest(false) })

	conn, _, cleanup, err := dbtest.NewSchemaHandle("media")
	if err != nil {
		if errors.Is(err, dbtest.ErrNoDatabase) {
			t.Skipf("skipping database-backed test: %v", err)
		}
		t.Fatalf("provision test database: %v", err)
	}
	t.Cleanup(cleanup)
	if err := db.EnsureCoreSchema(conn); err != nil {
		t.Fatalf("ensure core schema: %v", err)
	}
	return conn, db.NewRepo(conn)
}

// seedImageEditsConnection stores one active OpenAI connection whose base URL
// points at the fake upstream.
func seedImageEditsConnection(t *testing.T, repo *db.Repo, upstreamURL, apiKey string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"apiKey":  apiKey,
		"baseUrl": upstreamURL + "/v1",
	})
	if err != nil {
		t.Fatalf("encode connection data: %v", err)
	}
	priority := 1
	if err := repo.CreateProviderConnectionFull("conn-images", "openai", "apikey", "Images Test", &priority, string(data)); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
}

// imageEditsHandler wires a media handler over the real repo, the way the
// router does.
func imageEditsHandler(repo *db.Repo) *MediaHandler {
	return NewMediaHandler(repo, nil, chat.NewChatHandler(repo))
}

// TestImageEditsJSONForwardsToUpstream is the positive case: a JSON body with
// a prompt and an image reaches the upstream on the images/edits route with
// the parsed fields intact, and the upstream's answer comes back to the client.
func TestImageEditsJSONForwardsToUpstream(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.String()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="}]}`))
	}))
	defer upstream.Close()

	_, repo := setupImageEditsEnv(t)
	seedImageEditsConnection(t, repo, upstream.URL, "sk-images-test")

	handler := imageEditsHandler(repo)

	payload := map[string]any{
		"prompt": "make it blue",
		"model":  "openai/dall-e-2",
		"image":  "https://example.com/test.png",
		"size":   "1024x1024",
		"n":      1,
	}
	body, err := json.Marshal(payload)
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
	if gotPath != "/v1/images/edits" {
		t.Errorf("upstream path = %q, want /v1/images/edits", gotPath)
	}
	if gotAuth != "Bearer sk-images-test" {
		t.Errorf("upstream Authorization = %q, want the connection's bearer key", gotAuth)
	}
	if !strings.Contains(gotBody, `"prompt":"make it blue"`) {
		t.Errorf("upstream body lost the prompt: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"image":"https://example.com/test.png"`) {
		t.Errorf("upstream body lost the image: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"size":"1024x1024"`) {
		t.Errorf("upstream body lost the size: %s", gotBody)
	}
	if !strings.Contains(rec.Body.String(), "b64_json") {
		t.Errorf("client did not receive the upstream payload: %s", rec.Body.String())
	}
}

// TestImageEditsJSONAcceptsDataURI is the other shape an OpenAI SDK sends: the
// image arrives as a base64 data URI in the JSON body rather than a URL. The
// lane must forward it verbatim — the upstream decodes it, not the gateway.
func TestImageEditsJSONAcceptsDataURI(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.String()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"AAAA"}]}`))
	}))
	defer upstream.Close()

	_, repo := setupImageEditsEnv(t)
	seedImageEditsConnection(t, repo, upstream.URL, "sk-images-test")

	handler := imageEditsHandler(repo)

	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("fake png bytes"))
	body, err := json.Marshal(map[string]any{
		"prompt": "make it blue",
		"model":  "openai/dall-e-2",
		"image":  dataURI,
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
	if !strings.Contains(gotBody, dataURI) {
		t.Errorf("upstream body lost the data URI: %s", gotBody)
	}
}

// TestImageEditsJSONRejectsMissingFields pins the two validation failures a
// client can hit. Both must answer 400 with the OpenAI error envelope rather
// than reaching the upstream with a half-built request.
func TestImageEditsJSONRejectsMissingFields(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	_, repo := setupImageEditsEnv(t)
	seedImageEditsConnection(t, repo, upstream.URL, "sk-images-test")

	handler := imageEditsHandler(repo)

	cases := []struct {
		name      string
		payload   map[string]any
		wantField string
	}{
		{
			name:      "missing prompt",
			payload:   map[string]any{"model": "openai/dall-e-2", "image": "https://example.com/test.png"},
			wantField: "Missing required field: prompt",
		},
		{
			name:      "missing image",
			payload:   map[string]any{"model": "openai/dall-e-2", "prompt": "make it blue"},
			wantField: "Missing required field: image",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.payload)
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
			if !strings.Contains(rec.Body.String(), tc.wantField) {
				t.Errorf("body %q does not name %q", rec.Body.String(), tc.wantField)
			}
		})
	}

	if upstreamCalls != 0 {
		t.Errorf("upstream received %d requests, want 0 — an invalid request must not reach the provider", upstreamCalls)
	}
}

// TestImageEditsMultipartStillWorks is the regression guard for the refactor:
// the JSON lane was added beside the multipart lane, not in place of it, so a
// multipart upload must still parse its file into a data URI and forward.
func TestImageEditsMultipartStillWorks(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.String()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"AAAA"}]}`))
	}))
	defer upstream.Close()

	_, repo := setupImageEditsEnv(t)
	seedImageEditsConnection(t, repo, upstream.URL, "sk-images-test")

	handler := imageEditsHandler(repo)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("prompt", "make it blue")
	_ = mw.WriteField("model", "openai/dall-e-2")
	fw, err := mw.CreateFormFile("image", "test.png")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	_, _ = fw.Write([]byte("fake png bytes"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()

	handler.HandleImageEdits(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, `"image":"data:image/`) {
		t.Errorf("multipart image was not converted to a data URI: %s", gotBody)
	}
}
