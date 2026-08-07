// Package plugin 提供插件系统核心：manifest 校验、Ed25519 签名验证与沙箱执行。
//
// 设计文档 4.24 / API 设计文档 7.13。插件 = 描述清单（manifest）+ 可执行逻辑；
// 安装必须携带基于开发公钥的 Ed25519 签名；运行经 internal/sandbox 隔离子进程
// （绝不落入宿主进程）。
package plugin

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"lumo/internal/domain"
)

// APIVersion 是当前支持的 manifest.api_version（不匹配即拒绝安装）。
const APIVersion = "1"

// 名称/版本长度上限（rune 数，与数据库 length(trim(name)) 语义一致按字符计）。
const (
	maxNameLen    = 100
	maxVersionLen = 50
)

// KnownPermissions 是 manifest 声明的可确认权限白名单（O3 能力点收敛面）。
// 权限含义（供前端弹窗展示）：
//   - read_questions  读取题库题目
//   - write_questions 写入题库题目
//   - read_notes      读取笔记
//   - read_flashcards 读取闪卡
//   - read_reports    读取学习报告
//   - call_provider   调用已配置的 LLM Provider
var KnownPermissions = map[string]bool{
	"read_questions":  true,
	"write_questions": true,
	"read_notes":      true,
	"read_flashcards": true,
	"read_reports":    true,
	"call_provider":   true,
}

// PluginManifest 是插件清单（manifest.json 的 JSON 契约）。
// 注意：签名覆盖的是 manifest 的「原始字节」（PluginInstall 请求中 path 指向的
// 文件内容），而非 Go 结构体重序列化后的字节——字段顺序/空白差异会导致签名失配。
type PluginManifest struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
	// Entrypoint 是插件入口命令与参数数组（例如 ["python3","main.py"]）。
	// 直接经 exec（无 shell）执行，禁止任何 shell 元字符。
	Entrypoint []string `json:"entrypoint"`
	// Permissions 是插件声明的权限（须 ⊆ KnownPermissions）。
	Permissions []string `json:"permissions"`
	APIVersion  string   `json:"api_version"`
}

// semverish 是宽松的语义化版本格式：1.0 / 1.0.0 / 1.0.0-beta.1 / 1.0.0+meta。
var semverish = regexp.MustCompile(`^[0-9]+\.[0-9]+(\.[0-9]+)?(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// shellMetaChars 是 entrypoint 中一律拒绝的字符（无 shell 场景下为纵深防御，
// 防止任何未来经 shell 执行的路径被注入）。
const shellMetaChars = "|><&;$`\x00\n\r"

// ValidateManifest 校验 manifest 字段；任一不合法返回 INVALID_ARGUMENT。
func ValidateManifest(m *PluginManifest) error {
	name := strings.TrimSpace(m.Name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		return domain.InvalidArg("插件名称须为非空且不超过 %d 字符", maxNameLen)
	}
	version := strings.TrimSpace(m.Version)
	if version == "" || utf8.RuneCountInString(version) > maxVersionLen || !semverish.MatchString(version) {
		return domain.InvalidArg("插件版本须为语义化版本（如 1.0.0）且不超过 %d 字符", maxVersionLen)
	}
	if len(m.Entrypoint) == 0 {
		return domain.InvalidArg("插件入口（entrypoint）不能为空")
	}
	for _, tok := range m.Entrypoint {
		if strings.TrimSpace(tok) == "" {
			return domain.InvalidArg("插件入口命令不能包含空参数")
		}
		if strings.ContainsAny(tok, shellMetaChars) {
			return domain.InvalidArg("插件入口命令含非法字符（禁止 shell 元字符）")
		}
	}
	for _, perm := range m.Permissions {
		if !KnownPermissions[perm] {
			return domain.InvalidArg("插件声明了未知权限：%s", perm)
		}
	}
	if m.APIVersion != APIVersion {
		return domain.InvalidArg("插件 api_version 须为 %s（当前为 %s）", APIVersion, m.APIVersion)
	}
	return nil
}

// ParseManifest 解析并校验 manifest 原始字节；JSON 非法 → INVALID_ARGUMENT。
// 签名验证方应把同样的原始字节传给 VerifySignature。
func ParseManifest(raw []byte) (*PluginManifest, error) {
	var m PluginManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, domain.InvalidArg("manifest 不是合法 JSON: %v", err)
	}
	if err := ValidateManifest(&m); err != nil {
		return nil, err
	}
	return &m, nil
}
