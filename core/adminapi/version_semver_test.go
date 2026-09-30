package adminapi

import (
	"strings"
	"testing"
)

// TestRemoteVersionURLNotUpstream 版本清单默认须指向 fork（v1.4.5 口径修复）：
// 上游 main 的 version.json 随上游核心版本走（v1.5.0 起 changelog 是上游桌面版事实：
// 管理后台移动端适配/PWA），指回上游会让核心位 1.2.9 持续判「有更新」并弹上游文案。
// 本测试拦住未来同步上游时的静默回退。
func TestRemoteVersionURLNotUpstream(t *testing.T) {
	if strings.Contains(remoteVersionURL, "ShadowSmallBaby/ClawProxyHub") {
		t.Fatalf("远端版本清单不应指向上游仓库: %s", remoteVersionURL)
	}
	if !strings.HasSuffix(remoteVersionURL, "version.json") {
		t.Fatalf("远端版本清单应为 version.json: %s", remoteVersionURL)
	}
}

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.3", "1.0.5", -1}, // 远端旧于本机 → 不提示更新
		{"1.0.5", "1.0.3", 1},  // 远端新于本机 → 提示更新
		{"1.0.5", "1.0.5", 0},  // 相等 → 不提示
		{"v1.2.0", "1.1.9", 1}, // v 前缀 + 段间进位
		{"1.0.10", "1.0.9", 1}, // 数值比较而非字典序
		{"1.1", "1.1.0", 0},    // 缺省段补零
		{"2.0.0-beta", "2.0.0", 0},
	}
	for _, c := range cases {
		if got := compareSemver(c.a, c.b); got != c.want {
			t.Errorf("compareSemver(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}
