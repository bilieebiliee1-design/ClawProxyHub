package adminapi

import (
	"encoding/json"
	"strings"
	"testing"

	"io.nexport.gateway/core/plugmgr"
)

func TestOfflineMarketIndex(t *testing.T) {
	var entries []MarketEntry
	if err := json.Unmarshal(offlineMarketJSON, &entries); err != nil || len(entries) == 0 {
		t.Fatalf("offline market broken: %v, len=%d", err, len(entries))
	}
	for _, e := range entries {
		if e.Name == "" || e.DownloadURL == "" {
			t.Fatalf("bad entry: %+v", e)
		}
	}
}

func TestIsGitHubURL(t *testing.T) {
	for _, u := range []string{
		"https://github.com/ShadowSmallBaby/ClawProxyHubPlugins/releases/latest/download/x.cphplugin",
		"https://raw.githubusercontent.com/ShadowSmallBaby/ClawProxyHubPlugins/main/index.json",
		"https://objects.githubusercontent.com/x",
	} {
		if !isGitHubURL(u) {
			t.Fatalf("should be github url: %s", u)
		}
	}
	for _, u := range []string{"https://example.com/x", "http://127.0.0.1:8080/y", "not-a-url"} {
		if isGitHubURL(u) {
			t.Fatalf("should not be github url: %s", u)
		}
	}
}

// buildMarketView 安卓适配标注：Lua 恒可装；内置 Go 标内置版本；非内置 Go 标不支持；
// 桌面（android=false）一律零值（Go 插件照常可装）。
func TestBuildMarketViewAndroidAnnotation(t *testing.T) {
	// 内置注册表实际条目做样本（随核心内嵌，桌面测试也有值）
	builtins := plugmgr.BuiltinPlugins()
	if len(builtins) == 0 {
		t.Fatalf("builtin registry empty: expected embedded builtin plugins")
	}
	builtin := builtins[0]

	entries := []MarketEntry{
		{Name: builtin.Name, Version: "9.9.9", Author: "cph"},               // 内置 Go（市场版本更新，也不应出可升级语义）
		{Name: "clinea", Version: "0.1.2", Author: "cph"},                   // 非内置 Go
		{Name: "autoclaw", Version: "0.1.0", Author: "cph", Runtime: "lua"}, // Lua
		{Name: builtin.Name + "x", Version: "0.1.0", Author: "cph"},         // 同前缀非内置 Go（防前缀误匹配）
	}
	local := map[string]string{"cph/" + builtin.Name: builtin.Version}

	// 桌面：适配字段零值，installed/updatable 逻辑不变
	for _, v := range buildMarketView(entries, local, false) {
		if v.AndroidBuiltin || v.AndroidSupported || v.AndroidVersion != "" {
			t.Fatalf("desktop view must not carry android annotation: %+v", v)
		}
	}
	desktop := buildMarketView(entries, local, false)
	if !desktop[0].Installed || !desktop[0].Updatable {
		t.Fatalf("desktop installed/updatable broken: %+v", desktop[0])
	}

	// 安卓：Lua 可装；内置 Go 标内置版本（市场版本再新也只标内置）；非内置 Go 不可装
	av := buildMarketView(entries, local, true)
	if av[0].AndroidBuiltin != true || av[0].AndroidVersion != builtin.Version || av[0].AndroidSupported != true {
		t.Fatalf("builtin go annotation wrong: %+v (want builtin=%s)", av[0], builtin.Version)
	}
	if av[0].AndroidVersion == entries[0].Version {
		t.Fatalf("android_version must be builtin version, not market version: %+v", av[0])
	}
	if av[1].AndroidBuiltin || av[1].AndroidSupported || av[1].AndroidVersion != "" {
		t.Fatalf("non-builtin go must be unsupported: %+v", av[1])
	}
	if av[2].Runtime != "lua" || !av[2].AndroidSupported || av[2].AndroidBuiltin {
		t.Fatalf("lua annotation wrong: %+v", av[2])
	}
	if av[3].AndroidBuiltin || av[3].AndroidSupported {
		t.Fatalf("prefix lookalike must not match builtin registry: %+v", av[3])
	}

	// JSON 形状：安卓标注字段名与前端消费一致
	raw, err := json.Marshal(av[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"android_builtin":true`, `"android_supported":true`, `"android_version":"` + builtin.Version + `"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("json missing %s: %s", key, raw)
		}
	}
}
