package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"axonrouter/proxy/internal/clisetup"
)

// OpenCode sync artifacts + bundle assembly (second half of the
// generator.ts port): opencode.json, the oh-my-* variant artifacts,
// the sanitized public artifact set, and the content hash.

func ocMcpArtifactConfig(servers []any) *clisetup.JObj {
	out := clisetup.NewJObj()
	for i, raw := range servers {
		server, _ := ocPlain(raw)
		name, _ := server.Get("name").(string)
		if name == "" {
			name = "server-" + itoa(i+1)
		}
		if server.Get("type") == "remote" {
			rawURL, _ := server.Get("url").(string)
			out.Set(name, clisetup.NewJObj("type", "remote", "url", rawURL))
			continue
		}
		var command any
		switch cmd := server.Get("command").(type) {
		case []any:
			command = ocStableClone(cmd)
		case string:
			command = cmd
		default:
			command = []any{}
		}
		out.Set(name, clisetup.NewJObj("type", "local", "command", command))
	}
	return out
}

func ocEnvArtifactConfig(envVars []any) *clisetup.JObj {
	out := clisetup.NewJObj()
	for _, raw := range envVars {
		item, _ := ocPlain(raw)
		key, _ := item.Get("key").(string)
		if key == "" {
			continue
		}
		if item.Get("secret") == true {
			out.Set(key, "<set-locally>")
			continue
		}
		value, _ := item.Get("value").(string)
		out.Set(key, value)
	}
	return out
}

// ocOpenCodeArtifact ports buildOpenCodeArtifact.
func ocOpenCodeArtifact(models *clisetup.JObj, defaultModel string, plugins, mcpServers, envVars []any, apiKey string) (*clisetup.JObj, error) {
	// assertUniqueArtifactModelIds: two distinct catalog ids that
	// trim to the same artifact id are a validation error.
	byArtifact := map[string][]string{}
	for _, id := range ocSortedKeys(models) {
		artifact := strings.TrimSpace(id)
		if artifact == "" {
			continue
		}
		byArtifact[artifact] = append(byArtifact[artifact], id)
	}
	for artifact, ids := range byArtifact {
		if len(ids) > 1 {
			return nil, &ocValidationError{
				`Multiple selected models normalize to the same artifact model id "` + artifact + `": ` + strings.Join(ids, ", "),
			}
		}
	}

	providerModels := clisetup.NewJObj()
	for _, id := range ocSortedKeys(models) {
		artifact := strings.TrimSpace(id)
		if artifact == "" {
			continue
		}
		providerModels.Set(artifact, ocConfigModelMetadata(id, models.Get(id)))
	}
	providerIDs := ocSortedKeys(providerModels)
	active := ""
	defaultArtifact := strings.TrimSpace(defaultModel)
	for _, id := range providerIDs {
		if id == defaultArtifact {
			active = defaultArtifact
		}
	}
	if active == "" && len(providerIDs) > 0 {
		active = providerIDs[0]
	}

	finalKey := apiKey
	if finalKey == "" {
		finalKey = ocDefaultAPIKey
	}
	modelField := ""
	if active != "" {
		modelField = ocProviderID + "/" + active
	}
	artifact := clisetup.NewJObj(
		"$schema", ocOpenCodeSchema,
		"plugin", plugins,
		"provider", clisetup.NewJObj(
			ocProviderID, clisetup.NewJObj(
				"npm", ocProviderPackage,
				"name", ocProviderName,
				"options", clisetup.NewJObj(
					"baseURL", ocDefaultBaseURL,
					"apiKey", finalKey,
				),
				"models", providerModels,
			),
		),
		"model", modelField,
	)
	if len(mcpServers) > 0 {
		artifact.Set("mcp", ocMcpArtifactConfig(mcpServers))
	}
	if len(envVars) > 0 {
		artifact.Set("env", ocEnvArtifactConfig(envVars))
	}
	clone, _ := ocStableClone(artifact).(*clisetup.JObj)
	return clone, nil
}

// ocAssignmentMap ports buildModelAssignmentMap: bare model ids
// become {key: {model: "axonrouter/<id>"}} entries.
func ocAssignmentMap(assignments any) *clisetup.JObj {
	obj, ok := ocPlain(assignments)
	if !ok {
		return nil
	}
	out := clisetup.NewJObj()
	for _, key := range ocSortedKeys(obj) {
		raw, _ := obj.Get(key).(string)
		normalized := strings.TrimSpace(raw)
		if normalized == "" {
			continue
		}
		out.Set(key, clisetup.NewJObj("model", ocProviderID+"/"+normalized))
	}
	if len(out.Keys()) == 0 {
		return nil
	}
	return out
}

// ocPickRelevant ports pickRelevantConfigFields.
func ocPickRelevant(config any, excluded map[string]bool, omitBalancedPreset bool) *clisetup.JObj {
	out := clisetup.NewJObj()
	obj, ok := ocPlain(config)
	if !ok {
		return out
	}
	for _, key := range ocSortedKeys(obj) {
		if excluded[key] {
			continue
		}
		value := obj.Get(key)
		if omitBalancedPreset && key == "preset" && value == "balanced" {
			continue
		}
		if value == nil {
			continue
		}
		if arr, isArr := value.([]any); isArr && len(arr) == 0 {
			continue
		}
		if sub, isObj := ocPlain(value); isObj && len(sub.Keys()) == 0 {
			continue
		}
		out.Set(key, ocStableClone(value))
	}
	return out
}

