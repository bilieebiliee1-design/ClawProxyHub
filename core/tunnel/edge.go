// edge.go — 安卓上 Cloudflare 边缘地址发现（绕开 Go 内建 resolver 的 SRV 死路）。
//
// 根因（上轮 strace 实证）：cloudflared 的边缘发现走 net.LookupSRV——Go 标准库没有
// SRV 的 CGO 实现，这条路径永远是纯 Go DNS client；安卓没有 /etc/resolv.conf，
// client 落到默认 [::1]:53 被 connection refused（"lookup _v2-origintunneld._tcp.
// argotunnel.com on [::1]:53"）。宿主进程的 CGO getaddrinfo 能做 A/AAAA 查询但做不了
// SRV；cloudflared 自带的 DoT 回退（1.1.1.1:853）在常见移动网络同样不可达（TCP 853
// 被拦截），因此两条路都死。
//
// 修法：本进程直接用原始 UDP DNS 报文向公网解析器池（含国内稳定可达的 AliDNS/
// DNSPod）做 SRV + A 查询，拿到边缘 ip:port 后以 `--edge`（StringSliceFlag，见
// cloudflared cmd/cloudflared/tunnel/cmd.go 与 supervisor/supervisor.go NewSupervisor：
// len(EdgeAddrs)>0 走 edgediscovery.StaticEdge → NewNoResolve，完全跳过内置 SRV/DoT
// 发现；IP 字面量经 net.ResolveTCPAddr 解析，不需要任何 DNS）传给子进程。
// SRV 动态解析失败时退回内置名单（2026-09-25 经 1.1.1.1 与 dns.google 双 DoH 源核对，
// 见下方注释）；两者都失败才退回不带 --edge 的旧路径（交还 cloudflared 自主发现）。
package tunnel

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"io.nexport.gateway/core/logsink"
)

const (
	// edgeSRVName v2 隧道边缘发现 SRV 记录（cloudflared allregions/discovery.go：
	// srvService="v2-origintunneld" + srvProto="tcp" + srvName="argotunnel.com"）。
	edgeSRVName = "_v2-origintunneld._tcp.argotunnel.com"

	// edgePerQueryTimeout 单解析器查询超时。解析器池并发查询、取最先成功者，
	// 整体最坏耗时即该值，不随池大小增长。
	edgePerQueryTimeout = 2500 * time.Millisecond

	// edgeResolveOverallTimeout 一次完整边缘解析（SRV+两级 A）的预算上限。
	edgeResolveOverallTimeout = 4 * edgePerQueryTimeout
)

// publicDNSResolvers 公网 UDP 解析器池。国内两列放前面（AliDNS/DNSPod 无干扰、
// 延迟低），Cloudflare/Google 兜底海外网络。全部 IP 直连，不依赖本机 resolver。
var publicDNSResolvers = []string{
	"223.5.5.5:53",    // AliDNS
	"119.29.29.29:53", // DNSPod
	"1.1.1.1:53",      // Cloudflare
	"8.8.8.8:53",      // Google
}

// fallbackEdgeAddrs 内置回退边缘地址（ip:port）。
//
// 取自 2026-09-25 对 region1.v2.argotunnel.com / region2.v2.argotunnel.com 的 A 记录
// （SRV _v2-origintunneld._tcp.argotunnel.com 的两个目标，端口均 7844），经
// https://1.1.1.1/dns-query 与 https://dns.google/resolve 双 DoH 源核对一致；
// region1/region2 各取 3 个。任播地址组多年稳定，仅作动态解析失败时的兜底。
var fallbackEdgeAddrs = []string{
	"198.41.192.7:7844",   // region1
	"198.41.192.47:7844",  // region1
	"198.41.192.107:7844", // region1
	"198.41.200.13:7844",  // region2
	"198.41.200.53:7844",  // region2
	"198.41.200.113:7844", // region2
}

const (
	maxAddrsPerRegion = 3 // 每区域最多携带的边缘地址数（连接池按需取用，无需全部）
	regionsExpected   = 2 // SRV 预期返回 region1/region2 两个目标
)

