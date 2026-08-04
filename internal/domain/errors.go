// Package domain 包含领域实体、值对象、错误模型与不依赖基础设施的规则。
package domain

import "fmt"

// ErrorCode 是稳定的错误码，与 API 文档附录 A 保持一致。
type ErrorCode string

const (
	CodeInvalidArgument     ErrorCode = "INVALID_ARGUMENT"
	CodeUnauthorized        ErrorCode = "UNAUTHORIZED"
	CodeForbidden           ErrorCode = "FORBIDDEN"
	CodeNotFound            ErrorCode = "NOT_FOUND"
	CodeConflict            ErrorCode = "CONFLICT"
	CodeInvalidState        ErrorCode = "INVALID_STATE"
	CodeDatabaseUnavailable ErrorCode = "DATABASE_UNAVAILABLE"
	CodeProviderTimeout     ErrorCode = "PROVIDER_TIMEOUT"
	CodeProviderRateLimited ErrorCode = "PROVIDER_RATE_LIMITED"
	CodeOutputInvalid       ErrorCode = "OUTPUT_INVALID"
	CodeImportFailed        ErrorCode = "IMPORT_FAILED"
	CodeSandboxLimit        ErrorCode = "SANDBOX_LIMIT"
	CodeRequestCancelled    ErrorCode = "REQUEST_CANCELLED"
	CodeInternal            ErrorCode = "INTERNAL"
)

// Error 是领域错误，包含稳定错误码、用户可读消息、是否可重试与附加详情。
type Error struct {
	Code      ErrorCode
	Message   string
	Retryable bool
	Details   map[string]any
	Err       error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// NewError 构造领域错误。
func NewError(code ErrorCode, message string) *Error {
	return &Error{Code: code, Message: message}
}

func WrapError(code ErrorCode, message string, err error) *Error {
	return &Error{Code: code, Message: message, Err: err}
}

// E 是便捷构造器。
func E(code ErrorCode, format string, args ...any) *Error {
	return NewError(code, fmt.Sprintf(format, args...))
}

// AsError 将任意错误归一化为 *Error；未知错误映射为 INTERNAL。
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	if de, ok := err.(*Error); ok {
		return de
	}
	return &Error{Code: CodeInternal, Message: "内部错误", Err: err}
}

// Common 构造器集合，方便调用方使用。
func NotFound(format string, args ...any) *Error   { return E(CodeNotFound, format, args...) }
func InvalidArg(format string, args ...any) *Error { return E(CodeInvalidArgument, format, args...) }
func InvalidState(format string, args ...any) *Error {
	return E(CodeInvalidState, format, args...)
}
func Conflict(format string, args ...any) *Error  { return E(CodeConflict, format, args...) }
func Forbidden(format string, args ...any) *Error { return E(CodeForbidden, format, args...) }
