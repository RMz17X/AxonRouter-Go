package providers

import (
	"net/http"
	"testing"
)

func TestClassifyError_CapacityExhausted(t *testing.T) {
	// Status 503 with MODEL_CAPACITY_EXHAUSTED
	cls := ClassifyError(http.StatusServiceUnavailable, "No capacity available for model gemini-3.8-flash-tiered on the server MODEL_CAPACITY_EXHAUSTED", 0)
	if !cls.ShouldFallback {
		t.Error("expected fallback for capacity exhausted error")
	}
	if cls.NewBackoffLevel != 1 {
		t.Errorf("expected backoff level 1, got %d", cls.NewBackoffLevel)
	}
	if cls.CooldownMs != 2000 {
		t.Errorf("expected 2000ms cooldown, got %d", cls.CooldownMs)
	}
}

func TestClassifyError_ResourceExhausted429(t *testing.T) {
	cls := ClassifyError(http.StatusTooManyRequests, "Resource has been exhausted (e.g. check quota). RESOURCE_EXHAUSTED", 0)
	if !cls.ShouldFallback {
		t.Error("expected fallback for 429 resource exhausted")
	}
	if cls.NewBackoffLevel != 1 {
		t.Errorf("expected backoff level 1, got %d", cls.NewBackoffLevel)
	}
}

func TestClassifyError_CreditExhaustionParksForAnHour(t *testing.T) {
	// A spent budget is not a rate limit: the exponential ladder must not
	// advance, and the account must be parked for the long quota cooldown
	// instead of the 2-second backoff floor. Retrying a zero balance every
	// two seconds burns a guaranteed-failed upstream call and makes the
	// dashboard show a false recovery.
	//
	// Fragment 977 split the family of texts in two: the upstream
	// ERROR_RULES credit texts ("insufficient credits", ...) now park
	// for the 30-day monthly-exhausted sentence and are marked
	// exhausted; the spellings ERROR_RULES does not carry ("credit
	// balance is too low") keep the one-hour quota park below.
	cls := ClassifyError(http.StatusBadRequest, "Insufficient credits to complete this request", 0)
	if !cls.ShouldFallback || cls.NewBackoffLevel != 0 || cls.CooldownMs != cooldownMonthlyExhausted || !cls.Exhausted {
		t.Errorf("ERROR_RULES credit text classified %+v, want 30d exhausted park", cls)
	}
	for _, text := range []string{
		"Your credit balance is too low to access the API",
	} {
		cls := ClassifyError(http.StatusBadRequest, text, 0)
		if !cls.ShouldFallback {
			t.Errorf("expected fallback for %q, got %+v", text, cls)
		}
		if cls.NewBackoffLevel != 0 {
			t.Errorf("expected backoff level unchanged for %q, got %d", text, cls.NewBackoffLevel)
		}
		if cls.CooldownMs != QuotaExhaustedCooldownMs {
			t.Errorf("expected %dms cooldown for %q, got %d", QuotaExhaustedCooldownMs, text, cls.CooldownMs)
		}
	}
	// A 400 that carries no account fault still says nothing about the credential.
	cls = ClassifyError(http.StatusBadRequest, "invalid request body", 0)
	if cls.ShouldFallback {
		t.Errorf("expected no fallback for a plain 400, got %+v", cls)
	}
}

func TestClassifyError_Status503Fallback(t *testing.T) {
	// Plain 503 without specific matching text should trigger backoff via status rule
	cls := ClassifyError(http.StatusServiceUnavailable, "", 0)
	if !cls.ShouldFallback {
		t.Error("expected fallback for 503")
	}
	if cls.NewBackoffLevel != 1 {
		t.Errorf("expected backoff level 1 for 503, got %d", cls.NewBackoffLevel)
	}
}

func TestClassifyError_RequestScoped4xxNoFallback(t *testing.T) {
	// Upstream parity (checkFallbackError): request-scoped 4xx must not lock the account.
	for _, status := range []int{http.StatusBadRequest, http.StatusMethodNotAllowed, http.StatusConflict, http.StatusUnprocessableEntity} {
		cls := ClassifyError(status, "bad request body", 0)
		if cls.ShouldFallback {
			t.Errorf("expected no fallback for %d, got %+v", status, cls)
		}
	}
	// Account-scoped statuses keep fallback.
	for _, status := range []int{http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests} {
		cls := ClassifyError(status, "", 0)
		if !cls.ShouldFallback {
			t.Errorf("expected fallback for %d, got %+v", status, cls)
		}
	}
	// 404 keeps its rule (upstream NON_RETRIABLE + status rule).
	cls := ClassifyError(http.StatusNotFound, "", 0)
	if !cls.ShouldFallback {
		t.Errorf("expected fallback for 404, got %+v", cls)
	}
}

