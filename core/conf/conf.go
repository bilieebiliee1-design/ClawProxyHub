// Package conf — 核心运行配置：环境变量 > 默认值（桌面/容器形态）。
//
// 基于 ClawProxyHub（AGPL-3.0）修改构建。安卓库形态不使用环境变量，由 app.Start 的
// Options 直接构造；关键安全差异：库形态一律 127.0.0.1 随机端口——原项目默认
// CPH_ADDR=":8080"（全接口）在安卓上绝不可接受。
package conf

import (
	"fmt"
	"os"

	"io.nexport.gateway/core/setting"
)

// Config 是核心进程的运行配置。
type Config struct {
	// HTTP 监听地址。桌面形态来自 CPH_ADDR（默认 ":8080"）；
	// 库形态由 app 强制 "127.0.0.1:0"（随机端口，仅回环）。
	Addr string
	// 数据目录（SQLite / 插件目录都在其下）
	DataDir string
	// 数据库 DSN；空则用 <DataDir>/cph.db
	DatabaseDSN string
	// 插件安装目录；空则用 <DataDir>/plugins
	PluginDir string
	// 插件市场索引 URL（仪表盘「设置」里的自建地址优先于此项）
	MarketplaceURL string
	// TempDir 临时目录：os.CreateTemp 的基目录（安卓必须指向应用缓存目录，
	// 否则落到不可写的 /data/local/tmp，市场下载/离线上传/备份导出全部报错）
	TempDir string
}

// Load 从环境变量读取配置，未设置项用默认值填充（桌面/容器形态）。
func Load() *Config {
	cfg := &Config{
		Addr:           envStr("CPH_ADDR", ":8080"),
		DataDir:        envStr("CPH_DATA_DIR", "./data"),
		MarketplaceURL: envStr("CPH_MARKETPLACE_URL", setting.DefaultMarketplaceURL),
	}
	cfg.DatabaseDSN = envStr("CPH_DATABASE_DSN", cfg.DataDir+"/cph.db")
	cfg.PluginDir = envStr("CPH_PLUGIN_DIR", cfg.DataDir+"/plugins")
	return cfg
}

// Loopback 返回库形态的强制监听地址：仅回环 + 随机端口。
// 安卓侧先 net.Listen 拿实际端口再上报宿主（见 app.Start）。
func Loopback() string { return "127.0.0.1:0" }

// LoopbackAt 返回库形态在指定端口上的强制监听地址：仅回环 + 固定端口。
// 供设置页「网关固定端口」配置项使用（app.Options.GatewayPort > 0 时）；
// 合法性（1-65535）由调用方校验，这里只做拼装保持纯函数。
func LoopbackAt(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Validate 检查配置合法性。
func (c *Config) Validate() error {
	if c.DataDir == "" {
		return fmt.Errorf("data dir must not be empty")
	}
	return nil
}
