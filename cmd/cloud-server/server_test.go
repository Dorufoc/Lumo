// 契约测试（TDD，先写后实现）：按 API 设计文档第 4 章 4.1–4.5 逐项验收。
// 覆盖：认证头（未配置拒绝 / 错误 token 401 / 正确 token 通过）、设备注册、
// push 逐项 accepted/duplicate/conflict/rejected、device:revoked 操作、
// pull 游标分页、延迟删除期拒绝写（409）。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// ---- 测试辅助 ----

const testToken = "test-secret-token"

// newTestServer 构造带临时 SQLite 的测试服务器。
func newTestServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "cloud.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := NewServer(store, token)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// doReq 发送请求并返回响应与响应体。
func doReq(t *testing.T, method, url, token, deviceID string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if deviceID != "" {
		req.Header.Set("X-Device-ID", deviceID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, rb
}

// errorEnvelope 解析错误信封 {error:{code,message}}。
func errorEnvelope(t *testing.T, body []byte) (string, string) {
	t.Helper()
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("解析错误信封失败: %v (body=%s)", err, body)
	}
	return env.Error.Code, env.Error.Message
}

func mustStatus(t *testing.T, resp *http.Response, want int, body []byte) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("状态码 = %d，期望 %d（body=%s）", resp.StatusCode, want, body)
	}
}

// pushOp 是推送操作的测试便捷构造。
type pushOp map[string]any

func pushReq(wsID string, ops ...pushOp) map[string]any {
	return map[string]any{"workspace_id": wsID, "operations": ops}
}

func op(id, entityType, entityID string, baseVersion int, operation string, payload any) pushOp {
	return pushOp{
		"operation_id": id, "entity_type": entityType, "entity_id": entityID,
		"base_version": baseVersion, "operation": operation,
		"payload": payload, "created_at": "2026-08-08T00:00:00Z",
	}
}

func jsonContains(raw []byte, s string) bool {
	return bytes.Contains(raw, []byte(s))
}

// ---- 4.1 认证 ----

func TestAuthTokenUnconfiguredRejectsAll(t *testing.T) {
	ts := newTestServer(t, "")
	resp, body := doReq(t, http.MethodGet, ts.URL+"/v1/sync/pull?workspace_id=ws-1", "", "dev-1", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未配置 CLOUD_SERVER_TOKEN 应拒绝一切请求：status=%d（body=%s）", resp.StatusCode, body)
	}
	if _, msg := errorEnvelope(t, body); msg == "" {
		t.Fatal("错误消息不应为空")
	}
}

func TestAuthBadTokenRejected(t *testing.T) {
	ts := newTestServer(t, testToken)
	// 无 Authorization 头
	resp, _ := doReq(t, http.MethodGet, ts.URL+"/v1/sync/pull", "", "dev-1", nil)
	mustStatus(t, resp, http.StatusUnauthorized, nil)
	// 错误 token
	resp, body := doReq(t, http.MethodGet, ts.URL+"/v1/sync/pull", "wrong-token", "dev-1", nil)
	mustStatus(t, resp, http.StatusUnauthorized, nil)
	code, _ := errorEnvelope(t, body)
	if code != "UNAUTHORIZED" {
		t.Fatalf("错误码 = %s，期望 UNAUTHORIZED", code)
	}
}

func TestAuthMissingDeviceIDRejected(t *testing.T) {
	ts := newTestServer(t, testToken)
	resp, body := doReq(t, http.MethodGet, ts.URL+"/v1/sync/pull", testToken, "", nil)
	mustStatus(t, resp, http.StatusBadRequest, body)
}

func TestAuthValidTokenAndHeaders(t *testing.T) {
	ts := newTestServer(t, testToken)
	resp, body := doReq(t, http.MethodPost, ts.URL+"/v1/devices", testToken, "dev-1",
		map[string]any{"device_id": "dev-1", "device_name": "Windows", "platform": "windows", "app_version": "2.0.0"})
	mustStatus(t, resp, http.StatusOK, body)
	if resp.Header.Get("X-Request-ID") == "" {
		t.Fatal("缺少 X-Request-ID 响应头")
	}
	if resp.Header.Get("Date") == "" {
		t.Fatal("缺少 Date 响应头")
	}
}

