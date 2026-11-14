package media

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	json "encoding/json/v2"

	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/middleware"
)

const (
	maxImageEdits           = 16
	errUnsupportedImageMime = "Unsupported image format: use PNG, JPEG, WEBP or GIF"
)

// HandleImageEdits handles POST /v1/images/edits (multipart/form-data and application/json).
// It parses the request, validates required fields, converts image files or values
// to base64 Data URIs / URLs, packages the request as JSON, and forwards it via
// the existing media forwarding pipeline.
func (h *MediaHandler) HandleImageEdits(w http.ResponseWriter, r *http.Request) {
	if isMultipartRequest(r) {
		h.handleMultipartImageEdits(w, r)
		return
	}
	h.handleJSONImageEdits(w, r)
}

func (h *MediaHandler) handleJSONImageEdits(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		middleware.WriteBodyReadError(w, r, err)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var rawMap map[string]any
	if err := json.Unmarshal(body, &rawMap); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}
	if rawMap == nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if maskVal, ok := rawMap["mask"]; ok && maskVal != nil {
		if s, isStr := maskVal.(string); !isStr || strings.TrimSpace(s) != "" {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "mask is not supported")
			return
		}
	}
	delete(rawMap, "mask")

	prompt, _ := rawMap["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: prompt")
		return
	}
	rawMap["prompt"] = prompt

	var refs []string
	if err := collectJSONImageRefs(rawMap["images"], &refs); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := collectJSONImageRefs(rawMap["image"], &refs); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	delete(rawMap, "image")
	delete(rawMap, "images")

	if len(refs) == 0 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: image")
		return
	}
	if len(refs) > maxImageEdits {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("At most %d images are allowed", maxImageEdits))
		return
	}

	if nVal, ok := rawMap["n"]; ok {
		if nStr, isStr := nVal.(string); isStr {
			if nInt, err := strconv.Atoi(strings.TrimSpace(nStr)); err == nil {
				rawMap["n"] = nInt
			}
		}
	}
	if compVal, ok := rawMap["output_compression"]; ok {
		if compStr, isStr := compVal.(string); isStr {
			if compInt, err := strconv.Atoi(strings.TrimSpace(compStr)); err == nil {
				rawMap["output_compression"] = compInt
			}
		}
	}

	if len(refs) == 1 {
		rawMap["image"] = refs[0]
	} else {
		rawMap["images"] = refs
	}

	jsonBody, err := json.Marshal(rawMap)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, fmt.Sprintf("marshal request: %v", err))
		return
	}

	r.Header.Set("Content-Type", "application/json")
	h.forwardMediaRequest(w, r, jsonBody, "dall-e-2", "/v1/images/edits")
}

func parseJSONImageRef(val any) (string, error) {
	switch v := val.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return "", nil
		}
		return trimmed, nil
	case map[string]any:
		if fileID, hasFileID := v["file_id"]; hasFileID && fileID != nil {
			if s, isStr := fileID.(string); !isStr || strings.TrimSpace(s) != "" {
				return "", errors.New("file_id references are not supported")
			}
		}
		if imageURL, hasImageURL := v["image_url"]; hasImageURL {
			switch u := imageURL.(type) {
			case string:
				trimmed := strings.TrimSpace(u)
				if trimmed == "" {
					return "", errors.New("Each image must be a URL, data URL or { image_url }")
				}
				return trimmed, nil
			case map[string]any:
				if fileID, hasFileID := u["file_id"]; hasFileID && fileID != nil {
					if s, isStr := fileID.(string); !isStr || strings.TrimSpace(s) != "" {
						return "", errors.New("file_id references are not supported")
					}
				}
				if rawURL, ok := u["url"].(string); ok {
					trimmed := strings.TrimSpace(rawURL)
					if trimmed == "" {
						return "", errors.New("Each image must be a URL, data URL or { image_url }")
					}
					return trimmed, nil
				}
				return "", errors.New("Each image must be a URL, data URL or { image_url }")
			default:
				return "", errors.New("Each image must be a URL, data URL or { image_url }")
			}
		}
		if rawURL, hasURL := v["url"].(string); hasURL {
			trimmed := strings.TrimSpace(rawURL)
			if trimmed == "" {
				return "", errors.New("Each image must be a URL, data URL or { image_url }")
			}
			return trimmed, nil
		}
		return "", errors.New("Each image must be a URL, data URL or { image_url }")
	default:
		return "", errors.New("Each image must be a URL, data URL or { image_url }")
	}
}

