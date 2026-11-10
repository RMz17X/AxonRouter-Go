package handlers

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/shutdown"
	"axonrouter/proxy/internal/tracing"
)

// consolePingInterval matches the Next dashboard's 25s keepalive so proxies
// that idle-out connections keep the SSE stream alive.
const consolePingInterval = 25 * time.Second

// filterConsoleEntries narrows entries to one request's trail: only lines
// containing the request ID survive. Both renderings of the ID on a line —
// the access line's id=req_… and the context axis's req_id=req_… — contain
// the bare ID, so one substring match catches the whole trail.
func filterConsoleEntries(entries []log.ConsoleEntry, requestID string) []log.ConsoleEntry {
	if requestID == "" {
		return entries
	}
	out := make([]log.ConsoleEntry, 0, len(entries))
	for _, e := range entries {
		if strings.Contains(e.Line, requestID) {
			out = append(out, e)
		}
	}
	return out
}

// HandleConsoleLogsGet returns the buffered console log entries (translator
// console-logs GET). Each entry carries the level and arrival time known at
// emit time, so the dashboard can colour and timestamp rows without re-parsing
// rendered text. Upstream Next returned bare strings here; the Svelte
// dashboard is the only consumer of this endpoint.
//
// Optional ?requestId=req_… returns only that request's lines (see
// filterConsoleEntries); optional ?n= caps the result to the last n entries.
// The filter runs first and the cap second, so ?n= always means "the last n
// lines of the (possibly filtered) trail", never "filter within the last n".
//
// A requestId search runs against the console file store when it is
// enabled: a trail is usually far older than the 200-line ring, and the
// rotated files are exactly what the ring forgot. Without a requestId
// the ring answers, as before.
func HandleConsoleLogsGet(w http.ResponseWriter, r *http.Request) {
	var entries []log.ConsoleEntry
	if requestID := r.URL.Query().Get("requestId"); requestID != "" {
		if found, ok := log.SearchConsoleFile(requestID); ok {
			entries = found
		} else {
			entries = filterConsoleEntries(log.ConsoleEntries(), requestID)
		}
	} else {
		entries = log.ConsoleEntries()
	}
	if q := r.URL.Query().Get("n"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && len(entries) > n {
			entries = entries[len(entries)-n:]
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"logs":    entries,
	})
}

// HandleDebugTraces returns recent request spans + p50/p95/p99 latency per
// provider+model. Optional ?n= query caps the span list.
func HandleDebugTraces(w http.ResponseWriter, r *http.Request) {
	n := 0
	if q := r.URL.Query().Get("n"); q != "" {
		if v, err := strconv.Atoi(q); err == nil && v > 0 {
			n = v
		}
	}
	body, err := tracing.JSON(n)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

// HandleConsoleLogsDelete clears the buffered console logs.
func HandleConsoleLogsDelete(w http.ResponseWriter, r *http.Request) {
	log.ClearConsoleLogs()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleConsoleLogsLevelGet returns the current server log level so the
// dashboard console-log page can show it without a restart.
func HandleConsoleLogsLevelGet(w http.ResponseWriter, r *http.Request) {
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"level":   log.LevelString(),
	})
}

// HandleConsoleLogsLevelPut changes the server log level at runtime
// (no restart needed). Body: {"level": "debug"|"info"|"warn"|"error"}.
func HandleConsoleLogsLevelPut(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Level string `json:"level"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Level == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing log level")
		return
	}
	lvl, ok := log.ParseLevel(req.Level)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid log level (debug, info, warn, error)")
		return
	}
	log.SetLevel(lvl)
	level := log.LevelString()
	log.Info("server", "log level changed", "level", level)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"level":   level,
	})
}

// HandleConsoleLogsStream streams live console output over SSE. On connect it
// sends the buffered entries as an "init" event, then one "line" event per
// captured entry as they arrive and a "clear" event on buffer clear, with a
// keepalive ping every 25s. Every payload carries {time, level, line}, so the
// dashboard never has to guess a severity from the message text.
func HandleConsoleLogsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(v map[string]any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return false
		}
		if _, err := w.Write([]byte("data: " + string(b) + "\n\n")); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Optional ?requestId= narrows the stream to one request's trail, with
	// the same substring rule as the GET (see filterConsoleEntries).
	requestID := r.URL.Query().Get("requestId")

	// Buffered entries first.
	if buffered := filterConsoleEntries(log.ConsoleEntries(), requestID); len(buffered) > 0 {
		if !send(map[string]any{"type": "init", "entries": buffered}) {
			return
		}
	}

	ch, cancel := log.SubscribeConsole()
	defer cancel()

	ping := time.NewTicker(consolePingInterval)
	defer ping.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-shutdown.Done():
			// Server is stopping: close the endless stream now instead of
			// holding server.Shutdown until its deadline (the browser tab
			// behind this connection never goes idle on its own).
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			switch ev.Kind() {
			case "clear":
				if !send(map[string]any{"type": "clear"}) {
					return
				}
			default:
				if requestID != "" && !strings.Contains(ev.Entry().Line, requestID) {
					continue
				}
				if !send(map[string]any{"type": "line", "entry": ev.Entry()}) {
					return
				}
			}
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
