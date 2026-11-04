package dashboard

import (
	"context"
	stdjson "encoding/json"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"9router/proxy/internal/clisetup"
	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// OpenCode sync model catalog. Port of Axon's
// src/lib/opencodeSync/modelCatalog.ts against the Go stores:
// combos first, then one connection per provider (explicit enabled
// models or the static registry list, with a live /models fetch for
// compatible providers that carry none), or the whole static
// registry when no connection exists.
//
// Known divergence (see the batch fragment): Axon's PROVIDER_MODELS
// entries are rich objects (name/label/contextWindow/costs); the Go
// registry carries bare model ids, so catalog entries built from it
// fall back to name = id and carry no limit/cost fields.

var ocUpstreamConnectionRE = regexp.MustCompile(`(?i)[-_][0-9a-f]{8,}$`)

// ocCatalogEntry ports buildCatalogEntry. source is a decoded
// connection/combo/static-model object (may be nil).
func ocCatalogEntry(modelID, ownedBy string, source *clisetup.JObj) *clisetup.JObj {
	provider := ownedBy
	root := modelID
	if idx := strings.Index(modelID, "/"); idx >= 0 {
		if provider == "" {
			provider = modelID[:idx]
		}
		root = modelID[idx+1:]
	}
	name := modelID
	if s := ocTrim(source.Get("name")); s != "" {
		name = s
	} else if s := ocTrim(source.Get("label")); s != "" {
		name = s
	}
	entry := clisetup.NewJObj(
		"id", modelID,
		"name", name,
	)
	if s := ocTrim(source.Get("label")); s != "" {
		entry.Set("label", s)
	}
	entry.Set("provider", provider)
	entry.Set("root", root)
	entry.Set("owned_by", provider)
	entry.Set("object", "model")
	for _, key := range []string{"contextWindow", "maxInputTokens", "maxOutputTokens", "inputCost", "outputCost"} {
		if v := source.Get(key); v != nil {
			entry.Set(key, v)
		}
	}
	for _, key := range []string{"tags", "capabilities"} {
		if arr, ok := source.Get(key).([]any); ok {
			entry.Set(key, ocStableClone(arr))
		}
	}
	return entry
}

type ocConnectionData struct {
	apiKey    string
	prefix    string
	baseURL   string
	enabled   []string
	hasModels bool
}

func ocParseConnectionData(raw string) ocConnectionData {
	out := ocConnectionData{}
	v, err := clisetup.ParseOrderedJSON([]byte(raw))
	if err != nil {
		return out
	}
	obj, _ := ocPlain(v)
	out.apiKey, _ = obj.Get("apiKey").(string)
	psd, _ := ocPlain(obj.Get("providerSpecificData"))
	out.prefix = ocTrim(psd.Get("prefix"))
	out.baseURL = ocTrim(psd.Get("baseUrl"))
	if arr, ok := psd.Get("enabledModels").([]any); ok {
		out.hasModels = len(arr) > 0
		for _, item := range arr {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out.enabled = append(out.enabled, s)
			}
		}
		// Axon dedupes the enabled list.
		seen := map[string]bool{}
		deduped := out.enabled[:0]
		for _, s := range out.enabled {
			if !seen[s] {
				seen[s] = true
				deduped = append(deduped, s)
			}
		}
		out.enabled = deduped
	}
	return out
}

func ocIsOpenAICompatible(providerID string) bool {
	return strings.HasPrefix(providerID, "openai-compatible-")
}

func ocIsAnthropicCompatible(providerID string) bool {
	return strings.HasPrefix(providerID, "anthropic-compatible-")
}

