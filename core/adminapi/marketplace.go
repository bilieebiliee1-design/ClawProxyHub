// marketplace.go — 插件市场与包安装。
package adminapi

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"io.nexport.gateway/core/model"
	"io.nexport.gateway/core/plugmgr"
	"io.nexport.gateway/core/setting"
)

// 离线插件市场索引（线上索引不可达时的兜底清单）
// 内容是 ClawProxyHubPlugins 仓库 index.json 的快照，随核心发布同步；sha256 为空表示跳过校验。
//
//go:embed offline_market.json
var offlineMarketJSON []byte

// marketHTTPClient 市场索引请求（小 JSON）：连接 / TLS / 响应头分段限时，整体 15s 兜底。
// 此前仅整体 20s 且无连接级超时——对不可达源要等满 20s 才报错，多源聚合再串行叠加，
// 是市场 / 源列表接口慢响应的根因之一。
var marketHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          8,
	},
}

// downloadHTTPClient 插件包下载（可能几十 MB 走 GitHub 代理）：不设整体超时，
// 只在连接 / 响应头 / 空闲阶段设限——慢速大包不被整体 timeout 砍断（升级超时根因）。
var downloadHTTPClient = &http.Client{
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	},
}

// offlineMarket 解析内置离线索引。
func offlineMarket() []MarketEntry {
	var entries []MarketEntry
	json.Unmarshal(offlineMarketJSON, &entries)
	return entries
}

// isGitHubURL 是否 GitHub 域（这些域在国内网络常见不可达，需要代理加速）。
func isGitHubURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "github.com" || host == "raw.githubusercontent.com" ||
		strings.HasSuffix(host, ".githubusercontent.com")
}

// withGitHubProxy 给 GitHub URL 套代理前缀（ghproxy 风格：proxy + 完整原始 URL）。
func (s *Server) withGitHubProxy(raw string) string {
	proxy := s.settings.GitHubProxy()
	if proxy == "" || !isGitHubURL(raw) {
		return raw
	}
	return strings.TrimSuffix(proxy, "/") + "/" + raw
}

// MarketEntry 市场 index.json 的单条目。
// 同名插件以 author+name 判定同插件（name 不保证全局唯一）。
type MarketEntry struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Author      string            `json:"author,omitempty"`
	Label       map[string]string `json:"label"` // 品牌名（多语言，zh 优先）
	PublishedAt string            `json:"published_at,omitempty"`
	DownloadURL string            `json:"download_url"`
	SHA256      string            `json:"sha256"`
	Source      string            `json:"source,omitempty"`  // 来源插件源名（聚合时由核心填入，索引里不含）
	Runtime     string            `json:"runtime,omitempty"` // 空=Go 插件；"lua"=脚本插件
}

// marketView 市场条目 + 本机安装状态（前端直接消费）。
// 安卓适配三字段仅安卓运行时填充（安卓化方案③：市场标注前置，避免用户点安装、
// 下载完成后才被核心以 noexec/未内置拒绝）；桌面端恒零值，前端按响应顶层
// android 标志决定是否消费（旧核心无该字段 → 前端按桌面行为渲染，向后兼容）。
type marketView struct {
	MarketEntry
	Installed bool   `json:"installed"`     // 已安装（含同版本；按落盘 manifest 判定，含已停止插件）
	Updatable bool   `json:"updatable"`     // 已安装且市场版本更新
	LocalVer  string `json:"local_version"` // 本机已装版本
	// 安卓端适配标注（仅 android=true 时有意义）：
	AndroidBuiltin   bool   `json:"android_builtin"`                      // 随 APK 内置（二进制随应用更新，市场包版本仅 manifest 层）
	AndroidVersion   string `json:"android_version,omitempty"`            // 内置版本（内置注册表 manifest，非市场包版本）
	AndroidSupported bool   `json:"android_supported"`                    // 安卓端可安装/升级：Lua 恒可；Go 插件 = 内置
	AndroidReason    string `json:"android_unsupported_reason,omitempty"` // 不可安装 / 不可在线升级的明确原因（前端直接展示）
	// AndroidReinstallable 已内置但未安装（v1.3.0 方案 ④：内置插件被卸载后）——前端显示
	// 「已内置，未安装」并提供一键本地重装按钮（POST /admin/plugins/{name}/reinstall-builtin，
	// 从 APK 内置二进制恢复，无需下载）。Installed 的判定源已从"运行中"改为"落盘 manifest"，
	// 停止的内置插件不会被误判为本条状态。
	AndroidReinstallable bool `json:"android_reinstallable"`
}

