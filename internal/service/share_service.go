// share_service.go 创作分享用例（完整设计文档 4.20 / API 设计文档 7.13）。
// ShareCreate（强制安全扫描 + 缓存）、ShareRevoke（立即失效 + 审计）、ShareResolve（token 消费 + 受限文件下载通道）。
// 不新增迁移、不新增错误码（SHARE_EXPIRED 复用既有 CodeShareExpired）。
package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// ShareService 实现创作分享用例。
type ShareService struct{ s *Services }

// 分享引用类型白名单（shares.ref_type CHECK 一致）。
var validShareRefTypes = map[string]bool{
	"question":       true,
	"paper":          true,
	"flashcard_pack": true,
	"note":           true,
}

// shareTTLDefault 分享链接默认有效期（4.20 P3：默认 7 天）。
const shareTTLDefault = 7

// shareScanOutcomeClean 分享行 scan_result 的"干净"标记。
const shareScanOutcomeClean = "clean"

// Share 是分享链接 DTO。
type Share struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	UserID      string  `json:"user_id"`
	RefType     string  `json:"ref_type"`
	RefID       string  `json:"ref_id"`
	Token       string  `json:"token"`
	ExpiresAt   *string `json:"expires_at"`
	RevokedAt   *string `json:"revoked_at"`
	ScanResult  *string `json:"scan_result"`
	CreatedAt   string  `json:"created_at"`
}

// ShareCreateReq 创建分享链接请求。
type ShareCreateReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	RefType        string `json:"ref_type"`
	RefID          string `json:"ref_id"`
	TTLDays        *int   `json:"ttl_days"` // nil=默认 7 天；0/-1=永久；合法值 {1,7,30}
	IdempotencyKey string `json:"idempotency_key"`
}

// ShareRevokeReq 撤销分享请求。
type ShareRevokeReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	ShareID     string `json:"share_id"`
}

// ShareResolveReq 消费分享链接请求（公开入口，仅 token）。
type ShareResolveReq struct {
	Token string `json:"token"`
}

// ShareResolveResult 消费分享链接结果：share + 受限通道相对下载路径。
type ShareResolveResult struct {
	Share        *Share `json:"share"`
	DownloadPath string `json:"download_path"`
}

// ShareCreate 创建分享链接：校验引用实体 → 强制安全扫描（结果缓存）→ 生成 token/有效期。
// 安全扫描未通过时拒绝发布（不创建 shares 行、不生成 token）；幂等键重放返回同一分享。
func (s *ShareService) ShareCreate(ctx context.Context, req ShareCreateReq) (*Share, error) {
	if err := s.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if !validShareRefTypes[req.RefType] {
		return nil, domain.InvalidArg("ref_type 仅支持 question/paper/flashcard_pack/note")
	}
	if !domain.ValidID(req.RefID) {
		return nil, domain.InvalidArg("ref_id 无效")
	}
	if err := s.s.assertUserActive(ctx, req.UserID); err != nil {
		return nil, err
	}
	return withIdempotency(s.s, ctx, req.WorkspaceID, req.IdempotencyKey, "ShareCreate",
		func() (*Share, error) { return s.doCreateShare(ctx, req) })
}

