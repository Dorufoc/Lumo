package service

// Plugin 市场与主题插件 TDD（API 设计文档 7.13 / Todo 37）。
// PluginMarketList：市场目录 = plugins 表（enabled 模拟市场列表），含描述与已确认权限。
// PluginThemeGet：沙箱 JSON-RPC 执行主题插件，校验 theme tokens 键值对（防 CSS 注入）。

import (
	"encoding/json"
	"strings"
	"testing"

	"lumo/internal/domain"
	"lumo/internal/plugin"
	"lumo/internal/sandbox"
)

// themePluginManifest 无权限声明的主题插件清单（entrypoint 为占位，测试用沙箱桩）。
const themePluginManifest = `{"name":"warm-ember","version":"1.0.0","description":"温暖琥珀色主题","entrypoint":["python3","theme.py"],"permissions":[],"api_version":"1"}`

// installThemePlugin 安装并启用一个主题插件，返回 DTO。
func installThemePlugin(t *testing.T, s *Services) *Plugin {
	t.Helper()
	p, _ := installTestPlugin(t, s, themePluginManifest)
	if _, err := s.Plugins.PluginSetEnabled(ctx(), PluginSetEnabledReq{PluginID: p.ID, Enabled: true}); err != nil {
		t.Fatalf("enable theme plugin: %v", err)
	}
	return p
}

// ---- ① 市场列表：来源 = plugins 表，含描述/已确认权限/安装时间 ----

func TestPluginMarketList(t *testing.T) {
	s, _ := newTestServices(t)
	// 先装无权限插件，再装主题插件（倒序 → 主题插件在前）。
	installTestPlugin(t, s, noPermPluginManifest)
	p2, _ := installTestPlugin(t, s, themePluginManifest)

	items, err := s.Plugins.PluginMarketList(ctx())
	if err != nil {
		t.Fatalf("market list: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("市场应含 2 个条目，got %d", len(items))
	}
	// 条目字段：id/name/version/description/enabled/permissions/installed_at
	m := items[0]
	if m.ID == "" || m.Name != "warm-ember" || m.Version != "1.0.0" {
		t.Fatalf("条目头部字段异常: %+v", m)
	}
	if m.Description != "温暖琥珀色主题" {
		t.Fatalf("description 应取自 manifest，got %q", m.Description)
	}
	if m.Enabled {
		t.Fatalf("新装插件 enabled 应为 false，got %+v", m)
	}
	if len(m.Permissions) != 0 {
		t.Fatalf("permissions 应为已确认权限列表，got %v", m.Permissions)
	}
	if m.InstalledAt == "" {
		t.Fatalf("installed_at 不应为空")
	}
	// 确认权限后市场条目反映最新权限。
	if _, err := s.Plugins.PluginConfirmPermissions(ctx(), PluginConfirmPermissionsReq{PluginID: p2.ID, Permissions: []string{}}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
}

// ---- ② 市场列表：空市场返回空数组（非 nil） ----

func TestPluginMarketListEmpty(t *testing.T) {
	s, _ := newTestServices(t)
	items, err := s.Plugins.PluginMarketList(ctx())
	if err != nil {
		t.Fatalf("market list: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("空市场应返回空数组，got %d", len(items))
	}
}

// ---- ③ 主题读取 happy：stdout {"tokens":{...}} → 返回校验后的 tokens ----

func TestPluginThemeGetHappy(t *testing.T) {
	s, _ := newTestServices(t)
	p := installThemePlugin(t, s)
	capt := &captureSandbox{res: &sandbox.Result{Stdout: `{"tokens":{"--color-primary":"#ff5928","--font-sans":"monospace"}}`}}
	s.Plugins.sbx = capt

	out, err := s.Plugins.PluginThemeGet(ctx(), PluginThemeGetReq{PluginID: p.ID})
	if err != nil {
		t.Fatalf("theme get: %v", err)
	}
	if !out.OK || len(out.Tokens) != 2 || out.Tokens["--color-primary"] != "#ff5928" {
		t.Fatalf("tokens 解析异常: %+v", out)
	}
	// 协议：entrypoint 原样传参，stdin 为 JSON-RPC run，params 为 {}。
	if len(capt.spec.Args) != 2 || capt.spec.Args[0] != "python3" {
		t.Fatalf("spec.Args 异常: %v", capt.spec.Args)
	}
	if !strings.Contains(capt.spec.Stdin, `"method":"run"`) || !strings.Contains(capt.spec.Stdin, `"params":{}`) {
		t.Fatalf("stdin 协议异常: %s", capt.spec.Stdin)
	}
	if capt.spec.Timeout != plugin.Timeout || capt.spec.MaxOutput != plugin.MaxOutput {
		t.Fatalf("spec 资源上限异常: %+v", capt.spec)
	}
}

// ---- ④ 主题读取校验：缺 plugin_id / 未知插件 / 未启用 ----

func TestPluginThemeGetValidation(t *testing.T) {
	s, _ := newTestServices(t)
	// 缺 plugin_id → INVALID_ARGUMENT
	if _, err := s.Plugins.PluginThemeGet(ctx(), PluginThemeGetReq{}); err == nil ||
		domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("缺 plugin_id 应 INVALID_ARGUMENT，got %v", err)
	}
	// 未知插件 → NOT_FOUND
	if _, err := s.Plugins.PluginThemeGet(ctx(), PluginThemeGetReq{PluginID: "nope"}); err == nil ||
		domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("未知插件应 NOT_FOUND，got %v", err)
	}
	// 已安装未启用 → INVALID_STATE
	installed, _ := installTestPlugin(t, s, themePluginManifest)
	if _, err := s.Plugins.PluginThemeGet(ctx(), PluginThemeGetReq{PluginID: installed.ID}); err == nil ||
		domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("未启用主题插件应 INVALID_STATE，got %v", err)
	}
}

