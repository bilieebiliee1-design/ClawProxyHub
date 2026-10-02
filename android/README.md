# android/ — 对接说明（壳工程本体在 `../app/`）

本目录保留核心侧 → 安卓侧的对接要点。**Kotlin 壳工程的 Gradle 模块已落在仓库根
`app/`**（模块 `:app`，Gradle root = 仓库根 `gateway-mobile/`）——硬性产物位置
`gateway-mobile/app/build/outputs/{apk,bundle}/release/` 决定了模块目录。

## 已实现（:app 模块，包名 io.nexport.gateway）

- 单 Activity（`MainActivity`）+ Fragment 分步可视化引导：Step0 免责声明/EULA（必勾选，
  拒绝即退出）→ Step1 强密码管理员账号（≥10 位含大小写/数字，实时强度条；服务端
  `validateCredentials` 同强度）→ Step2 启动核心（工作线程调 `Bridge.start`，阶段动画 +
  日志上屏，随后 `POST /admin/setup` + 自动登录）→ Step3 指南（可跳过）。
- 主界面：核心状态卡（版本/网关端口/在跑插件）、隧道卡（默认关 + 二次确认 + URL 复制）、
  后台卡（specialUse 前台服务 / 电池优化 / 开机自启默认关）、「免责声明」「关于与许可」
  固定入口、退出即回收。
- 关于页源码提供（AGPL §6/§13）：发行包内随附 `assets/nexport-source.zip`（本 fork
  完整对应源码，`tools/make-source-zip.py` 于 prepare-android.sh 生成；排除
  keystore/构建产物/二进制），关于页一键导出；`BuildConfig.FORK_SOURCE_URL` 为
  仓库公开后的辅助通道（当前为空，关于页如实显示提示，不留占位链接）。
- `PanelActivity`（WebView）：仅加载 `http://127.0.0.1:<GatewayPort>/`；禁 file 访问、
  无 addJavascriptInterface；两桥齐备 —— `onShowFileChooser`（离线上传/备份导入）与
  `DownloadListener`（备份导出 zip、日志 CSV 经 SAF；面板 blob 导出经一次性 JS 垫片
  + 单向 evaluateJavascript 取回，见类注释）；外部链接 Custom Tabs。
- `GatewayService`：specialUse 单路 FGS（Android 15 dataSync 6h 配额规避），通知展示
  隧道 URL + 一键断开；`BootReceiver` 开机自启（默认关，specialUse 可由 BOOT_COMPLETED
  拉起）。
- `KeyEnvelope`：secret.key 双格式兼容的安卓侧实现 —— 裸 32 字节（桌面格式）自动换入
  Android Keystore（StrongBox 优先）信封（首行 `NXPORT-KEY-ENVELOPE v1`，与
  core/account/crypto.go 协议对齐），解封后经 `Config.SecretKeyHex` 注入；解封失败明确
  报错绝不重生成（保住桌面备份凭据）。备份恢复闭环走 `CoreController.restart()` =
  完整 Stop+Start（重新 prepareInjection 注入换入后的新密钥——`Bridge.restart()` 会
  保留旧注入密钥并遮蔽恢复文件，见 crypto.go loadOrCreateKey 优先级）。已知限制：
  恢复「另一台安卓设备」的本应用备份（其 secret.key 为对方 Keystore 信封）无法在本机
  解封而报错——桌面备份（裸 32 字节）不受影响。

## 核心构件（core/dist/，经 tools/prepare-android.sh 落位）

| 构件 | 落位 |
| --- | --- |
| `gateway-core.aar` 的 classes.jar + jni/<abi>/libgojni.so | `app/libs/gateway-core-classes.jar` + `app/src/main/jniLibs/<abi>/libgojni.so` |
| `libluahost-android-{arm64,x64}.so` | `app/src/main/jniLibs/<abi>/libluahost.so` |
| `cloudflared-android-{arm64,x64}` | `app/src/main/jniLibs/<abi>/libcloudflared.so` |
| LICENSE / THIRD_PARTY_NOTICES.md | `app/src/main/assets/`（关于页查看，AGPL §13） |

说明：:app 以「提取内容」方式消费 AAR（本地 .aar 的 files() 导入在 bundle/AAB 场景
不可靠，且自持 jniLibs 才能精确控制 useLegacyPackaging 与 ABI 目录名）；AAR 本体仍是
规范产物（core/dist/gateway-core.aar）。

## 构建与产物

```bash
bash tools/build-android.sh   # prepare + :app:assembleRelease :app:bundleRelease
```

- 硬性产物：`app/build/outputs/apk/release/` 下 `app-arm64-v8a-release.apk`、
  `app-x86_64-release.apk`、`app-universal-release.apk`（ABI 拆分，
  `splits.abi { include("arm64-v8a", "x86_64"); isUniversalApk = true }`）与
  `app/build/outputs/bundle/release/app-release.aab`（AGP 9.4.0 / Gradle 9.6.1 /
  JDK 21 / compileSdk 36 / minSdk 26 / targetSdk 35，ABI arm64-v8a + x86_64）。
  发行口径：Release 附 arm64 专用包（面向手机用户）+ universal 兜底；
  x86_64 专用包仅供模拟器/开发调试。**仅支持 64 位设备**——不提供 armeabi-v7a
  （32 位）产物，32 位设备安装报 `INSTALL_FAILED_NO_MATCHING_ABIS` 属预期排除
  （见 dist/RELEASE_NOTES 系统要求）。
- `packaging { jniLibs { useLegacyPackaging = true } }`（manifest extractNativeLibs=true
  已实测）——nativeLibraryDir 才有实体 .so，插件子进程 / luahost / cloudflared 三条
  exec 路径依赖它。
- 签名：`keystore/nexport-release.jks`（alias `nexport`），密码在
  `keystore/keystore.properties`（本地文件，勿入库）；缺失时 release 不签名。
- Bridge 线程约束（Java→Go 同步阻塞；Go→Kotlin 回调在 Go 线程）已封装在
  `CoreController`（工作线程调用 + 主线程分发），UI 层不直接触碰 bridge。
