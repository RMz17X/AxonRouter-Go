package media

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	json "encoding/json/v2"

	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/middleware"
)

// HandleImageEdits handles POST /v1/images/edits (multipart/form-data and application/json).
// It parses the request, validates required fields, converts image/mask files
// or values to base64 Data URIs / URLs, packages the request as JSON, and forwards
// it via the existing media forwarding pipeline.
func (h *MediaHandler) HandleImageEdits(w http.ResponseWriter, r *http.Request) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		h.handleMultipartImageEdits(w, r)
		return
	}
	h.handleJSONImageEdits(w, r)
}

func (h *MediaHandler) handleJSONImageEdits(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	var req struct {
		Prompt         string   `json:"prompt"`
		Image          string   `json:"image"`
		Images         []string `json:"images,omitempty"`
		Mask           string   `json:"mask,omitempty"`
		Model          string   `json:"model,omitempty"`
		Size           string   `json:"size,omitempty"`
		ResponseFormat string   `json:"response_format,omitempty"`
		Background     string   `json:"background,omitempty"`
		Quality        string   `json:"quality,omitempty"`
		N              int      `json:"n,omitempty"`
	}

	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: prompt")
		return
	}

	image := strings.TrimSpace(req.Image)
	if image == "" && len(req.Images) > 0 {
		image = strings.TrimSpace(req.Images[0])
		req.Images = req.Images[1:]
	}
	if image == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: image")
		return
	}

	reqBody := map[string]any{
		"prompt": prompt,
		"image":  image,
	}
	if req.Model != "" {
		reqBody["model"] = strings.TrimSpace(req.Model)
	}
	if req.Size != "" {
		reqBody["size"] = strings.TrimSpace(req.Size)
	}
	if req.ResponseFormat != "" {
		reqBody["response_format"] = strings.TrimSpace(req.ResponseFormat)
	}
	if req.Background != "" {
		reqBody["background"] = strings.TrimSpace(req.Background)
	}
	if req.Quality != "" {
		reqBody["quality"] = strings.TrimSpace(req.Quality)
	}
	if len(req.Images) > 0 {
		reqBody["images"] = req.Images
	}
	if req.Mask != "" {
		reqBody["mask"] = strings.TrimSpace(req.Mask)
	}
	if req.N > 0 {
		reqBody["n"] = req.N
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("marshal request: %v", err))
		return
	}

	h.forwardMediaRequest(w, r, jsonBody, "dall-e-2", "/v1/images/edits")
}

func (h *MediaHandler) handleMultipartImageEdits(w http.ResponseWriter, r *http.Request) {
	const maxMemory = 32 << 20 // 32MB
	if err := r.ParseMultipartForm(maxMemory); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid multipart form: %v", err))
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	prompt := strings.TrimSpace(r.FormValue("prompt"))
	if prompt == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: prompt")
		return
	}

	var imageFiles []*multipart.FileHeader
	for _, key := range []string{"image", "images", "image[]", "images[]"} {
		if files := r.MultipartForm.File[key]; len(files) > 0 {
			imageFiles = append(imageFiles, files...)
		}
	}
	if len(imageFiles) > int(MediaMaxItems) {
		handlerutil.WriteJSONError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("request contains %d media items; this endpoint accepts at most %d", len(imageFiles), MediaMaxItems))
		return
	}

	var primaryImage string
	var images []string

	if len(imageFiles) > 0 {
		var err error
		primaryImage, err = fileToDataURI(imageFiles[0])
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("failed to read image: %v", err))
			return
		}
		for i := 1; i < len(imageFiles); i++ {
			uri, err := fileToDataURI(imageFiles[i])
			if err != nil {
				handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("failed to read image[%d]: %v", i, err))
				return
			}
			images = append(images, uri)
		}
	} else {
		for _, key := range []string{"image", "images", "image[]", "images[]"} {
			if vals := r.MultipartForm.Value[key]; len(vals) > 0 {
				for _, v := range vals {
					if trimmed := strings.TrimSpace(v); trimmed != "" {
						if primaryImage == "" {
							primaryImage = trimmed
						} else {
							images = append(images, trimmed)
						}
					}
				}
			}
		}
	}

	if primaryImage == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: image (upload at least one image file)")
		return
	}

	var mask string
	var maskFiles []*multipart.FileHeader
	for _, key := range []string{"mask", "mask[]"} {
		if files := r.MultipartForm.File[key]; len(files) > 0 {
			maskFiles = append(maskFiles, files...)
		}
	}
	if len(maskFiles) > 0 {
		maskURI, err := fileToDataURI(maskFiles[0])
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("failed to read mask: %v", err))
			return
		}
		mask = maskURI
	} else if maskVal := strings.TrimSpace(r.FormValue("mask")); maskVal != "" {
		mask = maskVal
	}

	model := strings.TrimSpace(r.FormValue("model"))
	size := strings.TrimSpace(r.FormValue("size"))
	responseFormat := strings.TrimSpace(r.FormValue("response_format"))
	background := strings.TrimSpace(r.FormValue("background"))
	quality := strings.TrimSpace(r.FormValue("quality"))

	n := 0
	if nStr := strings.TrimSpace(r.FormValue("n")); nStr != "" {
		_, _ = fmt.Sscanf(nStr, "%d", &n)
	}

	reqBody := map[string]any{
		"prompt":          prompt,
		"image":           primaryImage,
		"model":           model,
		"size":            size,
		"response_format": responseFormat,
		"background":      background,
		"quality":         quality,
	}
	if len(images) > 0 {
		reqBody["images"] = images
	}
	if mask != "" {
		reqBody["mask"] = mask
	}
	if n > 0 {
		reqBody["n"] = n
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("marshal request: %v", err))
		return
	}

	// Update Content-Type so downstream guardMediaInput and upstream forwarding
	// treat the body as JSON rather than attempting to parse jsonBody as multipart.
	r.Header.Set("Content-Type", "application/json")
	h.forwardMediaRequest(w, r, jsonBody, "dall-e-2", "/v1/images/edits")
}

func fileToDataURI(fh *multipart.FileHeader) (string, error) {
	if fh.Size > MediaMaxItemBytes {
		return "", fmt.Errorf("file size %s exceeds maximum allowed %s",
			middleware.HumanBytes(fh.Size), middleware.HumanBytes(MediaMaxItemBytes))
	}
	f, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	lr := io.LimitReader(f, MediaMaxItemBytes+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return "", err
	}
	if int64(len(data)) > MediaMaxItemBytes {
		return "", fmt.Errorf("file size exceeds maximum allowed %s", middleware.HumanBytes(MediaMaxItemBytes))
	}

	mimeType := fh.Header.Get("Content-Type")
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mimeType, "image/") {
		exts, _ := mime.ExtensionsByType(mimeType)
		if len(exts) > 0 && strings.HasPrefix(exts[0], ".image") {
		} else {
			mimeType = "image/png"
		}
	}

	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
