package media

import (
	stdjson "encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"

	"9router/proxy/internal/clisetup"
)

// Ordered-JSON helpers for the batch-5 read surfaces. Axon's settings
// and MCP responses echo parsed config files back through
// JSON.stringify, which preserves the file's key order; decoding into
// a Go map would reserialize with sorted keys. These helpers decode
// into clisetup.JObj trees (insertion-ordered) and write them back
// with the same stringifier the script generators use, so an echoed
// config keeps its shape.

var trailingCommaRE = regexp.MustCompile(`,(\s*[}\]])`)

// decodeOrderedJSON parses JSON into an ordered value tree: objects
// become *clisetup.JObj, numbers stay json.Number literals.
func decodeOrderedJSON(data []byte) (any, error) {
	return clisetup.ParseOrderedJSON(data)
}

// writeOrderedJSON writes a value built from ordered trees as JSON.
func writeOrderedJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, clisetup.StringifyJSON(v))
}

// readJSONCFile reads a JSON/JSONC file tolerantly, the way Axon's
// settings readers do: trailing commas are stripped and any failure
// (missing file, unparseable content) yields nil — "no config" rather
// than an error.
func readJSONCFile(path string) any {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	stripped := trailingCommaRE.ReplaceAll(raw, []byte("$1"))
	v, err := decodeOrderedJSON(stripped)
	if err != nil {
		return nil
	}
	return v
}

// jGet reads a key from an ordered object (nil-safe).
func jGet(v any, key string) any {
	if o, ok := v.(*clisetup.JObj); ok {
		return o.Get(key)
	}
	return nil
}

// jString reads a string field ("" when absent or not a string).
func jString(v any, key string) string {
	if s, ok := jGet(v, key).(string); ok {
		return s
	}
	return ""
}

// jKeys lists an ordered object's keys in file order.
func jKeys(v any) []string {
	if o, ok := v.(*clisetup.JObj); ok {
		return o.Keys()
	}
	return nil
}

// truthyJS applies JS truthiness to a decoded JSON value: nil, false,
// "" and numeric zero are falsy; everything else (including empty
// arrays/objects) is truthy.
func truthyJS(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case stdjson.Number:
		f, err := t.Float64()
		return err == nil && f != 0
	default:
		return true
	}
}
