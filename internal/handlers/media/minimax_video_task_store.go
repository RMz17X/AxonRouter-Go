package media

import (
	"encoding/json"
	"time"
)

// Matrix query requires the same model that submitted the task. The client
// only owns the opaque task id, so the router keeps that association in the
// KV table. Entries are bounded by age and count to avoid an unbounded row
// set on a long-running gateway.
const (
	miniMaxVideoTaskScope   = "minimaxMatrixVideoTasks"
	miniMaxVideoTaskTTL     = 2 * time.Hour
	miniMaxVideoTaskMaxRows = 512
)

type miniMaxVideoTaskMeta struct {
	Model string `json:"model"`
	At    int64  `json:"at"`
}

func miniMaxMatrixTaskID(body []byte) string {
	var payload struct {
		TaskID string `json:"task_id"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	return payload.TaskID
}

func (h *MediaHandler) rememberMiniMaxVideoTask(taskID, model string) error {
	entries, err := h.Repo.GetKVScope(miniMaxVideoTaskScope)
	if err != nil {
		return err
	}
	now := time.Now()
	for key, raw := range entries {
		var meta miniMaxVideoTaskMeta
		if json.Unmarshal([]byte(raw), &meta) == nil && now.Sub(time.Unix(meta.At, 0)) <= miniMaxVideoTaskTTL {
			continue
		}
		_ = h.Repo.DeleteKV(miniMaxVideoTaskScope, key)
		delete(entries, key)
	}
	if len(entries) >= miniMaxVideoTaskMaxRows {
		// Oldest first: the map is small and this is a maintenance path.
		var oldestKey string
		var oldestAt int64
		for key, raw := range entries {
			var meta miniMaxVideoTaskMeta
			if json.Unmarshal([]byte(raw), &meta) != nil {
				oldestKey, oldestAt = key, 0
				break
			}
			if oldestKey == "" || meta.At < oldestAt {
				oldestKey, oldestAt = key, meta.At
			}
		}
		if oldestKey != "" {
			_ = h.Repo.DeleteKV(miniMaxVideoTaskScope, oldestKey)
		}
	}
	value, err := json.Marshal(miniMaxVideoTaskMeta{Model: model, At: now.Unix()})
	if err != nil {
		return err
	}
	return h.Repo.SetKV(miniMaxVideoTaskScope, taskID, string(value))
}

func (h *MediaHandler) recallMiniMaxVideoModel(taskID string) string {
	entries, err := h.Repo.GetKVScope(miniMaxVideoTaskScope)
	if err != nil {
		return miniMaxMatrixH3Model
	}
	raw, ok := entries[taskID]
	if !ok {
		return miniMaxMatrixH3Model
	}
	var meta miniMaxVideoTaskMeta
	if json.Unmarshal([]byte(raw), &meta) != nil || meta.Model == "" ||
		time.Since(time.Unix(meta.At, 0)) > miniMaxVideoTaskTTL {
		return miniMaxMatrixH3Model
	}
	return meta.Model
}
