package domain

import "testing"

// TestNewErrorCodes verifies the 11 new Appendix-A error codes exist as
// constants with the exact string values from 完整设计文档.md 附录 A, and that
// NewError constructs an *Error carrying them.
func TestNewErrorCodes(t *testing.T) {
	newCodes := []struct {
		code ErrorCode
		want string
	}{
		{CodeFeatureDisabled, "FEATURE_DISABLED"},
		{CodeQuotaExceeded, "QUOTA_EXCEEDED"},
		{CodeExamInProgress, "EXAM_IN_PROGRESS"},
		{CodePluginError, "PLUGIN_ERROR"},
		{CodeWebhookFailed, "WEBHOOK_FAILED"},
		{CodeShareExpired, "SHARE_EXPIRED"},
		{CodeFamilyBound, "FAMILY_BOUND"},
		{CodeReviewRequired, "REVIEW_REQUIRED"},
		{CodeWorkspaceLocked, "WORKSPACE_LOCKED"},
		{CodeStorageFull, "STORAGE_FULL"},
		{CodeMigrationBlocked, "MIGRATION_BLOCKED"},
	}
	for _, tc := range newCodes {
		t.Run(tc.want, func(t *testing.T) {
			if string(tc.code) != tc.want {
				t.Errorf("code string = %q, want %q", tc.code, tc.want)
			}
			e := NewError(tc.code, "测试消息")
			if e.Code != tc.code {
				t.Errorf("NewError code = %s, want %s", e.Code, tc.code)
			}
			if e.Message != "测试消息" {
				t.Errorf("NewError message = %q, want %q", e.Message, "测试消息")
			}
			if e.Retryable {
				t.Error("NewError retryable should default to false")
			}
		})
	}
}

// TestNewCodeFactories verifies each new code has a factory method that
// returns an *Error with the matching code (NewError-based via E).
func TestNewCodeFactories(t *testing.T) {
	cases := []struct {
		name string
		got  *Error
		want ErrorCode
	}{
		{"feature_disabled", FeatureDisabled("功能未启用"), CodeFeatureDisabled},
		{"quota_exceeded", QuotaExceeded("配额超限"), CodeQuotaExceeded},
		{"exam_in_progress", ExamInProgress("考试进行中"), CodeExamInProgress},
		{"plugin_error", PluginError("插件异常"), CodePluginError},
		{"webhook_failed", WebhookFailed("webhook 投递失败"), CodeWebhookFailed},
		{"share_expired", ShareExpired("分享链接已过期"), CodeShareExpired},
		{"family_bound", FamilyBound("家庭绑定冲突"), CodeFamilyBound},
		{"review_required", ReviewRequired("内容需人工审核"), CodeReviewRequired},
		{"workspace_locked", WorkspaceLocked("工作区被占用"), CodeWorkspaceLocked},
		{"storage_full", StorageFull("磁盘空间不足"), CodeStorageFull},
		{"migration_blocked", MigrationBlocked("数据库版本过高"), CodeMigrationBlocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got == nil {
				t.Fatalf("factory returned nil for %s", tc.name)
			}
			if tc.got.Code != tc.want {
				t.Errorf("factory code = %s, want %s", tc.got.Code, tc.want)
			}
		})
	}
}
