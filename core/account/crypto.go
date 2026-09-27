// crypto.go — 凭据加密存储：AES-256-GCM。
// 密钥来源（按序）：CPH_SECRET_KEY（hex）> 运行时注入（安卓 Keystore 解封，SetInjectedKey）
// > <data>/secret.key 落盘密钥（双格式兼容，见下）> 自动生成并落盘。
//
// secret.key 落盘格式（NexPort fork 双格式兼容，基于 ClawProxyHub（AGPL-3.0）修改构建）：
//   - 桌面格式：32 字节裸密钥（无头）。恢复桌面备份（ApplyPendingRestore 换入 secret.key）
//     后即为此格式，必须原样可用；
//   - 信封格式（安卓）：首行 ASCII 头 "NXPORT-KEY-ENVELOPE v1"，其后为 base64(
//     Android Keystore 包裹的密钥块)。Go 无法解开 Keystore——Kotlin 侧解封后经
//     SetInjectedKey（或 bridge Config.SecretKeyHex）注入；信封存在但未注入时
//     明确报错，绝不静默重新生成（否则桌面备份凭据全解不开）。
// 加密数据以 0x01 前缀标记；解密失败按明文返回（平滑兼容存量数据）。
package account

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"io.nexport.gateway/core/logsink"
)

const encPrefix = byte(0x01)

// keyEnvelopeHeader 信封格式文件头（首个换行前）。
const keyEnvelopeHeader = "NXPORT-KEY-ENVELOPE v1"

// KeyFormat secret.key 落盘格式（InspectKeyFile 返回值）。
const (
	KeyFormatMissing   = "missing"   // 尚未生成
	KeyFormatRaw       = "raw"       // 桌面 32 字节裸密钥（备份换入场景）
	KeyFormatEnvelope  = "envelope"  // 安卓 Keystore 信封（需注入密钥）
	KeyFormatGenerated = "generated" // 本次调用新生成
)

// KeyLookupHash 返回 apikey 明文的确定性查找哈希（sha256 hex，64 字符）。
// 高熵随机 key 无需加盐；用作 keys.key_lookup 索引列，鉴权按等值 O(1) 命中，
// 免去 AES-GCM 密文（nonce 随机不可等值查）导致的全表解密扫描。
// 与存量 sha256 hex 密钥（KeyCipher 即本值）天然一致，故可直接回填。
func KeyLookupHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

var (
	keyMu       sync.Mutex
	aead        cipher.AEAD
	keyErr      error
	keyResolved bool // 当前密钥已解析（缓存有效）
	injectedKey []byte
)

// SetInjectedKey 注入运行时密钥（安卓侧由 Keystore 解封后传入，32 字节）。
// 使已缓存的解析结果失效（换 key 后下次加解密重载）。
func SetInjectedKey(key []byte) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if len(key) > 0 && len(key) != 32 {
		keyErr = fmt.Errorf("注入密钥长度须为 32 字节，实际 %d", len(key))
		return
	}
	if len(key) == 32 {
		injectedKey = append([]byte(nil), key...)
	} else {
		injectedKey = nil
	}
	keyResolved = false
	keyErr = nil
}

// loadKey 初始化加密器（进程内缓存；SetInjectedKey 使其失效重载）。
func loadKey(dataDir string) (cipher.AEAD, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if keyResolved {
		return aead, keyErr
	}
	key, err := loadOrCreateKey(dataDir)
	if err != nil {
		keyErr = err
		keyResolved = true
		return nil, keyErr
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		keyErr = err
		keyResolved = true
		return nil, keyErr
	}
	aead, keyErr = cipher.NewGCM(block)
	keyResolved = true
	return aead, keyErr
}

// EnsureKey 预检密钥可用性（app 启动时快速失败用）。
func EnsureKey(dataDir string) error {
	_, err := loadKey(dataDir)
	return err
}

