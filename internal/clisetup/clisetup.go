// Package clisetup renders per-tool host setup scripts (bash and
// PowerShell) that point a client CLI tool at this gateway. It is the
// Go port of AxonRouter's src/lib/cliSetup generators driven by
// src/shared/constants/cliToolTemplates.js: templates are data (one
// entry per tool), the generators are pure renderers over that data,
// and the HTTP layer only maps query params onto Params.
package clisetup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Params is the setup-script input set, mirroring the params object
// Axon's setup route builds from the query string. The Has* flags
// carry the JS distinction between an absent query param (undefined —
// template defaults apply) and a present one; the route maps empty
// strings to absent, exactly like the JS `|| undefined` chain.
type Params struct {
	BaseURL          string
	APIKey           string
	Model            string
	HasModel         bool
	SubagentModel    string
	HasSubagentModel bool
	// Models carries the named mapping overrides the route collects:
	// "model" (from ?model=), "sonnet", "opus", "haiku", "fable".
	Models           map[string]string
	ModelsList       []string
	HasModelsList    bool
	MaxContextTokens string
	HasMaxContext    bool
}

// apiKeyOr returns the configured key or Axon's placeholder default.
func (p Params) apiKeyOr() string {
	if p.APIKey != "" {
		return p.APIKey
	}
	return "sk_axonrouter"
}

// modelOr resolves a destructured `model = "<def>"` template default.
func (p Params) modelOr(def string) string {
	if p.HasModel {
		return p.Model
	}
	return def
}

// modelsListOr resolves a destructured `modelsList = [...]` default.
func (p Params) modelsListOr(def []string) []string {
	if p.HasModelsList {
		return p.ModelsList
	}
	return def
}

// cleanBaseV1 returns baseUrl with a trailing /v1 ensured (the JS
// `baseUrl.endsWith("/v1") ? baseUrl : baseUrl + "/v1"` idiom).
func cleanBaseV1(baseURL string) string {
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL
	}
	return baseURL + "/v1"
}

// cleanBaseStripV1 returns baseUrl with one trailing /v1 removed (the
// JS `baseUrl.endsWith("/v1") ? baseUrl.slice(0, -3) : baseUrl` idiom).
func cleanBaseStripV1(baseURL string) string {
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL[:len(baseURL)-3]
	}
	return baseURL
}

// FileSpec is one file of a multi_file template: per-OS target paths
// plus the content renderer.
type FileSpec struct {
	PathPosix string
	PathWin   string
	Content   func(Params) string
}

// Template is one tool's entry in TOOL_TEMPLATES.
type Template struct {
	ID       string
	Name     string
	Format   string // json_merge | json_replace | toml_block | toml_replace | copilot_json | env_export | multi_file
	Posix    []string
	Darwin   []string
	Windows  []string
	Files    []FileSpec // multi_file only
	Generate func(Params) any
	Verify   string // verifyCmd, "" when the template has none
}

// Lookup returns the template for a tool id.
func Lookup(toolID string) (Template, bool) {
	t, ok := templates[toolID]
	return t, ok
}

// ToolIDs lists every template id (for tests and diagnostics).
func ToolIDs() []string {
	ids := make([]string, 0, len(templates))
	for id := range templates {
		ids = append(ids, id)
	}
	return ids
}

// --- Ordered JSON --------------------------------------------------

// JObj is a JSON object that marshals with its keys in insertion
// order, matching JS object literal order through JSON.stringify
// (Go maps would scramble it).
type JObj struct {
	keys []string
	vals map[string]any
}

// NewJObj builds a JObj from alternating key/value arguments.
func NewJObj(pairs ...any) *JObj {
	o := &JObj{vals: make(map[string]any, len(pairs)/2)}
	for i := 0; i+1 < len(pairs); i += 2 {
		k, _ := pairs[i].(string)
		o.keys = append(o.keys, k)
		o.vals[k] = pairs[i+1]
	}
	return o
}

// Set appends or replaces a key, preserving first-insertion order
// (JS object semantics).
func (o *JObj) Set(key string, v any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

// ParseOrderedJSON decodes JSON into an ordered value tree: objects
// become *JObj (insertion order preserved), arrays []any, numbers
// json.Number literals. It is the shared decoder for handlers that
// echo or transform parsed JSON the way JS code would.
func ParseOrderedJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return readOrdered(dec)
}

func readOrdered(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := NewJObj()
			for dec.More() {
				ktok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := ktok.(string)
				v, err := readOrdered(dec)
				if err != nil {
					return nil, err
				}
				obj.Set(key, v)
			}
			_, err := dec.Token()
			return obj, err
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := readOrdered(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			_, err := dec.Token()
			return arr, err
		}
		return nil, nil
	default:
		return tok, nil
	}
}

