package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/config"
	"9router/proxy/internal/constants"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/proxy/executor"
	"9router/proxy/internal/proxy/poolstate"
	"9router/proxy/internal/speedlayer"
	"9router/proxy/internal/updater"
)

// System endpoints (batch 7, AxonRouter parity): GET
// /api/system/machine-id, /api/system/memory, /api/system/version.

// HandleSystemMachineID serves GET /api/system/machine-id: the consistent
// machine id (shared/utils/machineId.js) — sha256(raw machine id + salt),
// truncated to 16 hex chars, where the raw id persists in
// DATA_DIR/machine-id (the same file the CLI token derives from).
func HandleSystemMachineID(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"machineId": consistentMachineID(),
	})
}

// consistentMachineID mirrors getConsistentMachineId() with no explicit
// salt: the configured MACHINE_ID_SALT (default "endpoint-proxy-salt").
// Only the CLI-auth salt additionally mixes in the cli-secret file.
func consistentMachineID() string {
	cfg := config.LoadConfig()
	dir := config.ResolveDataDir()
	raw := readOrCreateHexFile(filepath.Join(dir, "machine-id"), 16)
	extra := ""
	if cfg.MachineIDSalt == "9r-cli-auth" {
		extra = readOrCreateHexFile(filepath.Join(dir, "cli-secret"), 32)
	}
	sum := sha256.Sum256([]byte(raw + cfg.MachineIDSalt + extra))
	return hex.EncodeToString(sum[:])[:16]
}

// readOrCreateHexFile mirrors auth.readOrCreateHexFile (kept local: the
// auth copy is unexported): load a hex secret file, generating and
// persisting n random bytes on first use.
func readOrCreateHexFile(path string, n int) string {
	if data, err := os.ReadFile(path); err == nil {
		if v := strings.TrimSpace(string(data)); v != "" {
			return v
		}
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	v := hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), constants.FilePermDir); err == nil {
		_ = os.WriteFile(path, []byte(v), constants.FilePermKey)
	}
	return v
}

// HandleSystemMemory serves GET /api/system/memory: data-dir and database
// sizes plus in-memory pool/session state sizes.
func HandleSystemMemory(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dataDir := config.ResolveDataDir()

		dbEngine := "PostgreSQL"
		dbSize := "N/A (PostgreSQL)"
		if pretty, err := repo.DatabaseSizePretty(); err == nil && pretty != "" {
			dbSize = pretty
		}

		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"dataDir":       dataDir,
			"dbEngine":      dbEngine,
			"dbSize":        dbSize,
			"dataDirSizeMB": formatMB(dirSizeBytes(dataDir, 4)),
			"inMemory": map[string]any{
				"fitnessPools": len(poolstate.SnapshotLive()),
				"geoPools":     poolstate.GeoPoolCount(),
				"freebuff":     executor.FreebuffSessionStateSize(),
			},
			// The speed layer's self-report (P3): mode, node identity,
			// and — in cluster mode — whether the breaker has degraded
			// serving onto the database backend. This is the dashboard
			// surface the failover rule requires; it rides the existing
			// system payload rather than growing a new one.
			"speedLayer": speedlayer.CurrentStatus(),
			// Usage-ledger writes discarded since boot (review F2):
			// the ledger lane is never droppable while the database is
			// reachable, so a non-zero value here means the database
			// was wedged past the enqueue wait — an operator must be
			// able to see that without reading warning logs.
			"usageWritesDropped": chat.UsageWritesDropped(),
		})
	}
}

// formatMB renders a byte count the way the upstream route does.
func formatMB(bytes int64) string {
	return fmt.Sprintf("%.2f MB", float64(bytes)/1048576)
}

// dirSizeBytes sums file sizes under dir, descending at most maxDepth
// directory levels (upstream dirSizeMB with maxDepth 4).
func dirSizeBytes(dir string, maxDepth int) int64 {
	var total int64
	var walk func(path string, depth int)
	walk = func(path string, depth int) {
		if depth > maxDepth {
			return
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return
		}
		for _, entry := range entries {
			full := filepath.Join(path, entry.Name())
			if entry.IsDir() {
				walk(full, depth+1)
				continue
			}
			if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
				total += info.Size()
			}
		}
	}
	walk(dir, 0)
	return total
}

