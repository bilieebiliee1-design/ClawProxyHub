// keys_mask_test.go — 密钥掩码尾缀与明文一致性回归（v1.4.8 修复①）。
// 旧掩码为 sha256(id|createdAt) 前 4 字节，与明文尾缀无关（首页/面板显示与
// reveal 明文尾 8 位对不上）；修复后掩码 = 明文尾 8 位，明文不可得（存量哈希
// 密钥 / 解密失败）时回退旧识别哈希口径。
package adminapi

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"io.nexport.gateway/core/account"
	"io.nexport.gateway/core/model"
)

// TestKeyMaskMatchesPlainTail 新格式密钥（0x01 前缀 AES-GCM）：掩码尾 8 位必须
// 等于明文尾 8 位（与首页/面板 reveal 回显一致）。
func TestKeyMaskMatchesPlainTail(t *testing.T) {
	t.Setenv("CPH_SECRET_KEY", strings.Repeat("ab", 32)) // 32 字节 hex 测试密钥
	dataDir := t.TempDir()
	raw := "cph-" + strings.Repeat("0123456789abcdef", 6) // createKey 同构：cph- + 48 hex，尾 8 位 = 89abcdef
	cipher := string(account.EncryptCredential(dataDir, []byte(raw)))
	if len(cipher) == len(raw) {
		t.Fatalf("加密不可用（测试前提不成立）：cipher 与明文等长")
	}
	k := model.Key{ID: 7, KeyCipher: cipher, CreatedAt: time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)}
	got := keyMask(dataDir, k)
	want := "cph-****" + raw[len(raw)-8:]
	if got != want {
		t.Fatalf("keyMask = %q, want %q（掩码尾缀须与明文尾 8 位一致）", got, want)
	}
}

// TestKeyMaskLegacyHashFallback 存量 sha256 哈希密钥（64 位 hex 无 0x01 前缀）：
// 明文未存，不可把哈希尾缀当明文展示，须回退识别哈希掩码（cph-****+8 hex 格式）。
func TestKeyMaskLegacyHashFallback(t *testing.T) {
	legacy := strings.Repeat("a", 64) // 存量 KeyCipher = sha256 hex
	k := model.Key{ID: 7, KeyCipher: legacy, CreatedAt: time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)}
	got := keyMask(t.TempDir(), k)
	if got == "cph-****"+legacy[len(legacy)-8:] {
		t.Fatalf("存量哈希密钥不应把 KeyCipher 尾缀当明文展示: %q", got)
	}
	if len(got) != len("cph-****")+8 || !strings.HasPrefix(got, "cph-****") {
		t.Fatalf("回退掩码格式错误: %q（应为 cph-****+8 hex）", got)
	}
}

// TestKeyMaskDecryptFailureFallback 解密失败（0x01 前缀但密文损坏）：不得把
// 密文/乱码尾缀当明文展示，须回退识别哈希掩码。
func TestKeyMaskDecryptFailureFallback(t *testing.T) {
	t.Setenv("CPH_SECRET_KEY", strings.Repeat("cd", 32))
	bad := string(append([]byte{0x01}, bytes.Repeat([]byte{0xEE}, 64)...))
	k := model.Key{ID: 3, KeyCipher: bad, CreatedAt: time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)}
	got := keyMask(t.TempDir(), k)
	if len(got) != len("cph-****")+8 || !strings.HasPrefix(got, "cph-****") {
		t.Fatalf("解密失败回退掩码格式错误: %q（应为 cph-****+8 hex）", got)
	}
	if strings.ContainsRune(strings.TrimPrefix(got, "cph-****"), 0xEE) {
		t.Fatalf("回退掩码不得包含密文字节: %q", got)
	}
}