// FromMap converts a decoded map into a JObj with sorted keys,
// recursing into nested maps and slices. Used where a structured
// parser (TOML) already discarded the source key order.
func FromMap(m map[string]any) *JObj {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	obj := NewJObj()
	for _, k := range keys {
		obj.Set(k, fromAny(m[k]))
	}
	return obj
}

func fromAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return FromMap(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = fromAny(e)
		}
		return out
	default:
		return v
	}
}

// MarshalJSON renders the object with JS key order via
// StringifyJSON, so ordered trees can travel through the repo's
// standard JSON response helpers.
func (o *JObj) MarshalJSON() ([]byte, error) {
	return []byte(StringifyJSON(o)), nil
}

// Get returns the value stored under key (nil when absent).
func (o *JObj) Get(key string) any {
	if o == nil {
		return nil
	}
	return o.vals[key]
}

// Keys returns the object's keys in insertion order.
func (o *JObj) Keys() []string {
	if o == nil {
		return nil
	}
	return append([]string(nil), o.keys...)
}

// StringifyJSON renders a value exactly like JS
// JSON.stringify(value, null, 2): two-space indent, ": " separators,
// no HTML escaping, non-ASCII passed through.
func StringifyJSON(v any) string {
	var sb strings.Builder
	writeJSONValue(&sb, v, 0, false)
	return sb.String()
}

// StringifyJSONCompact renders JSON.stringify(v) with no indent —
// the canonical form content hashes are computed over.
func StringifyJSONCompact(v any) string {
	var sb strings.Builder
	writeJSONValue(&sb, v, 0, true)
	return sb.String()
}

func writeJSONValue(sb *strings.Builder, v any, depth int, compact bool) {
	switch t := v.(type) {
	case nil:
		sb.WriteString("null")
	case string:
		writeJSONString(sb, t)
	case bool:
		sb.WriteString(strconv.FormatBool(t))
	case int:
		sb.WriteString(strconv.Itoa(t))
	case int64:
		sb.WriteString(strconv.FormatInt(t, 10))
	case float64:
		sb.WriteString(strconv.FormatFloat(t, 'g', -1, 64))
	case json.Number:
		sb.WriteString(t.String())
	case *JObj:
		if len(t.keys) == 0 {
			sb.WriteString("{}")
			return
		}
		if compact {
			sb.WriteString("{")
			for i, k := range t.keys {
				if i > 0 {
					sb.WriteString(",")
				}
				writeJSONString(sb, k)
				sb.WriteString(":")
				writeJSONValue(sb, t.vals[k], depth+1, compact)
			}
			sb.WriteString("}")
			return
		}
		sb.WriteString("{\n")
		ind := strings.Repeat("  ", depth+1)
		for i, k := range t.keys {
			if i > 0 {
				sb.WriteString(",\n")
			}
			sb.WriteString(ind)
			writeJSONString(sb, k)
			sb.WriteString(": ")
			writeJSONValue(sb, t.vals[k], depth+1, compact)
		}
		sb.WriteString("\n")
		sb.WriteString(strings.Repeat("  ", depth))
		sb.WriteString("}")
	case []any:
		if len(t) == 0 {
			sb.WriteString("[]")
			return
		}
		if compact {
			sb.WriteString("[")
			for i, item := range t {
				if i > 0 {
					sb.WriteString(",")
				}
				writeJSONValue(sb, item, depth+1, compact)
			}
			sb.WriteString("]")
			return
		}
		sb.WriteString("[\n")
		ind := strings.Repeat("  ", depth+1)
		for i, item := range t {
			if i > 0 {
				sb.WriteString(",\n")
			}
			sb.WriteString(ind)
			writeJSONValue(sb, item, depth+1, compact)
		}
		sb.WriteString("\n")
		sb.WriteString(strings.Repeat("  ", depth))
		sb.WriteString("]")
	case []string:
		arr := make([]any, len(t))
		for i, s := range t {
			arr[i] = s
		}
		writeJSONValue(sb, arr, depth, compact)
	default:
		// Templates only produce the types above; a stray value renders
		// as its Go default rather than failing script generation.
		fmt.Fprintf(sb, "%v", t)
	}
}

// writeJSONString escapes exactly the characters JSON.stringify does:
// quotes, backslash, and control characters (\b \f \n \r \t by name,
// the rest as \u00XX). Forward slashes and non-ASCII pass through.
func writeJSONString(sb *strings.Builder, s string) {
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\b':
			sb.WriteString(`\b`)
		case '\f':
			sb.WriteString(`\f`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(sb, `\u%04x`, r)
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
}
