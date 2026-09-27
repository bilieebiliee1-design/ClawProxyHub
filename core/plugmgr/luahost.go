// luahost.go — lua 插件的共享 luahost 管理（P1）：桌面版一份 luahost 二进制置于 data/hosts/，
// 各 lua 插件进程以 `--dir <插件目录>` 启动；安卓（NexPort fork）改为使用 APK 内
// jniLibs 打包的 libluahost.so（nativeLibraryDir 只读，targetSdk≥29 下唯一合规可执行位置）。
// 基于 ClawProxyHub（AGPL-3.0）修改构建。
package plugmgr

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// isAndroidRuntime 当前是否运行于安卓（GOOS=android）。
func isAndroidRuntime() bool { return runtime.GOOS == "android" }

// nativeLib 当前注入的 nativeLibraryDir（空 = 未注入/桌面）。
func (m *Manager) nativeLib() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.nativeLibDir
}

// hostsDir 共享 host 二进制目录（data/hosts，与 data/plugins 同级）。安卓不使用。
func (m *Manager) hostsDir() string { return filepath.Join(filepath.Dir(m.dir), "hosts") }

// luahostFile 当前平台共享 luahost 路径。
// 安卓：<nativeLibDir>/libluahost.so（APK jniLibs 解出的实体文件，只读）。
func (m *Manager) luahostFile() string {
	if isAndroidRuntime() {
		return filepath.Join(m.nativeLib(), "libluahost.so")
	}
	name := fmt.Sprintf("luahost-%s-%s", runtimeOS(), runtimeArch())
	if runtimeOS() == "windows" {
		name += ".exe"
	}
	return filepath.Join(m.hostsDir(), name)
}

// luahostManual 是否存在手动上传标记（存在则不被内置字节自动覆盖，保留用户上传版本）。
// 安卓不支持手动替换，恒为 false。
func (m *Manager) luahostManual() bool {
	if isAndroidRuntime() {
		return false
	}
	_, err := os.Stat(filepath.Join(m.hostsDir(), ".manual"))
	return err == nil
}

// ensureLuahost 确保共享 luahost 就位并返回路径。
// 桌面：缺失、或与内置字节 sha256 不一致（且非手动上传）时用内置字节覆盖（P0 升级刷新）；
// 内置为空（未 -tags luahost_embed）且文件也缺失时报错。
// 安卓：nativeLibraryDir 只读——仅校验 libluahost.so 存在（APK 打包侧保证），
// 不写 data/hosts、不嵌入字节（构建不加 luahost_embed tag）。
func (m *Manager) ensureLuahost() (string, error) {
	path := m.luahostFile()
	if isAndroidRuntime() {
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path, nil
		}
		return "", fmt.Errorf("luahost 未随 APK 打包（nativeLibraryDir 缺少 libluahost.so）：%s", path)
	}
	if cur, err := os.ReadFile(path); err == nil {
		if m.luahostManual() || len(luahostBin) == 0 || sha256.Sum256(cur) == sha256.Sum256(luahostBin) {
			return path, nil // 已就位且无需刷新
		}
	}
	if len(luahostBin) == 0 {
		return "", fmt.Errorf("核心未内置 luahost（请以 -tags luahost_embed 构建），且 %s 不存在", path)
	}
	if err := os.MkdirAll(m.hostsDir(), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, luahostBin, 0o755); err != nil {
		return "", fmt.Errorf("write luahost: %w", err)
	}
	return path, nil
}

// manifestRuntimeAt 读插件目录 manifest.json 的 runtime（读不到即空 = Go 插件）。
func manifestRuntimeAt(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return ""
	}
	var mf struct {
		Runtime string `json:"runtime"`
	}
	_ = json.Unmarshal(data, &mf)
	return mf.Runtime
}

// launchable 目录是否可启动：lua → 有共享 luahost（安卓=nativeLibraryDir，桌面=内置/共享），
// 或插件目录自带 luahost（旧式逐插件拷贝）；go → 目录内有当前平台二进制（安卓回查 nativeLibraryDir）。
func (m *Manager) launchable(dir string) bool {
	if manifestRuntimeAt(dir) == "lua" {
		if isAndroidRuntime() {
			return fileExists(m.luahostFile())
		}
		if len(luahostBin) > 0 || fileExists(m.luahostFile()) {
			return true
		}
		_, err := m.pluginBinary(dir)
		return err == nil
	}
	_, err := m.pluginBinary(dir)
	return err == nil
}

// resolveLaunch 由插件目录解析启动命令与插件名：lua 优先共享 luahost + `--dir 插件目录`（P1）。
// 安卓：`libluahost.so --dir <filesDir>/plugins/<ns>/<name>`（dir 即本方法入参，宿主文件目录布局）；
// 桌面无内置/共享时回退插件目录自带的 luahost（pack -install 旧式）；go → 目录内二进制。
func (m *Manager) resolveLaunch(dir string) (string, *exec.Cmd, error) {
	name := filepath.Base(dir)
	if manifestRuntimeAt(dir) == "lua" {
		if isAndroidRuntime() {
			host, err := m.ensureLuahost()
			if err != nil {
				return "", nil, err
			}
			return name, execCommand(host, "--dir", dir), nil
		}
		if len(luahostBin) > 0 || fileExists(m.luahostFile()) {
			host, err := m.ensureLuahost()
			if err != nil {
				return "", nil, err
			}
			return name, execCommand(host, "--dir", dir), nil
		}
		if bin, err := m.pluginBinary(dir); err == nil {
			return name, execCommand(bin), nil // 旧式：luahost 读自身所在目录
		}
		return "", nil, fmt.Errorf("lua 插件 %s 无可用 luahost（内置/共享/自带均缺）", name)
	}
	bin, err := m.pluginBinary(dir)
	if err != nil {
		return "", nil, err
	}
	return name, execCommand(bin), nil
}

// ReplaceLuahost 手动替换共享 luahost（仅桌面）：先停在跑的 lua 插件（释放文件锁），
// 覆盖二进制并打 .manual 标记（此后不被内置字节自动刷新），再拉起原先在跑的插件。
// 安卓：nativeLibraryDir 只读（APK 签名保护），不支持替换——明确报错。
func (m *Manager) ReplaceLuahost(data []byte) error {
	if isAndroidRuntime() {
		return fmt.Errorf("安卓端 luahost 随 APK 打包（nativeLibraryDir 只读），不支持手动替换")
	}
	if len(data) == 0 {
		return fmt.Errorf("空的 luahost 二进制")
	}
	var toRestart []string
	for _, dir := range m.pluginDirs() {
		if manifestRuntimeAt(dir) != "lua" {
			continue
		}
		name := filepath.Base(dir)
		if _, running := m.Get(name); running {
			m.Stop(name, false) // 临时停（非持久），换完拉起
			toRestart = append(toRestart, dir)
		}
	}
	if err := os.MkdirAll(m.hostsDir(), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(m.luahostFile(), data, 0o755); err != nil {
		return fmt.Errorf("write luahost: %w", err)
	}
	_ = os.WriteFile(filepath.Join(m.hostsDir(), ".manual"), []byte("manual"), 0o644)
	for _, dir := range toRestart {
		_, _ = m.Start(context.Background(), dir)
	}
	return nil
}

// fileExists 报告路径是否存在（文件/目录皆可）。
func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
