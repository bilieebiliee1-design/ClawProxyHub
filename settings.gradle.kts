// settings.gradle.kts — NexPort 安卓壳工程（root = gateway-mobile/，模块 :app）。
// 基于 ClawProxyHub（AGPL-3.0）修改构建。
pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        // Maven Central 镜像（本构建环境对 repo1.maven.org TLS 握手被断，实测
        // aliyun 可达且坐标一致——Markwon 4.6.2 解析验证通过；坐标错误仍会在
        // 首次 sync 即失败，保持构建期门禁语义）
        maven { url = uri("https://maven.aliyun.com/repository/central") }
        mavenCentral()
    }
}

rootProject.name = "NexPort"
include(":app")
