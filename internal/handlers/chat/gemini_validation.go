package chat

import (
	json "encoding/json/v2"
	"regexp"
	"strings"
)

var googleURLRegex = regexp.MustCompile(`https?://[^\s"'\\]+`)

// extractGoogleValidationURL attempts to extract account verification / validation
// URLs returned in Google API error payloads (e.g. details metadata or embedded links).
func extractGoogleValidationURL(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var raw struct {
		Error struct {
			Details []map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &raw); err == nil {
		for _, d := range raw.Error.Details {
			if meta, ok := d["metadata"].(map[string]any); ok {
				for _, k := range []string{"validation_url", "validationUrl", "url", "link"} {
					if u, ok := meta[k].(string); ok && strings.HasPrefix(u, "http") {
						return u
					}
				}
			}
			if links, ok := d["links"].([]any); ok {
				for _, l := range links {
					if lMap, ok := l.(map[string]any); ok {
						if u, ok := lMap["url"].(string); ok && strings.HasPrefix(u, "http") {
							return u
						}
					}
				}
			}
		}
	}
	matches := googleURLRegex.FindAll(body, -1)
	for _, m := range matches {
		s := string(m)
		if strings.Contains(s, "accounts.google.com") || strings.Contains(s, "cloud.google.com") || strings.Contains(s, "antigravity.google") {
			return s
		}
	}
	return ""
}
