// engine.go — 调度引擎：周期扫描 + 有界 worker 异步执行（tick 不阻塞）。
// 随上游 v1.5.2 f49335e 重构：规则经有界队列与 worker 池执行、调度推进与执行解耦、
// 时区感知（settings.Location()，内嵌 tzdata）与设置保存串行化（SaveSettings）。
// NexPort fork 保留：一键签到批量执行（RunAllNow + RunEvent 进度 + 跳过识别）、
// logsink 日志、批量互斥（runAllMu）。
package task

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"io.nexport.gateway/core/event"
	"io.nexport.gateway/core/logsink"
	"io.nexport.gateway/core/model"
	"io.nexport.gateway/core/runlog"
	"io.nexport.gateway/core/setting"
	"io.nexport.gateway/core/textutil"
	pb "io.nexport.gateway/core/sdk/proto/cphv1"
)

// maxWorkers 限制同时执行的规则数。
const maxWorkers = 4

// Runner 是调度引擎对插件调用层的抽象（由 plugin.Manager 适配注入）。
type Runner interface {
	// ListCapabilities 返回插件声明的任务能力；instanceID>0 时插件可按实例配置裁剪。
	ListCapabilities(ctx context.Context, pluginName string, instanceID int64) ([]*pb.TaskCapability, error)
	// RunTask 触发一次能力执行。credential 为 nil 表示不针对具体账号。
	RunTask(ctx context.Context, pluginName string, req *pb.RunTaskRequest) (*pb.RunTaskResponse, error)
}

// Engine 周期扫描 task_rules，到期规则异步执行（tick 不阻塞）。
type Engine struct {
	db         *gorm.DB
	dataDir    string
	runner     Runner
	bus        *event.Bus
	settings   *setting.Store
	scheduleMu sync.Mutex
	stop       chan struct{}
	stopped    sync.Once
	workers    sync.Once
	mu         sync.Mutex
	running    map[int64]bool
	queue      chan ruleJob
	ctx        context.Context
	cancel     context.CancelFunc
}

// NewEngine 创建调度引擎。
func NewEngine(db *gorm.DB, dataDir string, runner Runner, bus *event.Bus, stores ...*setting.Store) *Engine {
	settings := setting.New(db)
	if len(stores) > 0 {
		settings = stores[0]
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{ctx: ctx, cancel: cancel, running: map[int64]bool{}, queue: make(chan ruleJob, 64), db: db, dataDir: dataDir, runner: runner, bus: bus, settings: settings, stop: make(chan struct{})}
}

// Start 启动扫描循环。残留的 running/queued 记录（上次进程异常退出）标记为 failed。
// 规则经有界队列执行，tick 不等上游 RPC。
func (e *Engine) Start(ctx context.Context) {
	e.db.Model(&model.TaskRun{}).Where("status IN ?", []string{"running", "queued"}).
		Update("status", "failed")

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				e.Stop()
				return
			case <-e.stop:
				return
			case <-ticker.C:
				e.tick(ctx)
			}
		}
	}()
}

// tick 执行一轮。单轮 panic 不退出进程，下一轮继续。
func (e *Engine) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			logsink.Printf("[task] tick panicked: %v", r)
			runlog.New(e.db, e.settings.RunLevel).
				Error("task", "tick", "任务扫描异常", fmt.Sprintf("%v", r), nil)
		}
	}()
	e.doTick(ctx)
}

// Stop 幂等停止（二次调用安全）。
func (e *Engine) Stop() {
	e.stopped.Do(func() { close(e.stop); e.cancel() })
}