func collectJSONImageRefs(val any, out *[]string) error {
	if val == nil {
		return nil
	}
	switch v := val.(type) {
	case []any:
		for _, item := range v {
			if err := collectJSONImageRefs(item, out); err != nil {
				return err
			}
		}
	default:
		ref, err := parseJSONImageRef(v)
		if err != nil {
			return err
		}
		if ref != "" {
			*out = append(*out, ref)
		}
	}
	return nil
}

func (h *MediaHandler) handleMultipartImageEdits(w http.ResponseWriter, r *http.Request) {
	const maxMemory = 32 << 20 // 32MB
	if err := r.ParseMultipartForm(maxMemory); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			middleware.WriteBodyReadError(w, r, err)
			return
		}
		handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid multipart form: %v", err))
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	if len(r.MultipartForm.File["mask"]) > 0 || len(r.MultipartForm.File["mask[]"]) > 0 ||
		len(r.MultipartForm.Value["mask"]) > 0 || len(r.MultipartForm.Value["mask[]"]) > 0 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "mask is not supported")
		return
	}

	for _, key := range []string{"image", "images", "image[]", "images[]"} {
		if vals := r.MultipartForm.Value[key]; len(vals) > 0 {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("%s must be a file upload", key))
			return
		}
	}

	reqBody := make(map[string]any)
	for key, vals := range r.MultipartForm.Value {
		if len(vals) == 0 {
			continue
		}
		trimmed := strings.TrimSpace(vals[len(vals)-1])
		if key == "n" || key == "output_compression" {
			if num, err := strconv.Atoi(trimmed); err == nil {
				reqBody[key] = num
				continue
			} else if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
				reqBody[key] = f
				continue
			}
		}
		reqBody[key] = trimmed
	}

	prompt, _ := reqBody["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: prompt")
		return
	}
	reqBody["prompt"] = prompt

	var imageFiles []*multipart.FileHeader
	for _, key := range []string{"image", "images", "image[]", "images[]"} {
		if files := r.MultipartForm.File[key]; len(files) > 0 {
			imageFiles = append(imageFiles, files...)
		}
	}

	if len(imageFiles) == 0 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: image (upload at least one image file)")
		return
	}

	if len(imageFiles) > int(MediaMaxItems) {
		handlerutil.WriteJSONError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("request contains %d media items; this endpoint accepts at most %d", len(imageFiles), MediaMaxItems))
		return
	}

	var parsedImages []string
	for i, fh := range imageFiles {
		uri, err := fileToDataURI(fh)
		if err != nil {
			if strings.Contains(err.Error(), "exceeds maximum") {
				handlerutil.WriteJSONError(w, http.StatusRequestEntityTooLarge, err.Error())
				return
			}
			if err.Error() == errUnsupportedImageMime {
				handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			handlerutil.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("failed to read image[%d]: %v", i, err))
			return
		}
		parsedImages = append(parsedImages, uri)
	}

	delete(reqBody, "image")
	delete(reqBody, "images")
	if len(parsedImages) == 1 {
		reqBody["image"] = parsedImages[0]
	} else {
		reqBody["images"] = parsedImages
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

func sniffImageMime(data []byte) string {
	if len(data) >= 8 && data[0] == 0x89 && string(data[1:4]) == "PNG" {
		return "image/png"
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg"
	}
	if len(data) >= 4 && string(data[:4]) == "GIF8" {
		return "image/gif"
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	return ""
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

	mimeType := sniffImageMime(data)
	if mimeType == "" {
		return "", errors.New(errUnsupportedImageMime)
	}

	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