// pluginKey 同插件判定键：author/name（author 缺省回退 name，兼容旧索引）。
func pluginKey(author, name string) string {
	if author == "" {
		return name
	}
	return author + "/" + name
}

// marketplace GET /admin/plugins/marketplace?source= — 指定源只拉该源（前端按源懒加载）；
// 不指定则聚合全部启用源。一个源都不可达时回落内置离线清单。
// 每条带来源与本机安装状态（installed/updatable，按 author+name 判定同插件）；
// 顶层 android=true 表示核心跑在安卓（前端据此消费各条目的安卓适配标注）。
func (s *Server) marketplace(w http.ResponseWriter, r *http.Request) {
	entries, online := s.fetchMarket(r.URL.Query().Get("source"))
	source := "offline"
	if online {
		source = "online"
	}
	// 本机已装版本：落盘 manifest 为准（v1.3.0 ④修正——此前用「运行中」实例判定，
	// 停止的插件被误判为未安装；磁盘即真相，与插件页 listPlugins 同源）
	local := map[string]string{}
	for _, mf := range s.plugins.Installed() {
		local[pluginKey(mf.Author, mf.Name)] = mf.Version
	}
	android := runtime.GOOS == "android"
	out := buildMarketView(entries, local, android)
	writeJSON(w, http.StatusOK, map[string]interface{}{"plugins": out, "source": source, "android": android})
}

// buildMarketView 市场视图构建（安卓标注逻辑独立成函数便于测试）。
// android=false（桌面核心）：市场 Go 插件照常可安装，适配字段一律零值；
// android=true：Lua 插件标可安装；Go 插件按内置注册表标注（plugmgr.BuiltinVersion，
// 内置即"随 APK 更新"，市场包不管多新都不出安装/升级按钮），非内置标不可安装。
func buildMarketView(entries []MarketEntry, local map[string]string, android bool) []marketView {
	out := make([]marketView, 0, len(entries))
	for _, e := range entries {
		v := marketView{MarketEntry: e, LocalVer: local[pluginKey(e.Author, e.Name)]}
		if v.LocalVer != "" {
			v.Installed = true
			// 升级语义两重门：市场版本须严格更新（修复旧逻辑"版本不同即标升级"——
			// 内置 0.1.5 对市场 0.1.4 会误报可升级）；安卓 Go 插件二进制随 APK 内置，
			// 一律不出在线升级按钮（更新随应用走，避免点了必然失败的按钮）。
			v.Updatable = e.Version != "" && compareSemver(e.Version, v.LocalVer) > 0 &&
				!(android && e.Runtime != "lua")
		}
		if android {
			annotateAndroid(&v, e)
			// 「已内置，未安装」（v1.3.0 ④）：内置注册表命中但落盘目录缺失（被用户卸载，
			// 墓碑在位 / 数据目录被清）→ 前端显示该状态并提供一键本地重装（内置二进制
			// 来自 APK，重装无网络依赖）；已安装时不置位（升级语义照旧随应用更新）。
			v.AndroidReinstallable = v.AndroidBuiltin && !v.Installed
			// 不支持/不可升级的明确原因（前端直接展示，与 android_supported/updatable 同源判定）：
			// 非内置 Go → 不可安装的原因；内置 Go 且市场版本比内置新 → 不可在线升级的原因
			// （二进制随 APK 更新；此时 updatable 已为 false，按钮不可点，文案解释为什么）。
			switch {
			case !v.AndroidSupported && e.Runtime != "lua":
				v.AndroidReason = "未随 APK 内置：安卓应用数据目录禁止执行二进制（targetSdk 29+ noexec），Go 插件二进制只能随 APK 预打包，无法在线安装"
			case v.AndroidSupported && e.Runtime != "lua" && v.Installed &&
				e.Version != "" && compareSemver(e.Version, v.AndroidVersion) > 0:
				v.AndroidReason = "市场版本 v" + e.Version + " 比内置 v" + v.AndroidVersion + " 新：Go 插件二进制随 APK 更新，无法在线升级，请等待应用更新"
			}
		}
		out = append(out, v)
	}
	return out
}

