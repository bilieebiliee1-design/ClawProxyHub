// emit.go — 级别前缀格式化（见 logsink.go）。
package logsink

import "fmt"

func tag(l Level) string {
	switch l {
	case LevelError:
		return "[error] "
	case LevelWarn:
		return "[warn] "
	case LevelDebug:
		return "[debug] "
	default:
		return ""
	}
}

// sprint 格式化并追加级别前缀（info 无前缀，与原项目日志行格式保持一致）。
func sprint(l Level, format string, args ...any) string {
	return tag(l) + fmt.Sprintf(format, args...)
}
