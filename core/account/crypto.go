// crypto.go — 主密钥可靠落盘后才启用加密，损坏的加密记录必须报错（随上游 v1.5.2
// f49335e 语义：加解密错误不再静默回退明文）。密钥来源（按序）：CPH_SECRET_KEY（hex）
// > 运行时注入（安卓 Keystore 解封，SetInjectedKey）> <data>/secret.key 落盘密钥
// （双格式兼容，见下）> 自动生成并落盘（临时文件 + 硬链接原子发布）。
//
// secret.key 落盘格式（NexPort fork 双格式兼容，基于 ClawProxyHub（AGPL-3.0）修改构建）：
//   - 桌面格式：32 字节裸密钥（无头）。恢复桌面备份（ApplyPendingRestore 换入 secret.key）
//     后即为此格式，必须原样可用；
//   - 信封格式（安卓）：首行 ASCII 头 "NXPORT-KEY-ENVELOPE v1"，其后为 base64(
//     Android Keystore 包裹的密钥块)。Go 无法解开 Keystore——Kotlin 侧解封后经
//     SetInjectedKey（或 bridge Config.SecretKeyHex）注入；信封存在但未注入时
//     明确报错，绝不静默重新生成（否则桌面备份凭据全解不开）。
// 加密数据以 0x01 前缀标记。
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

// keyMu 保护 ciphers 缓存与 injectedKey；ciphers 按 dataDir 绝对路径 + 环境变量
// 缓存（多 dataDir 场景，如备份恢复预览），SetInjectedKey 时整体失效。
var (
	keyMu       sync.Mutex
	ciphers     = map[string]cipher.AEAD{}
	injectedKey []byte
)

// SetInjectedKey 注入运行时密钥（安卓侧由 Keystore 解封后传入，32 字节）。
// 使已缓存的解析结果失效（换 key 后下次加解密重载）。
func SetInjectedKey(key []byte) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if len(key) > 0 && len(key) != 32 {
		logsink.Errorf("[crypto] 注入密钥长度须为 32 字节，实际 %d，忽略本次注入", len(key))
		return
	}
	if len(key) == 32 {
		injectedKey = append([]byte(nil), key...)
	} else {
		injectedKey = nil
	}
	ciphers = map[string]cipher.AEAD{}
}

// InitCrypto 启动时预解析密钥（快速失败：密钥不可用直接报错，不等到首条凭据读写）。
func InitCrypto(dataDir string) error { _, err := loadKey(dataDir); return err }

// EnsureKey 预检密钥可用性（app 启动时快速失败用）。
func EnsureKey(dataDir string) error {
	_, err := loadKey(dataDir)
	return err
}

// BackupKey 导出当前实际密钥，环境变量密钥与文件密钥遵循同一优先级（备份打包用）。
func BackupKey(dataDir string) ([]byte, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	return loadOrCreateKey(dataDir)
}

// loadKey 初始化加密器（按 dataDir 缓存；SetInjectedKey 使其整体失效重载）。
func loadKey(dataDir string) (cipher.AEAD, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	path, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	cacheKey := path + "\x00" + os.Getenv("CPH_SECRET_KEY")
	if c := ciphers[cacheKey]; c != nil {
		return c, nil
	}
	key, err := loadOrCreateKey(dataDir)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err == nil {
		ciphers[cacheKey] = gcm
	}
	return gcm, err
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
// 非法配置报错而非静默回退；信封存在但无注入密钥时返回错误（绝不重新生成覆盖，
// 避免凭据全解不开）。
func loadOrCreateKey(dataDir string) ([]byte, error) {
	if env := os.Getenv("CPH_SECRET_KEY"); env != "" {
		if key, err := hex.DecodeString(env); err == nil && len(key) == 32 {
			return key, nil
		}
		// 非 hex 的按原始字节取（凑满 32 字节即可用）
		if len(env) == 32 {
			return []byte(env), nil
		}
		return nil, errors.New("CPH_SECRET_KEY 非法（须为 hex 或 32 字节）")
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
		return nil, fmt.Errorf("secret.key 长度 %d 非法（须 32 字节）", len(b))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate secret key: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	// 临时文件 + fsync + 硬链接原子发布（随上游 f49335e）：并发首启不会互相覆盖半截文件。
	tmp, err := os.CreateTemp(dataDir, ".secret-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, err
	}
	if _, err := tmp.Write(key); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(tmp.Name(), keyPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, readErr := os.ReadFile(keyPath)
			if readErr != nil {
				return nil, readErr
			}
			if len(existing) != 32 {
				return nil, fmt.Errorf("secret.key 长度 %d 非法（须 32 字节）", len(existing))
			}
			return existing, nil
		}
		// 安卓 data 目录（f2fs/sdcardfs 部分内核）可能不支持硬链接：回退直接写。
		if werr := os.WriteFile(keyPath, key, 0o600); werr != nil {
			return nil, fmt.Errorf("persist secret.key: %w", werr)
		}
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

// EncryptCredential 加密凭据 blob；加密器不可用时返回错误（不再静默明文落盘）。
func EncryptCredential(dataDir string, blob []byte) ([]byte, error) {
	if len(blob) == 0 {
		return nil, nil
	}
	gcm, err := loadKey(dataDir)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(append([]byte{encPrefix}, nonce...), nonce, blob, nil), nil
}

// DecryptCredential 解密凭据 blob；非加密格式（无 0x01 前缀）按明文返回；
// 密文损坏/密钥不匹配返回错误（随上游 v1.5.2：错误必须传播，交由调用方报凭据错误）。
func DecryptCredential(dataDir string, data []byte) ([]byte, error) {
	if len(data) == 0 || data[0] != encPrefix {
		return data, nil
	}
	gcm, err := loadKey(dataDir)
	if err != nil {
		return nil, err
	}
	n := gcm.NonceSize()
	if len(data) < 1+n+gcm.Overhead() {
		return nil, errors.New("truncated encrypted credential")
	}
	return gcm.Open(nil, data[1:1+n], data[1+n:], nil)
}
