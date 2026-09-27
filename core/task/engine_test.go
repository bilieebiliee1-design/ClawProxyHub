// engine_test.go — 一键签到批量执行回归（v1.3.0 方案 ③）：RunAllNow 进度事件、
// 停用规则跳过、执行历史落库、批量互斥。
package task

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"io.nexport.gateway/core/database"
	"io.nexport.gateway/core/event"
	"io.nexport.gateway/core/model"
	pb "io.nexport.gateway/core/sdk/proto/cphv1"
)

// fakeRunner 假插件执行器：每次 RunTask 记录调用并返回成功。
type fakeRunner struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeRunner) ListCapabilities(ctx context.Context, pluginName string, instanceID int64) ([]*pb.TaskCapability, error) {
	return nil, nil
}

func (f *fakeRunner) RunTask(ctx context.Context, pluginName string, req *pb.RunTaskRequest) (*pb.RunTaskResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("%s|%s", pluginName, req.CapabilityId))
	f.mu.Unlock()
	return &pb.RunTaskResponse{Summary: "ok:" + req.CapabilityId}, nil
}

// seedEngine 建库 + 插件/账号/规则（2 启用 + 1 停用）。
func seedEngine(t *testing.T) (*Engine, *fakeRunner) {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(context.Background(), filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
	p := model.Plugin{Name: "p", Version: "1", Author: "a", ManifestJSON: "{}"}
	db.Create(&p)
	inst := model.Instance{PluginID: p.ID, Name: "i"}
	db.Create(&inst)
	a1 := model.Account{PluginID: p.ID, InstanceID: inst.ID, DisplayName: "a1"}
	a2 := model.Account{PluginID: p.ID, InstanceID: inst.ID, DisplayName: "a2"}
	db.Create(&a1)
	db.Create(&a2)
	r1 := model.TaskRule{PluginID: p.ID, CapabilityID: "checkin", TriggerType: "daily",
		TriggerValue: "09:00", TargetScope: "account_ids", TargetJSON: fmt.Sprintf("[%d,%d]", a1.ID, a2.ID), Enabled: true}
	r2 := model.TaskRule{PluginID: p.ID, CapabilityID: "growth", TriggerType: "daily",
		TriggerValue: "10:00", TargetScope: "all", Enabled: true}
	r3 := model.TaskRule{PluginID: p.ID, CapabilityID: "off", TriggerType: "daily",
		TriggerValue: "11:00", TargetScope: "all", Enabled: false}
	db.Create(&r1)
	db.Create(&r2)
	db.Create(&r3)
	// gorm 零值 bool + default:true 会被忽略（Create 落库为 true）：停用规则显式回写
	db.Model(&r3).Update("enabled", false)
	runner := &fakeRunner{}
	engine := NewEngine(db, dir, runner, event.New())
	return engine, runner
}

// TestRunAllNowProgress 一键签到：启用规则全执行（停用跳过），逐任务 running+result
// 事件有序播报，执行历史逐任务落库。
func TestRunAllNowProgress(t *testing.T) {
	engine, runner := seedEngine(t)
	var events []RunEvent
	total, err := engine.RunAllNow(context.Background(), func(ev RunEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatalf("RunAllNow: %v", err)
	}
	// r1（account_ids × 2 账号）+ r2（all × 2 账号）= 4 任务；r3 停用跳过
	if total != 4 {
		t.Fatalf("total = %d, want 4", total)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("RunTask calls = %v, want 4", runner.calls)
	}
	// 事件序列：每任务先 running 后 result，index 递增
	if len(events) != 8 {
		t.Fatalf("events = %d, want 8（4 任务 × 开始+结果）", len(events))
	}
	for i, ev := range events {
		taskIdx := i / 2
		if ev.TaskIndex != taskIdx || ev.TaskTotal != 4 {
			t.Fatalf("event[%d] index/total = %d/%d, want %d/4", i, ev.TaskIndex, ev.TaskTotal, taskIdx)
		}
		if wantRunning := i%2 == 0; ev.Running != wantRunning {
			t.Fatalf("event[%d] running = %v, want %v", i, ev.Running, wantRunning)
		}
	}
	// 结果事件：状态与摘要来自 fakeRunner
	for i, ev := range events {
		if i%2 == 1 && (ev.Status != "success" || ev.Summary != "ok:"+ev.CapabilityID) {
			t.Fatalf("event[%d] result = %s/%s", i, ev.Status, ev.Summary)
		}
	}
	// 执行历史落库（4 条 success）
	var runs []model.TaskRun
	engine.db.Find(&runs)
	if len(runs) != 4 {
		t.Fatalf("task_runs = %d, want 4", len(runs))
	}
	for _, run := range runs {
		if run.Status != "success" || run.StartedAt.IsZero() || run.FinishedAt == nil {
			t.Fatalf("run %+v incomplete", run)
		}
	}
	// 事件里账号名可读（非空）
	for _, ev := range events {
		if ev.Account == "" {
			t.Fatalf("event missing account display name: %+v", ev)
		}
	}
}

// TestRunAllNowMutex 批量执行互斥：进行中再次调用报错。
func TestRunAllNowMutex(t *testing.T) {
	engine, _ := seedEngine(t)
	release := make(chan struct{})
	block := &blockingRunner{release: release}
	engine.runner = block
	done := make(chan struct{})
	go func() {
		_, _ = engine.RunAllNow(context.Background(), nil)
		close(done)
	}()
	// 等 blockingRunner 进入执行（轮询 calls）
	deadline := time.Now().Add(2 * time.Second)
	for {
		block.mu.Lock()
		n := len(block.calls)
		block.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := engine.RunAllNow(context.Background(), nil); err == nil {
		t.Fatalf("second RunAllNow should conflict while running")
	}
	close(release)
	<-done
}

// blockingRunner 阻塞型假执行器：首次调用挂起直到 release。
type blockingRunner struct {
	mu      sync.Mutex
	calls   []string
	release chan struct{}
}

func (b *blockingRunner) ListCapabilities(ctx context.Context, pluginName string, instanceID int64) ([]*pb.TaskCapability, error) {
	return nil, nil
}

func (b *blockingRunner) RunTask(ctx context.Context, pluginName string, req *pb.RunTaskRequest) (*pb.RunTaskResponse, error) {
	b.mu.Lock()
	b.calls = append(b.calls, req.CapabilityId)
	b.mu.Unlock()
	<-b.release
	return &pb.RunTaskResponse{Summary: "ok"}, nil
}

var _ Runner = (*fakeRunner)(nil)
var _ Runner = (*blockingRunner)(nil)
