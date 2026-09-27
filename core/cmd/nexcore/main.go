// nexcore — NexPort 核心的桌面冒烟入口（NexPort 移植新增）。
//
// 基于 ClawProxyHub（AGPL-3.0）修改构建。安卓侧不经过本入口——bridge 层直接调
// app.Start/Stop/Restart；本命令以同样的库形态启动核心（信号驱动退出），
// 用于桌面/CI 冒烟验证：go run ./cmd/nexcore（环境变量同原项目 CPH_*）。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"io.nexport.gateway/core/app"
	"io.nexport.gateway/core/conf"
	"io.nexport.gateway/core/logsink"
)

func main() {
	cfg := conf.Load()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
	gwPort, tunPort, err := app.Start(context.Background(), app.Options{
		DataDir:        cfg.DataDir,
		MarketplaceURL: cfg.MarketplaceURL,
		LogLevel:       envOr("CPH_LOG_LEVEL", "info"),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
	logsink.Printf("[nexcore] desktop smoke entry: gateway=127.0.0.1:%d tunnel-target=127.0.0.1:%d", gwPort, tunPort)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	_ = app.Stop()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
