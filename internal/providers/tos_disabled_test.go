package providers

import (
	"net/http"
	"testing"
)

func TestLooksLikeToSDisabled(t *testing.T) {
	cases := []struct {
		name   string
		status int
		text   string
		want   bool
	}{
		{
			name:   "google tos violation token",
			status: http.StatusForbidden,
			text:   `{"error":{"message":"This service has been disabled in this account for violation of Terms of Service.","status":"TOS_VIOLATION"}}`,
			want:   true,
		},
		{
			name:   "disablement sentence without token",
			status: http.StatusForbidden,
			text:   "This service has been disabled in this account.",
			want:   true,
		},
		{
			name:   "tokenharbor consent refusal must not read as a tos disable",
			status: http.StatusForbidden,
			text:   `{"error":{"code":"free_models_disabled","message":"Free models are disabled in this account. Confirm free-model data sharing to enable them."}}`,
			want:   false,
		},
		{
			name:   "consent wording alone also vetoes",
			status: http.StatusForbidden,
			text:   "Free models are disabled in this account. Please confirm free model data sharing.",
			want:   false,
		},
		{
			name:   "other statuses never match",
			status: http.StatusTooManyRequests,
			text:   "This service has been disabled in this account for violation of Terms of Service.",
			want:   false,
		},
		{
			name:   "empty text never matches",
			status: http.StatusForbidden,
			text:   "",
			want:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LooksLikeToSDisabled(c.status, c.text); got != c.want {
				t.Errorf("LooksLikeToSDisabled(%d, %q) = %v, want %v", c.status, c.text, got, c.want)
			}
		})
	}
}
