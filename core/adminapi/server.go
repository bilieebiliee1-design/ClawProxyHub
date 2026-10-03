// Package admin — 管理后台 API。管理员密码存 users 表（bcrypt），
// 首启经 /setup 引导设置；CPH_ADMIN_PASSWORD 仅作容器化引导注入。
package adminapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"

	"io.nexport.gateway/core/account"
	"io.nexport.gateway/core/logsink"
	"io.nexport.gateway/core/model"
	"io.nexport.gateway/core/plugmgr"
	pb "io.nexport.gateway/core/sdk/proto/cphv1"
	"io.nexport.gateway/core/setting"
	"io.nexport.gateway/core/task"
)

// Server 管理后台。
type Server struct {
	db             *gorm.DB
	accounts       *account.Service
	plugins        *plugmgr.Manager
	engine         *task.Engine
	settings       *setting.Store
	marketplaceURL string
	dataDir        string // 数据目录（备份含 secret.key / restore 暂存）
	tmpDir         string // os.CreateTemp 基目录（安卓=应用缓存目录；空 = os.TempDir()）
	dbPath         string // SQLite 文件路径（系统信息体积 / 备份）
	// runtimeInfo 运行时信息提供者（app.Start 装配：网关端口 / 端口变更 / 局域网端点），
	// system/info 返回时合并；nil = 不合并（测试 / 桌面直跑）。
	runtimeInfo func() map[string]interface{}
	// onLanChange 局域网开关设置变化回调（app.Start 装配热切换；nil = 仅落库不重载）。
	onLanChange func()
	// probeChat 测活聊天客户端注入（仅测试；nil = 生产用 plugins.Manager）。
	probeChat ChatClient
	// lazyMu 懒启动单飞锁：并发请求同时拉起同一未运行插件会重复 spawn 子进程
	//（plugmgr.Start 成功后按名写实例表，后写覆盖先写 → 先写的进程泄漏），须串行化。
	lazyMu sync.Mutex
}

// New 创建管理后台；表空且配置了 CPH_ADMIN_PASSWORD 时自动引导建号。
func New(db *gorm.DB, accounts *account.Service, plugins *plugmgr.Manager, engine *task.Engine, settings *setting.Store, marketplaceURL, dataDir, dbPath string) *Server {
	// TMPDIR 修复（NexPort fork）：安卓上 os.CreateTemp("") 会落到不可写的 /data/local/tmp，
	// 市场下载/离线上传/备份导出三处临时文件全部报错——统一改走 s.tmpDir。
	s := &Server{
		tmpDir: os.TempDir(),
		db:     db, accounts: accounts, plugins: plugins, engine: engine,
		settings: settings, marketplaceURL: marketplaceURL, dataDir: dataDir, dbPath: dbPath,
	}
	// 插件源初始化：源列表缺失时用官方地址（config 默认 = env 覆盖或官方地址）建 official 源，
	// 旧版 marketplace_url 自建地址一并导入；生效顺序：启用源聚合 > 离线兜底
	settings.EnsurePluginSources(marketplaceURL)
	s.ensureAdminSeed()
	return s
}

// authed 鉴权路由注册器：h() 注册的处理器统一套 s.auth，杜绝漏包鉴权。
type authed struct {
	mux *http.ServeMux
	s   *Server
}

func (a authed) h(pattern string, fn http.HandlerFunc) { a.mux.HandleFunc(pattern, a.s.auth(fn)) }

// Handler 管理路由：免鉴权引导 + 按资源分组的鉴权路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// 首启引导 + 登录签发（免鉴权）
	mux.HandleFunc("GET /admin/setup-status", s.setupStatus)
	mux.HandleFunc("POST /admin/setup", s.setup)
	mux.HandleFunc("POST /admin/login", s.login)
	mux.HandleFunc("GET /admin/branding", s.getBranding)

	r := authed{mux: mux, s: s}
	s.routeSession(r)
	s.routePlugins(r)
	s.routeAccounts(r)
	s.routeInstances(r)
	s.routeKeys(r)
	s.routeGroups(r)
	s.routeProxies(r)
	s.routeRoutes(r)
	s.routeTasks(r)
	s.routeOAuth(r)
	s.routeLogs(r)
	s.routeNotifications(r)
	s.routeSystem(r)
	s.routeSettingsStats(r)
	return mux
}

