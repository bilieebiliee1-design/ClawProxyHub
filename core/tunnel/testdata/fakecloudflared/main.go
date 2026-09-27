// fakecloudflared — 测试用假 cloudflared（由 tunnel_test.go 在运行时构建，不参与 ./...）。
//
// 行为由环境变量驱动（Manager.Start 只传二进制路径，故不用命令行参数）：
//   - 默认：打印一行含 trycloudflare 域名的输出（触发 StateActive），然后常驻；
//   - FAKE_TCP_PORT：在 127.0.0.1:<port> 开 TCP 监听循环 Accept——测试父进程用
//     拨号成败探测子进程存活（本机进程沙箱对 %TEMP% 文件视图按进程树隔离，
//     跨进程文件心跳不可见，TCP 是可靠的进程内通道）；
//   - FAKE_MODE=exit-now：立即 exit(1)，不输出域名（验证「未获取域名即退出」路径）；
//   - FAKE_MODE=silent-hang：不输出任何内容挂起（验证 acquireTimeout 超时路径；
//     用 time.Sleep 而非 select{}——后者触发 Go 死锁检测直接退出）；
//   - FAKE_EXIT_AFTER_MS>0：拿到域名后再存活指定毫秒后自行退出（模拟运行中断连）。
//
// 忽略所有真实参数（--url/--protocol/--edge/…）。
package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

func main() {
	switch os.Getenv("FAKE_MODE") {
	case "exit-now":
		fmt.Fprintln(os.Stderr, "fakecloudflared: exit-now")
		os.Exit(1)
	case "silent-hang":
		time.Sleep(10 * time.Minute)
	}

	// 监听先行（打印域名前）：保证父进程看到 Active 时拨号目标已就绪
	if p := os.Getenv("FAKE_TCP_PORT"); p != "" {
		if ln, err := net.Listen("tcp", "127.0.0.1:"+p); err == nil {
			go func() {
				for {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					_ = c.Close()
				}
			}()
		}
	}

	fmt.Println("2026-09-25T00:00:00Z INF +-----------------------------------------------------------------------------+")
	fmt.Println("https://fake-" + os.Getenv("FAKE_URL_NAME") + ".trycloudflare.com")

	quit := time.Time{}
	if v := os.Getenv("FAKE_EXIT_AFTER_MS"); v != "" {
		ms, _ := strconv.Atoi(v)
		quit = time.Now().Add(time.Duration(ms) * time.Millisecond)
	}
	for {
		if !quit.IsZero() && time.Now().After(quit) {
			os.Exit(0)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
