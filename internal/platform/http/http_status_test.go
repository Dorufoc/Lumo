package http

import (
	"net/http"
	"testing"

	"lumo/internal/domain"
)

// TestHTTPStatusBaseline pins the CURRENT httpStatus mapping for the 13
// Appendix-A codes plus CodeInternal, as it exists BEFORE Todo 3. It must pass
// on unchanged code and keep passing after the mapping extension (existing
// codes keep their current status).
func TestHTTPStatusBaseline(t *testing.T) {
	cases := []struct {
		name string
		code domain.ErrorCode
		want int
	}{
		{"invalid_argument", domain.CodeInvalidArgument, http.StatusBadRequest},
		{"unauthorized", domain.CodeUnauthorized, http.StatusUnauthorized},
		{"forbidden", domain.CodeForbidden, http.StatusForbidden},
		{"not_found", domain.CodeNotFound, http.StatusNotFound},
		{"conflict", domain.CodeConflict, http.StatusConflict},
		{"invalid_state", domain.CodeInvalidState, http.StatusConflict},
		{"database_unavailable", domain.CodeDatabaseUnavailable, http.StatusInternalServerError},
		{"provider_timeout", domain.CodeProviderTimeout, http.StatusServiceUnavailable},
		{"provider_rate_limited", domain.CodeProviderRateLimited, http.StatusServiceUnavailable},
		{"output_invalid", domain.CodeOutputInvalid, http.StatusServiceUnavailable},
		{"import_failed", domain.CodeImportFailed, http.StatusServiceUnavailable},
		{"sandbox_limit", domain.CodeSandboxLimit, http.StatusServiceUnavailable},
		{"request_cancelled", domain.CodeRequestCancelled, http.StatusServiceUnavailable},
		{"internal", domain.CodeInternal, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := httpStatus(tc.code); got != tc.want {
				t.Errorf("httpStatus(%s) = %d, want %d", tc.code, got, tc.want)
			}
		})
	}
}

// TestHTTPStatusNewCodes pins the Todo-3 mapping for the 11 new codes.
// Uses literal code strings so this compiles (and fails) against the
// UNCHANGED httpStatus, proving the mapping does not yet exist.
func TestHTTPStatusNewCodes(t *testing.T) {
	cases := []struct {
		name string
		code domain.ErrorCode
		want int
	}{
		{"feature_disabled", "FEATURE_DISABLED", http.StatusBadRequest},
		{"plugin_error", "PLUGIN_ERROR", http.StatusBadRequest},
		{"exam_in_progress", "EXAM_IN_PROGRESS", http.StatusConflict},
		{"family_bound", "FAMILY_BOUND", http.StatusConflict},
		{"review_required", "REVIEW_REQUIRED", http.StatusConflict},
		{"workspace_locked", "WORKSPACE_LOCKED", http.StatusConflict},
		{"migration_blocked", "MIGRATION_BLOCKED", http.StatusConflict},
		{"share_expired", "SHARE_EXPIRED", http.StatusGone},
		{"quota_exceeded", "QUOTA_EXCEEDED", http.StatusTooManyRequests},
		{"storage_full", "STORAGE_FULL", http.StatusTooManyRequests},
		{"webhook_failed", "WEBHOOK_FAILED", http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := httpStatus(tc.code); got != tc.want {
				t.Errorf("httpStatus(%s) = %d, want %d", tc.code, got, tc.want)
			}
		})
	}
}

// TestHTTPStatusUnknownCode: an unregistered error code must fall back to 503
// and must not panic.
func TestHTTPStatusUnknownCode(t *testing.T) {
	if got := httpStatus("NO_SUCH_CODE_XYZ"); got != http.StatusServiceUnavailable {
		t.Errorf("httpStatus(unknown) = %d, want 503", got)
	}
}

// TestEnvelopeStatusIntegration proves the RPC envelope carries error.code as
// the string code and httpStatus maps it to the pinned HTTP status
// (e.g. EXAM_IN_PROGRESS -> "EXAM_IN_PROGRESS" -> 409, SHARE_EXPIRED -> 410).
func TestEnvelopeStatusIntegration(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode string
		wantHTTP int
	}{
		{"exam_in_progress", domain.NewError(domain.CodeExamInProgress, "考试进行中不允许该操作"), "EXAM_IN_PROGRESS", http.StatusConflict},
		{"share_expired", domain.NewError(domain.CodeShareExpired, "分享链接已过期"), "SHARE_EXPIRED", http.StatusGone},
		{"feature_disabled", domain.NewError(domain.CodeFeatureDisabled, "功能未启用"), "FEATURE_DISABLED", http.StatusBadRequest},
		{"quota_exceeded", domain.NewError(domain.CodeQuotaExceeded, "配额超限"), "QUOTA_EXCEEDED", http.StatusTooManyRequests},
		{"webhook_failed", domain.NewError(domain.CodeWebhookFailed, "webhook 投递失败"), "WEBHOOK_FAILED", http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := EnvelopeError(tc.err, "req-1")
			if env.Error == nil {
				t.Fatal("envelope error is nil")
			}
			if env.Error.Code != tc.wantCode {
				t.Errorf("envelope error.code = %q, want %q", env.Error.Code, tc.wantCode)
			}
			de := domain.AsError(tc.err)
			if string(de.Code) != tc.wantCode {
				t.Errorf("AsError code = %q, want %q", de.Code, tc.wantCode)
			}
			if got := httpStatus(de.Code); got != tc.wantHTTP {
				t.Errorf("httpStatus(%s) = %d, want %d", de.Code, got, tc.wantHTTP)
			}
		})
	}
}