// doTick 一轮扫描：补算缺失 next_run_at → 取到期规则 → 逐条异步触发。
// 一键签到批量执行期间整轮跳过（互斥，见 runAllMu），下一轮补算。
func (e *Engine) doTick(ctx context.Context) {
	if !runAllMu.TryLock() {
		return
	}
	defer runAllMu.Unlock()
	e.scheduleMu.Lock()
	defer e.scheduleMu.Unlock()
	now := time.Now().UTC()

	// next_run_at 缺失的启用规则（直插 DB / 历史数据）补算下次触发时刻
	var unscheduled []model.TaskRule
	e.db.Where("enabled = ? AND next_run_at IS NULL", true).Limit(50).Find(&unscheduled)
	for i := range unscheduled {
		if next := e.computeNext(&unscheduled[i], now); next != nil {
			e.db.Model(&unscheduled[i]).Update("next_run_at", next)
		} else {
			e.db.Model(&unscheduled[i]).Update("enabled", false) // 触发值非法，禁用防反复扫描
		}
	}

	var rules []model.TaskRule
	// 队列限制执行并发；扫描不能只取前几条，否则无可用账号的规则会饿死后续任务。
	if err := e.db.Where("enabled = ? AND next_run_at IS NOT NULL AND next_run_at <= ?",
		true, now).Order("next_run_at, id").Find(&rules).Error; err != nil {
		return
	}
	for i := range rules {
		e.fire(ctx, &rules[i])
	}
}

// SaveSettings 与调度扫描串行，避免旧时区计算覆盖刚失效的日历规则。
func (e *Engine) SaveSettings(values map[string]string) error {
	e.scheduleMu.Lock()
	defer e.scheduleMu.Unlock()
	return e.settings.SetMany(values)
}

// fire 入队成功才推进调度；队列满或同规则运行中则下轮再试。
func (e *Engine) fire(ctx context.Context, rule *model.TaskRule) { _, _ = e.enqueue(rule, true) }

// RunNow 立即执行一次规则（不新建规则、不影响调度时刻），返回首个执行记录 id。
func (e *Engine) RunNow(ctx context.Context, rule *model.TaskRule) (int64, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	return e.enqueue(rule, false)
}

type ruleJob struct {
	rule     model.TaskRule
	accounts []*model.Account
	runs     []model.TaskRun
}

