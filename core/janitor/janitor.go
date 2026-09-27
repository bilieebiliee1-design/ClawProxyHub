// Package janitor — 后台清理：按设置的保留天数定期删除过期日志（调用日志与运行日志
// 同窗口；0 = 永久不清理）。
package janitor

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"io.nexport.gateway/core/logsink"
	"io.nexport.gateway/core/runlog"
	"io.nexport.gateway/core/setting"
)

// StartLogRetention 启动即清一次，之后每小时检查一次；ctx 取消退出。
func StartLogRetention(ctx context.Context, db *gorm.DB, settings *setting.Store) {
	go func() {
		sweepLogs(db, settings)
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepLogs(db, settings)
			}
		}
	}()
}

// sweepLogs 删除 created_at 早于保留窗口的日志（request_logs 与 run_logs 同窗口：
// 默认级别 info 后 run_logs 会累积核心启动 / 隧道生命周期事件，需要同一保留策略）。
func sweepLogs(db *gorm.DB, settings *setting.Store) {
	days := settings.LogRetentionDays()
	if days <= 0 {
		return
	}
	window := fmt.Sprintf("-%d days", days)
	reqPurged, runPurged := int64(0), int64(0)
	if res := db.Exec("DELETE FROM request_logs WHERE created_at < datetime('now', ?)", window); res.Error == nil {
		reqPurged = res.RowsAffected
	}
	if res := db.Exec("DELETE FROM run_logs WHERE created_at < datetime('now', ?)", window); res.Error == nil {
		runPurged = res.RowsAffected
	}
	if reqPurged > 0 || runPurged > 0 {
		logsink.Printf("[janitor] purged %d request logs / %d run logs older than %d days", reqPurged, runPurged, days)
		runlog.New(db, func() string { return settings.RunLevel() }).
			Info("janitor", "purge", fmt.Sprintf("清理过期日志：调用 %d 条、运行 %d 条（%d 天前）", reqPurged, runPurged, days), "", nil)
	}
}
