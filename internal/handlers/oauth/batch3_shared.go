package oauth

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
)

// Batch 3 (AxonRouter backend parity): the OAuth import / auto-import /
// dynamic-dispatch routes. Axon's routes answer errors in the flat
// {error: "message"} shape — not the dashboard's nested envelope — so these
// handlers write errors through axonError while reusing the established
// connection storage machinery everywhere else.

// axonError writes Axon's flat error body: {"error": "<message>"}.
func axonError(w http.ResponseWriter, status int, message string) {
	handlerutil.WriteJSON(w, status, map[string]any{"error": message})
}

// axonNowISO formats now the way JS Date.toISOString() does (millisecond
// precision, Z suffix) for the timestamp fields Axon's payloads carry.
func axonNowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// axonExpiresISO is axonNowISO offset by a duration (token expiry fields).
func axonExpiresISO(d time.Duration) string {
	return time.Now().Add(d).UTC().Format("2006-01-02T15:04:05.000Z")
}

// captureWriter buffers a handler's response so a delegating handler can
// reshape it. The dedicated per-provider handlers in this package already run
// the real authorize/exchange flows; the generic {provider}/{action} route
// reuses them through this writer instead of duplicating their logic.
type captureWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newCaptureWriter() *captureWriter {
	return &captureWriter{header: http.Header{}, status: http.StatusOK}
}

func (c *captureWriter) Header() http.Header         { return c.header }
func (c *captureWriter) WriteHeader(status int)      { c.status = status }
func (c *captureWriter) Write(b []byte) (int, error) { return c.body.Write(b) }

// runCaptured invokes fn against a buffered writer and decodes the JSON body
// it produced. A non-JSON body yields a nil map with the raw status.
func runCaptured(fn func(w http.ResponseWriter)) (int, map[string]any) {
	cw := newCaptureWriter()
	fn(cw)
	var out map[string]any
	if cw.body.Len() > 0 {
		_ = json.Unmarshal(cw.body.Bytes(), &out)
	}
	return cw.status, out
}

// capturedErrorMessage extracts the message from a captured error body,
// tolerating both the dashboard envelope ({error:{message}}) and flat or
// string shapes.
func capturedErrorMessage(body map[string]any) string {
	if body == nil {
		return ""
	}
	switch e := body["error"].(type) {
	case string:
		return e
	case map[string]any:
		if msg, _ := e["message"].(string); msg != "" {
			return msg
		}
	}
	return ""
}

// saveAxonConnection upserts one provider connection row in the shape Axon's
// createProviderConnection produces: the credential payload lives in the data
// blob, name/email are real columns (the dashboard list flattens columns over
// the blob), and a stored refresh token survives an update whose new payload
// omits one. A nil priority resolves to the provider's next priority.
func (h *OAuthHandler) saveAxonConnection(id, provider, authType, name, email string, priority *int, dataMap map[string]any) error {
	if h.Repo == nil || h.Repo.RawDB() == nil {
		return fmt.Errorf("connection store unavailable")
	}
	db := h.Repo.RawDB()
	var existing string
	if err := db.QueryRow("SELECT data FROM providerConnections WHERE id = ?", id).Scan(&existing); err == nil && existing != "" {
		if _, has := dataMap["refreshToken"]; !has {
			var old map[string]any
			if jerr := json.Unmarshal([]byte(existing), &old); jerr == nil {
				if rt, ok := old["refreshToken"].(string); ok && rt != "" {
					dataMap["refreshToken"] = rt
				}
			}
		}
		dataBytes, err := json.Marshal(dataMap)
		if err != nil {
			return fmt.Errorf("marshal connection data: %w", err)
		}
		_, err = db.Exec(
			"UPDATE providerConnections SET name = ?, email = ?, data = ?, updatedAt = ? WHERE id = ?",
			name, nullString(email), string(dataBytes), currentTimestamp(), id,
		)
		return err
	}
	dataBytes, err := json.Marshal(dataMap)
	if err != nil {
		return fmt.Errorf("marshal connection data: %w", err)
	}
	prio := 1
	if priority != nil {
		prio = *priority
	} else {
		prio = h.Repo.ResolveDefaultPriority(provider)
	}
	now := currentTimestamp()
	_, err = db.Exec(
		"INSERT INTO providerConnections (id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?)",
		id, provider, authType, name, nullString(email), prio, string(dataBytes), now, now,
	)
	return err
}

// nullString maps "" to a SQL NULL so optional columns stay null like Axon's.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// connectionEmail reads a stored connection's email: the column wins (it is
// what the dashboard list shows), the data blob is the fallback for rows the
// older handlers wrote.
func connectionEmail(data map[string]any, column *string) string {
	if column != nil && *column != "" {
		return *column
	}
	if data != nil {
		if e, _ := data["email"].(string); e != "" {
			return e
		}
	}
	return ""
}

// decodeJWTPayload decodes a JWT's payload claims without verification —
// the import flows only read identity claims (email, account, plan) from
// tokens the user pastes, exactly like Axon's import routes.
func decodeJWTPayload(token string) map[string]any {
	claims, ok := decodeJWTClaims(token)
	if !ok {
		return nil
	}
	return claims
}

// jsonStringField reads a trimmed string field from a decoded JSON body,
// tolerating non-string values the way Axon's typeof checks do (→ "").
func jsonStringField(body map[string]any, key string) string {
	if body == nil {
		return ""
	}
	if s, ok := body[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}