// routeOAuth 第三方平台登录态托管（供插件换取上游 token）。
func (s *Server) routeOAuth(r authed) {
	r.h("GET /admin/oauth-credentials", s.listOAuth)
	r.h("POST /admin/oauth-credentials", s.createOAuth)
	r.h("PUT /admin/oauth-credentials/{id}", s.updateOAuth)
	r.h("DELETE /admin/oauth-credentials/{id}", s.deleteOAuth)
}

// routeSession 会话：改密 / 当前用户。
func (s *Server) routeSession(r authed) {
	r.h("POST /admin/password", s.changePassword)
	r.h("GET /admin/me", s.me)
}

// routePlugins 插件：概览 / 设置 / 分发。
func (s *Server) routePlugins(r authed) {
	r.h("GET /admin/plugins", s.listPlugins)
	r.h("GET /admin/plugins/{name}/auth-methods", s.authMethods)
	r.h("GET /admin/plugins/{name}/settings", s.pluginSettings)
	r.h("GET /admin/plugins/{name}/task-capabilities", s.pluginTaskCapabilities)
	r.h("PUT /admin/plugins/{name}/settings", s.putPluginSettings)
	r.h("GET /admin/plugins/marketplace", s.marketplace)
	r.h("POST /admin/plugins/install-market", s.installMarket)
	r.h("POST /admin/plugins/install-upload", s.installUpload)
	r.h("POST /admin/plugins/luahost-upload", s.uploadLuahost)
	r.h("GET /admin/plugin-sources", s.listPluginSources)
	r.h("GET /admin/plugin-sources/probe", s.probePluginSource)
	r.h("PUT /admin/plugin-sources", s.putPluginSources)
	r.h("POST /admin/plugins/{name}/stop", s.stopPlugin)
	r.h("POST /admin/plugins/{name}/start", s.startPlugin)
	r.h("POST /admin/plugins/{name}/reinstall-builtin", s.reinstallBuiltin) // 内置插件一键本地重装（④）
	r.h("DELETE /admin/plugins/{name}", s.uninstallPlugin)
	r.h("GET /admin/plugins/{name}/impact", s.pluginImpact)
}

// routeAccounts 账号：登录 / 详情 / 模型 / 代理 / 调度 / 测试。
func (s *Server) routeAccounts(r authed) {
	r.h("POST /admin/accounts/login", s.submitLogin)
	r.h("GET /admin/accounts", s.listAccounts)
	r.h("GET /admin/accounts/{id}/detail", s.accountDetail)
	r.h("GET /admin/accounts/{id}/models", s.accountModels)
	r.h("PUT /admin/accounts/{id}/models", s.saveAccountModels)
	r.h("DELETE /admin/accounts/{id}", s.deleteAccount)
	r.h("GET /admin/accounts/{id}/impact", s.accountImpact)
	r.h("POST /admin/accounts/{id}/refresh", s.refreshAccount)
	r.h("POST /admin/accounts/{id}/pause", s.pauseAccount)
	r.h("POST /admin/accounts/{id}/resume", s.resumeAccount)
	r.h("PUT /admin/accounts/{id}", s.updateAccount)
	r.h("GET /admin/accounts/{id}/proxies", s.listAccountProxies)
	r.h("PUT /admin/accounts/{id}/proxies", s.bindAccountProxies)
	r.h("POST /admin/accounts/{id}/test", s.testAccount)
}