// doCreateShare 创建分享的业务主体（幂等 fn 内执行）。
func (s *ShareService) doCreateShare(ctx context.Context, req ShareCreateReq) (*Share, error) {
	// 校验引用实体存在且属于调用方工作区，并序列化内容（待扫描）。
	content, err := s.serializeRefContent(ctx, req.WorkspaceID, req.RefType, req.RefID)
	if err != nil {
		return nil, err
	}
	// 强制安全扫描（结果按 (ref_type, ref_id, content_hash) 缓存复用）。
	scanRes, err := s.scanAndCache(ctx, req.RefType, req.RefID, content)
	if err != nil {
		return nil, err
	}
	if !scanRes.Clean {
		return nil, domain.InvalidArg("内容未通过安全扫描，禁止分享：%s", strings.Join(scanRes.Findings, ", "))
	}

	// ttl 语义（4.20 P3）：nil=默认 7 天；0/-1=永久；1/7/30 显式天数；其余 INVALID_ARGUMENT。
	ttlDays := shareTTLDefault
	if req.TTLDays != nil {
		ttlDays = *req.TTLDays
	}
	var expiresAt *string
	switch ttlDays {
	case 0, -1:
		expiresAt = nil // 永久
	case 1, 7, 30:
		t := Now()
		if parsed, err := time.Parse(time.RFC3339, t); err == nil {
			exp := parsed.AddDate(0, 0, ttlDays).Format(time.RFC3339)
			expiresAt = &exp
		}
	default:
		return nil, domain.InvalidArg("ttl_days 仅支持 1/7/30 天或永久（0 或 -1）")
	}

	token, err := newShareToken()
	if err != nil {
		return nil, domain.NewError(domain.CodeInternal, fmt.Sprintf("生成分享令牌失败: %v", err))
	}
	outcome := shareScanOutcomeClean
	row := &repository.ShareRow{
		ID: NewID(), WorkspaceID: req.WorkspaceID, UserID: req.UserID,
		RefType: req.RefType, RefID: req.RefID, Token: token,
		ExpiresAt: expiresAt, ScanResult: &outcome,
	}
	if err := s.s.Repo.CreateShare(ctx, row); err != nil {
		return nil, err
	}
	created, err := s.s.Repo.GetShareByID(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	if created == nil {
		return nil, domain.Conflict("分享创建失败，请重试")
	}
	s.s.audit(ctx, req.WorkspaceID, "share.create", "share", created.ID,
		map[string]any{"ref_type": created.RefType, "ref_id": created.RefID,
			"ttl_days": ttlDays, "expires_at": created.ExpiresAt})
	return shareFromRow(created), nil
}

// ShareRevoke 撤销分享：设置 revoked_at（立即失效）。仅分享属主可撤销，否则 FORBIDDEN。
func (s *ShareService) ShareRevoke(ctx context.Context, req ShareRevokeReq) (*DeleteResult, error) {
	if err := s.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.ShareID == "" {
		return nil, domain.InvalidArg("share_id 必填")
	}
	if err := s.s.assertUserActive(ctx, req.UserID); err != nil {
		return nil, err
	}
	sh, err := s.s.Repo.GetShareByID(ctx, req.ShareID)
	if err != nil {
		return nil, err
	}
	if sh == nil {
		return nil, domain.NotFound("分享不存在")
	}
	// 工作区隔离 + 属主校验（跨工作区不泄露存在性 → NOT_FOUND；同工作区非属主 → FORBIDDEN）。
	if sh.WorkspaceID != req.WorkspaceID {
		return nil, domain.NotFound("分享不存在")
	}
	if sh.UserID != req.UserID {
		s.s.audit(ctx, req.WorkspaceID, "share.revoke", "share", sh.ID,
			map[string]any{"forbidden": true, "user_id": req.UserID})
		return nil, domain.Forbidden("无权撤销该分享")
	}
	if sh.RevokedAt != nil {
		return nil, domain.InvalidState("该分享已撤销")
	}
	revokedAt := Now()
	ok, err := s.s.Repo.RevokeShare(ctx, sh.ID, revokedAt)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.Conflict("分享状态已变化，请刷新后重试")
	}
	s.s.audit(ctx, req.WorkspaceID, "share.revoke", "share", sh.ID,
		map[string]any{"ref_type": sh.RefType, "ref_id": sh.RefID})
	return &DeleteResult{Deleted: true, DeletedAt: revokedAt}, nil
}

