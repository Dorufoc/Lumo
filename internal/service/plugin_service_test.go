package service

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lumo/internal/domain"
	"lumo/internal/plugin"
	"lumo/internal/sandbox"
)

// testDevPrivateKeyHex 是开发 Ed25519 私钥（与 internal/plugin/keys.go 公钥配对）。
// 仅供测试签名：构造合法签名（篡改字节后签名即失效）。生产路径绝无此常量。
const testDevPrivateKeyHex = "d2f4a2fd81be3babf3af371a39b641c22036bf102839a719d9eeb1910ac0ec43f0fcac376d688aa1dbb9154ea2fc91cf57d7614819dfb9c65be33647e36328a6"

// noPermPluginManifest 无权限声明的合法插件清单。
const noPermPluginManifest = `{"name":"hello","version":"1.0.0","description":"示例插件","entrypoint":["python3","main.py"],"permissions":[],"api_version":"1"}`

// permPluginManifest 声明 read_questions 权限的合法插件清单。
const permPluginManifest = `{"name":"perm","version":"1.0.0","description":"需要权限","entrypoint":["python3","main.py"],"permissions":["read_questions"],"api_version":"1"}`

// signTestPlugin 用开发私钥为 manifest 原始字节签名。
func signTestPlugin(t *testing.T, raw []byte) string {
	t.Helper()
	key, err := hex.DecodeString(testDevPrivateKeyHex)
	if err != nil {
		t.Fatalf("decode dev private key: %v", err)
	}
	return hex.EncodeToString(ed25519.Sign(ed25519.PrivateKey(key), raw))
}

