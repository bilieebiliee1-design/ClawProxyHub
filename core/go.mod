module io.nexport.gateway/core

go 1.27.0

require (
	github.com/ShadowSmallBaby/ClawProxyHub v1.5.3-0.20261010045129-6805f658f819
	github.com/ShadowSmallBaby/ClawProxyHubPlugins v0.0.0-20261015122607-d4090ee18e83
	github.com/golang-migrate/migrate/v4 v4.20.1
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/hashicorp/go-plugin v1.8.0
	github.com/mattn/go-sqlite3 v1.14.52
	github.com/warpdotdev/warp-proto-apis/apis/multi_agent v0.0.0-20260917164411-f5c1878026bc
	golang.org/x/crypto v0.57.0
	golang.org/x/net v0.58.0
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.12
	gorm.io/driver/sqlite v1.6.0
	gorm.io/gorm v1.31.2
)

// SDK 钉死 0ddc177（v1.5.3-era 末代未重构 SDK）：ClawProxyHubPlugins @d4090ee 的
// go.mod 要求 SDK ≥6805f65（其 sdk.Serve 已迁 process.go 且 //go:build !android，
// GOOS=android 构建下 builtin/*/cmd 无入口）——MVS 会静默顶版，replace 压制。
// 29 个插件源实测零使用 v2.0-only SDK 符号，0ddc177 面全覆盖；未来升 SDK v2.0.x
// 须同步换注入模型（见 mobile-port v1.5.1 轮方案）。
replace github.com/ShadowSmallBaby/ClawProxyHub => github.com/ShadowSmallBaby/ClawProxyHub v1.5.3-0.20261004201619-0ddc177bf78f

require (
	github.com/fatih/color v1.13.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/mattn/go-colorable v0.1.12 // indirect
	github.com/mattn/go-isatty v0.0.17 // indirect
	github.com/oklog/run v1.1.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260630182238-925bb5da69e7 // indirect
)