// ShareResolve 消费分享链接（公开入口，仅凭 token）：
// 已撤销或已过期 → SHARE_EXPIRED；有效则把序列化内容写入 exports/ 受限目录并返回下载路径。
// 导出文件按需（lazy）生成，ShareCreate 不要求文件预存在。
func (s *ShareService) ShareResolve(ctx context.Context, req ShareResolveReq) (*ShareResolveResult, error) {
	if strings.TrimSpace(req.Token) == "" {
		return nil, domain.InvalidArg("token 必填")
	}
	sh, err := s.s.Repo.GetShareByToken(ctx, strings.TrimSpace(req.Token))
	if err != nil {
		return nil, err
	}
	if sh == nil {
		return nil, domain.NotFound("分享链接不存在")
	}
	if sh.RevokedAt != nil {
		return nil, domain.ShareExpired("分享链接已撤销")
	}
	if expired := shareIsExpired(sh.ExpiresAt); expired {
		return nil, domain.ShareExpired("分享链接已过期")
	}
	content, err := s.serializeRefContent(ctx, sh.WorkspaceID, sh.RefType, sh.RefID)
	if err != nil {
		return nil, err
	}
	fileName := "share-" + sh.Token + ".json"
	relPath := filepath.Join("exports", fileName)
	fullPath := filepath.Join(s.s.Cfg.DataDir, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		return nil, domain.NewError(domain.CodeInternal, fmt.Sprintf("创建导出目录失败: %v", err))
	}
	if err := os.WriteFile(fullPath, content, 0o600); err != nil {
		return nil, domain.NewError(domain.CodeInternal, fmt.Sprintf("生成分享导出文件失败: %v", err))
	}
	return &ShareResolveResult{Share: shareFromRow(sh), DownloadPath: relPath}, nil
}

// shareIsExpired 判断分享是否过期（expires_at NULL=永久不过期；解析失败视为已过期）。
func shareIsExpired(expiresAt *string) bool {
	if expiresAt == nil {
		return false
	}
	t, err := time.Parse(time.RFC3339, *expiresAt)
	if err != nil {
		return true
	}
	return time.Now().UTC().After(t)
}

// newShareToken 生成 32 位十六进制分享令牌（crypto/rand，128 位熵）。
func newShareToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func shareFromRow(r *repository.ShareRow) *Share {
	return &Share{
		ID: r.ID, WorkspaceID: r.WorkspaceID, UserID: r.UserID,
		RefType: r.RefType, RefID: r.RefID, Token: r.Token,
		ExpiresAt: r.ExpiresAt, RevokedAt: r.RevokedAt, ScanResult: r.ScanResult,
		CreatedAt: r.CreatedAt,
	}
}

