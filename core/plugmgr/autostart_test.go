package plugmgr

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"io.nexport.gateway/core/database"
	"io.nexport.gateway/core/model"
)

// AutoStarts 双重过滤（perf 修复轮）：①持久化停止（enabled=0）跳过；②仅保留
// 已配置账号的插件（Account.PluginID → Plugin.ID 关联）——无账号插件（无论有无
// DB 记录）不再自启，经管理页「启动」显式拉起。
func TestAutoStartsFiltersDisabledAndAccountless(t *testing.T) {
	root := t.TempDir()
	mk := func(name string) {
		dir := filepath.Join(root, name)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"name":"`+name+`"}`), 0o644)
		bin := filepath.Join(dir, "plugin-"+runtimeOS()+"-"+runtimeArch())
		if runtimeOS() == "windows" {
			bin += ".exe"
		}
		os.WriteFile(bin, []byte("bin"), 0o755)
	}
	mk("alpha") // enabled=1 且已配置账号 → 自启
	mk("beta")  // enabled=0（持久化停止）但已配置账号 → 停用语义优先，跳过
	mk("gamma") // enabled=1 无账号 → 账号门槛跳过
	mk("delta") // 无 DB 记录（新装）→ 账号门槛跳过

	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
	pa := &model.Plugin{Name: "alpha", Version: "1", Author: "a", ManifestJSON: "{}", Enabled: true}
	if err := db.Create(pa).Error; err != nil {
		t.Fatal(err)
	}
	pb := &model.Plugin{Name: "beta", Version: "1", Author: "a", ManifestJSON: "{}"}
	if err := db.Create(pb).Error; err != nil {
		t.Fatal(err)
	}
	// Enabled 带 default:true，Create 传零值 false 会被 gorm 当"未设置"→ 用 Update 显式落 0（同生产 Stop 路径）
	db.Model(&model.Plugin{}).Where("name = ?", "beta").Update("enabled", false)
	pg := &model.Plugin{Name: "gamma", Version: "1", Author: "a", ManifestJSON: "{}", Enabled: true}
	if err := db.Create(pg).Error; err != nil {
		t.Fatal(err)
	}
	// 账号：alpha、beta 各一条（凭据 blob 允许空，门槛只看「存在已配置账号」）
	if err := db.Create(&model.Account{PluginID: pa.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Account{PluginID: pb.ID}).Error; err != nil {
		t.Fatal(err)
	}

	m := &Manager{dir: root, db: db}
	dirs, err := m.AutoStarts()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range dirs {
		got[filepath.Base(d)] = true // AutoStarts 现返回插件目录
	}
	if !got["alpha"] || got["beta"] || got["gamma"] || got["delta"] {
		t.Fatalf("autostart set wrong: %v (want only alpha)", got)
	}
}

// AutoStarts 降级语义：账号查询出错时 fail-open 返回全量 Scan 结果（仅①过滤目标
// 行为不可用时与既有「DB 不可用照常拉起」一致）。这里以关库制造查询失败路径。
func TestAutoStartsFailOpenOnDBError(t *testing.T) {
	root := t.TempDir()
	mk := func(name string) {
		dir := filepath.Join(root, name)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"name":"`+name+`"}`), 0o644)
		bin := filepath.Join(dir, "plugin-"+runtimeOS()+"-"+runtimeArch())
		if runtimeOS() == "windows" {
			bin += ".exe"
		}
		os.WriteFile(bin, []byte("bin"), 0o755)
	}
	mk("alpha")
	mk("beta")

	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Plugin{Name: "alpha", Version: "1", Author: "a", ManifestJSON: "{}", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	// 关库使后续查询失败（fail-open 口径）
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.Close()
	}

	m := &Manager{dir: root, db: db}
	dirs, err := m.AutoStarts()
	if err != nil {
		t.Fatalf("AutoStarts should not propagate query errors: %v", err)
	}
	if len(dirs) != 2 {
		t.Fatalf("fail-open expected all 2 dirs, got %v", dirs)
	}
}