// ocFetchCompatibleModelIDs ports fetchCompatibleModelIds: a 5s
// live GET {baseUrl}/models for compatible providers.
func ocFetchCompatibleModelIDs(client *http.Client, providerID string, data ocConnectionData) []string {
	if data.apiKey == "" || data.baseURL == "" {
		return nil
	}
	base := strings.TrimSuffix(data.baseURL, "/")
	url := base + "/models"
	headers := map[string]string{"Content-Type": "application/json"}
	if ocIsOpenAICompatible(providerID) {
		headers["Authorization"] = "Bearer " + data.apiKey
	} else if ocIsAnthropicCompatible(providerID) {
		// Literal port of the JS slice(0, -9) munging.
		if strings.HasSuffix(url, "/messages/models") {
			url = url[:len(url)-9]
		} else if strings.HasSuffix(url, "/messages") {
			url = url[:len(url)-9] + "/models"
		}
		headers["x-api-key"] = data.apiKey
		headers["anthropic-version"] = "2023-06-01"
		headers["Authorization"] = "Bearer " + data.apiKey
	} else {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil
	}
	var parsed any
	if err := stdjson.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	var rawList []any
	switch t := parsed.(type) {
	case []any:
		rawList = t
	case map[string]any:
		for _, key := range []string{"data", "models", "results"} {
			if arr, ok := t[key].([]any); ok {
				rawList = arr
				break
			}
		}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, item := range rawList {
		m, _ := item.(map[string]any)
		var id string
		for _, key := range []string{"id", "name", "model"} {
			if s, ok := m[key].(string); ok && s != "" {
				id = s
				break
			}
		}
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// ocLoadCatalog ports loadAxonRouterModelCatalog: the reduced
// id → entry map, in build order.
func ocLoadCatalog(repo *db.Repo, client *http.Client) *ocCatalog {
	catalog := &ocCatalog{entries: map[string]*clisetup.JObj{}}
	add := func(entry *clisetup.JObj) {
		id, _ := entry.Get("id").(string)
		if id == "" {
			return
		}
		if !catalog.has(id) {
			catalog.order = append(catalog.order, id)
		}
		catalog.entries[id] = entry
	}

	combos, _ := repo.GetCombos()
	for _, combo := range combos {
		name := strings.TrimSpace(combo.Name)
		if name == "" {
			continue
		}
		add(ocCatalogEntry(name, "combo", clisetup.NewJObj("name", combo.Name)))
	}

	connections, err := repo.GetProviderConnections("", true)
	if err != nil {
		connections = nil
	}

	if len(connections) == 0 {
		aliases := make([]string, 0, len(providers.ProviderModels))
		for alias := range providers.ProviderModels {
			aliases = append(aliases, alias)
		}
		ocSortStrings(aliases)
		for _, alias := range aliases {
			for _, rootID := range providers.ProviderModels[alias] {
				rootID = strings.TrimSpace(rootID)
				if rootID == "" {
					continue
				}
				add(ocCatalogEntry(alias+"/"+rootID, alias, nil))
			}
		}
		return ocNormalizeCatalog(catalog)
	}

	firstByProvider := map[string]int{}
	providerOrder := []string{}
	for i, conn := range connections {
		if _, seen := firstByProvider[conn.Provider]; !seen {
			firstByProvider[conn.Provider] = i
			providerOrder = append(providerOrder, conn.Provider)
		}
	}
	for _, providerID := range providerOrder {
		conn := connections[firstByProvider[providerID]]
		data := ocParseConnectionData(conn.Data)
		staticAlias := providers.GetProviderAlias(providerID)
		outputAlias := data.prefix
		if outputAlias == "" {
			outputAlias = staticAlias
		}
		staticIDs := providers.GetProviderModels(staticAlias)

		var rawIDs []string
		if data.hasModels {
			rawIDs = data.enabled
		} else {
			rawIDs = staticIDs
		}
		compatible := ocIsOpenAICompatible(providerID) || ocIsAnthropicCompatible(providerID)
		if compatible && len(rawIDs) == 0 && !ocUpstreamConnectionRE.MatchString(providerID) {
			rawIDs = ocFetchCompatibleModelIDs(client, providerID, data)
		}
		for _, rawID := range rawIDs {
			id := rawID
			for _, prefix := range []string{outputAlias, staticAlias, providerID} {
				if strings.HasPrefix(id, prefix+"/") {
					id = id[len(prefix)+1:]
					break
				}
			}
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			add(ocCatalogEntry(outputAlias+"/"+id, outputAlias, nil))
		}
	}
	return ocNormalizeCatalog(catalog)
}

// ocNormalizeCatalog applies the generator-side normalizeModelCatalog
// over the built map (entries are already entry objects keyed by id).
func ocNormalizeCatalog(c *ocCatalog) *ocCatalog {
	out := &ocCatalog{entries: map[string]*clisetup.JObj{}}
	keys := append([]string{}, c.order...)
	ocSortStrings(keys)
	for _, key := range keys {
		entry := c.entries[key]
		id := ocCatalogEntryID(entry, key)
		if id == "" {
			id = key
		}
		if !out.has(id) {
			out.order = append(out.order, id)
		}
		out.entries[id] = ocNormalizeCatalogEntry(id, entry)
	}
	return out
}

// ocCatalogModels ports previewResponse.buildCatalogModels over the
// catalog object form.
func ocCatalogModels(c *ocCatalog) []any {
	out := []any{}
	for _, key := range c.order {
		entry := c.entries[key]
		id := ocCatalogEntryID(entry, key)
		if id == "" {
			continue
		}
		name := id
		if s := ocTrim(entry.Get("name")); s != "" {
			name = s
		}
		provider := ocTrim(entry.Get("provider"))
		if provider == "" {
			provider = strings.SplitN(id, "/", 2)[0]
		}
		out = append(out, clisetup.NewJObj("id", id, "name", name, "provider", provider))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return ocCompare(out[i].(*clisetup.JObj).Get("id").(string), out[j].(*clisetup.JObj).Get("id").(string)) < 0
	})
	return out
}
