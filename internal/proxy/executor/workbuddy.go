package executor

import (
	"encoding/base64"
	json "encoding/json/v2"
	"net/http"
	"strconv"
	"strings"

	"axonrouter/proxy/internal/providers"

	"github.com/google/uuid"
)

// WorkBuddy executor — the thin brand specialization of the shared
// CodeBuddy-family forward, mirroring upstream's class shape
// (open-sse/executors/workbuddy.js extends the CodeBuddy executor and
// overrides buildHeaders only). Everything else — force-stream,
// reasoning mirror, prompt merge, 6004 parsing, pacing — is inherited
// from forwardCodebuddy unchanged.
const (
	workbuddySystemPrompt = "You are WorkBuddy."
	workbuddyUserAgent    = "WorkBuddy/5.5.6"
	workbuddyIDEVersion   = "5.5.6"
	workbuddyHost         = "www.workbuddy.ai"
)

var workbuddyFlavor = codebuddyFlavor{
	prompt:     workbuddySystemPrompt,
	headers:    workbuddyHeaders,
	stallLabel: "workbuddy",
}

// ForwardWorkbuddy forwards to WorkBuddy (www.workbuddy.ai) with the
// WorkBuddy client identity the channel verification demands.
func ForwardWorkbuddy(w http.ResponseWriter, req *Request) error {
	return forwardCodebuddy(w, req, workbuddyFlavor)
}

// workbuddyHeaders starts from the CodeBuddy header set and rewrites it
// into the WorkBuddy identity, exactly as upstream does:
//
//   - The CodeBuddy CLI/Stainless identity is stripped: a request that
//     still presents as the CodeBuddy CLI fails WorkBuddy's channel
//     verification (error 11128, "please use WorkBuddy to access this
//     service").
//   - The client identity becomes WorkBuddy/5.5.6 — mandatory, the
//     service errors 10085 ("client version too old") without it.
//   - Channel/conversation ids are FRESH per request. Upstream's
//     WorkBuddy analytics channel re-mints its ids on every send and a
//     replayed conversation id is precisely how a non-WorkBuddy client
//     stands out; the CodeBuddy lane's session-stable id does not apply.
//   - X-User-Id carries the account id decoded from the connection's
//     JWT, which the endpoint requires for its team-management checks.
func workbuddyHeaders(req *Request, cfg *providers.ProviderConfig, body []byte) map[string]string {
	h := codebuddyHeaders(req, cfg, body)

	for k := range h {
		lower := strings.ToLower(k)
		if strings.HasPrefix(lower, "x-stainless-") {
			delete(h, k)
			continue
		}
		switch lower {
		case "x-app", "x-private-data", "x-codebuddy-request":
			delete(h, k)
		}
	}

	host := workbuddyHost
	if req != nil && req.ConnData != nil {
		if d, ok := req.ConnData["domain"].(string); ok && d != "" {
			host = d
		}
	}

	reqID := strings.ReplaceAll(uuid.NewString(), "-", "")
	convReqID := strings.ReplaceAll(uuid.NewString(), "-", "")

	h["User-Agent"] = workbuddyUserAgent
	h["X-IDE-Type"] = "WorkBuddy"
	h["X-IDE-Name"] = "WorkBuddy"
	h["X-IDE-Version"] = workbuddyIDEVersion
	h["X-Agent-Type"] = "main"
	h["X-Domain"] = host
	h["X-Request-ID"] = reqID
	h["X-Conversation-ID"] = uuid.NewString()
	h["X-Conversation-Request-ID"] = convReqID
	h["X-Conversation-Message-ID"] = reqID

	if req != nil {
		if uid := workbuddyJWTUserID(req.APIKey); uid != "" {
			h["X-User-Id"] = uid
		}
	}
	return h
}

// workbuddyJWTUserID decodes the account id from a JWT access token.
// Claim precedence mirrors upstream's extractor: user_id, then userId,
// then uid, then sub. A non-JWT credential (an API key) yields "".
func workbuddyJWTUserID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return ""
	}
	for _, key := range []string{"user_id", "userId", "uid", "sub"} {
		switch v := claims[key].(type) {
		case string:
			if v != "" {
				return v
			}
		case float64:
			if v == float64(int64(v)) {
				return strconv.FormatInt(int64(v), 10)
			}
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	return ""
}
