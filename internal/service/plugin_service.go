package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"lumo/internal/domain"
	"lumo/internal/plugin"
	"lumo/internal/repository"
	"lumo/internal/sandbox"
)

// PluginService 插件用例（API 设计文档 7.13 / 完整设计文档 4.24）。
//
// 插件是全局资源（plugins 表无 workspace_id），所有方法不接收 workspace_id；
// 因此不写审计日志（audit_events.workspace_id 为必填 FK）。
type PluginService struct {
	s   *Services
	sbx sandbox.Runner // 可注入沙箱执行器（测试替换为桩；nil 用默认子进程沙箱）
}

// sandbox 返回沙箱执行器（默认真实子进程沙箱）。
func (p *PluginService) sandbox() sandbox.Runner {
	if p.sbx != nil {
		return p.sbx
	}
	return sandbox.DefaultRunner
}

// Plugin 是插件 DTO（permissions = 用户已确认的权限；manifest = 完整清单）。
type Plugin struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Version     string                `json:"version"`
	Manifest    plugin.PluginManifest `json:"manifest"`
	Enabled     bool                  `json:"enabled"`
	Permissions []string              `json:"permissions"`
	InstalledAt string                `json:"installed_at"`
	UpdatedAt   string                `json:"updated_at"`
}

// PluginInstallReq 安装请求（API 文档 7.13：path 或 url + signature）。
// path 为插件包路径：目录（读取 <dir>/manifest.json）或单个 .json 清单文件。
type PluginInstallReq struct {
	Path      string `json:"path"`
	Signature string `json:"signature"`
}

// PluginSetEnabledReq 启用/禁用请求。
type PluginSetEnabledReq struct {
	PluginID string `json:"plugin_id"`
	Enabled  bool   `json:"enabled"`
}

// PluginUninstallReq 卸载请求。
type PluginUninstallReq struct {
	PluginID string `json:"plugin_id"`
}

// PluginConfirmPermissionsReq 权限确认请求（前端弹窗同意后落库 permissions_json）。
type PluginConfirmPermissionsReq struct {
	PluginID    string   `json:"plugin_id"`
	Permissions []string `json:"permissions"`
}

// PluginInvokeReq 运行插件请求（method 缺省 "run"；params 为任意 JSON）。
type PluginInvokeReq struct {
	PluginID string          `json:"plugin_id"`
	Method   string          `json:"method,omitempty"`
	Params   json.RawMessage `json:"params,omitempty"`
}

// PluginInvokeResult 插件运行结果：ok=false 表示插件自身失败（error 为 stderr 诊断），
// 此时不是服务错误；ok=true 时 result 为插件 stdout 解析出的 JSON 值。
type PluginInvokeResult struct {
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// PluginInstall 安装签名合法的插件：读取 manifest → 校验字段 → 验证 Ed25519
// 签名 → 落库（enabled=0，权限待用户确认）。
func (p *PluginService) PluginInstall(ctx context.Context, req PluginInstallReq) (*Plugin, error) {
	if req.Path == "" {
		return nil, domain.InvalidArg("path 必填")
	}
	if req.Signature == "" {
		return nil, domain.InvalidArg("signature 必填")
	}
	raw, err := readPluginManifestBytes(req.Path)
	if err != nil {
		return nil, err
	}
	m, err := plugin.ParseManifest(raw)
	if err != nil {
		return nil, err
	}
	// 签名验证失败 → INVALID_ARGUMENT（严格拒绝，非 PLUGIN_ERROR）。
	if !plugin.VerifySignature(raw, req.Signature) {
		return nil, domain.InvalidArg("插件签名验证失败")
	}
	// 同名插件去重（name 全局唯一语义）。
	if existing, err := p.s.Repo.GetPluginByName(ctx, m.Name); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, domain.Conflict("同名插件已安装（%s %s）", existing.Name, existing.Version)
	}
	row := &repository.PluginRow{
		ID:              NewID(),
		Name:            m.Name,
		Version:         m.Version,
		ManifestJSON:    string(raw), // 保存原始签名字节，逐字不变
		PermissionsJSON: "[]",        // 待用户确认
	}
	if err := p.s.Repo.CreatePlugin(ctx, row); err != nil {
		return nil, err
	}
	return p.pluginByID(ctx, row.ID)
}