// writeTestManifest 把 manifest 原始字节写入临时文件，返回文件路径。
func writeTestManifest(t *testing.T, raw string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(p, []byte(raw), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return p
}

// installTestPlugin 安装一个签名合法插件，返回 DTO 与 manifest 文件路径。
func installTestPlugin(t *testing.T, s *Services, manifestRaw string) (*Plugin, string) {
	t.Helper()
	path := writeTestManifest(t, manifestRaw)
	sig := signTestPlugin(t, []byte(manifestRaw))
	p, err := s.Plugins.PluginInstall(ctx(), PluginInstallReq{Path: path, Signature: sig})
	if err != nil {
		t.Fatalf("install plugin: %v", err)
	}
	return p, path
}

// captureSandbox 记录最后一次执行规范（验证协议）并返回固定结果。
type captureSandbox struct {
	res  *sandbox.Result
	err  error
	spec sandbox.Spec
}

func (c *captureSandbox) Run(_ context.Context, spec sandbox.Spec) (*sandbox.Result, error) {
	c.spec = spec
	return c.res, c.err
}

// ---- ① 安装 happy：签名合法 → 落库 enabled=0，manifest 逐字保存，权限待确认 ----

func TestPluginInstallHappy(t *testing.T) {
	s, _ := newTestServices(t)
	p, path := installTestPlugin(t, s, permPluginManifest)

	if p.ID == "" || p.Name != "perm" || p.Version != "1.0.0" {
		t.Fatalf("unexpected plugin DTO: %+v", p)
	}
	if p.Enabled {
		t.Fatal("新装插件应默认禁用")
	}
	if len(p.Permissions) != 0 {
		t.Fatalf("新装插件权限应待确认，got %v", p.Permissions)
	}
	if len(p.Manifest.Entrypoint) != 2 || p.Manifest.Entrypoint[0] != "python3" {
		t.Fatalf("manifest 解析异常: %+v", p.Manifest)
	}
	// 落库行：enabled=0，manifest_json 与源文件字节一致
	row, err := s.Repo.GetPluginByID(ctx(), p.ID)
	if err != nil {
		t.Fatalf("get plugin row: %v", err)
	}
	if row == nil || row.Enabled {
		t.Fatalf("row 应存在且禁用，got %+v", row)
	}
	raw, _ := os.ReadFile(path)
	if row.ManifestJSON != string(raw) {
		t.Fatalf("manifest_json 应保存原始签名字节")
	}
	if row.PermissionsJSON != "[]" {
		t.Fatalf("permissions_json 初始应为空数组，got %q", row.PermissionsJSON)
	}
}

// ---- ② 安装来源：目录形式（<dir>/manifest.json）+ 单文件形式 ----

func TestPluginInstallFromDirectory(t *testing.T) {
	s, _ := newTestServices(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(noPermPluginManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	sig := signTestPlugin(t, []byte(noPermPluginManifest))
	p, err := s.Plugins.PluginInstall(ctx(), PluginInstallReq{Path: dir, Signature: sig})
	if err != nil {
		t.Fatalf("install from dir: %v", err)
	}
	if p.Name != "hello" {
		t.Fatalf("unexpected plugin: %+v", p)
	}
}

// ---- ③ 安装校验矩阵：字段非法 → INVALID_ARGUMENT（即使签名合法） ----

func TestPluginInstallValidationMatrix(t *testing.T) {
	s, _ := newTestServices(t)
	cases := []struct {
		name     string
		manifest string
	}{
		{"missing name", `{"version":"1.0.0","entrypoint":["python3","main.py"],"permissions":[],"api_version":"1"}`},
		{"bad api_version", `{"name":"x","version":"1.0.0","entrypoint":["python3","main.py"],"permissions":[],"api_version":"2"}`},
		{"shell metachar", `{"name":"x","version":"1.0.0","entrypoint":["sh","-c","ls | cat"],"permissions":[],"api_version":"1"}`},
		{"unknown permission", `{"name":"x","version":"1.0.0","entrypoint":["python3","main.py"],"permissions":["read_questions","delete_everything"],"api_version":"1"}`},
		{"bad version", `{"name":"x","version":"abc","entrypoint":["python3","main.py"],"permissions":[],"api_version":"1"}`},
		{"empty entrypoint", `{"name":"x","version":"1.0.0","entrypoint":[],"permissions":[],"api_version":"1"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeTestManifest(t, c.manifest)
			sig := signTestPlugin(t, []byte(c.manifest))
			_, err := s.Plugins.PluginInstall(ctx(), PluginInstallReq{Path: path, Signature: sig})
			if err == nil {
				t.Fatalf("期望 INVALID_ARGUMENT，got nil")
			}
			if de, ok := err.(*domain.Error); !ok || de.Code != domain.CodeInvalidArgument {
				t.Fatalf("期望 INVALID_ARGUMENT，got %v", err)
			}
		})
	}
}

// ---- ④ 签名验证：篡改字节 / 错误签名 / 非 JSON → INVALID_ARGUMENT ----

func TestPluginInstallSignatureRejected(t *testing.T) {
	s, _ := newTestServices(t)
	raw := []byte(noPermPluginManifest)

	// 篡改 manifest：文件内容与签名对象不一致
	path := writeTestManifest(t, string(raw)+" ")
	_, err := s.Plugins.PluginInstall(ctx(), PluginInstallReq{Path: path, Signature: signTestPlugin(t, raw)})
	if err == nil || domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("篡改 manifest 应 INVALID_ARGUMENT，got %v", err)
	}

	// 错误签名
	path2 := writeTestManifest(t, noPermPluginManifest)
	_, err = s.Plugins.PluginInstall(ctx(), PluginInstallReq{Path: path2, Signature: signTestPlugin(t, []byte(permPluginManifest))})
	if err == nil || domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("错误签名应 INVALID_ARGUMENT，got %v", err)
	}

	// 非 JSON manifest
	path3 := writeTestManifest(t, "{not json")
	_, err = s.Plugins.PluginInstall(ctx(), PluginInstallReq{Path: path3, Signature: signTestPlugin(t, []byte("{not json"))})
	if err == nil || domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("非法 JSON 应 INVALID_ARGUMENT，got %v", err)
	}
}

// ---- ⑤ 安装失败：路径不存在 / 缺 signature / 目录缺 manifest.json ----

func TestPluginInstallNotFound(t *testing.T) {
	s, _ := newTestServices(t)
	_, err := s.Plugins.PluginInstall(ctx(), PluginInstallReq{
		Path: filepath.Join(t.TempDir(), "nope", "manifest.json"), Signature: "deadbeef",
	})
	if err == nil || domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("不存在路径应 NOT_FOUND，got %v", err)
	}
	// 空目录 → 缺 manifest.json → NOT_FOUND
	dir := t.TempDir()
	_, err = s.Plugins.PluginInstall(ctx(), PluginInstallReq{Path: dir, Signature: "deadbeef"})
	if err == nil || domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("目录缺 manifest.json 应 NOT_FOUND，got %v", err)
	}
	// 缺 signature
	path := writeTestManifest(t, noPermPluginManifest)
	_, err = s.Plugins.PluginInstall(ctx(), PluginInstallReq{Path: path})
	if err == nil || domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("缺 signature 应 INVALID_ARGUMENT，got %v", err)
	}
}

// ---- ⑥ 安装去重：同名插件 → CONFLICT ----

func TestPluginInstallDuplicateName(t *testing.T) {
	s, _ := newTestServices(t)
	installTestPlugin(t, s, noPermPluginManifest)
	path := writeTestManifest(t, noPermPluginManifest)
	_, err := s.Plugins.PluginInstall(ctx(), PluginInstallReq{Path: path, Signature: signTestPlugin(t, []byte(noPermPluginManifest))})
	if err == nil || domain.AsError(err).Code != domain.CodeConflict {
		t.Fatalf("同名插件应 CONFLICT，got %v", err)
	}
}

// ---- ⑦ 启用权限门禁：声明权限未确认 → INVALID_STATE；确认后可启用；禁用始终成功 ----

func TestPluginSetEnabledPermissionGate(t *testing.T) {
	s, _ := newTestServices(t)
	p, _ := installTestPlugin(t, s, permPluginManifest)

	// 声明了权限但未确认 → 拒绝启用
	_, err := s.Plugins.PluginSetEnabled(ctx(), PluginSetEnabledReq{PluginID: p.ID, Enabled: true})
	if err == nil || domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("未确认权限启用应 INVALID_STATE，got %v", err)
	}
	// 确认权限（弹窗同意）
	confirmed, err := s.Plugins.PluginConfirmPermissions(ctx(), PluginConfirmPermissionsReq{PluginID: p.ID, Permissions: []string{"read_questions"}})
	if err != nil {
		t.Fatalf("confirm permissions: %v", err)
	}
	if len(confirmed.Permissions) != 1 || confirmed.Permissions[0] != "read_questions" {
		t.Fatalf("confirmed permissions 异常: %+v", confirmed)
	}
	// 确认后启用成功
	on, err := s.Plugins.PluginSetEnabled(ctx(), PluginSetEnabledReq{PluginID: p.ID, Enabled: true})
	if err != nil {
		t.Fatalf("enable after confirm: %v", err)
	}
	if !on.Enabled {
		t.Fatal("插件应已启用")
	}
	// 禁用始终成功
	off, err := s.Plugins.PluginSetEnabled(ctx(), PluginSetEnabledReq{PluginID: p.ID, Enabled: false})
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if off.Enabled {
		t.Fatal("插件应已禁用")
	}
}

// ---- ⑧ 无权限插件可直接启用 ----

func TestPluginSetEnabledNoPermissions(t *testing.T) {
	s, _ := newTestServices(t)
	p, _ := installTestPlugin(t, s, noPermPluginManifest)
	on, err := s.Plugins.PluginSetEnabled(ctx(), PluginSetEnabledReq{PluginID: p.ID, Enabled: true})
	if err != nil {
		t.Fatalf("无权限插件启用应直接成功，got %v", err)
	}
	if !on.Enabled {
		t.Fatal("插件应已启用")
	}
}

// ---- ⑨ 权限确认校验：未声明权限 / 未知插件 ----

func TestPluginConfirmPermissionsValidation(t *testing.T) {
	s, _ := newTestServices(t)
	p, _ := installTestPlugin(t, s, permPluginManifest)
	// 确认未声明权限 → INVALID_ARGUMENT
	_, err := s.Plugins.PluginConfirmPermissions(ctx(), PluginConfirmPermissionsReq{PluginID: p.ID, Permissions: []string{"read_flashcards"}})
	if err == nil || domain.AsError(err).Code != domain.CodeInvalidArgument {
		t.Fatalf("未声明权限应 INVALID_ARGUMENT，got %v", err)
	}
	// 未知插件 → NOT_FOUND
	_, err = s.Plugins.PluginConfirmPermissions(ctx(), PluginConfirmPermissionsReq{PluginID: "nope", Permissions: []string{"read_questions"}})
	if err == nil || domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("未知插件应 NOT_FOUND，got %v", err)
	}
	// 未知插件 SetEnabled → NOT_FOUND
	_, err = s.Plugins.PluginSetEnabled(ctx(), PluginSetEnabledReq{PluginID: "nope", Enabled: true})
	if err == nil || domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("未知插件 SetEnabled 应 NOT_FOUND，got %v", err)
	}
}

// ---- ⑩ 调用 happy：桩沙箱返回 JSON stdout → 解析成功 + 协议校验 ----

func TestPluginInvokeHappy(t *testing.T) {
	s, _ := newTestServices(t)
	p, _ := installTestPlugin(t, s, noPermPluginManifest)
	if _, err := s.Plugins.PluginSetEnabled(ctx(), PluginSetEnabledReq{PluginID: p.ID, Enabled: true}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	capt := &captureSandbox{res: &sandbox.Result{Stdout: `{"ok":true,"greeting":"hello"}`}}
	s.Plugins.sbx = capt

	out, err := s.Plugins.PluginInvoke(ctx(), PluginInvokeReq{PluginID: p.ID, Params: json.RawMessage(`{"name":"world"}`)})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !out.OK || out.Error != "" {
		t.Fatalf("unexpected result: %+v", out)
	}
	got, ok := out.Result.(map[string]any)
	if !ok || got["greeting"] != "hello" {
		t.Fatalf("result 解析异常: %+v", out.Result)
	}
	// 协议：entrypoint 原样传参，stdin 为 JSON-RPC 请求，超时/输出上限与常量一致
	if len(capt.spec.Args) != 2 || capt.spec.Args[0] != "python3" {
		t.Fatalf("spec.Args 异常: %v", capt.spec.Args)
	}
	if !strings.Contains(capt.spec.Stdin, `"method":"run"`) || !strings.Contains(capt.spec.Stdin, `"params":{"name":"world"}`) {
		t.Fatalf("stdin 协议异常: %s", capt.spec.Stdin)
	}
	if capt.spec.Timeout != plugin.Timeout || capt.spec.MaxOutput != plugin.MaxOutput {
		t.Fatalf("spec 资源上限异常: %+v", capt.spec)
	}
}

// ---- ⑪ 调用禁用插件 → INVALID_STATE ----

func TestPluginInvokeDisabled(t *testing.T) {
	s, _ := newTestServices(t)
	p, _ := installTestPlugin(t, s, noPermPluginManifest)
	_, err := s.Plugins.PluginInvoke(ctx(), PluginInvokeReq{PluginID: p.ID})
	if err == nil || domain.AsError(err).Code != domain.CodeInvalidState {
		t.Fatalf("禁用插件调用应 INVALID_STATE，got %v", err)
	}
	// 未知插件 → NOT_FOUND
	_, err = s.Plugins.PluginInvoke(ctx(), PluginInvokeReq{PluginID: "nope"})
	if err == nil || domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("未知插件调用应 NOT_FOUND，got %v", err)
	}
}

// ---- ⑫ 调用沙箱超限 → SANDBOX_LIMIT ----

func TestPluginInvokeSandboxLimit(t *testing.T) {
	s, _ := newTestServices(t)
	p, _ := installTestPlugin(t, s, noPermPluginManifest)
	if _, err := s.Plugins.PluginSetEnabled(ctx(), PluginSetEnabledReq{PluginID: p.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s.Plugins.sbx = &captureSandbox{err: &sandbox.LimitError{Kind: sandbox.KindTimeout, Message: "timed out"}}
	_, err := s.Plugins.PluginInvoke(ctx(), PluginInvokeReq{PluginID: p.ID})
	if err == nil || domain.AsError(err).Code != domain.CodeSandboxLimit {
		t.Fatalf("沙箱超限应 SANDBOX_LIMIT，got %v", err)
	}
}

// ---- ⑬ 调用插件自身失败：非零退出码 → ok=false（非服务错误） ----

func TestPluginInvokePluginFailure(t *testing.T) {
	s, _ := newTestServices(t)
	p, _ := installTestPlugin(t, s, noPermPluginManifest)
	if _, err := s.Plugins.PluginSetEnabled(ctx(), PluginSetEnabledReq{PluginID: p.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s.Plugins.sbx = &captureSandbox{res: &sandbox.Result{ExitCode: 1, Stderr: "boom"}}
	out, err := s.Plugins.PluginInvoke(ctx(), PluginInvokeReq{PluginID: p.ID})
	if err != nil {
		t.Fatalf("插件自身失败不应是服务错误，got %v", err)
	}
	if out.OK || !strings.Contains(out.Error, "boom") {
		t.Fatalf("应 ok=false + stderr 诊断，got %+v", out)
	}
}

// ---- ⑭ 调用协议错误：stdout 非 JSON → PLUGIN_ERROR ----

func TestPluginInvokeProtocolError(t *testing.T) {
	s, _ := newTestServices(t)
	p, _ := installTestPlugin(t, s, noPermPluginManifest)
	if _, err := s.Plugins.PluginSetEnabled(ctx(), PluginSetEnabledReq{PluginID: p.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s.Plugins.sbx = &captureSandbox{res: &sandbox.Result{Stdout: "not json"}}
	_, err := s.Plugins.PluginInvoke(ctx(), PluginInvokeReq{PluginID: p.ID})
	if err == nil || domain.AsError(err).Code != domain.CodePluginError {
		t.Fatalf("协议错误应 PLUGIN_ERROR，got %v", err)
	}
}

// ---- ⑮ 卸载：删除行；二次卸载 → NOT_FOUND ----

func TestPluginUninstall(t *testing.T) {
	s, _ := newTestServices(t)
	p, _ := installTestPlugin(t, s, noPermPluginManifest)
	del, err := s.Plugins.PluginUninstall(ctx(), PluginUninstallReq{PluginID: p.ID})
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !del.Deleted || del.DeletedAt == "" {
		t.Fatalf("unexpected DeleteResult: %+v", del)
	}
	row, err := s.Repo.GetPluginByID(ctx(), p.ID)
	if err != nil || row != nil {
		t.Fatalf("行应已删除，row=%+v err=%v", row, err)
	}
	_, err = s.Plugins.PluginUninstall(ctx(), PluginUninstallReq{PluginID: p.ID})
	if err == nil || domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("二次卸载应 NOT_FOUND，got %v", err)
	}
}

// ---- ⑯ 列表：多插件倒序返回 ----

func TestPluginList(t *testing.T) {
	s, _ := newTestServices(t)
	installTestPlugin(t, s, noPermPluginManifest)
	installTestPlugin(t, s, permPluginManifest)
	list, err := s.Plugins.PluginList(ctx())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("应列出 2 个插件，got %d", len(list))
	}
}
