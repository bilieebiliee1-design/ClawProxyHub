// logsink.go — 核心日志统一出口（NexPort 移植新增）。
//
// 基于 ClawProxyHub（AGPL-3.0）修改构建。原项目日志散落在 stdout/stderr
// （database.go 的 gorm logger、gateway/server.go、plugin/host.go、task/engine.go、
// janitor、sdk 等十余处）；安卓进程的 stdout/stderr 默认只进 logcat，无法被宿主
// App 编程消费。故 fork 将全部日志点改写经本包输出：
//
//   - 未注册 sink 时：写 stderr（桌面/容器形态行为与原项目一致）；
//   - 注册 sink 后：逐行投递给 sink（bridge 层负责节流后回调 Kotlin）。
package logsink

import (
	"io"
	"os"
	"strings"
	"sync"
)

// Level 日志级别（越小的值越重要）。
type Level int

const (
	LevelError Level = iota
	LevelWarn
	LevelInfo
	LevelDebug
)

// ParseLevel 把级别字符串转为 Level；无法识别时按 Info。
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "error":
		return LevelError
	case "warn", "warning":
		return LevelWarn
	case "debug", "trace":
		return LevelDebug
	default:
		return LevelInfo
	}
}

var (
	mu       sync.RWMutex
	out      io.Writer = os.Stderr
	minLevel           = LevelInfo
)

// SetOutput 重定向日志出口（nil 恢复 stderr）。bridge 注册节流回调时传入。
func SetOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	if w == nil {
		out = os.Stderr
		return
	}
	out = w
}

// SetMinLevel 设置最低输出级别（低于该级别的日志丢弃）。
func SetMinLevel(l Level) {
	mu.Lock()
	defer mu.Unlock()
	minLevel = l
}

// Writer 返回可注入标准库 log / gorm logger 的 io.Writer（转发到当前出口）。
func Writer() io.Writer { return writer{} }

type writer struct{}

func (writer) Write(p []byte) (int, error) {
	mu.RLock()
	w := out
	mu.RUnlock()
	return w.Write(p)
}

// Printf 按格式写一行日志（带换行）。
func Printf(format string, args ...any) { emit(LevelInfo, format, args...) }

// Warnf 写一行警告日志。
func Warnf(format string, args ...any) { emit(LevelWarn, format, args...) }

// Errorf 写一行错误日志。
func Errorf(format string, args ...any) { emit(LevelError, format, args...) }

// Debugf 写一行调试日志。
func Debugf(format string, args ...any) { emit(LevelDebug, format, args...) }

func emit(l Level, format string, args ...any) {
	mu.RLock()
	w, min := out, minLevel
	mu.RUnlock()
	if l > min {
		return
	}
	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}
	_, _ = io.WriteString(w, sprint(l, format, args...))
}
