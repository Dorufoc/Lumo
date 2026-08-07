package plugin

import (
	"crypto/ed25519"
	"encoding/hex"
)

// VerifySignature 验证 manifest 原始字节上的 Ed25519 签名（十六进制）。
//
// 签名覆盖的是 manifest 的「原始字节」（客户端在 manifest 字段中发送/读取的
// 确切字节），与 ParseManifest 使用同一份 raw；任何字节变更都会导致验证失败。
// 验证使用内嵌的开发公钥（DevPublicKeyHex，编译期常量），与 internal/plugin/sign
// 命令、SignForTest 测试辅助函数的私钥配对。
func VerifySignature(manifestBytes []byte, sigHex string) bool {
	pub, err := hex.DecodeString(DevPublicKeyHex)
	if err != nil {
		return false
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), manifestBytes, sig)
}