// ---- 4.2 设备注册 ----

func TestDeviceRegister(t *testing.T) {
	ts := newTestServer(t, testToken)
	body := map[string]any{
		"device_id": "device-1", "device_name": "Windows Desktop", "platform": "windows", "app_version": "2.0.0",
	}
	resp, raw := doReq(t, http.MethodPost, ts.URL+"/v1/devices", testToken, "device-1", body)
	mustStatus(t, resp, http.StatusOK, raw)
	var out struct {
		DeviceID   string `json:"device_id"`
		Status     string `json:"status"`
		ServerTime string `json:"server_time"`
		Workspace  string `json:"workspace"`
		Cursor     int64  `json:"cursor"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.DeviceID != "device-1" || out.Status != "registered" {
		t.Fatalf("注册响应异常: %+v", out)
	}
	if out.ServerTime == "" {
		t.Fatal("缺少 server_time")
	}

	// 重复注册 → already_registered（非错误）
	resp, raw = doReq(t, http.MethodPost, ts.URL+"/v1/devices", testToken, "device-1", body)
	mustStatus(t, resp, http.StatusOK, raw)
	_ = json.Unmarshal(raw, &out)
	if out.Status != "already_registered" {
		t.Fatalf("重复注册状态 = %s，期望 already_registered", out.Status)
	}
}

func TestDeviceRegisterMissingDeviceID(t *testing.T) {
	ts := newTestServer(t, testToken)
	resp, raw := doReq(t, http.MethodPost, ts.URL+"/v1/devices", testToken, "dev-1",
		map[string]any{"device_name": "Windows", "platform": "windows", "app_version": "2.0.0"})
	mustStatus(t, resp, http.StatusBadRequest, raw)
}

// ---- 4.3 推送变更（逐项 accepted/duplicate/conflict/rejected）----

func TestPushPerItemResults(t *testing.T) {
	ts := newTestServer(t, testToken)
	wsID := "ws-push-1"

	// 1) accepted：card-1 base=0 首次写入 → server_sequence=1, server_version=1
	resp, raw := doReq(t, http.MethodPost, ts.URL+"/v1/sync/push", testToken, "dev-1",
		pushReq(wsID, op("op-a", "review_card", "card-1", 0, "update", map[string]any{"rating": "good"})))
	mustStatus(t, resp, http.StatusOK, raw)
	if !jsonContains(raw, `"accepted"`) || !jsonContains(raw, `"server_sequence":1`) {
		t.Fatalf("首次推送应 accepted + server_sequence=1: %s", raw)
	}

	// 2) duplicate：operation_id 已存在
	resp, raw = doReq(t, http.MethodPost, ts.URL+"/v1/sync/push", testToken, "dev-1",
		pushReq(wsID, op("op-a", "review_card", "card-1", 0, "update", map[string]any{"rating": "good"})))
	mustStatus(t, resp, http.StatusOK, raw)
	if !jsonContains(raw, `"duplicate"`) {
		t.Fatalf("期望 duplicate: %s", raw)
	}

	// 3) accepted：card-2 base=0 → server_sequence=2
	resp, raw = doReq(t, http.MethodPost, ts.URL+"/v1/sync/push", testToken, "dev-1",
		pushReq(wsID, op("op-b", "review_card", "card-2", 0, "update", map[string]any{"rating": "great"})))
	mustStatus(t, resp, http.StatusOK, raw)
	if !jsonContains(raw, `"server_sequence":2`) {
		t.Fatalf("第二操作应 server_sequence=2: %s", raw)
	}

	// 4) conflict：card-1 当前版本为 1，客户端 base=0 落后 → conflict + 冲突副本
	resp, raw = doReq(t, http.MethodPost, ts.URL+"/v1/sync/push", testToken, "dev-1",
		pushReq(wsID, op("op-c", "review_card", "card-1", 0, "update", map[string]any{"rating": "old"})))
	mustStatus(t, resp, http.StatusOK, raw)
	var cc struct {
		Items []struct {
			OperationID  string          `json:"operation_id"`
			Result       string          `json:"result"`
			ConflictCopy json.RawMessage `json:"conflict_copy"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &cc); err != nil {
		t.Fatal(err)
	}
	if len(cc.Items) != 1 || cc.Items[0].Result != "conflict" {
		t.Fatalf("期望 conflict: %s", raw)
	}
	var copy struct {
		ConflictOf    string          `json:"conflict_of"`
		ServerVersion int             `json:"server_version"`
		LocalPayload  json.RawMessage `json:"local_payload"`
	}
	if err := json.Unmarshal(cc.Items[0].ConflictCopy, &copy); err != nil {
		t.Fatalf("冲突副本解析失败: %v", err)
	}
	if copy.ConflictOf != "op-c" || copy.ServerVersion != 1 {
		t.Fatalf("冲突副本结构异常: %+v", copy)
	}
	if copy.LocalPayload == nil {
		t.Fatal("冲突副本缺少 local_payload")
	}

	// 5) rejected：非法的 operation 值
	resp, raw = doReq(t, http.MethodPost, ts.URL+"/v1/sync/push", testToken, "dev-1",
		pushReq(wsID, op("op-d", "review_card", "card-9", 0, "rename", map[string]any{"x": 1})))
	mustStatus(t, resp, http.StatusOK, raw)
	var rej struct {
		Items []struct {
			OperationID string `json:"operation_id"`
			Result      string `json:"result"`
		} `json:"items"`
	}
	_ = json.Unmarshal(raw, &rej)
	if len(rej.Items) != 1 || rej.Items[0].Result != "rejected" {
		t.Fatalf("期望 rejected: %s", raw)
	}

	// 任一失败不回滚已接受操作：已接受的 op-a/op-b 仍可拉取
	resp, raw = doReq(t, http.MethodGet, ts.URL+"/v1/sync/pull?workspace_id="+wsID+"&cursor=0", testToken, "dev-1", nil)
	mustStatus(t, resp, http.StatusOK, raw)
	var pull struct {
		Operations []struct {
			OperationID string `json:"operation_id"`
			EntityID    string `json:"entity_id"`
		} `json:"operations"`
		ServerTime string `json:"server_time"`
	}
	_ = json.Unmarshal(raw, &pull)
	if len(pull.Operations) != 2 {
		t.Fatalf("已接受操作数 = %d，期望 2（op-a+op-b）: %s", len(pull.Operations), raw)
	}
	if pull.ServerTime == "" {
		t.Fatal("缺少 server_time")
	}
}

