package providers

import (
	"math"
	"net/http"
	"strings"
)

// ErrorRule classifies an upstream error by text match or status code.
// Checked top-to-bottom: text rules first, then status rules.
type ErrorRule struct {
	Text       string // substring match (case-insensitive); empty means not text-based
	Status     int    // HTTP status code match; 0 means not status-based
	CooldownMs int    // fixed cooldown duration; 0 means use exponential backoff
	Backoff    bool   // true = use exponential backoff (rate limit)
	// Provider scopes a text rule to one canonical provider id; empty applies
	// to every provider.
	Provider string
	// RequireStatus gates a text rule to one HTTP status; 0 applies it at
	// any status. The banned-account spellings that name a gone model or
	// entity need it: they are verdicts a 404 carries, and a 429 quota
	// body that merely QUOTES one of them (a provider wrapping the model
	// error inside its throttle response) must keep its quota reading
	// instead of inheriting a 24h account sentence.
	RequireStatus int
	// ExcludeStatus vetoes a text rule at one HTTP status; 0 vetoes it
	// nowhere. It is the mirror of RequireStatus for the banned-account
	// ToS spellings, and the decision lives here, in one place: a 429 is
	// a throttle verdict whatever its body quotes — providers wrap
	// account verdicts inside throttle responses — so at 429 the ToS
	// rules stay silent and the 429 backoff rule claims the response.
	// At every other status the same phrases keep their 24h reading
	// (review cycle 2, N4).
	ExcludeStatus int
	// Quota marks an error that means the account's credit/billing budget is
	// spent. These do NOT clear on a short timer, so they get a long cooldown
	// and are surfaced as a distinct status instead of a transient blip.
	Quota bool
	// Exhausted marks the JS isExhausted verdict: the account's budget is
	// spent, so alongside the (long) cooldown the account must read as
	// quota_exhausted once parked — the status derivation in
	// connection_status.go does that from the stored error text, and
	// cooldownSecForClassification carries the cooldown unclamped, the
	// same treatment the banned-account class gets.
	Exhausted bool
	// ModelScoped marks a verdict that names the model, not the account:
	// the consumer locks only the model and never writes an account lock
	// (upstream lockAll: false). The classifier cannot express lock scope
	// in CooldownMs alone, and the chat layer's text-based scope guess
	// (isModelScopedError) misreads these — a CodeBuddy "model busy" 429
	// carries no quota wording, so it used to park the whole account.
	ModelScoped bool
	// RequestLevel marks a refusal that is the request's fault (a safety
	// review, a content filter): no fallback, no lock, no cooldown — the
	// account and the model are both healthy and the next account would
	// answer the same prompt the same way.
	RequestLevel bool
}

// BackoffConfig controls exponential backoff scaling.
var BackoffConfig = struct {
	BaseMs   int
	MaxMs    int
	MaxLevel int
}{
	BaseMs:   2000,          // 2 seconds base
	MaxMs:    5 * 60 * 1000, // 5 minutes cap
	MaxLevel: 15,
}

// TransientCooldownMs is the default cooldown for unmatched/unknown errors.
const TransientCooldownMs = 30 * 1000 // 30 seconds

