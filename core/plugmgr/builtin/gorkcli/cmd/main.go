// gorkcli 安卓 gomobile 构建入口（NexPort builtin 适配层，源自上游 plugins-go/gorkcli/cmd/main.go）。
// 偏离两处（登记于 tools/build-plugins.sh 头注，重同步上游时逐项保留）：
//   ① 去上游 //go:build !android 标签——GOOS=android 需有 main 入口；正树 SDK pin
//      v1.5.3-0…0ddc177 下 sdk.Serve 定义于 sdk.go 无平台标签，安卓可编译；
//   ② import 改指正树命名包 io.nexport.gateway/core/plugmgr/builtin/gorkcli
//      （上游模块路径在本仓模块内不可达，正树自包含）。
package main

import (
	"github.com/ShadowSmallBaby/ClawProxyHub/sdk"
	"io.nexport.gateway/core/plugmgr/builtin/gorkcli"
)

var version = "dev"

func main() { sdk.Serve(gorkcli.New(version)) }
