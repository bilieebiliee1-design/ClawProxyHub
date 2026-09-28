// builtin_test.go — 内置 Go 插件注册表回归（安卓化方案 ①）。
package plugmgr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuiltinRegistryAssets 内嵌清单与二进制版本同源：官方源 index 全量 10 个插件必须带 manifest 与 icon。
func TestBuiltinRegistryAssets(t *testing.T) {
	want := map[string]string{
		"cline": "0.1.5", "commandcode": "0.1.2", "ima": "0.1.3",
		"lobsterai": "0.1.7", "mirasim": "0.1.1", "newapi": "0.1.4",
		"opencode": "0.1.4", "todofor": "0.1.1", "workbuddy": "0.1.7", "zcode": "0.1.1",
	}
	got := map[string]string{}
	for _, b := range BuiltinPlugins() {
		got[b.Name] = b.Version
		if BuiltinVersion(b.Name) != b.Version {
			t.Fatalf("BuiltinVersion(%s) = %q, want %q", b.Name, BuiltinVersion(b.Name), b.Version)
		}
		e, ok := builtinRegistry[b.Name]
		if !ok || len(e.manifest) == 0 {
			t.Fatalf("builtin %s: manifest 缺失", b.Name)
		}
		if len(e.icon) == 0 {
			t.Fatalf("builtin %s: icon.png 缺失", b.Name)
		}
	}
	for name, ver := range want {
		if got[name] != ver {
			t.Fatalf("builtin %s 版本 = %q, want %q（与构建脚本 / ClawProxyHubPlugins 发布 tag 同源）", name, got[name], ver)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("内置插件数 = %d, want %d: %v", len(got), len(want), got)
	}
}

// TestEnsureBuiltinsIn nativeLibraryDir 有的插件落盘 manifest+icon；目录已存在则不动；没有二进制的不落盘。
func TestEnsureBuiltinsIn(t *testing.T) {
	nl := t.TempDir()
	// 只有 lobsterai 打进了 APK（模拟裁剪包；文件名按 GOARCH 口径 ABI 段）
	if err := os.WriteFile(filepath.Join(nl, builtinLibName("lobsterai")), []byte("elf"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// 已存在同名目录（含用户状态）→ 不覆盖
	pre := filepath.Join(dir, "workbuddy")
	os.MkdirAll(pre, 0o755)
	os.WriteFile(filepath.Join(pre, "manifest.json"), []byte(`{"name":"workbuddy","version":"9.9.9"}`), 0o644)

	m := &Manager{dir: dir}
	m.ensureBuiltinsIn(nl)

	// lobsterai：落盘且版本/图标与内嵌一致
	mf, err := os.ReadFile(filepath.Join(dir, "lobsterai", "manifest.json"))
	if err != nil {
		t.Fatalf("lobsterai manifest 未落盘: %v", err)
	}
	if !strings.Contains(string(mf), `"version": "0.1.7"`) {
		t.Fatalf("lobsterai manifest 版本不符: %s", mf)
	}
	if _, err := os.Stat(filepath.Join(dir, "lobsterai", "icon.png")); err != nil {
		t.Fatalf("lobsterai icon 未落盘: %v", err)
	}
	// workbuddy：目录已存在 → 保留用户状态
	b, _ := os.ReadFile(filepath.Join(pre, "manifest.json"))
	if !strings.Contains(string(b), "9.9.9") {
		t.Fatalf("已存在目录被覆盖: %s", b)
	}
	// newapi：APK 无二进制 → 不落盘
	if _, err := os.Stat(filepath.Join(dir, "newapi")); !os.IsNotExist(err) {
		t.Fatalf("newapi 不应有目录（APK 未打包其二进制）")
	}
	// 空目录 / 幂等重放
	m.ensureBuiltinsIn("")
	m.ensureBuiltinsIn(nl) // 第二次全部已存在 → 不报错不改写
}

// TestBuiltinTombstone 卸载墓碑：卸载后不复活，重装清除后恢复落盘。
func TestBuiltinTombstone(t *testing.T) {
	nl := t.TempDir()
	if err := os.WriteFile(filepath.Join(nl, builtinLibName("lobsterai")), []byte("elf"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m := &Manager{dir: dir}

	// 首启落盘
	m.ensureBuiltinsIn(nl)
	if _, err := os.Stat(filepath.Join(dir, "lobsterai", "manifest.json")); err != nil {
		t.Fatalf("首启应落盘: %v", err)
	}
	// 卸载：目录删除 + 写墓碑 → 再落盘不复活
	m.Uninstall("lobsterai")
	if _, err := os.Stat(filepath.Join(dir, "lobsterai")); !os.IsNotExist(err) {
		t.Fatalf("卸载后目录应删除")
	}
	m.ensureBuiltinsIn(nl)
	if _, err := os.Stat(filepath.Join(dir, "lobsterai")); !os.IsNotExist(err) {
		t.Fatalf("墓碑在位时不应复活（下次核心启动）")
	}
	// 市场重装（清墓碑）→ 恢复
	m.clearBuiltinUninstalled("lobsterai")
	m.ensureBuiltinsIn(nl)
	if _, err := os.Stat(filepath.Join(dir, "lobsterai", "manifest.json")); err != nil {
		t.Fatalf("重装后应恢复落盘: %v", err)
	}
	// 非法名防护：../evil 被拒 → 墓碑目录不应存在任何条目
	m.markBuiltinUninstalled("../evil")
	if entries, err := os.ReadDir(filepath.Join(dir, ".builtin-uninstalled")); err == nil && len(entries) > 0 {
		t.Fatalf("路径穿越不应写入墓碑: %v", entries)
	}
}

// TestBuiltinInstallCheck 非内置拒绝（含可装清单）、内置缺二进制拒绝、内置且二进制在位放行。
func TestBuiltinInstallCheck(t *testing.T) {
	nl := t.TempDir()
	if err := os.WriteFile(filepath.Join(nl, builtinLibName("newapi")), []byte("elf"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := &Manager{dir: t.TempDir()}
	m.SetNativeLibDir(nl)

	if err := m.builtinInstallCheck("newapi"); err != nil {
		t.Fatalf("内置且二进制在位应放行: %v", err)
	}
	err := m.builtinInstallCheck("chatjimmy") // 仓库在库但未上官方 index / 未内置
	if err == nil || !strings.Contains(err.Error(), "未随 APK 内置") || !strings.Contains(err.Error(), "lobsterai v0.1.7") {
		t.Fatalf("非内置应拒绝且列出可装清单, got: %v", err)
	}
	m2 := &Manager{dir: t.TempDir()} // 无 nativeLibDir
	if err := m2.builtinInstallCheck("newapi"); err == nil || !strings.Contains(err.Error(), "二进制缺失") {
		t.Fatalf("缺二进制应拒绝, got: %v", err)
	}
}

// TestBuiltinVersionMismatch 版本差异文案；一致 / 非内置为空。
func TestBuiltinVersionMismatch(t *testing.T) {
	if got := builtinVersionMismatch("lobsterai", "0.1.6"); got == "" || !strings.Contains(got, "0.1.6") || !strings.Contains(got, "0.1.7") {
		t.Fatalf("版本差异应给出说明, got: %q", got)
	}
	if got := builtinVersionMismatch("lobsterai", "0.1.7"); got != "" {
		t.Fatalf("同版本应无文案, got: %q", got)
	}
	if got := builtinVersionMismatch("不存在", "1.0"); got != "" {
		t.Fatalf("非内置应无文案, got: %q", got)
	}
}

// TestBuiltinBinaryPathCandidates 内置二进制解析：新命名 libplugin_<名>_<abi>.so 优先，
// 旧命名 libplugin_<名>.so 兜底（v1.2.0 升级覆盖期兼容）。
func TestBuiltinBinaryPathCandidates(t *testing.T) {
	nl := t.TempDir()
	legacy := filepath.Join(nl, "libplugin_lobsterai.so")
	if err := os.WriteFile(legacy, []byte("elf"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 只有旧命名：兜底可用
	if got := builtinBinaryPath(nl, "lobsterai"); got != legacy {
		t.Fatalf("legacy 兜底未生效: %s", got)
	}
	if !builtinBinaryExists(nl, "lobsterai") {
		t.Fatalf("legacy 命名应可被识别")
	}
	// 新旧并存：新命名优先
	suffixed := filepath.Join(nl, builtinLibName("lobsterai"))
	if err := os.WriteFile(suffixed, []byte("elf2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := builtinBinaryPath(nl, "lobsterai"); got != suffixed {
		t.Fatalf("应优先新命名 libplugin_<名>_<abi>.so: %s", got)
	}
	// 文件名必须含 ABI 段（与 build-plugins.sh / prepare-android.sh 命名一致）
	if tag := builtinABITag(); tag != "x86_64" && tag != "arm64" && tag != runtimeArch() {
		t.Fatalf("builtinABITag = %q", tag)
	}
}