func TestClassifyDeepSeekWebAccountRefusals(t *testing.T) {
	// A ban names the account, so it parks it far longer than the generic
	// 2-minute 403 rule would.
	ban := ClassifyErrorForProvider(http.StatusForbidden, "deepseek-web: the upstream refused this account: user is banned", 0, "deepseek-web")
	if !ban.ShouldFallback {
		t.Error("a banned account must fall back")
	}
	if ban.CooldownMs != cooldownBannedAccount {
		t.Errorf("ban cooldown = %dms, want %dms", ban.CooldownMs, cooldownBannedAccount)
	}
	// An address refusal is a different verdict: the account is fine, the
	// egress is not, so the pause is short.
	address := ClassifyErrorForProvider(http.StatusForbidden, "deepseek-web: the upstream refused this address: ip access restricted", 0, "deepseek-web")
	if !address.ShouldFallback {
		t.Error("an address refusal must fall back")
	}
	if address.CooldownMs != cooldownAddressRefused {
		t.Errorf("address cooldown = %dms, want %dms", address.CooldownMs, cooldownAddressRefused)
	}
	// The rules are scoped to deepseek-web: another provider's 403 keeps the
	// generic 2-minute rule.
	other := ClassifyErrorForProvider(http.StatusForbidden, "the upstream refused this account", 0, "gemini")
	if other.CooldownMs != cooldownLong {
		t.Errorf("other provider 403 cooldown = %dms, want %dms", other.CooldownMs, cooldownLong)
	}
}

func TestClassify404AndToSViolation24hCooldown(t *testing.T) {
	// 404 status rule parks for 24 hours
	c404 := ClassifyErrorForProvider(http.StatusNotFound, "Requested entity was not found.", 0, "antigravity")
	if !c404.ShouldFallback {
		t.Error("404 must fall back")
	}
	if c404.CooldownMs != cooldownBannedAccount {
		t.Errorf("404 cooldown = %dms, want %dms (24h)", c404.CooldownMs, cooldownBannedAccount)
	}

	// ToS violation parks for 24 hours
	tos := ClassifyErrorForProvider(http.StatusForbidden, "This service has been disabled in this account for violation of Terms of Service. TOS_VIOLATION", 0, "antigravity")
	if !tos.ShouldFallback {
		t.Error("TOS violation must fall back")
	}
	if tos.CooldownMs != cooldownBannedAccount {
		t.Errorf("TOS cooldown = %dms, want %dms (24h)", tos.CooldownMs, cooldownBannedAccount)
	}
}

func TestClassifyDahlModelConcurrency(t *testing.T) {
	msg := `{"error":{"code":"model_concurrency","message":"This model is at concurrency capacity. Paid accounts are admitted first."}}`
	res := ClassifyErrorForProvider(http.StatusTooManyRequests, msg, 0, "dahl")
	if !res.ShouldFallback {
		t.Fatal("model_concurrency should trigger fallback to next candidate")
	}
	if !res.ModelScoped {
		t.Errorf("model_concurrency must be ModelScoped (not account-scoped)")
	}
	if res.CooldownMs != 30000 {
		t.Errorf("expected 30000ms (30s) cooldown, got %dms", res.CooldownMs)
	}

	// Also test extracted prose when code is absent in message text
	prose := "This model is at concurrency capacity. Paid accounts are admitted first."
	resProse := ClassifyErrorForProvider(http.StatusTooManyRequests, prose, 0, "dahl")
	if !resProse.ShouldFallback || !resProse.ModelScoped || resProse.CooldownMs != 30000 {
		t.Errorf("expected 30s model-scoped fallback for prose, got %+v", resProse)
	}
}

func TestClassifyUnsupportedImageUrl(t *testing.T) {
	msg := `messages[43].content[1].type has unsupported value "image_url"`
	res := ClassifyErrorForProvider(http.StatusBadRequest, msg, 0, "dahl")
	if !res.ShouldFallback {
		t.Fatal("unsupported image_url should trigger fallback to next candidate")
	}
	if !res.ModelScoped {
		t.Errorf("unsupported image_url must be ModelScoped")
	}
	if res.CooldownMs != 0 {
		t.Errorf("expected 0ms cooldown (no lock), got %dms", res.CooldownMs)
	}
}
