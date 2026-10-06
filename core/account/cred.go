// cred.go — 凭据信封构造（网关/刷新/任务/测试四条链路统一入口）+ 实例归属。
package account

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"gorm.io/gorm"

	"io.nexport.gateway/core/model"
	pb "io.nexport.gateway/core/sdk/proto/cphv1"
)

// BuildCred 账号 → 凭据信封：解密 blob + 刷新时间 + 实例 + 出站代理。
// groupID>0 为路由命中的分组（代理优先级：账号 > 该分组）；0 = 仅按账号绑定回退全部分组。
func BuildCred(db *gorm.DB, dataDir string, acct *model.Account, groupID int64) (*pb.CredentialBlob, error) {
	blob, err := DecryptCredential(dataDir, acct.CredentialBlob)
	if err != nil {
		return nil, err
	}
	cred := &pb.CredentialBlob{
		AccountId:  fmt.Sprintf("%d", acct.ID),
		Blob:       blob,
		InstanceId: acct.InstanceID,
	}
	if acct.LastRefreshAt != nil {
		cred.UpdatedAt = acct.LastRefreshAt.Unix()
	}
	cred.Proxy, err = ProxyForAccountIn(db, dataDir, acct.ID, groupID)
	return cred, err
}

// DefaultInstance 单例插件的默认实例（最早创建的一个）；没有则建一个「默认」。
func DefaultInstance(db *gorm.DB, pluginID int64) (*model.Instance, error) {
	var inst model.Instance
	err := db.Where("plugin_id = ?", pluginID).Order("id").First(&inst).Error
	if err == nil {
		return &inst, nil
	}
	inst = model.Instance{PluginID: pluginID, Name: "默认", SettingsJSON: "{}"}
	if err := db.Create(&inst).Error; err != nil {
		return nil, err
	}
	return &inst, nil
}

// ResolveInstance 校验实例归属：instanceID>0 须属于该插件；
// 0 时单例插件（multi=false）落默认实例，多实例插件必须显式指定（不自动建）。
func ResolveInstance(db *gorm.DB, pluginID, instanceID int64, multi bool) (*model.Instance, error) {
	if instanceID <= 0 {
		if multi {
			return nil, fmt.Errorf("该插件支持多实例，请先创建实例并指定")
		}
		return DefaultInstance(db, pluginID)
	}
	var inst model.Instance
	if err := db.First(&inst, instanceID).Error; err != nil || inst.PluginID != pluginID {
		return nil, fmt.Errorf("实例不存在或与插件不一致")
	}
	return &inst, nil
}

// CredentialFingerprint 凭据指纹（sha256 hex 前 12 位，对明文 blob 计算）：审计日志用于
// 关联同一凭据的生命周期事件（建档 / 轮换 / 失效），只反映内容、不还原明文；
// 注意必须传解密后的明文（落盘密文因 AES-GCM nonce 随机，每次封装都不同）。
// 空 blob 返回空串。v1.5.0 随豆包账号消失审计（miscFixes③）引入。
func CredentialFingerprint(blob []byte) string {
	if len(blob) == 0 {
		return ""
	}
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:6])
}