// cooldown durations (ms) used by ERROR_RULES
const (
	cooldownLong  = 2 * 60 * 1000 // 2 minutes
	cooldownShort = 5 * 1000      // 5 seconds

	// cooldownBannedAccount parks a deepseek-web account whose credential the
	// site refuses (40012 USER_IS_BANNED). The ban names the account, not a
	// model and not a transient condition: the generic 2-minute 403 rule
	// would re-pick the punished account every two minutes, and each attempt
	// is another request the site is actively penalizing.
	cooldownBannedAccount = 24 * 60 * 60 * 1000

	// cooldownAddressRefused is the pause for a deepseek-web address-level
	// refusal (40029 IP_ACCESS_RESTRICTED). The account is fine; the egress
	// is not, so the account waits out a short window instead of absorbing
	// every turn the same address is refused on.
	cooldownAddressRefused = 30 * 60 * 1000

	// QuotaExhaustedCooldownMs is how long an account is parked after the
	// upstream reports its credit/quota budget is spent. Ported from the
	// upstream classify429 helper (QUOTA_EXHAUSTED_COOLDOWN_MS): a rate limit
	// clears in a minute, but "insufficient balance" will not fix itself in 30
	// seconds — retrying that often just burns a guaranteed-failed upstream
	// call and keeps the dashboard showing a false recovery.
	QuotaExhaustedCooldownMs = 60 * 60 * 1000 // 1 hour

	// cooldownSevenDays is the CodeBuddy-family credit-exhaustion sentence
	// (upstream 14018 "Credits exhausted" on the direct path): the spending
	// pool is empty for every model on the account, so the account parks
	// exhausted for a week. It used to also carry the family's "unusual
	// activity" restriction texts at any status — that reading was wrong
	// (fragment 977): a restriction is a gated 403 + code-11140 verdict
	// with its own 2h sentence in errorclassify_codebuddy.go.
	cooldownSevenDays = 7 * 24 * 60 * 60 * 1000 // 7 days

	// cooldownMonthlyExhausted is upstream's ERROR_RULES sentence for the
	// generic credit/balance exhaustion texts (COOLDOWN.monthlyExhausted):
	// account-wide lock, marked exhausted, 30 days.
	cooldownMonthlyExhausted = 30 * 24 * 60 * 60 * 1000 // 30 days
)

