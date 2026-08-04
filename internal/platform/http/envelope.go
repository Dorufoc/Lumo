// Package http 是 Web 传输层：方法名式 RPC over HTTP + SSE 事件流。
// 服务层（internal/service）不依赖本包；未来迁移到 Wails 绑定时仅替换本层。
package http

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"lumo/internal/domain"
)

// ErrorDTO 是统一错误结构，与 API 文档 1.2 一致。
type ErrorDTO struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

// Response 是统一响应信封。
type Response struct {
	Data      any      `json:"data"`
	Error     *ErrorDTO `json:"error"`
	RequestID string   `json:"request_id"`
}

// Envelope 构造成功响应。
func Envelope(data any, requestID string) Response {
	return Response{Data: data, Error: nil, RequestID: requestID}
}

// EnvelopeError 构造失败响应。
func EnvelopeError(err error, requestID string) Response {
	de := domain.AsError(err)
	return Response{
		Data:      nil,
		Error:     &ErrorDTO{Code: string(de.Code), Message: de.Message, Retryable: de.Retryable, Details: de.Details},
		RequestID: requestID,
	}
}

// writeJSON 写出 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json response", "error", err)
	}
}

// WriteJSON 与 WriteErrorJSON 供 app 层（上传处理器等）复用。
func WriteJSON(w http.ResponseWriter, status int, v any) { writeJSON(w, status, v) }
func WriteErrorJSON(w http.ResponseWriter, v any)        { writeJSON(w, http.StatusBadRequest, v) }

// decodeBody 将请求体解码到 target，限制大小并拒绝未知字段。
func decodeBody(w http.ResponseWriter, r *http.Request, target any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return domain.InvalidArg("请求体解析失败: %v", err)
	}
	return nil
}

// requestID 从请求头或请求体提取 request_id；缺失时生成新值。
func requestID(r *http.Request, body map[string]json.RawMessage) string {
	if v := r.Header.Get("X-Request-ID"); v != "" {
		return v
	}
	if raw, ok := body["request_id"]; ok {
		var v string
		if json.Unmarshal(raw, &v) == nil && v != "" {
			return v
		}
	}
	return uuid.NewString()
}