// routeKeys 密钥：签发 / 回显 / 改名 / 路由授权。
func (s *Server) routeKeys(r authed) {
	r.h("GET /admin/keys", s.listKeys)
	r.h("GET /admin/keys/{id}/reveal", s.revealKey)
	r.h("POST /admin/keys", s.createKey)
	r.h("DELETE /admin/keys/{id}", s.deleteKey)
	r.h("POST /admin/keys/{id}/toggle", s.toggleKey)
	r.h("PUT /admin/keys/{id}", s.updateKey)
	r.h("PUT /admin/keys/{id}/routes", s.bindKeyRoutes)
}

// routeGroups 分组：增删 / 代理绑定。
func (s *Server) routeGroups(r authed) {
	r.h("GET /admin/groups", s.listGroups)
	r.h("POST /admin/groups", s.createGroup)
	r.h("PUT /admin/groups/{id}", s.updateGroup)
	r.h("DELETE /admin/groups/{id}", s.deleteGroup)
	r.h("PUT /admin/groups/{id}/proxies", s.bindGroupProxies)
	r.h("GET /admin/groups/{id}/proxies", s.listGroupProxies)
	r.h("GET /admin/groups/{id}/models", s.groupModels)
}

// routeProxies 出站代理增删改查 + 连通性测试。
func (s *Server) routeProxies(r authed) {
	r.h("GET /admin/proxies", s.listProxies)
	r.h("POST /admin/proxies", s.createProxy)
	r.h("PUT /admin/proxies/{id}", s.updateProxy)
	r.h("DELETE /admin/proxies/{id}", s.deleteProxy)
	r.h("POST /admin/proxies/{id}/test", s.testProxy)
	r.h("POST /admin/probe/run", s.runProbe) // 一键测活：并行探测全部渠道/模型 + 实时进度（③）
}

// routeRoutes 路由增删改查。
func (s *Server) routeRoutes(r authed) {
	r.h("GET /admin/routes", s.listRoutes)
	r.h("POST /admin/routes", s.createRoute)
	r.h("PUT /admin/routes/{id}", s.updateRoute)
	r.h("DELETE /admin/routes/{id}", s.deleteRoute)
}

// routeTasks 任务调度规则与执行历史。
func (s *Server) routeTasks(r authed) {
	r.h("GET /admin/task-rules", s.listTaskRules)
	r.h("POST /admin/task-rules", s.createTaskRule)
	r.h("POST /admin/task-rules/{id}/toggle", s.toggleTaskRule)
	r.h("PUT /admin/task-rules/{id}", s.updateTaskRule)
	r.h("DELETE /admin/task-rules/{id}", s.deleteTaskRule)
	r.h("POST /admin/task-rules/{id}/run", s.runTaskRule)
	r.h("POST /admin/tasks/checkin", s.checkinAll) // 一键签到：全部调度任务立即执行 + 实时进度（③）
	r.h("GET /admin/task-runs", s.listTaskRuns)
}

// routeSettingsStats 系统设置 / 仪表盘统计。
func (s *Server) routeSettingsStats(r authed) {
	r.h("GET /admin/settings", s.getSettings)
	r.h("PUT /admin/settings", s.putSettings)
	r.h("GET /admin/stats", s.dashboardStats)
	r.h("GET /admin/stats/quota", s.dashboardQuota)
	r.h("GET /admin/stats/trend", s.dashboardTrend)
	r.h("GET /admin/version", s.coreVersion)
}

