// Package crypto 提供本地备份的对称加密：AES-256-GCM + 迭代密钥派生。
// 用途：加密导出/备份文件（端到端本地加密，服务端不解密）。
package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// 文件格式：magic(8) | version(1) | salt(16) | nonce(12) | ciphertext...
var (
	magic     = []byte("LUMO_BK1")
	version   = byte(1)
	saltLen   = 16
	nonceLen  = 12
	iterCount = 100_000
)

// deriveKey 从密码与盐派生 32 字节密钥（迭代 SHA-256，简化 PBKDF2）。
func deriveKey(password string, salt []byte) []byte {
	key := sha256.Sum256(append([]byte(password), salt...))
	for i := 0; i < iterCount-1; i++ {
		key = sha256.Sum256(key[:])
	}
	return key[:]
}

// EncryptFile 将 src 内容加密写入 dst；dst 不存在则创建（0600）。
func EncryptFile(src, dst, password string) error {
	plain, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	block, err := aes.NewCipher(deriveKey(password, salt))
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	ciphertext := gcm.Seal(nil, nonce, plain, nil)

	var buf bytes.Buffer
	buf.Write(magic)
	buf.WriteByte(version)
	buf.Write(salt)
	buf.Write(nonce)
	buf.Write(ciphertext)
	return os.WriteFile(dst, buf.Bytes(), 0o600)
}

// DecryptFile 将加密文件解密写入 dst，并校验 magic 与 GCM 认证。
func DecryptFile(src, dst, password string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	head := len(magic) + 1 + saltLen + nonceLen
	if len(raw) < head || !bytes.Equal(raw[:len(magic)], magic) {
		return errors.New("不是有效的 Lumo 加密备份文件")
	}
	if raw[len(magic)] != version {
		return fmt.Errorf("不支持的备份版本: %d", raw[len(magic)])
	}
	salt := raw[len(magic)+1 : len(magic)+1+saltLen]
	nonce := raw[len(magic)+1+saltLen : head]
	ciphertext := raw[head:]

	block, err := aes.NewCipher(deriveKey(password, salt))
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return errors.New("密码错误或备份文件已损坏")
	}
	return os.WriteFile(dst, plain, 0o600)
}

// RandomBytes 生成 n 字节随机数。
func RandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(rand.Reader, b)
	return b, err
}

// RandomUint64 生成随机 64 位整数。
func RandomUint64() (uint64, error) {
	b, err := RandomBytes(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b), nil
}
