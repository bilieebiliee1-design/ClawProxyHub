// builtin.go — 内置 Go 插件注册表（NexPort fork 新增，安卓化方案 ①）。
//
// 背景：targetSdk 29+ 应用数据目录 noexec，Go 插件在安卓上无法落盘安装/热更新，
// 唯一合规可执行位置是 nativeLibraryDir（APK jniLibs 解出，签名保护）。
// 因此官方 Go 插件随 APK 预打包为 libplugin_<名>_<abi>.so（jniLibs 规范命名，
// GOARCH 口径 arm64/x86_64；v1.2.0 旧命名 libplugin_<name>.so 仍兜底识别），
// 本注册表让核心"认识"它们：
//
//   - 描述随核心内嵌（manifest.json + icon.png，由 tools/build-plugins.sh 与
//     二进制同源生成，版本必然一致）；
//   - EnsureBuiltinPlugins 在安卓上按 nativeLibraryDir 实际存在的 .so 落盘插件目录，
//     使 Scan / Installed / 市场安装状态 / 开机自启全部走既有链路（磁盘即真相）；
//   - InstallZip 安卓安装 Go 插件时：内置的映射到内置二进制（包内二进制一律忽略），
//     非内置的给出可读原因拒绝（而非模糊的 missing binary）。
//
// 桌面形态（GOOS != android）本注册表不参与：数据目录可执行，市场安装照旧。
package plugmgr

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"io.nexport.gateway/core/logsink"
)

//go:embed all:builtin
var builtinFS embed.FS

// BuiltinInfo 一个内置 Go 插件的静态描述（读自内嵌 manifest.json）。
type BuiltinInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Author  string `json:"author"`
}

// builtinEntry 内嵌资产：manifest 原文 + 图标字节（icon 可缺省）。
type builtinEntry struct {
	BuiltinInfo
	manifest []byte
	icon     []byte
}

// builtinRegistry 名称 → 条目（init 构建一次；builtin/ 目录由构建脚本同步自插件仓库）。
var builtinRegistry = func() map[string]builtinEntry {
	out := map[string]builtinEntry{}
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		mf, err := fs.ReadFile(builtinFS, "builtin/"+name+"/manifest.json")
		if err != nil {
			continue
		}
		var info BuiltinInfo
		if json.Unmarshal(mf, &info) != nil || info.Name == "" {
			continue
		}
		icon, _ := fs.ReadFile(builtinFS, "builtin/"+name+"/icon.png")
		out[info.Name] = builtinEntry{BuiltinInfo: info, manifest: mf, icon: icon}
	}
	return out
}()

