package providers

import (
	"net/http"
	"testing"
)

// CodeBuddy Intl refuses a request with HTTP 400 "request illegal" or
// "conversation id missing". Fragment 977 aligned these to upstream's
// current ERROR_RULES: they are request-level refusals (no fallback, no
// lock, no cooldown, backoff level unmoved) for every provider — the
// account/IP backoff reading this file used to pin came from an older
// upstream revision (3b137aa6) that the current tree no longer carries.
// The 11140 account fault that hides behind the same "request illegal"
// msg is claimed by the gated code verdict instead (see
// TestClassifyError_Codebuddy11140Verdicts).
func TestClassifyErrorForProvider_CodebuddyIntlBlocks(t *testing.T) {
	for _, msg := range []string{"11128:request illegal", "conversation id missing"} {
		cls := ClassifyErrorForProvider(400, msg, 0, "codebuddy-intl")
		if cls.ShouldFallback || cls.CooldownMs != 0 || cls.NewBackoffLevel != 0 {
			t.Errorf("block %q classified %+v, want request-level (no fallback, no cooldown)", msg, cls)
		}
	}
}

// A request-level verdict never moves the backoff level, at any level.
func TestClassifyErrorForProvider_CodebuddyRequestLevelKeepsBackoffLevel(t *testing.T) {
	for _, level := range []int{0, 1, 2, BackoffConfig.MaxLevel} {
		cls := ClassifyErrorForProvider(400, "request illegal", level, "codebuddy-intl")
		if cls.ShouldFallback || cls.CooldownMs != 0 || cls.NewBackoffLevel != level {
			t.Errorf("level %d classified %+v, want request-level with level unmoved", level, cls)
		}
	}
}

// The same texts stay request-scoped everywhere else: no cooldown, no
// fallback, and the backoff level does not move.
func TestClassifyErrorForProvider_OtherProvidersStayRequestScoped(t *testing.T) {
	for _, provider := range []string{"openai", "codebuddy-cn", ""} {
		cls := ClassifyErrorForProvider(400, "request illegal", 0, provider)
		if cls.ShouldFallback || cls.CooldownMs != 0 {
			t.Errorf("provider %q classified %+v, want request-scoped", provider, cls)
		}
	}
	// The legacy signature has no provider context, so the scoped rules
	// cannot fire there either.
	if cls := ClassifyError(400, "request illegal", 0); cls.ShouldFallback {
		t.Errorf("provider-agnostic classify fell back: %+v", cls)
	}
}

// Not every 11128 code is a block: a malformed request is the caller's bug.
func TestClassifyErrorForProvider_MalformedCodebuddyRequest(t *testing.T) {
	cls := ClassifyErrorForProvider(400, "first message is not system prompt", 0, "codebuddy-intl")
	if cls.ShouldFallback || cls.CooldownMs != 0 {
		t.Errorf("malformed request classified %+v, want request-scoped", cls)
	}
}

// The combo member id may arrive as the cbai alias; the family-gated
// 429 verdicts still match (fragment 977: 14018-family credit wording
// on the direct path is 7 days, exhausted).
func TestClassifyErrorForProvider_CbaiAliasResolves(t *testing.T) {
	cls := ClassifyErrorForProvider(429, "credits exhausted (code 14018)", 0, "cbai")
	if !cls.ShouldFallback || cls.CooldownMs != cooldownSevenDays || !cls.Exhausted {
		t.Errorf("cbai alias classified %+v, want 7d exhausted", cls)
	}
}

// The gated 11140 verdicts (fragment 977): restriction wording at
// 403 + code 11140 is 2h and exhausted; a bare 11140 is 5 minutes and
// not exhausted; a safety wording vetoes the guard back to
// request-level.
func TestClassifyError_Codebuddy11140Verdicts(t *testing.T) {
	restricted := ClassifyErrorForProvider(http.StatusForbidden,
		"Your account has been restricted due to unusual activity (code 11140)", 0, "codebuddy-intl")
	if !restricted.ShouldFallback || restricted.CooldownMs != codebuddyRestrictedCooldownMs || !restricted.Exhausted {
		t.Errorf("restricted 11140 classified %+v, want 2h exhausted", restricted)
	}
	bare := ClassifyErrorForProvider(http.StatusForbidden,
		"request illegal (code 11140)", 0, "codebuddy-cn")
	if !bare.ShouldFallback || bare.CooldownMs != codebuddyBare11140CooldownMs || bare.Exhausted {
		t.Errorf("bare 11140 classified %+v, want 5m not exhausted", bare)
	}
	safety := ClassifyErrorForProvider(http.StatusForbidden,
		"request did not pass the safety review (code 11140)", 0, "workbuddy")
	if safety.ShouldFallback || safety.CooldownMs != 0 {
		t.Errorf("safety 11140 classified %+v, want request-level", safety)
	}
}

func TestClassifyError_CodebuddyCodes(t *testing.T) {
	cls1 := ClassifyError(400, "11158: model_not_in_plan", 0)
	if !cls1.ShouldFallback || cls1.CooldownMs != 0 || !cls1.ModelScoped {
		t.Errorf("11158 classified %+v, want fallback with 0 model-scoped cooldown", cls1)
	}
	// The generic credit path (upstream ERROR_RULES) is 30 days,
	// exhausted — the old 2-minute generic cooldown for 14018 was the
	// fragment 977 bug.
	cls2 := ClassifyError(400, "14018: credits exhausted", 0)
	if !cls2.ShouldFallback || cls2.CooldownMs != cooldownMonthlyExhausted || !cls2.Exhausted {
		t.Errorf("14018 classified %+v, want 30d exhausted", cls2)
	}
	// A restriction verdict needs the gated shape (403 + code token);
	// the bare text alone no longer sentences any account.
	cls3 := ClassifyError(400, "11140: account has been restricted due to unusual activity", 0)
	if cls3.ShouldFallback || cls3.CooldownMs != 0 {
		t.Errorf("ungated restriction text classified %+v, want request-scoped", cls3)
	}
}