// listPlugins GET /admin/plugins — 已安装插件概览（磁盘为准，含已停止的）。
// 运行中的以握手 manifest 为准；已停止的用 plugins 表的 manifest 快照（每次启动同步），卡片内容不因停止而变。
func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	type pluginView struct {
		ID          int64             `json:"id"`
		Name        string            `json:"name"`
		Label       string            `json:"label"` // 品牌名（关联字段统一显示它）
		Version     string            `json:"version"`
		Author      string            `json:"author"`
		Icon        string            `json:"icon"` // 包内相对路径（空 = 前端兜底）
		Running     bool              `json:"running"`
		Capability  []string          `json:"capabilities"`
		AuthMethods []*authMethodView `json:"auth_methods"`
		// 实例级设置 JSON Schema（空 = 实例只有 name + base_url）
		InstanceSchema string `json:"instance_schema,omitempty"`
		// 契约版本与多实例能力（旧契约 / 未声明 instances 的插件只有默认实例，前端不展示实例选择）
		ProtocolVersion int32  `json:"protocol_version"`
		MultiInstance   bool   `json:"multi_instance"`
		Runtime         string `json:"runtime,omitempty"` // 空=Go；"lua"=脚本插件（取自落盘 manifest，非握手）
	}
	out := []pluginView{}
	for _, mf := range s.plugins.Installed() {
		v := pluginView{Name: mf.Name, Label: labelOf(mf.Label, mf.Name), Version: mf.Version, Author: mf.Author,
			ProtocolVersion: mf.ProtocolVersion, Runtime: mf.Runtime}
		var rec model.Plugin // DB id（建分组/规则时引用）+ 停止时的 manifest 快照
		if err := s.db.Where("name = ?", mf.Name).First(&rec).Error; err == nil {
			v.ID = rec.ID
		}
		m, protocol := (*pb.Manifest)(nil), rec.ProtocolVersion
		if inst, ok := s.plugins.Get(mf.Name); ok {
			v.Running = true
			m, protocol = inst.Manifest, inst.Protocol
		} else if snap := (&pb.Manifest{}); protojson.Unmarshal([]byte(rec.ManifestJSON), snap) == nil && snap.Name != "" {
			m = snap
		}
		if m != nil {
			v.Label, v.Version, v.Author = brandName(m), m.Version, m.Author
			v.Capability = m.Capabilities
			v.InstanceSchema = m.InstanceSchema
			v.ProtocolVersion = protocol
			v.MultiInstance = plugmgr.ManifestMultiInstance(m, protocol)
			for _, am := range m.AuthMethods {
				v.AuthMethods = append(v.AuthMethods, viewAuthMethod(am))
			}
		}
		// icon：以落盘文件为准（前端 <img> 直接引用，免鉴权静态端点）
		if _, ok := s.plugins.IconFile(mf.Name); ok {
			v.Icon = "/assets/plugins/" + mf.Name + "/icon"
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"plugins": out})
}

// lazyStartTimeout 懒启动整体超时：go-plugin 子进程 spawn + 握手实测 ~1s 量级，
// 10s 余量覆盖低端机/负载尖峰（app.Start 自启批处理并发 spawn 期），防止添加账号
// 请求无限挂住（admin HTTP server 无 WriteTimeout，须由本超时兜底）。
const lazyStartTimeout = 10 * time.Second

