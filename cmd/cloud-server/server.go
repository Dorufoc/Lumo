// server.go —— cloud-server 的 HTTP 处理器：按 API 设计文档第 4 章契约实现
// POST /v1/devices、POST /v1/sync/push、GET /v1/sync/pull、POST /v1/backups、
// DELETE /v1/workspaces/{id}。错误用 HTTP 状态码 + JSON {error:{code,message}} 表达。
package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"lumo/internal/domain"
)

// 错误码：复用既有稳定错误码字符串，不引入 domain 新枚举。
const (
	codeUnauthorized       = "UNAUTHORIZED"
	codeInvalidArgument    = "INVALID_ARGUMENT"
	codeConflict           = "CONFLICT"
	codeInternal           = "INTERNAL"
	codeServiceUnavailable = "SERVICE_UNAVAILABLE"
)

// Server 是 cloud-server HTTP 处理器。
type Server struct {
	store *Store
	token string // CLOUD_SERVER_TOKEN；空表示未配置，拒绝一切请求
	now   func() string
}

// NewServer 构造服务端。token 为空时所有请求被拒绝（503）。
func NewServer(store *Store, token string) *Server {
	return &Server{store: store, token: token, now: func() string { return domain.NowUTC() }}
}

// Handler 返回认证包裹的路由处理器。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/devices", s.handleDevices)
	mux.HandleFunc("/v1/sync/push", s.handleSyncPush)
	mux.HandleFunc("/v1/sync/pull", s.handleSyncPull)
	mux.HandleFunc("/v1/backups", s.handleBackups)
	mux.HandleFunc("/v1/workspaces/", s.handleWorkspaces)
	return s.auth(mux)
}

// auth 认证中间件：未配置 CLOUD_SERVER_TOKEN 时拒绝一切请求（503）；
// 配置后校验 Authorization: Bearer <token>（不匹配 401）；X-Device-ID 必须存在。
// 所有响应附带 X-Request-ID 与 Date。
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-ID")
		if rid == "" {
			rid = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", rid)
		w.Header().Set("Date", s.now())
		if s.token == "" {
			writeError(w, http.StatusServiceUnavailable, codeServiceUnavailable, "服务器未配置访问令牌（CLOUD_SERVER_TOKEN）")
			return
		}
		const prefix = "Bearer "
		authz := r.Header.Get("Authorization")
		if !strings.HasPrefix(authz, prefix) {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "缺少 Authorization 请求头")
			return
		}
		tok := strings.TrimSpace(authz[len(prefix):])
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "无效的访问令牌")
			return
		}
		if r.Header.Get("X-Device-ID") == "" {
			writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少 X-Device-ID 请求头")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- DTO（与客户端 SyncService 契约一致）----

// Device 是设备注册信息。
type Device struct {
	DeviceID    string `json:"device_id"`
	WorkspaceID string `json:"-"`
	DeviceName  string `json:"device_name"`
	Platform    string `json:"platform"`
	AppVersion  string `json:"app_version"`
	LastSeenAt  string `json:"-"`
	CreatedAt   string `json:"-"`
}

// Op 是变更操作 DTO（对应 SyncOpDTO）。
type Op struct {
	OperationID string          `json:"operation_id"`
	EntityType  string          `json:"entity_type"`
	EntityID    string          `json:"entity_id"`
	BaseVersion int             `json:"base_version"`
	Operation   string          `json:"operation"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   string          `json:"created_at"`
}

// valid 校验操作字段是否合法（rejected 判定）。
func (o Op) valid() bool {
	if o.OperationID == "" || o.EntityType == "" || o.EntityID == "" || o.Operation == "" {
		return false
	}
	switch o.Operation {
	case "create", "update", "delete":
	default:
		return false
	}
	if o.BaseVersion < 0 || !json.Valid(o.Payload) {
		return false
	}
	return true
}

// ItemResult 是逐项推送结果（对应 PushItemResult）。
type ItemResult struct {
	OperationID   string          `json:"operation_id"`
	Result        string          `json:"result"` // accepted | duplicate | conflict | rejected
	ServerSeq     *int64          `json:"server_sequence,omitempty"`
	ServerVersion *int            `json:"server_version,omitempty"`
	ConflictCopy  json.RawMessage `json:"conflict_copy,omitempty"`
}

// Backup 是备份元数据登记。
type Backup struct {
	ID          string `json:"backup_id"`
	WorkspaceID string `json:"workspace_id"`
	DeviceID    string `json:"-"`
	Name        string `json:"name"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	MetaJSON    string `json:"-"`
	CreatedAt   string `json:"created_at"`
}

// ---- 端点实现 ----

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, codeInvalidArgument, "仅支持 POST")
		return
	}
	var req struct {
		DeviceID   string `json:"device_id"`
		DeviceName string `json:"device_name"`
		Platform   string `json:"platform"`
		AppVersion string `json:"app_version"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体解析失败: "+err.Error())
		return
	}
	if req.DeviceID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "device_id 必填")
		return
	}
	now := s.now()
	status, err := s.store.RegisterDevice(r.Context(), Device{
		DeviceID: req.DeviceID, DeviceName: req.DeviceName, Platform: req.Platform,
		AppVersion: req.AppVersion, LastSeenAt: now, CreatedAt: now,
	})
	if err != nil {
		slog.Error("注册设备失败", "error", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "服务器内部错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id": req.DeviceID, "status": status, "server_time": now,
		"workspace": "", "cursor": 0,
	})
}

func (s *Server) handleSyncPush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, codeInvalidArgument, "仅支持 POST")
		return
	}
	var req struct {
		WorkspaceID string `json:"workspace_id"`
		Operations  []Op   `json:"operations"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体解析失败: "+err.Error())
		return
	}
	if req.WorkspaceID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "workspace_id 必填")
		return
	}
	items, err := s.store.PushOps(r.Context(), req.WorkspaceID, r.Header.Get("X-Device-ID"), req.Operations, s.now())
	if errors.Is(err, ErrWorkspaceDeleting) {
		writeError(w, http.StatusConflict, codeConflict, "工作区处于删除期，拒绝新的写操作")
		return
	}
	if err != nil {
		slog.Error("推送变更失败", "error", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "服务器内部错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace_id": req.WorkspaceID, "items": items, "server_time": s.now(),
	})
}