func ocMergeInto(dst *clisetup.JObj, src *clisetup.JObj) {
	for _, k := range src.Keys() {
		dst.Set(k, src.Get(k))
	}
}

// ocOpenAgentArtifact ports buildOpenAgentArtifact.
func ocOpenAgentArtifact(config any) *clisetup.JObj {
	cfg, _ := ocPlain(config)
	out := clisetup.NewJObj("$schema", ocOpenAgentSchema)
	if agents := ocAssignmentMap(cfg.Get("agentAssignments")); agents != nil {
		out.Set("agents", agents)
	}
	if categories := ocAssignmentMap(cfg.Get("categoryAssignments")); categories != nil {
		out.Set("categories", categories)
	}
	out.Set("auto_update", false)
	out.Set("background_task", clisetup.NewJObj("defaultConcurrency", 5))
	out.Set("sisyphus_agent", clisetup.NewJObj("planner_enabled", true, "replace_plan", true))
	out.Set("git_master", clisetup.NewJObj("commit_footer", false, "include_co_authored_by", false))
	ocMergeInto(out, ocPickRelevant(config, map[string]bool{
		"agentAssignments": true, "categoryAssignments": true, "preset": true,
	}, false))
	clone, _ := ocStableClone(out).(*clisetup.JObj)
	return clone
}

// ocSlimArtifact ports buildSlimArtifact.
func ocSlimArtifact(config any) *clisetup.JObj {
	cfg, _ := ocPlain(config)
	out := clisetup.NewJObj("$schema", ocSlimSchema)
	if agents := ocAssignmentMap(cfg.Get("agentAssignments")); agents != nil {
		out.Set("agents", agents)
	}
	ocMergeInto(out, ocPickRelevant(config, map[string]bool{
		"agentAssignments": true, "categoryAssignments": true,
	}, true))
	clone, _ := ocStableClone(out).(*clisetup.JObj)
	return clone
}

// ocBundleResult is the slice of buildOpenCodeSyncBundle's return
// the routes consume: the hash + public artifacts.
type ocBundleResult struct {
	Hash            string
	Revision        string
	SchemaVersion   int
	PublicArtifacts *clisetup.JObj
}

// ocBuildBundle ports buildOpenCodeSyncBundle (apiKey is always the
// default in the routes — they never pass one).
func ocBuildBundle(preferencesInput any, catalog *ocCatalog) (*ocBundleResult, error) {
	prefs, err := ocValidatePreferences(preferencesInput)
	if err != nil {
		return nil, err
	}
	plugins := ocPluginList(prefs)
	models := ocModelMap(prefs, catalog)

	variant, _ := prefs.Get("variant").(string)
	overridesObj, _ := ocPlain(prefs.Get("advancedOverrides"))
	advancedOverrides := ocMergePlain(ocTemplateOverrides(prefs), overridesObj.Get(variant))

	defaultModel, _ := prefs.Get("defaultModel").(string)
	if defaultModel != "" {
		if _, present := hasKey(models, defaultModel); !present {
			return nil, &ocValidationError{"Default model must be included in generated bundle models"}
		}
	}

	mcpServers, _ := prefs.Get("mcpServers").([]any)
	envVars, _ := prefs.Get("envVars").([]any)

	opencode, err := ocOpenCodeArtifact(models, defaultModel, plugins, mcpServers, envVars, "")
	if err != nil {
		return nil, err
	}

	var ohMyOpencode, ohMySlim any
	variantConfig := ocVariantConfig(variant, models, defaultModel, advancedOverrides)
	switch variant {
	case "openagent":
		ohMyOpencode = ocSanitize(ocStableClone(ocOpenAgentArtifact(variantConfig)), "")
	case "slim":
		ohMySlim = ocSanitize(ocStableClone(ocSlimArtifact(variantConfig)), "")
	}

	publicArtifacts, _ := ocStableClone(clisetup.NewJObj(
		"opencode", opencode,
		"ohMyOpencode", ohMyOpencode,
		"ohMyOpenCodeSlim", ohMySlim,
	)).(*clisetup.JObj)

	sum := sha256.Sum256([]byte(clisetup.StringifyJSONCompact(publicArtifacts)))
	hash := hex.EncodeToString(sum[:])
	return &ocBundleResult{
		Hash:            hash,
		Revision:        hash[:12],
		SchemaVersion:   ocBundleSchemaVer,
		PublicArtifacts: publicArtifacts,
	}, nil
}

// ocPublicResponse builds the shared {version, opencode,
// ohMyOpencode, ohMyOpenCodeSlim} response body (previewResponse.ts /
// sync bundle route).
func ocPublicResponse(b *ocBundleResult) *clisetup.JObj {
	return clisetup.NewJObj(
		"version", b.Hash,
		"opencode", b.PublicArtifacts.Get("opencode"),
		"ohMyOpencode", b.PublicArtifacts.Get("ohMyOpencode"),
		"ohMyOpenCodeSlim", b.PublicArtifacts.Get("ohMyOpenCodeSlim"),
	)
}

// itoa is a tiny strconv wrapper kept local to this feature's files.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}
