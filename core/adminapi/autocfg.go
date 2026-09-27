// autocfg.go — 自动配置引擎（NexPort v1.3.0 方案 ②，开箱即用）。
//
// 触发时机：用户为插件 P 添加账号成功（submitLogin 建档完成）后同步执行（纯 DB 操作，
// 毫秒级；模型目录已在建档时同步落库，不产生额外上游请求），结果随登录响应回显，
// 应用即可展示「本地/局域网端点 + 密钥 + 模型映射说明」。
//
// 自动完成三件事（幂等，重复添加账号不产生重复配置）：
//  1. 分组：为 P 的每个实例建一个分组并纳入其全部账号（同插件多账号同一分组；
//     分组模型限定同实例，多实例插件按实例各建一组）。已有 P+实例 的分组则直接复用，
//     新账号并入（不搬迁用户手工配置）。
//  2. 模型路由：模型名原样映射——对外路由名 = 客户端请求的 model = 分组内真实模型 id。
//     跨插件同名模型的确定性消歧策略（详见本文件下方「同名模型消歧」）：
//     先配置者保留原名路由；后配置者建 <模型名>@<插件名>；同名路由已指向本插件
//     分组时视为已配置（跳过）。
//  3. 默认 API 密钥：库里不存在任何密钥时生成一把（与面板「API 密钥」同一张表、
//     同一 AES-256-GCM 格式，列表/回显/删除全同步）；已有密钥则复用第一把并在响应中
//     回显明文（与 reveal 端点同源解密；存量 sha256 哈希格式不可解密则不回显）。
//
// 同名模型消歧（确定性策略，写入文档 = README「v1.3.0 核心侧改造」节 + 本注释）：
//   - 路由名全局唯一（routes.name uniqueIndex），天然不允许两插件占用同名路由；
//   - 事件顺序即优先级：账号建档成功的时间顺序决定谁保留原名——先来者用原模型名，
//     后来者一律 <模型名>@<插件名>（插件名唯一，结果确定且可预测）；
//   - 该策略与账号添加顺序绑定而非随机：同一设备上重复操作结果一致（幂等跳过），
//     换设备按相同顺序操作得到相同映射；用户可在面板「路由」页自行改名调整。
package adminapi

import (
	"encoding/json"
	"fmt"
	"strings"

	"io.nexport.gateway/core/account"
	"io.nexport.gateway/core/model"
	"io.nexport.gateway/core/runlog"
)

// autoConfigResult 登录响应中回显的自动配置结果（auto_config 字段）。
type autoConfigResult struct {
	// 分组（每实例一组；单实例插件恒 1 组）
	Groups []struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Created bool   `json:"created"`
	} `json:"groups"`
	// 路由：Created 本次新建；Existing 已存在且已覆盖本插件（幂等跳过）；
	// Renamed 同名模型冲突后按 <模型名>@<插件名> 确定性消歧新建。
	Created  []string `json:"routes_created"`
	Existing []string `json:"routes_existing"`
	Renamed  []struct {
		Model string `json:"model"` // 原模型名（被其他插件占用）
		Route string `json:"route"` // 实际建出的路由名（<model>@<plugin>）
	} `json:"routes_renamed"`
	// 默认 API 密钥（无则生成；与面板密钥列表同步）
	Key *struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Key     string `json:"key,omitempty"` // 明文（本响应唯一一次回显；存量哈希格式为空）
		Created bool   `json:"created"`
	} `json:"key,omitempty"`
	// 端点与用法说明（应用直接展示）
	LocalEndpoint string   `json:"local_endpoint"`         // http://127.0.0.1:<gwPort>
	LanEndpoint   string   `json:"lan_endpoint,omitempty"` // http://<lanIP>:<gwPort>（未开启为空）
	Usage         string   `json:"usage"`                  // 一句话用法（Bearer + 模型名）
	ModelNote     string   `json:"model_note"`             // 模型映射说明（原样映射 + 同名消歧策略）
	Errors        []string `json:"errors,omitempty"`       // 非致命失败（分组/路由单项失败不影响其余步骤）
}

