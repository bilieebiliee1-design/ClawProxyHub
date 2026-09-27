// marketplace_builtin_test.go — 市场「已内置，未安装」状态回归（v1.3.0 方案 ④）：
// 内置插件卸载后市场标注可一键本地重装；时区规范化返回时间。
package adminapi

import (
	"testing"
	"time"

	"io.nexport.gateway/core/plugmgr"
)

// TestBuildMarketViewBuiltinReinstallable 内置插件安装状态三态：
// 已安装（installed=true / reinstallable=false）、卸载未装（installed=false / reinstallable=true）、
// 非内置（恒 false）。
func TestBuildMarketViewBuiltinReinstallable(t *testing.T) {
	builtins := plugmgr.BuiltinPlugins()
	if len(builtins) == 0 {
		t.Fatalf("builtin registry empty")
	}
	builtin := builtins[0]
	entries := []MarketEntry{
		{Name: builtin.Name, Version: builtin.Version, Author: "cph"},       // 内置 Go
		{Name: "lua-auto", Version: "9.9.9", Author: "cph", Runtime: "lua"}, // Lua（非内置）
		{Name: "not-builtin", Version: "0.1.0", Author: "cph"},              // 非内置 Go
	}

	// 已安装（磁盘 manifest 在）：installed，不标重装
	installedLocal := map[string]string{"cph/" + builtin.Name: builtin.Version}
	for _, v := range buildMarketView(entries, installedLocal, true) {
		if v.Name == builtin.Name {
			if !v.Installed || v.AndroidReinstallable {
				t.Fatalf("installed builtin: installed=%v reinstallable=%v, want true/false",
					v.Installed, v.AndroidReinstallable)
			}
		}
	}
	// 卸载后（磁盘无 manifest）：未安装 + 标记可本地重装
	uninstalledLocal := map[string]string{}
	for _, v := range buildMarketView(entries, uninstalledLocal, true) {
		if v.Name == builtin.Name {
			if v.Installed || !v.AndroidBuiltin || !v.AndroidReinstallable {
				t.Fatalf("uninstalled builtin: installed=%v builtin=%v reinstallable=%v, want false/true/true",
					v.Installed, v.AndroidBuiltin, v.AndroidReinstallable)
			}
		}
		if v.Runtime == "lua" && v.AndroidReinstallable {
			t.Fatalf("lua plugin should never be reinstallable-marked")
		}
	}
	// 桌面形态：重装标注恒 false（桌面走在线安装）
	for _, v := range buildMarketView(entries, uninstalledLocal, false) {
		if v.AndroidReinstallable {
			t.Fatalf("desktop reinstallable must be false: %s", v.Name)
		}
	}
}

// TestToLocal 时区规范化（方案 ⑤）：In(Local) 不改时刻、只换时区标注——
// UTC 落库的存量记录经规范化后按本地时区序列化。
func TestToLocal(t *testing.T) {
	old := time.Local
	t.Cleanup(func() { time.Local = old })
	time.Local = time.FixedZone("TEST", 8*3600)

	utc := time.Date(2026, 9, 27, 4, 0, 0, 0, time.UTC) // 本机 12:00
	got := toLocal(utc)
	if !got.Equal(utc) {
		t.Fatalf("toLocal changed the instant: %v vs %v", got, utc)
	}
	if got.Format("15:04") != "12:00" {
		t.Fatalf("toLocal wall time = %s, want 12:00（+8 时区展示）", got.Format("15:04"))
	}
	// 指针版：nil 透传
	if toLocalPtr(nil) != nil {
		t.Fatalf("toLocalPtr(nil) should be nil")
	}
	p := utc
	if lp := toLocalPtr(&p); lp == nil || lp.Format("15:04") != "12:00" {
		t.Fatalf("toLocalPtr = %v", lp)
	}
}