// annotateAndroid 单条目安卓适配标注：Lua 不受数据目录 noexec 限制照常安装/升级；
// Go 插件只能随 APK 预打包（nativeLibraryDir），内置的标内置版本，未内置的标不支持。
func annotateAndroid(v *marketView, e MarketEntry) {
	if e.Runtime == "lua" {
		v.AndroidSupported = true
		return
	}
	if bv := plugmgr.BuiltinVersion(e.Name); bv != "" {
		v.AndroidBuiltin = true
		v.AndroidVersion = bv
		v.AndroidSupported = true
	}
}

// installMarket POST /admin/plugins/install-market — body: {name, author, source?}
// 同插件判定 = author+name（name 不保证全局唯一，与市场列表/已装列表同一判定键）；
// source 指定来源（多源同名时必填），缺省取第一个匹配。
// 响应是 NDJSON 进度流（下载可能走 GitHub 代理、耗时不定）：
// {phase:"downloading",received,total} → {phase:"installing"} → {phase:"starting"} → {installed} 或 {error}。
func (s *Server) installMarket(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Author string `json:"author"`
		Source string `json:"source"`
	}
	if !readBody(w, r, &body) || body.Name == "" || body.Author == "" {
		http.Error(w, `{"error":"name and author required"}`, http.StatusBadRequest)
		return
	}
	entries, _ := s.fetchMarket(body.Source)
	var entry *MarketEntry
	for i := range entries {
		if pluginKey(entries[i].Author, entries[i].Name) != pluginKey(body.Author, body.Name) {
			continue
		}
		if body.Source == "" || entries[i].Source == body.Source {
			entry = &entries[i]
			break
		}
	}
	if entry == nil {
		http.Error(w, `{"error":"not found in marketplace"}`, http.StatusNotFound)
		return
	}
	// 安卓前置拒绝：非内置 / 内置二进制缺失的 Go 插件在下载之前就拒绝（HTTP 422 +
	// 明确原因），与市场列表 android_supported=false 同一判定源（AndroidGoInstallCheck），
	// 不再"下载完成后才被拒"；桌面端恒放行（平台差异由 InstallZip 把关）。
	if entry.Runtime != "lua" {
		if err := s.plugins.AndroidGoInstallCheck(entry.Name); err != nil {
			http.Error(w, `{"error":`+strconv.Quote(err.Error())+`}`, http.StatusUnprocessableEntity)
			return
		}
	}

	dl := *entry // 下载 URL 套 GitHub 代理（索引里的原始地址保持干净）
	dl.DownloadURL = s.withGitHubProxy(entry.DownloadURL)
	pw := newProgressWriter(w)
	// r.Context() 随客户端断开而取消：前端点「取消」abort fetch → 连接断 → 下载中断
	zipPath, err := downloadToTemp(r.Context(), s.tmpDir, &dl, func(received, total int64) {
		pw.send(map[string]interface{}{"phase": "downloading", "received": received, "total": total})
	})
	if err != nil {
		pw.send(map[string]string{"error": err.Error()})
		return
	}
	defer os.Remove(zipPath)
	name, err := s.installZip(r.Context(), zipPath, entry.Source, func(phase string) {
		pw.send(map[string]string{"phase": phase})
	})
	if err != nil {
		pw.send(map[string]string{"error": err.Error()})
		return
	}
	pw.send(map[string]string{"installed": name})
}

// progressWriter NDJSON 进度流：每行一个 JSON 事件，写后即刷（前端逐行渲染）。
type progressWriter struct {
	w  http.ResponseWriter
	fl http.Flusher
}

func newProgressWriter(w http.ResponseWriter) *progressWriter {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // 反代不缓冲
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	return &progressWriter{w: w, fl: fl}
}

func (p *progressWriter) send(v interface{}) {
	json.NewEncoder(p.w).Encode(v) // Encode 自带换行
	if p.fl != nil {
		p.fl.Flush()
	}
}