// autoConfigForAccount 账号建档成功后的自动配置入口（submitLogin 调用）。
// 永不失败登录：内部 recover，逐项 best-effort，失败项进 Errors 并落运行日志。
func (s *Server) autoConfigForAccount(pluginName string, accountID int64) *autoConfigResult {
	defer func() {
		if r := recover(); r != nil {
			s.runLogger().Error("account", "autocfg", "自动配置异常: "+pluginName, fmt.Sprintf("%v", r), &accountID)
		}
	}()
	out := &autoConfigResult{}
	s.autoConfigGroups(pluginName, accountID, out)
	s.autoConfigRoutes(pluginName, accountID, out)
	s.autoConfigKey(out)
	s.autoConfigEndpoints(out)
	if len(out.Errors) > 0 {
		s.runLogger().Warn("account", "autocfg", "自动配置部分失败: "+pluginName,
			strings.Join(out.Errors, "；"), &accountID)
	}
	s.runLogger().Info("account", "autocfg", "自动配置完成: "+pluginName,
		fmt.Sprintf("路由新建 %d / 复用 %d / 消歧 %d；密钥 %s", len(out.Created), len(out.Existing),
			len(out.Renamed), keySummary(out.Key)), nil)
	return out
}

// runLogger 运行日志（级别走设置实时读取，与其他模块一致）。
func (s *Server) runLogger() *runlog.Logger {
	return runlog.New(s.db, func() string { return s.settings.RunLevel() })
}

// keySummary 密钥摘要（日志用，不落明文）。
func keySummary(k *struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Key     string `json:"key,omitempty"`
	Created bool   `json:"created"`
}) string {
	if k == nil {
		return "无"
	}
	if k.Created {
		return fmt.Sprintf("已生成 #%d", k.ID)
	}
	return fmt.Sprintf("复用 #%d", k.ID)
}

// autoConfigGroups 为插件每个实例确保分组存在，并保证新账号已入组。
func (s *Server) autoConfigGroups(pluginName string, accountID int64, out *autoConfigResult) {
	var p model.Plugin
	if err := s.db.Where("name = ?", pluginName).First(&p).Error; err != nil {
		out.Errors = append(out.Errors, "插件记录不存在: "+pluginName)
		return
	}
	var accts []model.Account
	if err := s.db.Where("plugin_id = ?", p.ID).Order("id").Find(&accts).Error; err != nil {
		out.Errors = append(out.Errors, "读取插件账号失败")
		return
	}
	if len(accts) == 0 {
		return
	}
	// 实例 → 账号（同插件多账号按实例聚组；单实例插件全部账号同一组）
	byInst := map[int64][]model.Account{}
	var instOrder []int64
	for _, a := range accts {
		if _, ok := byInst[a.InstanceID]; !ok {
			instOrder = append(instOrder, a.InstanceID)
		}
		byInst[a.InstanceID] = append(byInst[a.InstanceID], a)
	}
	for _, instID := range instOrder {
		var g model.Group
		err := s.db.Where("plugin_id = ? AND instance_id = ?", p.ID, instID).Order("id").First(&g).Error
		created := false
		if err != nil {
			name := s.freeGroupName(p.Name)
			g = model.Group{Name: name, PluginID: p.ID, InstanceID: instID}
			if err := s.db.Create(&g).Error; err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("创建分组失败(%s): %v", name, err))
				continue
			}
			created = true
		}
		// 全部账号纳入该组（INSERT OR IGNORE 幂等：手工移出过的账号不会被强行拉回）
		for _, a := range byInst[instID] {
			s.db.Exec(`INSERT OR IGNORE INTO account_groups (account_id, group_id) VALUES (?, ?)`, a.ID, g.ID)
		}
		type gv struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Created bool   `json:"created"`
		}
		out.Groups = append(out.Groups, gv{ID: g.ID, Name: g.Name, Created: created})
	}
}

// freeGroupName 分组名候选：插件名（全局唯一）→ <名>-auto → <名>-auto-2…；
// 跳过已被任何插件占用的名字（routes/groups.name 均为 uniqueIndex）。
func (s *Server) freeGroupName(base string) string {
	for i := 0; ; i++ {
		name := base
		if i == 1 {
			name = base + "-auto"
		} else if i > 1 {
			name = fmt.Sprintf("%s-auto-%d", base, i)
		}
		var n int64
		s.db.Model(&model.Group{}).Where("name = ?", name).Count(&n)
		if n == 0 {
			return name
		}
	}
}