func (s *Server) handleSyncPull(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, codeInvalidArgument, "仅支持 GET")
		return
	}
	q := r.URL.Query()
	wsID := q.Get("workspace_id")
	if wsID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "workspace_id 必填")
		return
	}
	var cursor int64
	_ = parseCursor(q.Get("cursor"), &cursor)
	limit := 200
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	ops, next, hasMore, err := s.store.PullOps(r.Context(), wsID, cursor, limit)
	if err != nil {
		slog.Error("拉取变更失败", "error", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "服务器内部错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"operations": ops, "next_cursor": next, "has_more": hasMore, "server_time": s.now(),
	})
}

func (s *Server) handleBackups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, codeInvalidArgument, "仅支持 POST")
		return
	}
	var req struct {
		WorkspaceID string         `json:"workspace_id"`
		Name        string         `json:"name"`
		SizeBytes   int64          `json:"size_bytes"`
		SHA256      string         `json:"sha256"`
		Meta        map[string]any `json:"meta,omitempty"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体解析失败: "+err.Error())
		return
	}
	if req.WorkspaceID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "workspace_id 必填")
		return
	}
	now := s.now()
	meta := "{}"
	if req.Meta != nil {
		if b, err := json.Marshal(req.Meta); err == nil {
			meta = string(b)
		}
	}
	b := Backup{
		ID: uuid.NewString(), WorkspaceID: req.WorkspaceID, DeviceID: r.Header.Get("X-Device-ID"),
		Name: req.Name, SizeBytes: req.SizeBytes, SHA256: req.SHA256, MetaJSON: meta, CreatedAt: now,
	}
	err := s.store.CreateBackup(r.Context(), b)
	if errors.Is(err, ErrWorkspaceDeleting) {
		writeError(w, http.StatusConflict, codeConflict, "工作区处于删除期，拒绝新的写操作")
		return
	}
	if err != nil {
		slog.Error("登记备份失败", "error", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "服务器内部错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"backup_id": b.ID, "workspace_id": req.WorkspaceID, "created_at": now, "status": "stored",
	})
}

func (s *Server) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, codeInvalidArgument, "仅支持 DELETE")
		return
	}
	// 路径形如 /v1/workspaces/{id}
	id := strings.TrimPrefix(r.URL.Path, "/v1/workspaces/")
	if id == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少工作区 id")
		return
	}
	now := s.now()
	deletedAt, deadline, err := s.store.SoftDeleteWorkspace(r.Context(), id, now)
	if err != nil {
		slog.Error("软删工作区失败", "error", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "服务器内部错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace_id": id, "deleted_at": deletedAt, "undo_deadline": deadline,
	})
}

// ---- 辅助 ----

// errBody 是错误响应体。
type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var b errBody
	b.Error.Code = code
	b.Error.Message = message
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json response", "error", err)
	}
}

// decodeBody 解码请求体，限制大小并拒绝未知字段。
func decodeBody(w http.ResponseWriter, r *http.Request, target any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(target)
}

// parseCursor 解析游标参数（缺省视为 0）。
func parseCursor(v string, out *int64) error {
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return err
	}
	*out = n
	return nil
}
