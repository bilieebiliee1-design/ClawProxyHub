# proguard-rules.pro — minifyEnabled=false，规则仅在未来开启 R8 时兜底：
# go.Seq / gomobile 绑定类与核心桥回调不可裁剪。
-keep class go.** { *; }
-keep class io.nexport.gateway.bridge.** { *; }
-dontwarn go.**
