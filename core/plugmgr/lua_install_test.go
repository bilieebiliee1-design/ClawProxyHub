//go:build luahost_embed

// lua_install_test.go — lua-dev-skill 的 hello-world 模板打包→安装→启动端到端回归
//（-tags luahost_embed 运行；用真实共享 luahost 子进程 + gRPC 握手，即核心实际加载路径）。
// 示例源唯一落点：app/src/main/assets/lua-dev-skill/examples/hello-world。
package plugmgr

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "io.nexport.gateway/core/sdk/proto/cphv1"
)

// TestInstallZipLuaHelloWorld 按指南打包 zip（manifest.json + main.lua 平铺根目录），
// 经 InstallZip 真实安装并启动子进程，ListModels 经 gRPC 回包。
func TestInstallZipLuaHelloWorld(t *testing.T) {
	src := filepath.Join("..", "..", "app", "src", "main", "assets", "lua-dev-skill", "examples", "hello-world")
	mfRaw, err := os.ReadFile(filepath.Join(src, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	luaRaw, err := os.ReadFile(filepath.Join(src, "main.lua"))
	if err != nil {
		t.Fatalf("read main.lua: %v", err)
	}

	// 按指南同款布局打 zip（根目录平铺）
	zipPath := filepath.Join(t.TempDir(), "hello-world.cphplugin")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	for name, data := range map[string][]byte{"manifest.json": mfRaw, "main.lua": luaRaw} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zf.Close()

	m := NewManager(filepath.Join(t.TempDir(), "plugins"), nil)
	t.Cleanup(m.StopAll)
	name, err := m.InstallZip(context.Background(), zipPath, "", nil)
	if err != nil {
		t.Fatalf("InstallZip: %v", err)
	}
	if name != "hello-world" {
		t.Fatalf("installed name = %q, want hello-world", name)
	}
	inst, ok := m.Get("hello-world")
	if !ok {
		t.Fatalf("plugin not running after install")
	}
	if inst.Manifest.ProtocolVersion != 2 || inst.Manifest.Name != "hello-world" {
		t.Fatalf("handshake manifest = %+v", inst.Manifest)
	}

	// 真实 gRPC 往返：ListModels 回 hello-model
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ml, err := inst.Client().ListModels(ctx, &pb.CredentialBlob{})
	if err != nil {
		t.Fatalf("ListModels rpc: %v", err)
	}
	if len(ml.Models) != 1 || ml.Models[0].Id != "hello-model" {
		t.Fatalf("models = %+v", ml.Models)
	}
}
