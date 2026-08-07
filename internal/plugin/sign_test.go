package plugin

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

// devPrivateKeyHex 是 Lumo 开发用 Ed25519 私钥（十六进制，64 字节）。
//
// 仅供测试签名使用（SignForTest），与 keys.go 的 DevPublicKeyHex 配对；
// 生产代码路径绝不引用本常量。QA/手工签名请使用 internal/plugin/sign 命令。
const devPrivateKeyHex = "d2f4a2fd81be3babf3af371a39b641c22036bf102839a719d9eeb1910ac0ec43f0fcac376d688aa1dbb9154ea2fc91cf57d7614819dfb9c65be33647e36328a6"

// SignForTest 用开发私钥为 manifest 原始字节签名，返回十六进制签名。
// 测试辅助函数：TDD/QA 用其构造合法签名（篡改字节后签名即失效）。
func SignForTest(manifestBytes []byte) string {
	key, err := hex.DecodeString(devPrivateKeyHex)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(ed25519.Sign(ed25519.PrivateKey(key), manifestBytes))
}

// TestSignVerifyRoundTrip：签名-验证闭环——合法签名通过；篡改 manifest 或
// 破坏签名均拒绝。
func TestSignVerifyRoundTrip(t *testing.T) {
	raw := []byte(`{"name":"hello","version":"1.0.0","entrypoint":["python3","main.py"],"permissions":[],"api_version":"1"}`)
	sig := SignForTest(raw)
	if sig == "" {
		t.Fatal("SignForTest 应产生非空签名")
	}
	if !VerifySignature(raw, sig) {
		t.Fatal("合法签名应通过验证")
	}
	// 篡改 manifest 字节 → 拒绝
	if VerifySignature([]byte(`{"name":"tampered"}`), sig) {
		t.Fatal("篡改后的 manifest 不应通过验证")
	}
	// 破坏签名 → 拒绝
	broken := sig[:len(sig)-2] + "00"
	if VerifySignature(raw, broken) {
		t.Fatal("损坏的签名不应通过验证")
	}
	// 非十六进制签名 → 拒绝（不 panic）
	if VerifySignature(raw, "not-hex-!!") {
		t.Fatal("非十六进制签名不应通过验证")
	}
	if VerifySignature(raw, "") {
		t.Fatal("空签名不应通过验证")
	}
}