// readPluginManifestBytes 读取插件包清单原始字节：目录 → <dir>/manifest.json；
// 文件 → 直接读取。路径不存在 → NOT_FOUND。
func readPluginManifestBytes(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, domain.NotFound("插件包不存在")
		}
		return nil, domain.InvalidArg("无法访问插件包路径: %v", err)
	}
	target := path
	if st.IsDir() {
		target = filepath.Join(path, "manifest.json")
		if _, err := os.Stat(target); err != nil {
			return nil, domain.NotFound("插件目录下缺少 manifest.json")
		}
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return nil, domain.InvalidArg("读取插件 manifest 失败: %v", err)
	}
	return raw, nil
}

// PluginSetEnabled 启用/禁用插件。启用前须已确认权限：manifest 声明了权限而
// permissions_json 为空 → INVALID_STATE（提示客户端先走权限确认弹窗）。
func (p *PluginService) PluginSetEnabled(ctx context.Context, req PluginSetEnabledReq) (*Plugin, error) {
	if req.PluginID == "" {
		return nil, domain.InvalidArg("plugin_id 必填")
	}
	row, err := p.s.Repo.GetPluginByID(ctx, req.PluginID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.NotFound("插件不存在")
	}
	if req.Enabled {
		ok, err := p.permissionsConfirmed(ctx, row)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, domain.InvalidState("插件声明了权限但尚未确认，请先在弹窗中同意权限后再启用")
		}
	}
	if _, err := p.s.Repo.SetPluginEnabled(ctx, row.ID, req.Enabled); err != nil {
		return nil, err
	}
	return p.pluginByID(ctx, row.ID)
}

// permissionsConfirmed 判断插件权限是否已确认：
// manifest 未声明权限 → 视为已确认；声明了权限 → 要求 permissions_json 非空。
func (p *PluginService) permissionsConfirmed(ctx context.Context, row *repository.PluginRow) (bool, error) {
	m, err := plugin.ParseManifest([]byte(row.ManifestJSON))
	if err != nil {
		return false, domain.PluginError("插件 manifest 解析失败: %v", err)
	}
	if len(m.Permissions) == 0 {
		return true, nil
	}
	var confirmed []string
	if err := json.Unmarshal([]byte(row.PermissionsJSON), &confirmed); err != nil {
		return false, domain.PluginError("插件权限数据损坏: %v", err)
	}
	return len(confirmed) > 0, nil
}

// PluginConfirmPermissions 确认插件权限：确认列表须为 manifest 已声明权限的
// 子集（含去重）；落库 permissions_json 后返回最新插件。
func (p *PluginService) PluginConfirmPermissions(ctx context.Context, req PluginConfirmPermissionsReq) (*Plugin, error) {
	if req.PluginID == "" {
		return nil, domain.InvalidArg("plugin_id 必填")
	}
	row, err := p.s.Repo.GetPluginByID(ctx, req.PluginID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.NotFound("插件不存在")
	}
	m, err := plugin.ParseManifest([]byte(row.ManifestJSON))
	if err != nil {
		return nil, domain.PluginError("插件 manifest 解析失败: %v", err)
	}
	declared := map[string]bool{}
	for _, perm := range m.Permissions {
		declared[perm] = true
	}
	seen := map[string]bool{}
	var confirmed []string
	for _, perm := range req.Permissions {
		if !declared[perm] {
			return nil, domain.InvalidArg("插件未声明该权限：%s", perm)
		}
		if seen[perm] {
			continue
		}
		seen[perm] = true
		confirmed = append(confirmed, perm)
	}
	sort.Strings(confirmed)
	permJSON, _ := json.Marshal(confirmed)
	if err := p.s.Repo.SetPluginPermissions(ctx, row.ID, string(permJSON)); err != nil {
		return nil, err
	}
	return p.pluginByID(ctx, row.ID)
}