// installUpload POST /admin/plugins/install-upload — multipart 上传 .cphplugin。
func (s *Server) installUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, `{"error":"invalid upload"}`, http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("package")
	if err != nil {
		http.Error(w, `{"error":"missing package field"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()

	tmp, err := os.CreateTemp(s.tmpDir, "cph-*.cphplugin")
	if err != nil {
		http.Error(w, `{"error":"temp file"}`, http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		http.Error(w, `{"error":"save upload"}`, http.StatusInternalServerError)
		return
	}
	tmp.Close()
	name, err := s.installZip(r.Context(), tmp.Name(), "", nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"installed": name})
}

// installZip 安装本地包文件并刷新目录（市场 / 上传共用），onPhase 透传给 InstallZip 报进度。
// source 为来源插件源名：官方源/手动上传为空装在根目录，其他源按源名建命名空间目录；
// 同名插件已从其他来源安装时拒绝（插件身份 = manifest.name，目录隔离不改变身份唯一性）。
func (s *Server) installZip(ctx context.Context, zipPath, source string, onPhase func(string)) (string, error) {
	if source == setting.OfficialSourceName {
		source = ""
	}
	// lua 插件安装网关：设置里关闭 Lua 时拒装（覆盖市场/上传两条路径）
	if zipManifestRuntime(zipPath) == "lua" && !s.settings.LuaEnabled() {
		return "", fmt.Errorf("Lua 插件安装已在系统设置中禁用")
	}
	name, err := s.plugins.InstallZip(ctx, zipPath, source, onPhase)
	if err != nil {
		return "", err
	}
	s.db.Exec(`INSERT OR IGNORE INTO plugins (name, version, author, protocol_version, manifest_json, source) VALUES (?,?,?,?,?,?)`, name, "", "cph", 0, "{}", source)
	s.db.Exec(`UPDATE plugins SET source = ? WHERE name = ?`, source, name)
	s.plugins.RefreshCatalog(ctx)
	return name, nil
}

// zipManifestRuntime 读 .cphplugin 包内 manifest.json 的 runtime（读不到即空=Go 插件）。
func zipManifestRuntime(zipPath string) string {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return ""
	}
	defer zr.Close()
	for _, f := range zr.File {
		if filepath.Base(f.Name) != "manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return ""
		}
		defer rc.Close()
		var mf struct {
			Runtime string `json:"runtime"`
		}
		_ = json.NewDecoder(rc).Decode(&mf)
		return mf.Runtime
	}
	return ""
}

// uploadLuahost POST /admin/plugins/luahost-upload — 手动上传共享 luahost 二进制（multipart file），
// 覆盖 data/hosts 下当前平台 luahost（打 .manual 标记，不再被内置字节自动刷新）并重启在跑的 lua 插件。
func (s *Server) uploadLuahost(w http.ResponseWriter, r *http.Request) {
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, `{"error":"missing file"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 128<<20)) // 128MB 上限
	if err != nil || len(data) == 0 {
		http.Error(w, `{"error":"read file"}`, http.StatusBadRequest)
		return
	}
	if err := s.plugins.ReplaceLuahost(data); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// stopPlugin POST /admin/plugins/{name}/stop — 停止并写持久化状态（重启核心保持停止）。
func (s *Server) stopPlugin(w http.ResponseWriter, r *http.Request) {
	s.plugins.Stop(r.PathValue("name"), true)
	writeJSON(w, http.StatusOK, map[string]bool{"stopped": true})
}

// reinstallBuiltin POST /admin/plugins/{name}/reinstall-builtin — 内置插件一键本地重装
// （v1.3.0 方案 ④：市场「已内置，未安装」的重装按钮）。
// 不走市场下载：清卸载墓碑 → 重新落盘 manifest/icon（目录缺失时）→ 启动并恢复自启，
// 二进制直接来自 APK nativeLibraryDir（签名保护）。已在运行视为重装完成（幂等）。
// 非内置 / 桌面运行时 / 内置二进制缺失 → 400 + 可读原因。
func (s *Server) reinstallBuiltin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.plugins.Get(name); ok {
		writeJSON(w, http.StatusOK, map[string]interface{}{"installed": name, "running": true})
		return
	}
	inst, err := s.plugins.ReinstallBuiltin(r.Context(), name)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	s.plugins.RefreshCatalog(r.Context())
	writeJSON(w, http.StatusOK, map[string]interface{}{"installed": inst.Name, "running": true})
}

// startPlugin POST /admin/plugins/{name}/start — 从插件目录重新启动（已在运行则直接返回）；
// 成功即清除持久化停止状态（下次重启核心照常拉起）。
func (s *Server) startPlugin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.plugins.Get(name); ok {
		writeJSON(w, http.StatusOK, map[string]bool{"started": true})
		return
	}
	bins, err := s.plugins.Scan()
	if err != nil {
		http.Error(w, `{"error":"scan"}`, http.StatusInternalServerError)
		return
	}
	for _, bin := range bins {
		if filepath.Base(bin) == name { // Scan 返回插件目录
			if _, err := s.plugins.Start(r.Context(), bin); err != nil {
				http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
				return
			}
			s.plugins.Resume(name)
			s.plugins.RefreshCatalog(r.Context())
			writeJSON(w, http.StatusOK, map[string]bool{"started": true})
			return
		}
	}
	http.Error(w, `{"error":"plugin binary not found"}`, http.StatusNotFound)
}

