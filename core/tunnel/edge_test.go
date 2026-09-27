// edge_test.go — 边缘地址发现测试：
//   - 线格式解析器（parseSRV/parseA）用 dnsmessage 构造应答做密封单测（不触网）；
//   - resolveEdgeAddrs 在解析器池全部不可达时必须回退内置名单（密封，回环拒绝最快）；
//   - 实网路径：真解析器池上 SRV→A 全链路应返回 7844 端口的公网 v4 地址。
package tunnel

import (
	"net"
	"strconv"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// buildResponse 构造一个带 RD+成功 RCode 的 DNS 应答报文（无问题段）。
func buildResponse(t *testing.T, id uint16, add func(b *dnsmessage.Builder) error) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, Response: true, RecursionAvailable: true})
	if err := b.StartAnswers(); err != nil {
		t.Fatalf("StartAnswers: %v", err)
	}
	if err := add(&b); err != nil {
		t.Fatalf("add answers: %v", err)
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return msg
}

func answerHeader(name string, typ dnsmessage.Type) dnsmessage.ResourceHeader {
	return dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name), Type: typ, Class: dnsmessage.ClassINET, TTL: 60}
}

func TestParseSRVWireMessage(t *testing.T) {
	msg := buildResponse(t, 0x1234, func(b *dnsmessage.Builder) error {
		if err := b.SRVResource(answerHeader("_v2-origintunneld._tcp.argotunnel.com.", dnsmessage.TypeSRV),
			dnsmessage.SRVResource{Priority: 1, Weight: 1, Port: 7844, Target: dnsmessage.MustNewName("region1.v2.argotunnel.com.")}); err != nil {
			return err
		}
		return b.SRVResource(answerHeader("_v2-origintunneld._tcp.argotunnel.com.", dnsmessage.TypeSRV),
			dnsmessage.SRVResource{Priority: 2, Weight: 1, Port: 7844, Target: dnsmessage.MustNewName("region2.v2.argotunnel.com.")})
	})
	recs, err := parseSRV(msg)
	if err != nil {
		t.Fatalf("parseSRV: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d SRV records, want 2", len(recs))
	}
	if recs[0].Priority != 1 || recs[1].Priority != 2 {
		t.Fatalf("priority 顺序错误: %d, %d", recs[0].Priority, recs[1].Priority)
	}
	if recs[0].Target.String() != "region1.v2.argotunnel.com." || recs[0].Port != 7844 {
		t.Fatalf("region1 记录错误: %+v", recs[0])
	}
	if recs[1].Target.String() != "region2.v2.argotunnel.com." || recs[1].Port != 7844 {
		t.Fatalf("region2 记录错误: %+v", recs[1])
	}
}

func TestParseSRVRejectsNXDOMAIN(t *testing.T) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 7, Response: true, RCode: dnsmessage.RCodeNameError})
	msg, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseSRV(msg); err == nil {
		t.Fatal("NXDOMAIN 应报错")
	}
}

func TestParseAWireMessageSkipsCNAME(t *testing.T) {
	msg := buildResponse(t, 0x5678, func(b *dnsmessage.Builder) error {
		if err := b.CNAMEResource(answerHeader("region1.v2.argotunnel.com.", dnsmessage.TypeCNAME),
			dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName("cname.example.com.")}); err != nil {
			return err
		}
		if err := b.AResource(answerHeader("cname.example.com.", dnsmessage.TypeA), dnsmessage.AResource{A: [4]byte{198, 41, 192, 7}}); err != nil {
			return err
		}
		return b.AResource(answerHeader("cname.example.com.", dnsmessage.TypeA), dnsmessage.AResource{A: [4]byte{198, 41, 192, 47}})
	})
	ips, err := parseA(msg)
	if err != nil {
		t.Fatalf("parseA: %v", err)
	}
	if len(ips) != 2 || ips[0].String() != "198.41.192.7" || ips[1].String() != "198.41.192.47" {
		t.Fatalf("A 解析错误: %v", ips)
	}
}

// TestResolveEdgeAddrsFallsBack 密封：解析器池全部不可达（回环拒绝端口）→ 内置回退。
func TestResolveEdgeAddrsFallsBack(t *testing.T) {
	old := publicDNSResolvers
	publicDNSResolvers = []string{"127.0.0.1:1", "127.0.0.1:2"} // 立即 connection refused
	t.Cleanup(func() { publicDNSResolvers = old })

	addrs, source := resolveEdgeAddrs()
	if source != "fallback" {
		t.Fatalf("source = %q, want fallback", source)
	}
	if len(addrs) != len(fallbackEdgeAddrs) {
		t.Fatalf("回退地址数 %d != 内置 %d", len(addrs), len(fallbackEdgeAddrs))
	}
	for _, a := range addrs {
		host, port, err := net.SplitHostPort(a)
		if err != nil {
			t.Fatalf("回退地址格式错误 %q: %v", a, err)
		}
		if port != "7844" || net.ParseIP(host) == nil {
			t.Fatalf("回退地址非法 %q", a)
		}
	}
}

// TestEdgeArgPairs --edge 重复 flag 的成对展开。
func TestEdgeArgPairs(t *testing.T) {
	got := edgeArgPairs([]string{"198.41.192.7:7844", "198.41.200.13:7844"})
	want := []string{"--edge", "198.41.192.7:7844", "--edge", "198.41.200.13:7844"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestResolveEdgeAddrsLive 实网全链路：SRV + A 两级解析应返回 ≥2 个 7844 端口地址。
// 需要外网 UDP/53 可达；断网环境该测试失败属预期（不 skip，如实暴露）。
func TestResolveEdgeAddrsLive(t *testing.T) {
	addrs, source := resolveEdgeAddrs()
	if source != "srv" {
		t.Fatalf("source = %q, want srv（实网应动态解析）", source)
	}
	if len(addrs) < 2 {
		t.Fatalf("仅解析到 %d 个边缘地址, want ≥2", len(addrs))
	}
	for _, a := range addrs {
		host, portS, err := net.SplitHostPort(a)
		if err != nil {
			t.Fatalf("地址格式 %q: %v", a, err)
		}
		port, _ := strconv.Atoi(portS)
		if port != 7844 {
			t.Fatalf("端口 %d != 7844", port)
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsPrivate() {
			t.Fatalf("非公网 IPv4 边缘地址 %q", a)
		}
	}
}
