// autocfg_test.go — 自动配置引擎回归（v1.3.0 方案 ②）：分组/路由/默认密钥自动生成、
// 幂等重入、跨插件同名模型确定性消歧。
package adminapi

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"io.nexport.gateway/core/account"
	"io.nexport.gateway/core/database"
	"io.nexport.gateway/core/model"
	"io.nexport.gateway/core/plugmgr"
	"io.nexport.gateway/core/setting"
)

// seedAutocfg 建库 + Server（真 sqlite；插件管理器指到空目录，不启动任何插件）。
func seedAutocfg(t *testing.T) (*Server, *setting.Store) {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(context.Background(), filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
	if err := account.EnsureKey(dir); err != nil {
		t.Fatal(err)
	}
	plugins := plugmgr.NewManager(filepath.Join(dir, "plugins"), db)
	settings := setting.New(db)
	s := New(db, account.New(db, dir, plugins), plugins, nil, settings,
		"https://example.invalid/index.json", dir, filepath.Join(dir, "t.db"))
	return s, settings
}

// addAccount 建插件+实例+账号（ModelsJSON 为账号模型目录快照）；同名插件复用记录。
func addAccount(t *testing.T, s *Server, pluginName, modelsJSON string) int64 {
	t.Helper()
	var p model.Plugin
	if err := s.db.Where("name = ?", pluginName).First(&p).Error; err != nil {
		p = model.Plugin{Name: pluginName, Version: "1", Author: "a", ManifestJSON: "{}"}
		if err := s.db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
	}
	var inst model.Instance
	if err := s.db.Where("plugin_id = ?", p.ID).First(&inst).Error; err != nil {
		inst = model.Instance{PluginID: p.ID, Name: "默认实例"}
		if err := s.db.Create(&inst).Error; err != nil {
			t.Fatal(err)
		}
	}
	acct := model.Account{PluginID: p.ID, InstanceID: inst.ID, DisplayName: pluginName + "-acct", ModelsJSON: modelsJSON}
	if err := s.db.Create(&acct).Error; err != nil {
		t.Fatal(err)
	}
	return acct.ID
}

func countRows(t *testing.T, s *Server, m interface{}, cond string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	if err := s.db.Model(m).Where(cond, args...).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// TestAutoConfigGroupRoutesKey 首次配置：建分组（账号入组）+ 模型原样映射路由 + 默认密钥。
func TestAutoConfigGroupRoutesKey(t *testing.T) {
	s, _ := seedAutocfg(t)
	acctID := addAccount(t, s, "ptest", `[{"id":"model-a","context_window":8000},{"id":"model-b"}]`)

	out := s.autoConfigForAccount("ptest", acctID)
	if len(out.Errors) > 0 {
		t.Fatalf("autoConfig errors: %v", out.Errors)
	}
	// 分组：1 个，名为插件名，账号已入组
	if len(out.Groups) != 1 || out.Groups[0].Name != "ptest" || !out.Groups[0].Created {
		t.Fatalf("groups = %+v, want 1 created 'ptest'", out.Groups)
	}
	if n := countRows(t, s, &model.AccountGroup{}, "group_id = ?", out.Groups[0].ID); n != 1 {
		t.Fatalf("group account links = %d, want 1", n)
	}
	// 路由：模型名原样映射，分组指向本插件分组，权重 100
	if len(out.Created) != 2 {
		t.Fatalf("routes_created = %v, want [model-a model-b]", out.Created)
	}
	for _, name := range out.Created {
		var rt model.Route
		if err := s.db.Where("name = ?", name).First(&rt).Error; err != nil {
			t.Fatalf("route %s missing: %v", name, err)
		}
		var entries []model.RouteGroupEntry
		if err := json.Unmarshal([]byte(rt.GroupsJSON), &entries); err != nil || len(entries) != 1 {
			t.Fatalf("route %s groups_json = %s", name, rt.GroupsJSON)
		}
		if entries[0].Model != name || entries[0].Weight != 100 || entries[0].GroupID != out.Groups[0].ID {
			t.Fatalf("route %s mapping = %+v, want model passthrough → group %d w100",
				name, entries[0], out.Groups[0].ID)
		}
	}
	// 密钥：无密钥时生成，格式与面板 createKey 一致（可回显、lookup 命中）
	if out.Key == nil || !out.Key.Created || !strings.HasPrefix(out.Key.Key, "cph-") {
		t.Fatalf("key = %+v, want created cph-*", out.Key)
	}
	if n := countRows(t, s, &model.Key{}, "key_lookup = ?", account.KeyLookupHash(out.Key.Key)); n != 1 {
		t.Fatalf("key_lookup rows = %d, want 1", n)
	}
	// 与面板密钥列表同步：同一张表可查、明文可解
	var k model.Key
	if err := s.db.First(&k, out.Key.ID).Error; err != nil {
		t.Fatal(err)
	}
	plain, err := account.DecryptCredential(s.dataDir, []byte(k.KeyCipher))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got := string(plain); got != out.Key.Key {
		t.Fatalf("reveal mismatch")
	}
}

// TestAutoConfigIdempotent 同插件重复加账号：复用分组、路由幂等跳过、不重复发密钥。
func TestAutoConfigIdempotent(t *testing.T) {
	s, _ := seedAutocfg(t)
	a1 := addAccount(t, s, "ptest", `[{"id":"model-a"}]`)
	if out := s.autoConfigForAccount("ptest", a1); len(out.Errors) > 0 {
		t.Fatalf("first: %v", out.Errors)
	}
	a2 := addAccount(t, s, "ptest", `[{"id":"model-a"}]`)
	out := s.autoConfigForAccount("ptest", a2)
	if len(out.Errors) > 0 {
		t.Fatalf("second: %v", out.Errors)
	}
	// 分组复用（不新建）
	if len(out.Groups) != 1 || out.Groups[0].Created {
		t.Fatalf("groups = %+v, want reused existing", out.Groups)
	}
	// 两账号同组（同插件多账号同一分组）
	if n := countRows(t, s, &model.AccountGroup{}, "group_id = ?", out.Groups[0].ID); n != 2 {
		t.Fatalf("group links = %d, want 2", n)
	}
	// 路由已覆盖 → 幂等跳过；密钥复用不重建
	if len(out.Created) != 0 || len(out.Existing) != 1 {
		t.Fatalf("created=%v existing=%v, want created empty", out.Created, out.Existing)
	}
	if out.Key == nil || out.Key.Created {
		t.Fatalf("key = %+v, want reused", out.Key)
	}
	if n := countRows(t, s, &model.Key{}, "1=1"); n != 1 {
		t.Fatalf("keys = %d, want 1", n)
	}
	if n := countRows(t, s, &model.Route{}, "name = ?", "model-a"); n != 1 {
		t.Fatalf("route rows = %d, want 1", n)
	}
}

// TestAutoConfigModelConflict 跨插件同名模型：先配置者保留原名，后来者 <模型名>@<插件名>。
func TestAutoConfigModelConflict(t *testing.T) {
	s, _ := seedAutocfg(t)
	a1 := addAccount(t, s, "first", `[{"id":"shared-model"},{"id":"only-first"}]`)
	out1 := s.autoConfigForAccount("first", a1)
	if len(out1.Errors) > 0 || len(out1.Created) != 2 {
		t.Fatalf("first plugin: errors=%v created=%v", out1.Errors, out1.Created)
	}
	a2 := addAccount(t, s, "second", `[{"id":"shared-model"}]`)
	out2 := s.autoConfigForAccount("second", a2)
	if len(out2.Errors) > 0 {
		t.Fatalf("second plugin: %v", out2.Errors)
	}
	// shared-model 被占用 → 消歧路由；确定性地命名为 <模型名>@<插件名>
	if len(out2.Renamed) != 1 || out2.Renamed[0].Model != "shared-model" ||
		out2.Renamed[0].Route != "shared-model@second" {
		t.Fatalf("renamed = %+v, want shared-model → shared-model@second", out2.Renamed)
	}
	var rt model.Route
	if err := s.db.Where("name = ?", "shared-model@second").First(&rt).Error; err != nil {
		t.Fatalf("disambiguated route missing: %v", err)
	}
	var entries []model.RouteGroupEntry
	_ = json.Unmarshal([]byte(rt.GroupsJSON), &entries)
	if len(entries) != 1 || entries[0].Model != "shared-model" {
		// 真实模型仍是原名（对外名消歧、上游模型名不变）
		t.Fatalf("disambiguated route real model = %+v, want shared-model", entries)
	}
	// 原名路由仍归先配置插件，未被篡改（新 dest 变量，避免 GORM 主键条件复用）
	var orig model.Route
	if err := s.db.Where("name = ?", "shared-model").First(&orig).Error; err != nil {
		t.Fatalf("original route missing: %v", err)
	}
	// 同名模型重复触发（第三个账号同插件）→ 幂等
	a3 := addAccount(t, s, "second", `[{"id":"shared-model"}]`)
	out3 := s.autoConfigForAccount("second", a3)
	if len(out3.Renamed) != 0 || len(out3.Created) != 0 || len(out3.Errors) > 0 {
		t.Fatalf("third: renamed=%v created=%v errors=%v, want idempotent", out3.Renamed, out3.Created, out3.Errors)
	}
}

// TestAutoConfigKeyExists 已有密钥：复用第一把并回显明文（可解密格式），不再生成。
func TestAutoConfigKeyExists(t *testing.T) {
	s, _ := seedAutocfg(t)
	raw := "cph-existing-key"
	blob, err := account.EncryptCredential(s.dataDir, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	s.db.Create(&model.Key{KeyCipher: string(blob),
		KeyLookup: account.KeyLookupHash(raw), Name: "existing", Enabled: true})
	acctID := addAccount(t, s, "ptest", `[{"id":"m"}]`)
	out := s.autoConfigForAccount("ptest", acctID)
	if out.Key == nil || out.Key.Created || out.Key.Key != raw {
		t.Fatalf("key = %+v, want reuse existing with plaintext", out.Key)
	}
}