// BuiltinPlugins 内置 Go 插件清单（按名称排序；供错误提示 / 市场标注使用）。
func BuiltinPlugins() []BuiltinInfo {
	out := make([]BuiltinInfo, 0, len(builtinRegistry))
	for _, e := range builtinRegistry {
		out = append(out, e.BuiltinInfo)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// BuiltinVersion 内置插件版本（非内置返回空）。
func BuiltinVersion(name string) string {
	if e, ok := builtinRegistry[name]; ok {
		return e.Version
	}
	return ""
}

// builtinABITag jniLibs 文件名的 ABI 段（GOARCH 口径，与 build-plugins.sh 产物
// libplugin_<名>_<abi>.so 命名一致）：arm64→arm64、amd64→x86_64；其余架构原样。
func builtinABITag() string {
	switch runtimeArch() {
	case "arm64":
		return "arm64"
	case "amd64":
		return "x86_64"
	default:
		return runtimeArch()
	}
}

// builtinLibName 当前 ABI 下的内置插件 .so 文件名（jniLibs 规范 libplugin_<名>_<abi>.so）。
// 双 ABI 共装一个 APK 时按文件名区分 ABI；安装期系统只解出匹配 ABI 的库，
// nativeLibraryDir 内文件名与 APK 内一致，运行期按 GOARCH 推导回查。
func builtinLibName(name string) string {
	return "libplugin_" + name + "_" + builtinABITag() + ".so"
}

// builtinBinaryCandidates 内置二进制查找顺序：新命名 libplugin_<名>_<abi>.so 优先，
// 旧命名 libplugin_<name>.so 兜底（v1.2.0 首个内置版本产物，升级覆盖期兼容）。
func builtinBinaryCandidates(nativeLibDir, name string) []string {
	return []string{
		filepath.Join(nativeLibDir, builtinLibName(name)),
		filepath.Join(nativeLibDir, "libplugin_"+name+".so"),
	}
}

// builtinBinaryPath 内置插件在 nativeLibraryDir 下的二进制路径（存在的那个；
// 都不存在时返回新命名路径，用于错误文案）。
func builtinBinaryPath(nativeLibDir, name string) string {
	for _, p := range builtinBinaryCandidates(nativeLibDir, name) {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return filepath.Join(nativeLibDir, builtinLibName(name))
}

// AndroidGoInstallCheck 安卓端市场安装 Go 插件的前置校验：非内置 / 内置二进制缺失
// 返回用户可读原因。供 adminapi 在下载之前拒绝（市场按钮状态渲染与 install-market
// 前置拒绝共用同一判定，杜绝"下载完成后才被拒"）；非安卓运行时恒返回 nil（桌面
// 数据目录可执行，在线安装照旧，由 InstallZip 内平台分支把关）。
func (m *Manager) AndroidGoInstallCheck(name string) error {
	if !isAndroidRuntime() {
		return nil
	}
	return m.builtinInstallCheck(name)
}

// builtinTombstoneDir 卸载墓碑目录（<插件根>/.builtin-uninstalled/<名>）。
// 卸载内置插件后写墓碑：此后 EnsureBuiltinPlugins 不再落盘该插件（否则下次核心启动
// 会复活并自启，违背卸载意图）；市场重装同名插件时清除墓碑恢复内置可用性。
// 子目录形式：pluginDirs 只认含 manifest.json 的目录，墓碑目录不会被扫成插件。
func (m *Manager) builtinTombstoneDir() string {
	return filepath.Join(m.dir, ".builtin-uninstalled")
}

// builtinUninstalled 插件是否已被用户卸载（墓碑在位）。
func (m *Manager) builtinUninstalled(name string) bool {
	if !validPluginName(name) {
		return false
	}
	st, err := os.Stat(filepath.Join(m.builtinTombstoneDir(), name))
	return err == nil && !st.IsDir()
}

// markBuiltinUninstalled 写卸载墓碑（幂等）。
func (m *Manager) markBuiltinUninstalled(name string) {
	if !validPluginName(name) {
		return
	}
	_ = os.MkdirAll(m.builtinTombstoneDir(), 0o755)
	_ = os.WriteFile(filepath.Join(m.builtinTombstoneDir(), name), []byte("uninstalled"), 0o644)
}

// clearBuiltinUninstalled 清除卸载墓碑（重装恢复）。
func (m *Manager) clearBuiltinUninstalled(name string) {
	if !validPluginName(name) {
		return
	}
	_ = os.Remove(filepath.Join(m.builtinTombstoneDir(), name))
}

// builtinBinaryExists 内置二进制是否已在 nativeLibraryDir 就位。
func builtinBinaryExists(nativeLibDir, name string) bool {
	if nativeLibDir == "" {
		return false
	}
	st, err := os.Stat(builtinBinaryPath(nativeLibDir, name))
	return err == nil && !st.IsDir()
}

// EnsureBuiltinPlugins 安卓端内置插件落盘：nativeLibraryDir 里存在的内置二进制，
// 对应插件目录缺失（首启 / 卸载后重装 APK）时写出 manifest.json + icon.png。
// 磁盘已有目录时一律不动（市场安装过的 manifest 以落盘为准；卸载是用户明确意图，
// 不因重启核心复活——重装 APK 才恢复可启动状态）。
// 桌面 / 未注入 nativeLibDir 时为空操作。
func (m *Manager) EnsureBuiltinPlugins() {
	if !isAndroidRuntime() {
		return
	}
	m.ensureBuiltinsIn(m.nativeLib())
}

// ensureBuiltinsIn 落盘实现（GOOS 无关，便于测试；nl 为空直接返回）。
func (m *Manager) ensureBuiltinsIn(nl string) {
	if nl == "" {
		return
	}
	for name, e := range builtinRegistry {
		if !builtinBinaryExists(nl, name) {
			continue // APK 未打包该插件（裁剪包场景）：不落盘、不展示
		}
		if m.builtinUninstalled(name) {
			continue // 用户明确卸载过：不复活（重装 APK 或市场重装才恢复）
		}
		if _, ok := m.pluginDir(name); ok {
			continue
		}
		target := filepath.Join(m.dir, name)
		if err := os.MkdirAll(target, 0o755); err != nil {
			logsink.Warnf("[plugin] 内置插件 %s 目录创建失败: %v", name, err)
			continue
		}
		if err := os.WriteFile(filepath.Join(target, "manifest.json"), e.manifest, 0o644); err != nil {
			logsink.Warnf("[plugin] 内置插件 %s manifest 写入失败: %v", name, err)
			continue
		}
		if len(e.icon) > 0 {
			_ = os.WriteFile(filepath.Join(target, "icon.png"), e.icon, 0o644)
		}
		logsink.Printf("[plugin] 内置插件就位: %s v%s（nativeLibraryDir/%s）", name, e.Version, builtinLibName(name))
	}
}

// builtinInstallCheck 安卓安装 Go 插件 .cphplugin 包时的内置校验。
// 返回 nil 表示可安装（内置且二进制在位）；非 nil 为用户可读的拒绝原因。
func (m *Manager) builtinInstallCheck(manifestName string) error {
	if _, ok := builtinRegistry[manifestName]; !ok {
		var names []string
		for _, b := range BuiltinPlugins() {
			names = append(names, b.Name+" v"+b.Version)
		}
		return fmt.Errorf("安卓端无法安装 Go 插件 %q：该插件未随 APK 内置。安卓应用数据目录禁止执行二进制（targetSdk 29+ noexec），Go 插件只能随 APK 预打包，无法在线安装；Lua 脚本插件不受限。当前内置 Go 插件：%s",
			manifestName, strings.Join(names, "、"))
	}
	if !builtinBinaryExists(m.nativeLib(), manifestName) {
		return fmt.Errorf("内置插件 %s 的二进制缺失（nativeLibraryDir 无 libplugin_%s_%s.so，APK 打包不完整，请反馈）", manifestName, manifestName, builtinABITag())
	}
	return nil
}

// builtinVersionMismatch 内置二进制版本 ≠ 安装包版本的说明文案（空 = 一致）。
func builtinVersionMismatch(manifestName, packageVersion string) string {
	bv := BuiltinVersion(manifestName)
	if bv == "" || bv == packageVersion {
		return ""
	}
	return fmt.Sprintf("插件 %s 市场包版本 v%s 与内置二进制 v%s 不一致：安卓端实际以内置二进制 v%s 运行（数据目录不可执行，Go 插件二进制只能随 APK 更新），运行中版本以握手回报为准",
		manifestName, packageVersion, bv, bv)
}

// ReinstallBuiltin 内置插件一键本地重装（市场「已内置，未安装」的重装按钮）：
// 清卸载墓碑 → 重新落盘 manifest/icon（目录缺失时）→ 启动并恢复自启。
// 不走市场下载：二进制直接来自 APK nativeLibraryDir（签名保护，无网络依赖）。
// 非内置插件 / 非安卓运行时 / 二进制缺失返回错误（adminapi 映射 400）。
func (m *Manager) ReinstallBuiltin(ctx context.Context, name string) (*Instance, error) {
	if _, ok := builtinRegistry[name]; !ok {
		return nil, fmt.Errorf("插件 %s 不是内置 Go 插件，无法本地重装", name)
	}
	if !isAndroidRuntime() {
		return nil, fmt.Errorf("内置插件本地重装仅安卓形态可用（桌面数据目录可执行，请走市场在线安装）")
	}
	if !builtinBinaryExists(m.nativeLib(), name) {
		return nil, fmt.Errorf("内置插件 %s 二进制缺失（nativeLibraryDir 无 %s，APK 打包不完整，请反馈）", name, builtinLibName(name))
	}
	m.clearBuiltinUninstalled(name)
	dir, ok := m.pluginDir(name)
	if !ok {
		dir = filepath.Join(m.dir, name)
		e := builtinRegistry[name]
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create plugin dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), e.manifest, 0o644); err != nil {
			return nil, fmt.Errorf("write manifest: %w", err)
		}
		if len(e.icon) > 0 {
			_ = os.WriteFile(filepath.Join(dir, "icon.png"), e.icon, 0o644)
		}
		logsink.Printf("[plugin] 内置插件重装就位: %s v%s", name, e.Version)
	}
	inst, err := m.Start(ctx, dir)
	if err != nil {
		return nil, err
	}
	m.Resume(name) // 清持久化停止状态：重装即恢复开机自启
	return inst, nil
}