// edgeArgs 预解析边缘地址并生成 cloudflared 的 --edge 参数（成对展开，重复 flag
// 在 urfave/cli 的 StringSliceFlag 下逐个追加，语义稳定）。
// 返回 nil 表示解析彻底失败（动态 + 内置均无果），调用方退回不带 --edge 的旧路径。
func edgeArgs() []string {
	addrs, source := resolveEdgeAddrs()
	if len(addrs) == 0 {
		logsink.Warnf("[tunnel] 边缘地址解析失败（SRV 与内置回退均无果），退回 cloudflared 自主发现")
		return nil
	}
	logsink.Printf("[tunnel] 边缘地址（source=%s）: %s", source, strings.Join(addrs, " "))
	return edgeArgPairs(addrs)
}

// edgeArgPairs 把地址列表展开成 --edge ip:port 重复 flag 对。
func edgeArgPairs(addrs []string) []string {
	args := make([]string, 0, 2*len(addrs))
	for _, a := range addrs {
		args = append(args, "--edge", a)
	}
	return args
}

// resolveEdgeAddrs SRV → A 两级解析；失败时回退内置名单。
// 返回 (地址列表, 来源)，来源 "srv"（动态）或 "fallback"（内置）。
func resolveEdgeAddrs() ([]string, string) {
	fallback := func() []string {
		cp := make([]string, len(fallbackEdgeAddrs))
		copy(cp, fallbackEdgeAddrs)
		return cp
	}

	ctx, cancel := context.WithTimeout(context.Background(), edgeResolveOverallTimeout)
	defer cancel()

	srvs, err := srvLookupAll(ctx, edgeSRVName)
	if err != nil || len(srvs) == 0 {
		logsink.Warnf("[tunnel] SRV 解析失败（%v），使用内置回退边缘地址", err)
		return fallback(), "fallback"
	}
	// RFC 2782：按 priority 升序处理（region1=1，region2=2）
	sort.Slice(srvs, func(i, j int) bool { return srvs[i].Priority < srvs[j].Priority })

	var addrs []string
	for i, srv := range srvs {
		if len(addrs) >= maxAddrsPerRegion*regionsExpected {
			break
		}
		ips, err := aLookupAll(ctx, strings.TrimSuffix(srv.Target.String(), "."))
		if err != nil || len(ips) == 0 {
			logsink.Warnf("[tunnel] SRV 目标 %s 的 A 解析失败: %v", srv.Target.String(), err)
			continue
		}
		// 同一 SRV 目标的 A 记录是同区域任播组，取前几个已足够冗余
		n := maxAddrsPerRegion
		if i >= regionsExpected { // 超出预期区域数（罕见），少取控制列表长度
			n = 1
		}
		for _, ip := range ips {
			if n == 0 {
				break
			}
			v4 := ip.To4()
			if v4 == nil {
				continue // 边缘地址走 IPv4 字面量（v6 依赖运营商，不带上）
			}
			addrs = append(addrs, net.JoinHostPort(v4.String(), strconv.Itoa(int(srv.Port))))
			n--
		}
	}
	if len(addrs) == 0 {
		return fallback(), "fallback"
	}
	return addrs, "srv"
}

// srvLookupAll 向解析器池并发发起 SRV 查询，返回最先成功者的全部记录。
func srvLookupAll(ctx context.Context, name string) ([]dnsmessage.SRVResource, error) {
	type result struct {
		recs []dnsmessage.SRVResource
		err  error
	}
	resCh := make(chan result, len(publicDNSResolvers))
	for _, server := range publicDNSResolvers {
		go func(server string) {
			msg, err := dnsExchange(ctx, server, dnsmessage.TypeSRV, name)
			if err == nil {
				var recs []dnsmessage.SRVResource
				recs, err = parseSRV(msg)
				resCh <- result{recs: recs, err: wrapServer(server, err)}
				return
			}
			resCh <- result{err: wrapServer(server, err)}
		}(server)
	}
	var lastErr error
	for range publicDNSResolvers {
		select {
		case <-ctx.Done():
			if lastErr == nil {
				lastErr = fmt.Errorf("SRV 查询超时: %w", ctx.Err())
			}
			return nil, lastErr
		case r := <-resCh:
			if r.err == nil && len(r.recs) > 0 {
				return r.recs, nil
			}
			if r.err != nil {
				lastErr = r.err
			}
		}
	}
	if lastErr == nil {
		lastErr = errors.New("所有解析器均未返回 SRV 记录")
	}
	return nil, lastErr
}