// ensurePluginRunning 懒启动（v1.4.10 回归根治）：v1.4.7 起开机自启仅拉「已配置账号」
// 的插件（app.Start → plugmgr.AutoStarts 双重过滤），未运行插件成为常态——添加账号
// 链路（auth-methods / accounts/login）此前对未运行插件直接 gRPC 失败 → 404「未启用
// 该插件」，与面板深链「点供应商加账号」组合即用户报告的回归。
// 此处对未运行插件按需拉起子进程：目录定位 + Start 复用自启路径（app.go 自启批处理
// 与 startPlugin 均走 plugmgr.Manager.Start），成功后清持久化停止态并刷新模型目录，
// 对添加账号场景完全透明；整体超时 10s，失败/超时返回明确错误（调用方一律 503）。
// 已运行（含崩溃自愈重启）直接放行；单飞锁 + 双检防并发重复 spawn。
func (s *Server) ensurePluginRunning(ctx context.Context, name string) error {
	if _, ok := s.plugins.Get(name); ok {
		return nil
	}
	s.lazyMu.Lock()
	defer s.lazyMu.Unlock()
	if _, ok := s.plugins.Get(name); ok { // 等锁期间已被并发请求拉起
		return nil
	}
	bins, err := s.plugins.Scan()
	if err != nil {
		return fmt.Errorf("插件 %q 启动失败: 扫描插件目录: %w", name, err)
	}
	for _, bin := range bins {
		if filepath.Base(bin) != name { // Scan 返回插件目录，键控与 startPlugin/AutoStarts 一致
			continue
		}
		startCtx, cancel := context.WithTimeout(ctx, lazyStartTimeout)
		defer cancel()
		type startResult struct {
			err error
		}
		done := make(chan startResult, 1) // 带缓冲：超时放行后 Start 仍可在后台收敛，goroutine 不阻塞
		go func() {
			_, err := s.plugins.Start(startCtx, bin)
			done <- startResult{err: err}
		}()
		select {
		case res := <-done:
			if res.err != nil {
				logsink.Printf("[plugin] lazy start %s failed: %v", name, res.err)
				return fmt.Errorf("插件 %q 启动失败: %w", name, res.err)
			}
		case <-startCtx.Done():
			logsink.Printf("[plugin] lazy start %s timed out after %s", name, lazyStartTimeout)
			return fmt.Errorf("插件 %q 启动超时（%s），请稍后重试或到插件页手动启动", name, lazyStartTimeout)
		}
		s.plugins.Resume(name) // 与 startPlugin 同语义：显式使用即恢复自启资格
		s.plugins.RefreshCatalog(startCtx)
		logsink.Printf("[plugin] lazy started %s (add-account path)", name)
		return nil
	}
	return fmt.Errorf("插件 %q 未安装或二进制缺失，无法启动", name)
}