// autoConfigRoutes 为新账号模型目录中的每个模型建路由（模型名原样映射）。
func (s *Server) autoConfigRoutes(pluginName string, accountID int64, out *autoConfigResult) {
	var p model.Plugin
	if err := s.db.Where("name = ?", pluginName).First(&p).Error; err != nil {
		return // groups 阶段已报错，这里静默
	}
	// 本插件全部分组（含既有 + 刚建的）：判定同名路由是否已覆盖本插件
	var groupIDs []int64
	s.db.Model(&model.Group{}).Where("plugin_id = ?", p.ID).Pluck("id", &groupIDs)
	inOurGroups := func(groupsJSON string) bool {
		var entries []model.RouteGroupEntry
		if json.Unmarshal([]byte(groupsJSON), &entries) != nil {
			return false
		}
		for _, e := range entries {
			for _, gid := range groupIDs {
				if e.GroupID == gid {
					return true
				}
			}
		}
		return false
	}
	// 新账号的模型目录（建档时已同步；空目录时无路由可建，应用侧按 errors/空列表提示）
	var firstGroupID int64
	if len(groupIDs) > 0 {
		firstGroupID = groupIDs[0]
	}
	if firstGroupID == 0 {
		return
	}
	for _, m := range s.accounts.StoredModels(accountID) {
		modelID := strings.TrimSpace(m.GetId())
		if modelID == "" {
			continue
		}
		var rt model.Route
		err := s.db.Where("name = ?", modelID).First(&rt).Error
		switch {
		case err == nil && inOurGroups(rt.GroupsJSON):
			out.Existing = append(out.Existing, modelID) // 幂等：已覆盖本插件
		case err == nil:
			// 同名模型被其他插件占用 → 确定性消歧：<模型名>@<插件名>
			alt := fmt.Sprintf("%s@%s", modelID, p.Name)
			var n int64
			s.db.Model(&model.Route{}).Where("name = ?", alt).Count(&n)
			if n > 0 {
				out.Existing = append(out.Existing, alt)
				continue
			}
			if err := s.createAutoRoute(alt, firstGroupID, modelID); err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("创建路由失败(%s): %v", alt, err))
				continue
			}
			out.Renamed = append(out.Renamed, struct {
				Model string `json:"model"`
				Route string `json:"route"`
			}{modelID, alt})
		default:
			if err := s.createAutoRoute(modelID, firstGroupID, modelID); err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("创建路由失败(%s): %v", modelID, err))
				continue
			}
			out.Created = append(out.Created, modelID)
		}
	}
}

// createAutoRoute 建一条自动路由：单分组 100 权重，真实模型 = 指定 id。
func (s *Server) createAutoRoute(name string, groupID int64, realModel string) error {
	entries := []model.RouteGroupEntry{{GroupID: groupID, Weight: 100, Model: realModel}}
	b, _ := json.Marshal(entries)
	return s.db.Create(&model.Route{Name: name, Strategy: "round_robin", GroupsJSON: string(b)}).Error
}

// autoConfigKey 确保默认 API 密钥存在（无则生成；有则回显第一把的明文）。
func (s *Server) autoConfigKey(out *autoConfigResult) {
	var n int64
	s.db.Model(&model.Key{}).Count(&n)
	if n == 0 {
		raw := "cph-" + randHex(24)
		k := model.Key{KeyCipher: string(account.EncryptCredential(s.dataDir, []byte(raw))),
			KeyLookup: account.KeyLookupHash(raw), Name: s.settings.SiteAbbr(), Enabled: true}
		if err := s.db.Create(&k).Error; err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("生成默认密钥失败: %v", err))
			return
		}
		out.Key = &struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Key     string `json:"key,omitempty"`
			Created bool   `json:"created"`
		}{ID: k.ID, Name: k.Name, Key: raw, Created: true}
		return
	}
	// 已有密钥：复用第一把（面板密钥列表首行），明文经 reveal 同源解密回显
	var k model.Key
	if err := s.db.Order("id").First(&k).Error; err != nil {
		return
	}
	plain := ""
	if len(k.KeyCipher) == 64 && k.KeyCipher[0] != 0x01 {
		plain = "" // 存量 sha256 哈希格式不可回显（与 reveal 行为一致）
	} else {
		plain = string(account.DecryptCredential(s.dataDir, []byte(k.KeyCipher)))
	}
	out.Key = &struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Key     string `json:"key,omitempty"`
		Created bool   `json:"created"`
	}{ID: k.ID, Name: k.Name, Key: plain, Created: false}
}

// autoConfigEndpoints 组装端点与用法说明（runtimeInfo 由 app.Start 装配；nil 时留空）。
func (s *Server) autoConfigEndpoints(out *autoConfigResult) {
	if s.runtimeInfo == nil {
		return
	}
	info := s.runtimeInfo()
	if port, ok := info["gateway_port"].(int); ok && port > 0 {
		out.LocalEndpoint = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	if lan, ok := info["lan_endpoint"].(string); ok {
		out.LanEndpoint = lan
	}
	out.Usage = "客户端 Base URL 指向上述端点，Authorization: Bearer <API 密钥>，model 用上方模型名即可"
	out.ModelNote = "模型名原样映射：请求的 model 直接路由到对应插件的同名真实模型；跨插件同名模型时，先配置者保留原名，后来者为 <模型名>@<插件名>（可在面板「路由」页查看与调整）"
}
