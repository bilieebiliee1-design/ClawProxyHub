// keys.go — 密钥与分组、路由管理。
package adminapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"io.nexport.gateway/core/account"
	"io.nexport.gateway/core/model"
)

// keyMask 密钥掩码：明文尾 8 位（cph-****<尾8位>），与「眼睛」回显的明文一致，
// 用户可与已复制明文直接核对（v1.4.8 修复：旧掩码为 id+创建时间哈希，与明文尾缀无关）。
// 明文不可得时回退旧口径 id + 创建时间哈希前 4 字节（仅识别用，与明文尾缀无对应）：
//   - 存量 sha256 哈希密钥（KeyCipher 64 位 hex，明文未存，reveal 亦提示重建）；
//   - 解密失败/密文异常（解密结果非 cph- 前缀视为不可信，不展示乱码尾缀）。
func keyMask(dataDir string, k model.Key) string {
	if tail := plainTail(dataDir, k.KeyCipher); tail != "" {
		return fmt.Sprintf("cph-****%s", tail)
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%d|%s", k.ID, k.CreatedAt.Format("2006-01-02 15:04:05"))))
	return fmt.Sprintf("cph-****%s", hex.EncodeToString(h[:4]))
}

// plainTail 可解密新格式密钥（0x01 前缀 AES-256-GCM）的明文尾 8 位；不可得返回空。
// 存量哈希密钥判定与 revealKey 一致（64 位 hex 且无 0x01 前缀）；解密结果须为
// cph- 前缀（createKey 固定格式）且长度足够，否则视为解密失败不派生。
func plainTail(dataDir string, cipher string) string {
	if len(cipher) == 64 && cipher[0] != 0x01 {
		return "" // 存量 sha256 hex：明文未存，无法派生
	}
	raw := string(account.DecryptCredential(dataDir, []byte(cipher)))
	if !strings.HasPrefix(raw, "cph-") || len(raw) < len("cph-")+8 {
		return ""
	}
	return raw[len(raw)-8:]
}

// listKeys GET /admin/keys — 密钥列表（含授权路由）。
func (s *Server) listKeys(w http.ResponseWriter, r *http.Request) {
	var keys []model.Key
	if err := s.db.Order("id").Find(&keys).Error; err != nil {
		http.Error(w, `{"error":"db"}`, http.StatusInternalServerError)
		return
	}
	// 最后调用时刻：request_logs 按 key 聚合
	type lastUse struct {
		KeyID   *int64
		MaxTime string
	}
	var lastUses []lastUse
	s.db.Model(&model.RequestLog{}).
		Select("key_id, MAX(created_at) AS max_time").
		Where("key_id IS NOT NULL").
		Group("key_id").Scan(&lastUses)
	lastUseMap := map[int64]string{}
	for _, lu := range lastUses {
		if lu.KeyID != nil {
			lastUseMap[*lu.KeyID] = lu.MaxTime
		}
	}

	type keyView struct {
		ID         int64   `json:"id"`
		Name       string  `json:"name"`
		Enabled    bool    `json:"enabled"`
		ExpiresAt  *string `json:"expires_at"`
		CreatedAt  string  `json:"created_at"`
		LastUsedAt string  `json:"last_used_at"` // 最后调用（空 = 从未）
		KeyMask    string  `json:"key_mask"`     // 掩码（cph-****<明文尾8位>；明文不可得时为识别哈希）
		RouteIDs   []int64 `json:"route_ids"`    // 空 = 全部路由
	}
	var out []keyView
	for _, k := range keys {
		v := keyView{ID: k.ID, Name: k.Name, Enabled: k.Enabled,
			CreatedAt:  k.CreatedAt.Format("2006-01-02 15:04:05"),
			LastUsedAt: lastUseMap[k.ID], KeyMask: keyMask(s.accounts.DataDir(), k)}
		if k.ExpiresAt != nil {
			t := k.ExpiresAt.Format("2006-01-02 15:04:05")
			v.ExpiresAt = &t
		}
		var routes []model.KeyRoute
		s.db.Where("key_id = ?", k.ID).Find(&routes)
		for _, kr := range routes {
			v.RouteIDs = append(v.RouteIDs, kr.RouteID)
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"keys": out})
}

// revealKey GET /admin/keys/{id}/reveal — 单独回显密钥明文（供列表复制）。
func (s *Server) revealKey(w http.ResponseWriter, r *http.Request) {
	var k model.Key
	if err := s.db.First(&k, parseInt(r.PathValue("id"))).Error; err != nil {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	plain := account.DecryptCredential(s.accounts.DataDir(), []byte(k.KeyCipher))
	// 存量哈希（无 0x01 前缀）无法回显明文，提示重建
	if len(k.KeyCipher) == 64 && k.KeyCipher[0] != 0x01 {
		http.Error(w, `{"error":"legacy key stored hashed, please recreate"}`, http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"key": string(plain)})
}

// createKey POST /admin/keys — 创建密钥，明文只返回一次；名称留空用站点缩写。
func (s *Server) createKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	readBody(w, r, &body)
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = s.settings.SiteAbbr()
	}
	raw := "cph-" + randHex(24)
	k := model.Key{KeyCipher: string(account.EncryptCredential(s.accounts.DataDir(), []byte(raw))),
		KeyLookup: account.KeyLookupHash(raw), Name: name, Enabled: true}
	if err := s.db.Create(&k).Error; err != nil {
		http.Error(w, `{"error":"create failed"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"id": k.ID, "key": raw})
}

// updateKey PUT /admin/keys/{id} — body: {name}，改名。
func (s *Server) updateKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !readBody(w, r, &body) {
		return
	}
	if err := s.db.Model(&model.Key{}).Where("id = ?", parseInt(r.PathValue("id"))).
		Update("name", body.Name).Error; err != nil {
		http.Error(w, `{"error":"update failed"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// deleteKey DELETE /admin/keys/{id}
func (s *Server) deleteKey(w http.ResponseWriter, r *http.Request) {
	id := parseInt(r.PathValue("id"))
	s.db.Where("key_id = ?", id).Delete(&model.KeyRoute{})
	s.db.Delete(&model.Key{}, id)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// toggleKey POST /admin/keys/{id}/toggle
func (s *Server) toggleKey(w http.ResponseWriter, r *http.Request) {
	var k model.Key
	if err := s.db.First(&k, parseInt(r.PathValue("id"))).Error; err != nil {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	s.db.Model(&k).Update("enabled", !k.Enabled)
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": !k.Enabled})
}

// bindKeyRoutes PUT /admin/keys/{id}/routes — body: {route_ids: []}，空 = 全部。
func (s *Server) bindKeyRoutes(w http.ResponseWriter, r *http.Request) {
	id := parseInt(r.PathValue("id"))
	var body struct {
		RouteIDs []int64 `json:"route_ids"`
	}
	if !readBody(w, r, &body) {
		return
	}
	s.db.Where("key_id = ?", id).Delete(&model.KeyRoute{})
	for _, rid := range body.RouteIDs {
		s.db.Create(&model.KeyRoute{KeyID: id, RouteID: rid})
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- 分组 ----------

// listGroups GET /admin/groups — 分组列表（含账号数）。
func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	var groups []model.Group
	if err := s.db.Order("id").Find(&groups).Error; err != nil {
		http.Error(w, `{"error":"db"}`, http.StatusInternalServerError)
		return
	}
	type groupView struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		PluginID    int64  `json:"plugin_id"`
		InstanceID  int64  `json:"instance_id"`
		Plugin      string `json:"plugin"`
		PluginLabel string `json:"plugin_label"` // 品牌名
		Accounts    int64  `json:"accounts"`
	}
	var out []groupView
	for _, g := range groups {
		v := groupView{ID: g.ID, Name: g.Name, PluginID: g.PluginID, InstanceID: g.InstanceID}
		var p model.Plugin
		if err := s.db.First(&p, g.PluginID).Error; err == nil {
			v.Plugin = p.Name
		}
		v.PluginLabel = s.pluginBrandByID(g.PluginID)
		s.db.Model(&model.AccountGroup{}).Where("group_id = ?", g.ID).Count(&v.Accounts)
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"groups": out})
}

// createGroup POST /admin/groups — body: {name, plugin_id, instance_id?}（instance_id 缺省 = 插件默认实例）
func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string `json:"name"`
		PluginID   int64  `json:"plugin_id"`
		InstanceID int64  `json:"instance_id"`
	}
	if !readBody(w, r, &body) || body.Name == "" || body.PluginID == 0 {
		http.Error(w, `{"error":"name and plugin_id required"}`, http.StatusBadRequest)
		return
	}
	inst, err := account.ResolveInstance(s.db, body.PluginID, body.InstanceID, s.multiInstance(body.PluginID))
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	g := model.Group{Name: body.Name, PluginID: body.PluginID, InstanceID: inst.ID}
	if err := s.db.Create(&g).Error; err != nil {
		http.Error(w, `{"error":"duplicate name"}`, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"id": g.ID})
}

// updateGroup PUT /admin/groups/{id} — body: {name?, instance_id?}；有账号挂靠时不可换实例。
func (s *Server) updateGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string `json:"name"`
		InstanceID int64  `json:"instance_id"`
	}
	if !readBody(w, r, &body) {
		return
	}
	var g model.Group
	if err := s.db.First(&g, parseInt(r.PathValue("id"))).Error; err != nil {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	updates := map[string]interface{}{}
	if name := strings.TrimSpace(body.Name); name != "" && name != g.Name {
		updates["name"] = name
	}
	if body.InstanceID > 0 && body.InstanceID != g.InstanceID {
		var n int64
		s.db.Model(&model.AccountGroup{}).Where("group_id = ?", g.ID).Count(&n)
		if n > 0 {
			http.Error(w, `{"error":"分组下仍有账号，请先移出账号再更换实例"}`, http.StatusBadRequest)
			return
		}
		inst, err := account.ResolveInstance(s.db, g.PluginID, body.InstanceID, s.multiInstance(g.PluginID))
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
		updates["instance_id"] = inst.ID
	}
	if len(updates) > 0 {
		if err := s.db.Model(&g).Updates(updates).Error; err != nil {
			http.Error(w, `{"error":"duplicate name"}`, http.StatusBadRequest)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// groupModels GET /admin/groups/{id}/models — 分组内全部账号模型目录的并集（路由映射下拉候选）。
func (s *Server) groupModels(w http.ResponseWriter, r *http.Request) {
	var ids []int64
	s.db.Model(&model.AccountGroup{}).Where("group_id = ?", parseInt(r.PathValue("id"))).Pluck("account_id", &ids)
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		for _, m := range s.accounts.StoredModels(id) {
			if m.Id != "" && !seen[m.Id] {
				seen[m.Id] = true
				out = append(out, m.Id)
			}
		}
	}
	if out == nil {
		out = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"models": out})
}

// deleteGroup DELETE /admin/groups/{id}
// 级联清理（v1.4.0 验收修复①）：删除分组后同步清理引用它的数据，避免孤儿残留——
//   - 模型路由：仅指向该分组的路由整条删除（否则孤儿路由残留会占用全局唯一路由名，
//     并使后续同插件自动配置按消歧策略只能建 <模型名>@<插件名> 后缀路由）；还指向
//     其他分组的路由仅摘除该分组条目（剩余条目原样保留，权重合计可再经面板编辑）；
//   - 降级引用：failover_group_id 指向该分组的路由关闭降级并清空引用（悬空引用）；
//   - 关联行：account_groups / group_proxies 关联行、以及被删路由的 key_routes
//     绑定行一并清理（无外键级联，SQLite 关联表手工清）。
func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id := parseInt(r.PathValue("id"))
	s.db.Delete(&model.Group{}, id)

	var routes []model.Route
	if err := s.db.Find(&routes).Error; err == nil {
		removedRouteIDs := []int64{}
		for _, rt := range routes {
			var entries []model.RouteGroupEntry
			if json.Unmarshal([]byte(rt.GroupsJSON), &entries) != nil {
				continue
			}
			kept := make([]model.RouteGroupEntry, 0, len(entries))
			had := false
			for _, e := range entries {
				if e.GroupID == id {
					had = true
					continue
				}
				kept = append(kept, e)
			}
			if !had {
				if rt.FailoverGroupID != nil && *rt.FailoverGroupID == id {
					s.db.Model(&model.Route{}).Where("id = ?", rt.ID).
						Updates(map[string]interface{}{"failover_enabled": false, "failover_group_id": nil})
				}
				continue // 未引用该分组：不动
			}
			if len(kept) == 0 {
				// 仅指向该分组 → 整条删除，并清理其密钥绑定行
				s.db.Delete(&model.Route{}, rt.ID)
				s.db.Exec(`DELETE FROM key_routes WHERE route_id = ?`, rt.ID)
				removedRouteIDs = append(removedRouteIDs, rt.ID)
				continue
			}
			// 还指向其他分组 → 摘除条目（failover 引用同组清空）
			keptJSON, merr := json.Marshal(kept)
			if merr != nil {
				continue
			}
			updates := map[string]interface{}{"groups_json": string(keptJSON)}
			if rt.FailoverGroupID != nil && *rt.FailoverGroupID == id {
				updates["failover_enabled"] = false
				updates["failover_group_id"] = nil
			}
			s.db.Model(&model.Route{}).Where("id = ?", rt.ID).Updates(updates)
		}
		_ = removedRouteIDs
	}
	s.db.Exec(`DELETE FROM account_groups WHERE group_id = ?`, id)
	s.db.Exec(`DELETE FROM group_proxies WHERE group_id = ?`, id)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// ---------- 路由 ----------

// listRoutes GET /admin/routes
func (s *Server) listRoutes(w http.ResponseWriter, r *http.Request) {
	var routes []model.Route
	if err := s.db.Order("id").Find(&routes).Error; err != nil {
		http.Error(w, `{"error":"db"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"routes": routes})
}

// routeBody 创建/编辑路由共用的请求体。
type routeBody struct {
	Name                     string                  `json:"name"`
	Strategy                 string                  `json:"strategy"`
	Groups                   []model.RouteGroupEntry `json:"groups"`
	FirstEventTimeoutSeconds int32                   `json:"first_event_timeout_seconds"`
	FirstTokenTimeoutSeconds int32                   `json:"first_token_timeout_seconds"`
	UserAgent                string                  `json:"user_agent"`
	FailoverEnabled          bool                    `json:"failover_enabled"`
	FailoverOn4xx            bool                    `json:"failover_on_4xx"`
	FailoverOn5xx            bool                    `json:"failover_on_5xx"`
	FailoverGroupID          *int64                  `json:"failover_group_id"`
	FailoverModel            string                  `json:"failover_model"`
}

// validate 分组权重须 0–100 且合计恰好 100（如 100 / 50+50 / 100+0+0 / 30+20+50）；
// 降级开启时必须配齐：触发状态类（4xx/5xx 至少一项）+ 降级分组 + 降级模型。
func (b *routeBody) validate() string {
	if b.Name == "" || len(b.Groups) == 0 {
		return "name and groups required"
	}
	sum := 0
	for _, g := range b.Groups {
		if g.GroupID == 0 || g.Model == "" {
			return "每个分组映射需指定分组与模型"
		}
		if g.Weight < 0 || g.Weight > 100 {
			return "权重需在 0–100 之间"
		}
		sum += g.Weight
	}
	if sum != 100 {
		return fmt.Sprintf("分组权重合计须为 100（当前 %d）", sum)
	}
	if b.Strategy == "" {
		b.Strategy = "round_robin"
	}
	if b.FirstEventTimeoutSeconds < 0 || b.FirstEventTimeoutSeconds > 3600 {
		return "first_event_timeout_seconds 需在 0–3600 秒之间（0 = 跟随全局）"
	}
	if b.FirstTokenTimeoutSeconds < 0 || b.FirstTokenTimeoutSeconds > 3600 {
		return "first_token_timeout_seconds 需在 0–3600 秒之间（0 = 跟随全局）"
	}
	b.UserAgent = strings.TrimSpace(b.UserAgent)
	if len(b.UserAgent) > 512 {
		return "user_agent 过长（最多 512 字符）"
	}
	if b.FailoverEnabled {
		if !b.FailoverOn4xx && !b.FailoverOn5xx {
			return "开启降级需至少勾选一种触发状态（4xx / 5xx）"
		}
		if b.FailoverGroupID == nil || *b.FailoverGroupID == 0 || b.FailoverModel == "" {
			return "开启降级需配置降级分组与降级模型"
		}
	}
	return ""
}

// createRoute POST /admin/routes
func (s *Server) createRoute(w http.ResponseWriter, r *http.Request) {
	var body routeBody
	if !readBody(w, r, &body) {
		return
	}
	if msg := body.validate(); msg != "" {
		http.Error(w, `{"error":"`+msg+`"}`, http.StatusBadRequest)
		return
	}
	groupsJSON, _ := json.Marshal(body.Groups)
	rt := model.Route{
		Name: body.Name, Strategy: body.Strategy, GroupsJSON: string(groupsJSON),
		FirstEventTimeoutSeconds: body.FirstEventTimeoutSeconds, FirstTokenTimeoutSeconds: body.FirstTokenTimeoutSeconds,
		UserAgent: body.UserAgent, FailoverEnabled: body.FailoverEnabled,
		FailoverOn4xx: body.FailoverOn4xx, FailoverOn5xx: body.FailoverOn5xx,
		FailoverGroupID: body.FailoverGroupID, FailoverModel: body.FailoverModel,
	}
	if err := s.db.Create(&rt).Error; err != nil {
		http.Error(w, `{"error":"duplicate name"}`, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"id": rt.ID})
}

// updateRoute PUT /admin/routes/{id}
func (s *Server) updateRoute(w http.ResponseWriter, r *http.Request) {
	var rt model.Route
	if err := s.db.First(&rt, parseInt(r.PathValue("id"))).Error; err != nil {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	var body routeBody
	if !readBody(w, r, &body) {
		return
	}
	if msg := body.validate(); msg != "" {
		http.Error(w, `{"error":"`+msg+`"}`, http.StatusBadRequest)
		return
	}
	groupsJSON, _ := json.Marshal(body.Groups)
	rt.Name = body.Name
	rt.Strategy = body.Strategy
	rt.GroupsJSON = string(groupsJSON)
	rt.FirstEventTimeoutSeconds = body.FirstEventTimeoutSeconds
	rt.FirstTokenTimeoutSeconds = body.FirstTokenTimeoutSeconds
	rt.UserAgent = body.UserAgent
	rt.FailoverEnabled = body.FailoverEnabled
	rt.FailoverOn4xx = body.FailoverOn4xx
	rt.FailoverOn5xx = body.FailoverOn5xx
	rt.FailoverGroupID = body.FailoverGroupID
	rt.FailoverModel = body.FailoverModel
	if err := s.db.Save(&rt).Error; err != nil {
		http.Error(w, `{"error":"save failed"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// deleteRoute DELETE /admin/routes/{id}
func (s *Server) deleteRoute(w http.ResponseWriter, r *http.Request) {
	id := parseInt(r.PathValue("id"))
	s.db.Where("route_id = ?", id).Delete(&model.KeyRoute{})
	s.db.Delete(&model.Route{}, id)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// ---------- 工具 ----------

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