// aLookupAll 向解析器池并发发起 A 查询，返回最先成功者的全部 IPv4。
func aLookupAll(ctx context.Context, name string) ([]net.IP, error) {
	type result struct {
		ips []net.IP
		err error
	}
	resCh := make(chan result, len(publicDNSResolvers))
	for _, server := range publicDNSResolvers {
		go func(server string) {
			msg, err := dnsExchange(ctx, server, dnsmessage.TypeA, name)
			if err == nil {
				var ips []net.IP
				ips, err = parseA(msg)
				resCh <- result{ips: ips, err: wrapServer(server, err)}
				return
			}
			resCh <- result{err: wrapServer(server, err)}
		}(server)
	}
	var lastErr error
	for range publicDNSResolvers {
		select {
		case <-ctx.Done():
			if lastErr == nil {
				lastErr = fmt.Errorf("A 查询超时: %w", ctx.Err())
			}
			return nil, lastErr
		case r := <-resCh:
			if r.err == nil && len(r.ips) > 0 {
				return r.ips, nil
			}
			if r.err != nil {
				lastErr = r.err
			}
		}
	}
	if lastErr == nil {
		lastErr = errors.New("所有解析器均未返回 A 记录")
	}
	return nil, lastErr
}

// wrapServer 给错误标注解析器地址。
func wrapServer(server string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", server, err)
}

// dnsExchange 对单个解析器做一次 UDP DNS 查询，返回原始应答报文。
// 逐次读包直至拿到匹配 ID 的应答（并发环境下偶尔读到别条查询的应答不计为失败）。
func dnsExchange(ctx context.Context, server string, qtype dnsmessage.Type, name string) ([]byte, error) {
	var idBuf [2]byte
	if _, err := rand.Read(idBuf[:]); err != nil {
		return nil, err
	}
	id := binary.BigEndian.Uint16(idBuf[:])

	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: true})
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	if err := builder.Question(dnsmessage.Question{
		// dnsmessage 要求名字以 '.' 结尾（canonical format）；调用方传的是裸域名
		Name:  dnsmessage.MustNewName(strings.TrimSuffix(name, ".") + "."),
		Type:  qtype,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		return nil, err
	}
	query, err := builder.Finish()
	if err != nil {
		return nil, err
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(edgePerQueryTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}

	buf := make([]byte, 4096) // SRV/A 应答远小于一页
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		if n >= 12 && binary.BigEndian.Uint16(buf[0:2]) == id {
			out := make([]byte, n)
			copy(out, buf[:n])
			return out, nil
		}
	}
}

// parseSRV 解析 SRV 应答（只取 Answer 区 SRV 记录；RCode 非 0 视为失败）。
func parseSRV(msg []byte) ([]dnsmessage.SRVResource, error) {
	var p dnsmessage.Parser
	hdr, err := p.Start(msg)
	if err != nil {
		return nil, err
	}
	if hdr.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("RCode=%d", hdr.RCode)
	}
	if err := p.SkipAllQuestions(); err != nil {
		return nil, err
	}
	var recs []dnsmessage.SRVResource
	for {
		ah, err := p.AnswerHeader()
		if err == dnsmessage.ErrSectionDone {
			break
		}
		if err != nil {
			return nil, err
		}
		if ah.Type != dnsmessage.TypeSRV {
			if err := p.SkipAnswer(); err != nil {
				return nil, err
			}
			continue
		}
		r, err := p.SRVResource()
		if err != nil {
			return nil, err
		}
		recs = append(recs, r)
	}
	return recs, nil
}

// parseA 解析 A 应答（CNAME 链跳过，只收 A 记录）。
func parseA(msg []byte) ([]net.IP, error) {
	var p dnsmessage.Parser
	hdr, err := p.Start(msg)
	if err != nil {
		return nil, err
	}
	if hdr.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("RCode=%d", hdr.RCode)
	}
	if err := p.SkipAllQuestions(); err != nil {
		return nil, err
	}
	var ips []net.IP
	for {
		ah, err := p.AnswerHeader()
		if err == dnsmessage.ErrSectionDone {
			break
		}
		if err != nil {
			return nil, err
		}
		if ah.Type != dnsmessage.TypeA {
			if err := p.SkipAnswer(); err != nil {
				return nil, err
			}
			continue
		}
		r, err := p.AResource()
		if err != nil {
			return nil, err
		}
		ips = append(ips, net.IP(r.A[:]).To4())
	}
	return ips, nil
}