// uninstallPlugin DELETE /admin/plugins/{name} — 停止 + 删除文件 + 级联清 DB（实例/分组/账号/任务）。
func (s *Server) uninstallPlugin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.plugins.Uninstall(name); err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	impact := deleteImpact{Routes: []string{}, Keys: []string{}}
	var p model.Plugin
	if err := s.db.Where("name = ?", name).First(&p).Error; err == nil {
		sc := scopePlugin(s.db, p.ID)
		impact = s.impact(sc)
		if err := s.cascadeDelete(sc); err != nil {
			http.Error(w, `{"error":"db"}`, http.StatusInternalServerError)
			return
		}
		s.db.Delete(&p)
	}
	s.db.Where("plugin = ?", name).Delete(&model.PluginStore{}) // 清该插件的 KV 状态
	s.plugins.RefreshCatalog(r.Context())
	writeJSON(w, http.StatusOK, map[string]interface{}{"uninstalled": true, "impact": impact})
}

// enabledSources 启用的插件源（一个都没有时退回 config 默认官方地址，避免误配把市场关死）。
func (s *Server) enabledSources() []setting.PluginSource {
	var out []setting.PluginSource
	for _, src := range s.settings.PluginSources() {
		if src.Enabled && strings.TrimSpace(src.URL) != "" {
			out = append(out, src)
		}
	}
	if len(out) == 0 {
		out = []setting.PluginSource{{Name: setting.OfficialSourceName, URL: s.marketplaceURL, Enabled: true}}
	}
	return out
}

// fetchMarket 拉插件索引（走 GitHub 代理配置），每条标记来源；
// only 非空只拉该源，否则并行聚合全部启用源（此前逐源串行回源，一个不可达源拖慢
// 整个响应 20s × 源数，是多源聚合慢响应的根因）。一个源都拉不到时回落内置离线清单
// （仅官方源语义），online=false。
func (s *Server) fetchMarket(only string) (entries []MarketEntry, online bool) {
	srcs := s.enabledSources()
	type fetched struct {
		list []MarketEntry
	}
	results := make([]fetched, len(srcs))
	var wg sync.WaitGroup
	for i, src := range srcs {
		if only != "" && src.Name != only {
			continue
		}
		wg.Add(1)
		go func(i int, src setting.PluginSource) {
			defer wg.Done()
			list, err := fetchIndexCached(s.withGitHubProxy(strings.TrimSpace(src.URL)))
			if err != nil {
				return // 单源失败不拖垮聚合（其余源照常返回）
			}
			for j := range list {
				list[j].Source = src.Name
			}
			results[i] = fetched{list: list}
		}(i, src)
	}
	wg.Wait()
	for i := range results { // 按源配置顺序聚合（稳定输出）
		if results[i].list == nil {
			continue
		}
		entries = append(entries, results[i].list...)
		online = true
	}
	if !online && (only == "" || only == setting.OfficialSourceName) {
		entries = offlineMarket()
		for i := range entries {
			entries[i].Source = setting.OfficialSourceName
		}
	}
	return entries, online
}

// indexCacheTTL / indexFailTTL 索引缓存时长：成功缓存 60s（面板频繁切页 / 源列表 +
// 市场连续请求不再回源）；失败缓存 10s（网络抖动快速重试，又不至于同秒打满上游）。
var (
	indexCacheTTL = 60 * time.Second // var 便于测试缩短
	indexFailTTL  = 10 * time.Second
)

// indexCache 索引 TTL 缓存（键 = 经代理展开后的完整 URL；fetchIndexCached 单飞降级为
// 互斥去重写——TTL 内的并发读全部命中缓存，miss 时并发回源至多几个重复请求，可接受）。
var (
	indexCacheMu sync.Mutex
	indexCache   = map[string]indexCacheEntry{}
	// refreshing 后台刷新去重（SWR）：同一 URL 同时至多一个回源 goroutine。
	refreshing = map[string]bool{}
)

