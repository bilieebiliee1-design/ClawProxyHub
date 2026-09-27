// port.go — 网关端口持久化（NexPort v1.3.0 方案 ①）。
//
// 需求：首次启动随机端口并持久化，之后每次启动复用同一端口；仅当端口被占用等导致
// 绑定失败时才重新随机，并把「端口变更（旧→新）」明确暴露给应用 UI。
//
// 实现说明：
//   - 持久化载体是 <DataDir>/gateway.port 纯文本（十进制端口一行），不是 settings 表：
//     监听器有意先于建库绑定（启动失败不占资源、快速报错），此刻 DB 尚未打开；
//   - 只服务于「自动端口」模式（Options.GatewayPort == 0）：设置页「固定端口」模式
//     由用户显式指定（绑定失败 Start 直接报错，语义不变，见 app.go），固定端口绑定
//     成功后同样落盘——用户切回「自动」时从最近一次生效的端口继续，不突兀换口；
//   - 端口变更通知三通道：app.PortChangeNotice()（bridge 逐次启动后查询，应用 UI 弹提示）、
//     run-logs（面板运行日志页可查）、站内通知（面板铃铛）。
package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"io.nexport.gateway/core/logsink"
)

// portFileName 端口持久化文件（DataDir 下）。
const portFileName = "gateway.port"

// readPersistedPort 读持久化的网关端口（缺失 / 非法 / 越界一律返回 0 = 视为未持久化）。
func readPersistedPort(dataDir string) int {
	raw, err := os.ReadFile(filepath.Join(dataDir, portFileName))
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || port <= 0 || port > 65535 {
		return 0
	}
	return port
}

// persistPort 落盘网关端口（best-effort：失败仅记日志，不影响本次启动）。
func persistPort(dataDir string, port int) {
	if dataDir == "" || port <= 0 {
		return
	}
	if err := os.WriteFile(filepath.Join(dataDir, portFileName), []byte(strconv.Itoa(port)), 0o600); err != nil {
		logsink.Printf("[app] 网关端口持久化失败: %v", err)
	}
}
