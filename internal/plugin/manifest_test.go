package plugin

import (
	"testing"

	"lumo/internal/domain"
)

// validManifest 是全字段合法的基线 manifest。
func validManifest() *PluginManifest {
	return &PluginManifest{
		Name:        "hello",
		Version:     "1.0.0",
		Description: "示例插件",
		Entrypoint:  []string{"python3", "main.py"},
		Permissions: []string{"read_questions"},
		APIVersion:  "1",
	}
}

// wantInvalid 断言 err 为 INVALID_ARGUMENT。
func wantInvalid(t *testing.T, name string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: 期望 INVALID_ARGUMENT，got nil", name)
	}
	if de, ok := err.(*domain.Error); !ok || de.Code != domain.CodeInvalidArgument {
		t.Fatalf("%s: 期望 INVALID_ARGUMENT，got %v", name, err)
	}
}

// TestValidateManifestValid：合法 manifest 通过校验。
func TestValidateManifestValid(t *testing.T) {
	if err := ValidateManifest(validManifest()); err != nil {
		t.Fatalf("合法 manifest 应通过：%v", err)
	}
}

// TestValidateManifestInvalid：字段矩阵 → INVALID_ARGUMENT。
func TestValidateManifestInvalid(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*PluginManifest)
	}{
		{"missing name", func(m *PluginManifest) { m.Name = "" }},
		{"missing version", func(m *PluginManifest) { m.Version = "" }},
		{"bad version", func(m *PluginManifest) { m.Version = "v1.0.0" }},
		{"empty entrypoint", func(m *PluginManifest) { m.Entrypoint = nil }},
		{"empty token", func(m *PluginManifest) { m.Entrypoint = []string{"python3", ""} }},
		{"shell pipe", func(m *PluginManifest) { m.Entrypoint = []string{"python3", "main.py | rm -rf /"} }},
		{"shell redir", func(m *PluginManifest) { m.Entrypoint = []string{"sh", "-c", "ls > /tmp/x"} }},
		{"shell amp", func(m *PluginManifest) { m.Entrypoint = []string{"cmd", "/c", "a & b"} }},
		{"shell dollar", func(m *PluginManifest) { m.Entrypoint = []string{"sh", "-c", "echo $HOME"} }},
		{"shell backtick", func(m *PluginManifest) { m.Entrypoint = []string{"sh", "-c", "`id`"} }},
		{"shell newline", func(m *PluginManifest) { m.Entrypoint = []string{"sh", "-c", "a\nb"} }},
		{"unknown permission", func(m *PluginManifest) { m.Permissions = []string{"delete_everything"} }},
		{"bad api_version", func(m *PluginManifest) { m.APIVersion = "2" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := validManifest()
			c.mut(m)
			wantInvalid(t, c.name, ValidateManifest(m))
		})
	}
}

// TestValidateManifestOverlongName：名称超过 100 字符 → INVALID_ARGUMENT。
func TestValidateManifestOverlongName(t *testing.T) {
	m := validManifest()
	m.Name = ""
	for i := 0; i < 101; i++ {
		m.Name += "名"
	}
	wantInvalid(t, "overlong name", ValidateManifest(m))
}

// TestValidateManifestSemver：合法语义化版本通过；非语义化拒绝。
func TestValidateManifestSemver(t *testing.T) {
	for _, v := range []string{"1.0", "1.0.0", "2.1.3-beta.1", "1.0.0+build.5"} {
		m := validManifest()
		m.Version = v
		if err := ValidateManifest(m); err != nil {
			t.Fatalf("版本 %q 应通过：%v", v, err)
		}
	}
	for _, v := range []string{"1", "v1.0.0", "1..0", "abc", "1.0.0-beta_1"} {
		m := validManifest()
		m.Version = v
		wantInvalid(t, "bad version "+v, ValidateManifest(m))
	}
}

// TestParseManifestBadJSON：非法 JSON → INVALID_ARGUMENT。
func TestParseManifestBadJSON(t *testing.T) {
	_, err := ParseManifest([]byte("{not json"))
	wantInvalid(t, "bad json", err)
}