func (e *Engine) enqueue(rule *model.TaskRule, scheduled bool) (int64, error) {
	if err := ValidateRule(rule); err != nil {
		return 0, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx.Err() != nil {
		return 0, e.ctx.Err()
	}
	if e.running[rule.ID] {
		return 0, fmt.Errorf("rule is already queued or running")
	}
	if len(e.queue) == cap(e.queue) {
		return 0, fmt.Errorf("task queue is full")
	}
	accounts, err := e.selectAccounts(rule)
	if err != nil {
		return 0, err
	}
	if len(accounts) == 0 {
		return 0, fmt.Errorf("no eligible accounts")
	}
	job := ruleJob{rule: *rule, accounts: accounts}
	now := time.Now().UTC()
	var next *time.Time
	if scheduled {
		next = e.computeNext(rule, now)
	}
	err = e.db.Transaction(func(tx *gorm.DB) error {
		if scheduled {
			res := tx.Model(&model.TaskRule{}).Where("id = ? AND enabled = ?", rule.ID, true).Updates(map[string]interface{}{"last_run_at": now, "next_run_at": next, "enabled": rule.TriggerType != "once"})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return fmt.Errorf("rule no longer enabled")
			}
		}
		for _, acct := range accounts {
			run := model.TaskRun{RuleID: &job.rule.ID, Status: "queued", StartedAt: now}
			if acct != nil {
				run.AccountID = &acct.ID
			}
			if err := tx.Create(&run).Error; err != nil {
				return err
			}
			job.runs = append(job.runs, run)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	e.running[rule.ID] = true
	e.workers.Do(func() {
		for i := 0; i < maxWorkers; i++ {
			go e.worker()
		}
	})
	e.queue <- job
	return job.runs[0].ID, nil
}

func (e *Engine) worker() {
	for {
		select {
		case <-e.ctx.Done():
			return
		case job := <-e.queue:
			for i, acct := range job.accounts {
				e.executeAccount(e.ctx, &job.rule, acct, &job.runs[i])
			}
			e.mu.Lock()
			delete(e.running, job.rule.ID)
			e.mu.Unlock()
		}
	}
}

// RunEvent 一键签到/批量执行时向外播报的单条进度（逐任务状态与结果）。
// TaskIndex/TaskTotal 由 RunAllNow 统一编号；PluginID/Plugin 为插件 id/名（展示名由调用方换算）。
type RunEvent struct {
	TaskIndex    int    `json:"task_index"`
	TaskTotal    int    `json:"task_total"`
	RuleID       int64  `json:"rule_id"`
	PluginID     int64  `json:"plugin_id"`
	Plugin       string `json:"plugin"`
	CapabilityID string `json:"capability_id"`
	Account      string `json:"account"`
	AccountID    int64  `json:"account_id"`
	// Running=true 表示任务开始执行（result 字段无意义）；false 表示该任务已出结果
	Running bool   `json:"running"`
	Status  string `json:"status,omitempty"`
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`
	// Skipped 标记「执行成功但实际动作无法自动完成」的跳过：插件以 warning 级站内通知
	// （需人工前往）或约定摘要前缀「跳过：」表达。DB 执行历史仍记 success（任务执行本身
	// 成功），仅批量进度通道区分，供 UI 计数「跳过」而非并入「成功」（v1.4.0 验收修复③）。
	Skipped bool `json:"skipped,omitempty"`
}

// runAllMu 批量执行互斥（一键签到 vs 一键签到 / vs 调度 tick）：
// 批量执行期间调度 tick 整轮跳过（下一轮 15s 后补算），避免同一账号任务重叠打上游。
var runAllMu sync.Mutex

// RunAllNow 立即执行全部启用规则（一键签到）：逐 (规则×账号) 播报进度。
// 与其他批量执行及调度 tick 互斥，进行中再次调用返回错误。
// 返回本次任务总数（含失败）。onEvent 为 nil 时等价静默批量执行。
func (e *Engine) RunAllNow(ctx context.Context, onEvent func(RunEvent)) (int, error) {
	if !runAllMu.TryLock() {
		return 0, fmt.Errorf("已有批量任务在执行，请稍后再试")
	}
	defer runAllMu.Unlock()

	var rules []model.TaskRule
	if err := e.db.Where("enabled = ?", true).Order("id").Find(&rules).Error; err != nil {
		return 0, err
	}
	// 预选账号算总数（进度条分母）；逐规则执行时重新选号（两次查询间隙账号变化无碍）
	total := 0
	plans := make([][]*model.Account, len(rules))
	for i := range rules {
		accts, err := e.selectAccounts(&rules[i])
		if err != nil || len(accts) == 0 {
			accts = nil
		}
		plans[i] = accts
		total += len(accts)
	}
	emit := func(ev RunEvent) {
		if onEvent != nil {
			onEvent(ev)
		}
	}
	idx := 0
	for i := range rules {
		rule := rules[i]
		if ctx.Err() != nil { // 客户端取消（进度通道断开）：停止后续任务
			return idx, ctx.Err()
		}
		pname := pluginNameByID(e.db, rule.PluginID)
		for _, acct := range plans[i] {
			ev := RunEvent{TaskIndex: idx, TaskTotal: total, RuleID: rule.ID,
				PluginID: rule.PluginID, Plugin: pname, CapabilityID: rule.CapabilityID}
			if acct != nil {
				ev.Account, ev.AccountID = acct.DisplayName, acct.ID
			}
			ev.Running = true
			emit(ev)
			ev.Running = false
			st, sum, er, sk := e.runOne(ctx, &rule, acct)
			ev.Status, ev.Summary, ev.Error, ev.Skipped = st, sum, er, sk
			emit(ev)
			idx++
		}
	}
	return total, nil
}

// runOne 对单个规则×账号执行一次能力并落执行历史（一键签到的单任务内核）。
// 执行内核与调度路径共用 runAccount（凭据账号锁 + 加解密错误传播，随上游 v1.5.2）。
// 返回 (status, summary, error, skipped)。skipped 仅在 status=success 且插件以结构化信号
// （warning 级站内通知 = 需人工前往）或约定摘要前缀「跳过：」表达「动作无法自动完成」时为
// true；执行历史仍落 success（v1.4.0 验收修复③：一键签到计数区分跳过）。
func (e *Engine) runOne(ctx context.Context, rule *model.TaskRule, acct *model.Account) (string, string, string, bool) {
	var run model.TaskRun
	run.RuleID = &rule.ID
	run.Status = "running"
	run.StartedAt = time.Now()
	e.db.Create(&run)
	if acct != nil {
		run.AccountID = &acct.ID
	}

	resp, err := e.runAccount(ctx, rule, acct)
	switch {
	case err != nil:
		run.Status = "failed"
		run.ErrorMessage = truncate(err.Error(), 1000)
	case resp == nil:
		run.Status = "failed"
		run.ErrorMessage = "empty task response"
	case resp.Error != nil && resp.Error.Code != 0:
		run.Status = "failed"
		run.ErrorMessage = truncate(resp.Error.Message, 1000)
	default:
		run.Status = "success"
		run.Summary = truncate(resp.Summary, 1000)
		// 结构化明细快照（如成长任务列表）持久化，账号详情弹窗直接渲染
		if len(resp.DetailJson) > 0 && len(resp.DetailJson) <= 1<<20 && json.Valid([]byte(resp.DetailJson)) {
			run.DetailJSON = resp.DetailJson
		}
	}
	// 插件要求提醒用户（如站点签到需人工前往）→ 落站内通知，成功/失败均可携带
	if resp != nil && resp.Notification != nil && resp.Notification.Title != "" {
		e.db.Create(&model.Notification{
			Title: truncate(resp.Notification.Title, 256), Content: truncate(resp.Notification.Content, 4000),
			Level: orDefault(resp.Notification.Level, "info"), AccountID: run.AccountID,
		})
	}
	fin := time.Now()
	run.FinishedAt = &fin
	e.db.Save(&run)

	if run.Status == "success" && acct != nil && e.bus != nil {
		e.bus.Publish(event.Event{Topic: event.TopicTaskCompleted, AccountID: acct.ID})
	}
	return run.Status, run.Summary, run.ErrorMessage,
		run.Status == "success" && ((resp.GetNotification() != nil && resp.GetNotification().GetLevel() == "warning") ||
			strings.HasPrefix(run.Summary, "跳过："))
}

// selectAccounts 按 target_scope 选出目标账号；返回 nil 元素表示"全局执行一次"。
// 任务与对话调度分离：disabled（停用调度）账号仍跑任务，只排除 expired（凭据失效）。
func (e *Engine) selectAccounts(rule *model.TaskRule) ([]*model.Account, error) {
	taskable := []string{"active", "disabled"}
	switch rule.TargetScope {
	case "all":
		var accts []model.Account
		if err := e.db.Where("plugin_id = ? AND status IN ?", rule.PluginID, taskable).Find(&accts).Error; err != nil {
			return nil, err
		}
		out := make([]*model.Account, len(accts))
		for i := range accts {
			out[i] = &accts[i]
		}
		return out, nil
	case "rotate":
		var acct model.Account
		if err := e.db.Where("plugin_id = ? AND status IN ?", rule.PluginID, taskable).
			Order("last_refresh_at IS NULL, last_refresh_at").First(&acct).Error; err != nil {
			return nil, err
		}
		return []*model.Account{&acct}, nil
	case "account_ids":
		var ids []int64
		if err := json.Unmarshal([]byte(rule.TargetJSON), &ids); err != nil {
			return nil, err
		}
		var accts []model.Account
		if err := e.db.Where("id IN ? AND plugin_id = ? AND status IN ?", ids, rule.PluginID, taskable).
			Find(&accts).Error; err != nil {
			return nil, err
		}
		out := make([]*model.Account, len(accts))
		for i := range accts {
			out[i] = &accts[i]
		}
		return out, nil
	case "global":
		return []*model.Account{nil}, nil
	default:
		return nil, fmt.Errorf("invalid target scope")
	}
}

// computeNext 计算下次触发时刻。
func (e *Engine) computeNext(rule *model.TaskRule, from time.Time) *time.Time {
	var next time.Time
	switch rule.TriggerType {
	case "interval":
		d, err := time.ParseDuration(rule.TriggerValue)
		if err != nil || d <= 0 {
			return nil
		}
		next = from.Add(d)
	case "daily":
		clock, err := time.Parse("15:04", rule.TriggerValue)
		if err != nil {
			return nil
		}
		// 按当地日历逐分钟查找，夏令时跳过不存在的时刻，回拨时按实际时间排序。
		next = nextCron(fmt.Sprintf("%d %d * * *", clock.Minute(), clock.Hour()), from.In(e.settings.Location()))
		if next.IsZero() {
			return nil
		}
		// 随机抖动：设定时刻之后延迟 0~jitter，错开多账号同刻打上游（每天各自随机）；管理端可配，0 = 关闭
		if jitter := e.settings.DailyJitter(); jitter > 0 {
			next = next.Add(time.Duration(rand.Int63n(int64(jitter))))
		}
	case "cron":
		next = nextCron(rule.TriggerValue, from.In(e.settings.Location()))
		if next.IsZero() {
			return nil
		}
	case "once":
		t, err := time.Parse(time.RFC3339, rule.TriggerValue)
		if err != nil {
			return nil
		}
		next = t
	default:
		return nil
	}
	next = next.UTC()
	return &next
}

// recordRun 记录无账号上下文的失败。
func (e *Engine) recordRun(rule *model.TaskRule, acctID *int64, status, summary, errMsg string) {
	var run model.TaskRun
	run.RuleID = &rule.ID
	run.AccountID = acctID
	run.Status = status
	run.Summary = summary
	run.ErrorMessage = errMsg
	run.StartedAt = time.Now()
	fin := time.Now()
	run.FinishedAt = &fin
	e.db.Create(&run)
}

// pluginNameByID 从 plugins 表取插件名（失败返回空串，调用侧按不存在处理）。
func pluginNameByID(db *gorm.DB, id int64) string {
	var p model.Plugin
	if err := db.First(&p, id).Error; err != nil {
		return ""
	}
	return p.Name
}

func truncate(s string, n int) string {
	return textutil.Truncate(s, n)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// ScheduleOnce 创建一条立即执行的 once 规则（手动触发/失败重跑都用它）。
func (e *Engine) ScheduleOnce(pluginID int64, capabilityID string, accountID int64) error {
	scope, target := "all", "[]"
	if accountID > 0 {
		scope = "account_ids"
		target = fmt.Sprintf("[%d]", accountID)
	}
	rule := model.TaskRule{
		Enabled:      true,
		PluginID:     pluginID,
		CapabilityID: capabilityID,
		TriggerType:  "once",
		TriggerValue: time.Now().UTC().Format(time.RFC3339),
		TargetScope:  scope,
		TargetJSON:   target,
		NextRunAt:    ptrTime(time.Now().UTC()),
	}
	return e.db.Create(&rule).Error
}

// EnsureAccountRules 新账号建档后，按插件声明的账号级任务能力自动生成规则（按账号所属实例查询，
// 实例关闭的能力不建规则）。生成的规则默认停用，用户在任务页确认调度后再启用。
func (e *Engine) EnsureAccountRules(ctx context.Context, pluginName string, accountID int64) {
	var p model.Plugin
	if err := e.db.Where("name = ?", pluginName).First(&p).Error; err != nil {
		return
	}
	var acct model.Account
	e.db.Select("instance_id").First(&acct, accountID)
	caps, err := e.runner.ListCapabilities(ctx, pluginName, acct.InstanceID)
	if err != nil {
		return
	}
	target := fmt.Sprintf("[%d]", accountID)
	for _, c := range caps {
		if !c.PerAccount {
			continue
		}
		tt, tv := parseSchedule(c.DefaultSchedule)
		e.db.Create(&model.TaskRule{
			PluginID: p.ID, CapabilityID: c.Id,
			TriggerType: tt, TriggerValue: tv,
			TargetScope: "account_ids", TargetJSON: target,
			Auto: true, Enabled: false,
		})
	}
}

// parseSchedule 解析能力声明的 default_schedule：
// "daily 09:00" / "interval 6h" / "cron 0 9 * * *" / "once"；空或不合法回退每天 09:00。
func parseSchedule(def string) (string, string) {
	kind, value, _ := strings.Cut(strings.TrimSpace(def), " ")
	switch kind {
	case "daily":
		if value == "" {
			value = "09:00"
		}
		return "daily", value
	case "interval":
		if value == "" {
			value = "6h"
		}
		return "interval", value
	case "cron":
		if value == "" {
			return "daily", "09:00"
		}
		return "cron", value
	case "once":
		return "once", time.Now().UTC().Format(time.RFC3339) // 不带时刻默认立即
	}
	return "daily", "09:00"
}

func ptrTime(t time.Time) *time.Time { return &t }
