package io.nexport.gateway.app

import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.io.File
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * KeyEnvelope — secret.key 的 Android Keystore 信封加密（NexPort 移植新增）。
 *
 * 基于 ClawProxyHub（AGPL-3.0）修改构建。
 *
 * 双格式协议（与 core/account/crypto.go 严格对齐）：
 *  - 桌面格式：<data>/secret.key 为 32 字节裸密钥（无头）——核心可直接读取，
 *    备份恢复（restore/ 换入桌面备份）即此格式，必须原样可用；
 *  - 信封格式：首行 ASCII 头 "NXPORT-KEY-ENVELOPE v1"，其后为 base64(密钥块)。
 *    密钥块 = IV(12B) + AES-256-GCM(32B 主密钥)。Go 侧不解内容（WrapEnvelope 约定），
 *    解封只能由本类完成，明文密钥经 Bridge.Config.SecretKeyHex 注入核心。
 *
 * 生命周期：首启核心落盘裸 32 字节 → 本类换入信封并注入 hex；
 * 恢复桌面备份后 secret.key 回到裸格式 → 同一逻辑换入。信封存在但 Keystore
 * 解封失败时明确报错（绝不静默重生成，否则桌面备份凭据全解不开）。
 */
object KeyEnvelope {
    private const val KEYSTORE = "AndroidKeyStore"
    private const val MASTER_CORE = "nexport_secret_key_v1"
    private const val MASTER_PREFS = "nexport_prefs_key_v1"
    const val ENVELOPE_HEADER = "NXPORT-KEY-ENVELOPE v1"

    private const val GCM_TAG_BITS = 128
    private const val IV_LEN = 12

    // ---- Keystore 主密钥 ----

    private fun loadOrCreate(alias: String): SecretKey {
        val ks = KeyStore.getInstance(KEYSTORE).apply { load(null) }
        (ks.getKey(alias, null) as? SecretKey)?.let { return it }
        return generate(alias)
    }

    private fun generate(alias: String): SecretKey {
        if (Build.VERSION.SDK_INT >= 28) {
            try {
                return generateSpec(alias, true) // StrongBox 优先（securityPlan ②）
            } catch (_: Exception) {
                // 设备无 StrongBox：降级 TEE
            }
        }
        return generateSpec(alias, false)
    }

    private fun generateSpec(alias: String, strongBox: Boolean): SecretKey {
        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, KEYSTORE)
        val b = KeyGenParameterSpec.Builder(
            alias,
            KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT
        )
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(256)
            .setRandomizedEncryptionRequired(true)
        if (strongBox && Build.VERSION.SDK_INT >= 28) b.setIsStrongBoxBacked(true)
        gen.init(b.build())
        return gen.generateKey()
    }

    private fun encrypt(alias: String, plain: ByteArray): ByteArray {
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.ENCRYPT_MODE, loadOrCreate(alias))
        val iv = c.iv // randomizedEncryptionRequired=true，自动生成 12B IV
        require(iv.size == IV_LEN) { "unexpected GCM IV length ${iv.size}" }
        val ct = c.doFinal(plain)
        return iv + ct
    }

    private fun decrypt(alias: String, blob: ByteArray): ByteArray {
        require(blob.size > IV_LEN) { "envelope blob too short" }
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.DECRYPT_MODE, loadOrCreate(alias), GCMParameterSpec(GCM_TAG_BITS, blob, 0, IV_LEN))
        return c.doFinal(blob, IV_LEN, blob.size - IV_LEN)
    }

    // ---- 对外：偏好凭据 ----

    fun encryptPrefs(plain: ByteArray): String =
        Base64.encodeToString(encrypt(MASTER_PREFS, plain), Base64.NO_WRAP)

    fun decryptPrefs(b64: String): ByteArray? = try {
        decrypt(MASTER_PREFS, Base64.decode(b64, Base64.NO_WRAP))
    } catch (_: Exception) {
        null
    }

    // ---- 对外：secret.key 双格式 ----

    enum class Format { MISSING, RAW, ENVELOPE, INVALID }

    fun inspect(dir: File): Format {
        val f = File(dir, "secret.key")
        if (!f.isFile) return Format.MISSING
        val b = f.readBytes()
        val firstLine = b.takeWhile { it != '\n'.code.toByte() }.toByteArray()
            .toString(Charsets.US_ASCII).trim()
        return when {
            firstLine == ENVELOPE_HEADER -> Format.ENVELOPE
            b.size == 32 -> Format.RAW
            else -> Format.INVALID
        }
    }

    /**
     * 启动前调用：把 secret.key 规整为信封格式并返回注入用 hex。
     *  - MISSING → 返回 null（核心将生成裸密钥；start 成功后 wrapIfRaw 再换入信封）
     *  - RAW     → 立即换入信封，返回其 hex
     *  - ENVELOPE→ 解封，返回 hex；Keystore 失败则抛出（明确失败，不重生成）
     *  - INVALID → 抛出（核心同样会拒绝该格式）
     */
    fun prepareInjection(dir: File): String? {
        return when (val fmt = inspect(dir)) {
            Format.MISSING -> null
            Format.ENVELOPE -> unwrapToHex(dir)
            Format.RAW -> { wrapIfRaw(dir); unwrapToHex(dir) }
            else -> throw IllegalStateException("secret.key 格式非法（$fmt），已中止启动以保护凭据")
        }
    }

    /** 核心首启落盘裸密钥后换入信封（幂等；RAW 才动作）。 */
    fun wrapIfRaw(dir: File): Boolean {
        val f = File(dir, "secret.key")
        if (!f.isFile) return false
        val raw = f.readBytes()
        if (raw.size != 32) return false
        val blob = encrypt(MASTER_CORE, raw)
        val out = (ENVELOPE_HEADER + "\n" +
            Base64.encodeToString(blob, Base64.NO_WRAP) + "\n").toByteArray(Charsets.US_ASCII)
        val tmp = File(dir, "secret.key.tmp")
        tmp.writeBytes(out)
        if (!tmp.renameTo(f)) {
            // Windows/部分文件系统 rename 语义差异兜底
            f.delete()
            if (!tmp.renameTo(f)) { tmp.delete(); return false }
        }
        return true
    }

    /** 解封信封 → 32 字节 hex（核心 SecretKeyHex 注入用）。 */
    fun unwrapToHex(dir: File): String {
        val f = File(dir, "secret.key")
        val text = f.readText(Charsets.US_ASCII)
        val b64 = text.lineSequence().drop(1).firstOrNull()?.trim()
            ?: throw IllegalStateException("secret.key 信封缺少密钥块")
        val key = decrypt(MASTER_CORE, Base64.decode(b64, Base64.NO_WRAP))
        if (key.size != 32) throw IllegalStateException("信封内密钥长度非法：${key.size}")
        return key.joinToString("") { "%02x".format(it) }
    }
}
