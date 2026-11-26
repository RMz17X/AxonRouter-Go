package providers

import (
	"net/http"
	"testing"
)

// DeepAI Code answers a revoked/invalidated session with 401 auth_required,
// not the generic "unauthorized" / "invalid api key" text. That verdict must
// deactivate the connection on the first sighting so the router rotates to a
// healthy account instead of locking the dead one for a minute and drawing it
// again on the next request.
func TestClassifyDeepAIAuthRequiredDeactivates(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		statusCode int
		body       string
		want       bool
	}{
		{
			name:       "deepai auth_required",
			provider:   "deepai",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":{"message":"Sign in to DeepAI to use DeepAI Code.","type":"auth_required","code":"auth_required"}}`,
			want:       true,
		},
		{
			name:       "deepai sign in prose only",
			provider:   "deepai",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":{"message":"Sign in to DeepAI to use DeepAI Code."}}`,
			want:       true,
		},
		{
			name:       "other provider 401",
			provider:   "openai",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":{"type":"auth_required"}}`,
			want:       false,
		},
		{
			name:       "deepai non-401",
			provider:   "deepai",
			statusCode: http.StatusForbidden,
			body:       `{"error":{"type":"auth_required"}}`,
			want:       false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cls := ClassifyErrorForProvider(tt.statusCode, tt.body, 0, tt.provider)
			if cls.DeactivateKey != tt.want {
				t.Errorf("DeactivateKey = %v, want %v (class %+v)", cls.DeactivateKey, tt.want, cls)
			}
		})
	}
}

func TestLooksLikePermanentFailureAuthRequired(t *testing.T) {
	if !LooksLikePermanentFailure(`{"error":{"code":"auth_required"}}`) {
		t.Error("auth_required must be reported as a permanent credential failure")
	}
}
