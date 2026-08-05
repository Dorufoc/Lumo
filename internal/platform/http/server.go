package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"lumo/internal/agent"
	"lumo/internal/domain"
)

// Handler 是方法名式 RPC 处理器：解码请求体并返回 data 或 error。
// 请求体字段为方法参数（与 Wails 绑定方法签名一致）。
type Handler func(ctx context.Context, body map[string]json.RawMessage) (any, error)

// UploadHandler 处理 multipart/form-data 上传（返回统一信封）。
type UploadHandler func(w http.ResponseWriter, r *http.Request)

// SSEBus 是事件流订阅接口（由 agent.Bus 实现）。
type SSEBus interface {
	Subscribe(sessionID string) (<-chan agent.Event, func())
}

// Server 组装 HTTP 服务。
type Server struct {
	router  map[string]Handler
	uploads map[string]UploadHandler
	mux     *http.ServeMux
	sseBus  SSEBus
}

// NewServer 创建空服务。
func NewServer() *Server {
	s := &Server{
		router:  map[string]Handler{},
		uploads: map[string]UploadHandler{},
		mux:     http.NewServeMux(),
	}
	s.mux.HandleFunc("/api/v1/", s.dispatch)
	s.mux.HandleFunc("/api/v1/events", s.sse)
	s.mux.HandleFunc("/health", s.health)
	return s
}

// RegisterSSE 注册事件总线（AI 流式推送）。
func (s *Server) RegisterSSE(bus SSEBus) { s.sseBus = bus }

// sse 处理 GET /api/v1/events?request_id=..&session_id=.. 长连接流。
func (s *Server) sse(w http.ResponseWriter, r *http.Request) {
	if s.sseBus == nil {
		writeJSON(w, http.StatusServiceUnavailable, EnvelopeError(
			domain.InvalidState("事件流未启用"), requestID(r, nil)))
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, EnvelopeError(
			domain.InvalidArg("session_id 必填"), requestID(r, nil)))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, EnvelopeError(
			domain.InvalidState("流式响应不受支持"), requestID(r, nil)))
		return
	}
	ch, unsub := s.sseBus.Subscribe(sessionID)
	defer unsub()
	// 心跳
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case ev := <-ch:
			data, err := json.Marshal(ev.Payload)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Name, data); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// Register 注册绑定方法。
func (s *Server) Register(method string, h Handler) {
	if _, dup := s.router[method]; dup {
		panic("duplicate method: " + method)
	}
	s.router[method] = h
}

// RegisterUpload 注册 multipart 上传方法。
func (s *Server) RegisterUpload(method string, h UploadHandler) {
	if _, dup := s.uploads[method]; dup {
		panic("duplicate upload method: " + method)
	}
	s.uploads[method] = h
}

// Mux 返回底层 mux（供静态文件挂载等使用）。
func (s *Server) Mux() *http.ServeMux { return s.mux }

// dispatch 将 POST /api/v1/{Method} 分发给注册的 handler。
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, EnvelopeError(
			domain.InvalidArg("仅支持 POST 方法"), requestID(r, nil)))
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	// multipart/form-data 上传走专用 handler。
	if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "multipart/form-data") {
		if h, ok := s.uploads[name]; ok {
			h(w, r)
			return
		}
		writeJSON(w, http.StatusNotFound, EnvelopeError(
			domain.NotFound("未知上传方法 %s", name), requestID(r, nil)))
		return
	}
	h, ok := s.router[name]
	if !ok {
		writeJSON(w, http.StatusNotFound, EnvelopeError(
			domain.NotFound("未知方法 %s", name), requestID(r, nil)))
		return
	}

	var body map[string]json.RawMessage
	if r.ContentLength > 0 {
		if err := decodeBody(w, r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, EnvelopeError(err, requestID(r, nil)))
			return
		}
	}
	if body == nil {
		body = map[string]json.RawMessage{}
	}
	rid := requestID(r, body)

	data, err := h(r.Context(), body)
	if err != nil {
		code := domain.AsError(err)
		status := httpStatus(code.Code)
		slog.Warn("method failed", "method", name, "request_id", rid,
			"code", code.Code, "message", code.Message)
		writeJSON(w, status, EnvelopeError(code, rid))
		return
	}
	slog.Debug("method ok", "method", name, "request_id", rid)
	writeJSON(w, http.StatusOK, Envelope(data, rid))
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Envelope(map[string]any{"status": "ok"}, requestID(r, nil)))
}

func httpStatus(code domain.ErrorCode) int {
	switch code {
	case domain.CodeInvalidArgument, domain.CodeFeatureDisabled, domain.CodePluginError:
		return http.StatusBadRequest
	case domain.CodeUnauthorized:
		return http.StatusUnauthorized
	case domain.CodeForbidden:
		return http.StatusForbidden
	case domain.CodeNotFound:
		return http.StatusNotFound
	case domain.CodeConflict, domain.CodeInvalidState, domain.CodeExamInProgress,
		domain.CodeFamilyBound, domain.CodeReviewRequired, domain.CodeWorkspaceLocked,
		domain.CodeMigrationBlocked:
		return http.StatusConflict
	case domain.CodeShareExpired:
		return http.StatusGone
	case domain.CodeQuotaExceeded, domain.CodeStorageFull:
		return http.StatusTooManyRequests
	case domain.CodeInternal, domain.CodeDatabaseUnavailable:
		return http.StatusInternalServerError
	case domain.CodeWebhookFailed:
		return http.StatusBadGateway
	default:
		return http.StatusServiceUnavailable
	}
}