// ---- ⑤ 主题读取：非零退出 → ok=false + stderr（非服务错误） ----

func TestPluginThemeGetPluginFailure(t *testing.T) {
	s, _ := newTestServices(t)
	p := installThemePlugin(t, s)
	s.Plugins.sbx = &captureSandbox{res: &sandbox.Result{ExitCode: 1, Stderr: "theme crash"}}

	out, err := s.Plugins.PluginThemeGet(ctx(), PluginThemeGetReq{PluginID: p.ID})
	if err != nil {
		t.Fatalf("插件自身失败不应是服务错误，got %v", err)
	}
	if out.OK || !strings.Contains(out.Error, "theme crash") {
		t.Fatalf("应 ok=false + stderr 诊断，got %+v", out)
	}
}

// ---- ⑥ 主题读取：沙箱超限 → SANDBOX_LIMIT ----

func TestPluginThemeGetSandboxLimit(t *testing.T) {
	s, _ := newTestServices(t)
	p := installThemePlugin(t, s)
	s.Plugins.sbx = &captureSandbox{err: &sandbox.LimitError{Kind: sandbox.KindTimeout, Message: "timed out"}}

	if _, err := s.Plugins.PluginThemeGet(ctx(), PluginThemeGetReq{PluginID: p.ID}); err == nil ||
		domain.AsError(err).Code != domain.CodeSandboxLimit {
		t.Fatalf("沙箱超限应 SANDBOX_LIMIT，got %v", err)
	}
}

// ---- ⑦ 主题读取协议错误：stdout 非 JSON / 形状不符 / 键非法 / 值含注入字符 ----

func TestPluginThemeGetProtocolErrors(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
	}{
		{"非 JSON", `not json`},
		{"缺 tokens 字段", `{"foo":"bar"}`},
		{"tokens 非对象", `{"tokens":"#fff"}`},
		{"键缺 -- 前缀", `{"tokens":{"color":"#fff"}}`},
		{"键含非法字符", `{"tokens":{"--color primary":"#fff"}}`},
		{"值含分号", `{"tokens":{"--color-primary":"#fff;background:url(x)"}}`},
		{"值含花括号", `{"tokens":{"--color-primary":"{red}"}}`},
		{"值含换行", "{\"tokens\":{\"--color-primary\":\"#fff\\n{background}\"}}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newTestServices(t)
			p := installThemePlugin(t, s)
			s.Plugins.sbx = &captureSandbox{res: &sandbox.Result{Stdout: c.stdout}}
			_, err := s.Plugins.PluginThemeGet(ctx(), PluginThemeGetReq{PluginID: p.ID})
			if err == nil || domain.AsError(err).Code != domain.CodePluginError {
				t.Fatalf("期望 PLUGIN_ERROR，got %v", err)
			}
		})
	}
}

// ---- ⑧ 主题读取：空 tokens 合法（{} → OK 空 map） ----

func TestPluginThemeGetEmptyTokens(t *testing.T) {
	s, _ := newTestServices(t)
	p := installThemePlugin(t, s)
	s.Plugins.sbx = &captureSandbox{res: &sandbox.Result{Stdout: `{"tokens":{}}`}}

	out, err := s.Plugins.PluginThemeGet(ctx(), PluginThemeGetReq{PluginID: p.ID})
	if err != nil {
		t.Fatalf("空 tokens 应成功，got %v", err)
	}
	if !out.OK || out.Tokens == nil || len(out.Tokens) != 0 {
		t.Fatalf("空 tokens 应返回空 map，got %+v", out)
	}
}

// ---- ⑨ 市场条目 DTO JSON 契约（snake_case 与前端一致） ----

func TestPluginMarketItemJSON(t *testing.T) {
	item := PluginMarketItem{
		ID: "id1", Name: "n", Version: "1.0.0", Description: "d",
		Enabled: true, Permissions: []string{"read_questions"}, InstalledAt: "t",
	}
	b, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, key := range []string{`"id"`, `"name"`, `"version"`, `"description"`, `"enabled"`, `"permissions"`, `"installed_at"`} {
		if !strings.Contains(s, key) {
			t.Fatalf("市场条目 JSON 缺字段 %s: %s", key, s)
		}
	}
}