type indexCacheEntry struct {
	entries []MarketEntry
	err     error
	at      time.Time
	// lastAttempt 最近一次回源尝试（含后台 SWR 刷新）时间：后台刷新失败时保留
	// 陈旧成功值供继续回陈旧，并按 indexFailTTL 限频重试，避免面板高频轮询打满上游。
	lastAttempt time.Time
}

// fetchIndexCached 拉一个源的 index.json，带短 TTL 缓存 + stale-while-revalidate：
// 市场聚合 / 源列表 / 探测三处共用（此前三处每次都无条件回源，是慢响应与无谓流量
// 的根因之一）。语义：
//   - TTL 内：直接命中；
//   - 过期且有历史成功值：立即回陈旧值（不阻塞响应），后台异步回源供下一次请求
//     （验收回归：面板冷路径此前被实时回源阻塞，模拟器网络下实测 2.1–3.7s 超目标）；
//     后台刷新以 refreshing 去重，失败保留陈旧成功值并按 indexFailTTL 限频重试；
//   - 过期且历史值为失败 / 无缓存：同步回源（失败结果仍缓存 indexFailTTL，
//     「编辑重试即刻生效」语义不变）。
func fetchIndexCached(url string) ([]MarketEntry, error) {
	indexCacheMu.Lock()
	c, ok := indexCache[url]
	indexCacheMu.Unlock()
	if ok {
		ttl := indexCacheTTL
		if c.err != nil {
			ttl = indexFailTTL
		}
		if time.Since(c.at) < ttl {
			return c.entries, c.err
		}
		// 过期：SWR 仅对历史成功值生效（失败值回陈旧无意义，走同步重试）
		if c.err == nil {
			indexCacheMu.Lock()
			retryGate := time.Since(c.lastAttempt) < indexFailTTL
			indexCacheMu.Unlock()
			if retryGate {
				return c.entries, nil // 刚失败过：限频窗口内继续回陈旧，不重试
			}
			go refreshIndexCached(url)
			return c.entries, nil
		}
	}
	entries, err := fetchIndex(url)
	now := time.Now()
	indexCacheMu.Lock()
	indexCache[url] = indexCacheEntry{entries: entries, err: err, at: now, lastAttempt: now}
	indexCacheMu.Unlock()
	return entries, err
}

// refreshIndexCached SWR 后台刷新：成功则覆盖缓存（下次请求拿到新值）；失败保留
// 陈旧成功值，仅推进 lastAttempt（限频重试起点），不把错误写进缓存顶掉好值。
func refreshIndexCached(url string) {
	indexCacheMu.Lock()
	if refreshing[url] {
		indexCacheMu.Unlock()
		return
	}
	refreshing[url] = true
	indexCacheMu.Unlock()
	defer func() {
		indexCacheMu.Lock()
		delete(refreshing, url)
		indexCacheMu.Unlock()
	}()
	entries, err := fetchIndex(url)
	now := time.Now()
	indexCacheMu.Lock()
	defer indexCacheMu.Unlock()
	if err != nil {
		if c, ok := indexCache[url]; ok && c.err == nil {
			// 保留陈旧成功值，仅推进尝试时间（限频）
			c.lastAttempt = now
			indexCache[url] = c
		}
		return
	}
	indexCache[url] = indexCacheEntry{entries: entries, err: nil, at: now, lastAttempt: now}
}

// fetchIndex 拉一个源的 index.json。
func fetchIndex(url string) ([]MarketEntry, error) {
	resp, err := marketHTTPClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var list []MarketEntry
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list, nil
}

// listPluginSources GET /admin/plugin-sources — 每个源附带实时条目数与本机已装数（源不可达为 0/reachable=false）。
func (s *Server) listPluginSources(w http.ResponseWriter, r *http.Request) {
	// 已装判定与市场列表同源：落盘 manifest（含已停止插件），而非运行中实例
	local := map[string]bool{}
	for _, mf := range s.plugins.Installed() {
		local[pluginKey(mf.Author, mf.Name)] = true
	}
	type sourceView struct {
		setting.PluginSource
		PluginCount    int  `json:"plugin_count"`
		InstalledCount int  `json:"installed_count"`
		Reachable      bool `json:"reachable"`
	}
	// 并行探测全部源（此前逐源串行回源，多源时接口耗时随源数线性叠加），
	// fetchIndexCached 短 TTL 缓存复用市场聚合刚拉过的结果。
	srcs := s.settings.PluginSources()
	out := make([]sourceView, len(srcs))
	var wg sync.WaitGroup
	for i, src := range srcs {
		wg.Add(1)
		go func(i int, src setting.PluginSource) {
			defer wg.Done()
			v := sourceView{PluginSource: src}
			list, err := fetchIndexCached(s.withGitHubProxy(strings.TrimSpace(src.URL)))
			if err != nil && src.Name == setting.OfficialSourceName {
				list, err = offlineMarket(), nil // 官方源不可达用离线快照计数
			}
			if err == nil {
				v.Reachable = true
				v.PluginCount = len(list)
				for _, e := range list {
					if local[pluginKey(e.Author, e.Name)] {
						v.InstalledCount++
					}
				}
			}
			out[i] = v
		}(i, src)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]interface{}{"sources": out})
}

