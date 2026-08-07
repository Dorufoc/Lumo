// scanner.go 分享前内容安全扫描器（完整设计文档 4.20：分享前强制安全扫描，扫描失败不允许发布）。
// 确定性扫描：对序列化后的分享内容做正则规则匹配，返回 {clean, findings}。
// 保持保守（宁严勿松）：命中任意规则即拒绝发布。
package service

import (
	"regexp"
	"strconv"
)

// ScanResult 是内容安全扫描结果。
type ScanResult struct {
	Clean    bool     `json:"clean"`
	Findings []string `json:"findings"`
}

// scanRule 描述一条扫描规则。
type scanRule struct {
	name    string
	pattern *regexp.Regexp
}

// shareScanRules 分享内容扫描规则集：
//  1. 可执行脚本标签 / 危险 URL scheme（javascript:/data:html/vbscript:）
//  2. 内联事件处理属性（onerror/onclick 等）
//  3. 绝对本地路径（file://、Unix /etc 等、Windows 盘符路径）
//  4. 明显 PII 标记（中国身份证 18 位、大陆手机号 11 位）
var shareScanRules = []scanRule{
	{name: "script_tag", pattern: regexp.MustCompile(`(?i)<\s*script\b`)},
	{name: "javascript_scheme", pattern: regexp.MustCompile(`(?i)javascript\s*:`)},
	{name: "data_html_scheme", pattern: regexp.MustCompile(`(?i)data\s*:\s*text/html`)},
	{name: "vbscript_scheme", pattern: regexp.MustCompile(`(?i)vbscript\s*:`)},
	{name: "event_handler", pattern: regexp.MustCompile(`(?i)\s(?:onerror|onload|onclick|ondblclick|onmousedown|onmouseup|onmousemove|onmouseover|onmouseout|onfocus|onblur|onkeydown|onkeyup|onkeypress|onsubmit|onchange|oninput|onpaste|ondrop|oncontextmenu)\s*=`)},
	{name: "file_scheme", pattern: regexp.MustCompile(`(?i)file\s*:\s*/`)},
	{name: "unix_abs_path", pattern: regexp.MustCompile(`(?i)(?:^|[\s"'` + "`" + `(])(?:/etc|/home|/usr|/var|/opt|/root|/tmp|/bin|/sbin|/lib)(?:/|\b)`)},
	{name: "windows_abs_path", pattern: regexp.MustCompile(`(?i)(?:^|[\s"'` + "`" + `(])[a-z]:[\\/]`)},
	{name: "id_card", pattern: regexp.MustCompile(`\b\d{17}[\dXx]\b`)},
	{name: "phone", pattern: regexp.MustCompile(`\b1[3-9]\d{9}\b`)},
}

// scanContent 对序列化内容执行安全扫描；无任何命中 → clean。
// 先对 JSON 里 ASCII 范围的 \u00XX 转义做还原（如 \u003c → '<'），
// 否则题目载荷中的 <script> 在序列化时被 json 转义成 \u003c，规则无法命中。
func scanContent(content []byte) ScanResult {
	var findings []string
	normalized := unescapeASCIIJSON(content)
	for _, r := range shareScanRules {
		if r.pattern.Match(normalized) {
			findings = append(findings, r.name)
		}
	}
	return ScanResult{Clean: len(findings) == 0, Findings: findings}
}

// asciiJSONEscape 匹配 JSON 中 ASCII 码点范围的 \u00XX 转义（\u0000–\u007F）。
var asciiJSONEscape = regexp.MustCompile(`\\u00([0-9a-fA-F]{2})`)

// unescapeASCIIJSON 把 JSON 字符串里的 \u00XX（ASCII 范围）还原为原始字节。
// 接收方解析 JSON 后看到的是还原文本，扫描对象应与接收方所见一致。
func unescapeASCIIJSON(b []byte) []byte {
	return asciiJSONEscape.ReplaceAllFunc(b, func(m []byte) []byte {
		v, err := strconv.ParseUint(string(m[4:6]), 16, 8)
		if err != nil {
			return m
		}
		return []byte{byte(v)}
	})
}