// authMethods GET /admin/plugins/{name}/auth-methods — 授权方式详情（渲染 tab + 表单）。
func (s *Server) authMethods(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.ensurePluginRunning(r.Context(), name); err != nil {
		httpErrorJSON(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	methods, err := s.accounts.AuthMethods(name)
	if err != nil {
		httpErrorJSON(w, http.StatusNotFound, err.Error())
		return
	}
	var out []*authMethodView
	for _, m := range methods {
		out = append(out, viewAuthMethod(m))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"auth_methods": out})
}

// submitLogin POST /admin/accounts/login — 提交一步登录（首步或后续步）。
// body: {plugin, method_id, form: {..}, state: "<base64>", instance_id?}（instance_id 缺省 = 插件默认实例）
func (s *Server) submitLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Plugin     string            `json:"plugin"`
		MethodID   string            `json:"method_id"`
		Form       map[string]string `json:"form"`
		State      string            `json:"state"`
		InstanceID int64             `json:"instance_id"`
	}
	if !readBody(w, r, &body) {
		return
	}
	var state []byte
	if body.State != "" {
		var err error
		state, err = base64.StdEncoding.DecodeString(body.State)
		if err != nil {
			http.Error(w, `{"error":"invalid state"}`, http.StatusBadRequest)
			return
		}
	}
	// 未运行插件先按需拉起（v1.4.10 懒启动）：插件多步登录/轮询反复走本接口，
	// 拉起仅在首个请求发生，之后 Get 命中直通。
	if err := s.ensurePluginRunning(r.Context(), body.Plugin); err != nil {
		httpErrorJSON(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	outcome, err := s.accounts.SubmitLogin(r.Context(), body.Plugin, body.MethodID, body.Form, state, body.InstanceID)
	if err != nil {
		// 业务错误（验证码错误/凭据格式/上游拒绝）用 400：401 专属管理员会话失效，
		// 前端见 401 会清 token 跳登录页
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	if outcome.Next != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"done": false, "next": viewNextStep(outcome.Next)})
		return
	}
	// 建档完成：按插件账号级任务能力自动生成规则（默认停用，任务页手动启用），
	// 并触发自动配置引擎（v1.3.0 ②：建分组/模型路由/默认密钥，结果随响应回显，
	// 应用据此展示「本地/局域网端点 + 密钥 + 模型映射说明」）。
	if outcome.AccountID > 0 {
		s.engine.EnsureAccountRules(r.Context(), body.Plugin, outcome.AccountID)
	}
	resp := map[string]interface{}{"done": true, "account_id": outcome.AccountID}
	if outcome.AccountID > 0 {
		if ac := s.autoConfigForAccount(body.Plugin, outcome.AccountID); ac != nil {
			resp["auto_config"] = ac
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// listAccounts GET /admin/accounts?plugin=stub
func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	pluginName := r.URL.Query().Get("plugin")
	var accts []model.Account
	q := s.db
	if pluginName != "" {
		var p model.Plugin
		if err := s.db.Where("name = ?", pluginName).First(&p).Error; err != nil {
			http.Error(w, `{"error":"unknown plugin"}`, http.StatusNotFound)
			return
		}
		q = q.Where("plugin_id = ?", p.ID)
	}
	if err := q.Order("id").Find(&accts).Error; err != nil {
		http.Error(w, `{"error":"db"}`, http.StatusInternalServerError)
		return
	}
	type acctView struct {
		ID          int64   `json:"id"`
		PluginID    int64   `json:"plugin_id"`
		InstanceID  int64   `json:"instance_id"`
		GroupIDs    []int64 `json:"group_ids"`
		Name        string  `json:"display_name"`
		Status      string  `json:"status"`
		PauseReason string  `json:"pause_reason"`
		PausedUntil *string `json:"paused_until"`
		RefreshAt   *string `json:"last_refresh_at"`
		Credits     *struct {
			Remaining string `json:"remaining,omitempty"`
			Total     string `json:"total,omitempty"`
		} `json:"credits,omitempty"`
	}
	var out []acctView
	for _, a := range accts {
		v := acctView{ID: a.ID, PluginID: a.PluginID, InstanceID: a.InstanceID, GroupIDs: accountGroupIDs(s.db, a.ID), Name: a.DisplayName,
			Status: a.Status, PauseReason: a.PauseReason}
		if a.PausedUntil != nil {
			t := a.PausedUntil.Format("2006-01-02T15:04:05Z07:00")
			v.PausedUntil = &t
		}
		if a.LastRefreshAt != nil {
			t := a.LastRefreshAt.Format("2006-01-02T15:04:05Z07:00")
			v.RefreshAt = &t
		}
		// 积分列：credits_json 快照里的剩余/总（插件解析了才有，无则不渲染该列）
		if a.CreditsJSON != "" {
			var c struct {
				Total     string `json:"total"`
				Remaining string `json:"remaining"`
			}
			if json.Unmarshal([]byte(a.CreditsJSON), &c) == nil && (c.Total != "" || c.Remaining != "") {
				v.Credits = &struct {
					Remaining string `json:"remaining,omitempty"`
					Total     string `json:"total,omitempty"`
				}{Remaining: c.Remaining, Total: c.Total}
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"accounts": out})
}

// deleteAccount DELETE /admin/accounts/{id} — 级联删除只指向该账号的任务规则与执行历史。
func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id := parseInt(r.PathValue("id"))
	var acct model.Account
	if err := s.db.First(&acct, id).Error; err != nil {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	sc := scopeAccount(s.db, id)
	impact := s.impact(sc)
	if err := s.cascadeDelete(sc); err != nil {
		http.Error(w, `{"error":"db"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": true, "impact": impact})
}

// refreshAccount POST /admin/accounts/{id}/refresh
func (s *Server) refreshAccount(w http.ResponseWriter, r *http.Request) {
	acct, err := s.accounts.Refresh(r.Context(), parseInt(r.PathValue("id")))
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": acct.Status})
}

// ---------- 视图映射（proto → JSON，前端直接消费） ----------

type authFieldView struct {
	Name        string            `json:"name"`
	Label       map[string]string `json:"label"`
	Type        string            `json:"type"`
	Required    bool              `json:"required"`
	Placeholder string            `json:"placeholder"`
}

type authMethodView struct {
	ID           string            `json:"id"`
	Label        map[string]string `json:"label"`
	Fields       []authFieldView   `json:"fields"`
	Capabilities []string          `json:"capabilities"`
	Callback     string            `json:"callback,omitempty"` // auto / wait / auto_wait
}

type nextStepView struct {
	Action string            `json:"action"`
	URL    string            `json:"url,omitempty"`
	Prompt map[string]string `json:"prompt,omitempty"`
	Fields []authFieldView   `json:"fields,omitempty"`
	State  string            `json:"state,omitempty"`
	Wait   bool              `json:"wait,omitempty"`
}

func viewAuthMethod(m *pb.AuthMethod) *authMethodView {
	v := &authMethodView{ID: m.Id, Label: m.Label, Capabilities: m.Capabilities, Callback: m.Callback}
	for _, f := range m.Fields {
		v.Fields = append(v.Fields, viewAuthField(f))
	}
	return v
}

func viewAuthField(f *pb.AuthField) authFieldView {
	return authFieldView{
		Name: f.Name, Label: f.Label, Type: f.Type,
		Required: f.Required, Placeholder: f.Placeholder,
	}
}

func viewNextStep(n *pb.LoginNextStep) *nextStepView {
	v := &nextStepView{Action: n.Action, URL: n.Url, Prompt: n.Prompt, Wait: n.Wait}
	for _, f := range n.Fields {
		v.Fields = append(v.Fields, viewAuthField(f))
	}
	if len(n.State) > 0 {
		v.State = base64.StdEncoding.EncodeToString(n.State)
	}
	return v
}

// ---------- 工具 ----------

// brandName 插件品牌名：manifest.label.zh 优先，缺省用插件 id。
func brandName(m *pb.Manifest) string { return labelOf(m.Label, m.Name) }

// labelOf 多语言品牌名取 zh，缺省回退插件名。
func labelOf(label map[string]string, name string) string {
	if v, ok := label["zh"]; ok && v != "" {
		return v
	}
	return name
}

// pluginBrandByID 品牌名 by 插件 id（优先运行实例，回退 DB manifest 快照解析）。
func (s *Server) pluginBrandByID(pluginID int64) string {
	var p model.Plugin
	if err := s.db.First(&p, pluginID).Error; err != nil {
		return ""
	}
	if inst, ok := s.plugins.Get(p.Name); ok {
		return brandName(inst.Manifest)
	}
	var m pb.Manifest
	if err := protojson.Unmarshal([]byte(p.ManifestJSON), &m); err == nil {
		if v := m.Label["zh"]; v != "" {
			return v
		}
	}
	return p.Name
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// httpErrorJSON JSON 安全的错误响应：err 文本（插件名/上游错误）可能含引号，
// 沿用 `{"error":"`+err+`"}` 手工拼接会产生非法 JSON，前端只能拿到原文兜底；
// 统一经 encoding/json 编码。
func httpErrorJSON(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readBody(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(v); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return false
	}
	return true
}

func parseInt(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// SetTempDir 注入临时目录基座（app.Start 从 Options.CacheDir 传入）。
// 安卓侧必须指向应用缓存目录：原项目三处 os.CreateTemp("", ...) 依赖 TMPDIR/TMP 环境变量，
// 在安卓默认落到不可写的 /data/local/tmp，导致市场下载、离线上传、备份导出全部失败。
func (s *Server) SetTempDir(dir string) {
	if dir != "" {
		s.tmpDir = dir
	}
}

// SetRuntimeInfo 注入运行时信息提供者（app.Start 装配）：system/info 响应合并
// gateway_port / port_change / lan_enabled / lan_endpoint（端口持久化① + 局域网④）。
func (s *Server) SetRuntimeInfo(fn func() map[string]interface{}) { s.runtimeInfo = fn }

// SetLanReloader 注入局域网监听热切换回调（app.Start 装配）：putSettings 改动
// network.lan_enabled 后触发，核心即时开/关局域网监听，无需重启。
func (s *Server) SetLanReloader(fn func()) { s.onLanChange = fn }
