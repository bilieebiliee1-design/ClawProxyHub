// app/build.gradle.kts — NexPort 安卓壳（Kotlin + WebView 混合形态）。
// 基于 ClawProxyHub（AGPL-3.0）修改构建。
//
// 硬性构建项（对应架构方案 buildPlan）：
//   ① packaging { jniLibs { useLegacyPackaging = true } } —— 等效 extractNativeLibs=true：
//      .so 安装期解入 nativeLibraryDir，插件子进程 / luahost / cloudflared 三条 exec
//      路径才拿得到实体文件（targetSdk≥29 W^X 下唯一合规可执行位置）。
//   ② minSdk 26 / targetSdk 35 / compileSdk 36；ABI：arm64-v8a + x86_64（x86_64 仅供
//      模拟器/开发，见 release 说明）。
//   ③ 核心：libs/gateway-core-classes.jar（由 tools/prepare-android.sh 自
//      core/dist/gateway-core.aar 提取）+ jniLibs libgojni.so（gomobile bind 产物）。
//   ④ 签名：keystore/keystore.properties（本地文件，不入库）。
import java.util.Properties

plugins {
    id("com.android.application")
}

// 签名配置（keystore/keystore.properties 缺失时 release 不签名，仅 debug 可构建）
val keystoreProps = Properties().apply {
    val f = rootProject.file("keystore/keystore.properties")
    if (f.exists()) f.inputStream().use { load(it) }
}

android {
    namespace = "io.nexport.gateway"
    compileSdk = 36

    defaultConfig {
        applicationId = "io.nexport.gateway"
        minSdk = 26
        targetSdk = 35
        versionCode = 13
        versionName = "1.4.8"
        // AGPL 合规常量：上游项目与 fork 源码链接（关于页固定展示，不可隐藏）
        buildConfigField("String", "UPSTREAM_NAME", "\"ClawProxyHub\"")
        buildConfigField("String", "UPSTREAM_URL", "\"https://github.com/ShadowSmallBaby/ClawProxyHub\"")
        // v1.3.0 ④：随包源码包（nexport-source.zip）自 assets 移除，源码提供改为指向
        // 公开 fork 仓库（EULA「开源与署名」/关于页/使用说明同步更新）
        buildConfigField("String", "FORK_SOURCE_URL", "\"https://github.com/bilieebiliee1-design/ClawProxyHub\"")
    }

    if (keystoreProps.isNotEmpty()) {
        signingConfigs {
            create("release") {
                storeFile = rootProject.file(keystoreProps.getProperty("storeFile"))
                storePassword = keystoreProps.getProperty("storePassword")
                keyAlias = keystoreProps.getProperty("keyAlias")
                keyPassword = keystoreProps.getProperty("keyPassword")
            }
        }
    }

    buildTypes {
        release {
            // minify 关闭：核心 go.Seq/bridge 绑定与 WebView 桥不做裁剪，保真优先
            isMinifyEnabled = false
            isShrinkResources = false
            if (keystoreProps.isNotEmpty()) {
                signingConfig = signingConfigs.getByName("release")
            }
        }
    }

    // ABI 拆分（perf 修复轮）：assembleRelease 产出 arm64-v8a 专用 / x86_64 专用 /
    // universal 三个 APK——x86_64 仅供模拟器/开发调试，双 ABI 全量包让真实用户为
    // 用不到的一半 native 体积买单（25×插件 so + libgojni/libluahost/cloudflared，
    // arm64 主用户群预计减包约 80MB）。发行口径：Release 附 arm64 专用包（面向手机
    // 用户）+ universal 兜底。useLegacyPackaging=true 保持不动（exec 三通道唯一
    // 合规可执行位置 + minSdk 26 的 legacy packaging 前提不变），AAB 不受影响
    // （按安装时 ABI 分发，天然即拆）。
    splits {
        abi {
            isEnable = true
            reset()
            include("arm64-v8a", "x86_64")
            isUniversalApk = true
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    buildFeatures {
        buildConfig = true
    }

    packaging {
        jniLibs {
            // ★ 评审最大遗漏项：现代 AGP 默认 false＝.so 不解压直接从 APK 加载，
            //   nativeLibraryDir 无实体文件，exec 三通道全 ENOENT。必须为 true。
            useLegacyPackaging = true
        }
    }
}

dependencies {
    // Go 核心（gomobile bind 绑定类 + go.Seq 运行时；libgojni.so 在 jniLibs）
    implementation(files("libs/gateway-core-classes.jar"))

    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("com.google.android.material:material:1.12.0")
    implementation("androidx.constraintlayout:constraintlayout:2.1.4")
    implementation("androidx.fragment:fragment-ktx:1.8.2")
    implementation("androidx.viewpager2:viewpager2:1.1.0")
    // 外部链接走 Custom Tabs（WebView 只承载 127.0.0.1 面板）
    implementation("androidx.browser:browser:1.8.0")
    // 面板对比度同步：addDocumentStartJavaScript（document-start 脚本，无 FOUC）
    implementation("androidx.webkit:webkit:1.12.0")
    // 预测性返回：OnBackAnimationCallback（开）vs 即时返回（关）
    implementation("androidx.activity:activity-ktx:1.9.3")
    // Markdown 渲染（markdownSpec）：Markwon 4.6.2 core（Apache-2.0，两份
    // THIRD_PARTY_NOTICES 已登记）。纯 TextView span、无 WebView、无远程图片插件。
    // 坐标为评审实测修正值（io.noties.group），本沙箱经 maven.aliyun.com 镜像解析
    // 实测存在（core-4.6.2.aar，约 130KB，体积影响很小）。
    implementation("io.noties.markwon:core:4.6.2")
    // v1.3.0 ②：表格渲染（THIRD_PARTY_NOTICES 依赖表此前渲染为原始竖线文本）——
    // ext-tables 提供 TablePlugin（传递 org.commonmark:commonmark-ext-gfm-tables，
    // BSD-2-Clause），亮暗主题色经 nx* token 注入 TableTheme。
    implementation("io.noties.markwon:ext-tables:4.6.2")
    // v1.3.0 ①：悬浮窗贴边/弹回 SpringAnimation（material 传递已有 1.0.0，此处显式固定）
    implementation("androidx.dynamicanimation:dynamicanimation:1.0.0")
}