// ---- 4.3 设备撤销传播（供 Todo 40 使用）----

func TestPushDeviceRevokedAccepted(t *testing.T) {
	ts := newTestServer(t, testToken)
	// 先注册设备
	resp, raw := doReq(t, http.MethodPost, ts.URL+"/v1/devices", testToken, "device-x",
		map[string]any{"device_id": "device-x", "device_name": "Desktop", "platform": "windows", "app_version": "2.0.0"})
	mustStatus(t, resp, http.StatusOK, raw)
	// device:revoked 操作（entity_type=device, operation=update, payload status=revoked）应被接受
	resp, raw = doReq(t, http.MethodPost, ts.URL+"/v1/sync/push", testToken, "device-x",
		pushReq("ws-revoke", op("op-revoke-1", "device", "device-x", 0, "update", map[string]any{"status": "revoked"})))
	mustStatus(t, resp, http.StatusOK, raw)
	var out struct {
		Items []struct {
			Result string `json:"result"`
		} `json:"items"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.Items) != 1 || out.Items[0].Result != "accepted" {
		t.Fatalf("device:revoked 操作应被接受: %s", raw)
	}
}

// ---- 4.4 拉取变更（游标分页）----

func TestPullCursorPagination(t *testing.T) {
	ts := newTestServer(t, testToken)
	wsID := "ws-pull-1"
	var ops []pushOp
	for i := 0; i < 5; i++ {
		ops = append(ops, op(fmt.Sprintf("op-%d", i), "review_card", fmt.Sprintf("card-%d", i), 0, "update", map[string]any{"i": i}))
	}
	resp, raw := doReq(t, http.MethodPost, ts.URL+"/v1/sync/push", testToken, "dev-1", pushReq(wsID, ops...))
	mustStatus(t, resp, http.StatusOK, raw)

	pull := func(cursor int64, limit int) (next int64, n int, hasMore bool) {
		u := fmt.Sprintf("%s/v1/sync/pull?workspace_id=%s&cursor=%d", ts.URL, wsID, cursor)
		if limit > 0 {
			u += fmt.Sprintf("&limit=%d", limit)
		}
		r, b := doReq(t, http.MethodGet, u, testToken, "dev-1", nil)
		mustStatus(t, r, http.StatusOK, b)
		var p struct {
			Operations []struct {
				OperationID string `json:"operation_id"`
			} `json:"operations"`
			NextCursor int64  `json:"next_cursor"`
			HasMore    bool   `json:"has_more"`
			ServerTime string `json:"server_time"`
		}
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatal(err)
		}
		if p.ServerTime == "" {
			t.Fatal("缺少 server_time")
		}
		return p.NextCursor, len(p.Operations), p.HasMore
	}

	// 第 1 页：limit=2 → 2 条 + has_more + next=2
	next, n, hasMore := pull(0, 2)
	if n != 2 || !hasMore || next != 2 {
		t.Fatalf("第一页异常：n=%d hasMore=%v next=%d", n, hasMore, next)
	}
	// 第 2 页
	next, n, hasMore = pull(next, 2)
	if n != 2 || !hasMore || next != 4 {
		t.Fatalf("第二页异常：n=%d hasMore=%v next=%d", n, hasMore, next)
	}
	// 第 3 页（余 1 条）
	next, n, hasMore = pull(next, 2)
	if n != 1 || hasMore || next != 5 {
		t.Fatalf("第三页异常：n=%d hasMore=%v next=%d", n, hasMore, next)
	}
	// 末页之后无更多
	_, n, hasMore = pull(next, 2)
	if n != 0 || hasMore {
		t.Fatalf("末页之后异常：n=%d hasMore=%v", n, hasMore)
	}
}

func TestPullLimitDefaultAndCap(t *testing.T) {
	ts := newTestServer(t, testToken)
	wsID := "ws-pull-cap"
	var ops []pushOp
	for i := 0; i < 250; i++ {
		ops = append(ops, op(fmt.Sprintf("op-cap-%d", i), "review_card", fmt.Sprintf("card-%d", i), 0, "update", map[string]any{"i": i}))
	}
	resp, raw := doReq(t, http.MethodPost, ts.URL+"/v1/sync/push", testToken, "dev-1", pushReq(wsID, ops...))
	mustStatus(t, resp, http.StatusOK, raw)

	// 缺省 limit → 200 上限
	r, b := doReq(t, http.MethodGet, ts.URL+"/v1/sync/pull?workspace_id="+wsID+"&cursor=0", testToken, "dev-1", nil)
	mustStatus(t, r, http.StatusOK, b)
	var p struct {
		Operations []json.RawMessage `json:"operations"`
		HasMore    bool              `json:"has_more"`
		NextCursor int64             `json:"next_cursor"`
	}
	_ = json.Unmarshal(b, &p)
	if len(p.Operations) != 200 || !p.HasMore || p.NextCursor != 200 {
		t.Fatalf("缺省 limit 应截断到 200：n=%d hasMore=%v next=%d", len(p.Operations), p.HasMore, p.NextCursor)
	}

	// 显式超上限 → 仍 200
	r, b = doReq(t, http.MethodGet, ts.URL+"/v1/sync/pull?workspace_id="+wsID+"&cursor=0&limit=500", testToken, "dev-1", nil)
	mustStatus(t, r, http.StatusOK, b)
	_ = json.Unmarshal(b, &p)
	if len(p.Operations) != 200 {
		t.Fatalf("limit=500 应截断到 200：n=%d", len(p.Operations))
	}
}

// ---- 4.5 备份与延迟删除 ----

func TestBackupStored(t *testing.T) {
	ts := newTestServer(t, testToken)
	wsID := "ws-backup"
	resp, raw := doReq(t, http.MethodPost, ts.URL+"/v1/backups", testToken, "dev-1",
		map[string]any{
			"workspace_id": wsID, "name": "backup-2026", "size_bytes": 2048,
			"sha256": "e3b0c44298fc1c149afbf4c8996fb924", "meta": map[string]any{"encrypted": true},
		})
	mustStatus(t, resp, http.StatusOK, raw)
	var out struct {
		BackupID    string `json:"backup_id"`
		WorkspaceID string `json:"workspace_id"`
		CreatedAt   string `json:"created_at"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.BackupID == "" || out.WorkspaceID != wsID || out.Status != "stored" || out.CreatedAt == "" {
		t.Fatalf("备份响应异常: %s", raw)
	}
}

func TestSoftDeleteRejectsWrites(t *testing.T) {
	ts := newTestServer(t, testToken)
	wsID := "ws-delete"
	// 建立工作区（推送一条）
	resp, raw := doReq(t, http.MethodPost, ts.URL+"/v1/sync/push", testToken, "dev-1",
		pushReq(wsID, op("op-1", "review_card", "card-1", 0, "create", map[string]any{"front": "q"})))
	mustStatus(t, resp, http.StatusOK, raw)

	// 延迟删除 → 返回可撤销截止时间
	resp, raw = doReq(t, http.MethodDelete, ts.URL+"/v1/workspaces/"+wsID, testToken, "dev-1", nil)
	mustStatus(t, resp, http.StatusOK, raw)
	var del struct {
		WorkspaceID  string `json:"workspace_id"`
		DeletedAt    string `json:"deleted_at"`
		UndoDeadline string `json:"undo_deadline"`
	}
	if err := json.Unmarshal(raw, &del); err != nil {
		t.Fatal(err)
	}
	if del.WorkspaceID != wsID || del.DeletedAt == "" || del.UndoDeadline == "" {
		t.Fatalf("删除响应异常: %s", raw)
	}

	// 删除期内 push → 409
	resp, raw = doReq(t, http.MethodPost, ts.URL+"/v1/sync/push", testToken, "dev-1",
		pushReq(wsID, op("op-2", "review_card", "card-2", 0, "create", map[string]any{"front": "w"})))
	mustStatus(t, resp, http.StatusConflict, raw)
	code, _ := errorEnvelope(t, raw)
	if code != "CONFLICT" {
		t.Fatalf("删除期错误码 = %s，期望 CONFLICT", code)
	}

	// 删除期内 backups → 409
	resp, raw = doReq(t, http.MethodPost, ts.URL+"/v1/backups", testToken, "dev-1",
		map[string]any{"workspace_id": wsID, "name": "b1", "size_bytes": 100, "sha256": "abc"})
	mustStatus(t, resp, http.StatusConflict, raw)

	// 读取不受影响（pull 仍可用）
	resp, raw = doReq(t, http.MethodGet, ts.URL+"/v1/sync/pull?workspace_id="+wsID+"&cursor=0", testToken, "dev-1", nil)
	mustStatus(t, resp, http.StatusOK, raw)

	// 重复删除幂等
	resp, raw = doReq(t, http.MethodDelete, ts.URL+"/v1/workspaces/"+wsID, testToken, "dev-1", nil)
	mustStatus(t, resp, http.StatusOK, raw)
}