// PluginUninstall 卸载插件（删除插件行）。
func (p *PluginService) PluginUninstall(ctx context.Context, req PluginUninstallReq) (*DeleteResult, error) {
	if req.PluginID == "" {
		return nil, domain.InvalidArg("plugin_id 必填")
	}
	row, err := p.s.Repo.GetPluginByID(ctx, req.PluginID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.NotFound("插件不存在")
	}
	if _, err := p.s.Repo.DeletePlugin(ctx, req.PluginID); err != nil {
		return nil, err
	}
	return &DeleteResult{Deleted: true, DeletedAt: Now()}, nil
}

// PluginInvoke 在隔离沙箱中运行已启用插件：stdin 传 JSON-RPC 请求，
// 解析 stdout 为结果。非零退出码 → ok=false（error=stderr，非服务错误）；
// 沙箱资源超限 → SANDBOX_LIMIT；协议（stdout 非 JSON）→ PLUGIN_ERROR。
func (p *PluginService) PluginInvoke(ctx context.Context, req PluginInvokeReq) (*PluginInvokeResult, error) {
	if req.PluginID == "" {
		return nil, domain.InvalidArg("plugin_id 必填")
	}
	row, err := p.s.Repo.GetPluginByID(ctx, req.PluginID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.NotFound("插件不存在")
	}
	if !row.Enabled {
		return nil, domain.InvalidState("插件未启用")
	}
	m, err := plugin.ParseManifest([]byte(row.ManifestJSON))
	if err != nil {
		return nil, domain.PluginError("插件 manifest 解析失败: %v", err)
	}
	res, err := plugin.Execute(ctx, p.sandbox(), m.Entrypoint, req.Params)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		stderr := res.Stderr
		if stderr == "" {
			stderr = fmt.Sprintf("退出码 %d", res.ExitCode)
		}
		return &PluginInvokeResult{OK: false, Error: stderr}, nil
	}
	var out any
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return nil, domain.PluginError("插件输出不是合法 JSON: %v", err)
	}
	return &PluginInvokeResult{OK: true, Result: out}, nil
}

// PluginList 列出全部已安装插件（全局，倒序）。
func (p *PluginService) PluginList(ctx context.Context) ([]*Plugin, error) {
	rows, err := p.s.Repo.ListPlugins(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*Plugin, 0, len(rows))
	for _, r := range rows {
		pl, err := pluginFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, pl)
	}
	return out, nil
}

// pluginByID 重取插件 DTO（SQLite 无 INSERT..RETURNING，落库后回读时间戳）。
func (p *PluginService) pluginByID(ctx context.Context, id string) (*Plugin, error) {
	row, err := p.s.Repo.GetPluginByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.NotFound("插件不存在")
	}
	return pluginFromRow(row)
}

// pluginFromRow 行 → DTO（解析 manifest 与已确认权限）。
func pluginFromRow(row *repository.PluginRow) (*Plugin, error) {
	m, err := plugin.ParseManifest([]byte(row.ManifestJSON))
	if err != nil {
		return nil, domain.PluginError("插件 manifest 解析失败: %v", err)
	}
	var perms []string
	if err := json.Unmarshal([]byte(row.PermissionsJSON), &perms); err != nil {
		return nil, domain.PluginError("插件权限数据损坏: %v", err)
	}
	return &Plugin{
		ID: row.ID, Name: row.Name, Version: row.Version, Manifest: *m,
		Enabled: row.Enabled, Permissions: perms,
		InstalledAt: row.InstalledAt, UpdatedAt: row.UpdatedAt,
	}, nil
}
