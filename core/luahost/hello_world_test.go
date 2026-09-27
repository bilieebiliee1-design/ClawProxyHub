// hello_world_test.go — lua-dev-skill 的 hello-world 模板契约回归：模板必须能被
// luahost 真实加载并跑通 login → models → chat 全链路（指南承诺"最小可运行"的守卫）。
// 示例落盘于 app/src/main/assets/lua-dev-skill/examples/hello-world（应用关于页展示
// 与用户导出的唯一源）。
package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	lua "github.com/yuin/gopher-lua"

	pb "io.nexport.gateway/core/sdk/proto/cphv1"
)

func helloWorldDir() string {
	return filepath.Join("..", "..", "app", "src", "main", "assets", "lua-dev-skill", "examples", "hello-world")
}

func newHelloWorld() *luahost {
	dir := helloWorldDir()
	return &luahost{dir: dir, pool: newVMPool(dir, nil)}
}

// TestLoadHelloWorld 沙箱 VM 能加载 main.lua 且 login/models/chat 约定函数齐全
//（本模板故意不定义 handshake/refresh/profile：验证降级路径）。
// 注意 lua.LNil 不是 Go nil：缺失字段用 == lua.LNil 判定（load_test.go 同款）。
func TestLoadHelloWorld(t *testing.T) {
	vm, err := newVM(helloWorldDir(), nil)
	if err != nil {
		t.Fatalf("newVM: %v", err)
	}
	defer vm.L.Close()
	for _, fn := range []string{"login", "models", "chat"} {
		if vm.fn(fn) == lua.LNil {
			t.Errorf("missing convention function: %s", fn)
		}
	}
	for _, absent := range []string{"handshake", "refresh", "profile"} {
		if vm.fn(absent) != lua.LNil {
			t.Errorf("%s should be absent (exercises fallback path)", absent)
		}
	}
}

// TestHelloWorldHandshakeFallback 未定义 handshake() 时回退 manifest.json：
// name/runtime 语义字段齐备，auth_methods 与 protocol_version（回填 2）正确。
func TestHelloWorldHandshakeFallback(t *testing.T) {
	h := newHelloWorld()
	resp, err := h.Handshake(context.Background(), &pb.HandshakeRequest{ProtocolVersion: 2})
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("handshake error: %s", resp.Error.Message)
	}
	mf := resp.Manifest
	if mf == nil || mf.Name != "hello-world" || mf.ProtocolVersion != 2 {
		t.Fatalf("manifest = %+v, want hello-world @ protocol 2", mf)
	}
	if len(mf.AuthMethods) != 1 || mf.AuthMethods[0].Id != "api_key" || len(mf.AuthMethods[0].Fields) != 1 {
		t.Fatalf("auth_methods = %+v, want single api_key with one field", mf.AuthMethods)
	}
	if len(mf.Capabilities) == 0 {
		t.Fatalf("capabilities empty")
	}
}

// TestHelloWorldLoginModelsChat 全链路：login 产出凭据 blob → models 返回目录 →
// chat 用该凭据走事件流；无凭据 chat 回 task_failed 401。
func TestHelloWorldLoginModelsChat(t *testing.T) {
	h := newHelloWorld()

	// login：api_key 非空 → blob（JSON）
	lr, err := h.Login(context.Background(), &pb.LoginRequest{
		MethodId: "api_key",
		Form:     map[string]string{"api_key": "sk-test-123"},
	})
	if err != nil || lr.Error != nil {
		t.Fatalf("login: err=%v resultErr=%+v", err, lr.Error)
	}
	if !strings.Contains(string(lr.Blob), "sk-test-123") || lr.Profile.DisplayName != "hello-user" {
		t.Fatalf("login result = blob:%q profile:%+v", string(lr.Blob), lr.Profile)
	}
	// login：空密钥 → 400
	lrBad, _ := h.Login(context.Background(), &pb.LoginRequest{MethodId: "api_key"})
	if lrBad.Error == nil || lrBad.Error.Code != 400 {
		t.Fatalf("login empty key should 400, got %+v", lrBad.Error)
	}

	// models：一个 hello-model
	ml, err := h.ListModels(context.Background(), nil)
	if err != nil || ml.Error != nil {
		t.Fatalf("models: err=%v resultErr=%+v", err, ml.Error)
	}
	if len(ml.Models) != 1 || ml.Models[0].Id != "hello-model" || ml.Models[0].ContextWindow != 8192 {
		t.Fatalf("models = %+v", ml.Models)
	}

	// chat：带凭据 → message_start / content_delta("hello, hi") / message_finish
	fs := &fakeChatServer{}
	if err := h.Chat(&pb.ChatRequest{
		Model:      "hello-model",
		Source:     "chat_completions",
		Credential: &pb.CredentialBlob{AccountId: "1", Blob: lr.Blob},
		Messages:   []*pb.EnvelopeMessage{{Role: "user", Text: "hi"}},
	}, fs); err != nil {
		t.Fatalf("chat: %v", err)
	}
	var sawStart, sawDelta, sawFinish bool
	for _, ev := range fs.events {
		switch e := ev.Event.(type) {
		case *pb.StreamEvent_MessageStart:
			sawStart = e.MessageStart.Model == "hello-model"
		case *pb.StreamEvent_ContentDelta:
			sawDelta = strings.Contains(e.ContentDelta.Text, "hello, hi")
		case *pb.StreamEvent_MessageFinish:
			sawFinish = e.MessageFinish.FinishReason == "stop" && e.MessageFinish.Usage != nil
		}
	}
	if !sawStart || !sawDelta || !sawFinish {
		t.Fatalf("chat events incomplete: start=%v delta=%v finish=%v (%d events)",
			sawStart, sawDelta, sawFinish, len(fs.events))
	}

	// chat：无凭据 → task_failed 401
	fs2 := &fakeChatServer{}
	if err := h.Chat(&pb.ChatRequest{Model: "hello-model"}, fs2); err != nil {
		t.Fatalf("chat no-cred: %v", err)
	}
	last := fs2.events[len(fs2.events)-1]
	if last.GetTaskFailed() == nil || last.GetTaskFailed().Error.Code != 401 {
		t.Fatalf("expected task_failed 401, got %+v", last.Event)
	}
}