// InspectKeyFile 检查 dataDir 下 secret.key 的落盘格式（诊断/引导页用）。
func InspectKeyFile(dataDir string) string {
	b, err := os.ReadFile(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return KeyFormatMissing
	}
	if isEnvelope(b) {
		return KeyFormatEnvelope
	}
	if len(b) == 32 {
		return KeyFormatRaw
	}
	return "invalid"
}

// loadOrCreateKey 取密钥：env(hex) > 注入 > secret.key（裸 32 字节 / 信封）> 自动生成。
// 信封存在但无注入密钥时返回错误（绝不重新生成覆盖，避免凭据全解不开）。
func loadOrCreateKey(dataDir string) ([]byte, error) {
	if env := os.Getenv("CPH_SECRET_KEY"); env != "" {
		if key, err := hex.DecodeString(env); err == nil && len(key) == 32 {
			return key, nil
		}
		// 非 hex 的按原始字节取（凑满 32 字节即可用）
		if len(env) == 32 {
			return []byte(env), nil
		}
		logsink.Warnf("[crypto] CPH_SECRET_KEY 非法（须为 hex 32 字节），忽略")
	}
	if len(injectedKey) == 32 {
		return injectedKey, nil
	}
	keyPath := filepath.Join(dataDir, "secret.key")
	if b, err := os.ReadFile(keyPath); err == nil {
		if isEnvelope(b) {
			return nil, errors.New(
				"secret.key 为 Keystore 信封格式但未注入密钥（Kotlin 侧须解封后经 SetInjectedKey/SecretKeyHex 提供）")
		}
		if len(b) == 32 {
			return b, nil // 桌面明文 32 字节（备份换入 / 桌面格式落盘）：原样可用
		}
		logsink.Warnf("[crypto] secret.key 长度 %d 非法（须 32 字节），重新生成", len(b))
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate secret key: %w", err)
	}
	_ = os.MkdirAll(dataDir, 0o700)
	// 注意：落盘为桌面兼容的裸 32 字节；安卓侧 Kotlin 在首启后可将其换入 Keystore 信封
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		return nil, fmt.Errorf("write secret.key: %w", err)
	}
	return key, nil
}

// isEnvelope 判断是否信封格式（首行头部匹配）。
func isEnvelope(b []byte) bool {
	first := b
	if i := strings.IndexByte(string(b), '\n'); i >= 0 {
		first = b[:i]
	}
	return strings.TrimSpace(string(first)) == keyEnvelopeHeader
}

// WrapEnvelope 构造信封格式文件内容（Kotlin 侧使用：把 Keystore 包裹的密钥块编码落盘）。
// blob 为 Keystore 加密后的任意字节（Go 不解释其内容）。
func WrapEnvelope(blob []byte) []byte {
	return []byte(keyEnvelopeHeader + "\n" + base64.StdEncoding.EncodeToString(blob) + "\n")
}

// EncryptCredential 加密凭据 blob；未初始化加密器时原样返回（并记错误日志）。
func EncryptCredential(dataDir string, blob []byte) []byte {
	gcm, err := loadKey(dataDir)
	if err != nil || len(blob) == 0 {
		if err != nil {
			logsink.Errorf("[crypto] 加密不可用，凭据按明文存储: %v", err)
		}
		return blob
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return blob
	}
	sealed := gcm.Seal(nil, nonce, blob, nil)
	return append([]byte{encPrefix}, append(nonce, sealed...)...)
}

// DecryptCredential 解密凭据 blob；非加密格式（无 0x01 前缀）按明文返回。
func DecryptCredential(dataDir string, data []byte) []byte {
	if len(data) == 0 || data[0] != encPrefix {
		return data
	}
	gcm, err := loadKey(dataDir)
	if err != nil {
		logsink.Errorf("[crypto] 解密不可用: %v", err)
		return data
	}
	nonceSize := gcm.NonceSize()
	if len(data) < 1+nonceSize+gcm.Overhead() {
		return data
	}
	nonce, sealed := data[1:1+nonceSize], data[1+nonceSize:]
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return data // 解不开（密钥不匹配的存量）按原样返回，交由插件报凭据错误
	}
	return plain
}