// serializeRefContent 校验引用实体存在且属于指定工作区，并将其序列化为 JSON 字节（分享内容/扫描对象）。
// 实体不存在（或已删除）→ NOT_FOUND/INVALID_STATE，不生成分享。
// flashcard_pack 语义：0005 无独立 pack 表（DDL 冻结），ref_id 按 (flashcards.id 或 flashcards.source_ref)
// 解析为"闪卡集合"——单卡 ID 或生成批次标识（如某笔记/文档转出的整批卡），不存在则视为无效引用。
func (s *ShareService) serializeRefContent(ctx context.Context, wsID, refType, refID string) ([]byte, error) {
	switch refType {
	case "question":
		q, err := s.s.Repo.GetQuestion(ctx, wsID, refID)
		if err != nil {
			return nil, err
		}
		if q == nil {
			return nil, domain.NotFound("题目不存在")
		}
		payload := json.RawMessage("{}")
		if q.CurrentVersionID != nil {
			if v, err := s.s.Repo.GetQuestionVersion(ctx, *q.CurrentVersionID); err == nil && v != nil {
				payload = v.Payload
			}
		}
		return marshalShareContent(map[string]any{
			"id": q.ID, "type": q.Type, "status": q.Status,
			"tags": json.RawMessage(q.Tags), "payload": payload,
		})
	case "paper":
		p, err := s.s.Repo.GetExamPaper(ctx, wsID, refID)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, domain.NotFound("试卷不存在")
		}
		sections, err := s.s.Repo.GetExamPaperSections(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		type sec struct {
			ID                 string   `json:"id"`
			Title              string   `json:"title"`
			OrderNo            int      `json:"order_no"`
			QuestionVersionIDs []string `json:"question_version_ids"`
			Score              int      `json:"score"`
		}
		secs := make([]sec, 0, len(sections))
		for _, x := range sections {
			var ids []string
			_ = json.Unmarshal([]byte(x.QuestionVersionIDs), &ids)
			secs = append(secs, sec{ID: x.ID, Title: x.Title, OrderNo: x.OrderNo,
				QuestionVersionIDs: ids, Score: x.Score})
		}
		return marshalShareContent(map[string]any{
			"id": p.ID, "title": p.Title, "status": p.Status,
			"config_json": json.RawMessage(p.ConfigJSON), "sections": secs,
		})
	case "flashcard_pack":
		cards, err := s.s.Repo.ListFlashcardsByPack(ctx, wsID, refID)
		if err != nil {
			return nil, err
		}
		if len(cards) == 0 {
			return nil, domain.NotFound("闪卡包不存在")
		}
		type card struct {
			ID       string `json:"id"`
			Front    string `json:"front"`
			Back     string `json:"back"`
			CardType string `json:"card_type"`
			State    string `json:"state"`
		}
		out := make([]card, 0, len(cards))
		for _, c := range cards {
			out = append(out, card{ID: c.ID, Front: c.Front, Back: c.Back,
				CardType: c.CardType, State: c.State})
		}
		return marshalShareContent(map[string]any{"pack_id": refID, "cards": out})
	case "note":
		n, err := s.s.Repo.GetNote(ctx, wsID, refID)
		if err != nil {
			return nil, err
		}
		if n == nil {
			return nil, domain.NotFound("笔记不存在")
		}
		if n.DeletedAt != nil {
			return nil, domain.InvalidState("该笔记已删除，无法分享")
		}
		return marshalShareContent(map[string]any{
			"id": n.ID, "kind": n.Kind, "title": n.Title, "body_md": n.BodyMD,
			"tags": json.RawMessage(n.Tags), "knowledge_ids": json.RawMessage(n.KnowledgeIDs),
		})
	}
	return nil, domain.InvalidArg("ref_type 不支持: %s", refType)
}

// scanAndCache 计算内容哈希（sha256）→ 查 content_scan_results 缓存 → 未命中则扫描并写入缓存。
// 同一引用 + 同一内容只扫描一次；后续分享复用缓存结论。
func (s *ShareService) scanAndCache(ctx context.Context, refType, refID string, content []byte) (*ScanResult, error) {
	hash := shareContentHash(content)
	cached, err := s.s.Repo.GetScanResult(ctx, refType, refID, hash)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		var res ScanResult
		if json.Unmarshal([]byte(cached.ResultJSON), &res) == nil {
			return &res, nil
		}
	}
	res := scanContent(content)
	if err := s.s.Repo.UpsertScanResult(ctx, &repository.ScanResultRow{
		ID: NewID(), RefType: refType, RefID: refID,
		ContentHash: hash, ResultJSON: repository.MarshalJSON(res),
	}); err != nil {
		return nil, err
	}
	return &res, nil
}

// shareContentHash 返回内容的 sha256 十六进制哈希。
func shareContentHash(content []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(content))
}

// marshalShareContent 序列化分享内容（JSON，不做 HTML 转义）。
// 默认 json.Marshal 会把 < > & 转成 \u003c 等，导致安全扫描看不到真实内容；
// 分享内容按 JSON 解析后原样呈现，扫描对象就应是未转义文本（与接收方所见一致）。
func marshalShareContent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