// ErrorRules is the ordered list of error classification rules, matching Next.js ERROR_RULES.
// Checked top-to-bottom: text rules first (by order), then status rules.
var ErrorRules = []ErrorRule{
	// Content filter / safety review (upstream ERROR_RULES, global and
	// unscoped): the prompt was refused, not the account — request-level,
	// no fallback, no lock, no cooldown. "request illegal" is Tencent's
	// generic refusal msg and reads the same way UNLESS the body carries
	// code 11140, which the gated verdict in errorclassify_codebuddy.go
	// claims before this list is consulted (and which a safety wording
	// vetoes there, so a genuine safety refusal lands here). The
	// "conversation id missing" sibling travels with it: upstream's
	// current ERROR_RULES carries no entry for it, and the old intl-only
	// backoff reading parked accounts over a request fault (fragment 977).
	{Text: "did not pass the safety review", RequestLevel: true},
	{Text: "safety review", RequestLevel: true},
	{Text: "request illegal", RequestLevel: true},
	{Text: "content filter", RequestLevel: true},
	{Text: "conversation id missing", RequestLevel: true},

	// Tencent plan gate: 11158 "model [x] is not allowed for user type" —
	// the account is healthy, the model is simply not in its plan. Skip
	// with no lock and no cooldown (upstream checkFallbackError guard,
	// status 400 only). The code-gated guard is a text rule here because
	// the extracted error text always carries the "(code 11158)" suffix.
	{Text: "11158", CooldownMs: 0, ModelScoped: true, RequireStatus: http.StatusBadRequest},

	// Dahl inference cluster queue capacity: temporary GPU concurrency saturation,
	// not an account quota issue. Pause this model for 30s so router skips it.
	{Text: "model_concurrency", CooldownMs: 30000, ModelScoped: true},
	{Text: "concurrency capacity", CooldownMs: 30000, ModelScoped: true},

	// Unsupported vision / image input format on text-only models (e.g. Dahl/MiniMax 400 "unsupported value \"image_url\"")
	{Text: "unsupported value \"image_url\"", CooldownMs: 0, ModelScoped: true},
	{Text: "does not support image", CooldownMs: 0, ModelScoped: true},
	{Text: "does not support vision", CooldownMs: 0, ModelScoped: true},
	{Text: "model_not_in_plan", CooldownMs: cooldownLong},
	{Text: "当前账号类型暂不支持该模型", CooldownMs: cooldownLong},
	{Text: "额度已用尽", CooldownMs: cooldownLong},

	// --- Generic credit / balance exhaustion (upstream ERROR_RULES,
	// COOLDOWN.monthlyExhausted): account-wide lock, marked exhausted,
	// 30 days. These precede the older 1-hour quota spellings below,
	// whose texts overlap ("insufficient balance", "credits exhausted",
	// "insufficient quota", "monthly quota exceeded"): the ERROR_RULES
	// reading is the request path's verdict, so it wins. ---
	{Text: "insufficient_user_quota", CooldownMs: cooldownMonthlyExhausted, Quota: true, Exhausted: true},
	{Text: "预扣费额度失败", CooldownMs: cooldownMonthlyExhausted, Quota: true, Exhausted: true},
	{Text: "用户剩余额度", CooldownMs: cooldownMonthlyExhausted, Quota: true, Exhausted: true},
	{Text: "用户额度不足", CooldownMs: cooldownMonthlyExhausted, Quota: true, Exhausted: true},
	{Text: "user's credit limit is insufficient", CooldownMs: cooldownMonthlyExhausted, Quota: true, Exhausted: true},
	{Text: "quota is running low", CooldownMs: cooldownMonthlyExhausted, Quota: true, Exhausted: true},
	{Text: "credits exhausted", CooldownMs: cooldownMonthlyExhausted, Quota: true, Exhausted: true},
	{Text: "insufficient credits", CooldownMs: cooldownMonthlyExhausted, Quota: true, Exhausted: true},
	{Text: "insufficient balance", CooldownMs: cooldownMonthlyExhausted, Quota: true, Exhausted: true},
	{Text: "out of credits", CooldownMs: cooldownMonthlyExhausted, Quota: true, Exhausted: true},
	{Text: "quota reached", CooldownMs: cooldownMonthlyExhausted, Quota: true, Exhausted: true},
	{Text: "quota exhausted", CooldownMs: cooldownMonthlyExhausted, Quota: true, Exhausted: true},
	{Text: "insufficient_quota", CooldownMs: cooldownMonthlyExhausted, Quota: true, Exhausted: true},
	{Text: "insufficient quota", CooldownMs: cooldownMonthlyExhausted, Quota: true, Exhausted: true},
	{Text: "exceeded your current quota", CooldownMs: cooldownMonthlyExhausted, Quota: true, Exhausted: true},
	{Text: "monthly quota exceeded", CooldownMs: cooldownMonthlyExhausted, Quota: true, Exhausted: true},

	// --- Quota / credit exhaustion (checked before generic rules) ---
	// A spent budget is not a transient throttle: give it the long cooldown
	// instead of the 30s transient default. Without this, "insufficient
	// balance" heals itself every 30 seconds and the dashboard keeps
	// claiming a guaranteed-failing account is connected.
	{Text: "insufficient credit", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	{Text: "credit exhausted", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	{Text: "no remaining credits", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	{Text: "quota exceeded", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	{Text: "monthly limit", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	{Text: "frequency limit", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	{Text: "payment required", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	// TokenHarbor's free tier answers 429 with "You've used this period's free
	// allowance. Your next rolling 7-day period starts on ...". The status
	// alone reads as a throttle, so it only ever earned a 2s→5min backoff and
	// the spent account was re-dialled all window. The allowance is spent for
	// days, which is what this cooldown family is for.
	{Text: "free allowance", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},

	// --- Text-based rules (checked first, order = priority) ---
	// DeepSeek web account-level refusals both arrive as 403, and they are
	// not the same verdict. A ban ("refused this account") is the account
	// itself; an address refusal ("refused this address") is the egress, and
	// the account should be tried from another address rather than parked
	// forever. The generic 403 rule cannot tell them apart.
	{Provider: "deepseek-web", Text: "refused this account", CooldownMs: cooldownBannedAccount},
	{Provider: "deepseek-web", Text: "refused this address", CooldownMs: cooldownAddressRefused},
	// Terms of Service violation / account disablement (e.g. Google Cloud Code / Antigravity):
	// Park for 24 hours (1 day) so it stops thrashing the disabled account.
	// ExcludeStatus 429: a throttle response quoting one of these phrases
	// is a throttle, not a ban — see the ExcludeStatus field comment.
	{Text: "terms of service", CooldownMs: cooldownBannedAccount, ExcludeStatus: http.StatusTooManyRequests},
	{Text: "tos_violation", CooldownMs: cooldownBannedAccount, ExcludeStatus: http.StatusTooManyRequests},
	{Text: "disabled in this account", CooldownMs: cooldownBannedAccount, ExcludeStatus: http.StatusTooManyRequests},
	{Text: "no project id", CooldownMs: cooldownBannedAccount},
	{Text: "validation_required", CooldownMs: cooldownBannedAccount},
	{Text: "verify your account", CooldownMs: cooldownBannedAccount},
	{Text: "invalid_grant", CooldownMs: cooldownBannedAccount},
	{Text: "refresh_token_reused", CooldownMs: cooldownBannedAccount},
	{Text: "account has been deleted", CooldownMs: cooldownBannedAccount},
	{Text: "account has been banned", CooldownMs: cooldownBannedAccount},
	{Text: "requested entity was not found", CooldownMs: cooldownBannedAccount, RequireStatus: http.StatusNotFound},
	{Text: "model_not_found", CooldownMs: cooldownBannedAccount, RequireStatus: http.StatusNotFound},
	{Text: "no credentials", CooldownMs: cooldownLong},
	{Text: "request not allowed", CooldownMs: cooldownShort},
	{Text: "improperly formed request", CooldownMs: cooldownLong},
	{Text: "rate limit", Backoff: true},
	{Text: "too many requests", Backoff: true},
	// "credit balance is too low" stays here: it is Anthropic's wording for a
	// spent balance but does not contain "insufficient credit", so the head
	// quota rules above do not match it.
	{Text: "credit balance is too low", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	{Text: "capacity", Backoff: true},
	{Text: "overloaded", Backoff: true},
	{Text: "resource_exhausted", Backoff: true},
	{Text: "resource has been exhausted", Backoff: true},
	{Text: "model_capacity_exhausted", Backoff: true},
	{Text: "server is temporarily unavailable", Backoff: true},
	{Text: "not found", CooldownMs: cooldownBannedAccount},
	{Text: "requested entity was not found", CooldownMs: cooldownBannedAccount},
	{Text: "not_found", CooldownMs: cooldownBannedAccount},

	// --- Status-based rules (fallback when text doesn't match) ---
	{Status: 401, CooldownMs: cooldownLong},
	{Status: 402, CooldownMs: cooldownLong},
	{Status: 403, CooldownMs: cooldownLong},
	{Status: 404, CooldownMs: cooldownBannedAccount},
	{Status: 429, Backoff: true},
	{Status: 502, Backoff: true},
	{Status: 503, Backoff: true},
	{Status: 504, Backoff: true},
}

// quotaExhaustedPatterns mirror the upstream classify429 QUOTA_EXHAUSTED_PATTERNS
// (plus the credit-balance wording gateways actually return). Matched
// case-insensitively against the error body.
var quotaExhaustedPatterns = []string{
	"monthly limit",
	"monthly quota",
	"per month limit",
	"per-month limit",
	"quota exceeded",
	"exceeded quota",
	"exceed quota",
	"exceeded your current quota",
	"insufficient quota",
	"insufficient balance",
	"insufficient credit",
	"billing cap",
	"credit exhausted",
	"credits exhausted",
	"out of credits",
	"no remaining credit",
	"hard limit",
	"plan limit",
	"resource exhausted",
	"individual quota reached",
	"enable overages",
	"billing required",
	"payment required",
	"balance=0",
	// CodeBuddy words a spent model quota as a frequency limit and states when
	// it resets: "usage exceeds frequency limit ... reset at <timestamp>". It is
	// not a transient throttle, so it belongs with the exhausted budgets rather
	// than the 2-second backoff floor.
	"frequency limit",
	// CodeBuddy-family exhausted verdicts whose stored text carries no
	// credit wording (fragment 977): upstream marks these accounts
	// testStatus "exhausted" outright — the 14018 credit code (its body
	// can be a bare code + an unrelated msg) and the 11140 account
	// restriction. The status derivation here is the Go equivalent of
	// that stored flag, so the vocabulary has to name both markers; the
	// strings are Tencent-only on the wire.
	"14018",
	"restricted due to unusual activity",
}

// LooksLikeQuotaExhausted reports whether an error body reads as a spent
// credit/quota budget rather than a transient failure. Exposed so the
// dashboard can classify stored errors (which predate any live request) with
// the same vocabulary the request path uses.
func LooksLikeQuotaExhausted(errorText string) bool {
	if errorText == "" {
		return false
	}
	lower := strings.ToLower(errorText)
	for _, pat := range quotaExhaustedPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

// rateLimitPatterns mark a throttle: the account is fine, it is being asked to
// slow down. Kept separate from quota exhaustion because the remedy differs --
// one waits, the other tops up. Matched case-insensitively against the error.
var rateLimitPatterns = []string{
	"rate limit",
	"rate_limit",
	"ratelimit",
	"too many requests",
	"resource_exhausted",
	"resource has been exhausted",
	"requests per minute",
	"rpm limit",
	"tpm limit",
	"tokens per minute",
	"requests per day",
	"rpd limit",
	"429",
}

// permanentFailurePatterns mark errors that will not clear by waiting: the
// credential is rejected, revoked, or points at something gone. Distinct from
// quota (needs money) and throttle (needs time) -- this needs the operator to
// fix the key.
var permanentFailurePatterns = []string{
	"invalid api key",
	"invalid_api_key",
	"invalid key",
	"apikey is invalid",
	"incorrect api key",
	"unauthorized",
	"authentication_error",
	"authentication failed",
	"permission denied",
	"access denied",
	"not authorized",
	"invalid_grant",
	"invalid_token",
	"token expired",
	"token has expired",
	"revoked",
	"no credentials",
	"account suspended",
	"account disabled",
	"account has been banned",
	"account has been deleted",
	"no project id",
	"validation_required",
	"verify your account",
	"terms of service",
	"tos_violation",
	"disabled in this account",
	"refresh_token_reused",
}

// LooksLikePermanentFailure reports whether an error means the credential is
// dead rather than temporarily unusable. Used by the dashboard so a rejected
// key is not silently reported as recovered once its cooldown lapses.
func LooksLikePermanentFailure(errorText string) bool {
	if errorText == "" {
		return false
	}
	lower := strings.ToLower(errorText)
	for _, pat := range permanentFailurePatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

// throttleWindowPatterns name an explicit short window (per minute, per second,
// rpm/tpm). These are throttles no matter what other words they carry: "quota
// exceeded per minute" is a rate limit, not a spent balance, and telling the
// operator to top up an account that only needs to wait is wrong advice.
var throttleWindowPatterns = []string{
	"per minute",
	"per-minute",
	"per second",
	"per-second",
	"per hour",
	"requests per minute",
	"requests per second",
	"tokens per minute",
	"tokens per second",
	"rpm limit",
	"tpm limit",
	"rps limit",
	"too many requests",
}

// LooksLikeThrottleWindow reports whether an error names a short rolling window
// that resets on its own. The dashboard uses it to keep such a message out of
// the quota-exhausted bucket, whose advice ("top up the account") would be
// wrong. Deliberately NOT applied to the request-path quota cooldown: parking a
// per-minute limit for an hour is wasteful, but mis-parking a real balance
// exhaustion for 30 seconds is worse, and that path already matches the
// narrower rules in ErrorRules.
func LooksLikeThrottleWindow(errorText string) bool {
	if errorText == "" {
		return false
	}
	lower := strings.ToLower(errorText)
	for _, pat := range throttleWindowPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

// LooksLikeRateLimited reports whether an error body reads as a throttle rather
// than a dead account. Mirrors LooksLikeQuotaExhausted for the dashboard's
// stored-error path.
func LooksLikeRateLimited(errorText string) bool {
	if errorText == "" {
		return false
	}
	lower := strings.ToLower(errorText)
	for _, pat := range rateLimitPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

// ModelFitErrorPatterns are the upstream messages that mean "this model cannot
// hold the request", as opposed to "this account cannot serve it". Matched
// case-insensitively against the error body.
//
// The distinction matters inside a combo: an account fault is worth another
// connection, but a prompt that does not fit the model gets the same answer from
// every connection to it, so the only useful move is a member with a bigger
// context window (#338).
var ModelFitErrorPatterns = []string{
	"context_length_exceeded",
	"maximum context length",
	"prompt is too long",
	"reduce the length of the messages",
	"input is too long",
	"too many tokens",
	"context window",
	"exceeds the maximum number of tokens",
	// A text-only model rejects the media an older turn still carries. The
	// trailing turn was text, so the request never registered as a vision
	// request and the history went out verbatim; every connection to this
	// model answers the same way, so like the length patterns above the only
	// useful move is the next member. The quoted spellings both matter: the
	// body is matched raw, so a JSON-encoded message carries the quotes as
	// \" while a plain-text error body carries them bare.
	`unsupported value \"image`,
	`unsupported value "image`,
	"not support image",
	"image input not supported",
	"images are not supported",
}

// ModelFitStatusCodes are the statuses an upstream uses to reject an oversized
// request. A 400 carries it almost everywhere; 413 and 422 are the other two
// spellings seen in the wild, and Zed sends it as a 403. 403 is otherwise the
// account-refusal status, so admitting it here means every 403 is re-checked
// against the body patterns before it can be called a fit failure — a plain
// permission refusal still lands on the generic 403 rule. Gating on the status
// keeps a provider's prose from triggering the classification on an unrelated
// failure.
var ModelFitStatusCodes = map[int]bool{
	http.StatusBadRequest:            true, // 400
	http.StatusRequestEntityTooLarge: true, // 413
	http.StatusUnprocessableEntity:   true, // 422
	http.StatusForbidden:             true, // 403, Zed
}

// IsModelFitError reports whether an upstream failure means the chosen model is
// too small for the request rather than the connection being at fault.
func IsModelFitError(statusCode int, body []byte) bool {
	if !ModelFitStatusCodes[statusCode] || len(body) == 0 {
		return false
	}
	text := strings.ToLower(string(body))
	for _, pattern := range ModelFitErrorPatterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

// IsModelConcurrencyError reports whether an upstream failure is a cluster-wide
// model concurrency limit (e.g. Dahl "model_concurrency"), meaning every connection
// on this provider will fail for this model. Combos should break to the next model
// immediately instead of exhausting connection retries.
func IsModelConcurrencyError(statusCode int, body []byte) bool {
	if statusCode != http.StatusTooManyRequests || len(body) == 0 {
		return false
	}
	text := strings.ToLower(string(body))
	return strings.Contains(text, "model_concurrency")
}

// IsUnsupportedMediaError reports whether an upstream rejected the request due to
// an unsupported media/vision block (e.g. "unsupported value \"image_url\"").
// Combos can failover to a vision-capable candidate.
func IsUnsupportedMediaError(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest || len(body) == 0 {
		return false
	}
	text := strings.ToLower(string(body))
	return strings.Contains(text, "image_url") && (strings.Contains(text, "unsupported") || strings.Contains(text, "not supported"))
}

// GetQuotaCooldown calculates exponential backoff cooldown for rate limits.
// Level 0 → 2s, Level 1 → 2s, Level 2 → 4s, Level 3 → 8s, ... capped at MaxMs.
func GetQuotaCooldown(backoffLevel int) int {
	level := max(backoffLevel-1, 0)
	cooldown := int(float64(BackoffConfig.BaseMs) * math.Pow(2, float64(level)))
	return min(cooldown, BackoffConfig.MaxMs)
}

// ErrorClassification holds the result of ClassifyError.
type ErrorClassification struct {
	ShouldFallback  bool
	CooldownMs      int
	NewBackoffLevel int // only meaningful when the matched rule has Backoff=true
	// BannedAccount marks the banned-account class: the classifier's
	// verdict is that the account itself is dead (a ban, a ToS
	// disablement, a gone entity), not throttled. The class cooldown
	// (cooldownBannedAccount, 24h) is a sentence, not a backoff, and the
	// chat layer must carry it through unclamped — see
	// cooldownSecForClassification in handlers/chat.
	BannedAccount bool
	// Exhausted marks the spent-budget class (upstream isExhausted): the
	// account parks with its long cooldown AND reads quota_exhausted via
	// the stored-error derivation — a lapsed timer does not refill a
	// balance, so the cooldown is a sentence like the banned class and
	// is likewise carried unclamped.
	Exhausted bool
	// ModelScoped marks a verdict against the model only (upstream
	// lockAll: false): consumers lock the model and must not write an
	// account lock for it.
	ModelScoped bool
}

// ClassifyError classifies an upstream error by matching text and status against ErrorRules.
// Returns the cooldown duration and new backoff level.
// Matches Next.js checkFallbackError() in open-sse/services/accountFallback.js.
func ClassifyError(statusCode int, errorText string, backoffLevel int) ErrorClassification {
	return ClassifyErrorForProvider(statusCode, errorText, backoffLevel, "")
}

// ClassifyErrorForProvider is ClassifyError with provider-scoped rules
// applied: provider is the canonical provider id the request routed to
// (aliases are resolved here, so `cbai` matches the codebuddy-intl rules).
func ClassifyErrorForProvider(statusCode int, errorText string, backoffLevel int, provider string) ErrorClassification {
	if provider != "" {
		provider = ResolveAlias(provider)
	}
	lowerError := ""
	if errorText != "" {
		lowerError = strings.ToLower(errorText)
	}

	// CodeBuddy-family gated verdicts run before the rule list, exactly
	// where upstream puts them: the 11140/11158 guards precede
	// ERROR_RULES in checkFallbackError (a bare "request illegal" text
	// rule would otherwise swallow the 11140 account fault), and the
	// family's 429 sentences override the generic rules in
	// markAccountUnavailable.
	if cls, ok := classifyCodebuddyFamily(statusCode, lowerError, backoffLevel, provider); ok {
		return cls
	}

	if cls, ok := classifyTokenHarbor(statusCode, lowerError, backoffLevel, provider); ok {
		return cls
	}

	// Model capability mismatch (e.g. text model receiving vision/image input)
	if statusCode == http.StatusBadRequest && strings.Contains(lowerError, "image_url") && (strings.Contains(lowerError, "unsupported") || strings.Contains(lowerError, "not supported")) {
		return ErrorClassification{
			ShouldFallback:  true,
			CooldownMs:      0,
			NewBackoffLevel: backoffLevel,
			ModelScoped:     true,
		}
	}

	for _, rule := range ErrorRules {
		// Text-based match (substring, case-insensitive)
		if rule.Text != "" && lowerError != "" && strings.Contains(lowerError, rule.Text) {
			if rule.Provider != "" && rule.Provider != provider {
				continue
			}
			if rule.RequireStatus != 0 && rule.RequireStatus != statusCode {
				continue
			}
			if rule.ExcludeStatus != 0 && rule.ExcludeStatus == statusCode {
				continue
			}
			if rule.RequestLevel {
				return ErrorClassification{
					ShouldFallback:  false,
					CooldownMs:      0,
					NewBackoffLevel: backoffLevel,
				}
			}
			if rule.Backoff {
				newLevel := min(backoffLevel+1, BackoffConfig.MaxLevel)
				return ErrorClassification{
					ShouldFallback:  true,
					CooldownMs:      GetQuotaCooldown(newLevel),
					NewBackoffLevel: newLevel,
				}
			}
			return ErrorClassification{
				ShouldFallback:  true,
				CooldownMs:      rule.CooldownMs,
				NewBackoffLevel: backoffLevel,
				BannedAccount:   rule.CooldownMs == cooldownBannedAccount,
				Exhausted:       rule.Exhausted,
				ModelScoped:     rule.ModelScoped,
			}
		}

		// Status-based match
		if rule.Status != 0 && rule.Status == statusCode {
			if rule.Backoff {
				newLevel := min(backoffLevel+1, BackoffConfig.MaxLevel)
				return ErrorClassification{
					ShouldFallback:  true,
					CooldownMs:      GetQuotaCooldown(newLevel),
					NewBackoffLevel: newLevel,
				}
			}
			return ErrorClassification{
				ShouldFallback:  true,
				CooldownMs:      rule.CooldownMs,
				NewBackoffLevel: backoffLevel,
				BannedAccount:   rule.CooldownMs == cooldownBannedAccount,
			}
		}
	}
	// Upstream parity (open-sse/services/accountFallback.js checkFallbackError):
	// request-scoped 4xx that match no rule say nothing about the credential,
	// so the account must not be cooled down. Account-scoped statuses keep
	// their rules above (401/402/403/404/429 + quota/capacity text rules).
	if statusCode >= 400 && statusCode < 500 && statusCode != 401 && statusCode != 402 && statusCode != 403 && statusCode != 404 && statusCode != 429 {
		return ErrorClassification{ShouldFallback: false}
	}

	// Default: transient cooldown for any unmatched error
	return ErrorClassification{
		ShouldFallback:  true,
		CooldownMs:      TransientCooldownMs,
		NewBackoffLevel: backoffLevel,
	}
}
