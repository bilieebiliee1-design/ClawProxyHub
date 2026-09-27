// builtin_reinstall_test.go — 内置插件本地重装守卫回归（v1.3.0 方案 ④）：
// 非内置插件 / 桌面运行时拒绝（安卓全流程需 nativeLibraryDir，模拟器集成验证）。
package plugmgr

import (
	"context"
	"path/filepath"
	"testing"
)

// TestReinstallBuiltinGuards 桌面运行时：非内置拒绝优先于平台守卫；内置在桌面返回
// 平台原因（安卓数据目录 noexec 语义不适用于桌面）。
func TestReinstallBuiltinGuards(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "plugins"), nil)
	// 非内置插件：直接拒绝
	if _, err := m.ReinstallBuiltin(context.Background(), "no-such-plugin"); err == nil {
		t.Fatalf("non-builtin reinstall should fail")
	}
	// 内置插件 + 桌面运行时：拒绝并说明仅安卓可用
	builtins := BuiltinPlugins()
	if len(builtins) == 0 {
		t.Fatalf("builtin registry empty")
	}
	if _, err := m.ReinstallBuiltin(context.Background(), builtins[0].Name); err == nil {
		t.Fatalf("desktop reinstall should fail (android-only)")
	}
}