// probePluginSource GET /admin/plugin-sources/probe?url= — 试拉一个索引地址，返回条目数（添加/编辑源时校验可达）。
func (s *Server) probePluginSource(w http.ResponseWriter, r *http.Request) {
	u := strings.TrimSpace(r.URL.Query().Get("url"))
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		http.Error(w, `{"error":"源地址需以 http:// 或 https:// 开头（指向 index.json）"}`, http.StatusBadRequest)
		return
	}
	list, err := fetchIndexCached(s.withGitHubProxy(u)) // 短 TTL 缓存；失败仅缓存 10s，编辑重试即刻生效
	if err != nil {
		http.Error(w, `{"error":"索引不可达或格式无效: `+err.Error()+`"}`, http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"plugin_count": len(list)})
}

// putPluginSources PUT /admin/plugin-sources — body: {sources: [{name,url,enabled}]} 全量替换。
// 源名用作命名空间目录名：仅允许字母数字 - _，且不可重复；至少保留 official。
func (s *Server) putPluginSources(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Sources []setting.PluginSource `json:"sources"`
	}
	if !readBody(w, r, &body) {
		return
	}
	seen := map[string]bool{}
	for i := range body.Sources {
		src := &body.Sources[i]
		src.Name = strings.TrimSpace(src.Name)
		src.URL = strings.TrimSpace(src.URL)
		if !validSourceName(src.Name) {
			http.Error(w, `{"error":"源名仅允许字母、数字、- 与 _（1–32 位）"}`, http.StatusBadRequest)
			return
		}
		if seen[src.Name] {
			http.Error(w, `{"error":"源名重复: `+src.Name+`"}`, http.StatusBadRequest)
			return
		}
		seen[src.Name] = true
		if !strings.HasPrefix(src.URL, "http://") && !strings.HasPrefix(src.URL, "https://") {
			http.Error(w, `{"error":"源地址需以 http:// 或 https:// 开头（指向 index.json）"}`, http.StatusBadRequest)
			return
		}
	}
	if !seen[setting.OfficialSourceName] {
		http.Error(w, `{"error":"必须保留 official 源（可停用但不可删除）"}`, http.StatusBadRequest)
		return
	}
	s.settings.SetPluginSources(body.Sources)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// validSourceName 源名同时是磁盘目录名，限制字符集防路径穿越。
func validSourceName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// downloadToTemp 下载市场包（走 GitHub 代理配置）并校验 sha256；report 按进度回调（total 未知为 -1）。
// ctx 取消（客户端断开）时 Body 读取即刻中断，用于「取消安装」。
func downloadToTemp(ctx context.Context, tmpDir string, entry *MarketEntry, report func(received, total int64)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", entry.DownloadURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := downloadHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(tmpDir, "cph-*.cphplugin")
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	src := &progressReader{r: resp.Body, total: resp.ContentLength, report: report}
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), src); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	tmp.Close()
	if entry.SHA256 != "" {
		got := hex.EncodeToString(hasher.Sum(nil))
		if got != entry.SHA256 {
			os.Remove(tmp.Name())
			return "", fmt.Errorf("sha256 mismatch: want %s got %s", entry.SHA256, got)
		}
	}
	return tmp.Name(), nil
}

// progressReader 计数读取器：每 256KB 或读完时回调一次，避免进度事件刷屏。
type progressReader struct {
	r                  io.Reader
	total              int64
	received, lastSent int64
	report             func(received, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.received += int64(n)
	if p.report != nil && (p.received-p.lastSent >= 256<<10 || err != nil) {
		p.lastSent = p.received
		p.report(p.received, p.total)
	}
	return n, err
}