// --- GET /api/system/version -------------------------------------------
//
// Ported from the upstream route: the current version, plus the newest
// semver tag published to the product's Docker Hub repository, cached
// for 12 hours. Registry failures degrade to { currentVersion,
// hasUpdate: false } (with the error message on transport failures),
// never an HTTP error.

var systemVersionCache = struct {
	sync.Mutex
	data      map[string]any
	expiresAt time.Time
}{}

var systemVersionClient = &http.Client{Timeout: 15 * time.Second}

var semverTagPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

// HandleSystemVersion serves GET /api/system/version.
func HandleSystemVersion(w http.ResponseWriter, r *http.Request) {
	currentVersion := updater.CurrentVersion

	systemVersionCache.Lock()
	if systemVersionCache.data != nil && time.Now().Before(systemVersionCache.expiresAt) {
		cached := systemVersionCache.data
		systemVersionCache.Unlock()
		handlerutil.WriteJSON(w, http.StatusOK, cached)
		return
	}
	systemVersionCache.Unlock()

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		"https://hub.docker.com/v2/repositories/rickicode/axonrouter/tags?page_size=100", nil)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"currentVersion": currentVersion, "hasUpdate": false, "error": err.Error(),
		})
		return
	}
	req.Header.Set("User-Agent", "AxonRouter/"+currentVersion)
	req.Header.Set("Accept", "application/json")

	resp, err := systemVersionClient.Do(req)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"currentVersion": currentVersion, "hasUpdate": false, "error": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"currentVersion": currentVersion, "hasUpdate": false,
		})
		return
	}

	var payload struct {
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || json.Unmarshal(raw, &payload) != nil {
		message := ""
		if err != nil {
			message = err.Error()
		} else {
			message = "invalid registry response"
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"currentVersion": currentVersion, "hasUpdate": false, "error": message,
		})
		return
	}

	// Only semver tags are comparable to the app version; branch/sha tags
	// are filtered out instead of parsing as 0.0.0 and masking an update.
	tags := make([]string, 0, len(payload.Results))
	for _, tag := range payload.Results {
		if semverTagPattern.MatchString(tag.Name) {
			tags = append(tags, tag.Name)
		}
	}
	sort.SliceStable(tags, func(i, j int) bool {
		return semverGreater(parseSemver(tags[i]), parseSemver(tags[j]))
	})
	latestVersion := currentVersion
	if len(tags) > 0 {
		latestVersion = tags[0]
	}

	result := map[string]any{
		"currentVersion": currentVersion,
		"latestVersion":  latestVersion,
		"hasUpdate":      semverGreater(parseSemver(latestVersion), parseSemver(currentVersion)),
		"releaseUrl":     "https://hub.docker.com/r/rickicode/axonrouter",
	}
	systemVersionCache.Lock()
	systemVersionCache.data = result
	systemVersionCache.expiresAt = time.Now().Add(12 * time.Hour)
	systemVersionCache.Unlock()

	handlerutil.WriteJSON(w, http.StatusOK, result)
}

// parseSemver mirrors the upstream parseSemver: strip a leading "v",
// split on ".", parseInt each part (leading digits; garbage is 0), pad
// to three parts.
func parseSemver(v string) [3]int {
	var out [3]int
	cleaned := strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(cleaned, ".")
	for i := 0; i < len(parts) && i < 3; i++ {
		out[i] = parseLeadingInt(parts[i])
	}
	return out
}

// parseLeadingInt is JS parseInt for decimal strings.
func parseLeadingInt(s string) int {
	end := 0
	if end < len(s) && (s[end] == '+' || s[end] == '-') {
		end++
	}
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0
	}
	return n
}

// semverGreater reports whether a > b component-wise.
func semverGreater(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}
