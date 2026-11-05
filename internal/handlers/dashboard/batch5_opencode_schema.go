package dashboard

import (
	"errors"
	"net/url"
	"strings"

	"axonrouter/proxy/internal/clisetup"
)

// OpenCode sync preferences: schema, normalization, validation and
// response sanitization. Port of Axon's src/lib/opencodeSync/schema.ts.
// Preferences travel as ordered JSON trees (clisetup.JObj) because the
// JS normalize step is a spread over the defaults — unknown keys are
// preserved in position and the response echoes them.

const ocRedactedValue = "********"

// ocValidationError marks a preferences/token validation failure the
// way Axon's OpenCodeValidationError code does.
type ocValidationError struct{ msg string }

func (e *ocValidationError) Error() string { return e.msg }

func ocIsValidationError(err error) bool {
	var ve *ocValidationError
	return errors.As(err, &ve)
}

func ocPlain(v any) (*clisetup.JObj, bool) {
	o, ok := v.(*clisetup.JObj)
	return o, ok
}

func ocIsPlain(v any) bool {
	_, ok := v.(*clisetup.JObj)
	return ok
}

// ocTrim mirrors normalizeString: non-strings become "".
func ocTrim(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func ocNullableString(v any) any {
	if s := ocTrim(v); s != "" {
		return s
	}
	return nil
}

func ocStringList(v any) []any {
	arr, ok := v.([]any)
	if !ok {
		return []any{}
	}
	seen := map[string]bool{}
	out := []any{}
	for _, item := range arr {
		s := ocTrim(item)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func ocEnvVars(v any) []any {
	arr, ok := v.([]any)
	if !ok {
		return []any{}
	}
	byKey := map[string]*clisetup.JObj{}
	order := []string{}
	for _, item := range arr {
		obj, _ := ocPlain(item)
		key := ocTrim(obj.Get("key"))
		if key == "" {
			continue
		}
		var value string
		switch raw := obj.Get("value"); {
		case raw == nil:
			value = ""
		default:
			if s, ok := raw.(string); ok {
				value = s
			} else {
				value = clisetup.StringifyJSON(raw)
				if strings.HasPrefix(value, `"`) {
					value = strings.Trim(value, `"`)
				}
			}
		}
		entry := clisetup.NewJObj(
			"key", key,
			"value", value,
			"secret", obj.Get("secret") == true,
		)
		if _, dup := byKey[key]; !dup {
			order = append(order, key)
		}
		byKey[key] = entry
	}
	ocSortStrings(order)
	out := []any{}
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out
}

func ocMcpCommand(v any) any {
	if arr, ok := v.([]any); ok {
		out := []any{}
		for _, item := range arr {
			if s := ocTrim(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return ocTrim(v)
}

func ocMcpServer(v any) *clisetup.JObj {
	obj, ok := ocPlain(v)
	if !ok {
		return nil
	}
	explicitType := ocTrim(obj.Get("type"))
	effective := explicitType
	if effective == "" {
		effective = "local"
	}
	out := clisetup.NewJObj("name", ocTrim(obj.Get("name")))
	if explicitType != "" {
		out.Set("type", explicitType)
	}
	if effective == "remote" {
		out.Set("url", ocTrim(obj.Get("url")))
	} else {
		out.Set("command", ocMcpCommand(obj.Get("command")))
	}
	return out
}

func ocMcpServers(v any) []any {
	arr, ok := v.([]any)
	if !ok {
		return []any{}
	}
	out := []any{}
	for _, item := range arr {
		if s := ocMcpServer(item); s != nil {
			out = append(out, s)
		}
	}
	return out
}

func ocAdvancedOverrides(v any) *clisetup.JObj {
	obj, _ := ocPlain(v)
	pick := func(key string) any {
		if sub, ok := ocPlain(obj.Get(key)); ok {
			return sub
		}
		return clisetup.NewJObj()
	}
	return clisetup.NewJObj(
		"openagent", pick("openagent"),
		"slim", pick("slim"),
		"custom", pick("custom"),
	)
}

// ocDefaultPreferences ports createDefaultOpenCodePreferences.
func ocDefaultPreferences() *clisetup.JObj {
	return clisetup.NewJObj(
		"variant", "openagent",
		"customTemplate", nil,
		"defaultModel", nil,
		"modelSelectionMode", "exclude",
		"includedModels", []any{},
		"excludedModels", []any{},
		"customPlugins", []any{},
		"mcpServers", []any{},
		"envVars", []any{},
		"advancedOverrides", clisetup.NewJObj(
			"openagent", clisetup.NewJObj(),
			"slim", clisetup.NewJObj(),
			"custom", clisetup.NewJObj(),
		),
		"updatedAt", nil,
	)
}

// ocNormalizePreferences ports normalizeOpenCodePreferences,
// including the {...defaults, ...source} spread of unknown keys.
func ocNormalizePreferences(input any) *clisetup.JObj {
	src, _ := ocPlain(input)
	out := ocDefaultPreferences()
	if src != nil {
		for _, k := range src.Keys() {
			out.Set(k, src.Get(k))
		}
	}
	strOr := func(v any, fallback string) string {
		if s := ocTrim(v); s != "" {
			return s
		}
		return fallback
	}
	out.Set("variant", strOr(src.Get("variant"), "openagent"))
	out.Set("customTemplate", ocNullableString(src.Get("customTemplate")))
	out.Set("defaultModel", ocNullableString(src.Get("defaultModel")))
	out.Set("modelSelectionMode", strOr(src.Get("modelSelectionMode"), "exclude"))
	out.Set("includedModels", ocStringList(src.Get("includedModels")))
	out.Set("excludedModels", ocStringList(src.Get("excludedModels")))
	out.Set("customPlugins", ocStringList(src.Get("customPlugins")))
	out.Set("mcpServers", ocMcpServers(src.Get("mcpServers")))
	out.Set("envVars", ocEnvVars(src.Get("envVars")))
	out.Set("advancedOverrides", ocAdvancedOverrides(src.Get("advancedOverrides")))
	out.Set("updatedAt", ocNullableString(src.Get("updatedAt")))
	return out
}

// ocValidatePreferences ports validateOpenCodePreferences: normalize,
// then the ordered rule checks with Axon's exact messages.
func ocValidatePreferences(input any) (*clisetup.JObj, error) {
	p := ocNormalizePreferences(input)

	variant, _ := p.Get("variant").(string)
	if variant != "openagent" && variant != "slim" && variant != "custom" {
		return nil, &ocValidationError{"Invalid OpenCode variant"}
	}
	tmpl := p.Get("customTemplate")
	if tmpl != nil {
		if s, _ := tmpl.(string); s != "minimal" && s != "opinionated" {
			return nil, &ocValidationError{"Invalid custom template"}
		}
	}
	if variant != "custom" && tmpl != nil {
		return nil, &ocValidationError{"Custom template is only valid for custom variant"}
	}
	if mode, _ := p.Get("modelSelectionMode").(string); mode != "include" && mode != "exclude" {
		return nil, &ocValidationError{"Invalid model selection mode"}
	}

	seen := map[string]bool{}
	servers, _ := p.Get("mcpServers").([]any)
	for _, raw := range servers {
		server, _ := ocPlain(raw)
		name, _ := server.Get("name").(string)
		if name == "" {
			return nil, &ocValidationError{"Invalid MCP server: name is required"}
		}
		dupKey := strings.ToLower(name)
		if seen[dupKey] {
			return nil, &ocValidationError{"Duplicate MCP server name: " + name}
		}
		seen[dupKey] = true

		serverType := ocTrim(server.Get("type"))
		if serverType == "" {
			serverType = "local"
		}
		if serverType != "local" && serverType != "remote" {
			return nil, &ocValidationError{"Invalid MCP server type for " + name}
		}
		if serverType == "remote" {
			rawURL, _ := server.Get("url").(string)
			if rawURL == "" {
				return nil, &ocValidationError{`Remote MCP server "` + name + `" requires a URL`}
			}
			parsed, err := url.Parse(rawURL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return nil, &ocValidationError{"Invalid MCP server URL for " + name}
			}
			continue
		}
		switch cmd := server.Get("command").(type) {
		case []any:
			if len(cmd) == 0 {
				return nil, &ocValidationError{`Local MCP server "` + name + `" requires a command`}
			}
		case string:
			if cmd == "" {
				return nil, &ocValidationError{`Local MCP server "` + name + `" requires a command`}
			}
		default:
			return nil, &ocValidationError{`Local MCP server "` + name + `" requires a command`}
		}
	}
	return p, nil
}

// --- sanitization ----------------------------------------------------

func ocIsSensitiveKey(key string) bool {
	lowered := strings.ToLower(strings.TrimSpace(key))
	switch lowered {
	case "secret", "issecret", "sensitive":
		return false
	}
	if strings.Contains(lowered, "secret") || strings.Contains(lowered, "password") ||
		strings.Contains(lowered, "authorization") {
		return true
	}
	if lowered == "apikey" || lowered == "api_key" ||
		strings.HasSuffix(lowered, "apikey") || strings.HasSuffix(lowered, "api_key") {
		return true
	}
	switch lowered {
	case "token", "accesstoken", "refreshtoken", "idtoken", "sessiontoken", "bearertoken":
		return true
	}
	return strings.HasSuffix(lowered, "token") && !strings.HasSuffix(lowered, "tokens")
}

// ocSanitize ports sanitizeSensitiveConfig (recursive redaction).
func ocSanitize(value any, key string) any {
	if arr, ok := value.([]any); ok {
		out := make([]any, len(arr))
		for i, item := range arr {
			out[i] = ocSanitize(item, key)
		}
		return out
	}
	if ocIsSensitiveKey(key) {
		return ocRedactedValue
	}
	obj, ok := ocPlain(value)
	if !ok {
		return value
	}
	looksSecret := obj.Get("secret") == true || obj.Get("isSecret") == true || obj.Get("sensitive") == true
	out := clisetup.NewJObj()
	for _, k := range obj.Keys() {
		v := obj.Get(k)
		switch {
		case looksSecret && k == "value":
			out.Set(k, ocRedactedValue)
		case ocIsSensitiveKey(k):
			out.Set(k, ocRedactedValue)
		default:
			out.Set(k, ocSanitize(v, k))
		}
	}
	return out
}

// ocSanitizePreferences ports sanitizeOpenCodePreferencesForResponse.
func ocSanitizePreferences(input any) *clisetup.JObj {
	sanitized, _ := ocSanitize(ocNormalizePreferences(input), "").(*clisetup.JObj)
	return sanitized
}
