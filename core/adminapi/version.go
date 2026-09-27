// version.go — 核心版本回显与检查更新。
package adminapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"io.nexport.gateway/core/version"
)

// remoteVersionURL 远端版本清单（仓库根 version.json，经 GitHubProxy 加速）。
const remoteVersionURL = "https://raw.githubusercontent.com/ShadowSmallBaby/ClawProxyHub/main/version.json"

// forkReleaseChannel NexPort 的发布通道（版本清单 / Releases），随 fork 发布仓库以 ldflags 注入：
// -ldflags "-X io.nexport.gateway/core/adminapi.forkReleaseChannel=https://<fork 仓库/releases"。
// 为空时回退上游仓库（AGPL §13 署名链路不受影响）。
var forkReleaseChannel = ""

// releaseURL 版本发布页（前端「有更新」跳转）。
const releaseURL = "https://github.com/ShadowSmallBaby/ClawProxyHub/releases"

// remoteManifest 远端 version.json 结构（含更新日志，中英双语）。
type remoteManifest struct {
	Version    string `json:"version"`
	ReleaseURL string `json:"release_url"`
	Changelog  struct {
		Title map[string]string   `json:"title"`
		Items []map[string]string `json:"items"`
	} `json:"changelog"`
}

// coreVersion GET /admin/version — 本机版本 + 远端最新版对比（远端不可达时静默降级，只回本机版本）。
// 有更新时附带远端更新日志（title/items 中英双语），供前端弹窗展示。
func (s *Server) coreVersion(w http.ResponseWriter, r *http.Request) {
	out := map[string]interface{}{"version": version.Core}
	if m := s.fetchLatestVersion(); m != nil && m.Version != "" {
		out["latest"] = m.Version
		// 仅当远端版本严格大于本机时才提示更新（避免本地领先/降级被误判为可更新）
		out["update_available"] = compareSemver(m.Version, version.Core) > 0
		switch {
		case forkReleaseChannel != "":
			// NexPort fork 发布通道优先（ldflags 注入，见 forkReleaseChannel 注释）
			out["release_url"] = forkReleaseChannel
		case m.ReleaseURL != "":
			out["release_url"] = m.ReleaseURL
		default:
			out["release_url"] = releaseURL
		}
		if len(m.Changelog.Items) > 0 {
			out["changelog"] = m.Changelog
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// fetchLatestVersion 拉远端 version.json；任何失败返回 nil（不阻塞前端）。
func (s *Server) fetchLatestVersion() *remoteManifest {
	req, err := http.NewRequest(http.MethodGet, s.withGitHubProxy(remoteVersionURL), nil)
	if err != nil {
		return nil
	}
	resp, err := marketHTTPClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var m remoteManifest
	if json.NewDecoder(resp.Body).Decode(&m) != nil {
		return nil
	}
	return &m
}

// compareSemver 逐段比较点分版本号（忽略 v 前缀与预发布后缀）：a>b 返回 1，a<b 返回 -1，相等返回 0。
func compareSemver(a, b string) int {
	pa := splitSemver(a)
	pb := splitSemver(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

// splitSemver 取版本主体的数值段（如 "v1.0.5-beta" -> [1,0,5]）。
func splitSemver(s string) []int {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	nums := make([]int, len(parts))
	for i, p := range parts {
		nums[i], _ = strconv.Atoi(p)
	}
	return nums
}
