// runtime.go — 平台与进程辅助（manager.go 依赖，拆分以保持单文件聚焦）。
// NexPort fork（基于 ClawProxyHub（AGPL-3.0）修改构建）：新增安卓运行时分支说明——
// GOOS=android 下插件二进制定位见 manager.pluginBinary / luahost.go 的 nativeLibraryDir 通道。
package plugmgr

import (
	"os/exec"
	"runtime"
)

// runtimeOS 返回 go-plugin 二进制命名用的 os 段。
func runtimeOS() string { return runtime.GOOS }

// runtimeArch 返回 go-plugin 二进制命名用的 arch 段。
func runtimeArch() string { return runtime.GOARCH }

// execCommand 构造插件子进程命令（binPath + 可选参数，如 luahost 的 --dir <插件目录>）。
func execCommand(binPath string, args ...string) *exec.Cmd {
	return exec.Command(binPath, args...)
}
