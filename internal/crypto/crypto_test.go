package crypto

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "plain.db")
	enc := filepath.Join(dir, "backup.sqz")
	dec := filepath.Join(dir, "restored.db")

	payload := bytes.Repeat([]byte("lumo-data-"), 1024)
	if err := os.WriteFile(src, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	password := "correct horse battery staple"

	if err := EncryptFile(src, enc, password); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if err := DecryptFile(enc, dec, password); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	got, err := os.ReadFile(dec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("roundtrip mismatch")
	}

	// 加密文件应包含 magic
	raw, _ := os.ReadFile(enc)
	if !bytes.Equal(raw[:len(magic)], magic) {
		t.Fatal("magic missing")
	}
	// 密文不应包含明文
	if bytes.Contains(raw, []byte("lumo-data-")) {
		t.Fatal("plaintext leaked into ciphertext")
	}
}

func TestDecryptWrongPassword(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "plain.db")
	enc := filepath.Join(dir, "backup.sqz")
	if err := os.WriteFile(src, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EncryptFile(src, enc, "right"); err != nil {
		t.Fatal(err)
	}
	if err := DecryptFile(enc, filepath.Join(dir, "out.db"), "wrong"); err == nil {
		t.Fatal("expected error for wrong password")
	}
}

func TestDecryptGarbage(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "garbage.sqz")
	if err := os.WriteFile(f, []byte("not a backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := DecryptFile(f, filepath.Join(dir, "out.db"), "x"); err == nil {
		t.Fatal("expected error for garbage file")
	}
}

func TestEncryptDifferentSalts(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "plain.db")
	if err := os.WriteFile(src, []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(dir, "a.sqz")
	b := filepath.Join(dir, "b.sqz")
	if err := EncryptFile(src, a, "pw"); err != nil {
		t.Fatal(err)
	}
	if err := EncryptFile(src, b, "pw"); err != nil {
		t.Fatal(err)
	}
	ra, _ := os.ReadFile(a)
	rb, _ := os.ReadFile(b)
	if bytes.Equal(ra, rb) {
		t.Fatal("same salt reused; encryption not randomized")
	}
}
