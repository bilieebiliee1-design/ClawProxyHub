// checkin.go — 一键签到（NexPort v1.3.0 方案 ③）。
//
// POST /admin/tasks/checkin：立即执行调度规则里的全部任务（启用中的规则，停用规则
// 按用户意图跳过），并以 NDJSON 进度流实时播报逐任务状态与结果（与市场安装进度
// 同一通道形态，前端 fetch 流式逐行读取）：
//
//	{"total":N}                                     首事件：任务总数（规则×账号）
//	{"task_index":i,...,"running":true}             任务开始
//	{"task_index":i,...,"status":"success|failed",
//	 "summary":"...","error":"..."}                 任务结果（含执行历史落库）
//	{"done":true,"success":M,"failed":K,"total":N}  收尾汇总
//	{"error":"..."}                                 异常（批量执行已在进行中等）
//
// 执行内核与调度触发完全同源（engine.runOne）：task_runs 落执行历史、通知落站内、
// 凭据变更回写账号；批量执行与调度 tick 互斥（engine.RunAllNow TryLock）。
package adminapi

import (
	"net/http"

	"io.nexport.gateway/core/task"
)

// checkinAll POST /admin/tasks/checkin — 一键签到（NDJSON 进度流）。
func (s *Server) checkinAll(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		http.Error(w, `{"error":"engine not available"}`, http.StatusServiceUnavailable)
		return
	}
	pw := newProgressWriter(w)
	// 展示名换算缓存（插件品牌 / 能力名；跨任务复用避免重复回插件拉能力表）
	brands := map[int64]string{}
	caps := map[string]map[string]string{}
	brandOf := func(pluginID int64) string {
		if b, ok := brands[pluginID]; ok {
			return b
		}
		b := s.pluginBrandByID(pluginID)
		brands[pluginID] = b
		return b
	}
	capOf := func(pluginName, capID string) string {
		if labels, ok := caps[pluginName]; ok {
			if v := labels[capID]; v != "" {
				return v
			}
			return capID
		}
		labels := s.capabilityLabelMap(pluginName)
		caps[pluginName] = labels
		if labels != nil && labels[capID] != "" {
			return labels[capID]
		}
		return capID
	}

	// success/failed/skipped 三计数（v1.4.0 验收修复③）：skipped = 任务执行成功但实际
	// 动作无法自动完成（如仅 API 密钥无法自动签到），与真成功分开计数与展示。
	success, failed, skipped := 0, 0, 0
	_, err := s.engine.RunAllNow(r.Context(), func(ev task.RunEvent) {
		payload := map[string]interface{}{
			"task_index": ev.TaskIndex, "task_total": ev.TaskTotal,
			"plugin": brandOf(ev.PluginID), "capability": capOf(ev.Plugin, ev.CapabilityID),
			"account": ev.Account, "account_id": ev.AccountID, "running": ev.Running,
		}
		if !ev.Running {
			// skipped 是成功的一种（DB 仍 success）；进度通道单列，UI 分开计数展示
			if ev.Skipped {
				payload["status"] = "skipped"
			} else {
				payload["status"] = ev.Status
			}
			payload["summary"] = ev.Summary
			if ev.Error != "" {
				payload["error"] = ev.Error
			}
			switch {
			case ev.Skipped:
				skipped++
			case ev.Status == "success":
				success++
			default:
				failed++
			}
		}
		pw.send(payload)
	})
	if err != nil {
		// 已有批量在执行 / ctx 取消：进度通道给出明确原因后收尾
		if failed == 0 && success == 0 {
			pw.send(map[string]interface{}{"total": 0, "done": false, "error": err.Error()})
			return
		}
	}
	pw.send(map[string]interface{}{"done": true, "total": success + failed + skipped,
		"success": success, "skipped": skipped, "failed": failed})
}
